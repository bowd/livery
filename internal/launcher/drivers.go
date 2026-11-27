package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/secrets"
)

type LaunchPlan struct {
	ProfileID     string            `json:"profile"`
	ProfileName   string            `json:"profile_name"`
	Harness       string            `json:"harness"`
	Binary        string            `json:"binary"`
	Argv          []string          `json:"argv"`
	EnvSet        map[string]string `json:"env_set"`
	EnvSecretRefs map[string]string `json:"-"`
	EnvUnset      []string          `json:"env_unset"`
	HomeInfo      map[string]any    `json:"home"`
	Notes         []string          `json:"notes"`
}

type Driver interface {
	Name() string
	DefaultBinary() string
	DefaultSharedHome() string
	AuthModes() []string
	ResolveHome(spec map[string]any) (mode, shared, eff string)
	RequiredSecrets(spec map[string]any) map[string]string
	BuildPlan(spec map[string]any, userArgs []string, callerEnv map[string]string, dryRun bool) (*LaunchPlan, error)
	LoginArgs(spec map[string]any) []string
	ProbeArgs(spec map[string]any) []string
}

var Drivers = map[string]Driver{
	"claude":   &claudeDriver{},
	"codex":    &codexDriver{},
	"opencode": &opencodeDriver{},
	"openclaw": &openclawDriver{},
	"agy":      &agyDriver{},
}

func strVal(m map[string]any, k, def string) string {
	if m == nil {
		return def
	}
	if v, ok := m[k]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return def
}

func boolVal(m map[string]any, k string, def bool) bool {
	if m == nil {
		return def
	}
	if v, ok := m[k]; ok && v != nil {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func intVal(m map[string]any, k string) (int, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[k]
	if !ok || v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case string:
		n, err := strconv.Atoi(x)
		return n, err == nil
	}
	return 0, false
}

func mapVal(m map[string]any, k string) map[string]any {
	if m == nil {
		return nil
	}
	if sub, ok := m[k].(map[string]any); ok {
		return sub
	}
	return nil
}

func strSliceVal(m map[string]any, k string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[k]
	if !ok || raw == nil {
		return nil
	}
	switch x := raw.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, el := range x {
			out = append(out, fmt.Sprint(el))
		}
		return out
	}
	return nil
}

func baseResolveHome(d Driver, spec map[string]any) (string, string, string) {
	mode := strVal(spec, "mode", "direct")
	sharedRaw := strVal(spec, "shared_home", d.DefaultSharedHome())
	shared := secrets.ExpandPath(sharedRaw)
	rawHome := strings.TrimSpace(strVal(spec, "home_path", ""))
	var eff string
	if rawHome != "" {
		eff = secrets.ExpandPath(rawHome)
	} else if mode == "direct" {
		eff = shared
	} else {
		eff = filepath.Join(HomesRoot(), d.Name(), strVal(spec, "id", "default"))
	}
	return mode, shared, eff
}

func baseRequiredSecrets(spec map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range mapVal(spec, "secrets") {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func uniqueSorted(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, s := range items {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("@%+=:,./-_", r)) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

func hasArg(args []string, needle string) bool {
	for _, a := range args {
		if a == needle {
			return true
		}
	}
	return false
}

// --- Claude Driver ------------------------------------------------------------------------------

type claudeDriver struct{}

var claudeScrubVars = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS",
	"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
	"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
	"CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR", "CLAUDE_CODE_API_KEY_HELPER_TTL_MS",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CONFIG_DIR", "ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_VERTEX_PROJECT_ID",
	"ANTHROPIC_FOUNDRY_API_KEY", "ANTHROPIC_FOUNDRY_BASE_URL", "ANTHROPIC_FOUNDRY_RESOURCE",
}

var claudeMgmtSubcmds = map[string]bool{
	"auth": true, "mcp": true, "plugin": true, "plugins": true, "doctor": true,
	"install": true, "update": true, "agents": true, "attach": true, "logs": true,
	"rm": true, "purge": true, "respawn": true, "gateway": true, "import": true, "auto-mode": true,
}

func (d *claudeDriver) Name() string              { return "claude" }
func (d *claudeDriver) DefaultBinary() string     { return "claude" }
func (d *claudeDriver) DefaultSharedHome() string { return "~/.claude" }
func (d *claudeDriver) AuthModes() []string       { return []string{"oauth", "api_key_helper", "gateway", "api"} }
func (d *claudeDriver) ResolveHome(spec map[string]any) (string, string, string) {
	return baseResolveHome(d, spec)
}

func (d *claudeDriver) RequiredSecrets(spec map[string]any) map[string]string {
	out := baseRequiredSecrets(spec)
	cfg := mapVal(spec, "claude")
	authMode := strVal(spec, "auth_mode", "oauth")
	tok := strings.TrimSpace(strVal(cfg, "token_secret", ""))
	if tok != "" {
		switch authMode {
		case "api_key_helper":
			out["__claude_api_key_helper__"] = tok
		case "gateway":
			out["ANTHROPIC_AUTH_TOKEN"] = tok
		case "api":
			out["ANTHROPIC_API_KEY"] = tok
		}
	}
	return out
}

func claudeBaseJSONPath(sharedAbs string) string {
	home, _ := os.UserHomeDir()
	defClaude, _ := filepath.Abs(filepath.Join(home, ".claude"))
	absShared, _ := filepath.Abs(sharedAbs)
	if absShared == defClaude {
		return filepath.Join(home, ".claude.json")
	}
	return filepath.Join(sharedAbs, ".claude.json")
}

// readClaudeBaseMCPServers extracts ONLY user-scope `mcpServers` from base .claude.json (never auth fields).
func readClaudeBaseMCPServers(sharedAbs string) (map[string]any, bool) {
	src := claudeBaseJSONPath(sharedAbs)
	b, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, true
		}
		return nil, false
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return nil, false
	}
	if mcps, ok := doc["mcpServers"].(map[string]any); ok {
		return mcps, true
	}
	return map[string]any{}, true
}

