package herdr

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bowd/livery/internal/version"
)

const (
	ModName        = "livery-launcher"
	LegacyModName  = "agent-launcher"
	DefaultBinding = "cmd-shift-n"
	ManifestFile   = ".livery-mod.json"
)

//go:embed mods/livery-launcher/main.js mods/livery-launcher/mod.json mods/livery-launcher/keys.json mods/livery-launcher/RECIPE.md
var EmbeddedModFS embed.FS

var bundledFiles = []string{"RECIPE.md", "keys.json", "main.js", "mod.json"}

type ModManifest struct {
	Mod          string            `json:"mod"`
	Version      string            `json:"version"`
	BundleSHA256 string            `json:"bundle_sha256"`
	Files        map[string]string `json:"files"`
	InstalledAt  string            `json:"installed_at,omitempty"`
}

// BundleManifest computes the deterministic SHA-256 manifest of the embedded livery-launcher mod.
func BundleManifest() (ModManifest, error) {
	files := make(map[string]string, len(bundledFiles))
	var lines []string
	sorted := append([]string(nil), bundledFiles...)
	sort.Strings(sorted)
	for _, name := range sorted {
		b, err := EmbeddedModFS.ReadFile("mods/" + ModName + "/" + name)
		if err != nil {
			return ModManifest{}, err
		}
		sum := sha256.Sum256(b)
		h := hex.EncodeToString(sum[:])
		files[name] = h
		lines = append(lines, h+"  "+name+"\n")
	}
	bundleSum := sha256.Sum256([]byte(strings.Join(lines, "")))
	return ModManifest{
		Mod:          ModName,
		Version:      version.Version,
		BundleSHA256: hex.EncodeToString(bundleSum[:]),
		Files:        files,
	}, nil
}

// ComputeDirChecksum computes the deterministic SHA-256 bundle checksum of an installed mod directory.
func ComputeDirChecksum(dir string) (string, map[string]string, error) {
	files := make(map[string]string, len(bundledFiles))
	var lines []string
	sorted := append([]string(nil), bundledFiles...)
	sort.Strings(sorted)
	for _, name := range sorted {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", files, err
		}
		sum := sha256.Sum256(b)
		h := hex.EncodeToString(sum[:])
		files[name] = h
		lines = append(lines, h+"  "+name+"\n")
	}
	bundleSum := sha256.Sum256([]byte(strings.Join(lines, "")))
	return hex.EncodeToString(bundleSum[:]), files, nil
}

// ExportModArchive writes a deterministic tar.gz bundle of the embedded mod + returns its SHA-256.
func ExportModArchive(outPath string) (archiveSHA string, manifest ModManifest, err error) {
	manifest, err = BundleManifest()
	if err != nil {
		return "", ModManifest{}, err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", ModManifest{}, err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return "", ModManifest{}, err
	}
	hasher := sha256.New()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	sorted := append([]string(nil), bundledFiles...)
	sort.Strings(sorted)
	for _, name := range sorted {
		b, err := EmbeddedModFS.ReadFile("mods/" + ModName + "/" + name)
		if err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			return "", ModManifest{}, err
		}
		hdr := &tar.Header{
			Name: ModName + "/" + name,
			Mode: 0o644,
			Size: int64(len(b)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			return "", ModManifest{}, err
		}
		if _, err := tw.Write(b); err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			return "", ModManifest{}, err
		}
	}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	mb = append(mb, '\n')
	if err := tw.WriteHeader(&tar.Header{
		Name: ModName + "/" + ManifestFile,
		Mode: 0o644,
		Size: int64(len(mb)),
	}); err != nil {
		tw.Close()
		gz.Close()
		f.Close()
		return "", ModManifest{}, err
	}
	if _, err := tw.Write(mb); err != nil {
		tw.Close()
		gz.Close()
		f.Close()
		return "", ModManifest{}, err
	}
	if err := tw.Close(); err != nil {
		gz.Close()
		f.Close()
		return "", ModManifest{}, err
	}
	if err := gz.Close(); err != nil {
		f.Close()
		return "", ModManifest{}, err
	}
	if err := f.Close(); err != nil {
		return "", ModManifest{}, err
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return "", ModManifest{}, err
	}
	_, _ = hasher.Write(raw)
	return hex.EncodeToString(hasher.Sum(nil)), manifest, nil
}

