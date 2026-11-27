package fleet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/remote"
	"github.com/bowd/livery/internal/skillshare"
	"gopkg.in/yaml.v3"
)

type DriftItem struct {
	ID          string   `json:"id"`          // unique key e.g. "skill:foo" | "mcp:bar" | "targets"
	Category    string   `json:"category"`    // skills | mcp | hooks | targets | local_extra
	Kind        string   `json:"kind"`        // added | modified | removed | changed
	Name        string   `json:"name"`        // item name
	Detail      string   `json:"detail"`      // human-readable description
	Actions     []string `json:"actions"`     // allowed resolution actions
	DefaultAct  string   `json:"default_act"` // recommended default action
}

type EnvDriftReport struct {
	Target   string      `json:"target"`
	OK       bool        `json:"ok"`
	HasGit   bool        `json:"has_git"`
	HasBase  bool        `json:"has_base"`
	BaseSHA  string      `json:"base_sha,omitempty"`
	HeadSHA  string      `json:"head_sha,omitempty"`
	HasDrift bool        `json:"has_drift"`
	Items    []DriftItem `json:"items"`
	Error    string      `json:"error,omitempty"`
}

type BackwardDecision struct {
	ID     string `json:"id"`     // matches DriftItem.ID
	Action string `json:"action"` // import_to_catalog | update_catalog | fork_to_catalog | disable_in_matrix | keep_local | save_env_override | ignore
}

type BackwardApplyReport struct {
	Target    string                `json:"target"`
	OK        bool                  `json:"ok"`
	Applied   []string              `json:"applied"`
	Conflicts []model.MatrixConflict `json:"conflicts"`
	Error     string                `json:"error,omitempty"`
}