func (d *claudeDriver) BuildPlan(spec map[string]any, userArgs []string, callerEnv map[string]string, dryRun bool) (*LaunchPlan, error) {
	mode, shared, eff := d.ResolveHome(spec)
	authMode := strVal(spec, "auth_mode", "oauth")
	cfg := mapVal(spec, "claude")
	shadowCfg := mapVal(spec, "shadow")

	var homeInfo map[string]any
	if mode == "shadow" {
		sharePlugins := boolVal(shadowCfg, "share_plugins", true)
		shareMCP := boolVal(shadowCfg, "share_mcp", true)
		sharedEntries := []string{
			"projects", "sessions", "history.jsonl", "file-history", "session-env",
			"CLAUDE.md", "skills", "hooks", "settings.json", "settings.local.json",
			"agents", "commands", "rules", "plans", "todos", "tasks", "teams", "keybindings.json",
		}
		if sharePlugins {
			sharedEntries = append(sharedEntries, "plugins")
		}
		sharedEntries = append(sharedEntries, strSliceVal(shadowCfg, "share")...)
		privEntries := append([]string{
			".claude.json", ".claude.json.lock", ".credentials.json", ".shared-mcp.json",
			"backups", "cache", "debug", "telemetry", "state", "spares",
			"stats-cache.json", ".last-cleanup", ".last-update-result.json", "ide",
		}, strSliceVal(shadowCfg, "private")...)
		groupName := strings.TrimSpace(strVal(spec, "session_group", ""))
		groupRoot := GroupHarnessDir(groupName, "claude")
		groupDirs := []string{"projects", "sessions", "file-history", "session-env", "plans", "todos", "tasks", "teams"}
		groupFiles := []string{"history.jsonl"}
		groupEntries := append(append([]string(nil), groupDirs...), groupFiles...)

		sharedMCPPath := filepath.Join(eff, ".shared-mcp.json")
		var mcps map[string]any
		mcpsOK := true
		if shareMCP {
			mcps, mcpsOK = readClaudeBaseMCPServers(shared)
		}
		if dryRun {
			homeInfo = map[string]any{
				"mode":           "shadow",
				"shared_home":    shared,
				"effective_home": eff,
				"private":        uniqueSorted(privEntries),
			}
			if groupName != "" {
				homeInfo["session_group"] = groupName
				homeInfo["group_root"] = groupRoot
				homeInfo["group_entries"] = groupEntries
			}
			if shareMCP && len(mcps) > 0 {
				names := make([]string, 0, len(mcps))
				for k := range mcps {
					names = append(names, k)
				}
				sort.Strings(names)
				homeInfo["shared_mcp_file"] = sharedMCPPath
				homeInfo["shared_mcp_servers"] = names
			}
		} else {
			var err error
			homeInfo, err = MaterializeShadowHome(shared, eff, ShadowOpts{
				KnownSharedDirs:      []string{"projects", "sessions", "file-history", "session-env", "skills", "hooks"},
				PrivateNames:         privEntries,
				SharedNames:          sharedEntries,
				VerifyPrivateNotLink: []string{".claude.json", ".credentials.json"},
				GroupRoot:            groupRoot,
				GroupEntries:         groupEntries,
				KnownGroupDirs:       groupDirs,
				KnownGroupFiles:      groupFiles,
			})
			if err != nil {
				return nil, err
			}
			homeInfo["mode"] = "shadow"
			homeInfo["effective_home"] = eff
			if groupName != "" {
				homeInfo["session_group"] = groupName
			}
			if shareMCP {
				if !mcpsOK {
					if _, err := os.Stat(sharedMCPPath); err == nil {
						homeInfo["shared_mcp_file"] = sharedMCPPath
					}
				} else if len(mcps) > 0 {
					payload, _ := json.MarshalIndent(map[string]any{"mcpServers": mcps}, "", "  ")
					payload = append(payload, '\n')
					if err := writePrivateFileIfChanged(sharedMCPPath, payload, 0o600); err != nil {
						return nil, err
					}
					names := make([]string, 0, len(mcps))
					for k := range mcps {
						names = append(names, k)
					}
					sort.Strings(names)
					homeInfo["shared_mcp_file"] = sharedMCPPath
					homeInfo["shared_mcp_servers"] = names
				} else {
					_ = os.Remove(sharedMCPPath)
				}
			} else {
				_ = os.Remove(sharedMCPPath)
			}
		}
	} else {
		if mode == "isolated" && !dryRun {
			if err := ensurePrivateDir(eff); err != nil {
				return nil, err
			}
		}
		homeInfo = map[string]any{"mode": mode, "shared_home": shared, "effective_home": eff}
	}

	envSet := map[string]string{}
	envSecrets := map[string]string{}
	envUnset := append(append([]string(nil), claudeScrubVars...), strSliceVal(spec, "unset_env")...)
	var notes []string

	if mode == "shadow" || mode == "isolated" || strings.TrimSpace(strVal(spec, "home_path", "")) != "" {
		envSet["CLAUDE_CONFIG_DIR"] = eff
	}
	if bu := strVal(cfg, "base_url", ""); bu != "" {
		envSet["ANTHROPIC_BASE_URL"] = bu
	}
	if ttl, ok := intVal(cfg, "api_key_helper_ttl_ms"); ok && ttl > 0 {
		envSet["CLAUDE_CODE_API_KEY_HELPER_TTL_MS"] = strconv.Itoa(ttl)
	}
	if dm := mapVal(cfg, "default_models"); dm != nil {
		dmMap := map[string]string{
			"sonnet":     "ANTHROPIC_DEFAULT_SONNET_MODEL",
			"opus":       "ANTHROPIC_DEFAULT_OPUS_MODEL",
			"haiku":      "ANTHROPIC_DEFAULT_HAIKU_MODEL",
			"small_fast": "ANTHROPIC_SMALL_FAST_MODEL",
			"subagent":   "CLAUDE_CODE_SUBAGENT_MODEL",
		}
		for k, envName := range dmMap {
			if v := strVal(dm, k, ""); v != "" {
				envSet[envName] = v
			}
		}
	}

	tok := strings.TrimSpace(strVal(cfg, "token_secret", ""))
	switch authMode {
	case "gateway":
		if tok != "" {
			envSecrets["ANTHROPIC_AUTH_TOKEN"] = tok
		}
		envSet["ANTHROPIC_API_KEY"] = ""
	case "api":
		if tok != "" {
			envSecrets["ANTHROPIC_API_KEY"] = tok
		}
	case "api_key_helper":
		notes = append(notes, "secret delivered via Claude apiKeyHelper (never in process env)")
	}

	for k, v := range mapVal(spec, "env") {
		envSet[k] = fmt.Sprint(v)
	}
	for k, v := range mapVal(spec, "secrets") {
		envSecrets[k] = fmt.Sprint(v)
	}

	first := ""
	if len(userArgs) > 0 {
		first = userArgs[0]
	}
	isMgmt := claudeMgmtSubcmds[first] || first == "-v" || first == "--version" || first == "-h" || first == "--help"

	var extraArgs []string
	if !isMgmt {
		settingsObj := map[string]any{}
		for k, v := range mapVal(cfg, "settings") {
			settingsObj[k] = v
		}
		if authMode == "api_key_helper" && tok != "" {
			exe, _ := os.Executable()
			if exe == "" {
				exe = "liv"
			}
			settingsObj["apiKeyHelper"] = fmt.Sprintf("%s secret-helper %s", shellQuote(exe), shellQuote(tok))
		}
		if len(settingsObj) > 0 {
			b, _ := json.Marshal(settingsObj)
			extraArgs = append(extraArgs, "--settings", string(b))
		}
		if mdl := strVal(cfg, "model", ""); mdl != "" {
			extraArgs = append(extraArgs, "--model", mdl)
		}
		if acw, ok := intVal(cfg, "auto_compact_window"); ok && acw > 0 {
			extraArgs = append(extraArgs, "--autocompact", strconv.Itoa(acw))
		}
		// Equals-form `--mcp-config=<path>` so Commander.js never swallows positional prompts.
		if mcpFile, _ := homeInfo["shared_mcp_file"].(string); mcpFile != "" && !hasArg(userArgs, "--strict-mcp-config") {
			extraArgs = append(extraArgs, "--mcp-config="+mcpFile)
			if mnames, _ := homeInfo["shared_mcp_servers"].([]string); len(mnames) > 0 {
				notes = append(notes, fmt.Sprintf("sharing %d user-scope MCP server(s) via --mcp-config (%s)", len(mnames), strings.Join(mnames, ", ")))
			}
		}
		if mc := strVal(cfg, "mcp_config", ""); mc != "" {
			extraArgs = append(extraArgs, "--mcp-config="+secrets.ExpandPath(mc))
		}
		extraArgs = append(extraArgs, strSliceVal(spec, "launch_args")...)
	}

	binName := strVal(spec, "binary", d.DefaultBinary())
	var bin string
	if dryRun {
		bin = TryResolveBinary(binName, callerEnv)
	} else {
		var err error
		bin, err = ResolveBinary(binName, callerEnv)
		if err != nil {
			return nil, err
		}
	}
	argv := append([]string{bin}, extraArgs...)
	argv = append(argv, userArgs...)
	pid := strVal(spec, "id", "")
	return &LaunchPlan{
		ProfileID:     pid,
		ProfileName:   model.DeriveProfileName(pid, d.Name(), strVal(spec, "profile", "")),
		Harness:       d.Name(),
		Binary:        bin,
		Argv:          argv,
		EnvSet:        envSet,
		EnvSecretRefs: envSecrets,
		EnvUnset:      uniqueSorted(envUnset),
		HomeInfo:      homeInfo,
		Notes:         notes,
	}, nil
}

