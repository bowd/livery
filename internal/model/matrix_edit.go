package model

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MatrixEnv represents one destination column in the Matrix (either an OS-user Host or a Project overlay).
type MatrixEnv struct {
	ID         string `json:"id"`                   // e.g. "dev@macbook" or "project:dev@macbook/demo-app"
	Kind       string `json:"kind"`                 // "host" | "project"
	Label      string `json:"label"`                // short display label
	SubLabel   string `json:"sublabel"`             // secondary label (hostname or parent host)
	HostTarget string `json:"host_target,omitempty"` // for projects: owning host target ID
	Path       string `json:"path,omitempty"`       // for projects: filesystem path
}

// MatrixCell describes whether an item is enabled on an environment and why.
type MatrixCell struct {
	Enabled   bool   `json:"enabled"`
	Source    string `json:"source"`            // "none" | "direct" | "profile"
	Profile   string `json:"profile,omitempty"` // profile ID if Source == "profile"
	Supported bool   `json:"supported"`
}

// MatrixRow is one catalog item row across all Matrix environments.
type MatrixRow struct {
	ID          string                `json:"id"`
	Label       string                `json:"label"`
	Description string                `json:"description,omitempty"`
	Category    string                `json:"category"`
	AllSelected bool                  `json:"all_selected"`
	Cells       map[string]MatrixCell `json:"cells"`
}

// MatrixConflict is returned when a user tries to disable an item inherited from a Profile.
type MatrixConflict struct {
	Category string `json:"category"`
	Item     string `json:"item"`
	Env      string `json:"env"`
	Profile  string `json:"profile"`
	Message  string `json:"message"`
}

func ProjectEnvID(targetID, slug string) string {
	return "project:" + targetID + "/" + slug
}

