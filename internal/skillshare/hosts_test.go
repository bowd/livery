package skillshare

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bowd/livery/internal/model"
)

func TestPrepareAndReconcileSlotConfigRoundTrip(t *testing.T) {
	repoYAML := []byte(`mode: merge
sources:
  skills: "~/.local/share/agent-config/skillshare/skills"
targets:
  claude:
    skills:
      include:
        - t3-operations
  codex:
    skills:
      path: "~/.codex/skills"
      include:
        - t3-operations
`)
	tmp := t.TempDir()
	repoSkills := filepath.Join(tmp, "repo-skills")
	sandbox := filepath.Join(tmp, "sandbox")
	_ = os.MkdirAll(repoSkills, 0o755)
	_ = os.MkdirAll(sandbox, 0o755)

	slotBytes, origPaths, err := PrepareSlotConfig(repoYAML, repoSkills, sandbox)
	if err != nil {
		t.Fatalf("PrepareSlotConfig: %v", err)
	}
	if !strings.Contains(string(slotBytes), repoSkills) {
		t.Fatalf("expected repoSkills in slot config:\n%s", slotBytes)
	}
	if !strings.Contains(string(slotBytes), filepath.Join(sandbox, "targets", "claude")) {
		t.Fatalf("expected sandboxed claude target path:\n%s", slotBytes)
	}
	if origPaths["claude"] != "" || origPaths["codex"] != "~/.codex/skills" {
		t.Fatalf("unexpected origPaths: %#v", origPaths)
	}

	// Simulate skillshare ui adding a new `cursor` target pointing into controller HOME
	mutated := string(slotBytes) + `  cursor:
    skills:
      path: /Users/alice/.cursor/skills
`
	repoOut, updatedSlot, slotChanged, err := ReconcileSlotConfig(
		[]byte(mutated), sandbox, "/Users/alice", "~/.local/share/agent-config/skillshare", origPaths,
	)
	if err != nil {
		t.Fatalf("ReconcileSlotConfig: %v", err)
	}
	outStr := string(repoOut)
	if strings.Contains(outStr, sandbox) {
		t.Fatalf("sandbox path leaked into repo config:\n%s", outStr)
	}
	if !strings.Contains(outStr, "~/.local/share/agent-config/skillshare/skills") {
		t.Fatalf("expected portable sources.skills:\n%s", outStr)
	}
	if !strings.Contains(outStr, "~/.codex/skills") {
		t.Fatalf("expected codex custom path preserved:\n%s", outStr)
	}
	if !strings.Contains(outStr, "~/.cursor/skills") {
		t.Fatalf("expected newly added cursor path collapsed to ~/:\n%s", outStr)
	}
	if !slotChanged || !strings.Contains(string(updatedSlot), filepath.Join(sandbox, "targets", "cursor")) {
		t.Fatalf("expected newly added cursor redirected in slot config (slotChanged=%v):\n%s", slotChanged, updatedSlot)
	}
}

func TestSummarizeHostConfigFiltering(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		d := filepath.Join(root, "skillshare", "skills", name)
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: desc "+name+"\n---\n# "+name+"\n"), 0o644)
	}
	cfgDir := filepath.Join(root, "skillshare", "hosts", "test@host")
	_ = os.MkdirAll(cfgDir, 0o755)
	_ = os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`mode: merge
sources:
  skills: "~/.local/share/agent-config/skillshare/skills"
targets:
  claude:
    skills:
      include:
        - alpha
        - beta
      exclude:
        - beta
  codex:
    skills:
      enabled: false
      path: "~/.codex/skills"
`), 0o644)

	sum := SummarizeHostConfig(root, &model.Target{ID: "test@host"})
	if len(sum.ActiveSkills) != 1 || sum.ActiveSkills[0] != "alpha" {
		t.Fatalf("ActiveSkills = %v, want [alpha]", sum.ActiveSkills)
	}
	if len(sum.EnabledTargets) != 1 || sum.EnabledTargets[0] != "claude" {
		t.Fatalf("EnabledTargets = %v, want [claude]", sum.EnabledTargets)
	}
	if len(sum.Targets) != 2 {
		t.Fatalf("Targets = %v, want [claude codex]", sum.Targets)
	}
}

func TestCompileEnvSkillshareConfigPreservesProjects(t *testing.T) {
	root := t.TempDir()
	if err := EnsureCatalogInitialized(root); err != nil {
		t.Fatal(err)
	}
	tgt := &model.Target{ID: "dev@box", Providers: []string{"claude", "codex"}, SkillshareRoot: "~/.local/share/agent-config/skillshare"}
	ov := LoadEnvOverride(root, tgt)
	ov.Projects = map[string]any{
		"/home/dev/work/demo": map[string]any{
			"name":    "demo",
			"targets": []string{"claude", "codex"},
		},
	}
	if err := SaveEnvOverride(root, tgt.ID, ov); err != nil {
		t.Fatal(err)
	}
	cfgBytes, _, _, err := CompileEnvSkillshareConfig(root, tgt, EnvEnabledItems{}, "/home/dev")
	if err != nil {
		t.Fatalf("CompileEnvSkillshareConfig: %v", err)
	}
	if !strings.Contains(string(cfgBytes), "projects:") || !strings.Contains(string(cfgBytes), "/home/dev/work/demo") {
		t.Fatalf("expected projects section in compiled config.yaml:\n%s", cfgBytes)
	}
}

