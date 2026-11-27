package model

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

func sprintf(f string, args ...any) string { return fmt.Sprintf(f, args...) }

// DefaultDataHome returns the default XDG control-plane directory (~/.local/share/livery).
func DefaultDataHome() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		return filepath.Join(ExpandHome(xdg), "livery")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "livery")
}

// FindRepoRoot resolves the livery control-plane home via explicit flag, LIVERY_HOME/LIVERY_REPO,
// cwd upward walk, default XDG ~/.local/share/livery, or saved ~/.config/livery/repo.
func FindRepoRoot(explicit string) (string, error) {
	if explicit != "" {
		abs, err := filepath.Abs(ExpandHome(explicit))
		if err != nil {
			return "", err
		}
		if isRepoDir(abs) {
			return abs, nil
		}
		return "", Errf("%s: missing inventory.toml", abs)
	}
	for _, envKey := range []string{"LIVERY_HOME", "LIVERY_REPO"} {
		if env := strings.TrimSpace(os.Getenv(envKey)); env != "" {
			abs, _ := filepath.Abs(ExpandHome(env))
			if isRepoDir(abs) {
				return abs, nil
			}
		}
	}
	// Walk upward from cwd (allows working inside a custom data repo or test fixture)
	if cwd, err := os.Getwd(); err == nil {
		cur := cwd
		for {
			if isRepoDir(cur) {
				return cur, nil
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	// Default XDG control plane (~/.local/share/livery)
	if def := DefaultDataHome(); isRepoDir(def) {
		return def, nil
	}
	// Saved controller config (~/.config/livery/repo)
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, ".config", "livery", "repo"),
		filepath.Join(home, ".config", "agent-config", "repo"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			cand := strings.TrimSpace(string(b))
			if cand != "" {
				abs, _ := filepath.Abs(ExpandHome(cand))
				if isRepoDir(abs) {
					return abs, nil
				}
			}
		}
	}
	return "", Errf("no livery data home found (expected %s, set LIVERY_HOME, or pass --repo)", DefaultDataHome())
}

func isRepoDir(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "inventory.toml"))
	return err == nil && !st.IsDir()
}