type InstallOptions struct {
	HerdrDir    string // defaults to ~/.config/herdr-app
	RepoRoot    string // optional: required only when Symlink=true
	Symlink     bool   // symlink to repo source instead of writing embedded files
	CompatAlias bool   // keep ~/.config/herdr-app/mods/agent-launcher -> livery-launcher so agent-folders stays working
	UpdateKeys  bool   // update ~/.config/herdr-app/keys.json binding cmd-shift-n -> mod.open:livery-launcher
}

type ModStatus struct {
	HerdrDir        string   `json:"herdr_dir"`
	HerdrPresent    bool     `json:"herdr_present"`
	ModDir          string   `json:"mod_dir"`
	Installed       bool     `json:"installed"`
	SymlinkTarget   string   `json:"symlink_target,omitempty"`
	CompatAlias     bool     `json:"compat_alias"`
	KeyBound        bool     `json:"key_bound"`
	KeyValue        string   `json:"key_value,omitempty"`
	HerdrAppBin     string   `json:"herdr_app_bin,omitempty"`
	ExpectedSHA256  string   `json:"expected_sha256"`
	InstalledSHA256 string   `json:"installed_sha256,omitempty"`
	UpToDate        bool     `json:"up_to_date"`
	DriftedFiles    []string `json:"drifted_files,omitempty"`
}

func DefaultHerdrDir() string {
	if v := os.Getenv("HERDR_APP_CONFIG"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr-app")
}

func FindHerdrAppBinary() string {
	if p, err := exec.LookPath("herdr-app"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{
		filepath.Join(home, ".local", "bin", "herdr-app"),
		"/opt/homebrew/bin/herdr-app",
		"/usr/local/bin/herdr-app",
	} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Mode().Perm()&0o111 != 0 {
			return c
		}
	}
	return ""
}

func Status(herdrDir string) ModStatus {
	if herdrDir == "" {
		herdrDir = DefaultHerdrDir()
	}
	manifest, _ := BundleManifest()
	st := ModStatus{
		HerdrDir:       herdrDir,
		ModDir:         filepath.Join(herdrDir, "mods", ModName),
		HerdrAppBin:    FindHerdrAppBinary(),
		ExpectedSHA256: manifest.BundleSHA256,
	}
	if info, err := os.Stat(herdrDir); err == nil && info.IsDir() {
		st.HerdrPresent = true
	}
	if lst, err := os.Lstat(st.ModDir); err == nil {
		st.Installed = true
		if lst.Mode()&os.ModeSymlink != 0 {
			if tgt, err := os.Readlink(st.ModDir); err == nil {
				st.SymlinkTarget = tgt
			}
		}
		if instSHA, instFiles, err := ComputeDirChecksum(st.ModDir); err == nil {
			st.InstalledSHA256 = instSHA
			st.UpToDate = instSHA == manifest.BundleSHA256
			if !st.UpToDate {
				for name, want := range manifest.Files {
					if instFiles[name] != want {
						st.DriftedFiles = append(st.DriftedFiles, name)
					}
				}
				sort.Strings(st.DriftedFiles)
			}
		} else {
			for name := range manifest.Files {
				if _, e := os.Stat(filepath.Join(st.ModDir, name)); e != nil {
					st.DriftedFiles = append(st.DriftedFiles, name)
				}
			}
			sort.Strings(st.DriftedFiles)
		}
	}
	aliasPath := filepath.Join(herdrDir, "mods", LegacyModName)
	if lst, err := os.Lstat(aliasPath); err == nil && lst.Mode()&os.ModeSymlink != 0 {
		if tgt, err := os.Readlink(aliasPath); err == nil && (tgt == ModName || tgt == st.ModDir) {
			st.CompatAlias = true
		}
	}
	if b, err := os.ReadFile(filepath.Join(herdrDir, "keys.json")); err == nil {
		var keys map[string]any
		if json.Unmarshal(b, &keys) == nil {
			if g, ok := keys["global"].(map[string]any); ok {
				if v, ok := g[DefaultBinding].(string); ok {
					st.KeyValue = v
					st.KeyBound = v == "mod.open:"+ModName
				}
			}
		}
	}
	return st
}

// InstallMod installs `livery-launcher` into Herdr's mods directory from the embedded assets
// (verifying the bundled SHA-256 checksum and recording `.livery-mod.json`),
// or symlinks to the repo tree when Symlink=true.
func InstallMod(opts InstallOptions) (ModStatus, error) {
	herdrDir := opts.HerdrDir
	if herdrDir == "" {
		herdrDir = DefaultHerdrDir()
	}
	modsDir := filepath.Join(herdrDir, "mods")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		return ModStatus{}, err
	}
	dst := filepath.Join(modsDir, ModName)

	manifest, err := BundleManifest()
	if err != nil {
		return ModStatus{}, err
	}

	if opts.Symlink {
		if opts.RepoRoot == "" {
			return ModStatus{}, fmt.Errorf("--symlink requires a livery repo root")
		}
		src := filepath.Join(opts.RepoRoot, "internal", "herdr", "mods", ModName)
		if _, err := os.Stat(filepath.Join(src, "main.js")); err != nil {
			return ModStatus{}, fmt.Errorf("source mod not found at %s", src)
		}
		_ = os.RemoveAll(dst)
		if err := os.Symlink(src, dst); err != nil {
			return ModStatus{}, err
		}
	} else {
		if lst, err := os.Lstat(dst); err == nil && lst.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(dst)
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return ModStatus{}, err
		}
		entries, err := fs.ReadDir(EmbeddedModFS, "mods/"+ModName)
		if err != nil {
			return ModStatus{}, err
		}
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			b, err := EmbeddedModFS.ReadFile("mods/" + ModName + "/" + ent.Name())
			if err != nil {
				return ModStatus{}, err
			}
			if err := os.WriteFile(filepath.Join(dst, ent.Name()), b, 0o644); err != nil {
				return ModStatus{}, err
			}
		}
		// Verify installed files match bundled SHA-256 and stamp .livery-mod.json
		gotSHA, _, err := ComputeDirChecksum(dst)
		if err != nil {
			return ModStatus{}, err
		}
		if gotSHA != manifest.BundleSHA256 {
			return ModStatus{}, fmt.Errorf("installed mod checksum mismatch: got %s want %s", gotSHA, manifest.BundleSHA256)
		}
		manifest.InstalledAt = time.Now().UTC().Format(time.RFC3339)
		mb, _ := json.MarshalIndent(manifest, "", "  ")
		_ = os.WriteFile(filepath.Join(dst, ManifestFile), append(mb, '\n'), 0o644)
	}

	if opts.CompatAlias {
		aliasPath := filepath.Join(modsDir, LegacyModName)
		if lst, err := os.Lstat(aliasPath); err == nil {
			if lst.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(aliasPath)
				_ = os.Symlink(ModName, aliasPath)
			}
		} else {
			_ = os.Symlink(ModName, aliasPath)
		}
	}

	if opts.UpdateKeys {
		if err := updateHerdrKeys(filepath.Join(herdrDir, "keys.json"), "mod.open:"+ModName); err != nil {
			return ModStatus{}, err
		}
	}

	return Status(herdrDir), nil
}

