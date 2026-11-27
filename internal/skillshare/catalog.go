package skillshare

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bowd/livery/internal/model"
	"gopkg.in/yaml.v3"
)

// CatalogDir returns the standalone library-only skillshare git repo directory:
// <dataHome>/catalog/skillshare (falls back to legacy <dataHome>/skillshare if only that exists).
func CatalogDir(dataHome string) string {
	modern := filepath.Join(dataHome, "catalog", "skillshare")
	if _, err := os.Stat(modern); err == nil {
		return modern
	}
	legacy := filepath.Join(dataHome, "skillshare")
	if _, err := os.Stat(filepath.Join(legacy, "skills")); err == nil {
		return legacy
	}
	return modern
}

func CatalogSkillsDir(dataHome string) string {
	return filepath.Join(CatalogDir(dataHome), "skills")
}

func CatalogAgentsDir(dataHome string) string {
	return filepath.Join(CatalogDir(dataHome), "agents")
}

func CatalogExtrasDir(dataHome string) string {
	return filepath.Join(CatalogDir(dataHome), "extras")
}

func CatalogConfigPath(dataHome string) string {
	return filepath.Join(CatalogDir(dataHome), "config.yaml")
}

// NormalizeCatalogLibraryConfig ensures library-only invariants in catalog/skillshare/config.yaml
// (`targets: []` on MCP servers without explicit targets so Skillshare preflight never errors when
// top-level `targets: {}` is empty, and `bindings: {}` on plugins).
func NormalizeCatalogLibraryConfig(dataHome string) {
	cfgPath := CatalogConfigPath(dataHome)
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil || doc == nil {
		return
	}
	changed := false
	if mcpSec, ok := doc["mcp"].(map[string]any); ok {
		if srvs, ok := mcpSec["servers"].(map[string]any); ok {
			for _, rv := range srvs {
				if sm, ok := rv.(map[string]any); ok {
					if _, has := sm["targets"]; !has {
						sm["targets"] = []any{}
						changed = true
					}
				}
			}
		}
	}
	if plSec, ok := doc["plugins"].(map[string]any); ok {
		if pkgs, ok := plSec["packages"].(map[string]any); ok {
			for _, rv := range pkgs {
				if pm, ok := rv.(map[string]any); ok {
					if bmap, ok := pm["bindings"].(map[string]any); ok && len(bmap) > 0 {
						pm["bindings"] = map[string]any{}
						changed = true
					}
				}
			}
		}
	}
	if changed {
		if out, err := encodeYAML(doc); err == nil {
			_ = os.WriteFile(cfgPath, out, 0o644)
		}
	}
}

// EnvOverridePath returns the per-environment local-only Skillshare override file:
// <dataHome>/env-overrides/<envID>.yaml.
func EnvOverridePath(dataHome, envID string) string {
	return filepath.Join(dataHome, "env-overrides", envID+".yaml")
}

