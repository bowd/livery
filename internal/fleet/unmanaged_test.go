package fleet

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/skillshare"
)

func TestSanitizeMCPServerStripsSecretsAndKeepsVarRefs(t *testing.T) {
	raw := map[string]any{
		"command": "/Users/alice/.local/bin/demo",
		"args":    []any{"mcp"},
		"env": map[string]any{
			"API_TOKEN":   "super-secret-value-never-expose",
			"PASSTHROUGH": "${MY_ENV_VAR}",
			"EMPTY":       "",
		},
		"headers": map[string]any{
			"Authorization": "Bearer secret-header",
		},
	}
	clean, keys := sanitizeMCPServer(raw)
	wantKeys := []string{"API_TOKEN", "Authorization"}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("envKeys = %v, want %v", keys, wantKeys)
	}
	envMap, _ := clean["env"].(map[string]any)
	if _, leaked := envMap["API_TOKEN"]; leaked {
		t.Fatalf("API_TOKEN leaked into sanitized server map: %+v", clean)
	}
	if envMap["PASSTHROUGH"] != "${MY_ENV_VAR}" {
		t.Fatalf("expected ${MY_ENV_VAR} passthrough preserved, got %+v", envMap)
	}
	if _, hasHdr := clean["headers"]; hasHdr {
		t.Fatalf("literal Authorization header should be stripped: %+v", clean)
	}
}

func copyDemoRepoForFleetTest(t *testing.T) string {
	t.Helper()
	srcRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "demo-repo"))
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	_ = filepath.WalkDir(srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(srcRoot, p)
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	return dst
}

func TestInspectAndImportHostUnmanaged(t *testing.T) {
	repoRoot := copyDemoRepoForFleetTest(t)
	if err := skillshare.EnsureCatalogInitialized(repoRoot); err != nil {
		t.Fatal(err)
	}

	fakeHome := t.TempDir()
	// Create two native unmanaged skills in fakeHome/.claude/skills:
	// 1) portable-skill (normal SKILL.md -> default import_to_catalog)
	// 2) hyp-mesh (per-harness hyphae:managed -> default mark_external)
	portableDir := filepath.Join(fakeHome, ".claude", "skills", "portable-skill")
	if err := os.MkdirAll(portableDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(portableDir, "SKILL.md"), []byte("---\nname: portable-skill\ndescription: demo\n---\nhello\n"), 0o644)

	hypDir := filepath.Join(fakeHome, ".claude", "skills", "hyp-mesh")
	if err := os.MkdirAll(hypDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(hypDir, "SKILL.md"), []byte("---\nname: hyp-mesh\n---\n<!-- hyphae:managed profile=claude -->\n"), 0o644)

	repo, err := model.LoadRepo(repoRoot)
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}
	tgt, err := repo.Target("dev@macbook")
	if err != nil {
		t.Fatal(err)
	}
	tgt.Home = fakeHome

	rep := InspectHostUnmanaged(repo, tgt)
	if !rep.OK {
		t.Fatalf("InspectHostUnmanaged failed: %s", rep.Error)
	}

	byID := map[string]UnmanagedItem{}
	for _, it := range rep.Items {
		byID[it.ID] = it
	}
	if it, ok := byID["skills:portable-skill"]; !ok || it.DefaultAct != "import_to_catalog" {
		t.Fatalf("expected skills:portable-skill with default import_to_catalog, got %+v", it)
	}
	if it, ok := byID["skills:hyp-mesh"]; !ok || !it.HarnessSpecific || it.DefaultAct != "mark_external" {
		t.Fatalf("expected skills:hyp-mesh with HarnessSpecific & mark_external, got %+v", it)
	}

	// Apply import_to_catalog on portable-skill + mark_external on hyp-mesh
	app := ImportHostUnmanaged(repo, tgt, []UnmanagedDecision{
		{ID: "skills:portable-skill", Action: "import_to_catalog"},
		{ID: "skills:hyp-mesh", Action: "mark_external"},
	})
	if !app.OK || app.Error != "" {
		t.Fatalf("ImportHostUnmanaged failed: %+v", app)
	}

	// Verify portable-skill copied into Catalog & unmanaged directory moved to backup
	if _, err := os.Stat(filepath.Join(skillshare.CatalogSkillsDir(repoRoot), "portable-skill", "SKILL.md")); err != nil {
		t.Fatalf("expected portable-skill in Catalog: %v", err)
	}
	if _, err := os.Stat(portableDir); !os.IsNotExist(err) {
		t.Fatalf("expected unmanaged portable-skill directory moved to backup, still exists at %s", portableDir)
	}

	// Verify repo still passes CheckRepo and has portable-skill enabled + hyp-mesh external
	if errs := model.CheckRepo(repoRoot); len(errs) > 0 {
		t.Fatalf("CheckRepo failed after import: %v", errs)
	}
	updated, err := model.LoadRepo(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	ut, _ := updated.Target("dev@macbook")
	skills, _ := updated.ResolveHostCategoryItems(ut, "skills")
	if !strings.Contains(strings.Join(skills, ","), "portable-skill") {
		t.Fatalf("expected portable-skill enabled on dev@macbook, got %v", skills)
	}
	ownExt, _ := model.TargetOwnExternal(repoRoot, "dev@macbook")
	if !strings.Contains(strings.Join(ownExt, ","), "hyp-mesh") {
		t.Fatalf("expected hyp-mesh in target external, got %v", ownExt)
	}
}

