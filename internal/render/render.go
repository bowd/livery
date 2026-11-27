package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bowd/livery/internal/launcher"
	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/skillshare"
)

type TreeFileMeta struct {
	SHA  string `json:"sha"`
	Exec bool   `json:"exec"`
}

type Item struct {
	Kind         string                  `json:"kind"` // file | tree | link | codex-mcp | claude-mcp | hook | codex-setting | herdr-program | t3-provider
	Path         string                  `json:"path"`
	Sources      []string                `json:"sources"`
	Content      []byte                  `json:"-"`
	Mode         string                  `json:"mode,omitempty"`
	Src          string                  `json:"src,omitempty"`
	Files        map[string]TreeFileMeta `json:"files,omitempty"`
	Target       string                  `json:"target,omitempty"`
	SubmoduleSHA string                  `json:"submodule_sha,omitempty"`
	Guard        string                  `json:"guard,omitempty"`
	Role         string                  `json:"role,omitempty"`
	Skill        string                  `json:"skill,omitempty"`
	Entry        string                  `json:"entry,omitempty"`
	Provider     string                  `json:"provider,omitempty"`
	File         string                  `json:"file,omitempty"`
	Event        string                  `json:"event,omitempty"`
	Command      string                  `json:"command,omitempty"`
	Spec         map[string]any          `json:"spec,omitempty"`
}

func (it *Item) IsEntry() bool {
	switch it.Kind {
	case "codex-mcp", "claude-mcp", "hook", "codex-setting", "herdr-program", "t3-provider":
		return true
	}
	return false
}

func (it *Item) SHA() string {
	if it.Kind == "tree" {
		return TreeSHA(it.Files)
	}
	if it.Kind == "link" {
		h := sha256.Sum256([]byte(it.Target))
		return hex.EncodeToString(h[:])
	}
	h := sha256.Sum256(it.Content)
	return hex.EncodeToString(h[:])
}

func TreeSHA(files map[string]TreeFileMeta) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		m := files[k]
		ex := "0"
		if m.Exec {
			ex = "1"
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", k, m.SHA, ex)
	}
	return hex.EncodeToString(h.Sum(nil))
}

var skipDirs = map[string]bool{".git": true, "__pycache__": true, "node_modules": true}

// WalkTree hashes a skill directory deterministically (matching Python payload.walk_tree).
func WalkTree(src string, excludeTop map[string]bool) (map[string]TreeFileMeta, error) {
	files := map[string]TreeFileMeta{}
	srcAbs, _ := filepath.Abs(src)
	err := filepath.WalkDir(srcAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == srcAbs {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Dir(p) == srcAbs && excludeTop[d.Name()] {
			return nil
		}
		if d.Name() == ".DS_Store" || d.Name() == ".localized" || strings.HasPrefix(d.Name(), "._") {
			return nil
		}
		rel, err := filepath.Rel(srcAbs, p)
		if err != nil {
			return err
		}
		lst, err := os.Lstat(p)
		if err != nil {
			return err
		}
		var sum [32]byte
		isExec := false
		if lst.Mode()&os.ModeSymlink != 0 {
			lt, err := os.Readlink(p)
			if err != nil {
				return err
			}
			sum = sha256.Sum256([]byte("link:" + lt))
		} else {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum = sha256.Sum256(b)
			isExec = lst.Mode().Perm()&0o111 != 0
		}
		files[filepath.ToSlash(rel)] = TreeFileMeta{
			SHA:  hex.EncodeToString(sum[:]),
			Exec: isExec,
		}
		return nil
	})
	return files, err
}

type Rendered struct {
	Items  []*Item
	Skills []string
}

func RenderPrompts(repo *model.Repo, t *model.Target, frags []string) ([]*Item, error) {
	var items []*Item
	for _, prov := range t.Providers {
		var ids []string
		var paths []string
		var bodies []string
		for _, fid := range frags {
			frag := repo.Fragments[fid]
			if frag == nil || !sliceHas(frag.Providers, prov) {
				continue
			}
			ids = append(ids, fid)
			paths = append(paths, frag.Path)
			bodies = append(bodies, frag.Body)
		}
		if len(ids) == 0 {
			continue
		}
		dest := t.ProviderPromptFiles[prov]
		if dest == "" {
			return nil, model.Errf("%s: no prompt file path for provider %s", t.ID, prov)
		}
		body := strings.Join(bodies, "\n\n") + "\n"
		// Only Claude strips HTML comments; Codex stays headerless.
		if prov == "claude" {
			commit := repo.LastCommit(paths)
			hdr := fmt.Sprintf("<!-- Managed by livery (commit %s; fragments: %s). Edit the fragments there; changes here are overwritten. -->\n\n",
				commit, strings.Join(ids, ", "))
			body = hdr + body
		}
		items = append(items, &Item{
			Kind: "file", Path: dest, Sources: ids, Content: []byte(body), Mode: "644", Role: "prompt",
		})
	}
	return items, nil
}

