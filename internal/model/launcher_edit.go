package model

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type LauncherBlueprint struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Harness     string `json:"harness"`
	AuthMode    string `json:"auth_mode"`
	Mode        string `json:"mode"`
	BaseURLHint string `json:"base_url_hint,omitempty"`
	SecretHint  string `json:"secret_hint,omitempty"`
	EnvKeyHint  string `json:"env_key_hint,omitempty"`
}

func LauncherBlueprints() []LauncherBlueprint {
	return []LauncherBlueprint{
		{
			ID: "claude-gateway", Title: "Claude · Gateway",
			Description: "Anthropic-compatible gateway (LiteLLM / company gateway) sharing skills while isolating auth",
			Harness: "claude", AuthMode: "gateway", Mode: "shadow",
			BaseURLHint: "https://gateway.example.com", SecretHint: "store:company-gateway-api-key",
		},
		{
			ID: "claude-oauth", Title: "Claude · OAuth account",
			Description: "Second simultaneous Claude subscription (personal vs work) with isolated credentials",
			Harness: "claude", AuthMode: "oauth", Mode: "shadow",
		},
		{
			ID: "claude-api", Title: "Claude · API key",
			Description: "Direct Anthropic API key via secret store / apiKeyHelper",
			Harness: "claude", AuthMode: "api_key_helper", Mode: "shadow",
			SecretHint: "store:anthropic-api-key",
		},
		{
			ID: "codex-custom", Title: "Codex · Custom provider",
			Description: "OpenAI Responses-compatible provider (Z.AI, Gateway, Qwen, OpenRouter) with child env scrubbing",
			Harness: "codex", AuthMode: "custom_provider", Mode: "shadow",
			BaseURLHint: "https://api.z.ai/api/v1", SecretHint: "store:zai-api-key", EnvKeyHint: "ZAI_API_KEY",
		},
		{
			ID: "codex-chatgpt", Title: "Codex · ChatGPT OAuth",
			Description: "Separate ChatGPT account with file-backed auth isolation",
			Harness: "codex", AuthMode: "chatgpt", Mode: "shadow",
		},
		{
			ID: "opencode-api", Title: "OpenCode · Gateway / API",
			Description: "Standalone OpenCode profile with isolated config & shared sessions",
			Harness: "opencode", AuthMode: "api", Mode: "shadow",
			SecretHint: "store:company-gateway-api-key", EnvKeyHint: "COMPANY_GATEWAY_API_KEY",
		},
		{
			ID: "openclaw-profile", Title: "OpenClaw · Profile",
			Description: "OpenClaw agent profile with optional memory/session group or --profile isolation",
			Harness: "openclaw", AuthMode: "profile", Mode: "shadow",
		},
		{
			ID: "agy-oauth", Title: "Antigravity · OAuth",
			Description: "Antigravity CLI profile with isolated GEMINI_HOME",
			Harness: "agy", AuthMode: "oauth", Mode: "shadow",
		},
	}
}

type LauncherProfileInput struct {
	ID              string   `json:"id"`
	Harness         string   `json:"harness"`
	Profile         string   `json:"profile"`
	Description     string   `json:"description"`
	Why             string   `json:"why"`
	Mode            string   `json:"mode"`
	AuthMode        string   `json:"auth_mode"`
	SessionGroup    string   `json:"session_group"`
	SharedHome      string   `json:"shared_home"`
	BaseURL         string   `json:"base_url"`
	Model           string   `json:"model"`
	TokenSecret     string   `json:"token_secret"`
	ProviderID      string   `json:"provider_id"`
	ProviderName    string   `json:"provider_name"`
	EnvKey          string   `json:"env_key"`
	OpenClawProfile string   `json:"openclaw_profile"`
	HerdrEnabled    *bool    `json:"herdr_enabled,omitempty"`
	HerdrName       string   `json:"herdr_name"`
	HerdrIcon       string   `json:"herdr_icon"`
	T3Enabled       *bool    `json:"t3_enabled,omitempty"`
	T3InstanceID    string   `json:"t3_instance_id"`
	T3DisplayName   string   `json:"t3_display_name"`
	T3AccentColor   string   `json:"t3_accent_color"`
	T3CustomModels  []string `json:"t3_custom_models"`
	AttachProfile   string   `json:"attach_profile,omitempty"` // e.g. "owner-workstation" or "base"
}

func defaultIconForHarness(h string) string {
	switch h {
	case "claude":
		return "icons/claude.svg"
	case "codex":
		return "icons/codex.svg"
	case "opencode":
		return "icons/opencode.svg"
	case "agy":
		return "icons/antigravity.svg"
	default:
		return "icons/agent.svg"
	}
}