func (d *claudeDriver) LoginArgs(spec map[string]any) []string {
	if strVal(spec, "auth_mode", "oauth") == "oauth" {
		return []string{"auth", "login"}
	}
	return nil
}

func (d *claudeDriver) ProbeArgs(_ map[string]any) []string { return []string{"auth", "status"} }

// --- Codex Driver -------------------------------------------------------------------------------

type codexDriver struct{}

var codexScrubVars = []string{
	"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "OPENAI_ORG_ID",
	"OPENAI_ORGANIZATION", "OPENAI_PROJECT_ID", "CODEX_AUTH_TOKEN",
	"CODEX_ACCESS_TOKEN", "ACCESS_TOKEN", "CODEX_HOME", "T3CODE_CODEX_LAUNCH_ARGS",
}

func (d *codexDriver) Name() string              { return "codex" }
func (d *codexDriver) DefaultBinary() string     { return "codex" }
func (d *codexDriver) DefaultSharedHome() string { return "~/.codex" }
func (d *codexDriver) AuthModes() []string       { return []string{"chatgpt", "api", "custom_provider"} }
func (d *codexDriver) ResolveHome(spec map[string]any) (string, string, string) {
	return baseResolveHome(d, spec)
}

func (d *codexDriver) RequiredSecrets(spec map[string]any) map[string]string {
	out := baseRequiredSecrets(spec)
	cfg := mapVal(spec, "codex")
	authMode := strVal(spec, "auth_mode", "chatgpt")
	tok := strings.TrimSpace(strVal(cfg, "token_secret", ""))
	if tok != "" {
		switch authMode {
		case "api":
			envKey := strVal(cfg, "env_key", "OPENAI_API_KEY")
			out[envKey] = tok
		case "custom_provider":
			if envKey := strVal(cfg, "env_key", ""); envKey != "" {
				out[envKey] = tok
			}
		}
	}
	return out
}

