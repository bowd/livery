package fleet

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/skillshare"
	"gopkg.in/yaml.v3"
)

// MigrateToXDG copies an existing Livery/agent-config data repository into the clean XDG control plane
// (default ~/.local/share/livery), initializes standalone git repos for BOTH the control plane AND
// catalog/skillshare/ (so Skillshare Git Sync never leaks parent files), migrates MCP/hooks/skills into
// catalog/skillshare/, and generates env-overrides/<host>.yaml for every target.
func MigrateToXDG(srcRepo, dstHome string) error {
	srcAbs, err := filepath.Abs(model.ExpandHome(srcRepo))
	if err != nil {
		return err
	}
	dstAbs, err := filepath.Abs(model.ExpandHome(dstHome))
	if err != nil {
		return err
	}
	if srcAbs == dstAbs {
		return fmt.Errorf("source and destination are identical (%s)", srcAbs)
	}
	if err := os.MkdirAll(dstAbs, 0o755); err != nil {
		return err
	}

	// 1. Copy core declarative directories & files
	for _, entry := range []string{"inventory.toml", "prompts", "profiles", "launchers", "projects", "mcp", "hooks", "provenance"} {
		src := filepath.Join(srcAbs, entry)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyPathRecursive(src, filepath.Join(dstAbs, entry)); err != nil {
			return fmt.Errorf("copy %s: %w", entry, err)
		}
	}

	// 2. Initialize standalone catalog/skillshare/ git repo
	if err := skillshare.EnsureCatalogInitialized(dstAbs); err != nil {
		return err
	}
	catSkills := skillshare.CatalogSkillsDir(dstAbs)

	// Populate catalog/skillshare/skills/ from existing skillshare/skills/ or skills/local + vendor
	for _, cand := range []string{
		filepath.Join(srcAbs, "skillshare", "skills"),
		filepath.Join(srcAbs, "skills", "local"),
	} {
		if entries, err := os.ReadDir(cand); err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					dst := filepath.Join(catSkills, e.Name())
					if _, err := os.Stat(dst); os.IsNotExist(err) {
						_ = skillshare.CopyTree(filepath.Join(cand, e.Name()), dst)
					}
				}
			}
		}
	}

	// 3. Migrate mcp/*.toml & hooks/*.toml into catalog/skillshare/config.yaml so they appear natively in Catalog + Matrix!
	repo, err := model.LoadRepo(dstAbs)
	if err != nil {
		return fmt.Errorf("load migrated repo: %w", err)
	}
	catCfgPath := skillshare.CatalogConfigPath(dstAbs)
	var catDoc map[string]any
	if b, err := os.ReadFile(catCfgPath); err == nil {
		_ = yaml.Unmarshal(b, &catDoc)
	}
	if catDoc == nil {
		catDoc = map[string]any{}
	}
	catDoc["mode"] = "merge"
	catDoc["git_root"] = "root"
	catDoc["sources"] = map[string]any{"skills": catSkills}
	catDoc["extras_source"] = skillshare.CatalogExtrasDir(dstAbs)
	catDoc["targets"] = map[string]any{}

	if len(repo.MCP) > 0 {
		srvs := map[string]any{}
		for name, m := range repo.MCP {
			srv := map[string]any{}
			if cmd, ok := m.Spec["command"].(string); ok && cmd != "" {
				srv["command"] = cmd
			}
			if args, ok := m.Spec["args"].([]any); ok && len(args) > 0 {
				var sa []string
				for _, a := range args {
					sa = append(sa, fmt.Sprint(a))
				}
				srv["args"] = sa
			}
			if u, ok := m.Spec["url"].(string); ok && u != "" {
				srv["transport"] = "streamable-http"
				srv["url"] = u
			}
			srvs[name] = srv
		}
		catDoc["mcp"] = map[string]any{"targets": []string{}, "servers": srvs}
	}
	if len(repo.Hooks) > 0 {
		ents := map[string]any{}
		for hid, h := range repo.Hooks {
			bindings := map[string]any{}
			for _, prov := range h.Providers {
				sp := h.ForProvider(prov)
				handler := map[string]any{"type": "command", "command": fmt.Sprint(sp["command"])}
				if to, ok := sp["timeout"]; ok && to != nil {
					handler["timeout"] = to
				}
				grp := map[string]any{"hooks": []any{handler}}
				if mt, ok := sp["matcher"].(string); ok && mt != "" {
					grp["matcher"] = mt
				}
				bindings[prov] = map[string]any{"events": map[string]any{fmt.Sprint(sp["event"]): []any{grp}}}
			}
			ed := map[string]any{"bindings": bindings}
			if h.Why != "" {
				ed["description"] = h.Why
			}
			ents[hid] = ed
		}
		catDoc["hooks"] = map[string]any{"entries": ents}
	}
	if b, err := yaml.Marshal(catDoc); err == nil {
		_ = os.WriteFile(catCfgPath, b, 0o644)
	}

	// Commit catalog/skillshare standalone git repo
	catDir := skillshare.CatalogDir(dstAbs)
	_ = exec.Command("git", "-C", catDir, "add", "-A").Run()
	_ = exec.Command("git", "-C", catDir, "commit", "-q", "-m", "Migrate skills, MCP, and hooks into livery catalog").Run()

	// 4. Migrate per-host target harness overrides from skillshare/hosts/<id>/config.yaml -> env-overrides/<id>.yaml
	for _, t := range repo.Targets {
		ov := skillshare.LoadEnvOverride(dstAbs, t)
		oldHostCfg := filepath.Join(srcAbs, "skillshare", "hosts", t.ID, "config.yaml")
		if b, err := os.ReadFile(oldHostCfg); err == nil {
			var hdoc map[string]any
			if yaml.Unmarshal(b, &hdoc) == nil && hdoc != nil {
				if m, ok := hdoc["mode"].(string); ok && m != "" {
					ov.Mode = m
				}
				if tm, ok := hdoc["targets"].(map[string]any); ok {
					cleanTargets := map[string]any{}
					for tn, rawT := range tm {
						sub, _ := rawT.(map[string]any)
						cleanSub := map[string]any{}
						if sub != nil {
							if scfg, ok := sub["skills"].(map[string]any); ok {
								if p, ok := scfg["path"].(string); ok && p != "" {
									cleanSub["skills"] = map[string]any{"path": p}
								}
							}
						}
						cleanTargets[tn] = cleanSub
					}
					ov.Targets = cleanTargets
				}
			}
		}
		// Seed Skillshare native projects from existing overlays on this host
		for _, proj := range repo.Overlays {
			if proj.Target != t.ID || proj.Path == "" {
				continue
			}
			if _, exists := ov.Projects[proj.Path]; !exists {
				entry := map[string]any{
					"name":    proj.Slug,
					"targets": t.Providers,
				}
				if len(proj.Skills) > 0 {
					entry["skills"] = map[string]any{"include": proj.Skills}
				}
				ov.Projects[proj.Path] = entry
			}
		}
		if err := skillshare.SaveEnvOverride(dstAbs, t.ID, ov); err != nil {
			return err
		}
	}

	// 5. Initialize parent XDG control-plane git repo (ignoring nested catalog/skillshare/ git repo!)
	gitignore := "catalog/skillshare/\nbuild/\n.DS_Store\n"
	_ = os.WriteFile(filepath.Join(dstAbs, ".gitignore"), []byte(gitignore), 0o644)
	if _, err := os.Stat(filepath.Join(dstAbs, ".git")); os.IsNotExist(err) {
		_ = exec.Command("git", "-C", dstAbs, "init", "-q").Run()
		_ = exec.Command("git", "-C", dstAbs, "config", "user.name", "livery").Run()
		_ = exec.Command("git", "-C", dstAbs, "config", "user.email", "livery@local").Run()
		_ = exec.Command("git", "-C", dstAbs, "add", "-A").Run()
		_ = exec.Command("git", "-C", dstAbs, "commit", "-q", "-m", "Initialize livery XDG control plane").Run()
	}

	// 6. Save source path (~/.config/livery/src) if srcAbs has go.mod so `liv env bootstrap` works from anywhere
	if _, err := os.Stat(filepath.Join(srcAbs, "go.mod")); err == nil {
		home, _ := os.UserHomeDir()
		srcMarker := filepath.Join(home, ".config", "livery", "src")
		_ = os.MkdirAll(filepath.Dir(srcMarker), 0o755)
		_ = os.WriteFile(srcMarker, []byte(srcAbs+"\n"), 0o644)
	}
	return nil
}

func copyPathRecursive(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		return os.WriteFile(dst, b, st.Mode().Perm())
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "__pycache__") {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(src, p)
		target := dst
		if rel != "." {
			target = filepath.Join(dst, rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		lst, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if lst.Mode()&os.ModeSymlink != 0 {
			lt, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Remove(target)
			return os.Symlink(lt, target)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		return os.WriteFile(target, b, lst.Mode().Perm())
	})
}
