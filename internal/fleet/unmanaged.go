package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/remote"
	"github.com/bowd/livery/internal/skillshare"
	"gopkg.in/yaml.v3"
)

type UnmanagedItem struct {
	ID              string   `json:"id"`               // e.g. "skill:hyp-setup", "mcp:hyphae", "hook:claude-stop", "plugin:foo"
	Category        string   `json:"category"`         // "skills" | "mcp" | "hooks" | "plugins"
	Name            string   `json:"name"`             // logical item name
	Harnesses       []string `json:"harnesses"`        // e.g. ["claude", "codex"]
	Summary         string   `json:"summary"`          // redacted command/path summary
	Note            string   `json:"note,omitempty"`   // e.g. "harness-specific (hyphae profile=claude)"
	External        bool     `json:"external"`         // matched by inventory.toml external = [...]
	DefaultExternal bool     `json:"default_external"` // matched by [defaults].external
	ExternalPattern string   `json:"external_pattern"` // pattern to add/remove in external = [...]
	HasInlineEnv    bool     `json:"has_inline_env"`   // true if MCP server has literal env/header values (never exposed)
	EnvKeys         []string `json:"env_keys,omitempty"`
	HarnessSpecific bool     `json:"harness_specific,omitempty"`
	SourceTarget    string   `json:"source_target,omitempty"`
	SourcePath      string   `json:"source_path,omitempty"`
	Actions         []string `json:"actions"`
	DefaultAct      string   `json:"default_act"`
}

type UnmanagedReport struct {
	Target    string          `json:"target"`
	OK        bool            `json:"ok"`
	Error     string          `json:"error,omitempty"`
	CheckedAt int64           `json:"checked_at"`
	Items     []UnmanagedItem `json:"items"`
}

type UnmanagedDecision struct {
	ID     string `json:"id"`
	Action string `json:"action"` // "import_to_catalog" | "keep_local" | "mark_external" | "unmark_external" | "ignore"
}

type UnmanagedApplyReport struct {
	Target  string   `json:"target"`
	OK      bool     `json:"ok"`
	Error   string   `json:"error,omitempty"`
	Applied []string `json:"applied"`
}

type rawProbeParsed struct {
	home                string
	skills              []probedSkill
	mcpsByTarget        map[string][]probedMCP
	hooksByTgt          map[string][]probedHook
	unmanagedHookGroups []probedUnmanagedHookGroup
	claudePlugs         map[string]any
	claudeMkts          map[string]any
}

type probedUnmanagedHookGroup struct {
	Target string   `json:"target"`
	Path   string   `json:"path"`
	Names  []string `json:"names"`
}

type probedSkill struct {
	target     string
	name       string
	kind       string // "skill" | "skills-plugin" | "dir"
	dir        string
	hyphaeNote string
}

type probedMCP struct {
	Name   string         `json:"name"`
	From   string         `json:"from"`
	Server map[string]any `json:"server"`
}

type probedHook struct {
	Name  string         `json:"name"`
	Entry map[string]any `json:"entry"`
}

var hookProbeTargets = []string{"claude", "codex", "cursor", "pi", "omp", "opencode", "gemini", "copilot", "droid", "qwen", "antigravity", "amp"}