func defaultSharedHomeForHarness(h string) string {
	switch h {
	case "claude":
		return "~/.claude"
	case "codex":
		return "~/.codex"
	case "opencode":
		return "~/.config/opencode"
	case "openclaw":
		return "~/.openclaw"
	case "agy":
		return "~/.gemini"
	default:
		return ""
	}
}

// SaveLauncherProfile creates or updates launchers/<id>.toml cleanly, validates via CheckRepo (rolling back on error),
// and optionally attaches the launcher to a repo profile in profiles/<attach>.toml.
func SaveLauncherProfile(repoRoot string, in LauncherProfileInput) (string, error) {
	in.Harness = strings.TrimSpace(strings.ToLower(in.Harness))
	if !containsStr(Harnesses, in.Harness) {
		return "", Errf("harness must be one of %v, not %q", Harnesses, in.Harness)
	}
	in.Profile = strings.TrimSpace(in.Profile)
	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		if in.Profile == "" {
			return "", Errf("profile name (or id) is required")
		}
		if strings.HasPrefix(in.Profile, in.Harness+"-") {
			in.ID = in.Profile
		} else {
			in.ID = in.Harness + "-" + in.Profile
		}
	}
	if !slugRe.MatchString(in.ID) {
		return "", Errf("invalid launcher id %q (use letters, digits, ., _, -)", in.ID)
	}
	if in.Profile == "" {
		in.Profile = DeriveProfileName(in.ID, in.Harness, "")
	}
	if in.Mode == "" {
		in.Mode = "shadow"
	}
	if !containsStr(LauncherModes, in.Mode) {
		return "", Errf("mode must be one of %v", LauncherModes)
	}
	in.SessionGroup = strings.TrimSpace(in.SessionGroup)
	if in.SessionGroup != "" && !slugRe.MatchString(in.SessionGroup) {
		return "", Errf("invalid session_group %q (use letters, digits, ., _, -)", in.SessionGroup)
	}
	if in.Description == "" {
		in.Description = fmt.Sprintf("%s (%s)", strings.Title(in.Harness), in.Profile)
	}
	if in.Why == "" {
		in.Why = fmt.Sprintf("Managed %s launch profile (%s).", in.Harness, in.Profile)
	}
	if in.TokenSecret != "" && !strings.Contains(in.TokenSecret, ":") {
		in.TokenSecret = "store:" + in.TokenSecret
	}

	dest := filepath.Join(repoRoot, "launchers", in.ID+".toml")
	var orig []byte
	hadOrig := false
	if b, err := os.ReadFile(dest); err == nil {
		orig = b
		hadOrig = true
	}

	// Merge existing fields if updating an existing launcher so advanced settings aren't lost
	doc := map[string]any{}
	if hadOrig {
		if existing, err := LoadTOMLMap(dest); err == nil {
			doc = existing
		}
	}
	doc["id"] = in.ID
	doc["profile"] = in.Profile
	doc["why"] = in.Why
	doc["description"] = in.Description
	doc["harness"] = in.Harness
	doc["mode"] = in.Mode
	if in.AuthMode != "" {
		doc["auth_mode"] = in.AuthMode
	}
	if in.SessionGroup != "" {
		doc["session_group"] = in.SessionGroup
	} else {
		delete(doc, "session_group")
	}
	if in.SharedHome != "" {
		doc["shared_home"] = in.SharedHome
	} else if _, has := doc["shared_home"]; !has && in.Mode == "shadow" {
		if def := defaultSharedHomeForHarness(in.Harness); def != "" {
			doc["shared_home"] = def
		}
	}

	// Harness-specific subtable
	hcfg, _ := doc[in.Harness].(map[string]any)
	if hcfg == nil {
		hcfg = map[string]any{}
	}
	switch in.Harness {
	case "claude":
		if in.BaseURL != "" {
			hcfg["base_url"] = in.BaseURL
		}
		if in.TokenSecret != "" {
			hcfg["token_secret"] = in.TokenSecret
		}
		if in.Model != "" {
			hcfg["model"] = in.Model
		}
	case "codex":
		if in.ProviderID != "" {
			hcfg["provider_id"] = in.ProviderID
		} else if in.AuthMode == "custom_provider" && asString(hcfg, "provider_id", "") == "" {
			hcfg["provider_id"] = in.Profile
		}
		if in.ProviderName != "" {
			hcfg["provider_name"] = in.ProviderName
		}
		if in.BaseURL != "" {
			hcfg["base_url"] = in.BaseURL
		}
		if in.AuthMode == "custom_provider" && asString(hcfg, "wire_api", "") == "" {
			hcfg["wire_api"] = "responses"
			hcfg["requires_openai_auth"] = false
			hcfg["scrub_child_env"] = true
		}
		if in.EnvKey != "" {
			hcfg["env_key"] = in.EnvKey
		}
		if in.TokenSecret != "" {
			hcfg["token_secret"] = in.TokenSecret
		}
		if in.Model != "" {
			hcfg["model"] = in.Model
		}
	case "opencode":
		if in.Model != "" {
			hcfg["model"] = in.Model
		}
		if _, ok := hcfg["standalone"]; !ok {
			hcfg["standalone"] = true
		}
		if in.TokenSecret != "" && in.EnvKey != "" {
			secs, _ := doc["secrets"].(map[string]any)
			if secs == nil {
				secs = map[string]any{}
			}
			secs[in.EnvKey] = in.TokenSecret
			doc["secrets"] = secs
		}
	case "openclaw":
		if in.OpenClawProfile != "" {
			hcfg["profile_name"] = in.OpenClawProfile
		} else if in.AuthMode == "profile" && asString(hcfg, "profile_name", "") == "" {
			hcfg["profile_name"] = in.Profile
		}
		if in.BaseURL != "" {
			hcfg["gateway_url"] = in.BaseURL
		}
		if in.EnvKey != "" {
			hcfg["env_key"] = in.EnvKey
		}
		if in.TokenSecret != "" {
			hcfg["token_secret"] = in.TokenSecret
		}
	case "agy":
		if in.BaseURL != "" {
			hcfg["base_url"] = in.BaseURL
		}
		if in.TokenSecret != "" {
			hcfg["token_secret"] = in.TokenSecret
		}
		if in.Model != "" {
			hcfg["model"] = in.Model
		}
	}
	if len(hcfg) > 0 {
		doc[in.Harness] = hcfg
	}

	// [herdr] section
	herdrTbl, _ := doc["herdr"].(map[string]any)
	if herdrTbl == nil {
		herdrTbl = map[string]any{}
	}
	if in.HerdrEnabled != nil {
		herdrTbl["enabled"] = *in.HerdrEnabled
	}
	if in.HerdrName != "" {
		herdrTbl["name"] = in.HerdrName
	} else if _, has := herdrTbl["name"]; !has {
		herdrTbl["name"] = in.Description
	}
	if in.HerdrIcon != "" {
		herdrTbl["icon"] = in.HerdrIcon
	} else if _, has := herdrTbl["icon"]; !has {
		herdrTbl["icon"] = defaultIconForHarness(in.Harness)
	}
	if in.Harness == "codex" {
		if _, has := herdrTbl["no_daemon"]; !has {
			herdrTbl["no_daemon"] = true
		}
	}
	doc["herdr"] = herdrTbl

	// [t3] section (for harnesses T3 supports)
	if in.Harness != "openclaw" || in.T3Enabled != nil || in.T3InstanceID != "" {
		t3Tbl, _ := doc["t3"].(map[string]any)
		if t3Tbl == nil {
			t3Tbl = map[string]any{}
		}
		if in.T3Enabled != nil {
			t3Tbl["enabled"] = *in.T3Enabled
		}
		if in.T3InstanceID != "" {
			t3Tbl["instance_id"] = in.T3InstanceID
		} else if _, has := t3Tbl["instance_id"]; !has && in.Harness != "openclaw" {
			t3Tbl["instance_id"] = strings.ReplaceAll(in.ID, "-", "_")
		}
		if in.T3DisplayName != "" {
			t3Tbl["display_name"] = in.T3DisplayName
		} else if _, has := t3Tbl["display_name"]; !has && in.Harness != "openclaw" {
			t3Tbl["display_name"] = in.Description
		}
		if in.T3AccentColor != "" {
			t3Tbl["accent_color"] = in.T3AccentColor
		}
		if in.T3CustomModels != nil {
			if len(in.T3CustomModels) == 0 {
				delete(t3Tbl, "custom_models")
			} else {
				t3Tbl["custom_models"] = in.T3CustomModels
			}
		}
		if len(t3Tbl) > 0 {
			doc["t3"] = t3Tbl
		}
	}

	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	if err := atomicWriteFile(dest, []byte(DumpsTOML(doc))); err != nil {
		return "", err
	}
	if errs := CheckRepo(repoRoot); len(errs) > 0 {
		if hadOrig {
			_ = atomicWriteFile(dest, orig)
		} else {
			_ = os.Remove(dest)
		}
		return "", Errf("launcher profile rejected: %s", strings.Join(errs, "; "))
	}

	if in.AttachProfile != "" {
		if err := AttachLauncherToProfile(repoRoot, in.AttachProfile, in.ID); err != nil {
			return in.ID, err
		}
	}
	return in.ID, nil
}