func TestCatalogAndCompileSkillshareV2Plugins(t *testing.T) {
	root := t.TempDir()
	if err := EnsureCatalogInitialized(root); err != nil {
		t.Fatal(err)
	}
	catCfg := `mode: merge
git_root: root
sources:
  skills:
    path: ./skills
targets:
  claude: {}
plugins:
  version: 2
  packages:
    frontend-design:
      source: https://github.com/anthropics/claude-plugins-official.git
      plugin: frontend-design
      bindings:
        claude:
          source: https://github.com/anthropics/claude-plugins-official.git
          plugin: frontend-design
          sync: true
        cursor:
          source: https://github.com/anthropics/claude-plugins-official.git
          plugin: frontend-design
          sync: true
    commit-commands:
      source: https://github.com/anthropics/claude-plugins-official.git
      plugin: commit-commands
      bindings: {}
    unused-plugin:
      source: https://github.com/example/unused.git
      bindings: {}
`
	if err := os.WriteFile(CatalogConfigPath(root), []byte(catCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cat := LoadCatalog(root)
	if len(cat.Plugins) != 3 {
		t.Fatalf("LoadCatalog plugins = %d, want 3: %+v", len(cat.Plugins), cat.Plugins)
	}
	if cat.Plugins[0].Name != "commit-commands" || cat.Plugins[1].Name != "frontend-design" || cat.Plugins[2].Name != "unused-plugin" {
		t.Fatalf("unexpected plugin names: %+v", cat.Plugins)
	}

	tgt := &model.Target{ID: "dev@box", Providers: []string{"claude", "codex"}, SkillshareRoot: "~/.local/share/agent-config/skillshare"}
	ov := LoadEnvOverride(root, tgt)
	ov.LocalPlugins["host-only"] = map[string]any{
		"bindings": map[string]any{"claude": map[string]any{"imported": true}},
	}
	if err := SaveEnvOverride(root, tgt.ID, ov); err != nil {
		t.Fatal(err)
	}

	cfgBytes, _, _, err := CompileEnvSkillshareConfig(root, tgt, EnvEnabledItems{
		Plugins: []string{"frontend-design", "commit-commands"},
	}, "/home/dev")
	if err != nil {
		t.Fatalf("CompileEnvSkillshareConfig: %v", err)
	}
	out := string(cfgBytes)
	for _, want := range []string{"version: 2", "packages:", "frontend-design:", "commit-commands:", "host-only:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("compiled config missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "unused-plugin:") {
		t.Fatalf("compiled config should not include disabled plugin:\n%s", out)
	}
	if strings.Contains(out, "cursor:") {
		t.Fatalf("compiled config should filter out non-host target binding cursor:\n%s", out)
	}
	// Verify commit-commands (catalog-only empty bindings) got default claude binding
	if !strings.Contains(out, "sync: true") {
		t.Fatalf("expected default claude sync binding:\n%s", out)
	}
}

func TestNormalizeCatalogLibraryConfigAndStripEmptyMCPTargets(t *testing.T) {
	root := t.TempDir()
	if err := EnsureCatalogInitialized(root); err != nil {
		t.Fatal(err)
	}
	catCfg := `mode: merge
git_root: root
targets: {}
mcp:
  servers:
    demo-mcp:
      command: npx
      args: ["-y", "demo-mcp"]
    claude-only-mcp:
      command: npx
      targets: ["claude"]
plugins:
  version: 2
  packages:
    demo-plugin:
      source: https://github.com/anthropics/claude-plugins-official.git
      plugin: demo-plugin
      bindings:
        claude:
          source: https://github.com/anthropics/claude-plugins-official.git
          plugin: demo-plugin
          sync: true
`
	if err := os.WriteFile(CatalogConfigPath(root), []byte(catCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	NormalizeCatalogLibraryConfig(root)

	normBytes, err := os.ReadFile(CatalogConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	normStr := string(normBytes)
	if !strings.Contains(normStr, "targets: []") {
		t.Fatalf("expected NormalizeCatalogLibraryConfig to set targets: [] on demo-mcp:\n%s", normStr)
	}
	if !strings.Contains(normStr, "bindings: {}") {
		t.Fatalf("expected NormalizeCatalogLibraryConfig to set bindings: {} on demo-plugin:\n%s", normStr)
	}

	tgt := &model.Target{ID: "dev@box", Providers: []string{"claude", "codex"}, SkillshareRoot: "~/.local/share/agent-config/skillshare"}
	cfgBytes, _, _, err := CompileEnvSkillshareConfig(root, tgt, EnvEnabledItems{
		MCP:     []string{"demo-mcp", "claude-only-mcp"},
		Plugins: []string{"demo-plugin"},
	}, "/home/dev")
	if err != nil {
		t.Fatalf("CompileEnvSkillshareConfig: %v", err)
	}
	compiled := string(cfgBytes)
	if strings.Contains(compiled, "targets: []") {
		t.Fatalf("expected CompileEnvSkillshareConfig to strip empty targets: [] from host MCP server:\n%s", compiled)
	}
	if !strings.Contains(compiled, "- claude") || !strings.Contains(compiled, "- codex") {
		t.Fatalf("expected host mcp.targets to include claude and codex:\n%s", compiled)
	}
	if !strings.Contains(compiled, "sync: true") {
		t.Fatalf("expected compiled host plugin to bind to claude with sync: true:\n%s", compiled)
	}
}