func buildUnmanagedProbeScript(sroot string) string {
	if sroot == "" {
		sroot = "~/.local/share/agent-config/skillshare"
	}
	rel := strings.TrimPrefix(strings.TrimRight(sroot, "/"), "~/")
	return fmt.Sprintf(`
set -u
UNAME_USER=$(id -un 2>/dev/null || echo "${USER:-}")
export PATH="$HOME/.local/bin:$HOME/.nix-profile/bin:/etc/profiles/per-user/$UNAME_USER/bin:/run/current-system/sw/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"
SS_ROOT="$HOME/%s"
CFG="$SS_ROOT/config.yaml"
printf '__HOME__=%%s\n' "$HOME"

printf '__SKILLS_START__\n'
scan_skill_dir() {
  tgt="$1"
  dir="$2"
  [ -d "$dir" ] || return 0
  for entry in "$dir"/*; do
    [ -e "$entry" ] || [ -L "$entry" ] || continue
    base=$(basename "$entry")
    case "$base" in
      .*|_*) continue ;;
    esac
    if [ -L "$entry" ]; then
      link_tgt=$(readlink "$entry" 2>/dev/null || true)
      case "$link_tgt" in
        *"/skillshare/skills/"*) continue ;;
      esac
    fi
    [ -d "$entry" ] || continue
    kind="dir"
    hnote=""
    if [ -d "$entry/.claude-plugin" ]; then
      kind="skills-plugin"
    elif [ -f "$entry/SKILL.md" ]; then
      kind="skill"
      hnote=$(head -n 12 "$entry/SKILL.md" 2>/dev/null | grep -o 'hyphae:managed[^-]*' | head -n 1 | tr '\t\r\n' '   ' || true)
    fi
    printf '%%s\t%%s\t%%s\t%%s\t%%s\n' "$tgt" "$base" "$kind" "$entry" "$hnote"
  done
}
scan_skill_dir "claude" "$HOME/.claude/skills"
scan_skill_dir "codex" "$HOME/.codex/skills"
scan_skill_dir "cursor" "$HOME/.cursor/skills"
scan_skill_dir "gemini" "$HOME/.gemini/skills"
scan_skill_dir "opencode" "$HOME/.config/opencode/skills"
scan_skill_dir "pi" "$HOME/.pi/agent/skills"
printf '__SKILLS_END__\n'

if command -v skillshare >/dev/null 2>&1 && [ -f "$CFG" ]; then
  probe_mcp() {
    tgt="$1"; check="$2"
    [ -e "$check" ] || return 0
    printf '__MCP_%%s_START__\n' "$tgt"
    SKILLSHARE_CONFIG="$CFG" NO_COLOR=1 skillshare mcp import --from "$tgt" -g --dry-run --json </dev/null 2>/dev/null || true
    printf '\n__MCP_%%s_END__\n' "$tgt"
  }
  probe_mcp "claude" "$HOME/.claude.json"
  probe_mcp "codex" "$HOME/.codex/config.toml"
  probe_mcp "cursor" "$HOME/.cursor/mcp.json"
  probe_mcp "opencode" "$HOME/.config/opencode"
  probe_mcp "gemini" "$HOME/.gemini/settings.json"

  printf '__HOOKS_LIST_START__\n'
  SKILLSHARE_CONFIG="$CFG" NO_COLOR=1 skillshare hooks list -g --json </dev/null 2>/dev/null || true
  printf '\n__HOOKS_LIST_END__\n'

  probe_hook() {
    tgt="$1"; check="$2"
    [ -e "$check" ] || return 0
    printf '__HOOKS_%%s_START__\n' "$tgt"
    SKILLSHARE_CONFIG="$CFG" NO_COLOR=1 skillshare hooks import --from "$tgt" -g --dry-run --json </dev/null 2>/dev/null || true
    printf '\n__HOOKS_%%s_END__\n' "$tgt"
  }
  probe_hook "claude" "$HOME/.claude/settings.json"
  probe_hook "codex" "$HOME/.codex/hooks.json"
  probe_hook "cursor" "$HOME/.cursor/hooks.json"
  probe_hook "pi" "$HOME/.pi/agent/extensions"
  probe_hook "omp" "$HOME/.omp/agent/extensions"
  probe_hook "opencode" "$HOME/.config/opencode/plugins"
  probe_hook "gemini" "$HOME/.gemini/settings.json"
  probe_hook "copilot" "$HOME/.copilot/hooks"
  if [ -e "$HOME/.factory/hooks.json" ]; then
    probe_hook "droid" "$HOME/.factory/hooks.json"
  else
    probe_hook "droid" "$HOME/.factory/settings.json"
  fi
  probe_hook "qwen" "$HOME/.qwen/settings.json"
  probe_hook "antigravity" "$HOME/.gemini/config/hooks.json"
  probe_hook "amp" "$HOME/.config/amp/plugins"
fi

printf '__CLAUDE_PLUGINS_START__\n'
if [ -f "$HOME/.claude/plugins/installed_plugins.json" ]; then
  cat "$HOME/.claude/plugins/installed_plugins.json"
fi
printf '\n__CLAUDE_PLUGINS_END__\n'

printf '__CLAUDE_MKT_START__\n'
if [ -f "$HOME/.claude/plugins/known_marketplaces.json" ]; then
  cat "$HOME/.claude/plugins/known_marketplaces.json"
fi
printf '\n__CLAUDE_MKT_END__\n'
`, rel)
}

func extractSection(raw, startMark, endMark string) string {
	s := strings.Index(raw, startMark+"\n")
	if s == -1 {
		return ""
	}
	s += len(startMark) + 1
	e := strings.Index(raw[s:], endMark)
	if e == -1 {
		return ""
	}
	return strings.TrimSpace(raw[s : s+e])
}

// sanitizeMCPServer inspects server["env"] and server["headers"] for literal values,
// records ONLY key names, and deletes literal values in place so secrets never persist in memory or disk.
func sanitizeMCPServer(srv map[string]any) (clean map[string]any, envKeys []string) {
	if srv == nil {
		return map[string]any{}, nil
	}
	out := map[string]any{}
	keySet := map[string]bool{}
	for k, v := range srv {
		kl := strings.ToLower(k)
		if kl == "env" || kl == "headers" || kl == "http_headers" {
			if m, ok := v.(map[string]any); ok {
				kept := map[string]any{}
				for ek, ev := range m {
					sv := strings.TrimSpace(fmt.Sprint(ev))
					if sv == "" {
						continue
					}
					// Keep ${VAR} interpolation references; redact any literal value
					if strings.HasPrefix(sv, "${") && strings.HasSuffix(sv, "}") {
						kept[ek] = sv
					} else {
						keySet[ek] = true
					}
				}
				if len(kept) > 0 {
					out[k] = kept
				}
			}
			continue
		}
		out[k] = v
	}
	for ek := range keySet {
		envKeys = append(envKeys, ek)
	}
	sort.Strings(envKeys)
	return out, envKeys
}

func replaceHomePlaceholder(v any, home string) any {
	if home == "" {
		return v
	}
	home = strings.TrimRight(home, "/")
	switch x := v.(type) {
	case string:
		if x == home {
			return "{home}"
		}
		if strings.HasPrefix(x, home+"/") {
			return "{home}" + x[len(home):]
		}
		return strings.ReplaceAll(x, home+"/", "{home}/")
	case []any:
		res := make([]any, len(x))
		for i, el := range x {
			res[i] = replaceHomePlaceholder(el, home)
		}
		return res
	case []string:
		res := make([]string, len(x))
		for i, el := range x {
			res[i] = fmt.Sprint(replaceHomePlaceholder(el, home))
		}
		return res
	case map[string]any:
		res := make(map[string]any, len(x))
		for k, val := range x {
			res[k] = replaceHomePlaceholder(val, home)
		}
		return res
	default:
		return v
	}
}

func prettyHome(s, home string) string {
	if home != "" {
		h := strings.TrimRight(home, "/")
		s = strings.ReplaceAll(s, h+"/", "~/")
		if s == h {
			s = "~"
		}
	}
	return strings.ReplaceAll(s, "{home}/", "~/")
}