func ExpandHome(p string) string {
	if p == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func LoadTOMLMap(path string) (map[string]any, error) {
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, Errf("%s: %v", path, err)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	return raw, nil
}

func LoadRepo(root string) (*Repo, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	r := &Repo{
		Root:      absRoot,
		Profiles:  map[string]*Profile{},
		Fragments: map[string]*Fragment{},
		MCP:       map[string]*McpServer{},
		Hooks:     map[string]*HookSpec{},
		Launchers: map[string]*LauncherSpec{},
	}
	if err := r.loadInventory(); err != nil {
		return nil, err
	}
	if err := r.loadProfiles(); err != nil {
		return nil, err
	}
	if err := r.loadFragments(); err != nil {
		return nil, err
	}
	if err := r.loadOverlays(); err != nil {
		return nil, err
	}
	if err := r.loadMCP(); err != nil {
		return nil, err
	}
	if err := r.loadHooks(); err != nil {
		return nil, err
	}
	if err := r.loadLaunchers(); err != nil {
		return nil, err
	}
	return r, nil
}

func asString(m map[string]any, k, def string) string {
	if v, ok := m[k]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return def
}

func asBool(m map[string]any, k string, def bool) bool {
	if v, ok := m[k]; ok && v != nil {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func asInt(m map[string]any, k string, def int) int {
	if v, ok := m[k]; ok && v != nil {
		switch x := v.(type) {
		case int64:
			return int(x)
		case int:
			return x
		}
	}
	return def
}

func asStringSlice(m map[string]any, k string) []string {
	v, ok := m[k]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func asStringMap(m map[string]any, k string) map[string]string {
	out := map[string]string{}
	v, ok := m[k]
	if !ok || v == nil {
		return out
	}
	sub, ok := v.(map[string]any)
	if !ok {
		return out
	}
	for sk, sv := range sub {
		out[sk] = fmt.Sprint(sv)
	}
	return out
}

func mergeStringMaps(base, over map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func mergeUnique(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func containsStr(list []string, val string) bool {
	for _, s := range list {
		if s == val {
			return true
		}
	}
	return false
}

func (r *Repo) loadInventory() error {
	inv, err := LoadTOMLMap(filepath.Join(r.Root, "inventory.toml"))
	if err != nil {
		return err
	}
	d, _ := inv["defaults"].(map[string]any)
	if d == nil {
		d = map[string]any{}
	}
	rawTargets, _ := inv["target"].([]map[string]any)
	if len(rawTargets) == 0 {
		if anyList, ok := inv["target"].([]any); ok {
			for _, item := range anyList {
				if tm, ok := item.(map[string]any); ok {
					rawTargets = append(rawTargets, tm)
				}
			}
		}
	}
	defExt := asStringSlice(d, "external")
	defSkillDirs := asStringMap(d, "provider_skill_dirs")
	defLinkMode := asStringMap(d, "link_mode")
	defPromptFiles := asStringMap(d, "provider_prompt_files")
	defHookFiles := mergeStringMaps(DefaultHookFiles, asStringMap(d, "provider_hook_files"))
	defSSModes := asStringMap(d, "skillshare_target_modes")
	defSSPaths := asStringMap(d, "skillshare_target_paths")

	for _, tm := range rawTargets {
		id := asString(tm, "id", "")
		ssh := asString(tm, "ssh", "")
		if id == "" || ssh == "" {
			return Errf("inventory.toml: target without id/ssh")
		}
		home := asString(tm, "home", "")
		if home != "" && ssh != "local" {
			return Errf("%s: `home` override is only allowed with ssh = \"local\"", id)
		}
		var codexHooks *bool
		if raw, exists := tm["codex_hooks_feature"]; exists && raw != nil {
			b, ok := raw.(bool)
			if !ok {
				return Errf("%s: codex_hooks_feature must be true or false", id)
			}
			codexHooks = &b
		}
		ssMode := asString(tm, "skillshare_mode", asString(d, "skillshare_mode", "merge"))
		if !containsStr(SkillshareModes, ssMode) {
			return Errf("%s: skillshare_mode must be one of %v, not %q", id, SkillshareModes, ssMode)
		}
		ssTargets := asStringSlice(tm, "skillshare_targets")
		if ssTargets == nil {
			ssTargets = asStringSlice(d, "skillshare_targets")
		}
		t := &Target{
			ID:                    id,
			SSH:                   ssh,
			Providers:             asStringSlice(tm, "providers"),
			Profiles:              asStringSlice(tm, "profiles"),
			Roles:                 asStringSlice(tm, "roles"),
			External:              mergeUnique(defExt, asStringSlice(tm, "external")),
			Adopt:                 asStringSlice(tm, "adopt"),
			Add:                   asStringSlice(tm, "add"),
			Remove:                asStringSlice(tm, "remove"),
			SkillRoot:             asString(tm, "skill_root", asString(d, "skill_root", "~/.local/share/agent-config/skills")),
			ProviderSkillDirs:     mergeStringMaps(defSkillDirs, asStringMap(tm, "provider_skill_dirs")),
			LinkMode:              mergeStringMaps(defLinkMode, asStringMap(tm, "link_mode")),
			BackupRoot:            asString(tm, "backup_root", asString(d, "backup_root", "~/.local/share/agent-config/backup")),
			ProviderPromptFiles:   mergeStringMaps(defPromptFiles, asStringMap(tm, "provider_prompt_files")),
			Home:                  home,
			Python:                asString(tm, "python", ""),
			Claude:                asString(tm, "claude", ""),
			CodexConfig:           asString(tm, "codex_config", asString(d, "codex_config", "~/.codex/config.toml")),
			ProviderHookFiles:     mergeStringMaps(defHookFiles, asStringMap(tm, "provider_hook_files")),
			HookRoot:              asString(tm, "hook_root", asString(d, "hook_root", "~/.local/share/agent-config/hooks")),
			HookStubDays:          asInt(tm, "hook_stub_days", asInt(d, "hook_stub_days", 30)),
			CodexHooksFeature:     codexHooks,
			LauncherRoot:          asString(tm, "launcher_root", asString(d, "launcher_root", "~/.local/share/agent-config/launchers")),
			BinDir:                asString(tm, "bin_dir", asString(d, "bin_dir", "~/.local/bin")),
			LauncherCLIAlias:      asString(tm, "launcher_cli_alias", asString(d, "launcher_cli_alias", "liv")),
			InstallLauncherCLI:    asBool(tm, "install_launcher_cli", asBool(d, "install_launcher_cli", false)),
			EmitShims:             asBool(tm, "emit_shims", asBool(d, "emit_shims", false)),
			SyncHerdrMod:          asBool(tm, "sync_herdr_mod", asBool(d, "sync_herdr_mod", false)),
			SyncHerdrPrograms:     asBool(tm, "sync_herdr_programs", asBool(d, "sync_herdr_programs", false)),
			HerdrProgramsFile:     asString(tm, "herdr_programs_file", asString(d, "herdr_programs_file", "~/.config/herdr-app/programs.json")),
			SyncHerdrTemplates:    asBool(tm, "sync_herdr_templates", asBool(d, "sync_herdr_templates", false)),
			HerdrTemplatesDir:     asString(tm, "herdr_templates_dir", asString(d, "herdr_templates_dir", "~/.config/herdr-core/templates")),
			SyncT3Providers:       asBool(tm, "sync_t3_providers", asBool(d, "sync_t3_providers", false)),
			T3SettingsFile:        asString(tm, "t3_settings_file", asString(d, "t3_settings_file", "~/.t3/userdata/settings.json")),
			SkillshareRoot:        asString(tm, "skillshare_root", asString(d, "skillshare_root", "~/.local/share/agent-config/skillshare")),
			SkillshareBin:         asString(tm, "skillshare_bin", asString(d, "skillshare_bin", "")),
			SkillshareTargets:     ssTargets,
			SkillshareMode:        ssMode,
			SkillshareTargetModes: mergeStringMaps(defSSModes, asStringMap(tm, "skillshare_target_modes")),
			SkillshareTargetPaths: mergeStringMaps(defSSPaths, asStringMap(tm, "skillshare_target_paths")),
			SkillshareManageMCP:   asBool(tm, "skillshare_manage_mcp", asBool(d, "skillshare_manage_mcp", true)),
			SkillshareManageHooks: asBool(tm, "skillshare_manage_hooks", asBool(d, "skillshare_manage_hooks", true)),
		}
		r.Targets = append(r.Targets, t)
	}
	return nil
}

func (r *Repo) loadProfiles() error {
	dir := filepath.Join(r.Root, "profiles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		stem := strings.TrimSuffix(e.Name(), ".toml")
		data, err := LoadTOMLMap(p)
		if err != nil {
			return err
		}
		pid := asString(data, "id", stem)
		if pid != stem {
			return Errf("%s: id %q does not match the file name", p, pid)
		}
		promptTbl, _ := data["prompt"].(map[string]any)
		skillsTbl, _ := data["skills"].(map[string]any)
		rawSkills := asStringSlice(skillsTbl, "include")
		if len(rawSkills) == 0 {
			rawSkills = asStringSlice(data, "skills")
		}
		r.Profiles[pid] = &Profile{
			ID:          pid,
			Description: asString(data, "description", ""),
			Extends:     asStringSlice(data, "extends"),
			Fragments:   asStringSlice(promptTbl, "fragments"),
			Skills:      rawSkills,
			MCP:         asStringSlice(data, "mcp"),
			Hooks:       asStringSlice(data, "hooks"),
			Agents:      asStringSlice(data, "agents"),
			Extras:      asStringSlice(data, "extras"),
			Plugins:     asStringSlice(data, "plugins"),
			Launchers:   asStringSlice(data, "launchers"),
		}
	}
	return nil
}

// ParseFragment parses a `+++` TOML frontmatter markdown fragment.
func ParseFragment(path, base string) (*Fragment, error) {
	rawBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(rawBytes)
	if !strings.HasPrefix(text, "+++\n") && !strings.HasPrefix(text, "+++\r\n") {
		return nil, Errf("%s: missing +++ front matter", path)
	}
	rest := text[4:]
	idx := strings.Index(rest, "\n+++")
	if idx == -1 {
		return nil, Errf("%s: unclosed +++ front matter", path)
	}
	fmText := rest[:idx]
	body := strings.TrimSpace(rest[idx+4:])
	var meta map[string]any
	if _, err := toml.Decode(fmText, &meta); err != nil {
		return nil, Errf("%s: %v", path, err)
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return nil, err
	}
	expected := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
	id := asString(meta, "id", "")
	if id != expected {
		return nil, Errf("%s: id %q should be %q (matches the path)", path, id, expected)
	}
	provs := asStringSlice(meta, "providers")
	if len(provs) == 0 {
		return nil, Errf("%s: providers must be a non-empty list", path)
	}
	for _, p := range provs {
		if !containsStr(Providers, p) {
			return nil, Errf("%s: unknown provider %q", path, p)
		}
	}
	scope := asString(meta, "scope", "")
	if !containsStr(Scopes, scope) {
		return nil, Errf("%s: scope must be one of %v, not %q", path, Scopes, scope)
	}
	return &Fragment{
		ID:        id,
		Path:      path,
		Providers: provs,
		Scope:     scope,
		Why:       asString(meta, "why", ""),
		Since:     asString(meta, "since", ""),
		Body:      body,
	}, nil
}

func (r *Repo) loadFragments() error {
	orderPath := filepath.Join(r.Root, "prompts", "order.toml")
	if _, err := os.Stat(orderPath); err == nil {
		var ord struct {
			Fragments []string `toml:"fragments"`
		}
		if _, err := toml.DecodeFile(orderPath, &ord); err != nil {
			return Errf("%s: %v", orderPath, err)
		}
		r.FragmentOrder = ord.Fragments
	}
	base := filepath.Join(r.Root, "prompts", "fragments")
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		frag, err := ParseFragment(p, base)
		if err != nil {
			return err
		}
		r.Fragments[frag.ID] = frag
		return nil
	})
}

func (r *Repo) loadOverlays() error {
	base := filepath.Join(r.Root, "projects")
	matches, _ := filepath.Glob(filepath.Join(base, "*", "*", "project.toml"))
	sort.Strings(matches)
	for _, pt := range matches {
		data, err := LoadTOMLMap(pt)
		if err != nil {
			return err
		}
		ovPath := asString(data, "path", "")
		if ovPath == "" {
			return Errf("%s: missing `path`", pt)
		}
		d := filepath.Dir(pt)
		var skills []string
		for _, s := range asStringSlice(data, "skills") {
			sn := NormalizeSkillName(s)
			if sn != "" && !containsStr(skills, sn) {
				skills = append(skills, sn)
			}
		}
		if entries, err := os.ReadDir(filepath.Join(d, "skills")); err == nil {
			for _, e := range entries {
				if e.IsDir() && !containsStr(skills, e.Name()) {
					skills = append(skills, e.Name())
				}
			}
			sort.Strings(skills)
		}
		r.Overlays = append(r.Overlays, &Overlay{
			Target:     filepath.Base(filepath.Dir(d)),
			Slug:       filepath.Base(d),
			Dir:        d,
			Path:       ovPath,
			Why:        asString(data, "why", ""),
			Files:      asStringSlice(data, "files"),
			Skills:     skills,
			MCP:        asStringSlice(data, "mcp"),
			CodexTrust: asBool(data, "codex_trust", false),
		})
	}
	return nil
}

func (r *Repo) loadMCP() error {
	dir := filepath.Join(r.Root, "mcp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		stem := strings.TrimSuffix(e.Name(), ".toml")
		data, err := LoadTOMLMap(p)
		if err != nil {
			return err
		}
		name := asString(data, "name", stem)
		if name != stem {
			return Errf("%s: name %q does not match the file name", p, name)
		}
		provs := asStringSlice(data, "providers")
		if len(provs) == 0 {
			provs = append([]string(nil), Providers...)
		}
		overrides := map[string]map[string]any{}
		spec := map[string]any{}
		for k, v := range data {
			if containsStr(Providers, k) {
				if sub, ok := v.(map[string]any); ok {
					overrides[k] = sub
				}
			} else if k != "name" && k != "why" && k != "providers" {
				spec[k] = v
			}
		}
		r.MCP[name] = &McpServer{
			Name:      name,
			Path:      p,
			Why:       asString(data, "why", ""),
			Providers: provs,
			Spec:      spec,
			Overrides: overrides,
		}
	}
	return nil
}

func (r *Repo) loadHooks() error {
	dir := filepath.Join(r.Root, "hooks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		stem := strings.TrimSuffix(e.Name(), ".toml")
		data, err := LoadTOMLMap(p)
		if err != nil {
			return err
		}
		hid := asString(data, "id", stem)
		if hid != stem {
			return Errf("%s: id %q does not match the file name", p, hid)
		}
		provs := asStringSlice(data, "providers")
		if len(provs) == 0 {
			provs = append([]string(nil), Providers...)
		}
		overrides := map[string]map[string]any{}
		spec := map[string]any{}
		for k, v := range data {
			if containsStr(Providers, k) {
				if sub, ok := v.(map[string]any); ok {
					overrides[k] = sub
				}
			} else if k != "id" && k != "why" && k != "providers" {
				spec[k] = v
			}
		}
		r.Hooks[hid] = &HookSpec{
			ID:        hid,
			Path:      p,
			Why:       asString(data, "why", ""),
			Providers: provs,
			Spec:      spec,
			Overrides: overrides,
		}
	}
	return nil
}

// DeriveProfileName computes the short profile name for `liv <harness> <profile>`
// (e.g. id="claude-gateway", harness="claude" -> "gateway").
func DeriveProfileName(id, harness, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	prefix := harness + "-"
	if harness != "" && strings.HasPrefix(id, prefix) && len(id) > len(prefix) {
		return id[len(prefix):]
	}
	return id
}

func (r *Repo) loadLaunchers() error {
	dir := filepath.Join(r.Root, "launchers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		stem := strings.TrimSuffix(e.Name(), ".toml")
		data, err := LoadTOMLMap(p)
		if err != nil {
			return err
		}
		lid := asString(data, "id", stem)
		if lid != stem {
			return Errf("%s: id %q does not match the file name", p, lid)
		}
		harness := asString(data, "harness", "")
		shim := lid
		if _, hasShim := data["shim"]; hasShim {
			shim = asString(data, "shim", "")
		}
		prof := DeriveProfileName(lid, harness, asString(data, "profile", ""))
		r.Launchers[lid] = &LauncherSpec{
			ID:           lid,
			Profile:      prof,
			SessionGroup: strings.TrimSpace(asString(data, "session_group", "")),
			Path:         p,
			Why:          asString(data, "why", ""),
			Harness:      harness,
			Shim:         shim,
			Raw:          data,
		}
	}
	return nil
}

// --- lookups & resolution -----------------------------------------------------------------------

func (r *Repo) Target(id string) (*Target, error) {
	for _, t := range r.Targets {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, Errf("unknown target %q (see inventory.toml)", id)
}

func (r *Repo) SelectTargets(id string) ([]*Target, error) {
	if id == "" {
		return append([]*Target(nil), r.Targets...), nil
	}
	t, err := r.Target(id)
	if err != nil {
		return nil, err
	}
	return []*Target{t}, nil
}

func (r *Repo) Skill(ref string) (*Skill, error) {
	kind, rest, ok := strings.Cut(ref, ":")
	if !ok {
		return nil, Errf("skill ref %q: must start with local: or vendor:", ref)
	}
	var src, submodule, metaPath string
	switch kind {
	case "local":
		src = filepath.Join(r.Root, "skills", "local", rest)
		metaPath = filepath.Join(src, "skill.toml")
	case "vendor":
		repoName, sub, _ := strings.Cut(rest, "/")
		src = filepath.Join(r.Root, "skills", "vendor", repoName, filepath.FromSlash(sub))
		submodule = "skills/vendor/" + repoName
		metaPath = filepath.Join(r.Root, "skills", "overrides", repoName, filepath.Base(src)+".toml")
	default:
		return nil, Errf("skill ref %q: must start with local: or vendor:", ref)
	}
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		rel, _ := filepath.Rel(r.Root, src)
		return nil, Errf("skill ref %q: no SKILL.md in %s", ref, rel)
	}
	provs := append([]string(nil), Providers...)
	if _, err := os.Stat(metaPath); err == nil {
		if meta, err := LoadTOMLMap(metaPath); err == nil {
			if ps := asStringSlice(meta, "providers"); len(ps) > 0 {
				provs = ps
			}
		}
	}
	return &Skill{
		Ref:       ref,
		Name:      filepath.Base(src),
		Src:       src,
		Providers: provs,
		Submodule: submodule,
	}, nil
}

// NormalizeSkillName converts legacy "local:foo", "vendor:repo/skills/foo", or "skill:foo" refs to bare skill name "foo".
func NormalizeSkillName(ref string) string {
	ref = strings.TrimSpace(ref)
	for _, pref := range []string{"skill:", "local:"} {
		if strings.HasPrefix(ref, pref) {
			return strings.TrimPrefix(ref, pref)
		}
	}
	if strings.HasPrefix(ref, "vendor:") {
		rest := strings.TrimPrefix(ref, "vendor:")
		return filepath.Base(rest)
	}
	return ref
}

func IsSkillRef(x string) bool {
	return strings.HasPrefix(x, "skill:") || strings.HasPrefix(x, "local:") || strings.HasPrefix(x, "vendor:")
}

func IsEntryRef(x string) bool {
	return hasAnyPrefix(x, "mcp:", "hook:", "launcher:", "agent:", "extra:", "plugin:")
}

func (r *Repo) ExpandProfiles(names []string) ([]string, error) {
	var order []string
	var visit func(pid string, stack []string) error
	visit = func(pid string, stack []string) error {
		if containsStr(stack, pid) {
			return Errf("profile cycle: %s", strings.Join(append(stack, pid), " -> "))
		}
		prof, ok := r.Profiles[pid]
		if !ok {
			where := "inventory"
			if len(stack) > 0 {
				where = fmt.Sprintf("profile %q", stack[len(stack)-1])
			}
			return Errf("%s refers to unknown profile %q", where, pid)
		}
		for _, parent := range prof.Extends {
			if err := visit(parent, append(stack, pid)); err != nil {
				return err
			}
		}
		if !containsStr(order, pid) {
			order = append(order, pid)
		}
		return nil
	}
	for _, n := range names {
		if err := visit(n, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func (r *Repo) Resolve(t *Target) (frags []string, skills []string, err error) {
	pids, err := r.ExpandProfiles(t.Profiles)
	if err != nil {
		return nil, nil, err
	}
	for _, pid := range pids {
		prof := r.Profiles[pid]
		for _, f := range prof.Fragments {
			if !containsStr(frags, f) {
				frags = append(frags, f)
			}
		}
		for _, s := range prof.Skills {
			if !containsStr(skills, s) {
				skills = append(skills, s)
			}
		}
	}
	for _, x := range t.Add {
		if IsEntryRef(x) {
			continue
		}
		if IsSkillRef(x) {
			if !containsStr(skills, x) {
				skills = append(skills, x)
			}
		} else {
			if !containsStr(frags, x) {
				frags = append(frags, x)
			}
		}
	}
	frags = filterOut(frags, t.Remove)
	skills = filterOut(skills, t.Remove)
	if len(r.FragmentOrder) > 0 {
		frags = r.SortFragmentsCanonical(frags)
	}
	for _, f := range frags {
		if _, ok := r.Fragments[f]; !ok {
			return nil, nil, Errf("%s: unknown fragment %q", t.ID, f)
		}
	}
	return frags, skills, nil
}

func (r *Repo) ResolveEntries(t *Target, profileIDs []string) (mcps []string, hooks []string, err error) {
	for _, pid := range profileIDs {
		prof := r.Profiles[pid]
		for _, m := range prof.MCP {
			if !containsStr(mcps, m) {
				mcps = append(mcps, m)
			}
		}
		for _, h := range prof.Hooks {
			if !containsStr(hooks, h) {
				hooks = append(hooks, h)
			}
		}
	}
	for _, x := range t.Add {
		if strings.HasPrefix(x, "mcp:") {
			name := x[4:]
			if !containsStr(mcps, name) {
				mcps = append(mcps, name)
			}
		} else if strings.HasPrefix(x, "hook:") {
			name := x[5:]
			if !containsStr(hooks, name) {
				hooks = append(hooks, name)
			}
		}
	}
	var outMCP, outHooks []string
	for _, m := range mcps {
		if !containsStr(t.Remove, "mcp:"+m) {
			if _, ok := r.MCP[m]; !ok {
				return nil, nil, Errf("%s: unknown MCP server %q (no mcp/%s.toml)", t.ID, m, m)
			}
			if t.ExternalMCP(m) {
				return nil, nil, Errf("%s: MCP server %q matches this target's `external`", t.ID, m)
			}
			outMCP = append(outMCP, m)
		}
	}
	for _, h := range hooks {
		if !containsStr(t.Remove, "hook:"+h) {
			if _, ok := r.Hooks[h]; !ok {
				return nil, nil, Errf("%s: unknown hook %q (no hooks/%s.toml)", t.ID, h, h)
			}
			outHooks = append(outHooks, h)
		}
	}
	return outMCP, outHooks, nil
}

func (r *Repo) ResolveLaunchers(t *Target, profileIDs []string) ([]string, error) {
	var selected []string
	for _, pid := range profileIDs {
		prof := r.Profiles[pid]
		for _, lid := range prof.Launchers {
			if !containsStr(selected, lid) {
				selected = append(selected, lid)
			}
		}
	}
	for _, x := range t.Add {
		if strings.HasPrefix(x, "launcher:") {
			lid := x[9:]
			if !containsStr(selected, lid) {
				selected = append(selected, lid)
			}
		}
	}
	var out []string
	for _, lid := range selected {
		if containsStr(t.Remove, "launcher:"+lid) {
			continue
		}
		spec, ok := r.Launchers[lid]
		if !ok {
			return nil, Errf("%s: unknown launcher %q (no launchers/%s.toml)", t.ID, lid, lid)
		}
		if t.EmitShims && spec.Shim != "" && t.ExternalLauncher(spec.Shim) {
			return nil, Errf("%s: launcher %q (shim %q) matches this target's `external`", t.ID, lid, spec.Shim)
		}
		out = append(out, lid)
	}
	return out, nil
}

func filterOut(items, remove []string) []string {
	var out []string
	for _, s := range items {
		if !containsStr(remove, s) {
			out = append(out, s)
		}
	}
	return out
}

// --- Git helpers --------------------------------------------------------------------------------

func (r *Repo) Git(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", r.Root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", Errf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *Repo) Head() string {
	h, err := r.Git("rev-parse", "HEAD")
	if err != nil {
		return "uncommitted"
	}
	return h
}

func (r *Repo) Dirty() bool {
	args := append([]string{"status", "--porcelain", "--"}, WatchedPaths...)
	out, err := r.Git(args...)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

func (r *Repo) LastCommit(paths []string) string {
	args := []string{"log", "-1", "--format=%h", "--"}
	for _, p := range paths {
		rel, err := filepath.Rel(r.Root, p)
		if err != nil {
			rel = p
		}
		args = append(args, rel)
	}
	out, err := r.Git(args...)
	if err != nil || out == "" {
		return "uncommitted"
	}
	return out
}

func (r *Repo) SubmoduleSHA(sub string) string {
	if sub == "" {
		return ""
	}
	out, err := r.Git("rev-parse", "HEAD:"+sub)
	if err != nil {
		return ""
	}
	return out
}

// --- Lockfile -----------------------------------------------------------------------------------

func LoadLock(root string) (map[string]*LockTarget, error) {
	p := filepath.Join(root, "provenance", "lock.toml")
	out := map[string]*LockTarget{}
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return out, nil
	}
	var doc struct {
		Entry  []LockEntry            `toml:"entry"`
		Target map[string]*LockTarget `toml:"target"`
	}
	if _, err := toml.DecodeFile(p, &doc); err != nil {
		return nil, Errf("%s: %v", p, err)
	}
	// Primary format: flat [[entry]] records (compatible with provenance/lock.toml)
	for _, e := range doc.Entry {
		if e.Target == "" {
			continue
		}
		lt, ok := out[e.Target]
		if !ok {
			lt = &LockTarget{}
			out[e.Target] = lt
		}
		if e.Commit != "" && (lt.Commit == "" || e.AppliedAt > lt.At) {
			lt.Commit = e.Commit
		}
		if e.AppliedAt > lt.At {
			lt.At = e.AppliedAt
		}
		lt.Items = append(lt.Items, e)
	}
	// Also support legacy [target."id"] table format if present
	for tid, lt := range doc.Target {
		if _, exists := out[tid]; !exists && lt != nil {
			out[tid] = lt
		}
	}
	return out, nil
}

// --- Catalog skill discovery --------------------------------------------------------------------

func SkillFrontmatterSummary(skillDir string) (desc string, nBytes int, nWords int) {
	b, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		return "", 0, 0
	}
	text := string(b)
	if strings.HasPrefix(text, "---") {
		parts := strings.SplitN(text, "---", 3)
		if len(parts) >= 3 {
			for _, ln := range strings.Split(parts[1], "\n") {
				tr := strings.TrimSpace(ln)
				if strings.HasPrefix(strings.ToLower(tr), "description:") {
					desc = strings.TrimSpace(tr[len("description:"):])
					desc = strings.Trim(desc, `"'`)
					if len(desc) > 240 {
						desc = desc[:240]
					}
					break
				}
			}
		}
	}
	return desc, len(b), len(strings.Fields(text))
}

func (r *Repo) DiscoverCatalogSkills() []CatalogSkill {
	seen := map[string]CatalogSkill{}
	localRoot := filepath.Join(r.Root, "skills", "local")
	if entries, err := os.ReadDir(localRoot); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			ref := "local:" + e.Name()
			if sk, err := r.Skill(ref); err == nil {
				desc, nb, nw := SkillFrontmatterSummary(sk.Src)
				seen[ref] = CatalogSkill{
					Ref: ref, Name: sk.Name, Source: "local",
					Providers: sk.Providers, Description: desc, Bytes: nb, Words: nw,
				}
			}
		}
	}
	vendorRoot := filepath.Join(r.Root, "skills", "vendor")
	if repos, err := os.ReadDir(vendorRoot); err == nil {
		for _, rd := range repos {
			if !rd.IsDir() {
				continue
			}
			repoDir := filepath.Join(vendorRoot, rd.Name())
			_ = filepath.WalkDir(repoDir, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() && d.Name() == ".git" {
					return filepath.SkipDir
				}
				if !d.IsDir() && d.Name() == "SKILL.md" {
					rel, err := filepath.Rel(repoDir, filepath.Dir(p))
					if err != nil || rel == "." {
						return nil
					}
					ref := "vendor:" + rd.Name() + "/" + filepath.ToSlash(rel)
					if sk, err := r.Skill(ref); err == nil {
						desc, nb, nw := SkillFrontmatterSummary(sk.Src)
						seen[ref] = CatalogSkill{
							Ref: ref, Name: sk.Name, Source: "vendor:" + rd.Name(),
							Providers: sk.Providers, Description: desc, Bytes: nb, Words: nw,
						}
					}
				}
				return nil
			})
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]CatalogSkill, 0, len(keys))
	for _, k := range keys {
		out = append(out, seen[k])
	}
	return out
}

func (r *Repo) HeadCommit() string {
	out, err := exec.Command("git", "-C", r.Root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "dirty"
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "dirty"
	}
	return s
}