func RenderSkillshareBundle(repo *model.Repo, t *model.Target, _, _, _ []string) ([]*Item, []string, error) {
	sroot := strings.TrimRight(t.SkillshareRoot, "/")
	skills, _ := repo.ResolveHostCategoryItems(t, "skills")
	agents, _ := repo.ResolveHostCategoryItems(t, "agents")
	extras, _ := repo.ResolveHostCategoryItems(t, "extras")
	mcps, _ := repo.ResolveHostCategoryItems(t, "mcp")
	hooks, _ := repo.ResolveHostCategoryItems(t, "hooks")
	plugins, _ := repo.ResolveHostCategoryItems(t, "plugins")

	enabled := skillshare.EnvEnabledItems{
		Skills:  skills,
		Agents:  agents,
		Extras:  extras,
		MCP:     mcps,
		Hooks:   hooks,
		Plugins: plugins,
	}
	cfgBytes, selectedSkills, selectedAgents, err := skillshare.CompileEnvSkillshareConfig(repo.Root, t, enabled, "~")
	if err != nil {
		return nil, nil, err
	}

	var items []*Item
	var names []string
	for _, rs := range selectedSkills {
		files, err := WalkTree(rs.Dir, map[string]bool{"skill.toml": true})
		if err != nil {
			return nil, nil, err
		}
		names = append(names, rs.Name)
		items = append(items, &Item{
			Kind: "tree", Path: sroot + "/skills/" + rs.RelPath,
			Sources: []string{"catalog/skillshare/skills/" + rs.RelPath},
			Src:     rs.Dir,
			Files:   files,
			Role:    "skillshare-skill",
			Skill:   rs.Name,
		})
	}
	for _, ag := range selectedAgents {
		st, err := os.Stat(ag.Dir)
		if err != nil {
			continue
		}
		if st.IsDir() {
			files, err := WalkTree(ag.Dir, nil)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, &Item{
				Kind: "tree", Path: sroot + "/agents/" + ag.RelPath,
				Sources: []string{"catalog/skillshare/agents/" + ag.RelPath},
				Src:     ag.Dir,
				Files:   files,
				Role:    "skillshare-agent",
			})
		} else {
			b, err := os.ReadFile(ag.Dir)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, &Item{
				Kind: "file", Path: sroot + "/agents/" + ag.RelPath,
				Sources: []string{"catalog/skillshare/agents/" + ag.RelPath},
				Content: b, Mode: "644", Role: "skillshare-agent",
			})
		}
	}

	items = append(items, &Item{
		Kind: "file", Path: sroot + "/config.yaml",
		Sources: []string{"catalog/skillshare/config.yaml", "inventory.toml"},
		Content: cfgBytes, Mode: "644",
		Role: "skillshare-config",
		Spec: map[string]any{"enabled": enabled},
	})
	// Symlink stock skillshare's default XDG config dir (~/.config/skillshare) to the managed git repo
	// so plain `skillshare` CLI commands and external installers (e.g. `hyp setup`) auto-detect it without
	// SKILLSHARE_CONFIG, and atomic config.yaml writes still happen inside the host's live git repo.
	if sroot != "" && sroot != "~/.config/skillshare" {
		items = append(items, &Item{
			Kind:    "link",
			Path:    "~/.config/skillshare",
			Sources: []string{"catalog/skillshare/config.yaml", "inventory.toml"},
			Target:  sroot,
			Role:    "skillshare-config-link",
		})
	}
	return items, names, nil
}