func runUnmanagedProbe(t *model.Target) (*rawProbeParsed, error) {
	raw, err := remote.RunHostShell(t, buildUnmanagedProbeScript(t.SkillshareRoot), 45*time.Second)
	if err != nil {
		return nil, err
	}
	p := &rawProbeParsed{
		mcpsByTarget: map[string][]probedMCP{},
		hooksByTgt:   map[string][]probedHook{},
		claudePlugs:  map[string]any{},
		claudeMkts:   map[string]any{},
	}
	for _, ln := range strings.Split(raw, "\n") {
		if strings.HasPrefix(ln, "__HOME__=") {
			p.home = strings.TrimSpace(strings.TrimPrefix(ln, "__HOME__="))
			break
		}
	}
	if p.home == "" && t.IsLocal() {
		if t.Home != "" {
			p.home = t.Home
		} else {
			p.home, _ = os.UserHomeDir()
		}
	}
	for _, ln := range strings.Split(extractSection(raw, "__SKILLS_START__", "__SKILLS_END__"), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		parts := strings.SplitN(ln, "\t", 5)
		if len(parts) >= 4 {
			hn := ""
			if len(parts) == 5 {
				hn = strings.TrimSpace(parts[4])
			}
			p.skills = append(p.skills, probedSkill{
				target: parts[0], name: parts[1], kind: parts[2], dir: parts[3], hyphaeNote: hn,
			})
		}
	}
	for _, tgt := range []string{"claude", "codex", "cursor", "opencode", "gemini"} {
		blk := extractSection(raw, "__MCP_"+tgt+"_START__", "__MCP_"+tgt+"_END__")
		if blk == "" || !strings.HasPrefix(blk, "[") {
			continue
		}
		var list []probedMCP
		if json.Unmarshal([]byte(blk), &list) == nil && len(list) > 0 {
			p.mcpsByTarget[tgt] = list
		}
	}
	if hl := extractSection(raw, "__HOOKS_LIST_START__", "__HOOKS_LIST_END__"); strings.HasPrefix(hl, "{") {
		var doc struct {
			Unmanaged []probedUnmanagedHookGroup `json:"unmanaged"`
		}
		if json.Unmarshal([]byte(hl), &doc) == nil {
			p.unmanagedHookGroups = doc.Unmanaged
		}
	}
	for _, tgt := range hookProbeTargets {
		blk := extractSection(raw, "__HOOKS_"+tgt+"_START__", "__HOOKS_"+tgt+"_END__")
		if blk == "" || !strings.HasPrefix(blk, "[") {
			continue
		}
		var list []probedHook
		if json.Unmarshal([]byte(blk), &list) == nil && len(list) > 0 {
			p.hooksByTgt[tgt] = list
		}
	}
	if cp := extractSection(raw, "__CLAUDE_PLUGINS_START__", "__CLAUDE_PLUGINS_END__"); strings.HasPrefix(cp, "{") {
		_ = json.Unmarshal([]byte(cp), &p.claudePlugs)
	}
	if cm := extractSection(raw, "__CLAUDE_MKT_START__", "__CLAUDE_MKT_END__"); strings.HasPrefix(cm, "{") {
		_ = json.Unmarshal([]byte(cm), &p.claudeMkts)
	}
	return p, nil
}

func matchAnyGlob(patterns []string, val string) bool {
	for _, pat := range patterns {
		if ok, _ := filepath.Match(pat, val); ok || pat == val {
			return true
		}
	}
	return false
}

func matchPrefixedGlob(patterns []string, prefix, val string) bool {
	for _, pat := range patterns {
		if strings.HasPrefix(pat, prefix) {
			sub := pat[len(prefix):]
			if ok, _ := filepath.Match(sub, val); ok || sub == val {
				return true
			}
		}
	}
	return false
}

