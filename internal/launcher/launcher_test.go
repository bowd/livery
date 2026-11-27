package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bowd/livery/internal/secrets"
)

func makeFakeBin(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveHarnessProfile(t *testing.T) {
	profiles := map[string]map[string]any{
		"claude-gateway":  {"id": "claude-gateway", "harness": "claude", "profile": "gateway"},
		"claude-personal": {"id": "claude-personal", "harness": "claude", "profile": "personal"},
		"codex-zai":       {"id": "codex-zai", "harness": "codex", "profile": "zai"},
	}
	got, err := ResolveHarnessProfile(profiles, "claude", "gateway")
	if err != nil || got["id"] != "claude-gateway" {
		t.Fatalf("ResolveHarnessProfile(claude, gateway) = %v, %v", got, err)
	}
	got2, err := ResolveHarnessProfile(profiles, "codex", "zai")
	if err != nil || got2["id"] != "codex-zai" {
		t.Fatalf("ResolveHarnessProfile(codex, zai) = %v, %v", got2, err)
	}
	if _, err := ResolveHarnessProfile(profiles, "claude", "nope"); err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("expected helpful error listing available profiles, got %v", err)
	}
}

func TestClaudeShadowDriverAndArgForwarding(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "home")
	fakeBin := filepath.Join(tmp, "bin")
	homesRoot := filepath.Join(tmp, "homes")
	_ = os.MkdirAll(filepath.Join(fakeHome, ".claude", "skills"), 0o755)
	_ = os.MkdirAll(fakeBin, 0o755)
	makeFakeBin(t, fakeBin, "claude")

	// Seed ~/.claude.json with user-scope mcpServers + a dummy oauth token that must NOT be copied into .shared-mcp.json
	claudeJSON := map[string]any{
		"oauthAccount": "SHOULD_NOT_BE_COPIED",
		"mcpServers": map[string]any{
			"hyphae": map[string]any{"command": "hyp", "args": []string{"mcp"}},
		},
	}
	b, _ := json.Marshal(claudeJSON)
	_ = os.WriteFile(filepath.Join(fakeHome, ".claude.json"), b, 0o600)

	t.Setenv("HOME", fakeHome)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("LIVERY_HOMES_ROOT", homesRoot)

	spec := map[string]any{
		"id":        "claude-gateway",
		"profile":   "gateway",
		"harness":   "claude",
		"mode":      "shadow",
		"auth_mode": "gateway",
		"claude": map[string]any{
			"token_secret": "store:company-gateway-api-key",
			"base_url":     "https://gateway.example.test",
		},
	}
	userArgs := []string{"--dangerously-skip-permissions", "-p", "hello world"}
	drv := Drivers["claude"]
	plan, err := drv.BuildPlan(spec, userArgs, map[string]string{"PATH": os.Getenv("PATH")}, false)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	// Verify equals-form --mcp-config= precedes user args and user args are forwarded verbatim
	if len(plan.Argv) < 5 {
		t.Fatalf("unexpected argv: %v", plan.Argv)
	}
	if !strings.HasPrefix(plan.Argv[1], "--mcp-config=") {
		t.Fatalf("expected --mcp-config= as argv[1], got %v", plan.Argv)
	}
	tail := plan.Argv[len(plan.Argv)-3:]
	if tail[0] != "--dangerously-skip-permissions" || tail[1] != "-p" || tail[2] != "hello world" {
		t.Fatalf("user args not forwarded verbatim: %v", tail)
	}

	// Verify .shared-mcp.json contains ONLY mcpServers and never oauthAccount
	mcpPath := strings.TrimPrefix(plan.Argv[1], "--mcp-config=")
	mb, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read shared-mcp.json: %v", err)
	}
	if !strings.Contains(string(mb), "hyphae") || strings.Contains(string(mb), "SHOULD_NOT_BE_COPIED") {
		t.Fatalf("unexpected .shared-mcp.json: %s", string(mb))
	}

	// Verify secret resolution via AssembleEnv injects ANTHROPIC_AUTH_TOKEN and never leaks in plan.EnvSet
	if _, leaked := plan.EnvSet["ANTHROPIC_AUTH_TOKEN"]; leaked {
		t.Fatalf("secret leaked into plan.EnvSet")
	}
	st, err := secrets.NewStore(secrets.WithBackend("file"), secrets.WithStateRoot(filepath.Join(tmp, "state")))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetManaged("company-gateway-api-key", "tok-abc-999")
	envSlice, err := AssembleEnv(plan, map[string]string{"ANTHROPIC_API_KEY": "stale"}, st)
	if err != nil {
		t.Fatalf("AssembleEnv: %v", err)
	}
	joined := strings.Join(envSlice, "\n")
	if !strings.Contains(joined, "ANTHROPIC_AUTH_TOKEN=tok-abc-999") {
		t.Fatalf("resolved secret missing from env")
	}
	if !strings.Contains(joined, "ANTHROPIC_API_KEY=\n") && !strings.HasSuffix(joined, "ANTHROPIC_API_KEY=") {
		t.Fatalf("ANTHROPIC_API_KEY should be emptied for gateway mode")
	}
}