func FinalizeSkillshareConfig(repo *model.Repo, t *model.Target, items []*Item, home string) {
	for _, it := range items {
		if it.Role != "skillshare-config" || it.Spec == nil {
			continue
		}
		enabled, ok := it.Spec["enabled"].(skillshare.EnvEnabledItems)
		if !ok {
			continue
		}
		if cfgBytes, _, _, err := skillshare.CompileEnvSkillshareConfig(repo.Root, t, enabled, home); err == nil {
			it.Content = cfgBytes
		}
	}
}

func RenderOverlays(repo *model.Repo, t *model.Target) ([]*Item, error) {
	var items []*Item
	for _, ov := range repo.Overlays {
		if ov.Target != t.ID {
			continue
		}
		root := strings.TrimRight(ov.Path, "/")
		srcID := fmt.Sprintf("projects/%s/%s", ov.Target, ov.Slug)
		for _, name := range ov.Files {
			f := filepath.Join(ov.Dir, name)
			data, err := os.ReadFile(f)
			if err != nil {
				return nil, model.Errf("%s: listed file %s is missing", srcID, name)
			}
			if strings.HasPrefix(name, "CLAUDE") && strings.HasSuffix(name, ".md") {
				hdr := fmt.Sprintf("<!-- Managed by livery (commit %s; %s/%s). Edit it there; changes here are overwritten. -->\n",
					repo.LastCommit([]string{f}), srcID, name)
				data = append([]byte(hdr), data...)
			}
			items = append(items, &Item{
				Kind: "file", Path: root + "/" + name, Sources: []string{srcID + "/" + name},
				Content: data, Mode: "644", Guard: root, Role: "overlay",
			})
		}
		for _, name := range ov.Skills {
			src := filepath.Join(ov.Dir, "skills", name)
			ref := srcID + "/skills/" + name
			if _, err := os.Stat(src); os.IsNotExist(err) {
				// Fall back to shared Catalog skill when assigned via Matrix!
				catSrc := filepath.Join(skillshare.CatalogSkillsDir(repo.Root), name)
				if _, cerr := os.Stat(catSrc); cerr == nil {
					src = catSrc
					ref = "catalog/skillshare/skills/" + name
				}
			}
			files, err := WalkTree(src, map[string]bool{"skill.toml": true})
			if err != nil {
				return nil, err
			}
			items = append(items,
				&Item{Kind: "tree", Path: root + "/.agents/skills/" + name, Sources: []string{ref}, Src: src, Files: files, Guard: root, Role: "overlay", Skill: name},
				&Item{Kind: "link", Path: root + "/.claude/skills/" + name, Sources: []string{ref}, Target: "../../.agents/skills/" + name, Guard: root, Role: "overlay", Skill: name},
			)
		}
		if ov.CodexTrust && sliceHas(t.Providers, "codex") {
			lbl := fmt.Sprintf("projects.%s.trust_level", model.TomlKey(root))
			items = append(items, &Item{
				Kind: "codex-setting", Path: t.CodexConfig + "#" + lbl, Sources: []string{srcID + "/project.toml"},
				Entry: lbl, Provider: "codex", File: t.CodexConfig, Role: "setting",
				Spec:    map[string]any{"table": []string{"projects", root}, "key": "trust_level", "value": "trusted"},
				Content: []byte(model.CanonJSON("trusted")),
			})
		}
		overlayMCPFiles := map[string]string{"claude": ".mcp.json", "codex": ".codex/config.toml"}
		cat := skillshare.LoadCatalog(repo.Root)
		catMCP := map[string]map[string]any{}
		for _, cm := range cat.MCP {
			catMCP[cm.Name] = cm.Raw
		}
		for _, prov := range t.Providers {
			var names []string
			specs := map[string]map[string]any{}
			srcs := []string{srcID + "/project.toml"}
			for _, mname := range ov.MCP {
				if m, ok := repo.MCP[mname]; ok {
					if !sliceHas(m.Providers, prov) {
						continue
					}
					names = append(names, mname)
					specs[mname] = m.ForProvider(prov)
					srcs = append(srcs, "mcp/"+mname+".toml")
				} else if cm, ok := catMCP[mname]; ok && cm != nil {
					names = append(names, mname)
					specs[mname] = cm
					srcs = append(srcs, "catalog/skillshare/config.yaml")
				}
			}
			if len(names) == 0 {
				continue
			}
			items = append(items, &Item{
				Kind: "file", Path: root + "/" + overlayMCPFiles[prov], Sources: srcs, Mode: "644",
				Guard: root, Role: "overlay-mcp", Provider: prov,
				Spec: map[string]any{"names": names, "servers": specs, "source": srcID},
			})
		}
	}
	return items, nil
}

