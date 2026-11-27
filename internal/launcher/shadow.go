package launcher

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bowd/livery/internal/secrets"
)

const ShimMarker = "# Managed by agent-config launcher"

var ephemeralShadowSuffixes = []string{"-wal", "-shm", "-journal", ".lock", ".tmp"}

func isEphemeralShadowEntry(name string) bool {
	if name == ".DS_Store" {
		return true
	}
	for _, suf := range ephemeralShadowSuffixes {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func ensureSymlink(linkPath, targetPath string, replaceable bool) error {
	if lst, err := os.Lstat(linkPath); err == nil {
		if lst.Mode()&os.ModeSymlink != 0 {
			existing, err := os.Readlink(linkPath)
			if err == nil {
				if !filepath.IsAbs(existing) {
					existing = filepath.Join(filepath.Dir(linkPath), existing)
				}
				absEx, _ := filepath.Abs(existing)
				absT, _ := filepath.Abs(targetPath)
				if absEx == absT {
					return nil
				}
			}
			_ = os.Remove(linkPath)
		} else if replaceable {
			if err := os.RemoveAll(linkPath); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("shadow home conflict: %s already exists and is not a symlink (expected -> %s)", linkPath, targetPath)
		}
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return err
	}
	return os.Symlink(targetPath, linkPath)
}

func assertNotSymlink(path, label string) error {
	if lst, err := os.Lstat(path); err == nil && lst.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to launch: private entry %q at %s must not be a symlink", label, path)
	}
	return nil
}

type ShadowOpts struct {
	KnownSharedDirs        []string
	PrivateNames           []string
	SharedNames            []string
	ShareAllOther          bool
	ReplaceableDirs        []string
	RemoveStalePrivateLink []string
	VerifyPrivateNotLink   []string
	GroupRoot              string
	GroupEntries           []string
	KnownGroupDirs         []string
	KnownGroupFiles        []string
}

func MaterializeShadowHome(sharedHome, shadowHome string, opts ShadowOpts) (map[string]any, error) {
	sharedAbs, err := filepath.Abs(secrets.ExpandPath(sharedHome))
	if err != nil {
		return nil, err
	}
	shadowAbs, err := filepath.Abs(secrets.ExpandPath(shadowHome))
	if err != nil {
		return nil, err
	}
	if sharedAbs == shadowAbs {
		return nil, fmt.Errorf("shadow home %q must differ from shared_home %q", shadowAbs, sharedAbs)
	}
	if err := os.MkdirAll(sharedAbs, 0o755); err != nil {
		return nil, err
	}
	if err := ensurePrivateDir(shadowAbs); err != nil {
		return nil, err
	}

	privSet := map[string]bool{}
	for _, n := range opts.PrivateNames {
		privSet[n] = true
	}
	replSet := map[string]bool{}
	for _, n := range opts.ReplaceableDirs {
		replSet[n] = true
	}

	for _, d := range opts.KnownSharedDirs {
		if !privSet[d] {
			_ = os.MkdirAll(filepath.Join(sharedAbs, d), 0o755)
		}
	}
	for _, name := range opts.RemoveStalePrivateLink {
		p := filepath.Join(shadowAbs, name)
		if lst, err := os.Lstat(p); err == nil && lst.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(p)
		}
	}

	// Prune dangling or ephemeral symlinks pointing into sharedAbs
	sharedPrefix := strings.TrimRight(sharedAbs, string(filepath.Separator)) + string(filepath.Separator)
	if entries, err := os.ReadDir(shadowAbs); err == nil {
		for _, e := range entries {
			ep := filepath.Join(shadowAbs, e.Name())
			lst, err := os.Lstat(ep)
			if err != nil || lst.Mode()&os.ModeSymlink == 0 {
				continue
			}
			tgt, err := os.Readlink(ep)
			if err != nil {
				continue
			}
			if !filepath.IsAbs(tgt) {
				tgt = filepath.Join(shadowAbs, tgt)
			}
			absT, _ := filepath.Abs(tgt)
			if strings.HasPrefix(absT, sharedPrefix) {
				if isEphemeralShadowEntry(e.Name()) {
					_ = os.Remove(ep)
				} else if _, statErr := os.Stat(ep); statErr != nil {
					_ = os.Remove(ep)
				}
			}
		}
	}

	for _, name := range opts.VerifyPrivateNotLink {
		if err := assertNotSymlink(filepath.Join(shadowAbs, name), name); err != nil {
			return nil, err
		}
	}

	var toLink []string
	seen := map[string]bool{}
	for _, name := range opts.KnownSharedDirs {
		if !privSet[name] && !seen[name] {
			toLink = append(toLink, name)
			seen[name] = true
		}
	}
	for _, name := range opts.SharedNames {
		if privSet[name] || seen[name] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(sharedAbs, name)); err == nil {
			toLink = append(toLink, name)
			seen[name] = true
		}
	}
	if opts.ShareAllOther {
		if entries, err := os.ReadDir(sharedAbs); err == nil {
			for _, e := range entries {
				name := e.Name()
				if privSet[name] || seen[name] || isEphemeralShadowEntry(name) {
					continue
				}
				toLink = append(toLink, name)
				seen[name] = true
			}
		}
	}

	for _, name := range toLink {
		if err := ensureSymlink(filepath.Join(shadowAbs, name), filepath.Join(sharedAbs, name), replSet[name]); err != nil {
			return nil, err
		}
	}

	var groupLinked []string
	if opts.GroupRoot != "" {
		groupAbs, err := filepath.Abs(secrets.ExpandPath(opts.GroupRoot))
		if err != nil {
			return nil, err
		}
		if err := ensurePrivateDir(groupAbs); err != nil {
			return nil, err
		}
		for _, d := range opts.KnownGroupDirs {
			if !privSet[d] {
				_ = os.MkdirAll(filepath.Join(groupAbs, d), 0o700)
			}
		}
		for _, f := range opts.KnownGroupFiles {
			if !privSet[f] {
				fp := filepath.Join(groupAbs, f)
				if _, err := os.Lstat(fp); os.IsNotExist(err) {
					_ = os.WriteFile(fp, nil, 0o600)
				}
			}
		}
		gSeen := map[string]bool{}
		for _, name := range opts.GroupEntries {
			if privSet[name] || gSeen[name] {
				continue
			}
			gSeen[name] = true
			if _, err := os.Lstat(filepath.Join(groupAbs, name)); err == nil {
				if err := ensureSymlink(filepath.Join(shadowAbs, name), filepath.Join(groupAbs, name), replSet[name]); err != nil {
					return nil, err
				}
				groupLinked = append(groupLinked, name)
			}
		}
		sort.Strings(groupLinked)
	}

	sort.Strings(toLink)
	privSorted := make([]string, 0, len(privSet))
	for k := range privSet {
		privSorted = append(privSorted, k)
	}
	sort.Strings(privSorted)
	res := map[string]any{
		"shared_home": sharedAbs,
		"shadow_home": shadowAbs,
		"linked":      toLink,
		"private":     privSorted,
	}
	if opts.GroupRoot != "" {
		res["group_root"] = secrets.ExpandPath(opts.GroupRoot)
		res["group_linked"] = groupLinked
	}
	return res, nil
}