// InspectHostUnmanaged discovers native Skills, MCP servers, Hooks, and Plugins on t
// that are not currently managed by Livery's Catalog/Matrix or host overrides.
func InspectHostUnmanaged(repo *model.Repo, t *model.Target) UnmanagedReport {
	rep := UnmanagedReport{Target: t.ID, CheckedAt: time.Now().Unix(), Items: []UnmanagedItem{}}
	pr, err := runUnmanagedProbe(t)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	rep.OK = true

	ov := skillshare.LoadEnvOverride(repo.Root, t)
	_, defExt := model.TargetOwnExternal(repo.Root, t.ID)

	enSkills, _ := repo.ResolveHostCategoryItems(t, "skills")
	enMCP, _ := repo.ResolveHostCategoryItems(t, "mcp")
	enHooks, _ := repo.ResolveHostCategoryItems(t, "hooks")
	enPlugins, _ := repo.ResolveHostCategoryItems(t, "plugins")

	managedSkills := toBoolSet(enSkills)
	for _, s := range ov.LocalSkills {
		managedSkills[s] = true
	}
	managedMCP := toBoolSet(enMCP)
	for k := range ov.LocalMCP {
		managedMCP[k] = true
	}
	managedHooks := toBoolSet(enHooks)
	for k := range ov.LocalHooks {
		managedHooks[k] = true
	}
	managedPlugins := toBoolSet(enPlugins)
	for k := range ov.LocalPlugins {
		managedPlugins[k] = true
	}

	// Collect managed hook commands/signatures so we don't report hooks already managed under another entry name
	cat := skillshare.LoadCatalog(repo.Root)
	managedHookCmds := map[string]bool{}
	collectHookCmds := func(raw any) {
		b, _ := yaml.Marshal(raw)
		var doc map[string]any
		_ = yaml.Unmarshal(b, &doc)
		for _, c := range extractHookCommands(doc, pr.home) {
			managedHookCmds[c] = true
		}
	}
	for _, h := range cat.Hooks {
		if managedHooks[h.Name] {
			collectHookCmds(h.Raw)
		}
	}
	for _, raw := range ov.LocalHooks {
		collectHookCmds(raw)
	}

	// 1. Skills (deduplicated across harnesses)
	type aggSkill struct {
		name       string
		harnesses  []string
		kinds      map[string]bool
		hyphae     []string
		firstTgt   string
		firstDir   string
	}
	skillMap := map[string]*aggSkill{}
	var skillNames []string
	for _, ps := range pr.skills {
		ag, ok := skillMap[ps.name]
		if !ok {
			ag = &aggSkill{name: ps.name, kinds: map[string]bool{}, firstTgt: ps.target, firstDir: ps.dir}
			skillMap[ps.name] = ag
			skillNames = append(skillNames, ps.name)
		}
		if !containsString(ag.harnesses, ps.target) {
			ag.harnesses = append(ag.harnesses, ps.target)
		}
		ag.kinds[ps.kind] = true
		if ps.hyphaeNote != "" && !containsString(ag.hyphae, ps.hyphaeNote) {
			ag.hyphae = append(ag.hyphae, ps.hyphaeNote)
		}
	}
	sort.Strings(skillNames)
	for _, name := range skillNames {
		ag := skillMap[name]
		sort.Strings(ag.harnesses)
		isExt := matchAnyGlob(t.SkillExternal(), name)
		isDefExt := matchAnyGlob(defExt, name)
		harnessSpec := len(ag.hyphae) > 0 || ag.kinds["skills-plugin"]
		var notes []string
		if ag.kinds["skills-plugin"] {
			notes = append(notes, "Claude skills-dir plugin (.claude-plugin)")
		}
		if len(ag.hyphae) > 0 {
			notes = append(notes, "per-harness generated ("+ag.hyphae[0]+")")
		}
		if managedSkills[name] {
			notes = append(notes, "unmanaged local directory shadows Catalog skill")
		}
		actions := []string{"import_to_catalog", "keep_local", "mark_external", "ignore"}
		defAct := "import_to_catalog"
		if isExt {
			actions = []string{"ignore", "import_to_catalog", "keep_local", "unmark_external"}
			defAct = "ignore"
		} else if harnessSpec || ag.kinds["dir"] {
			defAct = "mark_external"
		}
		rep.Items = append(rep.Items, UnmanagedItem{
			ID:              "skills:" + name,
			Category:        "skills",
			Name:            name,
			Harnesses:       ag.harnesses,
			Summary:         prettyHome(ag.firstDir, pr.home),
			Note:            strings.Join(notes, " · "),
			External:        isExt,
			DefaultExternal: isDefExt,
			ExternalPattern: name,
			HarnessSpecific: harnessSpec,
			SourceTarget:    ag.firstTgt,
			SourcePath:      ag.firstDir,
			Actions:         actions,
			DefaultAct:      defAct,
		})
	}

	// 2. MCP servers
	type aggMCP struct {
		name      string
		harnesses []string
		firstTgt  string
		firstSrv  map[string]any
		envKeys   []string
		differs   bool
	}
	mcpMap := map[string]*aggMCP{}
	var mcpNames []string
	for _, tgt := range []string{"claude", "codex", "cursor", "opencode", "gemini"} {
		for _, pm := range pr.mcpsByTarget[tgt] {
			if managedMCP[pm.Name] {
				continue
			}
			cleanSrv, ekeys := sanitizeMCPServer(pm.Server)
			normSrv, _ := replaceHomePlaceholder(cleanSrv, pr.home).(map[string]any)
			// Strip harmless transport="stdio" differences before comparing across harnesses
			cmpSrv := map[string]any{}
			for k, v := range normSrv {
				if k == "transport" || k == "type" || k == "env_vars" {
					continue
				}
				cmpSrv[k] = v
			}
			ag, ok := mcpMap[pm.Name]
			if !ok {
				ag = &aggMCP{name: pm.Name, firstTgt: tgt, firstSrv: cmpSrv}
				mcpMap[pm.Name] = ag
				mcpNames = append(mcpNames, pm.Name)
			} else if !reflect.DeepEqual(ag.firstSrv, cmpSrv) {
				ag.differs = true
			}
			if !containsString(ag.harnesses, tgt) {
				ag.harnesses = append(ag.harnesses, tgt)
			}
			for _, ek := range ekeys {
				if !containsString(ag.envKeys, ek) {
					ag.envKeys = append(ag.envKeys, ek)
				}
			}
		}
	}
	sort.Strings(mcpNames)
	for _, name := range mcpNames {
		ag := mcpMap[name]
		sort.Strings(ag.harnesses)
		sort.Strings(ag.envKeys)
		cmdStr := fmt.Sprint(ag.firstSrv["command"])
		if urlStr, ok := ag.firstSrv["url"].(string); ok && urlStr != "" {
			cmdStr = urlStr
		} else if args, ok := ag.firstSrv["args"].([]any); ok && len(args) > 0 {
			var as []string
			for _, a := range args {
				as = append(as, fmt.Sprint(a))
			}
			cmdStr = strings.TrimSpace(cmdStr + " " + strings.Join(as, " "))
		}
		isExt := t.ExternalMCP(name)
		isDefExt := matchPrefixedGlob(defExt, "mcp:", name)
		var notes []string
		if ag.differs {
			notes = append(notes, "per-harness CLI args differ across "+strings.Join(ag.harnesses, "/"))
		}
		if len(ag.envKeys) > 0 {
			notes = append(notes, "inline env redacted ("+strings.Join(ag.envKeys, ", ")+")")
		}
		hasEnv := len(ag.envKeys) > 0
		actions := []string{"import_to_catalog", "keep_local", "mark_external", "ignore"}
		defAct := "import_to_catalog"
		if hasEnv {
			// Never commit literal env secrets; allow stripping env on import or marking external
			actions = []string{"mark_external", "import_to_catalog", "keep_local", "ignore"}
			defAct = "mark_external"
		}
		if isExt {
			actions = []string{"ignore", "import_to_catalog", "keep_local"}
			if !isDefExt {
				actions = append(actions, "unmark_external")
			}
			defAct = "ignore"
		} else if ag.differs {
			defAct = "mark_external"
		}
		rep.Items = append(rep.Items, UnmanagedItem{
			ID:              "mcp:" + name,
			Category:        "mcp",
			Name:            name,
			Harnesses:       ag.harnesses,
			Summary:         prettyHome(cmdStr, pr.home),
			Note:            strings.Join(notes, " · "),
			External:        isExt,
			DefaultExternal: isDefExt,
			ExternalPattern: "mcp:" + name,
			HasInlineEnv:    hasEnv,
			EnvKeys:         ag.envKeys,
			HarnessSpecific: ag.differs,
			SourceTarget:    ag.firstTgt,
			Actions:         actions,
			DefaultAct:      defAct,
		})
	}

	// 3. Hooks (across all supported agents, merging multi-agent entries that share the same logical name)
	type aggHook struct {
		name      string
		harnesses []string
		firstTgt  string
		firstCmd  string
		note      string
	}
	hookAgg := map[string]*aggHook{}
	var hookOrder []string
	for _, tgt := range hookProbeTargets {
		for _, ph := range pr.hooksByTgt[tgt] {
			canonName := canonicalizeImportedHookName(tgt, ph)
			if managedHooks[canonName] || managedHooks[ph.Name] {
				continue
			}
			cmds := extractHookCommands(ph.Entry, pr.home)
			var unmgCmds []string
			for _, c := range cmds {
				if !managedHookCmds[c] {
					unmgCmds = append(unmgCmds, c)
				}
			}
			if len(unmgCmds) == 0 && len(cmds) > 0 {
				continue
			}
			ag, ok := hookAgg[canonName]
			if !ok {
				fc := ""
				if len(unmgCmds) > 0 {
					fc = unmgCmds[0]
				}
				note := detectHookToolNote(canonName, fc)
				if bm, _ := nestedBindingMap(ph.Entry, tgt); bm != nil && bm["code"] != nil {
					if note != "" {
						note += " · extension file adopted on import"
					} else {
						note = "extension file adopted on import"
					}
				}
				ag = &aggHook{name: canonName, firstTgt: tgt, firstCmd: fc, note: note}
				hookAgg[canonName] = ag
				hookOrder = append(hookOrder, canonName)
			}
			if !containsString(ag.harnesses, tgt) {
				ag.harnesses = append(ag.harnesses, tgt)
			}
		}
	}
	for _, name := range hookOrder {
		ag := hookAgg[name]
		sort.Strings(ag.harnesses)
		isExt := (ag.firstCmd != "" && t.ExternalHook(ag.firstCmd)) || t.ExternalHook(name)
		isDefExt := ag.firstCmd != "" && matchPrefixedGlob(defExt, "hook:", ag.firstCmd)
		extPat := "hook:" + name
		actions := []string{"import_to_catalog", "keep_local", "mark_external", "ignore"}
		defAct := "import_to_catalog"
		if detectHookToolNote(name, ag.firstCmd) != "" {
			defAct = "mark_external"
		}
		if isExt {
			actions = []string{"ignore", "import_to_catalog", "keep_local", "unmark_external"}
			defAct = "ignore"
		}
		rep.Items = append(rep.Items, UnmanagedItem{
			ID:              "hooks:" + name,
			Category:        "hooks",
			Name:            name,
			Harnesses:       ag.harnesses,
			Summary:         ag.firstCmd,
			Note:            ag.note,
			External:        isExt,
			DefaultExternal: isDefExt,
			ExternalPattern: extPat,
			SourceTarget:    ag.firstTgt,
			Actions:         actions,
			DefaultAct:      defAct,
		})
	}
	// Also surface non-importable unmanaged hook sections reported by `skillshare hooks list`
	// (e.g. inline `[hooks.state]` trust table in `~/.codex/config.toml` that Skillshare leaves untouched).
	for _, ug := range pr.unmanagedHookGroups {
		if len(pr.hooksByTgt[ug.Target]) > 0 {
			continue
		}
		for _, rawName := range ug.Names {
			entryName := ug.Target + "-" + rawName
			if managedHooks[entryName] || hookAgg[entryName] != nil {
				continue
			}
			sum := fmt.Sprintf("%s (%s)", prettyHome(ug.Path, pr.home), rawName)
			note := "unmanaged native hook entry (Skillshare leaves untouched; not directly importable)"
			if ug.Target == "codex" && strings.HasSuffix(ug.Path, "config.toml") && rawName == "state" {
				note = "Codex internal hook trust table [hooks.state] in config.toml (not a runnable hook; Skillshare leaves untouched)"
			}
			isExt := t.ExternalHook(entryName) || t.ExternalHook(rawName)
			isDefExt := matchPrefixedGlob(defExt, "hook:", entryName)
			actions := []string{"mark_external", "ignore"}
			defAct := "mark_external"
			if isExt {
				actions = []string{"ignore"}
				if !isDefExt {
					actions = append(actions, "unmark_external")
				}
				defAct = "ignore"
			}
			rep.Items = append(rep.Items, UnmanagedItem{
				ID:              "hooks:" + entryName,
				Category:        "hooks",
				Name:            entryName,
				Harnesses:       []string{ug.Target},
				Summary:         sum,
				Note:            note,
				External:        isExt,
				DefaultExternal: isDefExt,
				ExternalPattern: "hook:" + entryName,
				HarnessSpecific: true,
				SourceTarget:    ug.Target,
				SourcePath:      ug.Path,
				Actions:         actions,
				DefaultAct:      defAct,
			})
		}
	}

	// 4. Native Claude Marketplace Plugins (excluding @skillshare-* managed installs)
	if pm, ok := pr.claudePlugs["plugins"].(map[string]any); ok {
		var pkeys []string
		for k := range pm {
			pkeys = append(pkeys, k)
		}
		sort.Strings(pkeys)
		for _, fullID := range pkeys {
			if strings.Contains(fullID, "@skillshare-") {
				continue
			}
			shortName, mktName, _ := strings.Cut(fullID, "@")
			if managedPlugins[shortName] {
				continue
			}
			repoURL := resolveMarketplaceRepoURL(pr.claudeMkts, mktName)
			sum := fullID
			if repoURL != "" {
				sum = fmt.Sprintf("%s (%s)", fullID, strings.TrimSuffix(strings.TrimPrefix(repoURL, "https://github.com/"), ".git"))
			}
			isExt := matchPrefixedGlob(t.External, "plugin:", shortName)
			actions := []string{"import_to_catalog", "keep_local", "mark_external", "ignore"}
			defAct := "import_to_catalog"
			if repoURL == "" {
				defAct = "mark_external"
			}
			if isExt {
				actions = []string{"ignore", "import_to_catalog", "keep_local", "unmark_external"}
				defAct = "ignore"
			}
			rep.Items = append(rep.Items, UnmanagedItem{
				ID:              "plugins:" + shortName,
				Category:        "plugins",
				Name:            shortName,
				Harnesses:       []string{"claude"},
				Summary:         sum,
				External:        isExt,
				ExternalPattern: "plugin:" + shortName,
				SourceTarget:    "claude",
				SourcePath:      repoURL,
				Actions:         actions,
				DefaultAct:      defAct,
			})
		}
	}

	return rep
}