func codexCustomModelsBytes(spec map[string]any) ([]byte, bool, error) {
	cfg := mapVal(spec, "codex")
	if cfg == nil {
		return nil, false, nil
	}
	if models, ok := cfg["models"]; ok && models != nil {
		b, err := json.MarshalIndent(map[string]any{"models": models}, "", "  ")
		if err != nil {
			return nil, true, err
		}
		return append(b, '\n'), true, nil
	}
	p := strings.TrimSpace(strVal(cfg, "models_json_path", strVal(cfg, "models_json", "")))
	if p == "" {
		return nil, false, nil
	}
	exp := secrets.ExpandPath(p)
	b, err := os.ReadFile(exp)
	if err != nil {
		return nil, true, fmt.Errorf("codex models_json file not found: %s", exp)
	}
	return b, true, nil
}

func (d *codexDriver) BuildPlan(spec map[string]any, userArgs []string, callerEnv map[string]string, dryRun bool) (*LaunchPlan, error) {
	mode, shared, eff := d.ResolveHome(spec)
	authMode := strVal(spec, "auth_mode", "chatgpt")
	cfg := mapVal(spec, "codex")
	shadowCfg := mapVal(spec, "shadow")
	hasModels := cfg != nil && (cfg["models"] != nil || strVal(cfg, "models_json_path", "") != "" || strVal(cfg, "models_json", "") != "")

	var homeInfo map[string]any
	if mode == "shadow" {
		priv := []string{"auth.json", "auth.json.lock", "auth.lock", "models_cache.json", "log", "memories", "tmp"}
		if hasModels {
			priv = append(priv, "models.json")
		}
		priv = append(priv, strSliceVal(shadowCfg, "private")...)
		stale := []string{"models_cache.json"}
		if hasModels {
			stale = append(stale, "models.json")
		}
		groupName := strings.TrimSpace(strVal(spec, "session_group", ""))
		groupRoot := GroupHarnessDir(groupName, "codex")
		groupDirs := []string{"sessions", "archived_sessions", "sqlite", "memories", "shell_snapshots", "worktrees"}
		if dryRun {
			homeInfo = map[string]any{
				"mode": "shadow", "shared_home": shared, "effective_home": eff,
				"private": uniqueSorted(priv), "custom_models_json": hasModels,
			}
			if groupName != "" {
				homeInfo["session_group"] = groupName
				homeInfo["group_root"] = groupRoot
				homeInfo["group_entries"] = groupDirs
			}
		} else {
			var err error
			homeInfo, err = MaterializeShadowHome(shared, eff, ShadowOpts{
				KnownSharedDirs:        []string{"sessions", "archived_sessions", "sqlite", "shell_snapshots", "worktrees", "skills", "plugins", "cache", "logs", "mcp-oauth-locks"},
				PrivateNames:           priv,
				ShareAllOther:          true,
				ReplaceableDirs:        []string{"mcp-oauth-locks"},
				RemoveStalePrivateLink: stale,
				VerifyPrivateNotLink:   []string{"auth.json"},
				GroupRoot:              groupRoot,
				GroupEntries:           groupDirs,
				KnownGroupDirs:         groupDirs,
			})
			if err != nil {
				return nil, err
			}
			homeInfo["mode"] = "shadow"
			homeInfo["effective_home"] = eff
			if groupName != "" {
				homeInfo["session_group"] = groupName
			}
			if hasModels {
				mb, _, err := codexCustomModelsBytes(spec)
				if err != nil {
					return nil, err
				}
				if len(mb) > 0 {
					mpath := filepath.Join(eff, "models.json")
					if err := writePrivateFileIfChanged(mpath, mb, 0o600); err != nil {
						return nil, err
					}
					homeInfo["models_json"] = mpath
				}
			}
		}
	} else {
		if mode == "isolated" && !dryRun {
			if err := ensurePrivateDir(eff); err != nil {
				return nil, err
			}
			if hasModels {
				mb, _, err := codexCustomModelsBytes(spec)
				if err != nil {
					return nil, err
				}
				if len(mb) > 0 {
					_ = writePrivateFileIfChanged(filepath.Join(eff, "models.json"), mb, 0o600)
				}
			}
		}
		homeInfo = map[string]any{"mode": mode, "shared_home": shared, "effective_home": eff}
	}

	envSet := map[string]string{}
	envSecrets := map[string]string{}
	envUnset := append(append([]string(nil), codexScrubVars...), strSliceVal(spec, "unset_env")...)
	var notes []string

	if ek := strVal(cfg, "env_key", ""); ek != "" {
		envUnset = append(envUnset, ek)
	}
	if mode == "shadow" || mode == "isolated" || strings.TrimSpace(strVal(spec, "home_path", "")) != "" {
		envSet["CODEX_HOME"] = eff
	}

	tok := strings.TrimSpace(strVal(cfg, "token_secret", ""))
	if authMode == "api" {
		ek := strVal(cfg, "env_key", "OPENAI_API_KEY")
		if tok != "" {
			envSecrets[ek] = tok
		}
		if bu := strVal(cfg, "base_url", ""); bu != "" {
			envSet["OPENAI_BASE_URL"] = bu
		}
	} else if authMode == "custom_provider" {
		ek := strVal(cfg, "env_key", "")
		if tok != "" && ek != "" {
			envSecrets[ek] = tok
		}
	}
	for k, v := range mapVal(spec, "env") {
		envSet[k] = fmt.Sprint(v)
	}
	for k, v := range mapVal(spec, "secrets") {
		envSecrets[k] = fmt.Sprint(v)
	}

	first := ""
	if len(userArgs) > 0 {
		first = userArgs[0]
	}
	isHelp := first == "-h" || first == "--help" || first == "-V" || first == "--version" || first == "help"

	var cOverrides []string
	if !isHelp {
		if authMode == "chatgpt" && (mode == "shadow" || mode == "isolated") {
			cOverrides = append(cOverrides, `cli_auth_credentials_store="file"`)
		}
		if authMode == "custom_provider" && cfg != nil {
			pid := strVal(cfg, "provider_id", "")
			pkey := model.TomlKey(pid)
			cOverrides = append(cOverrides, "model_provider="+model.TomlScalar(pid))
			if pn := strVal(cfg, "provider_name", ""); pn != "" {
				cOverrides = append(cOverrides, fmt.Sprintf("model_providers.%s.name=%s", pkey, model.TomlScalar(pn)))
			}
			cOverrides = append(cOverrides, fmt.Sprintf("model_providers.%s.base_url=%s", pkey, model.TomlScalar(strVal(cfg, "base_url", ""))))
			cOverrides = append(cOverrides, fmt.Sprintf("model_providers.%s.wire_api=%s", pkey, model.TomlScalar(strVal(cfg, "wire_api", "responses"))))
			if ek := strVal(cfg, "env_key", ""); ek != "" {
				cOverrides = append(cOverrides, fmt.Sprintf("model_providers.%s.env_key=%s", pkey, model.TomlScalar(ek)))
			}
			reqOA := boolVal(cfg, "requires_openai_auth", false)
			cOverrides = append(cOverrides, fmt.Sprintf("model_providers.%s.requires_openai_auth=%s", pkey, model.TomlScalar(reqOA)))
			if mcu := strVal(cfg, "model_catalog_url", ""); mcu != "" {
				cOverrides = append(cOverrides, fmt.Sprintf("model_providers.%s.model_catalog_url=%s", pkey, model.TomlScalar(mcu)))
			}
			if _, hasWS := cfg["supports_websockets"]; hasWS {
				cOverrides = append(cOverrides, fmt.Sprintf("model_providers.%s.supports_websockets=%s", pkey, model.TomlScalar(boolVal(cfg, "supports_websockets", false))))
			}
			if boolVal(cfg, "api_key_model_discovery", false) {
				cOverrides = append(cOverrides, "features.api_key_model_discovery=true")
			}
		}
		if mdl := strVal(cfg, "model", ""); mdl != "" {
			cOverrides = append(cOverrides, "model="+model.TomlScalar(mdl))
		}
		if boolVal(cfg, "scrub_child_env", true) && len(envSecrets) > 0 {
			secNames := make([]string, 0, len(envSecrets))
			for k := range envSecrets {
				secNames = append(secNames, k)
			}
			sort.Strings(secNames)
			cOverrides = append(cOverrides,
				"shell_environment_policy.ignore_default_excludes=false",
				"shell_environment_policy.exclude="+model.TomlScalar(secNames),
			)
			notes = append(notes, fmt.Sprintf("shell_environment_policy excludes %s from agent subprocesses", strings.Join(secNames, ", ")))
		}
		if ovs := mapVal(cfg, "config_overrides"); ovs != nil {
			keys := make([]string, 0, len(ovs))
			for k := range ovs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				cOverrides = append(cOverrides, fmt.Sprintf("%s=%s", k, model.TomlScalar(ovs[k])))
			}
		}
	}

	var extraArgs []string
	for _, ov := range cOverrides {
		extraArgs = append(extraArgs, "-c", ov)
	}
	if !isHelp {
		extraArgs = append(extraArgs, strSliceVal(spec, "launch_args")...)
	}

	binName := strVal(spec, "binary", d.DefaultBinary())
	var bin string
	if dryRun {
		bin = TryResolveBinary(binName, callerEnv)
	} else {
		var err error
		bin, err = ResolveBinary(binName, callerEnv)
		if err != nil {
			return nil, err
		}
	}
	argv := append([]string{bin}, extraArgs...)
	argv = append(argv, userArgs...)
	pid := strVal(spec, "id", "")
	return &LaunchPlan{
		ProfileID:     pid,
		ProfileName:   model.DeriveProfileName(pid, d.Name(), strVal(spec, "profile", "")),
		Harness:       d.Name(),
		Binary:        bin,
		Argv:          argv,
		EnvSet:        envSet,
		EnvSecretRefs: envSecrets,
		EnvUnset:      uniqueSorted(envUnset),
		HomeInfo:      homeInfo,
		Notes:         notes,
	}, nil
}