func writePrivateFileIfChanged(path string, content []byte, perm os.FileMode) error {
	if lst, err := os.Lstat(path); err == nil && lst.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(path)
	}
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, content) {
		_ = os.Chmod(path, perm)
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".livery-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func isOurBinaryOrShim(path string) bool {
	realP, err := filepath.EvalSymlinks(path)
	if err == nil {
		if selfExe, err2 := os.Executable(); err2 == nil {
			if realSelf, err3 := filepath.EvalSymlinks(selfExe); err3 == nil && realP == realSelf {
				return true
			}
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	return bytes.Contains(buf[:n], []byte(ShimMarker))
}

func isExecutableFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// ResolveBinary locates a harness executable on PATH (skipping livery/agent-config shims to prevent loops).
func ResolveBinary(binary string, callerEnv map[string]string) (string, error) {
	if strings.Contains(binary, "/") || strings.HasPrefix(binary, "~") {
		exp := secrets.ExpandPath(binary)
		if !isExecutableFile(exp) {
			return "", fmt.Errorf("configured binary %q (%s) is not executable", binary, exp)
		}
		return exp, nil
	}
	pathStr := os.Getenv("PATH")
	if callerEnv != nil {
		if v, ok := callerEnv["PATH"]; ok {
			pathStr = v
		}
	}
	var searchDirs []string
	seen := map[string]bool{}
	addDir := func(d string) {
		if d == "" {
			return
		}
		abs, _ := filepath.Abs(secrets.ExpandPath(d))
		if !seen[abs] {
			seen[abs] = true
			searchDirs = append(searchDirs, abs)
		}
	}
	for _, entry := range filepath.SplitList(pathStr) {
		addDir(entry)
	}
	home, _ := os.UserHomeDir()
	for _, fb := range []string{
		BinDir(),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".opencode", "bin"),
		filepath.Join(home, "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	} {
		addDir(fb)
	}
	for _, d := range searchDirs {
		cand := filepath.Join(d, binary)
		if isExecutableFile(cand) && !isOurBinaryOrShim(cand) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("could not find %q on PATH (excluding livery shims)", binary)
}

func TryResolveBinary(binary string, callerEnv map[string]string) string {
	if p, err := ResolveBinary(binary, callerEnv); err == nil {
		return p
	}
	return binary
}
