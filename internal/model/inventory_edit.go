package model

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

var AllowedTargetEditKeys = map[string]bool{
	"providers":               true,
	"profiles":                true,
	"roles":                   true,
	"add":                     true,
	"remove":                  true,
	"external":                true,
	"skillshare_targets":      true,
	"skillshare_mode":         true,
	"skillshare_manage_mcp":   true,
	"skillshare_manage_hooks": true,
	"skillshare_bin":          true,
	"skillshare_root":         true,
	"install_launcher_cli":    true,
	"emit_shims":              true,
	"sync_herdr_mod":          true,
	"sync_herdr_programs":     true,
	"sync_herdr_templates":    true,
	"sync_t3_providers":       true,
}

var (
	headerLineRe = regexp.MustCompile(`^\s*\[`)
	targetHdrRe  = regexp.MustCompile(`^\s*\[\[target\]\]\s*(?:#.*)?$`)
	kvLineRe     = regexp.MustCompile(`^(\s*)([A-Za-z0-9_-]+)\s*=(.*)$`)
)

func splitInlineComment(rhs string) string {
	inStr := false
	var quote byte
	escaped := false
	depth := 0
	for i := 0; i < len(rhs); i++ {
		ch := rhs[i]
		if inStr {
			if escaped {
				escaped = false
			} else if ch == '\\' && quote == '"' {
				escaped = true
			} else if ch == quote {
				inStr = false
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inStr = true
			quote = ch
		} else if ch == '[' || ch == '{' {
			depth++
		} else if ch == ']' || ch == '}' {
			if depth > 0 {
				depth--
			}
		} else if ch == '#' && depth == 0 {
			start := i
			for start > 0 && (rhs[start-1] == ' ' || rhs[start-1] == '\t') {
				start--
			}
			return rhs[start:]
		}
	}
	return ""
}

func bracketDelta(text string) int {
	inStr := false
	var quote byte
	escaped := false
	delta := 0
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inStr {
			if escaped {
				escaped = false
			} else if ch == '\\' && quote == '"' {
				escaped = true
			} else if ch == quote {
				inStr = false
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inStr = true
			quote = ch
		} else if ch == '#' {
			break
		} else if ch == '[' || ch == '{' {
			delta++
		} else if ch == ']' || ch == '}' {
			delta--
		}
	}
	return delta
}

func parseBlockTargetID(lines []string) string {
	var doc struct {
		Target []struct {
			ID string `toml:"id"`
		} `toml:"target"`
	}
	if _, err := toml.Decode(strings.Join(lines, "\n"), &doc); err != nil {
		return ""
	}
	if len(doc.Target) > 0 {
		return doc.Target[0].ID
	}
	return ""
}

func findTargetBlockRanges(lines []string) [][2]int {
	var ranges [][2]int
	for i := 0; i < len(lines); {
		if targetHdrRe.MatchString(lines[i]) {
			start := i
			j := i + 1
			for j < len(lines) && !headerLineRe.MatchString(lines[j]) {
				j++
			}
			ranges = append(ranges, [2]int{start, j})
			i = j
		} else {
			i++
		}
	}
	return ranges
}

func findKeySpan(block []string, key string) (int, int, bool) {
	for i := 1; i < len(block); i++ {
		m := kvLineRe.FindStringSubmatch(block[i])
		if m != nil && m[2] == key {
			end := i + 1
			depth := bracketDelta(m[3])
			for depth > 0 && end < len(block) {
				depth += bracketDelta(block[end])
				end++
			}
			return i, end, true
		}
	}
	return 0, 0, false
}

func validateTargetPatch(patch map[string]any) (map[string]any, error) {
	clean := map[string]any{}
	for k, v := range patch {
		if !AllowedTargetEditKeys[k] {
			return nil, Errf("unsupported editable target field: %q", k)
		}
		if v == nil {
			clean[k] = nil
			continue
		}
		switch k {
		case "skillshare_mode":
			s := fmt.Sprint(v)
			if !containsStr(SkillshareModes, s) {
				return nil, Errf("skillshare_mode must be one of %v, not %q", SkillshareModes, s)
			}
			clean[k] = s
		case "providers":
			list, ok := toCleanStrings(v)
			if !ok {
				return nil, Errf("providers must be a list")
			}
			for _, p := range list {
				if !containsStr(Providers, p) {
					return nil, Errf("unknown provider %q", p)
				}
			}
			clean[k] = list
		case "profiles", "roles", "add", "remove", "external", "skillshare_targets":
			list, ok := toCleanStrings(v)
			if !ok {
				return nil, Errf("%s must be a list", k)
			}
			clean[k] = list
		case "skillshare_manage_mcp", "skillshare_manage_hooks", "install_launcher_cli", "emit_shims",
			"sync_herdr_mod", "sync_herdr_programs", "sync_herdr_templates", "sync_t3_providers":
			b, ok := v.(bool)
			if !ok {
				return nil, Errf("%s must be a boolean", k)
			}
			clean[k] = b
		case "skillshare_bin", "skillshare_root":
			s := strings.TrimSpace(fmt.Sprint(v))
			if s == "" {
				clean[k] = nil
			} else {
				clean[k] = s
			}
		}
	}
	return clean, nil
}

func toCleanStrings(v any) ([]string, bool) {
	var raw []string
	switch x := v.(type) {
	case []string:
		raw = x
	case []any:
		raw = make([]string, 0, len(x))
		for _, el := range x {
			raw = append(raw, fmt.Sprint(el))
		}
	default:
		return nil, false
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, true
}

// PatchInventoryText applies a comment-preserving surgical update to one [[target]] block.
func PatchInventoryText(text, targetID string, patch map[string]any) (string, error) {
	clean, err := validateTargetPatch(patch)
	if err != nil {
		return "", err
	}
	if len(clean) == 0 {
		return text, nil
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	ranges := findTargetBlockRanges(lines)
	found := false
	var start, end int
	for _, rg := range ranges {
		if parseBlockTargetID(lines[rg[0]:rg[1]]) == targetID {
			start, end = rg[0], rg[1]
			found = true
			break
		}
	}
	if !found {
		return "", Errf("target %q not found in inventory.toml", targetID)
	}
	block := append([]string(nil), lines[start:end]...)

	keys := make([]string, 0, len(clean))
	for k := range clean {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		val := clean[key]
		sIdx, eIdx, exists := findKeySpan(block, key)
		if val == nil {
			if exists {
				block = append(block[:sIdx], block[eIdx:]...)
			}
			continue
		}
		rendered := TomlScalar(val)
		if exists {
			m := kvLineRe.FindStringSubmatch(block[sIdx])
			indent := ""
			comment := ""
			if m != nil {
				indent = m[1]
				if eIdx == sIdx+1 {
					comment = splitInlineComment(m[3])
				}
			}
			repl := fmt.Sprintf("%s%s = %s%s", indent, key, rendered, comment)
			block = append(block[:sIdx], append([]string{repl}, block[eIdx:]...)...)
		} else {
			insertAt := len(block)
			for insertAt > 1 && strings.TrimSpace(block[insertAt-1]) == "" {
				insertAt--
			}
			line := fmt.Sprintf("%s = %s", key, rendered)
			block = append(block[:insertAt], append([]string{line}, block[insertAt:]...)...)
		}
	}

	newLines := make([]string, 0, len(lines)+len(block))
	newLines = append(newLines, lines[:start]...)
	newLines = append(newLines, block...)
	newLines = append(newLines, lines[end:]...)
	newText := strings.Join(newLines, "\n") + "\n"
	var checkDoc map[string]any
	if _, err := toml.Decode(newText, &checkDoc); err != nil {
		return "", Errf("generated invalid inventory.toml: %v", err)
	}
	return newText, nil
}

func atomicWriteFile(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".inventory-*.toml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func writeValidatedInventory(repoRoot, newText string) error {
	invPath := filepath.Join(repoRoot, "inventory.toml")
	orig, err := os.ReadFile(invPath)
	if err != nil {
		return err
	}
	if string(orig) == newText {
		return nil
	}
	if err := atomicWriteFile(invPath, []byte(newText)); err != nil {
		return err
	}
	if errs := CheckRepo(repoRoot); len(errs) > 0 {
		_ = atomicWriteFile(invPath, orig)
		limit := len(errs)
		if limit > 5 {
			limit = 5
		}
		return Errf("inventory update rejected by repo check: %s", strings.Join(errs[:limit], "; "))
	}
	return nil
}

// UpdateTargetInInventory updates an existing target block and rolls back if CheckRepo fails.
func UpdateTargetInInventory(repoRoot, targetID string, patch map[string]any) ([]string, error) {
	invPath := filepath.Join(repoRoot, "inventory.toml")
	orig, err := os.ReadFile(invPath)
	if err != nil {
		return nil, err
	}
	updated, err := PatchInventoryText(string(orig), targetID, patch)
	if err != nil {
		return nil, err
	}
	if err := writeValidatedInventory(repoRoot, updated); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// TargetOwnExternal returns a target's own unmerged `external` list alongside `[defaults].external`.
func TargetOwnExternal(repoRoot, targetID string) (own []string, defaults []string) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "inventory.toml"))
	if err != nil {
		return nil, nil
	}
	var raw struct {
		Defaults struct {
			External []string `toml:"external"`
		} `toml:"defaults"`
		Targets []struct {
			ID       string   `toml:"id"`
			External []string `toml:"external"`
		} `toml:"target"`
	}
	if err := toml.Unmarshal(b, &raw); err != nil {
		return nil, nil
	}
	defaults = raw.Defaults.External
	for _, t := range raw.Targets {
		if t.ID == targetID {
			own = t.External
			break
		}
	}
	return own, defaults
}

// NewTargetSpec defines fields for adding a new environment to inventory.toml.
type NewTargetSpec struct {
	ID                string   `json:"id"`
	SSH               string   `json:"ssh"`
	Providers         []string `json:"providers"`
	Profiles          []string `json:"profiles"`
	SkillshareTargets []string `json:"skillshare_targets"`
	External          []string `json:"external"`
	Roles             []string `json:"roles"`
}

// AddTargetToInventory appends a new [[target]] block cleanly and validates via CheckRepo.
func AddTargetToInventory(repoRoot string, spec NewTargetSpec) error {
	spec.ID = strings.TrimSpace(spec.ID)
	spec.SSH = strings.TrimSpace(spec.SSH)
	if spec.ID == "" || spec.SSH == "" {
		return Errf("target id and ssh are required")
	}
	if len(spec.Providers) == 0 {
		spec.Providers = []string{"claude", "codex"}
	}
	invPath := filepath.Join(repoRoot, "inventory.toml")
	orig, err := os.ReadFile(invPath)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(orig), "\n"), "\n")
	for _, rg := range findTargetBlockRanges(lines) {
		if parseBlockTargetID(lines[rg[0]:rg[1]]) == spec.ID {
			return Errf("target %q already exists in inventory.toml", spec.ID)
		}
	}
	var block []string
	block = append(block,
		"",
		"[[target]]",
		fmt.Sprintf("id = %s", TomlScalar(spec.ID)),
		fmt.Sprintf("ssh = %s", TomlScalar(spec.SSH)),
		fmt.Sprintf("providers = %s", TomlScalar(spec.Providers)),
		fmt.Sprintf("profiles = %s", TomlScalar(nonNilSlice(spec.Profiles))),
	)
	if len(spec.SkillshareTargets) > 0 {
		block = append(block, fmt.Sprintf("skillshare_targets = %s", TomlScalar(spec.SkillshareTargets)))
	}
	block = append(block,
		fmt.Sprintf("roles = %s", TomlScalar(nonNilSlice(spec.Roles))),
		fmt.Sprintf("external = %s", TomlScalar(nonNilSlice(spec.External))),
		"adopt = []",
	)
	newText := strings.TrimRight(string(orig), "\n") + "\n" + strings.Join(block, "\n") + "\n"
	return writeValidatedInventory(repoRoot, newText)
}