func nestedBindingMap(entry map[string]any, tgt string) (map[string]any, bool) {
	bmap, _ := entry["bindings"].(map[string]any)
	if bmap == nil {
		return nil, false
	}
	bm, ok := bmap[tgt].(map[string]any)
	return bm, ok
}

// canonicalizeImportedHookName normalizes single-event command hooks to `<target>-<event>` (e.g.
// `gemini-aftertool`, `antigravity-sessionstart`) instead of `argv[0]` names like `hyp-2` or `python3-4`,
// while keeping logical filenames on code-extension hooks (`tmux-mesh-activity`, `agents-mesh`, etc.).
func canonicalizeImportedHookName(tgt string, ph probedHook) string {
	bm, ok := nestedBindingMap(ph.Entry, tgt)
	if !ok || bm == nil {
		return ph.Name
	}
	if cs, _ := bm["code"].(string); strings.TrimSpace(cs) != "" {
		return ph.Name
	}
	if evMap, ok := bm["events"].(map[string]any); ok && len(evMap) == 1 {
		for evName := range evMap {
			if strings.TrimSpace(evName) != "" {
				return strings.ToLower(tgt + "-" + evName)
			}
		}
	}
	return ph.Name
}

func detectHookToolNote(name, summary string) string {
	combined := strings.ToLower(name + " " + summary)
	switch {
	case strings.Contains(combined, "tmux-mesh"):
		return "installed by tmux-mesh"
	case strings.Contains(combined, "agents-mesh"):
		return "installed by agents-mesh"
	case strings.Contains(combined, "hyphae") || strings.Contains(combined, "hyp hook "):
		return "installed by hyphae"
	case strings.Contains(combined, "herdr"):
		return "installed by herdr"
	default:
		return ""
	}
}