func (d *codexDriver) LoginArgs(spec map[string]any) []string {
	if strVal(spec, "auth_mode", "chatgpt") == "chatgpt" {
		return []string{"login"}
	}
	return nil
}

func (d *codexDriver) ProbeArgs(_ map[string]any) []string { return []string{"login", "status"} }

// --- OpenCode Driver ----------------------------------------------------------------------------

type opencodeDriver struct{}

var opencodeScrubVars = []string{
	"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "OPENCODE_SERVER_PASSWORD",
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY",
	"OPENROUTER_API_KEY", "ZAI_API_KEY",
}

func (d *opencodeDriver) Name() string              { return "opencode" }
func (d *opencodeDriver) DefaultBinary() string     { return "opencode" }
func (d *opencodeDriver) DefaultSharedHome() string { return "~/.config/opencode" }
func (d *opencodeDriver) AuthModes() []string       { return []string{"native", "api", "server"} }
func (d *opencodeDriver) ResolveHome(spec map[string]any) (string, string, string) {
	return baseResolveHome(d, spec)
}

func (d *opencodeDriver) RequiredSecrets(spec map[string]any) map[string]string {
	out := baseRequiredSecrets(spec)
	if sp := strVal(mapVal(spec, "opencode"), "server_password_secret", ""); sp != "" {
		out["OPENCODE_SERVER_PASSWORD"] = sp
	}
	return out
}