func updateHerdrKeys(keysPath, action string) error {
	doc := map[string]any{}
	if b, err := os.ReadFile(keysPath); err == nil {
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("parse %s: %w", keysPath, err)
		}
		bakPath := keysPath + ".livery.bak"
		if _, err := os.Stat(bakPath); os.IsNotExist(err) {
			_ = os.WriteFile(bakPath, b, 0o644)
		}
	}
	glob, ok := doc["global"].(map[string]any)
	if !ok {
		glob = map[string]any{}
		doc["global"] = glob
	}
	if cur, _ := glob[DefaultBinding].(string); cur == action {
		return nil
	}
	glob[DefaultBinding] = action
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(keysPath, append(out, '\n'), 0o644)
}

func UninstallMod(herdrDir string) error {
	if herdrDir == "" {
		herdrDir = DefaultHerdrDir()
	}
	modsDir := filepath.Join(herdrDir, "mods")
	dst := filepath.Join(modsDir, ModName)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	aliasPath := filepath.Join(modsDir, LegacyModName)
	if lst, err := os.Lstat(aliasPath); err == nil && lst.Mode()&os.ModeSymlink != 0 {
		if tgt, err := os.Readlink(aliasPath); err == nil && (tgt == ModName || tgt == dst) {
			_ = os.Remove(aliasPath)
		}
	}
	return nil
}

// CheckMod runs `herdr-app check <modDir>` to validate syntax and API usage.
func CheckMod(modDir string) (string, error) {
	bin := FindHerdrAppBinary()
	if bin == "" {
		return "", fmt.Errorf("herdr-app binary not found on PATH or ~/.local/bin")
	}
	if modDir == "" {
		modDir = filepath.Join(DefaultHerdrDir(), "mods", ModName)
	}
	cmd := exec.Command(bin, "check", modDir)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
