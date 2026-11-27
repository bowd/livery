package model

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var fragIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(/[a-z0-9][a-z0-9_-]*)+$`)

// PromptFragmentInput describes a prompt fragment to create or update under prompts/fragments/<id>.md.
type PromptFragmentInput struct {
	ID             string   `json:"id"`
	Providers      []string `json:"providers"`
	Scope          string   `json:"scope"`
	Why            string   `json:"why"`
	Since          string   `json:"since"`
	Body           string   `json:"body"`
	Profiles       []string `json:"profiles"`
	UpdateProfiles bool     `json:"update_profiles"`
}

func validateFragmentID(id string) (string, error) {
	id = strings.TrimSpace(strings.TrimSuffix(filepath.ToSlash(id), ".md"))
	if !fragIDRe.MatchString(id) || strings.Contains(id, "..") {
		return "", Errf("invalid fragment id %q (expected <area>/<name>, e.g. style/no-any)", id)
	}
	return id, nil
}

func formatFragmentFile(id string, providers []string, scope, why, since, body string) string {
	var lines []string
	lines = append(lines, "+++")
	lines = append(lines, fmt.Sprintf("id = %s", TomlScalar(id)))
	lines = append(lines, fmt.Sprintf("providers = %s", TomlScalar(providers)))
	lines = append(lines, fmt.Sprintf("scope = %s", TomlScalar(scope)))
	if strings.TrimSpace(why) != "" {
		lines = append(lines, fmt.Sprintf("why = %s", TomlScalar(strings.TrimSpace(why))))
	}
	if strings.TrimSpace(since) != "" {
		lines = append(lines, fmt.Sprintf("since = %s", TomlScalar(strings.TrimSpace(since))))
	}
	lines = append(lines, "+++")
	body = strings.TrimSpace(body)
	return strings.Join(lines, "\n") + "\n" + body + "\n"
}

// SavePromptFragment creates or updates prompts/fragments/<id>.md, validates via CheckRepo (rolling back on error),
// and optionally syncs profile [prompt].fragments memberships.
func SavePromptFragment(repoRoot string, in PromptFragmentInput) (string, error) {
	id, err := validateFragmentID(in.ID)
	if err != nil {
		return "", err
	}
	var provs []string
	seenP := map[string]bool{}
	for _, p := range in.Providers {
		p = strings.TrimSpace(p)
		if p == "" || seenP[p] {
			continue
		}
		if !containsStr(Providers, p) {
			return "", Errf("unknown provider %q (allowed: %v)", p, Providers)
		}
		seenP[p] = true
		provs = append(provs, p)
	}
	if len(provs) == 0 {
		return "", Errf("providers must include at least one of %v", Providers)
	}
	scope := strings.TrimSpace(in.Scope)
	if scope == "" {
		scope = "user"
	}
	if !containsStr(Scopes, scope) {
		return "", Errf("scope must be one of %v, not %q", Scopes, scope)
	}
	body := strings.TrimSpace(in.Body)
	if body == "" {
		return "", Errf("fragment body cannot be empty")
	}

	dest := filepath.Join(repoRoot, "prompts", "fragments", filepath.FromSlash(id)+".md")
	var orig []byte
	hadOrig := false
	if b, err := os.ReadFile(dest); err == nil {
		orig = b
		hadOrig = true
	}

	since := strings.TrimSpace(in.Since)
	if since == "" {
		if hadOrig {
			if existing, err := ParseFragment(dest, filepath.Join(repoRoot, "prompts", "fragments")); err == nil && existing.Since != "" {
				since = existing.Since
			}
		}
		if since == "" {
			since = time.Now().Format("2006-01-02")
		}
	}

	content := formatFragmentFile(id, provs, scope, in.Why, since, body)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := atomicWriteFile(dest, []byte(content)); err != nil {
		return "", err
	}
	if errs := CheckRepo(repoRoot); len(errs) > 0 {
		if hadOrig {
			_ = atomicWriteFile(dest, orig)
		} else {
			_ = os.Remove(dest)
		}
		return "", Errf("prompt fragment rejected: %s", strings.Join(errs, "; "))
	}

	if in.UpdateProfiles {
		if err := SetFragmentProfiles(repoRoot, id, in.Profiles); err != nil {
			return id, err
		}
	}
	return id, nil
}

// updateProfileFragmentsBlock surgically updates `fragments = [...]` inside the `[prompt]` table of a profile TOML file,
// preserving surrounding comments and ordering.
func updateProfileFragmentsBlock(text string, next []string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var rendered string
	if len(next) <= 2 {
		rendered = fmt.Sprintf("fragments = %s", TomlScalar(nonNilSlice(next)))
	} else {
		var items []string
		for _, f := range next {
			items = append(items, "  "+TomlScalar(f)+",")
		}
		rendered = "fragments = [\n" + strings.Join(items, "\n") + "\n]"
	}

	inPrompt := false
	promptHdrIdx := -1
	promptEndIdx := len(lines)
	for i, ln := range lines {
		trim := strings.TrimSpace(ln)
		if headerLineRe.MatchString(ln) {
			if inPrompt {
				promptEndIdx = i
				break
			}
			if trim == "[prompt]" || strings.HasPrefix(trim, "[prompt] ") || strings.HasPrefix(trim, "[prompt]#") {
				inPrompt = true
				promptHdrIdx = i
			}
		}
	}

	if promptHdrIdx >= 0 {
		for i := promptHdrIdx + 1; i < promptEndIdx; i++ {
			if m := kvLineRe.FindStringSubmatch(lines[i]); m != nil && m[2] == "fragments" {
				end := i + 1
				depth := bracketDelta(m[3])
				for depth > 0 && end < promptEndIdx {
					depth += bracketDelta(lines[end])
					end++
				}
				comment := ""
				if end == i+1 && len(next) <= 2 {
					comment = splitInlineComment(m[3])
				}
				replLines := strings.Split(m[1]+rendered+comment, "\n")
				out := make([]string, 0, len(lines)-(end-i)+len(replLines))
				out = append(out, lines[:i]...)
				out = append(out, replLines...)
				out = append(out, lines[end:]...)
				return strings.Join(out, "\n") + "\n"
			}
		}
		// [prompt] exists without a fragments key: insert at end of [prompt] section
		insertAt := promptEndIdx
		for insertAt > promptHdrIdx+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
			insertAt--
		}
		replLines := strings.Split(rendered, "\n")
		out := make([]string, 0, len(lines)+len(replLines))
		out = append(out, lines[:insertAt]...)
		out = append(out, replLines...)
		out = append(out, lines[insertAt:]...)
		return strings.Join(out, "\n") + "\n"
	}

	// No [prompt] section yet: append one
	lines = append(lines, "[prompt]")
	lines = append(lines, strings.Split(rendered, "\n")...)
	return strings.Join(lines, "\n") + "\n"
}

// SetFragmentProfiles sets which repo profiles include fragID in their [prompt].fragments list.
func SetFragmentProfiles(repoRoot, fragID string, profileIDs []string) error {
	want := map[string]bool{}
	for _, pid := range profileIDs {
		pid = strings.TrimSpace(pid)
		if pid != "" {
			want[pid] = true
		}
	}
	pdir := filepath.Join(repoRoot, "profiles")
	entries, err := os.ReadDir(pdir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	type backup struct {
		path string
		orig []byte
	}
	var modified []backup
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		pid := strings.TrimSuffix(e.Name(), ".toml")
		pp := filepath.Join(pdir, e.Name())
		data, err := LoadTOMLMap(pp)
		if err != nil {
			continue
		}
		promptTbl, _ := data["prompt"].(map[string]any)
		cur := asStringSlice(promptTbl, "fragments")
		has := containsStr(cur, fragID)
		should := want[pid]
		if has == should {
			continue
		}
		var next []string
		if should {
			next = append(append([]string(nil), cur...), fragID)
		} else {
			for _, f := range cur {
				if f != fragID {
					next = append(next, f)
				}
			}
		}
		orig, err := os.ReadFile(pp)
		if err != nil {
			return err
		}
		updated := updateProfileFragmentsBlock(string(orig), next)
		if err := atomicWriteFile(pp, []byte(updated)); err != nil {
			return err
		}
		modified = append(modified, backup{path: pp, orig: orig})
	}
	if len(modified) == 0 {
		return nil
	}
	if errs := CheckRepo(repoRoot); len(errs) > 0 {
		for _, b := range modified {
			_ = atomicWriteFile(b.path, b.orig)
		}
		return Errf("profile fragment update rejected: %s", strings.Join(errs, "; "))
	}
	return nil
}

// DeletePromptFragment deletes prompts/fragments/<id>.md and removes references from profiles/*.toml and inventory.toml.
func DeletePromptFragment(repoRoot, fragID string) error {
	id, err := validateFragmentID(fragID)
	if err != nil {
		return err
	}
	dest := filepath.Join(repoRoot, "prompts", "fragments", filepath.FromSlash(id)+".md")
	origFrag, err := os.ReadFile(dest)
	if err != nil {
		return Errf("prompt fragment %q not found", id)
	}

	// 1. Strip from all profiles/*.toml
	if err := SetFragmentProfiles(repoRoot, id, nil); err != nil {
		return err
	}

	// 2. Strip from any target's add/remove in inventory.toml
	repo, err := LoadRepo(repoRoot)
	if err == nil {
		for _, t := range repo.Targets {
			patch := map[string]any{}
			if containsStr(t.Add, id) {
				patch["add"] = filterOut(t.Add, []string{id})
			}
			if containsStr(t.Remove, id) {
				patch["remove"] = filterOut(t.Remove, []string{id})
			}
			if len(patch) > 0 {
				if _, err := UpdateTargetInInventory(repoRoot, t.ID, patch); err != nil {
					return err
				}
			}
		}
	}

	// 3. Remove the file (and empty area directory if clean)
	if err := os.Remove(dest); err != nil {
		return err
	}
	baseDir := filepath.Join(repoRoot, "prompts", "fragments")
	parent := filepath.Dir(dest)
	if parent != baseDir {
		if ents, err := os.ReadDir(parent); err == nil && len(ents) == 0 {
			_ = os.Remove(parent)
		}
	}

	if errs := CheckRepo(repoRoot); len(errs) > 0 {
		_ = os.MkdirAll(filepath.Dir(dest), 0o755)
		_ = atomicWriteFile(dest, origFrag)
		return Errf("delete prompt fragment rejected: %s", strings.Join(errs, "; "))
	}
	pruneFragmentFromOrderFile(repoRoot, id)
	return nil
}

// FragmentArea returns the top-level area folder of a fragment ID (e.g. "style" for "style/no-any").
func FragmentArea(id string) string {
	area, _, ok := strings.Cut(id, "/")
	if !ok {
		return "general"
	}
	return area
}

// SortedFragmentIDs returns all fragment IDs in canonical compilation order:
// 1. Explicit `prompts/order.toml` order (if present),
// 2. `base` profile order -> targets' expanded profile order,
// 3. Remaining fragments alphabetically.
func (r *Repo) SortedFragmentIDs() []string {
	seen := make(map[string]bool, len(r.Fragments))
	out := make([]string, 0, len(r.Fragments))
	add := func(id string) {
		if _, ok := r.Fragments[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range r.FragmentOrder {
		add(id)
	}
	if base, ok := r.Profiles["base"]; ok && base != nil {
		for _, id := range base.Fragments {
			add(id)
		}
	}
	for _, t := range r.Targets {
		pids, err := r.ExpandProfiles(t.Profiles)
		if err != nil {
			continue
		}
		for _, pid := range pids {
			if pr := r.Profiles[pid]; pr != nil {
				for _, id := range pr.Fragments {
					add(id)
				}
			}
		}
	}
	var rest []string
	for id := range r.Fragments {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	out = append(out, rest...)
	return out
}

// FragmentRankMap returns 1-based canonical rank for each fragment ID.
func (r *Repo) FragmentRankMap() map[string]int {
	ordered := r.SortedFragmentIDs()
	ranks := make(map[string]int, len(ordered))
	for i, id := range ordered {
		ranks[id] = i + 1
	}
	return ranks
}

// SortFragmentsCanonical sorts a subset of fragment IDs according to the repo's canonical fragment order.
func (r *Repo) SortFragmentsCanonical(ids []string) []string {
	if len(ids) <= 1 {
		return ids
	}
	ranks := r.FragmentRankMap()
	cp := append([]string(nil), ids...)
	sort.SliceStable(cp, func(i, j int) bool {
		ri, okI := ranks[cp[i]]
		rj, okJ := ranks[cp[j]]
		if okI && okJ {
			return ri < rj
		}
		if okI != okJ {
			return okI
		}
		return cp[i] < cp[j]
	})
	return cp
}

// SavePromptOrder persists the canonical global prompt fragment order to `prompts/order.toml`.
func SavePromptOrder(repoRoot string, ordered []string) ([]string, error) {
	repo, err := LoadRepo(repoRoot)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var final []string
	for _, raw := range ordered {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		if _, ok := repo.Fragments[id]; !ok {
			return nil, Errf("unknown prompt fragment %q", id)
		}
		seen[id] = true
		final = append(final, id)
	}
	// Append any remaining fragments in current canonical order so none are lost
	for _, id := range repo.SortedFragmentIDs() {
		if !seen[id] {
			seen[id] = true
			final = append(final, id)
		}
	}
	orderPath := filepath.Join(repoRoot, "prompts", "order.toml")
	_ = os.MkdirAll(filepath.Dir(orderPath), 0o755)
	doc := map[string]any{"fragments": nonNilSlice(final)}
	content := "# Canonical compilation order for prompt fragments across all profiles & hosts.\n" + DumpsTOML(doc)
	if err := atomicWriteFile(orderPath, []byte(content)); err != nil {
		return nil, err
	}
	return final, nil
}

func pruneFragmentFromOrderFile(repoRoot, removedID string) {
	orderPath := filepath.Join(repoRoot, "prompts", "order.toml")
	if _, err := os.Stat(orderPath); err != nil {
		return
	}
	if repo, err := LoadRepo(repoRoot); err == nil && len(repo.FragmentOrder) > 0 {
		var next []string
		for _, id := range repo.FragmentOrder {
			if id != removedID {
				next = append(next, id)
			}
		}
		_, _ = SavePromptOrder(repoRoot, next)
	}
}