func (d *opencodeDriver) BuildPlan(spec map[string]any, userArgs []string, callerEnv map[string]string, dryRun bool) (*LaunchPlan, error) {
	mode, shared, eff := d.ResolveHome(spec)
	authMode := strVal(spec, "auth_mode", "native")
	cfg := mapVal(spec, "opencode")
	shadowCfg := mapVal(spec, "shadow")

	cfgDir := filepath.Join(eff, "config")
	dataDir := filepath.Join(eff, "data")
	stateDir := filepath.Join(eff, "state")

	var homeInfo map[string]any
	switch mode {
	case "direct":
		homeInfo = map[string]any{"mode": "direct", "shared_home": shared, "effective_home": eff}
	case "isolated":
		if !dryRun {
			for _, p := range []string{eff, cfgDir, dataDir, stateDir} {
				if err := ensurePrivateDir(p); err != nil {
					return nil, err
				}
			}
		}
		homeInfo = map[string]any{"mode": "isolated", "shared_home": shared, "effective_home": eff, "config_dir": cfgDir, "xdg_data_home": dataDir, "xdg_state_home": stateDir}
	default:
		priv := append([]string{"service.json"}, strSliceVal(shadowCfg, "private")...)
		if dryRun {
			homeInfo = map[string]any{"mode": "shadow", "shared_home": shared, "effective_home": eff, "config_dir": cfgDir, "shared_data": true}
		} else {
			_ = ensurePrivateDir(eff)
			var err error
			homeInfo, err = MaterializeShadowHome(shared, cfgDir, ShadowOpts{
				PrivateNames:  priv,
				ShareAllOther: true,
			})
			if err != nil {
				return nil, err
			}
			homeInfo["mode"] = "shadow"
			homeInfo["effective_home"] = eff
			homeInfo["config_dir"] = cfgDir
			homeInfo["shared_data"] = true
		}
	}

	envSet := map[string]string{}
	envSecrets := map[string]string{}
	envUnset := append(append([]string(nil), opencodeScrubVars...), strSliceVal(spec, "unset_env")...)
	var notes []string

	if mode == "isolated" {
		envSet["OPENCODE_CONFIG_DIR"] = cfgDir
		envSet["XDG_DATA_HOME"] = dataDir
		envSet["XDG_STATE_HOME"] = stateDir
	} else if mode == "shadow" {
		envSet["OPENCODE_CONFIG_DIR"] = cfgDir
		if grp := strings.TrimSpace(strVal(spec, "session_group", "")); grp != "" {
			groot := GroupHarnessDir(grp, "opencode")
			gData := filepath.Join(groot, "data")
			gState := filepath.Join(groot, "state")
			if !dryRun {
				_ = ensurePrivateDir(gData)
				_ = ensurePrivateDir(gState)
			}
			envSet["XDG_DATA_HOME"] = gData
			envSet["XDG_STATE_HOME"] = gState
			homeInfo["session_group"] = grp
			homeInfo["group_root"] = groot
		}
	}
	if cf := strVal(cfg, "config_file_path", strVal(cfg, "config_file", "")); cf != "" {
		envSet["OPENCODE_CONFIG"] = secrets.ExpandPath(cf)
	}
	contentObj := map[string]any{}
	for k, v := range mapVal(cfg, "config_content") {
		contentObj[k] = v
	}
	if mdl := strVal(cfg, "model", ""); mdl != "" {
		if _, ok := contentObj["model"]; !ok {
			contentObj["model"] = mdl
		}
	}
	if len(contentObj) > 0 {
		b, _ := json.Marshal(contentObj)
		envSet["OPENCODE_CONFIG_CONTENT"] = string(b)
	}
	if sp := strVal(cfg, "server_password_secret", ""); sp != "" {
		envSecrets["OPENCODE_SERVER_PASSWORD"] = sp
	}
	for k, v := range mapVal(spec, "env") {
		envSet[k] = fmt.Sprint(v)
	}
	for k, v := range mapVal(spec, "secrets") {
		envSecrets[k] = fmt.Sprint(v)
	}

	first := ""
	if len(userArgs) > 0 {
		first = userArgs[0]
	}
	isHelp := first == "-h" || first == "--help" || first == "-v" || first == "--version"
	var extraArgs []string
	if !isHelp {
		if su := strings.TrimSpace(strVal(cfg, "server_url", "")); su != "" {
			extraArgs = append(extraArgs, "--server", su)
		} else {
			defStandalone := mode != "direct" || authMode == "api" || len(envSecrets) > 0 || len(contentObj) > 0
			useStandalone := boolVal(cfg, "standalone", defStandalone) && !boolVal(cfg, "use_service", false)
			if useStandalone {
				extraArgs = append(extraArgs, "--standalone")
			}
		}
		extraArgs = append(extraArgs, strSliceVal(spec, "launch_args")...)
	}

	binName := strVal(spec, "binary", d.DefaultBinary())
	var bin string
	if dryRun {
		bin = TryResolveBinary(binName, callerEnv)
	} else {
		var err error
		bin, err = ResolveBinary(binName, callerEnv)
		if err != nil {
			return nil, err
		}
	}
	argv := append([]string{bin}, extraArgs...)
	argv = append(argv, userArgs...)
	pid := strVal(spec, "id", "")
	return &LaunchPlan{
		ProfileID:     pid,
		ProfileName:   model.DeriveProfileName(pid, d.Name(), strVal(spec, "profile", "")),
		Harness:       d.Name(),
		Binary:        bin,
		Argv:          argv,
		EnvSet:        envSet,
		EnvSecretRefs: envSecrets,
		EnvUnset:      uniqueSorted(envUnset),
		HomeInfo:      homeInfo,
		Notes:         notes,
	}, nil
}

func (d *opencodeDriver) LoginArgs(spec map[string]any) []string {
	if strVal(spec, "auth_mode", "native") == "native" {
		return []string{"auth", "login"}
	}
	return nil
}

func (d *opencodeDriver) ProbeArgs(_ map[string]any) []string { return []string{"auth", "list"} }

// --- Antigravity (`agy`) Driver -----------------------------------------------------------------

type agyDriver struct{}

var agyScrubVars = []string{
	"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GEMINI_BASE_URL", "GOOGLE_APPLICATION_CREDENTIALS",
	"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_QUOTA_PROJECT",
	"GOOGLE_GENAI_USE_VERTEXAI", "GCLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT",
	"AGY_ADC_AUTH", "AGY_ACP_CCPA_PROJECT", "AGY_ACP_ENABLE_OAUTH", "AGY_ACP_FORCE_FILE_STORAGE",
	"GEMINI_HOME",
}

