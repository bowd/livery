package skillshare

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bowd/livery/internal/model"
	"gopkg.in/yaml.v3"
)

// RepoSkillsDir returns the canonical git-tracked shared skillshare skills directory: <repo>/skillshare/skills.
func RepoSkillsDir(repoRoot string) string {
	return filepath.Join(repoRoot, "skillshare", "skills")
}

// HostConfigPath returns the canonical git-tracked stock skillshare config for one host:
// <repo>/skillshare/hosts/<targetID>/config.yaml.
func HostConfigPath(repoRoot, targetID string) string {
	return filepath.Join(repoRoot, "skillshare", "hosts", targetID, "config.yaml")
}

type RepoSkill struct {
	Name        string `json:"name"`
	RelPath     string `json:"rel_path"`
	Dir         string `json:"-"`
	Description string `json:"description"`
	Bytes       int    `json:"bytes"`
	Words       int    `json:"words"`
}

// DiscoverRepoSkills finds every skill directory (containing SKILL.md) under <repo>/skillshare/skills.
func DiscoverRepoSkills(repoRoot string) []RepoSkill {
	root := RepoSkillsDir(repoRoot)
	var out []RepoSkill
	if _, err := os.Stat(root); err != nil {
		return out
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p == root {
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
				rel, _ := filepath.Rel(root, p)
				rel = filepath.ToSlash(rel)
				desc := extractFrontmatterField(string(raw), "description")
				out = append(out, RepoSkill{
					Name:        filepath.Base(p),
					RelPath:     rel,
					Dir:         p,
					Description: desc,
					Bytes:       len(raw),
					Words:       len(strings.Fields(string(raw))),
				})
				return filepath.SkipDir
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func extractFrontmatterField(md, field string) string {
	if !strings.HasPrefix(md, "---\n") && !strings.HasPrefix(md, "---\r\n") {
		return ""
	}
	rest := md[4:]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return ""
	}
	var fm map[string]any
	if yaml.Unmarshal([]byte(rest[:end]), &fm) == nil {
		if v, ok := fm[field]; ok && v != nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return ""
}

// DefaultHostConfigYAML builds a clean initial stock skillshare config.yaml for a new host.
func DefaultHostConfigYAML(t *model.Target) []byte {
	sroot := strings.TrimRight(t.SkillshareRoot, "/")
	if sroot == "" {
		sroot = "~/.local/share/agent-config/skillshare"
	}
	mode := t.SkillshareMode
	if mode == "" {
		mode = "merge"
	}
	ssTargets := t.EffectiveSkillshareTargets()
	if len(ssTargets) == 0 {
		ssTargets = []string{"claude", "codex"}
	}
	targets := map[string]any{}
	for _, st := range ssTargets {
		scfg := map[string]any{}
		if st == "codex" {
			p := t.ProviderSkillDirs["codex"]
			if p == "" {
				p = "~/.codex/skills"
			}
			scfg["path"] = p
		}
		if len(scfg) > 0 {
			targets[st] = map[string]any{"skills": scfg}
		} else {
			targets[st] = map[string]any{}
		}
	}
	doc := map[string]any{
		"mode":    mode,
		"sources": map[string]any{"skills": sroot + "/skills"},
		"targets": targets,
	}
	b, _ := encodeYAML(doc)
	return b
}

// EnsureHostConfig creates skillshare/hosts/<targetID>/config.yaml if it does not exist yet.
func EnsureHostConfig(repoRoot string, t *model.Target) (string, error) {
	p := HostConfigPath(repoRoot, t.ID)
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, DefaultHostConfigYAML(t), 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// LoadHostConfigBytes reads skillshare/hosts/<targetID>/config.yaml, falling back to DefaultHostConfigYAML.
func LoadHostConfigBytes(repoRoot string, t *model.Target) ([]byte, error) {
	p := HostConfigPath(repoRoot, t.ID)
	b, err := os.ReadFile(p)
	if err == nil {
		return b, nil
	}
	if os.IsNotExist(err) {
		return DefaultHostConfigYAML(t), nil
	}
	return nil, err
}

type HostConfigSummary struct {
	Mode           string              `json:"mode"`
	Targets        []string            `json:"targets"`
	EnabledTargets []string            `json:"enabled_targets"`
	ActiveSkills   []string            `json:"active_skills"`
	TargetSkills   map[string][]string `json:"target_skills"`
	MCPCount       int                 `json:"mcp_count"`
	HooksCount     int                 `json:"hooks_count"`
}

func yamlStrSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		if s := strings.TrimSpace(fmt.Sprint(el)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func matchAnyGlob(patterns []string, name string) bool {
	for _, pat := range patterns {
		if pat == name {
			return true
		}
		if ok, err := filepath.Match(pat, name); err == nil && ok {
			return true
		}
	}
	return false
}

// SummarizeHostConfig parses a host's skillshare/hosts/<id>/config.yaml and computes active targets & skills.
func SummarizeHostConfig(repoRoot string, t *model.Target) HostConfigSummary {
	sum := HostConfigSummary{
		Mode:           "merge",
		Targets:        []string{},
		EnabledTargets: []string{},
		ActiveSkills:   []string{},
		TargetSkills:   map[string][]string{},
	}
	raw, err := LoadHostConfigBytes(repoRoot, t)
	if err != nil {
		return sum
	}
	var doc map[string]any
	if yaml.Unmarshal(raw, &doc) != nil || doc == nil {
		return sum
	}
	if m, ok := doc["mode"].(string); ok && m != "" {
		sum.Mode = m
	}
	if mcp, ok := doc["mcp"].(map[string]any); ok {
		if srvs, ok := mcp["servers"].(map[string]any); ok {
			sum.MCPCount = len(srvs)
		}
	}
	if hooks, ok := doc["hooks"].(map[string]any); ok {
		if ents, ok := hooks["entries"].(map[string]any); ok {
			sum.HooksCount = len(ents)
		}
	}

	allSkills := DiscoverRepoSkills(repoRoot)
	skillNames := make([]string, 0, len(allSkills))
	for _, s := range allSkills {
		skillNames = append(skillNames, s.Name)
	}

	targetsMap, _ := doc["targets"].(map[string]any)
	var tnames []string
	for k := range targetsMap {
		tnames = append(tnames, k)
	}
	sort.Strings(tnames)
	sum.Targets = tnames

	activeSet := map[string]bool{}
	for _, tn := range tnames {
		tval, _ := targetsMap[tn].(map[string]any)
		scfg := tval
		if sub, ok := tval["skills"].(map[string]any); ok {
			scfg = sub
		}
		enabled := true
		if scfg != nil {
			if en, ok := scfg["enabled"].(bool); ok {
				enabled = en
			}
		}
		if !enabled {
			sum.TargetSkills[tn] = []string{}
			continue
		}
		sum.EnabledTargets = append(sum.EnabledTargets, tn)
		inc := yamlStrSlice(scfg["include"])
		exc := yamlStrSlice(scfg["exclude"])
		var matched []string
		for _, sn := range skillNames {
			if len(inc) > 0 && !matchAnyGlob(inc, sn) {
				continue
			}
			if len(exc) > 0 && matchAnyGlob(exc, sn) {
				continue
			}
			matched = append(matched, sn)
			activeSet[sn] = true
		}
		sum.TargetSkills[tn] = matched
	}
	for sn := range activeSet {
		sum.ActiveSkills = append(sum.ActiveSkills, sn)
	}
	sort.Strings(sum.ActiveSkills)
	return sum
}

func encodeYAML(doc map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return buf.Bytes(), nil
}

func collapseHomePath(p, home string) string {
	home = strings.TrimRight(home, "/")
	if home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// NormalizeHostConfigYAML normalizes a pulled or edited config.yaml so it is portable across macOS and NixOS:
// sets sources.skills to "<skillshareRoot>/skills" and collapses home prefixes to "~/".
func NormalizeHostConfigYAML(raw []byte, home, skillshareRoot string) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("invalid skillshare config.yaml: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	sroot := strings.TrimRight(skillshareRoot, "/")
	if sroot == "" {
		sroot = "~/.local/share/agent-config/skillshare"
	}
	sources, _ := doc["sources"].(map[string]any)
	if sources == nil {
		sources = map[string]any{}
	}
	sources["skills"] = sroot + "/skills"
	// Collapse home in any extra sources (e.g. agents, extras)
	for k, v := range sources {
		if k != "skills" {
			if s, ok := v.(string); ok {
				sources[k] = collapseHomePath(s, home)
			}
		}
	}
	doc["sources"] = sources
	delete(doc, "source") // legacy single-source key if present

	if targets, ok := doc["targets"].(map[string]any); ok {
		for tn, rawT := range targets {
			tm, ok := rawT.(map[string]any)
			if !ok || tm == nil {
				targets[tn] = map[string]any{}
				continue
			}
			if p, ok := tm["path"].(string); ok && p != "" {
				tm["path"] = collapseHomePath(p, home)
			}
			if scfg, ok := tm["skills"].(map[string]any); ok && scfg != nil {
				if p, ok := scfg["path"].(string); ok && p != "" {
					scfg["path"] = collapseHomePath(p, home)
				}
			}
			if acfg, ok := tm["agents"].(map[string]any); ok && acfg != nil {
				if p, ok := acfg["path"].(string); ok && p != "" {
					acfg["path"] = collapseHomePath(p, home)
				}
			}
		}
	}
	return encodeYAML(doc)
}

// PrepareSlotConfig converts a git-tracked host config.yaml into a working slot config where:
//   - sources.skills points directly at the REAL git repo skills directory (repoSkillsAbs)
//   - each target's skills.path points at <sandboxDir>/targets/<name> so preview syncs never touch controller ~/.claude
// Returns the working YAML and origPaths (original portable path per target, "" if omitted).
func PrepareSlotConfig(repoYAML []byte, repoSkillsAbs, sandboxDir string) ([]byte, map[string]string, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(repoYAML, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse host config.yaml: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	sources, _ := doc["sources"].(map[string]any)
	if sources == nil {
		sources = map[string]any{}
	}
	sources["skills"] = repoSkillsAbs
	doc["sources"] = sources
	delete(doc, "source")

	origPaths := map[string]string{}
	targets, _ := doc["targets"].(map[string]any)
	if targets == nil {
		targets = map[string]any{}
		doc["targets"] = targets
	}
	for tn, rawT := range targets {
		tm, _ := rawT.(map[string]any)
		if tm == nil {
			tm = map[string]any{}
			targets[tn] = tm
		}
		var orig string
		if scfg, ok := tm["skills"].(map[string]any); ok && scfg != nil {
			if p, ok := scfg["path"].(string); ok {
				orig = p
			}
		}
		if orig == "" {
			if p, ok := tm["path"].(string); ok {
				orig = p
			}
		}
		origPaths[tn] = orig

		// Redirect skills path into sandbox dir
		tdir := filepath.Join(sandboxDir, "targets", tn)
		_ = os.MkdirAll(tdir, 0o755)
		delete(tm, "path")
		scfg, _ := tm["skills"].(map[string]any)
		if scfg == nil {
			scfg = map[string]any{}
			tm["skills"] = scfg
		}
		scfg["path"] = tdir
	}
	b, err := encodeYAML(doc)
	return b, origPaths, err
}

// ReconcileSlotConfig reads the mutated slot config.yaml after a `skillshare ui` write operation,
// restores portable sources.skills & target paths for git, and redirects any newly-added targets into sandboxDir.
func ReconcileSlotConfig(slotYAML []byte, sandboxDir, localHome, skillshareRoot string, origPaths map[string]string) (repoYAML []byte, updatedSlotYAML []byte, slotChanged bool, err error) {
	var slotDoc map[string]any
	if err := yaml.Unmarshal(slotYAML, &slotDoc); err != nil {
		return nil, nil, false, err
	}
	if slotDoc == nil {
		slotDoc = map[string]any{}
	}
	// Deep copy via YAML round-trip for repoDoc
	var repoDoc map[string]any
	_ = yaml.Unmarshal(slotYAML, &repoDoc)
	if repoDoc == nil {
		repoDoc = map[string]any{}
	}

	sroot := strings.TrimRight(skillshareRoot, "/")
	if sroot == "" {
		sroot = "~/.local/share/agent-config/skillshare"
	}
	repoSources, _ := repoDoc["sources"].(map[string]any)
	if repoSources == nil {
		repoSources = map[string]any{}
	}
	repoSources["skills"] = sroot + "/skills"
	repoDoc["sources"] = repoSources
	delete(repoDoc, "source")

	sandboxTargetsPrefix := filepath.Clean(filepath.Join(sandboxDir, "targets")) + string(filepath.Separator)
	repoTargets, _ := repoDoc["targets"].(map[string]any)
	slotTargets, _ := slotDoc["targets"].(map[string]any)

	for tn, rawT := range repoTargets {
		tm, _ := rawT.(map[string]any)
		if tm == nil {
			repoTargets[tn] = map[string]any{}
			continue
		}
		// Extract current path from nested skills.path or flat path
		var curPath string
		scfg, hasSkills := tm["skills"].(map[string]any)
		if hasSkills && scfg != nil {
			if p, ok := scfg["path"].(string); ok {
				curPath = p
			}
		}
		flatPath, _ := tm["path"].(string)
		if curPath == "" && flatPath != "" {
			curPath = flatPath
		}

		isSandboxPath := strings.HasPrefix(filepath.Clean(curPath)+string(filepath.Separator), sandboxTargetsPrefix) ||
			strings.HasPrefix(curPath, sandboxTargetsPrefix)

		if isSandboxPath {
			orig := origPaths[tn]
			delete(tm, "path")
			if hasSkills && scfg != nil {
				if orig != "" {
					scfg["path"] = orig
				} else {
					delete(scfg, "path")
					if len(scfg) == 0 {
						delete(tm, "skills")
					}
				}
			}
		} else {
			// Newly added target or user explicitly changed target path!
			portable := collapseHomePath(curPath, localHome)
			if portable == "" && tn == "codex" {
				portable = "~/.codex/skills"
			}
			origPaths[tn] = portable
			if portable != "" {
				delete(tm, "path")
				if !hasSkills || scfg == nil {
					scfg = map[string]any{}
					tm["skills"] = scfg
				}
				scfg["path"] = portable
			} else {
				delete(tm, "path")
				if hasSkills && scfg != nil {
					delete(scfg, "path")
					if len(scfg) == 0 {
						delete(tm, "skills")
					}
				}
			}
			// Redirect the slot config's target path into sandboxDir so future preview syncs stay isolated
			if slotTargets != nil {
				stm, _ := slotTargets[tn].(map[string]any)
				if stm == nil {
					stm = map[string]any{}
					slotTargets[tn] = stm
				}
				delete(stm, "path")
				sscfg, _ := stm["skills"].(map[string]any)
				if sscfg == nil {
					sscfg = map[string]any{}
					stm["skills"] = sscfg
				}
				tdir := filepath.Join(sandboxDir, "targets", tn)
				_ = os.MkdirAll(tdir, 0o755)
				sscfg["path"] = tdir
				slotChanged = true
			}
		}
	}

	// Clean up origPaths for removed targets
	for tn := range origPaths {
		if _, exists := repoTargets[tn]; !exists {
			delete(origPaths, tn)
		}
	}

	repoYAML, err = encodeYAML(repoDoc)
	if err != nil {
		return nil, nil, false, err
	}
	if slotChanged {
		updatedSlotYAML, err = encodeYAML(slotDoc)
		if err != nil {
			return nil, nil, false, err
		}
	}
	return repoYAML, updatedSlotYAML, slotChanged, nil
}