// InspectEnvDrift compares an environment's live skillshare git repo against its `livery-base` snapshot
// and classifies every local change into structured backward-sync items.
func InspectEnvDrift(repo *model.Repo, t *model.Target) EnvDriftReport {
	rep := EnvDriftReport{Target: t.ID, Items: []DriftItem{}}
	snap, err := remote.InspectEnvSkillshareGit(t, t.SkillshareRoot)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	rep.OK = true
	rep.HasGit = snap.HasGit
	rep.HasBase = snap.HasBase
	rep.BaseSHA = snap.BaseSHA
	rep.HeadSHA = snap.HeadSHA
	if !snap.HasGit || !snap.HasBase {
		return rep
	}

	// 1. Classify skill directory changes from git diff + untracked files
	skillStatus := map[string]string{} // name -> added | modified | removed
	skillFileKinds := map[string]map[string]bool{}
	recordSkillFile := func(relPath, status string) {
		if !strings.HasPrefix(relPath, "skills/") {
			return
		}
		rest := strings.TrimPrefix(relPath, "skills/")
		name, _, _ := strings.Cut(rest, "/")
		if name == "" || strings.HasPrefix(name, ".") {
			return
		}
		if skillFileKinds[name] == nil {
			skillFileKinds[name] = map[string]bool{}
		}
		skillFileKinds[name][status] = true
	}
	for _, ln := range snap.DiffLines {
		parts := strings.Fields(ln)
		if len(parts) < 2 {
			continue
		}
		st := parts[0][:1]
		p := parts[len(parts)-1]
		recordSkillFile(p, st)
	}
	for _, p := range snap.Untracked {
		recordSkillFile(p, "A")
	}

	cat := skillshare.LoadCatalog(repo.Root)
	catSkillSet := map[string]bool{}
	for _, s := range cat.Skills {
		catSkillSet[s.Name] = true
	}

	var skillNames []string
	for n := range skillFileKinds {
		skillNames = append(skillNames, n)
	}
	sort.Strings(skillNames)
	for _, name := range skillNames {
		kinds := skillFileKinds[name]
		if kinds["D"] && !kinds["A"] && !kinds["M"] {
			skillStatus[name] = "removed"
		} else if kinds["A"] && !kinds["M"] && !kinds["D"] && !catSkillSet[name] {
			skillStatus[name] = "added"
		} else {
			skillStatus[name] = "modified"
		}
	}

	for _, name := range skillNames {
		switch skillStatus[name] {
		case "added":
			rep.Items = append(rep.Items, DriftItem{
				ID:         "skill:" + name,
				Category:   "skills",
				Kind:       "added",
				Name:       name,
				Detail:     fmt.Sprintf("New skill %q installed locally on %s", name, t.ID),
				Actions:    []string{"import_to_catalog", "keep_local", "ignore"},
				DefaultAct: "import_to_catalog",
			})
		case "modified":
			rep.Items = append(rep.Items, DriftItem{
				ID:         "skill:" + name,
				Category:   "skills",
				Kind:       "modified",
				Name:       name,
				Detail:     fmt.Sprintf("Skill %q modified locally on %s", name, t.ID),
				Actions:    []string{"update_catalog", "fork_to_catalog", "keep_local", "ignore"},
				DefaultAct: "update_catalog",
			})
		case "removed":
			rep.Items = append(rep.Items, DriftItem{
				ID:         "skill:" + name,
				Category:   "skills",
				Kind:       "removed",
				Name:       name,
				Detail:     fmt.Sprintf("Skill %q removed locally on %s", name, t.ID),
				Actions:    []string{"disable_in_matrix", "ignore"},
				DefaultAct: "disable_in_matrix",
			})
		}
	}

	// 2. Diff base config.yaml vs live config.yaml for MCP, Hooks, Targets, and non-library local extras
	var baseDoc, liveDoc map[string]any
	_ = yaml.Unmarshal([]byte(snap.BaseConfig), &baseDoc)
	_ = yaml.Unmarshal([]byte(snap.LiveConfig), &liveDoc)
	if baseDoc == nil {
		baseDoc = map[string]any{}
	}
	if liveDoc == nil {
		liveDoc = map[string]any{}
	}

	diffSectionMap := func(catName, secKey, subKey string) {
		baseMap := nestedMap(baseDoc, secKey, subKey)
		liveMap := nestedMap(liveDoc, secKey, subKey)
		seen := map[string]bool{}
		var keys []string
		for k := range baseMap {
			seen[k] = true
			keys = append(keys, k)
		}
		for k := range liveMap {
			if !seen[k] {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			bv, inBase := baseMap[k]
			lv, inLive := liveMap[k]
			switch {
			case !inBase && inLive:
				rep.Items = append(rep.Items, DriftItem{
					ID:         catName + ":" + k,
					Category:   catName,
					Kind:       "added",
					Name:       k,
					Detail:     fmt.Sprintf("New %s %q added locally on %s", catName, k, t.ID),
					Actions:    []string{"import_to_catalog", "keep_local", "ignore"},
					DefaultAct: "import_to_catalog",
				})
			case inBase && !inLive:
				rep.Items = append(rep.Items, DriftItem{
					ID:         catName + ":" + k,
					Category:   catName,
					Kind:       "removed",
					Name:       k,
					Detail:     fmt.Sprintf("%s %q removed locally on %s", strings.ToUpper(catName), k, t.ID),
					Actions:    []string{"disable_in_matrix", "ignore"},
					DefaultAct: "disable_in_matrix",
				})
			case inBase && inLive && !reflect.DeepEqual(bv, lv):
				rep.Items = append(rep.Items, DriftItem{
					ID:         catName + ":" + k,
					Category:   catName,
					Kind:       "modified",
					Name:       k,
					Detail:     fmt.Sprintf("%s %q modified locally on %s", strings.ToUpper(catName), k, t.ID),
					Actions:    []string{"update_catalog", "keep_local", "ignore"},
					DefaultAct: "update_catalog",
				})
			}
		}
	}
	diffSectionMap("mcp", "mcp", "servers")
	diffSectionMap("hooks", "hooks", "entries")
	diffSectionMap("plugins", "plugins", "packages")

	// Targets or sync mode changed locally?
	if !reflect.DeepEqual(baseDoc["targets"], liveDoc["targets"]) || fmt.Sprint(baseDoc["mode"]) != fmt.Sprint(liveDoc["mode"]) {
		rep.Items = append(rep.Items, DriftItem{
			ID:         "targets:config",
			Category:   "targets",
			Kind:       "changed",
			Name:       "targets & mode",
			Detail:     fmt.Sprintf("Harness targets or sync mode changed locally on %s", t.ID),
			Actions:    []string{"save_env_override", "ignore"},
			DefaultAct: "save_env_override",
		})
	}

	// Skillshare native projects changed locally?
	if !reflect.DeepEqual(baseDoc["projects"], liveDoc["projects"]) {
		rep.Items = append(rep.Items, DriftItem{
			ID:         "projects:config",
			Category:   "projects",
			Kind:       "changed",
			Name:       "skillshare projects",
			Detail:     fmt.Sprintf("Registered Skillshare projects changed locally on %s", t.ID),
			Actions:    []string{"save_env_override", "ignore"},
			DefaultAct: "save_env_override",
		})
	}

	// Any other non-library top-level keys (audit, hub, gitlab_hosts, extras, etc.)
	reserved := map[string]bool{"mode": true, "git_root": true, "sources": true, "source": true, "extras_source": true, "targets": true, "projects": true, "mcp": true, "hooks": true, "plugins": true}
	var extraChanged []string
	for k, lv := range liveDoc {
		if reserved[k] {
			continue
		}
		if !reflect.DeepEqual(baseDoc[k], lv) {
			extraChanged = append(extraChanged, k)
		}
	}
	sort.Strings(extraChanged)
	if len(extraChanged) > 0 {
		rep.Items = append(rep.Items, DriftItem{
			ID:         "local_extra:config",
			Category:   "local_extra",
			Kind:       "changed",
			Name:       strings.Join(extraChanged, ", "),
			Detail:     fmt.Sprintf("Host-specific Skillshare config (%s) modified locally", strings.Join(extraChanged, ", ")),
			Actions:    []string{"save_env_override", "ignore"},
			DefaultAct: "save_env_override",
		})
	}

	rep.HasDrift = len(rep.Items) > 0
	return rep
}

func nestedMap(doc map[string]any, k1, k2 string) map[string]any {
	m1, _ := doc[k1].(map[string]any)
	if m1 == nil {
		return map[string]any{}
	}
	m2, _ := m1[k2].(map[string]any)
	if m2 == nil {
		return map[string]any{}
	}
	return m2
}

// ApplyBackwardSync applies user decisions for local changes detected on an environment back into
// the XDG Catalog, Matrix, and env-local overrides.
func ApplyBackwardSync(repo *model.Repo, t *model.Target, decisions []BackwardDecision) BackwardApplyReport {
	out := BackwardApplyReport{Target: t.ID, Applied: []string{}, Conflicts: []model.MatrixConflict{}}
	snap, err := remote.InspectEnvSkillshareGit(t, t.SkillshareRoot)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	var liveDoc map[string]any
	_ = yaml.Unmarshal([]byte(snap.LiveConfig), &liveDoc)
	if liveDoc == nil {
		liveDoc = map[string]any{}
	}
	sroot := strings.TrimRight(t.SkillshareRoot, "/")
	if sroot == "" {
		sroot = "~/.local/share/agent-config/skillshare"
	}
	skillsAbs := sroot + "/skills"
	if strings.HasPrefix(skillsAbs, "~/") && snap.Home != "" {
		skillsAbs = filepath.Join(snap.Home, skillsAbs[2:])
	}

	ov := skillshare.LoadEnvOverride(repo.Root, t)
	ovDirty := false
	catCfgDirty := false
	cat := skillshare.LoadCatalog(repo.Root)
	catDoc := cat.RawConfig
	if catDoc == nil {
		catDoc = map[string]any{}
	}

	shortHost := t.ID
	if _, h, ok := strings.Cut(t.ID, "@"); ok && h != "" {
		shortHost = h
	}

	for _, d := range decisions {
		if d.Action == "" || d.Action == "ignore" {
			continue
		}
		catKind, name, _ := strings.Cut(d.ID, ":")
		switch catKind {
		case "skill":
			srcSkill := filepath.Join(skillsAbs, name)
			switch d.Action {
			case "import_to_catalog", "update_catalog":
				dst := filepath.Join(skillshare.CatalogSkillsDir(repo.Root), name)
				if _, err := remote.RsyncPullDir(t, srcSkill, dst, false, true); err != nil {
					out.Error = err.Error()
					return out
				}
				if d.Action == "import_to_catalog" {
					if _, err := model.SetMatrixCell(repo.Root, "skills", name, t.ID, true); err != nil {
						out.Error = err.Error()
						return out
					}
					out.Applied = append(out.Applied, fmt.Sprintf("imported skill %q to Catalog & enabled on %s", name, t.ID))
				} else {
					out.Applied = append(out.Applied, fmt.Sprintf("updated Catalog skill %q from %s", name, t.ID))
				}
			case "fork_to_catalog":
				forkName := name + "-" + shortHost
				dst := filepath.Join(skillshare.CatalogSkillsDir(repo.Root), forkName)
				if _, err := remote.RsyncPullDir(t, srcSkill, dst, false, true); err != nil {
					out.Error = err.Error()
					return out
				}
				if _, err := model.SetMatrixCell(repo.Root, "skills", forkName, t.ID, true); err != nil {
					out.Error = err.Error()
					return out
				}
				out.Applied = append(out.Applied, fmt.Sprintf("forked skill %q as %q & enabled on %s", name, forkName, t.ID))
			case "disable_in_matrix":
				conf, err := model.SetMatrixCell(repo.Root, "skills", name, t.ID, false)
				if err != nil {
					out.Error = err.Error()
					return out
				}
				if conf != nil {
					out.Conflicts = append(out.Conflicts, *conf)
				} else {
					out.Applied = append(out.Applied, fmt.Sprintf("disabled skill %q on %s", name, t.ID))
				}
			case "keep_local":
				found := false
				for _, ls := range ov.LocalSkills {
					if ls == name {
						found = true
						break
					}
				}
				if !found {
					ov.LocalSkills = append(ov.LocalSkills, name)
					sort.Strings(ov.LocalSkills)
					ovDirty = true
				}
				out.Applied = append(out.Applied, fmt.Sprintf("preserved %q as local-only skill on %s", name, t.ID))
			}

		case "mcp":
			liveMCP := nestedMap(liveDoc, "mcp", "servers")
			switch d.Action {
			case "import_to_catalog", "update_catalog":
				if val, ok := liveMCP[name]; ok {
					mcpSec, _ := catDoc["mcp"].(map[string]any)
					if mcpSec == nil {
						mcpSec = map[string]any{}
						catDoc["mcp"] = mcpSec
					}
					srvs, _ := mcpSec["servers"].(map[string]any)
					if srvs == nil {
						srvs = map[string]any{}
						mcpSec["servers"] = srvs
					}
					srvs[name] = val
					catCfgDirty = true
					if d.Action == "import_to_catalog" {
						_, _ = model.SetMatrixCell(repo.Root, "mcp", name, t.ID, true)
						out.Applied = append(out.Applied, fmt.Sprintf("imported MCP %q to Catalog & enabled on %s", name, t.ID))
					} else {
						out.Applied = append(out.Applied, fmt.Sprintf("updated Catalog MCP %q from %s", name, t.ID))
					}
				}
			case "disable_in_matrix":
				conf, err := model.SetMatrixCell(repo.Root, "mcp", name, t.ID, false)
				if err != nil {
					out.Error = err.Error()
					return out
				}
				if conf != nil {
					out.Conflicts = append(out.Conflicts, *conf)
				} else {
					out.Applied = append(out.Applied, fmt.Sprintf("disabled MCP %q on %s", name, t.ID))
				}
			case "keep_local":
				if val, ok := liveMCP[name]; ok {
					ov.LocalMCP[name] = val
					ovDirty = true
					out.Applied = append(out.Applied, fmt.Sprintf("saved MCP %q as local-only override on %s", name, t.ID))
				}
			}

		case "hooks":
			liveHooks := nestedMap(liveDoc, "hooks", "entries")
			switch d.Action {
			case "import_to_catalog", "update_catalog":
				if val, ok := liveHooks[name]; ok {
					hSec, _ := catDoc["hooks"].(map[string]any)
					if hSec == nil {
						hSec = map[string]any{}
						catDoc["hooks"] = hSec
					}
					ents, _ := hSec["entries"].(map[string]any)
					if ents == nil {
						ents = map[string]any{}
						hSec["entries"] = ents
					}
					ents[name] = val
					catCfgDirty = true
					if d.Action == "import_to_catalog" {
						_, _ = model.SetMatrixCell(repo.Root, "hooks", name, t.ID, true)
						out.Applied = append(out.Applied, fmt.Sprintf("imported hook %q to Catalog & enabled on %s", name, t.ID))
					} else {
						out.Applied = append(out.Applied, fmt.Sprintf("updated Catalog hook %q from %s", name, t.ID))
					}
				}
			case "disable_in_matrix":
				conf, err := model.SetMatrixCell(repo.Root, "hooks", name, t.ID, false)
				if err != nil {
					out.Error = err.Error()
					return out
				}
				if conf != nil {
					out.Conflicts = append(out.Conflicts, *conf)
				} else {
					out.Applied = append(out.Applied, fmt.Sprintf("disabled hook %q on %s", name, t.ID))
				}
			case "keep_local":
				if val, ok := liveHooks[name]; ok {
					ov.LocalHooks[name] = val
					ovDirty = true
					out.Applied = append(out.Applied, fmt.Sprintf("saved hook %q as local-only override on %s", name, t.ID))
				}
			}

		case "plugins":
			livePlugins := nestedMap(liveDoc, "plugins", "packages")
			switch d.Action {
			case "import_to_catalog", "update_catalog":
				if val, ok := livePlugins[name]; ok {
					pSec, _ := catDoc["plugins"].(map[string]any)
					if pSec == nil {
						pSec = map[string]any{"version": 2}
						catDoc["plugins"] = pSec
					}
					if _, hasVer := pSec["version"]; !hasVer {
						pSec["version"] = 2
					}
					pkgs, _ := pSec["packages"].(map[string]any)
					if pkgs == nil {
						pkgs = map[string]any{}
						pSec["packages"] = pkgs
					}
					pkgs[name] = val
					catCfgDirty = true
					if d.Action == "import_to_catalog" {
						_, _ = model.SetMatrixCell(repo.Root, "plugins", name, t.ID, true)
						out.Applied = append(out.Applied, fmt.Sprintf("imported plugin %q to Catalog & enabled on %s", name, t.ID))
					} else {
						out.Applied = append(out.Applied, fmt.Sprintf("updated Catalog plugin %q from %s", name, t.ID))
					}
				}
			case "disable_in_matrix":
				conf, err := model.SetMatrixCell(repo.Root, "plugins", name, t.ID, false)
				if err != nil {
					out.Error = err.Error()
					return out
				}
				if conf != nil {
					out.Conflicts = append(out.Conflicts, *conf)
				} else {
					out.Applied = append(out.Applied, fmt.Sprintf("disabled plugin %q on %s", name, t.ID))
				}
			case "keep_local":
				if val, ok := livePlugins[name]; ok {
					ov.LocalPlugins[name] = val
					ovDirty = true
					out.Applied = append(out.Applied, fmt.Sprintf("saved plugin %q as local-only override on %s", name, t.ID))
				}
			}

		case "targets":
			if d.Action == "save_env_override" {
				if normBytes, err := skillshare.NormalizeHostConfigYAML([]byte(snap.LiveConfig), snap.Home, sroot); err == nil {
					var normDoc map[string]any
					if yaml.Unmarshal(normBytes, &normDoc) == nil {
						if m, ok := normDoc["mode"].(string); ok && m != "" {
							ov.Mode = m
						}
						if tm, ok := normDoc["targets"].(map[string]any); ok {
							ov.Targets = tm
						}
						ovDirty = true
						out.Applied = append(out.Applied, fmt.Sprintf("saved target & mode overrides for %s", t.ID))
					}
				}
			}

		case "projects":
			if d.Action == "save_env_override" {
				if pm, ok := liveDoc["projects"].(map[string]any); ok {
					ov.Projects = pm
				} else {
					ov.Projects = map[string]any{}
				}
				ovDirty = true
				out.Applied = append(out.Applied, fmt.Sprintf("saved Skillshare project registrations for %s", t.ID))
			}

		case "local_extra":
			if d.Action == "save_env_override" {
				reserved := map[string]bool{"mode": true, "git_root": true, "sources": true, "source": true, "extras_source": true, "targets": true, "projects": true, "mcp": true, "hooks": true, "plugins": true}
				for k, v := range liveDoc {
					if !reserved[k] {
						ov.ExtraConfig[k] = v
					}
				}
				ovDirty = true
				out.Applied = append(out.Applied, fmt.Sprintf("saved host-local extras (%s) for %s", name, t.ID))
			}
		}
	}

	if catCfgDirty {
		if b, err := yaml.Marshal(catDoc); err == nil {
			_ = os.WriteFile(skillshare.CatalogConfigPath(repo.Root), b, 0o644)
			skillshare.NormalizeCatalogLibraryConfig(repo.Root)
		}
	}
	if ovDirty {
		if err := skillshare.SaveEnvOverride(repo.Root, t.ID, ov); err != nil {
			out.Error = err.Error()
			return out
		}
	}
	// Commit any updates inside catalog/skillshare standalone git repo cleanly
	catDir := skillshare.CatalogDir(repo.Root)
	if _, err := os.Stat(filepath.Join(catDir, ".git")); err == nil && len(out.Applied) > 0 {
		_ = exec.Command("git", "-C", catDir, "add", "-A").Run()
		_ = exec.Command("git", "-C", catDir, "commit", "-q", "-m", "Backward sync from "+t.ID).Run()
	}
	if len(out.Conflicts) == 0 && len(out.Applied) > 0 {
		_, _ = remote.CommitEnvSkillshareBase(t, t.SkillshareRoot, "backward-sync")
	}

	out.OK = len(out.Conflicts) == 0
	return out
}
