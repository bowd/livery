package apps

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/render"
)

func TestHerdrT3AndOpenClawEditors(t *testing.T) {
	tmp := t.TempDir()

	// 1. Herdr programs.json + templates sync using `liv <harness> <profile>`
	progFile := filepath.Join(tmp, "herdr-app", "programs.json")
	tplDir := filepath.Join(tmp, "herdr-core", "templates")
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "demo-repo"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := model.LoadRepo(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	n, err := SyncHerdrFromRepo(repo, progFile, tplDir)
	if err != nil || n == 0 {
		t.Fatalf("SyncHerdrFromRepo: n=%d err=%v", n, err)
	}
	hov := LoadHerdrOverview(progFile, tplDir)
	if !hov.Present || len(hov.Programs) != n {
		t.Fatalf("LoadHerdrOverview = %+v", hov)
	}
	foundLivCmd := false
	for _, p := range hov.Programs {
		if p.Key == "claude-gateway" && strings.Join(p.Run, " ") == "liv claude gateway" {
			foundLivCmd = true
		}
	}
	if !foundLivCmd {
		t.Fatalf("expected claude-gateway to run 'liv claude gateway', got %+v", hov.Programs)
	}

	// 2. T3 Code editor preserves sensitive env values and NEVER leaks them
	t3File := filepath.Join(tmp, "t3", "settings.json")
	_ = os.MkdirAll(filepath.Dir(t3File), 0o755)
	secretVal := "sk-ant-never-leak-t3-secret"
	origT3 := map[string]any{
		"providerInstances": map[string]any{
			"claudeAgent": map[string]any{
				"driver":      "claudeAgent",
				"displayName": "Claude Old",
				"enabled":     true,
				"environment": []map[string]any{
					{"name": "ANTHROPIC_BASE_URL", "value": "https://gw.test", "sensitive": false},
					{"name": "ANTHROPIC_AUTH_TOKEN", "value": secretVal, "sensitive": true},
				},
				"config": map[string]any{"binaryPath": "claude"},
			},
		},
	}
	b, _ := json.MarshalIndent(origT3, "", "  ")
	_ = os.WriteFile(t3File, b, 0o600)

	t3ov := LoadT3Overview(t3File)
	ovJSON, _ := json.Marshal(t3ov)
	if strings.Contains(string(ovJSON), secretVal) {
		t.Fatalf("T3 overview leaked secret value!")
	}

	newName := "Claude Gateway"
	if err := UpsertT3Provider(t3File, T3ProviderPatch{
		InstanceID:   "claudeAgent",
		DisplayName:  &newName,
		CustomModels: []string{"claude-opus-4-6"},
	}); err != nil {
		t.Fatalf("UpsertT3Provider: %v", err)
	}
	rawAfter, _ := os.ReadFile(t3File)
	if !strings.Contains(string(rawAfter), secretVal) || !strings.Contains(string(rawAfter), "Claude Gateway") {
		t.Fatalf("T3 edit should preserve sensitive env entry and update displayName")
	}
	if _, err := os.Stat(t3File + ".livery.bak"); err != nil {
		t.Fatalf("expected .livery.bak backup")
	}

	// 3. OpenClaw profile, plugin, and MCP editing
	ocHome := filepath.Join(tmp, "ochome")
	_ = os.MkdirAll(ocHome, 0o755)
	if _, err := CreateOpenClawProfile(ocHome, "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateOpenClawProfile(ocHome, "work"); err != nil {
		t.Fatal(err)
	}
	if err := SetOpenClawPlugin(ocHome, "work", "hyphae", true); err != nil {
		t.Fatal(err)
	}
	if err := SetOpenClawMCP(ocHome, "work", OpenClawMCPView{Name: "hyphae", Command: "/usr/local/bin/hyp", Args: []string{"mcp"}}); err != nil {
		t.Fatal(err)
	}
	ocOv := LoadOpenClawOverview(ocHome)
	if !ocOv.Present || len(ocOv.Profiles) != 2 {
		t.Fatalf("unexpected OpenClaw overview: %+v", ocOv)
	}
	if err := DeleteOpenClawMCP(ocHome, "work", "hyphae"); err != nil {
		t.Fatal(err)
	}
}

func TestHerdrDefaultLauncherOverrides(t *testing.T) {
	tmp := t.TempDir()
	progFile := filepath.Join(tmp, "herdr-app", "programs.json")
	tplDir := filepath.Join(tmp, "herdr-core", "templates")
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "demo-repo"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := model.LoadRepo(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	repo.Launchers["codex-zai"] = &model.LauncherSpec{
		ID:      "codex-zai",
		Profile: "zai",
		Harness: "codex",
		Raw: map[string]any{
			"id":          "codex-zai",
			"harness":     "codex",
			"description": "Codex ZAI",
			"herdr":       map[string]any{"name": "Codex (ZAI)", "no_daemon": true},
		},
	}
	repo.Profiles["base"].Launchers = append(repo.Profiles["base"].Launchers, "codex-zai")
	for _, tgt := range repo.Targets {
		tgt.SyncHerdrTemplates = true
		tgt.HerdrTemplatesDir = "~/.config/herdr-core/templates"
		tgt.HerdrDefaultLaunchers = map[string]string{
			"claude": "claude-gateway",
			"codex":  "codex-zai",
		}
	}

	// 1. SyncHerdrFromRepo writes default claude/codex program overrides + claude.md/codex.md templates
	n, err := SyncHerdrFromRepo(repo, progFile, tplDir)
	if err != nil || n == 0 {
		t.Fatalf("SyncHerdrFromRepo: n=%d err=%v", n, err)
	}
	hov := LoadHerdrOverview(progFile, tplDir)
	byKey := map[string]HerdrProgram{}
	for _, p := range hov.Programs {
		byKey[p.Key] = p
	}
	if got := strings.Join(byKey["claude"].Run, " "); got != "liv claude gateway" {
		t.Fatalf("expected claude default override 'liv claude gateway', got %q", got)
	}
	if got := strings.Join(byKey["codex"].Run, " "); got != "liv codex zai --no-daemon" {
		t.Fatalf("expected codex default override 'liv codex zai --no-daemon', got %q", got)
	}
	for _, wantTpl := range []struct {
		file string
		cmd  string
	}{
		{"claude.md", "command: liv claude gateway"},
		{"codex.md", "command: liv codex zai --no-daemon"},
	} {
		b, err := os.ReadFile(filepath.Join(tplDir, wantTpl.file))
		if err != nil {
			t.Fatalf("missing template %s: %v", wantTpl.file, err)
		}
		if !strings.Contains(string(b), wantTpl.cmd) {
			t.Fatalf("template %s = %q, want %q", wantTpl.file, string(b), wantTpl.cmd)
		}
	}

	// 2. RenderTarget emits ~/.config/herdr-core/templates/{claude,codex}.md for fleet SSH sync
	rend, err := render.RenderTarget(repo, repo.Targets[0])
	if err != nil {
		t.Fatalf("RenderTarget: %v", err)
	}
	renderedPaths := map[string]string{}
	for _, it := range rend.Items {
		renderedPaths[it.Path] = string(it.Content)
	}
	if !strings.Contains(renderedPaths["~/.config/herdr-core/templates/claude.md"], "command: liv claude gateway") {
		t.Fatalf("expected rendered claude.md template, got %q", renderedPaths["~/.config/herdr-core/templates/claude.md"])
	}
	if !strings.Contains(renderedPaths["~/.config/herdr-core/templates/codex.md"], "command: liv codex zai --no-daemon") {
		t.Fatalf("expected rendered codex.md template, got %q", renderedPaths["~/.config/herdr-core/templates/codex.md"])
	}
}
