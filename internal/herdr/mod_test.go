package herdr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCheckUninstallMod(t *testing.T) {
	herdrDir := t.TempDir()
	// Seed existing keys.json with another binding + old agent-launcher binding
	origKeys := map[string]any{
		"global": map[string]any{
			"cmd-p":       "mod.open:picker",
			"cmd-shift-n": "mod.open:agent-launcher",
		},
	}
	kb, _ := json.MarshalIndent(origKeys, "", "  ")
	if err := os.WriteFile(filepath.Join(herdrDir, "keys.json"), kb, 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := InstallMod(InstallOptions{
		HerdrDir:    herdrDir,
		Symlink:     false,
		CompatAlias: true,
		UpdateKeys:  true,
	})
	if err != nil {
		t.Fatalf("InstallMod: %v", err)
	}
	if !st.Installed || !st.CompatAlias || !st.KeyBound || !st.UpToDate {
		t.Fatalf("unexpected status after install: %+v", st)
	}
	if st.ExpectedSHA256 == "" || st.InstalledSHA256 != st.ExpectedSHA256 {
		t.Fatalf("checksum mismatch: expected=%q installed=%q", st.ExpectedSHA256, st.InstalledSHA256)
	}
	if _, err := os.Stat(filepath.Join(st.ModDir, ManifestFile)); err != nil {
		t.Fatalf("mod manifest %s missing: %v", ManifestFile, err)
	}

	// Verify drift detection when main.js changes on disk
	origJS, _ := os.ReadFile(filepath.Join(st.ModDir, "main.js"))
	_ = os.WriteFile(filepath.Join(st.ModDir, "main.js"), append(origJS, []byte("\n// drift\n")...), 0o644)
	drifted := Status(herdrDir)
	if drifted.UpToDate || len(drifted.DriftedFiles) != 1 || drifted.DriftedFiles[0] != "main.js" {
		t.Fatalf("expected drift on main.js, got up_to_date=%v drifted=%v", drifted.UpToDate, drifted.DriftedFiles)
	}
	// Restore clean copy via reinstall
	if _, err := InstallMod(InstallOptions{HerdrDir: herdrDir, CompatAlias: true, UpdateKeys: true}); err != nil {
		t.Fatalf("reinstall: %v", err)
	}

	// Verify ExportModArchive produces deterministic archive + manifest checksum
	archPath := filepath.Join(t.TempDir(), "livery-launcher-mod.tar.gz")
	archSHA, man, err := ExportModArchive(archPath)
	if err != nil || archSHA == "" || man.BundleSHA256 != st.ExpectedSHA256 {
		t.Fatalf("ExportModArchive: sha=%q man=%+v err=%v", archSHA, man, err)
	}

	// Verify keys.json preserved cmd-p and created backup
	if _, err := os.Stat(filepath.Join(herdrDir, "keys.json.livery.bak")); err != nil {
		t.Fatalf("keys backup missing: %v", err)
	}
	var updated map[string]map[string]string
	raw, _ := os.ReadFile(filepath.Join(herdrDir, "keys.json"))
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatal(err)
	}
	if updated["global"]["cmd-p"] != "mod.open:picker" || updated["global"]["cmd-shift-n"] != "mod.open:livery-launcher" {
		t.Fatalf("unexpected keys.json: %+v", updated)
	}

	// Run real `herdr-app check` on installed embedded copy if binary is available
	if FindHerdrAppBinary() != "" {
		if out, err := CheckMod(st.ModDir); err != nil {
			t.Fatalf("herdr-app check failed: %v\n%s", err, out)
		}
	}

	if err := UninstallMod(herdrDir); err != nil {
		t.Fatalf("UninstallMod: %v", err)
	}
	st2 := Status(herdrDir)
	if st2.Installed || st2.CompatAlias {
		t.Fatalf("expected uninstalled status, got %+v", st2)
	}
}