func ParseProjectEnvID(envID string) (targetID, slug string, ok bool) {
	if !strings.HasPrefix(envID, "project:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(envID, "project:")
	targetID, slug, ok = strings.Cut(rest, "/")
	return targetID, slug, ok && targetID != "" && slug != ""
}

// CategorySupportsProjects reports which Matrix categories can be assigned to Project environments.
func CategorySupportsProjects(cat string) bool {
	return cat == "skills" || cat == "mcp"
}

// AddRefForCategory encodes a direct assignment token in inventory.toml `add = [...]`.
func AddRefForCategory(category, itemID string) string {
	switch category {
	case "skills":
		return "skill:" + NormalizeSkillName(itemID)
	case "mcp":
		return "mcp:" + itemID
	case "hooks":
		return "hook:" + itemID
	case "agents":
		return "agent:" + itemID
	case "extras":
		return "extra:" + itemID
	case "plugins":
		return "plugin:" + itemID
	case "launchers":
		return "launcher:" + itemID
	case "prompts":
		return itemID
	default:
		return itemID
	}
}

// ResolveHostCategoryItems returns the ordered enabled items for one host & category,
// along with each item's provenance ("direct" vs "profile:<id>").
func (r *Repo) ResolveHostCategoryItems(t *Target, category string) (items []string, sourceByItem map[string]string) {
	sourceByItem = map[string]string{}
	pids, _ := r.ExpandProfiles(t.Profiles)

	// Profiles category itself is purely direct on t.Profiles
	if category == "profiles" {
		for _, pid := range t.Profiles {
			if _, seen := sourceByItem[pid]; !seen {
				sourceByItem[pid] = "direct"
				items = append(items, pid)
			}
		}
		return items, sourceByItem
	}

	addFrom := func(id, src string) {
		if id == "" {
			return
		}
		if _, seen := sourceByItem[id]; !seen {
			sourceByItem[id] = src
			items = append(items, id)
		}
	}

	// 1. Inherited from profiles (track which top-level t.Profiles entry pulled it in so guardrails act cleanly!)
	for _, topPID := range t.Profiles {
		expanded, err := r.ExpandProfiles([]string{topPID})
		if err != nil {
			continue
		}
		for _, pid := range expanded {
			pr := r.Profiles[pid]
			if pr == nil {
				continue
			}
			switch category {
			case "skills":
				for _, s := range pr.Skills {
					addFrom(NormalizeSkillName(s), "profile:"+topPID)
				}
			case "mcp":
				for _, m := range pr.MCP {
					addFrom(m, "profile:"+topPID)
				}
			case "hooks":
				for _, h := range pr.Hooks {
					addFrom(h, "profile:"+topPID)
				}
			case "agents":
				for _, a := range pr.Agents {
					addFrom(a, "profile:"+topPID)
				}
			case "extras":
				for _, e := range pr.Extras {
					addFrom(e, "profile:"+topPID)
				}
			case "plugins":
				for _, pl := range pr.Plugins {
					addFrom(pl, "profile:"+topPID)
				}
			case "prompts":
				for _, f := range pr.Fragments {
					addFrom(f, "profile:"+topPID)
				}
			case "launchers":
				for _, l := range pr.Launchers {
					addFrom(l, "profile:"+topPID)
				}
			}
		}
	}
	_ = pids

	// 2. Direct additions in t.Add
	for _, raw := range t.Add {
		switch category {
		case "skills":
			if IsSkillRef(raw) {
				addFrom(NormalizeSkillName(raw), "direct")
			}
		case "mcp":
			if strings.HasPrefix(raw, "mcp:") {
				addFrom(strings.TrimPrefix(raw, "mcp:"), "direct")
			}
		case "hooks":
			if strings.HasPrefix(raw, "hook:") {
				addFrom(strings.TrimPrefix(raw, "hook:"), "direct")
			}
		case "agents":
			if strings.HasPrefix(raw, "agent:") {
				addFrom(strings.TrimPrefix(raw, "agent:"), "direct")
			}
		case "extras":
			if strings.HasPrefix(raw, "extra:") {
				addFrom(strings.TrimPrefix(raw, "extra:"), "direct")
			}
		case "plugins":
			if strings.HasPrefix(raw, "plugin:") {
				addFrom(strings.TrimPrefix(raw, "plugin:"), "direct")
			}
		case "launchers":
			if strings.HasPrefix(raw, "launcher:") {
				addFrom(strings.TrimPrefix(raw, "launcher:"), "direct")
			}
		case "prompts":
			if !IsSkillRef(raw) && !IsEntryRef(raw) {
				addFrom(raw, "direct")
			}
		}
	}

	// 3. Respect legacy t.Remove entries if any exist in inventory.toml
	if len(t.Remove) > 0 {
		var filtered []string
		for _, id := range items {
			removed := false
			for _, rem := range t.Remove {
				if rem == id || rem == AddRefForCategory(category, id) || (category == "skills" && NormalizeSkillName(rem) == id) {
					removed = true
					break
				}
			}
			if removed {
				delete(sourceByItem, id)
			} else {
				filtered = append(filtered, id)
			}
		}
		items = filtered
	}
	if category == "prompts" && len(r.FragmentOrder) > 0 {
		items = r.SortFragmentsCanonical(items)
	}
	return items, sourceByItem
}

// BuildMatrixEnvs returns all Host environments as ordered Matrix columns.
// Project folders are managed natively via stock Skillshare (`projects:` in each host's config.yaml).
func (r *Repo) BuildMatrixEnvs() []MatrixEnv {
	var out []MatrixEnv
	for _, t := range r.Targets {
		user, host, _ := strings.Cut(t.ID, "@")
		out = append(out, MatrixEnv{
			ID:       t.ID,
			Kind:     "host",
			Label:    user,
			SubLabel: host,
		})
	}
	return out
}

// BuildMatrixRows constructs interactive Matrix rows for one category.
func (r *Repo) BuildMatrixRows(category string, itemIDs []string, descriptions map[string]string) []MatrixRow {
	envs := r.BuildMatrixEnvs()
	hostSources := map[string]map[string]string{}
	for _, t := range r.Targets {
		_, srcMap := r.ResolveHostCategoryItems(t, category)
		hostSources[t.ID] = srcMap
	}
	ovByEnv := map[string]*Overlay{}
	for _, ov := range r.Overlays {
		ovByEnv[ProjectEnvID(ov.Target, ov.Slug)] = ov
	}

	rows := make([]MatrixRow, 0, len(itemIDs))
	for _, id := range itemIDs {
		cells := map[string]MatrixCell{}
		supportedCount := 0
		enabledCount := 0
		for _, env := range envs {
			if env.Kind == "host" {
				supportedCount++
				src := hostSources[env.ID][id]
				cell := MatrixCell{Supported: true, Source: "none"}
				if src == "direct" {
					cell.Enabled = true
					cell.Source = "direct"
					enabledCount++
				} else if strings.HasPrefix(src, "profile:") {
					cell.Enabled = true
					cell.Source = "profile"
					cell.Profile = strings.TrimPrefix(src, "profile:")
					enabledCount++
				}
				cells[env.ID] = cell
			} else {
				sup := CategorySupportsProjects(category)
				cell := MatrixCell{Supported: sup, Source: "none"}
				if sup {
					supportedCount++
					if ov := ovByEnv[env.ID]; ov != nil {
						var list []string
						if category == "mcp" {
							list = ov.MCP
						} else if category == "skills" {
							list = ov.Skills
						}
						if containsStr(list, id) {
							cell.Enabled = true
							cell.Source = "direct"
							enabledCount++
						}
					}
				}
				cells[env.ID] = cell
			}
		}
		rows = append(rows, MatrixRow{
			ID:          id,
			Label:       id,
			Description: descriptions[id],
			Category:    category,
			AllSelected: supportedCount > 0 && enabledCount == supportedCount,
			Cells:       cells,
		})
	}
	return rows
}

func matchesAddEntry(raw, category, itemID string) bool {
	if raw == AddRefForCategory(category, itemID) {
		return true
	}
	if category == "skills" && IsSkillRef(raw) && NormalizeSkillName(raw) == NormalizeSkillName(itemID) {
		return true
	}
	if category == "prompts" && raw == itemID {
		return true
	}
	return false
}

// SetMatrixCell toggles one (category, itemID, envID) cell with Profile inheritance guardrails.
// Returns a non-nil *MatrixConflict (with nil error) if disabling an item inherited from a profile.
func SetMatrixCell(repoRoot, category, itemID, envID string, enabled bool) (*MatrixConflict, error) {
	repo, err := LoadRepo(repoRoot)
	if err != nil {
		return nil, err
	}
	// Project environment?
	if targetID, slug, isProj := ParseProjectEnvID(envID); isProj {
		if !CategorySupportsProjects(category) {
			return nil, Errf("category %q is not supported on project environments", category)
		}
		return nil, updateProjectItem(repoRoot, targetID, slug, category, itemID, enabled)
	}

	t, err := repo.Target(envID)
	if err != nil {
		return nil, err
	}

	// Profiles category directly mutates t.Profiles
	if category == "profiles" {
		if _, ok := repo.Profiles[itemID]; !ok {
			return nil, Errf("unknown profile %q", itemID)
		}
		next := append([]string(nil), t.Profiles...)
		if enabled {
			if !containsStr(next, itemID) {
				if itemID == "base" {
					next = append([]string{"base"}, next...)
				} else {
					next = append(next, itemID)
				}
			}
		} else {
			next = filterOut(next, []string{itemID})
		}
		_, err := UpdateTargetInInventory(repoRoot, envID, map[string]any{"profiles": next})
		return nil, err
	}

	_, srcMap := repo.ResolveHostCategoryItems(t, category)
	curSrc := srcMap[itemID]

	if enabled {
		// Clear any legacy remove entry for this item if present
		var nextRem []string
		remChanged := false
		for _, rem := range t.Remove {
			if matchesAddEntry(rem, category, itemID) || rem == itemID {
				remChanged = true
				continue
			}
			nextRem = append(nextRem, rem)
		}
		if curSrc != "" && !remChanged {
			return nil, nil // already enabled (direct or via profile)
		}
		patch := map[string]any{}
		if remChanged {
			patch["remove"] = nonNilSlice(nextRem)
		}
		if curSrc == "" {
			ref := AddRefForCategory(category, itemID)
			nextAdd := append([]string(nil), t.Add...)
			if !containsStr(nextAdd, ref) {
				nextAdd = append(nextAdd, ref)
			}
			patch["add"] = nextAdd
		}
		if len(patch) > 0 {
			_, err := UpdateTargetInInventory(repoRoot, envID, patch)
			return nil, err
		}
		return nil, nil
	}

	// Disabling: check Profile guardrail!
	if strings.HasPrefix(curSrc, "profile:") {
		profID := strings.TrimPrefix(curSrc, "profile:")
		return &MatrixConflict{
			Category: category,
			Item:     itemID,
			Env:      envID,
			Profile:  profID,
			Message:  fmt.Sprintf("%q is enabled on %s via profile %q. Disable profile %q on this environment, or duplicate it without %q.", itemID, envID, profID, profID, itemID),
		}, nil
	}
	if curSrc == "" {
		return nil, nil // already disabled
	}
	// Direct item -> remove from t.Add
	var nextAdd []string
	for _, raw := range t.Add {
		if matchesAddEntry(raw, category, itemID) {
			continue
		}
		nextAdd = append(nextAdd, raw)
	}
	_, err = UpdateTargetInInventory(repoRoot, envID, map[string]any{"add": nonNilSlice(nextAdd)})
	return nil, err
}

// SetMatrixRow toggles an entire row (Select All / Select None) across all supported environments.
// When disabling, environments inheriting the item via a Profile are left untouched and returned in skippedProfileEnvs.
func SetMatrixRow(repoRoot, category, itemID string, enableAll bool) (updatedEnvs []string, skippedProfileEnvs []string, err error) {
	repo, err := LoadRepo(repoRoot)
	if err != nil {
		return nil, nil, err
	}
	for _, env := range repo.BuildMatrixEnvs() {
		if env.Kind == "project" && !CategorySupportsProjects(category) {
			continue
		}
		conflict, cerr := SetMatrixCell(repoRoot, category, itemID, env.ID, enableAll)
		if cerr != nil {
			return updatedEnvs, skippedProfileEnvs, cerr
		}
		if conflict != nil {
			skippedProfileEnvs = append(skippedProfileEnvs, fmt.Sprintf("%s (%s)", env.ID, conflict.Profile))
			continue
		}
		updatedEnvs = append(updatedEnvs, env.ID)
	}
	return updatedEnvs, skippedProfileEnvs, nil
}

// DetachProfileFromEnv removes profileID from envID's profiles in inventory.toml.
func DetachProfileFromEnv(repoRoot, envID, profileID string) error {
	_, err := SetMatrixCell(repoRoot, "profiles", profileID, envID, false)
	return err
}

// DuplicateProfileWithoutItem creates a flattened copy of profileID named newProfileID without (category, itemID),
// and swaps profileID -> newProfileID on envID in inventory.toml.
func DuplicateProfileWithoutItem(repoRoot, envID, profileID, newProfileID, category, itemID string) error {
	newProfileID = strings.TrimSpace(newProfileID)
	if !slugRe.MatchString(newProfileID) {
		return Errf("invalid new profile name %q (use letters, digits, ., _, -)", newProfileID)
	}
	repo, err := LoadRepo(repoRoot)
	if err != nil {
		return err
	}
	if _, exists := repo.Profiles[newProfileID]; exists {
		return Errf("profile %q already exists", newProfileID)
	}
	origProf, ok := repo.Profiles[profileID]
	if !ok {
		return Errf("profile %q not found", profileID)
	}
	t, err := repo.Target(envID)
	if err != nil {
		return err
	}

	// Identify every top-level profile on envID whose inheritance chain brings in (category, itemID)
	// (e.g. both `base` and `ops-bot` which extends `base`), and flatten them into newProfileID without itemID!
	var affectedTop []string
	affectedSet := map[string]bool{}
	for _, topPID := range t.Profiles {
		if topPID == profileID {
			affectedTop = append(affectedTop, topPID)
			affectedSet[topPID] = true
			continue
		}
		expanded, err := repo.ExpandProfiles([]string{topPID})
		if err != nil {
			continue
		}
		if profileChainContains(repo, expanded, category, itemID) {
			affectedTop = append(affectedTop, topPID)
			affectedSet[topPID] = true
		}
	}
	if len(affectedTop) == 0 {
		affectedTop = []string{profileID}
		affectedSet[profileID] = true
	}

	expanded, err := repo.ExpandProfiles(affectedTop)
	if err != nil {
		return err
	}
	var frags, skills, mcps, hooks, agents, extras, plugins, launchers []string
	appendUniq := func(dst *[]string, vals []string, normSkill bool) {
		for _, v := range vals {
			if normSkill {
				v = NormalizeSkillName(v)
			}
			if !containsStr(*dst, v) {
				*dst = append(*dst, v)
			}
		}
	}
	for _, pid := range expanded {
		pr := repo.Profiles[pid]
		if pr == nil {
			continue
		}
		appendUniq(&frags, pr.Fragments, false)
		appendUniq(&skills, pr.Skills, true)
		appendUniq(&mcps, pr.MCP, false)
		appendUniq(&hooks, pr.Hooks, false)
		appendUniq(&agents, pr.Agents, false)
		appendUniq(&extras, pr.Extras, false)
		appendUniq(&plugins, pr.Plugins, false)
		appendUniq(&launchers, pr.Launchers, false)
	}

	switch category {
	case "prompts":
		frags = filterOut(frags, []string{itemID})
	case "skills":
		skills = filterOut(skills, []string{NormalizeSkillName(itemID)})
	case "mcp":
		mcps = filterOut(mcps, []string{itemID})
	case "hooks":
		hooks = filterOut(hooks, []string{itemID})
	case "agents":
		agents = filterOut(agents, []string{itemID})
	case "extras":
		extras = filterOut(extras, []string{itemID})
	case "plugins":
		plugins = filterOut(plugins, []string{itemID})
	case "launchers":
		launchers = filterOut(launchers, []string{itemID})
	}

	doc := map[string]any{
		"id":          newProfileID,
		"description": fmt.Sprintf("%s (without %s)", strings.TrimSpace(origProf.Description), itemID),
		"extends":     []string{},
		"prompt":      map[string]any{"fragments": nonNilSlice(frags)},
		"skills":      map[string]any{"include": nonNilSlice(skills)},
	}
	if len(mcps) > 0 {
		doc["mcp"] = mcps
	}
	if len(hooks) > 0 {
		doc["hooks"] = hooks
	}
	if len(agents) > 0 {
		doc["agents"] = agents
	}
	if len(extras) > 0 {
		doc["extras"] = extras
	}
	if len(plugins) > 0 {
		doc["plugins"] = plugins
	}
	if len(launchers) > 0 {
		doc["launchers"] = launchers
	}

	dest := filepath.Join(repoRoot, "profiles", newProfileID+".toml")
	if err := atomicWriteFile(dest, []byte(DumpsTOML(doc))); err != nil {
		return err
	}
	if errs := CheckRepo(repoRoot); len(errs) > 0 {
		_ = os.Remove(dest)
		return Errf("new profile rejected: %s", strings.Join(errs, "; "))
	}

	// Swap all affected profiles on envID with newProfileID
	var nextProfiles []string
	inserted := false
	for _, pid := range t.Profiles {
		if affectedSet[pid] {
			if !inserted {
				nextProfiles = append(nextProfiles, newProfileID)
				inserted = true
			}
			continue
		}
		if !containsStr(nextProfiles, pid) {
			nextProfiles = append(nextProfiles, pid)
		}
	}
	if _, err := UpdateTargetInInventory(repoRoot, envID, map[string]any{"profiles": nextProfiles}); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return nil
}

func profileChainContains(repo *Repo, expanded []string, category, itemID string) bool {
	for _, pid := range expanded {
		pr := repo.Profiles[pid]
		if pr == nil {
			continue
		}
		switch category {
		case "prompts":
			if containsStr(pr.Fragments, itemID) {
				return true
			}
		case "skills":
			want := NormalizeSkillName(itemID)
			for _, s := range pr.Skills {
				if NormalizeSkillName(s) == want {
					return true
				}
			}
		case "mcp":
			if containsStr(pr.MCP, itemID) {
				return true
			}
		case "hooks":
			if containsStr(pr.Hooks, itemID) {
				return true
			}
		case "agents":
			if containsStr(pr.Agents, itemID) {
				return true
			}
		case "extras":
			if containsStr(pr.Extras, itemID) {
				return true
			}
		case "plugins":
			if containsStr(pr.Plugins, itemID) {
				return true
			}
		case "launchers":
			if containsStr(pr.Launchers, itemID) {
				return true
			}
		}
	}
	return false
}

// updateProjectItem toggles an MCP server or skill assignment on projects/<target>/<slug>/project.toml.
func updateProjectItem(repoRoot, targetID, slug, category, itemID string, enabled bool) error {
	pPath := filepath.Join(repoRoot, "projects", targetID, slug, "project.toml")
	orig, err := os.ReadFile(pPath)
	if err != nil {
		return Errf("project %s/%s not found", targetID, slug)
	}
	data, err := LoadTOMLMap(pPath)
	if err != nil {
		return err
	}
	if category == "mcp" {
		cur := asStringSlice(data, "mcp")
		next := cur
		if enabled {
			if !containsStr(next, itemID) {
				next = append(next, itemID)
			}
		} else {
			next = filterOut(next, []string{itemID})
		}
		updated := patchTopLevelKey(string(orig), "mcp", TomlScalar(nonNilSlice(next)))
		if err := atomicWriteFile(pPath, []byte(updated)); err != nil {
			return err
		}
		if errs := CheckRepo(repoRoot); len(errs) > 0 {
			_ = atomicWriteFile(pPath, orig)
			return Errf("project update rejected: %s", strings.Join(errs, "; "))
		}
		return nil
	}
	if category == "skills" {
		// Project-level skills can be listed in `skills = [...]` in project.toml
		cur := asStringSlice(data, "skills")
		next := cur
		name := NormalizeSkillName(itemID)
		if enabled {
			if !containsStr(next, name) {
				next = append(next, name)
			}
		} else {
			next = filterOut(next, []string{name})
		}
		updated := patchTopLevelKey(string(orig), "skills", TomlScalar(nonNilSlice(next)))
		if err := atomicWriteFile(pPath, []byte(updated)); err != nil {
			return err
		}
		return nil
	}
	return Errf("unsupported project category %q", category)
}

func patchTopLevelKey(text, key, rendered string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		if headerLineRe.MatchString(lines[i]) {
			break
		}
		if m := kvLineRe.FindStringSubmatch(lines[i]); m != nil && m[2] == key {
			end := i + 1
			depth := bracketDelta(m[3])
			for depth > 0 && end < len(lines) {
				depth += bracketDelta(lines[end])
				end++
			}
			repl := fmt.Sprintf("%s%s = %s", m[1], key, rendered)
			lines = append(lines[:i], append([]string{repl}, lines[end:]...)...)
			return strings.Join(lines, "\n") + "\n"
		}
	}
	lines = append(lines, fmt.Sprintf("%s = %s", key, rendered))
	return strings.Join(lines, "\n") + "\n"
}

