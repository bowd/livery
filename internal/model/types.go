package model

import (
	"path/filepath"
	"strings"
)

var (
	Providers       = []string{"claude", "codex"}
	SkillshareModes = []string{"merge", "symlink", "copy"}
	Scopes          = []string{"user", "host", "role-pointer"}
	LinkModes       = []string{"symlink", "copy"}
	Harnesses       = []string{"claude", "codex", "opencode", "openclaw", "agy"}
	LauncherModes   = []string{"direct", "shadow", "isolated"}
	Transports      = []string{"stdio", "http"}
	EntryKinds      = []string{"codex-mcp", "claude-mcp", "hook", "codex-setting", "herdr-program", "t3-provider"}
	WatchedPaths    = []string{"prompts", "profiles", "skills", "projects", "mcp", "hooks", "launchers", "inventory.toml"}
	SecretishKeys   = []string{"env", "headers", "http_headers", "token", "secret", "password", "authorization", "api_key", "apikey"}

	DefaultHookFiles = map[string]string{
		"claude": "~/.claude/settings.json",
		"codex":  "~/.codex/hooks.json",
	}
)

type ConfigError struct {
	Msg string
}

func (e *ConfigError) Error() string { return e.Msg }

func Errf(format string, args ...any) error {
	return &ConfigError{Msg: sprintf(format, args...)}
}

// Target describes one OS-user target from inventory.toml merged with [defaults].
type Target struct {
	ID                  string
	SSH                 string
	Providers           []string
	Profiles            []string
	Roles               []string
	External            []string
	Adopt               []string
	Add                 []string
	Remove              []string
	SkillRoot           string
	ProviderSkillDirs   map[string]string
	LinkMode            map[string]string
	BackupRoot          string
	ProviderPromptFiles map[string]string
	Home                string // test-only override; only with ssh = "local"
	Python              string
	Claude              string
	CodexConfig         string
	ProviderHookFiles   map[string]string
	HookRoot            string
	HookStubDays        int
	CodexHooksFeature   *bool

	// Launchers & Livery CLI
	LauncherRoot       string
	BinDir             string
	LauncherCLIAlias   string
	InstallLauncherCLI bool
	EmitShims          bool // default false in Livery (`liv <harness> <profile>` replaces per-profile shims)

	// Optional Herdr & T3 Code integration
	SyncHerdrMod       bool
	SyncHerdrPrograms  bool
	HerdrProgramsFile  string
	SyncHerdrTemplates bool
	HerdrTemplatesDir  string
	SyncT3Providers    bool
	T3SettingsFile     string

	// Stock runkids/skillshare distribution settings (single skill backend in Livery)
	SkillshareRoot        string
	SkillshareBin         string
	SkillshareTargets     []string
	SkillshareMode        string
	SkillshareTargetModes map[string]string
	SkillshareTargetPaths map[string]string
	SkillshareManageMCP   bool
	SkillshareManageHooks bool
}

func (t *Target) IsLocal() bool {
	return t.SSH == "local"
}

func (t *Target) SkillExternal() []string {
	var out []string
	for _, g := range t.External {
		if !hasAnyPrefix(g, "mcp:", "hook:", "launcher:", "herdr-program:", "t3-provider:") {
			out = append(out, g)
		}
	}
	return out
}

func (t *Target) matchExternalPrefix(prefix, val string) bool {
	for _, g := range t.External {
		if strings.HasPrefix(g, prefix) {
			pat := g[len(prefix):]
			if ok, _ := filepath.Match(pat, val); ok {
				return true
			}
		}
	}
	return false
}

func (t *Target) ExternalMCP(name string) bool       { return t.matchExternalPrefix("mcp:", name) }
func (t *Target) ExternalHook(cmd string) bool       { return t.matchExternalPrefix("hook:", cmd) }
func (t *Target) ExternalLauncher(shim string) bool  { return t.matchExternalPrefix("launcher:", shim) }
func (t *Target) ExternalHerdrProgram(k string) bool { return t.matchExternalPrefix("herdr-program:", k) }
func (t *Target) ExternalT3Provider(id string) bool  { return t.matchExternalPrefix("t3-provider:", id) }