// FinalizeOverlayMCP renders project-scoped .mcp.json and .codex/config.toml once target HOME is known.
func FinalizeOverlayMCP(items []*Item, home string) {
	subst := func(s string) string { return strings.ReplaceAll(s, "{home}", home) }
	for _, it := range items {
		if it.Role != "overlay-mcp" || it.Spec == nil {
			continue
		}
		names, _ := it.Spec["names"].([]string)
		servers, _ := it.Spec["servers"].(map[string]map[string]any)
		srcID := fmt.Sprint(it.Spec["source"])
		if it.Provider == "claude" {
			mcps := map[string]any{}
			for _, n := range names {
				sp := servers[n]
				tr, _ := sp["transport"].(string)
				if tr == "http" {
					obj := map[string]any{"type": "http", "url": subst(fmt.Sprint(sp["url"]))}
					mcps[n] = obj
				} else {
					var args []string
					if raw, ok := sp["args"].([]any); ok {
						for _, a := range raw {
							args = append(args, subst(fmt.Sprint(a)))
						}
					}
					mcps[n] = map[string]any{"command": subst(fmt.Sprint(sp["command"])), "args": args}
				}
			}
			b, _ := json.MarshalIndent(map[string]any{"mcpServers": mcps}, "", "  ")
			it.Content = append(b, '\n')
		} else {
			head := fmt.Sprintf("# Managed by agent-config (%s/project.toml and mcp/). Edit it there; changes\n# here are overwritten. Codex reads this file only while this folder is trusted.\n\n", srcID)
			var blocks []string
			for _, n := range names {
				sp := servers[n]
				lines := []string{fmt.Sprintf("[mcp_servers.%s]", model.TomlKey(n))}
				if cmd, ok := sp["command"].(string); ok && cmd != "" {
					lines = append(lines, "command = "+model.TomlScalar(subst(cmd)))
				}
				if raw, ok := sp["args"].([]any); ok && len(raw) > 0 {
					var sa []string
					for _, a := range raw {
						sa = append(sa, subst(fmt.Sprint(a)))
					}
					lines = append(lines, "args = "+model.TomlScalar(sa))
				}
				if u, ok := sp["url"].(string); ok && u != "" {
					lines = append(lines, "url = "+model.TomlScalar(subst(u)))
				}
				blocks = append(blocks, strings.Join(lines, "\n"))
			}
			it.Content = []byte(head + strings.Join(blocks, "\n\n") + "\n")
		}
	}
}

func RenderEntries(repo *model.Repo, t *model.Target, mcps, hooks []string) ([]*Item, error) {
	var items []*Item
	for _, name := range mcps {
		m := repo.MCP[name]
		src := []string{"mcp/" + name + ".toml"}
		for _, prov := range t.Providers {
			if !sliceHas(m.Providers, prov) {
				continue
			}
			sp := m.ForProvider(prov)
			if prov == "codex" {
				items = append(items, &Item{
					Kind: "codex-mcp", Path: t.CodexConfig + "#mcp_servers." + name, Sources: src,
					Entry: name, Provider: prov, File: t.CodexConfig, Spec: sp, Role: "mcp",
				})
			} else {
				items = append(items, &Item{
					Kind: "claude-mcp", Path: "claude-cli:user/" + name, Sources: src,
					Entry: name, Provider: prov, Spec: sp, Role: "mcp",
				})
			}
		}
	}
	scripts := map[string]*Item{}
	for _, hid := range hooks {
		h := repo.Hooks[hid]
		for _, prov := range t.Providers {
			if !sliceHas(h.Providers, prov) {
				continue
			}
			hf := t.ProviderHookFiles[prov]
			if hf == "" {
				return nil, model.Errf("%s: no provider_hook_files entry for %s", t.ID, prov)
			}
			sp := h.ForProvider(prov)
			items = append(items, &Item{
				Kind: "hook", Path: hf + "#hook:" + hid, Sources: []string{"hooks/" + hid + ".toml"},
				Entry: hid, Provider: prov, File: hf, Event: fmt.Sprint(sp["event"]), Spec: sp, Role: "hook",
			})
			if sc, ok := sp["script"].(string); ok && sc != "" {
				dest := strings.TrimRight(t.HookRoot, "/") + "/" + sc
				if _, exists := scripts[dest]; !exists {
					b, err := os.ReadFile(filepath.Join(repo.Root, "hooks", "scripts", sc))
					if err != nil {
						return nil, err
					}
					scripts[dest] = &Item{
						Kind: "file", Path: dest, Sources: []string{"hooks/scripts/" + sc},
						Content: b, Mode: "755", Role: "hook-script",
					}
				}
			}
		}
	}
	for _, it := range scripts {
		items = append(items, it)
	}
	if t.CodexHooksFeature != nil && sliceHas(t.Providers, "codex") {
		items = append(items, &Item{
			Kind: "codex-setting", Path: t.CodexConfig + "#features.hooks", Sources: []string{"inventory.toml"},
			Entry: "features.hooks", Provider: "codex", File: t.CodexConfig, Role: "setting",
			Spec:    map[string]any{"table": []string{"features"}, "key": "hooks", "value": *t.CodexHooksFeature},
			Content: []byte(model.CanonJSON(*t.CodexHooksFeature)),
		})
	}
	return items, nil
}

