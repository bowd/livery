package launcher

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bowd/livery/internal/secrets"
)

// GroupsRoot returns the directory where memory/session groups live (~/.local/share/livery/groups).
func GroupsRoot() string {
	if v := strings.TrimSpace(os.Getenv("LIVERY_GROUPS_ROOT")); v != "" {
		return secrets.ExpandPath(v)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "livery", "groups")
}

// GroupHarnessDir returns ~/.local/share/livery/groups/<group>/<harness>.
func GroupHarnessDir(group, harness string) string {
	g := strings.TrimSpace(group)
	if g == "" {
		return ""
	}
	return filepath.Join(GroupsRoot(), g, harness)
}

type GroupSummary struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // "group" | "default" | "isolated"
	Path      string   `json:"path,omitempty"`
	Profiles  []string `json:"profiles"`
	Harnesses []string `json:"harnesses"`
	Bytes     int64    `json:"bytes"`
}

// SummarizeGroups groups all loaded launcher profiles by their effective memory/session pool.
func SummarizeGroups(profiles map[string]map[string]any) []GroupSummary {
	type acc struct {
		name      string
		kind      string
		path      string
		profiles  []string
		harnesses map[string]bool
	}
	buckets := map[string]*acc{}
	add := func(key, name, kind, path, label, harness string) {
		b, ok := buckets[key]
		if !ok {
			b = &acc{name: name, kind: kind, path: path, harnesses: map[string]bool{}}
			buckets[key] = b
		}
		b.profiles = append(b.profiles, label)
		if harness != "" {
			b.harnesses[harness] = true
		}
	}

	for _, spec := range profiles {
		h := strVal(spec, "harness", "")
		prof := strVal(spec, "profile", strVal(spec, "id", ""))
		mode := strVal(spec, "mode", "direct")
		grp := strings.TrimSpace(strVal(spec, "session_group", ""))
		label := h + " " + prof
		if grp != "" {
			add("grp:"+grp, grp, "group", filepath.Join(GroupsRoot(), grp), label, h)
		} else if mode == "isolated" {
			add("isolated", "isolated (private)", "isolated", "", label, h)
		} else {
			add("default", "default (shared home)", "default", "", label, h)
		}
	}

	out := make([]GroupSummary, 0, len(buckets))
	for _, b := range buckets {
		sort.Strings(b.profiles)
		hs := make([]string, 0, len(b.harnesses))
		for k := range b.harnesses {
			hs = append(hs, k)
		}
		sort.Strings(hs)
		var sz int64
		if b.path != "" {
			sz = dirBytes(b.path)
		}
		out = append(out, GroupSummary{
			Name:      b.name,
			Kind:      b.kind,
			Path:      b.path,
			Profiles:  b.profiles,
			Harnesses: hs,
			Bytes:     sz,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		order := map[string]int{"group": 0, "default": 1, "isolated": 2}
		if order[out[i].Kind] != order[out[j].Kind] {
			return order[out[i].Kind] < order[out[j].Kind]
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func dirBytes(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