func extractHookCommands(entry map[string]any, home string) []string {
	var out []string
	bmap, _ := entry["bindings"].(map[string]any)
	for tgt, bv := range bmap {
		bm, _ := bv.(map[string]any)
		if bm == nil {
			continue
		}
		// Code-based extension hooks (Pi, OMP, OpenCode)
		if codeStr, ok := bm["code"].(string); ok && strings.TrimSpace(codeStr) != "" {
			firstLine := strings.TrimSpace(strings.SplitN(strings.TrimSpace(codeStr), "\n", 2)[0])
			out = append(out, prettyHome(fmt.Sprintf("[%s code] %s", tgt, firstLine), home))
		}
		evMap, _ := bm["events"].(map[string]any)
		for _, evList := range evMap {
			arr, _ := evList.([]any)
			for _, m := range arr {
				mm, _ := m.(map[string]any)
				if mm == nil {
					continue
				}
				// Cursor flat command style: events.<ev>[].command
				if directCmd, ok := mm["command"].(string); ok && directCmd != "" {
					out = append(out, prettyHome(directCmd, home))
				}
				// Claude / Codex nested style: events.<ev>[].hooks[].command
				hlist, _ := mm["hooks"].([]any)
				for _, h := range hlist {
					hm, _ := h.(map[string]any)
					if c, _ := hm["command"].(string); c != "" {
						out = append(out, prettyHome(c, home))
					}
				}
			}
		}
	}
	return out
}

func resolveMarketplaceRepoURL(mkts map[string]any, mktName string) string {
	m, _ := mkts[mktName].(map[string]any)
	if m == nil {
		return ""
	}
	src, _ := m["source"].(map[string]any)
	if src == nil {
		return ""
	}
	if repo, _ := src["repo"].(string); repo != "" {
		if strings.HasPrefix(repo, "http://") || strings.HasPrefix(repo, "https://") {
			return repo
		}
		return "https://github.com/" + repo + ".git"
	}
	if url, _ := src["url"].(string); url != "" {
		return url
	}
	return ""
}