type CatalogItem struct {
	Name        string         `json:"name"`
	Category    string         `json:"category"` // skills | agents | extras | mcp | hooks | plugins
	RelPath     string         `json:"rel_path,omitempty"`
	Dir         string         `json:"-"`
	Description string         `json:"description,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Words       int            `json:"words,omitempty"`
	Raw         map[string]any `json:"-"`
}

type CatalogContents struct {
	Dir       string         `json:"dir"`
	Config    string         `json:"config"`
	Skills    []RepoSkill    `json:"skills"`
	Agents    []CatalogItem  `json:"agents"`
	Extras    []CatalogItem  `json:"extras"`
	MCP       []CatalogItem  `json:"mcp"`
	Hooks     []CatalogItem  `json:"hooks"`
	Plugins   []CatalogItem  `json:"plugins"`
	RawConfig map[string]any `json:"-"`
}

// EnsureCatalogInitialized ensures <dataHome>/catalog/skillshare exists as a clean standalone git repo
// with library-only config.yaml (targets: {}) so stock `skillshare ui` Git Sync never leaks parent files.
func EnsureCatalogInitialized(dataHome string) error {
	dir := filepath.Join(dataHome, "catalog", "skillshare")
	for _, sub := range []string{"skills", "agents", "extras"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		doc := map[string]any{
			"mode":          "merge",
			"git_root":      "root",
			"sources":       map[string]any{"skills": filepath.Join(dir, "skills")},
			"extras_source": filepath.Join(dir, "extras"),
			"targets":       map[string]any{},
		}
		b, err := encodeYAML(doc)
		if err != nil {
			return err
		}
		if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
			return err
		}
	} else if raw, err := os.ReadFile(cfgPath); err == nil {
		// Ensure git_root: root matches our root-level .git and relative source paths resolve cleanly.
		var doc map[string]any
		if yaml.Unmarshal(raw, &doc) == nil && doc != nil {
			changed := false
			if gr, _ := doc["git_root"].(string); gr == "" {
				doc["git_root"] = "root"
				changed = true
			}
			if src, ok := doc["sources"].(map[string]any); ok {
				if sk, ok := src["skills"].(string); ok && sk != "" && !filepath.IsAbs(sk) && !strings.HasPrefix(sk, "~") {
					src["skills"] = filepath.Join(dir, sk)
					changed = true
				}
			}
			if ex, ok := doc["extras_source"].(string); ok && ex != "" && !filepath.IsAbs(ex) && !strings.HasPrefix(ex, "~") {
				doc["extras_source"] = filepath.Join(dir, ex)
				changed = true
			}
			if changed {
				if b, err := encodeYAML(doc); err == nil {
					_ = os.WriteFile(cfgPath, b, 0o644)
				}
			}
		}
	}
	// Ensure standalone .git so stock `skillshare` Git Sync ONLY sees catalog/skillshare!
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if err := runGit(dir, "init", "-q"); err == nil {
			_ = runGit(dir, "config", "user.name", "livery")
			_ = runGit(dir, "config", "user.email", "livery@local")
			_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".DS_Store\n__pycache__/\n"), 0o644)
			_ = runGit(dir, "add", "-A")
			_ = runGit(dir, "commit", "-q", "-m", "Initialize livery skillshare catalog")
		}
	}
	return nil
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	return cmd.Run()
}

// LoadCatalog discovers all 6 stock skillshare library item categories from <dataHome>/catalog/skillshare/.
func LoadCatalog(dataHome string) *CatalogContents {
	dir := CatalogDir(dataHome)
	cfgPath := filepath.Join(dir, "config.yaml")
	cat := &CatalogContents{
		Dir:       dir,
		Config:    cfgPath,
		Skills:    []RepoSkill{},
		Agents:    []CatalogItem{},
		Extras:    []CatalogItem{},
		MCP:       []CatalogItem{},
		Hooks:     []CatalogItem{},
		Plugins:   []CatalogItem{},
		RawConfig: map[string]any{},
	}

	// 1. Skills (skills/<name>/SKILL.md)
	skillsRoot := filepath.Join(dir, "skills")
	if _, err := os.Stat(skillsRoot); err == nil {
		_ = filepath.WalkDir(skillsRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil || p == skillsRoot {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if name == ".git" || name == "__pycache__" || name == "node_modules" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				skillMD := filepath.Join(p, "SKILL.md")
				if st, err := os.Stat(skillMD); err == nil && !st.IsDir() {
					raw, _ := os.ReadFile(skillMD)
					rel, _ := filepath.Rel(skillsRoot, p)
					cat.Skills = append(cat.Skills, RepoSkill{
						Name:        filepath.Base(p),
						RelPath:     filepath.ToSlash(rel),
						Dir:         p,
						Description: extractFrontmatterField(string(raw), "description"),
						Bytes:       len(raw),
						Words:       len(strings.Fields(string(raw))),
					})
					return filepath.SkipDir
				}
			}
			return nil
		})
	}
	sort.Slice(cat.Skills, func(i, j int) bool { return cat.Skills[i].Name < cat.Skills[j].Name })

	// 2. Agents (agents/*.md or agents/<name>/)
	agentsRoot := filepath.Join(dir, "agents")
	if entries, err := os.ReadDir(agentsRoot); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				raw, _ := os.ReadFile(filepath.Join(agentsRoot, e.Name()))
				name := strings.TrimSuffix(e.Name(), ".md")
				cat.Agents = append(cat.Agents, CatalogItem{
					Name:        name,
					Category:    "agents",
					RelPath:     e.Name(),
					Dir:         filepath.Join(agentsRoot, e.Name()),
					Description: extractFrontmatterField(string(raw), "description"),
					Words:       len(strings.Fields(string(raw))),
				})
			} else if e.IsDir() {
				cat.Agents = append(cat.Agents, CatalogItem{
					Name:     e.Name(),
					Category: "agents",
					RelPath:  e.Name(),
					Dir:      filepath.Join(agentsRoot, e.Name()),
				})
			}
		}
	}
	sort.Slice(cat.Agents, func(i, j int) bool { return cat.Agents[i].Name < cat.Agents[j].Name })

	// Parse catalog config.yaml for MCP, Hooks, Extras, Plugins
	if rawCfg, err := os.ReadFile(cfgPath); err == nil {
		var doc map[string]any
		if yaml.Unmarshal(rawCfg, &doc) == nil && doc != nil {
			cat.RawConfig = doc
		}
	}

	// 3. MCP servers (mcp.servers)
	if mcpSec, ok := cat.RawConfig["mcp"].(map[string]any); ok {
		if srvs, ok := mcpSec["servers"].(map[string]any); ok {
			var names []string
			for k := range srvs {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, name := range names {
				sm, _ := srvs[name].(map[string]any)
				summary := ""
				if sm != nil {
					if u, ok := sm["url"].(string); ok && u != "" {
						summary = u
					} else if c, ok := sm["command"].(string); ok && c != "" {
						summary = c
					}
				}
				cat.MCP = append(cat.MCP, CatalogItem{
					Name:     name,
					Category: "mcp",
					Summary:  summary,
					Raw:      sm,
				})
			}
		}
	}

	// 4. Hooks (hooks.entries)
	if hooksSec, ok := cat.RawConfig["hooks"].(map[string]any); ok {
		if ents, ok := hooksSec["entries"].(map[string]any); ok {
			var names []string
			for k := range ents {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, name := range names {
				hm, _ := ents[name].(map[string]any)
				desc := ""
				if hm != nil {
					desc, _ = hm["description"].(string)
				}
				cat.Hooks = append(cat.Hooks, CatalogItem{
					Name:        name,
					Category:    "hooks",
					Description: desc,
					Raw:         hm,
				})
			}
		}
	}

	// 5. Extras (extras list in config.yaml, plus dirs under extras/)
	seenExtras := map[string]bool{}
	if exList, ok := cat.RawConfig["extras"].([]any); ok {
		for _, raw := range exList {
			em, _ := raw.(map[string]any)
			if em == nil {
				continue
			}
			name := strings.TrimSpace(fmt.Sprint(em["name"]))
			if name == "" || name == "<nil>" || seenExtras[name] {
				continue
			}
			seenExtras[name] = true
			cat.Extras = append(cat.Extras, CatalogItem{
				Name:     name,
				Category: "extras",
				Summary:  fmt.Sprint(em["mode"]),
				Raw:      em,
			})
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "extras")); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !seenExtras[e.Name()] {
				seenExtras[e.Name()] = true
				cat.Extras = append(cat.Extras, CatalogItem{
					Name:     e.Name(),
					Category: "extras",
					Dir:      filepath.Join(dir, "extras", e.Name()),
				})
			}
		}
	}
	sort.Slice(cat.Extras, func(i, j int) bool { return cat.Extras[i].Name < cat.Extras[j].Name })

	// 6. Plugins (skillshare v2 schema: plugins.version + plugins.packages.<name>, plus legacy flat/list fallback)
	if plMap, ok := cat.RawConfig["plugins"].(map[string]any); ok {
		pkgs := plMap
		isV2 := false
		if sub, ok := plMap["packages"].(map[string]any); ok {
			pkgs = sub
			isV2 = true
		}
		var names []string
		for k := range pkgs {
			if !isV2 && (k == "version" || k == "packages") {
				continue
			}
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			pm, _ := pkgs[name].(map[string]any)
			cat.Plugins = append(cat.Plugins, CatalogItem{
				Name:     name,
				Category: "plugins",
				Summary:  summarizePluginSpec(pm),
				Raw:      pm,
			})
		}
	} else if plList, ok := cat.RawConfig["plugins"].([]any); ok {
		for _, raw := range plList {
			pm, _ := raw.(map[string]any)
			if pm == nil {
				continue
			}
			name := strings.TrimSpace(fmt.Sprint(pm["name"]))
			if name == "" || name == "<nil>" {
				continue
			}
			cat.Plugins = append(cat.Plugins, CatalogItem{
				Name:     name,
				Category: "plugins",
				Summary:  summarizePluginSpec(pm),
				Raw:      pm,
			})
		}
		sort.Slice(cat.Plugins, func(i, j int) bool { return cat.Plugins[i].Name < cat.Plugins[j].Name })
	}

	return cat
}

func summarizePluginSpec(pm map[string]any) string {
	if pm == nil {
		return ""
	}
	var bits []string
	if src, _ := pm["source"].(string); src != "" {
		s := strings.TrimPrefix(src, "https://github.com/")
		s = strings.TrimSuffix(s, ".git")
		bits = append(bits, s)
	}
	if bmap, ok := pm["bindings"].(map[string]any); ok && len(bmap) > 0 {
		var tgts []string
		for k := range bmap {
			tgts = append(tgts, k)
		}
		sort.Strings(tgts)
		bits = append(bits, strings.Join(tgts, ", "))
	}
	return strings.Join(bits, " · ")
}

// ItemNamesByCategory returns category -> sorted catalog item names for Matrix building.
func (c *CatalogContents) ItemNamesByCategory() map[string][]string {
	out := map[string][]string{
		"skills":  {},
		"agents":  {},
		"extras":  {},
		"mcp":     {},
		"hooks":   {},
		"plugins": {},
	}
	for _, s := range c.Skills {
		out["skills"] = append(out["skills"], s.Name)
	}
	for _, a := range c.Agents {
		out["agents"] = append(out["agents"], a.Name)
	}
	for _, e := range c.Extras {
		out["extras"] = append(out["extras"], e.Name)
	}
	for _, m := range c.MCP {
		out["mcp"] = append(out["mcp"], m.Name)
	}
	for _, h := range c.Hooks {
		out["hooks"] = append(out["hooks"], h.Name)
	}
	for _, p := range c.Plugins {
		out["plugins"] = append(out["plugins"], p.Name)
	}
	return out
}

// --- Per-Environment Local-Only Skillshare Overrides --------------------------------------------

// EnvLocalOverride stores host-specific Skillshare settings & local-only items that live outside
// the shared Catalog/Matrix distribution and must be preserved across outward syncs.
type EnvLocalOverride struct {
	Mode         string         `yaml:"mode,omitempty" json:"mode,omitempty"`
	Targets      map[string]any `yaml:"targets,omitempty" json:"targets,omitempty"`
	Projects     map[string]any `yaml:"projects,omitempty" json:"projects,omitempty"`
	LocalSkills  []string       `yaml:"local_skills,omitempty" json:"local_skills,omitempty"`
	LocalMCP     map[string]any `yaml:"local_mcp,omitempty" json:"local_mcp,omitempty"`
	LocalHooks   map[string]any `yaml:"local_hooks,omitempty" json:"local_hooks,omitempty"`
	LocalPlugins map[string]any `yaml:"local_plugins,omitempty" json:"local_plugins,omitempty"`
	LocalExtras  []any          `yaml:"local_extras,omitempty" json:"local_extras,omitempty"`
	ExtraConfig  map[string]any `yaml:"extra_config,omitempty" json:"extra_config,omitempty"`
}

func LoadEnvOverride(dataHome string, t *model.Target) *EnvLocalOverride {
	ov := &EnvLocalOverride{
		Mode:         t.SkillshareMode,
		Targets:      map[string]any{},
		Projects:     map[string]any{},
		LocalSkills:  []string{},
		LocalMCP:     map[string]any{},
		LocalHooks:   map[string]any{},
		LocalPlugins: map[string]any{},
		LocalExtras:  []any{},
		ExtraConfig:  map[string]any{},
	}
	if ov.Mode == "" {
		ov.Mode = "merge"
	}
	p := EnvOverridePath(dataHome, t.ID)
	if b, err := os.ReadFile(p); err == nil {
		_ = yaml.Unmarshal(b, ov)
		if ov.Mode == "" {
			ov.Mode = "merge"
		}
		if ov.Targets == nil {
			ov.Targets = map[string]any{}
		}
		if ov.Projects == nil {
			ov.Projects = map[string]any{}
		}
		if ov.LocalMCP == nil {
			ov.LocalMCP = map[string]any{}
		}
		if ov.LocalHooks == nil {
			ov.LocalHooks = map[string]any{}
		}
		if ov.LocalPlugins == nil {
			ov.LocalPlugins = map[string]any{}
		}
		if ov.ExtraConfig == nil {
			ov.ExtraConfig = map[string]any{}
		}
		return ov
	}
	// Default target map from inventory target settings
	for _, st := range t.EffectiveSkillshareTargets() {
		if st == "codex" {
			cp := t.ProviderSkillDirs["codex"]
			if cp == "" {
				cp = "~/.codex/skills"
			}
			ov.Targets[st] = map[string]any{"skills": map[string]any{"path": cp}}
		} else {
			ov.Targets[st] = map[string]any{}
		}
	}
	return ov
}

func SaveEnvOverride(dataHome, envID string, ov *EnvLocalOverride) error {
	p := EnvOverridePath(dataHome, envID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	var buf []byte
	var err error
	buf, err = yaml.Marshal(ov)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".env-ov-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, p)
}

// EnvEnabledItems holds the resolved set of Catalog items enabled for one environment (direct + Profile).
type EnvEnabledItems struct {
	Skills  []string
	Agents  []string
	Extras  []string
	MCP     []string
	Hooks   []string
	Plugins []string
}

func deepCloneAny(v any) any {
	b, err := yaml.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if yaml.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}

// CompileEnvSkillshareConfig compiles an environment's stock skillshare config.yaml and selects
// only the enabled Catalog items + preserves all environment-local overrides.
func CompileEnvSkillshareConfig(dataHome string, t *model.Target, enabled EnvEnabledItems, home string) (configYAML []byte, selectedSkills []RepoSkill, selectedAgents []CatalogItem, err error) {
	cat := LoadCatalog(dataHome)
	ov := LoadEnvOverride(dataHome, t)

	sroot := strings.TrimRight(t.SkillshareRoot, "/")
	if sroot == "" {
		sroot = "~/.local/share/agent-config/skillshare"
	}

	// Selected skills from Catalog
	wantSkills := map[string]bool{}
	for _, s := range enabled.Skills {
		wantSkills[model.NormalizeSkillName(s)] = true
	}
	for _, rs := range cat.Skills {
		if wantSkills[rs.Name] {
			selectedSkills = append(selectedSkills, rs)
		}
	}

	// Selected agents from Catalog
	wantAgents := map[string]bool{}
	for _, a := range enabled.Agents {
		wantAgents[a] = true
	}
	for _, ag := range cat.Agents {
		if wantAgents[ag.Name] {
			selectedAgents = append(selectedAgents, ag)
		}
	}

	hasAnySkills := len(selectedSkills) > 0 || len(ov.LocalSkills) > 0

	// Build targets map from ov.Targets
	targetsDoc := map[string]any{}
	var tnames []string
	for k := range ov.Targets {
		tnames = append(tnames, k)
	}
	sort.Strings(tnames)
	if len(tnames) == 0 {
		tnames = t.EffectiveSkillshareTargets()
	}
	for _, tn := range tnames {
		rawT := ov.Targets[tn]
		tm, _ := deepCloneAny(rawT).(map[string]any)
		if tm == nil {
			tm = map[string]any{}
		}
		if !hasAnySkills {
			scfg, _ := tm["skills"].(map[string]any)
			if scfg == nil {
				scfg = map[string]any{}
				tm["skills"] = scfg
			}
			scfg["enabled"] = false
		}
		if tn == "codex" {
			scfg, _ := tm["skills"].(map[string]any)
			if scfg == nil {
				scfg = map[string]any{}
				tm["skills"] = scfg
			}
			if _, hasPath := scfg["path"]; !hasPath {
				cp := t.ProviderSkillDirs["codex"]
				if cp == "" {
					cp = "~/.codex/skills"
				}
				scfg["path"] = cp
			}
		}
		targetsDoc[tn] = tm
	}

	doc := map[string]any{
		"mode":     ov.Mode,
		"git_root": "root",
		"sources":  map[string]any{"skills": sroot + "/skills"},
		"targets":  targetsDoc,
	}
	if len(selectedAgents) > 0 {
		doc["sources"].(map[string]any)["agents"] = sroot + "/agents"
	}

	// MCP servers: Catalog enabled + LocalMCP
	wantMCP := map[string]bool{}
	for _, m := range enabled.MCP {
		wantMCP[m] = true
	}
	mcpServers := map[string]any{}
	for _, item := range cat.MCP {
		if wantMCP[item.Name] && item.Raw != nil {
			cloned := deepCloneAny(item.Raw)
			if home != "" {
				cloned = substHome(cloned, home)
			}
			// Strip library-only `targets: []` placeholder so the host's top-level `mcp.targets` applies
			if cm, ok := cloned.(map[string]any); ok {
				if tgts, has := cm["targets"]; has {
					switch tv := tgts.(type) {
					case []any:
						if len(tv) == 0 {
							delete(cm, "targets")
						}
					case []string:
						if len(tv) == 0 {
							delete(cm, "targets")
						}
					}
				}
			}
			mcpServers[item.Name] = cloned
		}
	}
	for k, v := range ov.LocalMCP {
		cloned := deepCloneAny(v)
		if home != "" {
			cloned = substHome(cloned, home)
		}
		mcpServers[k] = cloned
	}
	if len(mcpServers) > 0 {
		var mcpTargets []string
		seenMT := map[string]bool{}
		for _, tn := range tnames {
			canon := tn
			if a, ok := MCPAliases[tn]; ok {
				canon = a
			}
			if MCPTargets[canon] && !seenMT[canon] {
				seenMT[canon] = true
				mcpTargets = append(mcpTargets, canon)
			}
		}
		doc["mcp"] = map[string]any{
			"targets": mcpTargets,
			"servers": mcpServers,
		}
	}

	// Hooks: Catalog enabled + LocalHooks
	wantHooks := map[string]bool{}
	for _, h := range enabled.Hooks {
		wantHooks[h] = true
	}
	hookEntries := map[string]any{}
	for _, item := range cat.Hooks {
		if wantHooks[item.Name] && item.Raw != nil {
			cloned := deepCloneAny(item.Raw)
			if home != "" {
				cloned = substHome(cloned, home)
			}
			hookEntries[item.Name] = cloned
		}
	}
	for k, v := range ov.LocalHooks {
		cloned := deepCloneAny(v)
		if home != "" {
			cloned = substHome(cloned, home)
		}
		hookEntries[k] = cloned
	}
	if len(hookEntries) > 0 {
		doc["hooks"] = map[string]any{"entries": hookEntries}
	}

	// Extras: Catalog enabled + LocalExtras
	wantExtras := map[string]bool{}
	for _, e := range enabled.Extras {
		wantExtras[e] = true
	}
	var extrasOut []any
	for _, item := range cat.Extras {
		if wantExtras[item.Name] && item.Raw != nil {
			extrasOut = append(extrasOut, deepCloneAny(item.Raw))
		}
	}
	for _, le := range ov.LocalExtras {
		extrasOut = append(extrasOut, deepCloneAny(le))
	}
	if len(extrasOut) > 0 {
		doc["extras"] = extrasOut
		doc["extras_source"] = sroot + "/extras"
	}

	// Plugins: Catalog enabled + LocalPlugins, emitted in skillshare v2 schema (plugins.version + plugins.packages)
	wantPlugins := map[string]bool{}
	for _, p := range enabled.Plugins {
		wantPlugins[p] = true
	}
	hostTargetSet := map[string]bool{}
	for _, tn := range tnames {
		hostTargetSet[tn] = true
	}
	pluginPkgs := map[string]any{}
	compilePluginSpec := func(raw any) any {
		cloned := deepCloneAny(raw)
		if home != "" {
			cloned = substHome(cloned, home)
		}
		pm, ok := cloned.(map[string]any)
		if !ok {
			return cloned
		}
		// Keep bindings only for targets configured on this host; if catalog package has no bindings yet
		// and has a non-npm source, bind to claude when claude is enabled on this host.
		if bmap, ok := pm["bindings"].(map[string]any); ok && len(bmap) > 0 {
			filtered := map[string]any{}
			for bt, bv := range bmap {
				if hostTargetSet[bt] {
					filtered[bt] = bv
				}
			}
			pm["bindings"] = filtered
		} else if hostTargetSet["claude"] {
			if src, _ := pm["source"].(string); src != "" && !strings.HasPrefix(src, "npm:") {
				binding := map[string]any{"source": src, "sync": true}
				if plName, _ := pm["plugin"].(string); plName != "" {
					binding["plugin"] = plName
				}
				pm["bindings"] = map[string]any{"claude": binding}
			}
		}
		return pm
	}
	for _, item := range cat.Plugins {
		if wantPlugins[item.Name] && item.Raw != nil {
			pluginPkgs[item.Name] = compilePluginSpec(item.Raw)
		}
	}
	for k, v := range ov.LocalPlugins {
		pluginPkgs[k] = compilePluginSpec(v)
	}
	if len(pluginPkgs) > 0 {
		doc["plugins"] = map[string]any{
			"version":  2,
			"packages": pluginPkgs,
		}
	}

	// Skillshare native projects managed on this host (preserved via env-overrides/<host>.yaml)
	if len(ov.Projects) > 0 {
		projOut := map[string]any{}
		for pRoot, rawP := range ov.Projects {
			key := pRoot
			if home != "" {
				key = strings.ReplaceAll(key, "{home}", home)
			}
			projOut[key] = deepCloneAny(rawP)
		}
		doc["projects"] = projOut
	}

	// Preserve non-library extra_config keys (audit, hub, gitlab_hosts, etc.)
	for k, v := range ov.ExtraConfig {
		if _, reserved := doc[k]; !reserved {
			doc[k] = deepCloneAny(v)
		}
	}

	cfgBytes, err := encodeYAML(doc)
	if err != nil {
		return nil, nil, nil, err
	}
	return cfgBytes, selectedSkills, selectedAgents, nil
}
