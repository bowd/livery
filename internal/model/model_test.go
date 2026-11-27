package model

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func demoRepoRoot(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "testdata", "demo-repo"))
	if err != nil {
		t.Fatalf("demoRepoRoot: %v", err)
	}
	return p
}

func copyRepoFixture(t *testing.T) string {
	t.Helper()
	srcRoot := demoRepoRoot(t)
	dst := t.TempDir()
	for _, entry := range []string{"inventory.toml", "catalog", "profiles", "prompts", "skills", "mcp", "hooks", "launchers", "projects", "provenance"} {
		src := filepath.Join(srcRoot, entry)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyAny(src, filepath.Join(dst, entry)); err != nil {
			t.Fatalf("copy %s: %v", entry, err)
		}
	}
	return dst
}

func copyAny(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "__pycache__") {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(src, p)
		target := dst
		if rel != "." {
			target = filepath.Join(dst, rel)
		}
		lst, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if lst.Mode()&os.ModeSymlink != 0 {
			lt, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			return os.Symlink(lt, target)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		return os.WriteFile(target, b, lst.Mode().Perm())
	})
}

func TestCheckRepoAndDeriveProfileName(t *testing.T) {
	root := demoRepoRoot(t)
	if errs := CheckRepo(root); len(errs) > 0 {
		t.Fatalf("CheckRepo failed: %v", errs)
	}
	repo, err := LoadRepo(root)
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}
	if len(repo.Targets) == 0 {
		t.Fatalf("expected targets")
	}
	lock, err := LoadLock(root)
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}
	if mac := lock["dev@macbook"]; mac == nil || len(mac.Items) == 0 {
		t.Fatalf("expected dev@macbook lock entries from provenance/lock.toml, got %+v", mac)
	}
	if got := DeriveProfileName("claude-gateway", "claude", ""); got != "gateway" {
		t.Fatalf("DeriveProfileName = %q, want gateway", got)
	}
	if got := DeriveProfileName("codex-zai", "codex", ""); got != "zai" {
		t.Fatalf("DeriveProfileName = %q, want zai", got)
	}
	if got := DeriveProfileName("custom", "claude", "work"); got != "work" {
		t.Fatalf("DeriveProfileName explicit = %q, want work", got)
	}
}

func TestInventoryCommentPreservingEditAndRollback(t *testing.T) {
	root := copyRepoFixture(t)
	invPath := filepath.Join(root, "inventory.toml")
	origBytes, err := os.ReadFile(invPath)
	if err != nil {
		t.Fatal(err)
	}

	// Valid patch preserves header comments and updates target
	changed, err := UpdateTargetInInventory(root, "ops@server", map[string]any{
		"skillshare_mode":    "merge",
		"skillshare_targets": []string{"claude", "codex", "opencode"},
	})
	if err != nil {
		t.Fatalf("UpdateTargetInInventory: %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("changed = %v", changed)
	}
	after, _ := os.ReadFile(invPath)
	if !strings.Contains(string(after), "# ") {
		t.Fatalf("comments were not preserved")
	}
	repo, err := LoadRepo(root)
	if err != nil {
		t.Fatalf("LoadRepo after edit: %v", err)
	}
	srvTarget, err := repo.Target("ops@server")
	if err != nil {
		t.Fatal(err)
	}
	if len(srvTarget.EffectiveSkillshareTargets()) != 3 {
		t.Fatalf("expected 3 skillshare targets, got %v", srvTarget.EffectiveSkillshareTargets())
	}

	// Invalid profile must be rejected and rolled back cleanly
	beforeInvalid, _ := os.ReadFile(invPath)
	if _, err := UpdateTargetInInventory(root, "ops@server", map[string]any{
		"profiles": []string{"definitely-does-not-exist"},
	}); err == nil {
		t.Fatalf("expected invalid profile error")
	}
	afterInvalid, _ := os.ReadFile(invPath)
	if string(afterInvalid) != string(beforeInvalid) {
		t.Fatalf("inventory.toml was not rolled back after invalid update")
	}

	// Add and remove new environment cleanly
	err = AddTargetToInventory(root, NewTargetSpec{
		ID:                "dev@sandbox",
		SSH:               "dev@sandbox",
		Providers:         []string{"claude", "codex"},
		Profiles:          []string{"base"},
		SkillshareTargets: []string{"claude", "codex"},
	})
	if err != nil {
		t.Fatalf("AddTargetToInventory: %v", err)
	}
	repo2, err := LoadRepo(root)
	if err != nil {
		t.Fatalf("LoadRepo after add: %v", err)
	}
	if _, err := repo2.Target("dev@sandbox"); err != nil {
		t.Fatalf("newly added target missing: %v", err)
	}
	if err := RemoveTargetFromInventory(root, "dev@sandbox"); err != nil {
		t.Fatalf("RemoveTargetFromInventory: %v", err)
	}
	repo3, _ := LoadRepo(root)
	if _, err := repo3.Target("dev@sandbox"); err == nil {
		t.Fatalf("removed target still present")
	}
	_ = origBytes
}

func TestLauncherProfileOnboardingAndGroups(t *testing.T) {
	root := copyRepoFixture(t)
	id, err := SaveLauncherProfile(root, LauncherProfileInput{
		Harness:       "claude",
		Profile:       "research",
		AuthMode:      "gateway",
		Mode:          "shadow",
		SessionGroup:  "work",
		Description:   "Claude (Research)",
		BaseURL:       "https://gw.example.test",
		TokenSecret:   "store:research-key",
		AttachProfile: "workstation",
	})
	if err != nil || id != "claude-research" {
		t.Fatalf("SaveLauncherProfile = %q, %v", id, err)
	}
	repo, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	sp, ok := repo.Launchers["claude-research"]
	if !ok || sp.SessionGroup != "work" || sp.Profile != "research" {
		t.Fatalf("unexpected saved launcher: %+v", sp)
	}
	if !containsStr(repo.Profiles["workstation"].Launchers, "claude-research") {
		t.Fatalf("expected claude-research attached to workstation")
	}

	// Reject literal secret value with atomic rollback
	if _, err := SaveLauncherProfile(root, LauncherProfileInput{
		Harness:     "claude",
		Profile:     "bad",
		Description: "sk-ant-api03-1234567890abcdef1234567890abcdef",
	}); err == nil {
		t.Fatalf("expected literal secret to be rejected")
	}
	if _, err := os.Stat(filepath.Join(root, "launchers", "claude-bad.toml")); !os.IsNotExist(err) {
		t.Fatalf("invalid launcher file was not rolled back")
	}

	if err := UpdateLauncherGroup(root, "claude-research", "personal"); err != nil {
		t.Fatal(err)
	}
	repo2, _ := LoadRepo(root)
	if repo2.Launchers["claude-research"].SessionGroup != "personal" {
		t.Fatalf("expected personal group")
	}
	if err := DeleteLauncherProfile(root, "claude-research"); err != nil {
		t.Fatal(err)
	}
	repo3, _ := LoadRepo(root)
	if containsStr(repo3.Profiles["workstation"].Launchers, "claude-research") {
		t.Fatalf("expected claude-research detached on delete")
	}
}