func TestExtractHookCommandsAcrossCursorPiAndClaudeFormats(t *testing.T) {
	home := "/home/alice"
	cursorEntry := map[string]any{
		"bindings": map[string]any{
			"cursor": map[string]any{
				"events": map[string]any{
					"preToolUse": []any{
						map[string]any{"command": "/home/alice/.local/share/tmux-mesh/hooks/cursor-hook.sh"},
					},
				},
			},
		},
	}
	gotCursor := extractHookCommands(cursorEntry, home)
	if len(gotCursor) != 1 || gotCursor[0] != "~/.local/share/tmux-mesh/hooks/cursor-hook.sh" {
		t.Fatalf("unexpected cursor hook commands: %v", gotCursor)
	}

	piEntry := map[string]any{
		"bindings": map[string]any{
			"pi": map[string]any{
				"code": "// Managed by tmux-mesh\nexport default function () {}\n",
			},
		},
	}
	gotPi := extractHookCommands(piEntry, home)
	if len(gotPi) != 1 || gotPi[0] != "[pi code] // Managed by tmux-mesh" {
		t.Fatalf("unexpected pi code hook summary: %v", gotPi)
	}

	claudeEntry := map[string]any{
		"bindings": map[string]any{
			"claude": map[string]any{
				"events": map[string]any{
					"Stop": []any{
						map[string]any{
							"hooks": []any{
								map[string]any{"command": "/home/alice/.local/bin/hyp hook stop"},
							},
						},
					},
				},
			},
		},
	}
	gotClaude := extractHookCommands(claudeEntry, home)
	if len(gotClaude) != 1 || gotClaude[0] != "~/.local/bin/hyp hook stop" {
		t.Fatalf("unexpected claude hook commands: %v", gotClaude)
	}

	// Verify canonical naming turns argv[0] names like `hyp-2` into `<target>-<event>` while preserving code hook names
	geminiHook := probedHook{
		Name: "hyp-2",
		Entry: map[string]any{
			"bindings": map[string]any{
				"gemini": map[string]any{
					"events": map[string]any{
						"AfterTool": []any{map[string]any{"command": "/usr/local/bin/hyp hook gemini post-tool-use"}},
					},
				},
			},
		},
	}
	if got := canonicalizeImportedHookName("gemini", geminiHook); got != "gemini-aftertool" {
		t.Fatalf("canonicalizeImportedHookName(gemini) = %q, want gemini-aftertool", got)
	}
	piHook := probedHook{Name: "tmux-mesh-activity", Entry: piEntry}
	if got := canonicalizeImportedHookName("pi", piHook); got != "tmux-mesh-activity" {
		t.Fatalf("canonicalizeImportedHookName(pi) = %q, want tmux-mesh-activity", got)
	}
}