func TestCodexShadowAndOpenCodeDrivers(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "home")
	fakeBin := filepath.Join(tmp, "bin")
	homesRoot := filepath.Join(tmp, "homes")
	_ = os.MkdirAll(filepath.Join(fakeHome, ".codex"), 0o755)
	_ = os.MkdirAll(fakeBin, 0o755)
	makeFakeBin(t, fakeBin, "codex")
	makeFakeBin(t, fakeBin, "opencode")

	t.Setenv("HOME", fakeHome)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("LIVERY_HOMES_ROOT", homesRoot)

	// Codex custom_provider shadow mode
	codexSpec := map[string]any{
		"id":        "codex-zai",
		"profile":   "zai",
		"harness":   "codex",
		"mode":      "shadow",
		"auth_mode": "custom_provider",
		"codex": map[string]any{
			"provider_id":     "zai",
			"model":           "glm-5.1",
			"provider_name":   "Z.AI",
			"base_url":        "https://api.z.ai/api/coding/paas/v4",
			"env_key":         "ZAI_API_KEY",
			"token_secret":    "store:zai-api-key",
			"scrub_child_env": true,
		},
	}
	cPlan, err := Drivers["codex"].BuildPlan(codexSpec, []string{"--full-auto"}, nil, false)
	if err != nil {
		t.Fatalf("codex BuildPlan: %v", err)
	}
	if cPlan.EnvSet["CODEX_HOME"] == "" {
		t.Fatalf("CODEX_HOME not set")
	}
	argvStr := strings.Join(cPlan.Argv, " ")
	if !strings.Contains(argvStr, `model_provider="zai"`) {
		t.Fatalf("missing model_provider override: %s", argvStr)
	}
	if !strings.Contains(argvStr, `shell_environment_policy.exclude=["ZAI_API_KEY"]`) {
		t.Fatalf("missing shell_environment_policy.exclude: %s", argvStr)
	}
	if cPlan.Argv[len(cPlan.Argv)-1] != "--full-auto" {
		t.Fatalf("trailing arg not forwarded: %v", cPlan.Argv)
	}

	// Codex chatgpt shadow mode forces file credential store
	codexOAuth := map[string]any{
		"id":        "codex-personal",
		"profile":   "personal",
		"harness":   "codex",
		"mode":      "shadow",
		"auth_mode": "chatgpt",
	}
	oAuthPlan, err := Drivers["codex"].BuildPlan(codexOAuth, nil, nil, true)
	if err != nil {
		t.Fatalf("codex oauth BuildPlan: %v", err)
	}
	if !strings.Contains(strings.Join(oAuthPlan.Argv, " "), `cli_auth_credentials_store="file"`) {
		t.Fatalf("missing cli_auth_credentials_store override: %v", oAuthPlan.Argv)
	}

	// OpenCode shadow api mode auto-adds --standalone and sets OPENCODE_CONFIG_DIR
	ocSpec := map[string]any{
		"id":        "opencode-gw",
		"profile":   "gateway",
		"harness":   "opencode",
		"mode":      "shadow",
		"auth_mode": "api",
		"secrets":   map[string]any{"COMPANY_GATEWAY_API_KEY": "store:company-gateway-api-key"},
	}
	oPlan, err := Drivers["opencode"].BuildPlan(ocSpec, []string{"run", "hello"}, nil, true)
	if err != nil {
		t.Fatalf("opencode BuildPlan: %v", err)
	}
	if oPlan.EnvSet["OPENCODE_CONFIG_DIR"] == "" {
		t.Fatalf("OPENCODE_CONFIG_DIR not set")
	}
	if len(oPlan.Argv) != 4 || oPlan.Argv[1] != "--standalone" || oPlan.Argv[2] != "run" || oPlan.Argv[3] != "hello" {
		t.Fatalf("unexpected opencode argv: %v", oPlan.Argv)
	}
}