// RenderLaunchers emits launcher profile JSON files and assets (NO per-profile shims unless EmitShims=true).
func RenderLaunchers(repo *model.Repo, t *model.Target, launcherIDs []string) ([]*Item, error) {
	if len(launcherIDs) == 0 {
		return nil, nil
	}
	lroot := strings.TrimRight(t.LauncherRoot, "/")
	var items []*Item
	for _, lid := range launcherIDs {
		spec := repo.Launchers[lid]
		src := []string{"launchers/" + lid + ".toml"}
		doc := map[string]any{}
		for k, v := range spec.Raw {
			doc[k] = v
		}
		doc["id"] = spec.ID
		doc["profile"] = spec.Profile
		if _, ok := doc["mode"]; !ok {
			doc["mode"] = "direct"
		}
		if _, ok := doc["auth_mode"]; !ok {
			if drv := launcher.Drivers[spec.Harness]; drv != nil && len(drv.AuthModes()) > 0 {
				doc["auth_mode"] = drv.AuthModes()[0]
			}
		}
		if !t.EmitShims {
			doc["shim"] = ""
		}

		// Bundle Codex models_json or OpenCode config_asset
		if spec.Harness == "codex" {
			if cc, ok := doc["codex"].(map[string]any); ok {
				ccCopy := map[string]any{}
				for k, v := range cc {
					ccCopy[k] = v
				}
				if mj, ok := ccCopy["models_json"].(string); ok && mj != "" {
					mb, err := os.ReadFile(filepath.Join(repo.Root, "launchers", mj))
					if err != nil {
						return nil, err
					}
					ccCopy["models_json_path"] = lroot + "/assets/" + lid + "/models.json"
					delete(ccCopy, "models_json")
					items = append(items, &Item{
						Kind: "file", Path: lroot + "/assets/" + lid + "/models.json",
						Sources: append(append([]string(nil), src...), "launchers/"+mj),
						Content: mb, Mode: "644", Role: "launcher-asset", Entry: lid,
					})
				}
				doc["codex"] = ccCopy
			}
		} else if spec.Harness == "opencode" {
			if oc, ok := doc["opencode"].(map[string]any); ok {
				ocCopy := map[string]any{}
				for k, v := range oc {
					ocCopy[k] = v
				}
				if ca, ok := ocCopy["config_asset"].(string); ok && ca != "" {
					cb, err := os.ReadFile(filepath.Join(repo.Root, "launchers", ca))
					if err != nil {
						return nil, err
					}
					ocCopy["config_file_path"] = lroot + "/assets/" + lid + "/config.json"
					delete(ocCopy, "config_asset")
					items = append(items, &Item{
						Kind: "file", Path: lroot + "/assets/" + lid + "/config.json",
						Sources: append(append([]string(nil), src...), "launchers/"+ca),
						Content: cb, Mode: "644", Role: "launcher-asset", Entry: lid,
					})
				}
				doc["opencode"] = ocCopy
			}
		}

		specBytes, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, err
		}
		specBytes = append(specBytes, '\n')
		items = append(items, &Item{
			Kind: "file", Path: lroot + "/profiles/" + lid + ".json",
			Sources: src, Content: specBytes, Mode: "644", Role: "launcher-spec", Entry: lid,
		})
		if t.SyncHerdrTemplates {
			hcfg, _ := spec.Raw["herdr"].(map[string]any)
			enabled := true
			if hcfg != nil {
				if en, ok := hcfg["enabled"].(bool); ok && !en {
					enabled = false
				}
				if tp, ok := hcfg["template"].(bool); ok && !tp {
					enabled = false
				}
			}
			if enabled {
				tname := lid
				if hcfg != nil {
					if v, ok := hcfg["template_name"].(string); ok && v != "" {
						tname = v
					}
				}
				cmd := fmt.Sprintf("liv %s %s", spec.Harness, spec.Profile)
				if spec.Harness == "codex" {
					noDaemon := true
					if hcfg != nil {
						if v, ok := hcfg["no_daemon"].(bool); ok {
							noDaemon = v
						}
					}
					if noDaemon {
						cmd += " --no-daemon"
					}
				}
				desc, _ := spec.Raw["description"].(string)
				if hcfg != nil {
					if v, ok := hcfg["name"].(string); ok && v != "" {
						desc = v
					}
				}
				if desc == "" {
					desc = lid
				}
				tdir := strings.TrimRight(t.HerdrTemplatesDir, "/")
				if tdir == "" {
					tdir = "~/.config/herdr-core/templates"
				}
				tcontent := fmt.Sprintf("---\ncommand: %s\ndescription: %s\n---\n", cmd, desc)
				items = append(items, &Item{
					Kind: "file", Path: tdir + "/" + tname + ".md",
					Sources: src, Content: []byte(tcontent), Mode: "644", Role: "launcher-template", Entry: lid,
				})
			}
		}
	}
	return items, nil
}