// UpdateLauncherGroup updates or clears `session_group` in launchers/<id>.toml.
func UpdateLauncherGroup(repoRoot, id, group string) error {
	group = strings.TrimSpace(group)
	if group == "none" || group == "default" || group == "-" {
		group = ""
	}
	if group != "" && !slugRe.MatchString(group) {
		return Errf("invalid session_group %q", group)
	}
	dest := filepath.Join(repoRoot, "launchers", id+".toml")
	orig, err := os.ReadFile(dest)
	if err != nil {
		return Errf("launcher %q not found at %s", id, dest)
	}
	doc, err := LoadTOMLMap(dest)
	if err != nil {
		return err
	}
	if group == "" {
		delete(doc, "session_group")
	} else {
		doc["session_group"] = group
	}
	if err := atomicWriteFile(dest, []byte(DumpsTOML(doc))); err != nil {
		return err
	}
	if errs := CheckRepo(repoRoot); len(errs) > 0 {
		_ = atomicWriteFile(dest, orig)
		return Errf("session_group update rejected: %s", strings.Join(errs, "; "))
	}
	return nil
}

// AttachLauncherToProfile adds a launcher ID to profiles/<profileID>.toml's `launchers = [...]`.
func AttachLauncherToProfile(repoRoot, profileID, launcherID string) error {
	pPath := filepath.Join(repoRoot, "profiles", profileID+".toml")
	orig, err := os.ReadFile(pPath)
	if err != nil {
		return Errf("profile %q not found", profileID)
	}
	data, err := LoadTOMLMap(pPath)
	if err != nil {
		return err
	}
	cur := asStringSlice(data, "launchers")
	if containsStr(cur, launcherID) {
		return nil
	}
	next := append(cur, launcherID)
	lines := strings.Split(strings.TrimRight(string(orig), "\n"), "\n")
	replaced := false
	for i := 0; i < len(lines); i++ {
		if m := kvLineRe.FindStringSubmatch(lines[i]); m != nil && m[2] == "launchers" {
			end := i + 1
			depth := bracketDelta(m[3])
			for depth > 0 && end < len(lines) {
				depth += bracketDelta(lines[end])
				end++
			}
			repl := fmt.Sprintf("%slaunchers = %s", m[1], TomlScalar(next))
			lines = append(lines[:i], append([]string{repl}, lines[end:]...)...)
			replaced = true
			break
		}
	}
	if !replaced {
		insertAt := len(lines)
		for i, ln := range lines {
			if headerLineRe.MatchString(ln) {
				insertAt = i
				break
			}
		}
		line := fmt.Sprintf("launchers = %s", TomlScalar(next))
		lines = append(lines[:insertAt], append([]string{line}, lines[insertAt:]...)...)
	}
	newText := strings.Join(lines, "\n") + "\n"
	if err := atomicWriteFile(pPath, []byte(newText)); err != nil {
		return err
	}
	if errs := CheckRepo(repoRoot); len(errs) > 0 {
		_ = atomicWriteFile(pPath, orig)
		return Errf("attach launcher rejected: %s", strings.Join(errs, "; "))
	}
	return nil
}