func TestSessionGroupsAndOpenClawDriver(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "home")
	fakeBin := filepath.Join(tmp, "bin")
	homesRoot := filepath.Join(tmp, "homes")
	groupsRoot := filepath.Join(tmp, "groups")
	_ = os.MkdirAll(filepath.Join(fakeHome, ".claude", "skills"), 0o755)
	_ = os.MkdirAll(filepath.Join(fakeHome, ".openclaw", "skills"), 0o755)
	_ = os.MkdirAll(fakeBin, 0o755)
	makeFakeBin(t, fakeBin, "claude")
	makeFakeBin(t, fakeBin, "openclaw")

	t.Setenv("HOME", fakeHome)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("LIVERY_HOMES_ROOT", homesRoot)
	t.Setenv("LIVERY_GROUPS_ROOT", groupsRoot)

	// Two Claude profiles sharing group "work" + one in group "personal"
	pWork1 := map[string]any{"id": "claude-gw", "profile": "gw", "harness": "claude", "mode": "shadow", "auth_mode": "oauth", "session_group": "work"}
	pWork2 := map[string]any{"id": "claude-corp", "profile": "corp", "harness": "claude", "mode": "shadow", "auth_mode": "oauth", "session_group": "work"}
	pPers := map[string]any{"id": "claude-pers", "profile": "pers", "harness": "claude", "mode": "shadow", "auth_mode": "oauth", "session_group": "personal"}

	plan1, err := Drivers["claude"].BuildPlan(pWork1, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := Drivers["claude"].BuildPlan(pWork2, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	plan3, err := Drivers["claude"].BuildPlan(pPers, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	tgt1, err := os.Readlink(filepath.Join(plan1.EnvSet["CLAUDE_CONFIG_DIR"], "projects"))
	if err != nil {
		t.Fatalf("readlink work1 projects: %v", err)
	}
	tgt2, err := os.Readlink(filepath.Join(plan2.EnvSet["CLAUDE_CONFIG_DIR"], "projects"))
	if err != nil {
		t.Fatalf("readlink work2 projects: %v", err)
	}
	tgt3, err := os.Readlink(filepath.Join(plan3.EnvSet["CLAUDE_CONFIG_DIR"], "projects"))
	if err != nil {
		t.Fatalf("readlink pers projects: %v", err)
	}
	if tgt1 != tgt2 || !strings.Contains(tgt1, filepath.Join("groups", "work", "claude", "projects")) {
		t.Fatalf("work profiles should share group projects dir: %q vs %q", tgt1, tgt2)
	}
	if tgt3 == tgt1 || !strings.Contains(tgt3, filepath.Join("groups", "personal", "claude", "projects")) {
		t.Fatalf("personal profile should use separate group projects dir: %q", tgt3)
	}
	// Shared skills still point to base ~/.claude/skills!
	skLink, err := os.Readlink(filepath.Join(plan1.EnvSet["CLAUDE_CONFIG_DIR"], "skills"))
	if err != nil || !strings.HasSuffix(skLink, filepath.Join(".claude", "skills")) {
		t.Fatalf("skills should still point to ~/.claude/skills, got %q (%v)", skLink, err)
	}

	// OpenClaw driver with session_group
	ocSpec := map[string]any{
		"id": "openclaw-work", "profile": "work", "harness": "openclaw",
		"mode": "shadow", "auth_mode": "profile", "session_group": "work",
	}
	ocPlan, err := Drivers["openclaw"].BuildPlan(ocSpec, []string{"chat"}, nil, false)
	if err != nil {
		t.Fatalf("openclaw BuildPlan: %v", err)
	}
	if ocPlan.EnvSet["OPENCLAW_STATE_DIR"] == "" || ocPlan.Argv[len(ocPlan.Argv)-1] != "chat" {
		t.Fatalf("unexpected openclaw plan: %+v", ocPlan)
	}
	ocMem, err := os.Readlink(filepath.Join(ocPlan.EnvSet["OPENCLAW_STATE_DIR"], "memory"))
	if err != nil || !strings.Contains(ocMem, filepath.Join("groups", "work", "openclaw", "memory")) {
		t.Fatalf("openclaw memory should point to group dir, got %q (%v)", ocMem, err)
	}

	groups := SummarizeGroups(map[string]map[string]any{
		"claude-gw": pWork1, "claude-corp": pWork2, "claude-pers": pPers, "openclaw-work": ocSpec,
	})
	if len(groups) != 2 || groups[0].Name != "personal" || groups[1].Name != "work" || len(groups[1].Profiles) != 3 {
		t.Fatalf("unexpected group summary: %+v", groups)
	}
}