// ImportHostUnmanaged applies user decisions for native/external items discovered on t.
func ImportHostUnmanaged(repo *model.Repo, t *model.Target, decisions []UnmanagedDecision) UnmanagedApplyReport {
	out := UnmanagedApplyReport{Target: t.ID, Applied: []string{}}
	pr, err := runUnmanagedProbe(t)
	if err != nil {
		out.Error = err.Error()
		return out
	}

	// Also index current report metadata by ID
	curRep := InspectHostUnmanaged(repo, t)
	byID := map[string]UnmanagedItem{}
	for _, it := range curRep.Items {
		byID[it.ID] = it
	}

	ownExt, _ := model.TargetOwnExternal(repo.Root, t.ID)
	extDirty := false
	addExt := func(pat string) {
		if pat != "" && !containsString(ownExt, pat) {
			ownExt = append(ownExt, pat)
			extDirty = true
		}
	}
	remExt := func(pat string) {
		var next []string
		for _, x := range ownExt {
			if x == pat {
				extDirty = true
				continue
			}
			next = append(next, x)
		}
		ownExt = next
	}

	ov := skillshare.LoadEnvOverride(repo.Root, t)
	ovDirty := false
	cat := skillshare.LoadCatalog(repo.Root)
	catDoc := cat.RawConfig
	if catDoc == nil {
		catDoc = map[string]any{}
	}
	catDirty := false
	var adoptSkillDirs []string
	var adoptHookFiles []string

	for _, d := range decisions {
		if d.Action == "" || d.Action == "ignore" {
			continue
		}
		it, ok := byID[d.ID]
		if !ok {
			continue
		}
		switch d.Action {
		case "mark_external":
			addExt(it.ExternalPattern)
			out.Applied = append(out.Applied, fmt.Sprintf("marked %s as external on %s", it.ExternalPattern, t.ID))
			continue
		case "unmark_external":
			remExt(it.ExternalPattern)
			out.Applied = append(out.Applied, fmt.Sprintf("unmarked %s from external on %s", it.ExternalPattern, t.ID))
			continue
		}

		// Importing removes any target-level external pattern first so CheckRepo allows managing it
		remExt(it.ExternalPattern)

		switch it.Category {
		case "skills":
			if it.SourcePath == "" {
				continue
			}
			if d.Action == "import_to_catalog" {
				dst := filepath.Join(skillshare.CatalogSkillsDir(repo.Root), it.Name)
				if _, err := remote.RsyncPullDir(t, it.SourcePath, dst, false, true); err != nil {
					out.Error = err.Error()
					return out
				}
				catDirty = true
				// Collect matching unmanaged skill dirs across harnesses on host to back up cleanly
				for _, ps := range pr.skills {
					if ps.name == it.Name {
						adoptSkillDirs = append(adoptSkillDirs, ps.dir)
					}
				}
				out.Applied = append(out.Applied, fmt.Sprintf("imported skill %q into Catalog", it.Name))
			} else if d.Action == "keep_local" {
				sroot := strings.TrimRight(t.SkillshareRoot, "/")
				if sroot == "" {
					sroot = "~/.local/share/agent-config/skillshare"
				}
				rel := strings.TrimPrefix(sroot, "~/")
				script := fmt.Sprintf(`mkdir -p "$HOME/%s/skills" && cp -R %q "$HOME/%s/skills/%s"`, rel, it.SourcePath, rel, it.Name)
				if _, err := remote.RunHostShell(t, script, 30*time.Second); err != nil {
					out.Error = err.Error()
					return out
				}
				if !containsString(ov.LocalSkills, it.Name) {
					ov.LocalSkills = append(ov.LocalSkills, it.Name)
					sort.Strings(ov.LocalSkills)
					ovDirty = true
				}
				for _, ps := range pr.skills {
					if ps.name == it.Name {
						adoptSkillDirs = append(adoptSkillDirs, ps.dir)
					}
				}
				out.Applied = append(out.Applied, fmt.Sprintf("saved skill %q as local override on %s", it.Name, t.ID))
			}

		case "mcp":
			var foundSrv map[string]any
			for _, pm := range pr.mcpsByTarget[it.SourceTarget] {
				if pm.Name == it.Name {
					cleanSrv, _ := sanitizeMCPServer(pm.Server)
					foundSrv, _ = replaceHomePlaceholder(cleanSrv, pr.home).(map[string]any)
					break
				}
			}
			if foundSrv == nil {
				continue
			}
			if d.Action == "import_to_catalog" {
				if _, hasTgts := foundSrv["targets"]; !hasTgts {
					foundSrv["targets"] = []any{}
				}
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
				srvs[it.Name] = foundSrv
				catDirty = true
				out.Applied = append(out.Applied, fmt.Sprintf("imported MCP %q into Catalog (secrets stripped)", it.Name))
			} else if d.Action == "keep_local" {
				ov.LocalMCP[it.Name] = foundSrv
				ovDirty = true
				out.Applied = append(out.Applied, fmt.Sprintf("saved MCP %q as local override on %s", it.Name, t.ID))
			}

		case "hooks":
			origHookName := it.Name
			mergedBindings := map[string]any{}
			for _, tgt := range hookProbeTargets {
				for _, ph := range pr.hooksByTgt[tgt] {
					if canonicalizeImportedHookName(tgt, ph) == origHookName || ph.Name == origHookName {
						normEntry, _ := replaceHomePlaceholder(ph.Entry, pr.home).(map[string]any)
						if bmap, ok := normEntry["bindings"].(map[string]any); ok {
							for bk, bv := range bmap {
								mergedBindings[bk] = bv
							}
						}
					}
				}
			}
			if len(mergedBindings) == 0 {
				continue
			}
			// Stock `skillshare` writes code hooks (`pi`, `omp`, `opencode`, `amp`) as `skillshare-<name>.ts`
			// WITHOUT removing the original `<name>.ts`, causing double-execution and endless duplicate imports!
			// Back up the original extension file on adoption so Skillshare owns a single clean copy on sync.
			for _, ug := range pr.unmanagedHookGroups {
				bm, ok := mergedBindings[ug.Target].(map[string]any)
				if !ok || bm == nil || bm["code"] == nil || ug.Path == "" {
					continue
				}
				for _, rawFile := range ug.Names {
					if strings.HasPrefix(rawFile, "skillshare-") {
						continue
					}
					stem := strings.TrimSuffix(rawFile, filepath.Ext(rawFile))
					if stem == origHookName {
						adoptHookFiles = append(adoptHookFiles, filepath.Join(ug.Path, rawFile))
					}
				}
			}
			foundEntry := map[string]any{"bindings": mergedBindings}
			if d.Action == "import_to_catalog" {
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
				// Reuse existing entry name if an identical hook binding already exists in Catalog
				targetName := it.Name
				for existingName, existingVal := range ents {
					if reflect.DeepEqual(existingVal, foundEntry) {
						targetName = existingName
						break
					}
				}
				ents[targetName] = foundEntry
				it.Name = targetName
				byID[d.ID] = it
				catDirty = true
				out.Applied = append(out.Applied, fmt.Sprintf("imported hook %q into Catalog", targetName))
			} else if d.Action == "keep_local" {
				ov.LocalHooks[it.Name] = foundEntry
				ovDirty = true
				out.Applied = append(out.Applied, fmt.Sprintf("saved hook %q as local override on %s", it.Name, t.ID))
			}

		case "plugins":
			if it.SourcePath == "" {
				continue
			}
			pkgSpec := map[string]any{
				"source": it.SourcePath,
				"plugin": it.Name,
				"bindings": map[string]any{
					"claude": map[string]any{
						"source": it.SourcePath,
						"plugin": it.Name,
						"sync":   true,
					},
				},
			}
			if d.Action == "import_to_catalog" {
				catSpec := map[string]any{
					"source":   it.SourcePath,
					"plugin":   it.Name,
					"bindings": map[string]any{},
				}
				pSec, _ := catDoc["plugins"].(map[string]any)
				if pSec == nil {
					pSec = map[string]any{"version": 2}
					catDoc["plugins"] = pSec
				}
				pSec["version"] = 2
				pkgs, _ := pSec["packages"].(map[string]any)
				if pkgs == nil {
					pkgs = map[string]any{}
					pSec["packages"] = pkgs
				}
				pkgs[it.Name] = catSpec
				catDirty = true
				out.Applied = append(out.Applied, fmt.Sprintf("imported plugin %q into Catalog", it.Name))
			} else if d.Action == "keep_local" {
				ov.LocalPlugins[it.Name] = pkgSpec
				ovDirty = true
				out.Applied = append(out.Applied, fmt.Sprintf("saved plugin %q as local override on %s", it.Name, t.ID))
			}
		}
	}

	if extDirty {
		if _, err := model.UpdateTargetInInventory(repo.Root, t.ID, map[string]any{"external": nonEmptySlice(ownExt)}); err != nil {
			out.Error = err.Error()
			return out
		}
	}
	if catDirty {
		if b, err := yaml.Marshal(catDoc); err == nil {
			_ = os.WriteFile(skillshare.CatalogConfigPath(repo.Root), b, 0o644)
		}
		skillshare.NormalizeCatalogLibraryConfig(repo.Root)
		catDir := skillshare.CatalogDir(repo.Root)
		if _, err := os.Stat(filepath.Join(catDir, ".git")); err == nil {
			_ = exec.Command("git", "-C", catDir, "add", "-A").Run()
			_ = exec.Command("git", "-C", catDir, "commit", "-q", "-m", "Import native items from "+t.ID).Run()
		}
	}
	if ovDirty {
		if err := skillshare.SaveEnvOverride(repo.Root, t.ID, ov); err != nil {
			out.Error = err.Error()
			return out
		}
	}

	// Enable any Catalog-imported items on t.ID in Matrix (after Catalog & external updates are written)
	for _, d := range decisions {
		if d.Action != "import_to_catalog" {
			continue
		}
		it, ok := byID[d.ID]
		if !ok {
			continue
		}
		if _, err := model.SetMatrixCell(repo.Root, it.Category, it.Name, t.ID, true); err != nil {
			out.Error = err.Error()
			return out
		}
	}

	// Safely move adopted plain skill directories into BackupRoot so Skillshare can link cleanly on Sync
	if len(adoptSkillDirs) > 0 {
		broot := strings.TrimRight(t.BackupRoot, "/")
		if broot == "" {
			broot = "~/.local/share/agent-config/backup"
		}
		bRel := strings.TrimPrefix(broot, "~/")
		stamp := time.Now().UTC().Format("20060102T150405Z")
		var mvLines []string
		mvLines = append(mvLines, fmt.Sprintf(`BDIR="$HOME/%s/adopted-skills-%s"; mkdir -p "$BDIR"`, bRel, stamp))
		for i, dir := range adoptSkillDirs {
			mvLines = append(mvLines, fmt.Sprintf(`if [ -d %q ] && [ ! -L %q ]; then mv %q "$BDIR/%d-$(basename %q)"; fi`, dir, dir, dir, i, dir))
		}
		_, _ = remote.RunHostShell(t, strings.Join(mvLines, "\n"), 20*time.Second)
	}

	// Safely move adopted code-extension hook files (`pi`, `omp`, `opencode`, `amp`) into BackupRoot so
	// stock Skillshare's `skillshare-<name>.ts` output replaces them cleanly instead of running twice.
	if len(adoptHookFiles) > 0 {
		broot := strings.TrimRight(t.BackupRoot, "/")
		if broot == "" {
			broot = "~/.local/share/agent-config/backup"
		}
		bRel := strings.TrimPrefix(broot, "~/")
		stamp := time.Now().UTC().Format("20060102T150405Z")
		var mvLines []string
		mvLines = append(mvLines, fmt.Sprintf(`BDIR="$HOME/%s/adopted-hooks-%s"; mkdir -p "$BDIR"`, bRel, stamp))
		for i, fpath := range adoptHookFiles {
			mvLines = append(mvLines, fmt.Sprintf(`if [ -f %q ] && [ ! -L %q ]; then mv %q "$BDIR/%d-$(basename %q)"; fi`, fpath, fpath, fpath, i, fpath))
		}
		_, _ = remote.RunHostShell(t, strings.Join(mvLines, "\n"), 20*time.Second)
	}

	out.OK = true
	return out
}

func toBoolSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

func containsString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

func nonEmptySlice(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