// RemoveTargetFromInventory deletes a [[target]] block cleanly and validates via CheckRepo.
func RemoveTargetFromInventory(repoRoot, targetID string) error {
	invPath := filepath.Join(repoRoot, "inventory.toml")
	orig, err := os.ReadFile(invPath)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(orig), "\n"), "\n")
	ranges := findTargetBlockRanges(lines)
	for _, rg := range ranges {
		if parseBlockTargetID(lines[rg[0]:rg[1]]) == targetID {
			start, end := rg[0], rg[1]
			// Trim one preceding blank line if present so formatting stays tidy
			if start > 0 && strings.TrimSpace(lines[start-1]) == "" {
				start--
			}
			newLines := append(append([]string(nil), lines[:start]...), lines[end:]...)
			newText := strings.TrimRight(strings.Join(newLines, "\n"), "\n") + "\n"
			return writeValidatedInventory(repoRoot, newText)
		}
	}
	return Errf("target %q not found in inventory.toml", targetID)
}

// RawTargetFields returns un-merged raw fields from inventory.toml for one target.
func RawTargetFields(repoRoot, targetID string) (map[string]any, error) {
	inv, err := LoadTOMLMap(filepath.Join(repoRoot, "inventory.toml"))
	if err != nil {
		return nil, err
	}
	if ts, ok := inv["target"].([]map[string]any); ok {
		for _, t := range ts {
			if asString(t, "id", "") == targetID {
				return t, nil
			}
		}
	}
	if ts, ok := inv["target"].([]any); ok {
		for _, item := range ts {
			if t, ok := item.(map[string]any); ok && asString(t, "id", "") == targetID {
				return t, nil
			}
		}
	}
	return nil, Errf("unknown target %q", targetID)
}

func nonNilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