// CreateProjectEnvironment scaffolds a new project destination at projects/<target>/<slug>/project.toml.
func CreateProjectEnvironment(repoRoot, targetID, slug, fsPath, why string) error {
	slug = strings.TrimSpace(slug)
	fsPath = strings.TrimSpace(fsPath)
	if !slugRe.MatchString(slug) {
		return Errf("invalid project slug %q (use letters, digits, ., _, -)", slug)
	}
	if fsPath == "" {
		return Errf("project path is required")
	}
	repo, err := LoadRepo(repoRoot)
	if err != nil {
		return err
	}
	if _, err := repo.Target(targetID); err != nil {
		return err
	}
	dir := filepath.Join(repoRoot, "projects", targetID, slug)
	pPath := filepath.Join(dir, "project.toml")
	if _, err := os.Stat(pPath); err == nil {
		return Errf("project %s/%s already exists", targetID, slug)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	doc := map[string]any{
		"path":        fsPath,
		"why":         why,
		"files":       []string{},
		"mcp":         []string{},
		"codex_trust": true,
	}
	return atomicWriteFile(pPath, []byte(DumpsTOML(doc)))
}

// SaveProfile creates or updates a Profile bundle in profiles/<id>.toml.
func SaveProfile(repoRoot, id, description string, extends, frags, skills, mcps, hooks, agents, extras, plugins, launchers []string) error {
	id = strings.TrimSpace(id)
	if !slugRe.MatchString(id) {
		return Errf("invalid profile id %q", id)
	}
	dest := filepath.Join(repoRoot, "profiles", id+".toml")
	orig, hadOrig := readFileOpt(dest)
	doc := map[string]any{
		"id":          id,
		"description": strings.TrimSpace(description),
		"extends":     nonNilSlice(extends),
		"prompt":      map[string]any{"fragments": nonNilSlice(frags)},
		"skills":      map[string]any{"include": nonNilSlice(skills)},
	}
	if len(mcps) > 0 {
		doc["mcp"] = mcps
	}
	if len(hooks) > 0 {
		doc["hooks"] = hooks
	}
	if len(agents) > 0 {
		doc["agents"] = agents
	}
	if len(extras) > 0 {
		doc["extras"] = extras
	}
	if len(plugins) > 0 {
		doc["plugins"] = plugins
	}
	if len(launchers) > 0 {
		doc["launchers"] = launchers
	}
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	if err := atomicWriteFile(dest, []byte(DumpsTOML(doc))); err != nil {
		return err
	}
	if errs := CheckRepo(repoRoot); len(errs) > 0 {
		if hadOrig {
			_ = atomicWriteFile(dest, orig)
		} else {
			_ = os.Remove(dest)
		}
		return Errf("profile rejected: %s", strings.Join(errs, "; "))
	}
	return nil
}

// DeleteProfile removes profiles/<id>.toml and detaches it from all targets and other profiles' extends.
func DeleteProfile(repoRoot, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return Errf("profile id is required")
	}
	repo, err := LoadRepo(repoRoot)
	if err != nil {
		return err
	}
	if _, ok := repo.Profiles[id]; !ok {
		return Errf("profile %q not found", id)
	}
	// Detach from any targets referencing it
	for _, t := range repo.Targets {
		if containsStr(t.Profiles, id) {
			next := filterOut(t.Profiles, []string{id})
			if _, err := UpdateTargetInInventory(repoRoot, t.ID, map[string]any{"profiles": next}); err != nil {
				return err
			}
		}
	}
	// Detach from any other profiles extending it
	for pid, pr := range repo.Profiles {
		if pid == id || !containsStr(pr.Extends, id) {
			continue
		}
		nextExt := filterOut(pr.Extends, []string{id})
		if err := SaveProfile(repoRoot, pr.ID, pr.Description, nextExt, pr.Fragments, pr.Skills, pr.MCP, pr.Hooks, pr.Agents, pr.Extras, pr.Plugins, pr.Launchers); err != nil {
			return err
		}
	}
	dest := filepath.Join(repoRoot, "profiles", id+".toml")
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func readFileOpt(p string) ([]byte, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	return b, true
}

// SortedStringsCopy returns a sorted copy without mutating input.
func SortedStringsCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
