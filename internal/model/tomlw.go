package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var bareKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// TomlKey formats a TOML key, quoting only when necessary.
func TomlKey(k string) string {
	if bareKeyRe.MatchString(k) {
		return k
	}
	b, _ := json.Marshal(k)
	return string(b)
}

// TomlScalar formats a TOML scalar, array, or inline table deterministically.
func TomlScalar(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		b, _ := json.Marshal(x)
		return string(b)
	case []string:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = TomlScalar(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []any:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = TomlScalar(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]string:
		if len(x) == 0 {
			return "{}"
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s = %s", TomlKey(k), TomlScalar(x[k]))
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case map[string]any:
		if len(x) == 0 {
			return "{}"
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s = %s", TomlKey(k), TomlScalar(x[k]))
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	default:
		b, _ := json.Marshal(fmt.Sprint(v))
		return string(b)
	}
}

func isTable(v any) bool {
	m, ok := v.(map[string]any)
	return ok && len(m) > 0
}

func isTableArray(v any) bool {
	s, ok := v.([]map[string]any)
	if ok && len(s) > 0 {
		return true
	}
	a, ok := v.([]any)
	if !ok || len(a) == 0 {
		return false
	}
	for _, el := range a {
		if _, isMap := el.(map[string]any); !isMap {
			return false
		}
	}
	return true
}

// DumpsTOML writes a map[string]any to clean block TOML (compatible with lock.toml and observed/*.toml).
func DumpsTOML(data map[string]any) string {
	var out []string
	emitTOML(data, nil, &out)
	return strings.Trim(strings.Join(out, "\n"), "\n") + "\n"
}

func emitTOML(table map[string]any, path []string, out *[]string) {
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := table[k]
		if !isTable(v) && !isTableArray(v) {
			*out = append(*out, fmt.Sprintf("%s = %s", TomlKey(k), TomlScalar(v)))
		}
	}
	for _, k := range keys {
		v := table[k]
		if isTable(v) {
			sub := append(append([]string(nil), path...), TomlKey(k))
			*out = append(*out, "", "["+strings.Join(sub, ".")+"]")
			emitTOML(v.(map[string]any), sub, out)
		}
	}
	for _, k := range keys {
		v := table[k]
		if !isTableArray(v) {
			continue
		}
		sub := append(append([]string(nil), path...), TomlKey(k))
		if ms, ok := v.([]map[string]any); ok {
			for _, elem := range ms {
				*out = append(*out, "", "[["+strings.Join(sub, ".")+"]]")
				emitTOML(elem, sub, out)
			}
		} else if as, ok := v.([]any); ok {
			for _, raw := range as {
				if elem, ok := raw.(map[string]any); ok {
					*out = append(*out, "", "[["+strings.Join(sub, ".")+"]]")
					emitTOML(elem, sub, out)
				}
			}
		}
	}
}

// CanonJSON encodes an object as sorted-key compact JSON (identical to Python canon()).
func CanonJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