func (d *agyDriver) Name() string              { return "agy" }
func (d *agyDriver) DefaultBinary() string     { return "agy" }
func (d *agyDriver) DefaultSharedHome() string { return "~/.gemini" }
func (d *agyDriver) AuthModes() []string       { return []string{"oauth", "api", "adc", "acp"} }
func (d *agyDriver) ResolveHome(spec map[string]any) (string, string, string) {
	return baseResolveHome(d, spec)
}

func (d *agyDriver) RequiredSecrets(spec map[string]any) map[string]string {
	out := baseRequiredSecrets(spec)
	cfg := mapVal(spec, "agy")
	authMode := strVal(spec, "auth_mode", "oauth")
	tok := strings.TrimSpace(strVal(cfg, "token_secret", ""))
	if tok != "" {
		if authMode == "api" {
			out["GEMINI_API_KEY"] = tok
		} else if authMode == "acp" {
			switch strVal(cfg, "acp_auth_method", "oauth-personal") {
			case "gemini-api-key":
				out["GEMINI_API_KEY"] = tok
			case "agent-platform":
				out["GOOGLE_API_KEY"] = tok
			}
		}
	}
	return out
}

func (d *agyDriver) BuildPlan(spec map[string]any, userArgs []string, callerEnv map[string]string, dryRun bool) (*LaunchPlan, error) {
	mode, shared, eff := d.ResolveHome(spec)
	authMode := strVal(spec, "auth_mode", "oauth")
	cfg := mapVal(spec, "agy")

	homeInfo := map[string]any{"mode": mode, "shared_home": shared, "effective_home": eff}
	if !dryRun && (mode != "direct" || authMode == "acp") {
		if err := ensurePrivateDir(eff); err != nil {
			return nil, err
		}
		if mode == "shadow" {
			for _, sub := range []string{"config/skills", "antigravity-cli/skills"} {
				src := filepath.Join(shared, filepath.FromSlash(sub))
				if _, err := os.Stat(src); err == nil {
					_ = ensureSymlink(filepath.Join(eff, filepath.FromSlash(sub)), src, false)
				}
			}
		}
		if authMode == "acp" {
			acpMethod := strVal(cfg, "acp_auth_method", "oauth-personal")
			doc := map[string]any{"auth": map[string]any{"type": acpMethod}}
			gcp := map[string]any{}
			if gp := strVal(cfg, "gcp_project", ""); gp != "" {
				gcp["project"] = gp
			}
			if gl := strVal(cfg, "gcp_location", ""); gl != "" {
				gcp["location"] = gl
			}
			if len(gcp) > 0 {
				doc["gcp"] = gcp
			}
			b, _ := json.MarshalIndent(doc, "", "  ")
			b = append(b, '\n')
			_ = writePrivateFileIfChanged(filepath.Join(eff, "antigravity-acp", "settings.json"), b, 0o600)
		}
	}

	envSet := map[string]string{}
	envSecrets := map[string]string{}
	envUnset := append(append([]string(nil), agyScrubVars...), strSliceVal(spec, "unset_env")...)
	if mode != "direct" || strings.TrimSpace(strVal(spec, "home_path", "")) != "" {
		envSet["GEMINI_HOME"] = eff
	}
	if bu := strVal(cfg, "base_url", ""); bu != "" {
		envSet["GOOGLE_GEMINI_BASE_URL"] = bu
	}
	if gp := strVal(cfg, "gcp_project", ""); gp != "" {
		envSet["GOOGLE_CLOUD_PROJECT"] = gp
	}
	if gl := strVal(cfg, "gcp_location", ""); gl != "" {
		envSet["GOOGLE_CLOUD_LOCATION"] = gl
	}
	if cf := strVal(cfg, "credentials_file", ""); cf != "" {
		envSet["GOOGLE_APPLICATION_CREDENTIALS"] = secrets.ExpandPath(cf)
	}
	tok := strings.TrimSpace(strVal(cfg, "token_secret", ""))
	switch authMode {
	case "api":
		if tok != "" {
			envSecrets["GEMINI_API_KEY"] = tok
		}
	case "adc":
		envSet["AGY_ADC_AUTH"] = "1"
		envSet["GOOGLE_GENAI_USE_VERTEXAI"] = "true"
	case "acp":
		envSet["AGY_ACP_FORCE_FILE_STORAGE"] = "1"
		m := strVal(cfg, "acp_auth_method", "oauth-personal")
		if m == "gemini-api-key" && tok != "" {
			envSecrets["GEMINI_API_KEY"] = tok
		} else if m == "agent-platform" && tok != "" {
			envSecrets["GOOGLE_API_KEY"] = tok
		}
	}
	for k, v := range mapVal(spec, "env") {
		envSet[k] = fmt.Sprint(v)
	}
	for k, v := range mapVal(spec, "secrets") {
		envSecrets[k] = fmt.Sprint(v)
	}

	extraArgs := append([]string(nil), strSliceVal(spec, "launch_args")...)
	if mdl := strVal(cfg, "model", ""); mdl != "" {
		extraArgs = append(extraArgs, "--model", mdl)
	}

	binName := strVal(spec, "binary", d.DefaultBinary())
	var bin string
	if dryRun {
		bin = TryResolveBinary(binName, callerEnv)
	} else {
		var err error
		bin, err = ResolveBinary(binName, callerEnv)
		if err != nil {
			return nil, err
		}
	}
	argv := append([]string{bin}, extraArgs...)
	argv = append(argv, userArgs...)
	pid := strVal(spec, "id", "")
	return &LaunchPlan{
		ProfileID:     pid,
		ProfileName:   model.DeriveProfileName(pid, d.Name(), strVal(spec, "profile", "")),
		Harness:       d.Name(),
		Binary:        bin,
		Argv:          argv,
		EnvSet:        envSet,
		EnvSecretRefs: envSecrets,
		EnvUnset:      uniqueSorted(envUnset),
		HomeInfo:      homeInfo,
	}, nil
}

func (d *agyDriver) LoginArgs(_ map[string]any) []string { return nil }
func (d *agyDriver) ProbeArgs(_ map[string]any) []string { return nil }