// RenderTarget compiles all items for one target.
func RenderTarget(repo *model.Repo, t *model.Target) (*Rendered, error) {
	frags, _, err := repo.Resolve(t)
	if err != nil {
		return nil, err
	}
	pids, err := repo.ExpandProfiles(t.Profiles)
	if err != nil {
		return nil, err
	}
	mcps, _ := repo.ResolveHostCategoryItems(t, "mcp")
	hooks, _ := repo.ResolveHostCategoryItems(t, "hooks")
	launchers, err := repo.ResolveLaunchers(t, pids)
	if err != nil {
		return nil, err
	}

	promptItems, err := RenderPrompts(repo, t, frags)
	if err != nil {
		return nil, err
	}
	overlayItems, err := RenderOverlays(repo, t)
	if err != nil {
		return nil, err
	}
	launcherItems, err := RenderLaunchers(repo, t, launchers)
	if err != nil {
		return nil, err
	}

	skillItems, skillNames, err := RenderSkillshareBundle(repo, t, nil, mcps, hooks)
	if err != nil {
		return nil, err
	}
	entryItems, err := RenderEntries(repo, t, nil, nil)
	if err != nil {
		return nil, err
	}

	var all []*Item
	all = append(all, promptItems...)
	all = append(all, skillItems...)
	all = append(all, overlayItems...)
	all = append(all, entryItems...)
	all = append(all, launcherItems...)

	seen := map[string]bool{}
	for _, it := range all {
		if seen[it.Path] {
			return nil, model.Errf("%s: two items render to %s", t.ID, it.Path)
		}
		seen[it.Path] = true
	}
	return &Rendered{Items: all, Skills: skillNames}, nil
}

var safeFileRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// WriteBuild writes build/<target>/home/ and manifest.json for local inspection.
func WriteBuild(repo *model.Repo, t *model.Target, items []*Item) (string, error) {
	out := filepath.Join(repo.Root, "build", t.ID)
	home := filepath.Join(out, "home")
	_ = os.RemoveAll(filepath.Join(out, "entries"))
	_ = os.RemoveAll(home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	var manifest []map[string]any
	for _, it := range items {
		if it.IsEntry() {
			fname := strings.Trim(safeFileRe.ReplaceAllString(it.Entry, "_"), "_") + ".json"
			dest := filepath.Join(out, "entries", it.Provider, it.Kind, fname)
			_ = os.MkdirAll(filepath.Dir(dest), 0o755)
			_ = os.WriteFile(dest, append(it.Content, '\n'), 0o644)
			manifest = append(manifest, map[string]any{
				"kind": it.Kind, "path": it.Path, "sources": it.Sources, "sha256": it.SHA(),
			})
			continue
		}
		rel := strings.TrimPrefix(it.Path, "~/")
		if !strings.HasPrefix(it.Path, "~/") {
			rel = filepath.Join("_root", strings.TrimPrefix(it.Path, "/"))
		}
		dest := filepath.Join(home, filepath.FromSlash(rel))
		if it.Kind == "file" {
			_ = os.MkdirAll(filepath.Dir(dest), 0o755)
			_ = os.WriteFile(dest, it.Content, 0o644)
		} else if it.Kind == "tree" && it.Src != "" {
			_ = skillshare.CopyTree(it.Src, dest)
		}
		row := map[string]any{
			"kind": it.Kind, "path": it.Path, "sources": it.Sources, "sha256": it.SHA(),
		}
		if it.Kind == "link" {
			row["target"] = it.Target
		}
		if it.Src != "" {
			row["src"] = it.Src
		}
		manifest = append(manifest, row)
	}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	_ = os.WriteFile(filepath.Join(out, "manifest.json"), append(mb, '\n'), 0o644)
	return out, nil
}

// BuildMatrix regenerates provenance/matrix.md.
func BuildMatrix(repo *model.Repo) (string, error) {
	targets := repo.Targets
	perFrags := map[string][]string{}
	perSkills := map[string][]string{}
	perMCP := map[string][]string{}
	perHooks := map[string][]string{}
	perLaunch := map[string][]string{}
	for _, t := range targets {
		f, _ := repo.ResolveHostCategoryItems(t, "prompts")
		s, _ := repo.ResolveHostCategoryItems(t, "skills")
		m, _ := repo.ResolveHostCategoryItems(t, "mcp")
		h, _ := repo.ResolveHostCategoryItems(t, "hooks")
		l, _ := repo.ResolveHostCategoryItems(t, "launchers")
		perFrags[t.ID], perSkills[t.ID], perMCP[t.ID], perHooks[t.ID], perLaunch[t.ID] = f, s, m, h, l
	}

	var headCols []string
	for _, t := range targets {
		headCols = append(headCols, "`"+t.ID+"`")
	}
	head := "| | " + strings.Join(headCols, " | ") + " |"
	sep := "|---|" + strings.Repeat("---|", len(targets))

	lines := []string{
		"# Matrix: targets × prompt fragments × skills × MCP servers × hooks × launchers",
		"",
		"Generated by `liv matrix` from inventory.toml and profiles/. Do not edit by hand.",
		"",
		"## Skills", "", head, sep,
	}
	skillSet := map[string]bool{}
	for _, list := range perSkills {
		for _, s := range list {
			skillSet[s] = true
		}
	}
	var skillRefs []string
	for s := range skillSet {
		skillRefs = append(skillRefs, s)
	}
	sort.Strings(skillRefs)
	for _, s := range skillRefs {
		var cells []string
		for _, t := range targets {
			if sliceHas(perSkills[t.ID], s) {
				cells = append(cells, "✓")
			} else {
				cells = append(cells, "")
			}
		}
		lines = append(lines, fmt.Sprintf("| `%s` | %s |", s, strings.Join(cells, " | ")))
	}

	lines = append(lines, "", "## Launchers", "", head, sep)
	var lids []string
	for k := range repo.Launchers {
		lids = append(lids, k)
	}
	sort.Strings(lids)
	for _, lid := range lids {
		sp := repo.Launchers[lid]
		var cells []string
		for _, t := range targets {
			if sliceHas(perLaunch[t.ID], lid) {
				cells = append(cells, "✓")
			} else {
				cells = append(cells, "")
			}
		}
		lines = append(lines, fmt.Sprintf("| `%s %s` | %s |", sp.Harness, sp.Profile, strings.Join(cells, " | ")))
	}

	out := filepath.Join(repo.Root, "provenance", "matrix.md")
	_ = os.MkdirAll(filepath.Dir(out), 0o755)
	if err := os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return "", err
	}
	return out, nil
}

func sliceHas(list []string, val string) bool {
	for _, s := range list {
		if s == val {
			return true
		}
	}
	return false
}