func (t *Target) EffectiveSkillshareTargets() []string {
	raw := t.SkillshareTargets
	if len(raw) == 0 {
		raw = t.Providers
	}
	var out []string
	seen := map[string]bool{}
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

type Fragment struct {
	ID        string
	Path      string
	Providers []string
	Scope     string
	Why       string
	Since     string
	Body      string
}

type Skill struct {
	Ref       string
	Name      string
	Src       string
	Providers []string
	Submodule string
}

type Overlay struct {
	Target     string
	Slug       string
	Dir        string
	Path       string
	Why        string
	Files      []string
	Skills     []string
	MCP        []string
	CodexTrust bool
}

type Profile struct {
	ID          string
	Description string
	Extends     []string
	Fragments   []string
	Skills      []string
	MCP         []string
	Hooks       []string
	Agents      []string
	Extras      []string
	Plugins     []string
	Launchers   []string
}

type LauncherSpec struct {
	ID           string
	Profile      string // short profile name under harness, e.g. "gateway" for id="claude-gateway"
	SessionGroup string // optional memory/session pool name, e.g. "work", "personal"
	Path         string
	Why          string
	Harness      string
	Shim         string
	Raw          map[string]any
}

type McpServer struct {
	Name      string
	Path      string
	Why       string
	Providers []string
	Spec      map[string]any
	Overrides map[string]map[string]any
}

func (m *McpServer) ForProvider(prov string) map[string]any {
	out := make(map[string]any, len(m.Spec)+4)
	for k, v := range m.Spec {
		out[k] = v
	}
	for k, v := range m.Overrides[prov] {
		out[k] = v
	}
	return out
}

type HookSpec struct {
	ID        string
	Path      string
	Why       string
	Providers []string
	Spec      map[string]any
	Overrides map[string]map[string]any
}

func (h *HookSpec) ForProvider(prov string) map[string]any {
	out := make(map[string]any, len(h.Spec)+4)
	for k, v := range h.Spec {
		out[k] = v
	}
	for k, v := range h.Overrides[prov] {
		out[k] = v
	}
	return out
}

type LockEntry struct {
	Target       string   `toml:"target" json:"target"`
	Path         string   `toml:"path" json:"path"`
	Kind         string   `toml:"kind" json:"kind"`
	SHA256       string   `toml:"sha256" json:"sha256"`
	Sources      []string `toml:"sources" json:"sources"`
	LinkTarget   string   `toml:"link_target,omitempty" json:"link_target,omitempty"`
	Commit       string   `toml:"commit,omitempty" json:"commit,omitempty"`
	AppliedAt    string   `toml:"applied_at,omitempty" json:"applied_at,omitempty"`
	Role         string   `toml:"role,omitempty" json:"role,omitempty"`
	File         string   `toml:"file,omitempty" json:"file,omitempty"`
	Entry        string   `toml:"entry,omitempty" json:"entry,omitempty"`
	Provider     string   `toml:"provider,omitempty" json:"provider,omitempty"`
	Event        string   `toml:"event,omitempty" json:"event,omitempty"`
	Command      string   `toml:"command,omitempty" json:"command,omitempty"`
	RetireAfter  string   `toml:"retire_after,omitempty" json:"retire_after,omitempty"`
	SubmoduleSHA string   `toml:"submodule_sha,omitempty" json:"submodule_sha,omitempty"`
}

type LockTarget struct {
	Commit string      `toml:"commit" json:"commit"`
	At     string      `toml:"at" json:"at"`
	Items  []LockEntry `toml:"item" json:"item"`
}

type Repo struct {
	Root          string
	Targets       []*Target
	Profiles      map[string]*Profile
	Fragments     map[string]*Fragment
	FragmentOrder []string
	Overlays      []*Overlay
	MCP           map[string]*McpServer
	Hooks         map[string]*HookSpec
	Launchers     map[string]*LauncherSpec
}

type CatalogSkill struct {
	Ref         string   `json:"ref"`
	Name        string   `json:"name"`
	Source      string   `json:"source"`
	Providers   []string `json:"providers"`
	Description string   `json:"description"`
	Bytes       int      `json:"bytes"`
	Words       int      `json:"words"`
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