// --- OpenClaw Driver ----------------------------------------------------------------------------

type openclawDriver struct{}

var openclawScrubVars = []string{
	"OPENCLAW_STATE_DIR", "OPENCLAW_CONFIG_PATH", "OPENCLAW_PROFILE", "OPENCLAW_GATEWAY_TOKEN",
}

func (d *openclawDriver) Name() string              { return "openclaw" }
func (d *openclawDriver) DefaultBinary() string     { return "openclaw" }
func (d *openclawDriver) DefaultSharedHome() string { return "~/.openclaw" }
func (d *openclawDriver) AuthModes() []string       { return []string{"native", "profile", "gateway", "api"} }
func (d *openclawDriver) ResolveHome(spec map[string]any) (string, string, string) {
	return baseResolveHome(d, spec)
}

func (d *openclawDriver) RequiredSecrets(spec map[string]any) map[string]string {
	out := baseRequiredSecrets(spec)
	cfg := mapVal(spec, "openclaw")
	if tok := strings.TrimSpace(strVal(cfg, "token_secret", "")); tok != "" {
		ek := strVal(cfg, "env_key", "OPENCLAW_GATEWAY_TOKEN")
		out[ek] = tok
	}
	return out
}

func (d *openclawDriver) BuildPlan(spec map[string]any, userArgs []string, callerEnv map[string]string, dryRun bool) (*LaunchPlan, error) {
	mode, shared, eff := d.ResolveHome(spec)
	authMode := strVal(spec, "auth_mode", "native")
	cfg := mapVal(spec, "openclaw")
	shadowCfg := mapVal(spec, "shadow")
	profName := strings.TrimSpace(strVal(cfg, "profile_name", ""))
	groupName := strings.TrimSpace(strVal(spec, "session_group", ""))
	groupRoot := GroupHarnessDir(groupName, "openclaw")
	groupDirs := []string{"sessions", "memory", "workspace"}

	var homeInfo map[string]any
	if mode == "shadow" {
		priv := append([]string{"credentials", "auth.json", "devices", "openclaw.json.lock"}, strSliceVal(shadowCfg, "private")...)
		if dryRun {
			homeInfo = map[string]any{"mode": "shadow", "shared_home": shared, "effective_home": eff, "private": uniqueSorted(priv)}
			if groupName != "" {
				homeInfo["session_group"] = groupName
				homeInfo["group_root"] = groupRoot
				homeInfo["group_entries"] = groupDirs
			}
		} else {
			var err error
			homeInfo, err = MaterializeShadowHome(shared, eff, ShadowOpts{
				KnownSharedDirs: []string{"skills", "extensions"},
				PrivateNames:    priv,
				ShareAllOther:   true,
				GroupRoot:       groupRoot,
				GroupEntries:    groupDirs,
				KnownGroupDirs:  groupDirs,
			})
			if err != nil {
				return nil, err
			}
			homeInfo["mode"] = "shadow"
			homeInfo["effective_home"] = eff
			if groupName != "" {
				homeInfo["session_group"] = groupName
			}
		}
	} else {
		if mode == "isolated" && !dryRun {
			if err := ensurePrivateDir(eff); err != nil {
				return nil, err
			}
		}
		homeInfo = map[string]any{"mode": mode, "shared_home": shared, "effective_home": eff}
	}

	envSet := map[string]string{}
	envSecrets := map[string]string{}
	envUnset := append(append([]string(nil), openclawScrubVars...), strSliceVal(spec, "unset_env")...)
	if mode == "shadow" || mode == "isolated" || strings.TrimSpace(strVal(spec, "home_path", "")) != "" {
		envSet["OPENCLAW_STATE_DIR"] = eff
		envSet["OPENCLAW_CONFIG_PATH"] = filepath.Join(eff, "openclaw.json")
	}
	if cp := strings.TrimSpace(strVal(cfg, "config_path", "")); cp != "" {
		envSet["OPENCLAW_CONFIG_PATH"] = secrets.ExpandPath(cp)
	}
	if gu := strings.TrimSpace(strVal(cfg, "gateway_url", "")); gu != "" {
		envSet["OPENCLAW_GATEWAY_URL"] = gu
	}
	if tok := strings.TrimSpace(strVal(cfg, "token_secret", "")); tok != "" {
		ek := strVal(cfg, "env_key", "OPENCLAW_GATEWAY_TOKEN")
		envSecrets[ek] = tok
	}
	for k, v := range mapVal(spec, "env") {
		envSet[k] = fmt.Sprint(v)
	}
	for k, v := range mapVal(spec, "secrets") {
		envSecrets[k] = fmt.Sprint(v)
	}

	var extraArgs []string
	if profName != "" && authMode == "profile" && mode == "direct" {
		extraArgs = append(extraArgs, "--profile", profName)
	}
	extraArgs = append(extraArgs, strSliceVal(spec, "launch_args")...)

	binName := strVal(spec, "binary", d.DefaultBinary())
	var bin string
	if dryRun {
		bin = TryResolveBinary(binName, callerEnv)
	} else {
		var err error
		bin, err = ResolveBinary(binName, callerEnv)
		if err != nil {
			return nil, err
		}
	}
	argv := append([]string{bin}, extraArgs...)
	argv = append(argv, userArgs...)
	pid := strVal(spec, "id", "")
	return &LaunchPlan{
		ProfileID:     pid,
		ProfileName:   model.DeriveProfileName(pid, d.Name(), strVal(spec, "profile", "")),
		Harness:       d.Name(),
		Binary:        bin,
		Argv:          argv,
		EnvSet:        envSet,
		EnvSecretRefs: envSecrets,
		EnvUnset:      uniqueSorted(envUnset),
		HomeInfo:      homeInfo,
	}, nil
}

func (d *openclawDriver) LoginArgs(_ map[string]any) []string { return []string{"configure"} }
func (d *openclawDriver) ProbeArgs(_ map[string]any) []string { return []string{"doctor"} }