// DeleteLauncherProfile removes launchers/<id>.toml and strips it from any profiles/*.toml.
func DeleteLauncherProfile(repoRoot, id string) error {
	dest := filepath.Join(repoRoot, "launchers", id+".toml")
	if _, err := os.Stat(dest); err != nil {
		return Errf("launcher %q not found", id)
	}
	// Strip references in profiles/*.toml first
	pdir := filepath.Join(repoRoot, "profiles")
	if entries, err := os.ReadDir(pdir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
				continue
			}
			pp := filepath.Join(pdir, e.Name())
			data, err := LoadTOMLMap(pp)
			if err != nil {
				continue
			}
			cur := asStringSlice(data, "launchers")
			if !containsStr(cur, id) {
				continue
			}
			var filtered []string
			for _, x := range cur {
				if x != id {
					filtered = append(filtered, x)
				}
			}
			orig, _ := os.ReadFile(pp)
			lines := strings.Split(strings.TrimRight(string(orig), "\n"), "\n")
			for i := 0; i < len(lines); i++ {
				if m := kvLineRe.FindStringSubmatch(lines[i]); m != nil && m[2] == "launchers" {
					end := i + 1
					depth := bracketDelta(m[3])
					for depth > 0 && end < len(lines) {
						depth += bracketDelta(lines[end])
						end++
					}
					repl := fmt.Sprintf("%slaunchers = %s", m[1], TomlScalar(nonNilSlice(filtered)))
					lines = append(lines[:i], append([]string{repl}, lines[end:]...)...)
					break
				}
			}
			_ = atomicWriteFile(pp, []byte(strings.Join(lines, "\n")+"\n"))
		}
	}
	return os.Remove(dest)
}

func SortStringsCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
