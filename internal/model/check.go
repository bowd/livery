package model

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	envNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	slugRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	secretValRe = regexp.MustCompile(`(?i)(^sk-[A-Za-z0-9_-]{20,}|^gh[pousr]_[A-Za-z0-9]{20,}|^AIza[0-9A-Za-z_-]{30,}|^Bearer\s+\S{16,}|-----BEGIN [A-Z ]*PRIVATE KEY-----)`)
)

func LooksLikeSecretValue(s string) bool {
	return secretValRe.MatchString(strings.TrimSpace(s))
}

func IsSecretishKey(k string) bool {
	lk := strings.ToLower(k)
	for _, s := range SecretishKeys {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

// CheckRepo validates inventory.toml, profiles, fragments, skills, MCP, hooks, and launchers.
func CheckRepo(root string) []string {
	repo, err := LoadRepo(root)
	if err != nil {
		return []string{err.Error()}
	}
	var errs []string

	// Unique target IDs & basic field checks
	seenTargets := map[string]bool{}
	for _, t := range repo.Targets {
		if seenTargets[t.ID] {
			errs = append(errs, fmt.Sprintf("inventory.toml: duplicate target id %q", t.ID))
		}
		seenTargets[t.ID] = true
		for _, p := range t.Providers {
			if !containsStr(Providers, p) {
				errs = append(errs, fmt.Sprintf("%s: unknown provider %q", t.ID, p))
			}
		}
		for prov, mode := range t.LinkMode {
			if !containsStr(LinkModes, mode) {
				errs = append(errs, fmt.Sprintf("%s: link_mode.%s must be symlink or copy, not %q", t.ID, prov, mode))
			}
		}
		pids, err := repo.ExpandProfiles(t.Profiles)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		_, skills, err := repo.Resolve(t)
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			seenNames := map[string]bool{}
			for _, ref := range skills {
				name := NormalizeSkillName(ref)
				if name == "" {
					continue
				}
				if seenNames[name] {
					errs = append(errs, fmt.Sprintf("%s: duplicate skill name %q", t.ID, name))
				}
				seenNames[name] = true
			}
		}
		resolvedLaunchers, err := repo.ResolveLaunchers(t, pids)
		if err != nil {
			errs = append(errs, err.Error())
		}
		for alias, lid := range t.HerdrDefaultLaunchers {
			if !slugRe.MatchString(alias) {
				errs = append(errs, fmt.Sprintf("%s: invalid herdr_default_launchers key %q", t.ID, alias))
			}
			if _, ok := repo.Launchers[lid]; !ok {
				errs = append(errs, fmt.Sprintf("%s: herdr_default_launchers.%s references unknown launcher %q", t.ID, alias, lid))
			} else if t.SyncHerdrTemplates && !containsStr(resolvedLaunchers, lid) {
				errs = append(errs, fmt.Sprintf("%s: herdr_default_launchers.%s launcher %q is not enabled on this target", t.ID, alias, lid))
			}
		}
	}

	// Profiles reference validation
	for pid, prof := range repo.Profiles {
		if _, err := repo.ExpandProfiles([]string{pid}); err != nil {
			errs = append(errs, err.Error())
		}
		for _, f := range prof.Fragments {
			if _, ok := repo.Fragments[f]; !ok {
				errs = append(errs, fmt.Sprintf("profiles/%s.toml: unknown fragment %q", pid, f))
			}
		}
		for _, l := range prof.Launchers {
			if _, ok := repo.Launchers[l]; !ok {
				errs = append(errs, fmt.Sprintf("profiles/%s.toml: unknown launcher %q", pid, l))
			}
		}
	}

	// MCP servers: no secret literals
	for name, m := range repo.MCP {
		where := fmt.Sprintf("mcp/%s.toml", name)
		for _, p := range m.Providers {
			if !containsStr(Providers, p) {
				errs = append(errs, fmt.Sprintf("%s: unknown provider %q", where, p))
			}
		}
		for _, prov := range m.Providers {
			sp := m.ForProvider(prov)
			for k, v := range sp {
				if k == "env_vars" {
					for _, vn := range asStringSlice(sp, "env_vars") {
						if !envNameRe.MatchString(vn) {
							errs = append(errs, fmt.Sprintf("%s: env_vars holds env var names only, not %q", where, vn))
						}
					}
					continue
				}
				if s, ok := v.(string); ok && LooksLikeSecretValue(s) {
					errs = append(errs, fmt.Sprintf("%s: %s looks like a secret value", where, k))
				}
			}
		}
	}

	// Hooks: script presence + no secret literals
	for hid, h := range repo.Hooks {
		where := fmt.Sprintf("hooks/%s.toml", hid)
		for _, prov := range h.Providers {
			sp := h.ForProvider(prov)
			if asString(sp, "event", "") == "" {
				errs = append(errs, fmt.Sprintf("%s: missing event", where))
			}
			if sc := asString(sp, "script", ""); sc != "" {
				if strings.Contains(sc, "/") || strings.HasPrefix(sc, ".") {
					errs = append(errs, fmt.Sprintf("%s: script must be a filename in hooks/scripts/", where))
				} else if _, err := os.Stat(filepath.Join(repo.Root, "hooks", "scripts", sc)); err != nil {
					errs = append(errs, fmt.Sprintf("%s: hooks/scripts/%s is missing", where, sc))
				}
			}
			if cmd := asString(sp, "command", ""); LooksLikeSecretValue(cmd) {
				errs = append(errs, fmt.Sprintf("%s: command looks like a secret value", where))
			}
		}
	}

	// Launchers: schema + no secret literals
	for lid, l := range repo.Launchers {
		where := fmt.Sprintf("launchers/%s.toml", lid)
		if !slugRe.MatchString(lid) {
			errs = append(errs, fmt.Sprintf("%s: invalid id %q", where, lid))
		}
		if !containsStr(Harnesses, l.Harness) {
			errs = append(errs, fmt.Sprintf("%s: harness must be one of %v, not %q", where, Harnesses, l.Harness))
		}
		if l.SessionGroup != "" && !slugRe.MatchString(l.SessionGroup) {
			errs = append(errs, fmt.Sprintf("%s: invalid session_group %q (use letters, digits, ., _, -)", where, l.SessionGroup))
		}
		if hcfg, ok := l.Raw["herdr"].(map[string]any); ok {
			for _, al := range asStringSlice(hcfg, "aliases") {
				if !slugRe.MatchString(al) {
					errs = append(errs, fmt.Sprintf("%s: invalid herdr.aliases entry %q", where, al))
				}
			}
		}
		checkNoSecretLiterals(l.Raw, where, "", &errs)
	}

	// Deduplicate preserving order
	sort.Strings(errs)
	var uniq []string
	for _, e := range errs {
		if len(uniq) == 0 || uniq[len(uniq)-1] != e {
			uniq = append(uniq, e)
		}
	}
	return uniq
}

func checkNoSecretLiterals(v any, where, path string, errs *[]string) {
	switch x := v.(type) {
	case string:
		if LooksLikeSecretValue(x) {
			*errs = append(*errs, fmt.Sprintf("%s: %s looks like a literal secret; use store:/keychain:/file:/env: refs", where, path))
		}
	case []any:
		for i, el := range x {
			checkNoSecretLiterals(el, where, fmt.Sprintf("%s[%d]", path, i), errs)
		}
	case map[string]any:
		for k, el := range x {
			sub := k
			if path != "" {
				sub = path + "." + k
			}
			checkNoSecretLiterals(el, where, sub, errs)
		}
	}
}
