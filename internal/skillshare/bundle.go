package skillshare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bowd/livery/internal/model"
)

var (
	MCPTargets = map[string]bool{
		"claude": true, "codex": true, "cursor": true, "opencode": true, "vscode": true, "antigravity": true,
		"pi": true, "omp": true, "amp": true, "claude-desktop": true, "cline": true, "copilot": true,
		"factory": true, "gemini": true, "goose": true, "hermes": true, "junie": true, "kiro": true,
		"lmstudio": true, "warp": true, "windsurf": true, "kilocode": true, "grok": true,
	}
	MCPNoFromEnv = map[string]bool{
		"antigravity": true, "claude-desktop": true, "junie": true, "lmstudio": true, "warp": true,
	}
	MCPAliases = map[string]string{
		"antigravity-cli": "antigravity",
		"agy":             "antigravity",
		"claude-code":     "claude",
		"gemini-cli":      "gemini",
	}
	HookTargets = map[string]bool{
		"claude": true, "codex": true, "gemini": true, "copilot": true,
		"cursor": true, "droid": true, "antigravity": true, "qwen": true,
	}
	HookAliases = map[string]string{
		"antigravity-cli": "antigravity",
		"agy":             "antigravity",
		"claude-code":     "claude",
		"gemini-cli":      "gemini",
	}
	KnownTargets = []string{
		"claude", "codex", "opencode", "antigravity-cli", "antigravity",
		"cursor", "pi", "gemini", "windsurf", "vscode", "cline", "copilot", "amp", "goose", "kiro",
	}
)

var (
	yamlSafeBare = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	yamlKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	yamlReserved = map[string]bool{
		"true": true, "false": true, "null": true, "yes": true, "no": true, "on": true, "off": true, "y": true, "n": true,
		"True": true, "False": true, "Null": true, "Yes": true, "No": true, "On": true, "Off": true,
		"TRUE": true, "FALSE": true, "NULL": true, "YES": true, "NO": true, "ON": true, "OFF": true,
	}
)

func yamlScalar(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
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
		if x != "" && yamlSafeBare.MatchString(x) && !yamlReserved[x] &&
			(x[0] < '0' || x[0] > '9') &&
			!strings.HasPrefix(x, "~") && !strings.HasPrefix(x, "-") && !strings.HasPrefix(x, ".") && !strings.HasPrefix(x, "/") {
			return x
		}
		b, _ := json.Marshal(x)
		return string(b)
	default:
		b, _ := json.Marshal(fmt.Sprint(v))
		return string(b)
	}
}

func yamlKey(k string) string {
	if k != "" && yamlKeyRe.MatchString(k) && !yamlReserved[k] {
		return k
	}
	b, _ := json.Marshal(k)
	return string(b)
}

// DumpsYAML formats nested maps/slices into clean block YAML 1.2 without external dependencies.
func DumpsYAML(obj any, indent int) string {
	pad := strings.Repeat(" ", indent)
	switch x := obj.(type) {
	case map[string]any:
		if len(x) == 0 {
			return "{}"
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var lines []string
		for _, k := range keys {
			v := x[k]
			ks := yamlKey(k)
			switch sub := v.(type) {
			case map[string]any:
				if len(sub) == 0 {
					lines = append(lines, fmt.Sprintf("%s%s: {}", pad, ks))
				} else {
					lines = append(lines, fmt.Sprintf("%s%s:", pad, ks), DumpsYAML(sub, indent+2))
				}
			case []any:
				if len(sub) == 0 {
					lines = append(lines, fmt.Sprintf("%s%s: []", pad, ks))
				} else {
					lines = append(lines, fmt.Sprintf("%s%s:", pad, ks), DumpsYAML(sub, indent+2))
				}
			case []string:
				if len(sub) == 0 {
					lines = append(lines, fmt.Sprintf("%s%s: []", pad, ks))
				} else {
					lines = append(lines, fmt.Sprintf("%s%s:", pad, ks), DumpsYAML(sub, indent+2))
				}
			default:
				lines = append(lines, fmt.Sprintf("%s%s: %s", pad, ks, yamlScalar(v)))
			}
		}
		return strings.Join(lines, "\n")
	case []string:
		if len(x) == 0 {
			return "[]"
		}
		lines := make([]string, len(x))
		for i, s := range x {
			lines[i] = fmt.Sprintf("%s- %s", pad, yamlScalar(s))
		}
		return strings.Join(lines, "\n")
	case []any:
		if len(x) == 0 {
			return "[]"
		}
		var lines []string
		for _, item := range x {
			if m, ok := item.(map[string]any); ok && len(m) > 0 {
				keys := make([]string, 0, len(m))
				for k := range m {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for idx, k := range keys {
					prefix := pad + "  "
					if idx == 0 {
						prefix = pad + "- "
					}
					v := m[k]
					ks := yamlKey(k)
					switch sub := v.(type) {
					case map[string]any:
						if len(sub) == 0 {
							lines = append(lines, fmt.Sprintf("%s%s: {}", prefix, ks))
						} else {
							lines = append(lines, fmt.Sprintf("%s%s:", prefix, ks), DumpsYAML(sub, indent+4))
						}
					case []any:
						if len(sub) == 0 {
							lines = append(lines, fmt.Sprintf("%s%s: []", prefix, ks))
						} else {
							lines = append(lines, fmt.Sprintf("%s%s:", prefix, ks), DumpsYAML(sub, indent+4))
						}
					case []string:
						if len(sub) == 0 {
							lines = append(lines, fmt.Sprintf("%s%s: []", prefix, ks))
						} else {
							lines = append(lines, fmt.Sprintf("%s%s:", prefix, ks), DumpsYAML(sub, indent+4))
						}
					default:
						lines = append(lines, fmt.Sprintf("%s%s: %s", prefix, ks, yamlScalar(v)))
					}
				}
			} else {
				lines = append(lines, fmt.Sprintf("%s- %s", pad, yamlScalar(item)))
			}
		}
		return strings.Join(lines, "\n")
	default:
		return pad + yamlScalar(obj)
	}
}

func canonicalSkillProvider(targetName string) string {
	switch targetName {
	case "claude-code":
		return "claude"
	case "agy", "antigravity-cli":
		return "antigravity"
	default:
		return targetName
	}
}

func SkillAppliesToTarget(skillProvs []string, targetName string) bool {
	hasClaude, hasCodex := false, false
	for _, p := range skillProvs {
		if p == "claude" {
			hasClaude = true
		}
		if p == "codex" {
			hasCodex = true
		}
	}
	if hasClaude && hasCodex {
		return true
	}
	canon := canonicalSkillProvider(targetName)
	for _, p := range skillProvs {
		if p == targetName || p == canon {
			return true
		}
	}
	return false
}

func substHome(v any, home string) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, "{home}", home)
	case []any:
		out := make([]any, len(x))
		for i, el := range x {
			out[i] = substHome(el, home)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, el := range x {
			out[k] = substHome(el, home)
		}
		return out
	default:
		return v
	}
}

func BuildConfigYAML(
	repo *model.Repo,
	t *model.Target,
	skillMap map[string][]string,
	allSkills []string,
	mcpNames []string,
	hookIDs []string,
	home string,
) []byte {
	sroot := strings.TrimRight(t.SkillshareRoot, "/")
	ssTargets := t.EffectiveSkillshareTargets()
	allSorted := append([]string(nil), allSkills...)
	sort.Strings(allSorted)

	targetsDoc := map[string]any{}
	for _, st := range ssTargets {
		sfor := append([]string(nil), skillMap[st]...)
		sort.Strings(sfor)
		scfg := map[string]any{}
		explicitPath := t.SkillshareTargetPaths[st]
		if explicitPath == "" && st == "codex" && t.ProviderSkillDirs["codex"] != "" {
			explicitPath = t.ProviderSkillDirs["codex"]
		}
		if explicitPath != "" {
			scfg["path"] = explicitPath
		}
		mode := t.SkillshareTargetModes[st]
		if mode == "" && t.LinkMode[st] == "copy" {
			mode = "copy"
		}
		if mode != "" && mode != t.SkillshareMode {
			scfg["mode"] = mode
		}
		if len(sfor) == 0 {
			scfg["enabled"] = false
		} else if !sameStringSlice(sfor, allSorted) {
			scfg["include"] = sfor
		}
		if len(scfg) > 0 {
			targetsDoc[st] = map[string]any{"skills": scfg}
		} else {
			targetsDoc[st] = map[string]any{}
		}
	}

	doc := map[string]any{
		"mode":    t.SkillshareMode,
		"sources": map[string]any{"skills": sroot + "/skills"},
		"targets": targetsDoc,
	}

	if mcpSec := buildMCPSection(repo, t, mcpNames, ssTargets, home); mcpSec != nil {
		doc["mcp"] = mcpSec
	}
	if hookSec := buildHooksSection(repo, t, hookIDs, ssTargets, home); hookSec != nil {
		doc["hooks"] = hookSec
	}

	header := "# Generated by livery (skillshare bundle).\n" +
		"# Edit inventory.toml / profiles / skills / mcp / hooks in your livery repo.\n"
	return []byte(header + DumpsYAML(doc, 0) + "\n")
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func buildMCPSection(repo *model.Repo, t *model.Target, mcpNames, ssTargets []string, home string) map[string]any {
	if !t.SkillshareManageMCP || len(mcpNames) == 0 {
		return nil
	}
	var mcpTargets []string
	seenT := map[string]bool{}
	for _, st := range ssTargets {
		canon := st
		if a, ok := MCPAliases[st]; ok {
			canon = a
		}
		if MCPTargets[canon] && !seenT[canon] {
			seenT[canon] = true
			mcpTargets = append(mcpTargets, canon)
		}
	}
	if len(mcpTargets) == 0 {
		return nil
	}
	servers := map[string]any{}
	for _, name := range mcpNames {
		m, ok := repo.MCP[name]
		if !ok {
			continue
		}
		var srvTargets []string
		hasBoth := sliceHas(m.Providers, "claude") && sliceHas(m.Providers, "codex")
		for _, mt := range mcpTargets {
			if hasBoth || sliceHas(m.Providers, mt) {
				srvTargets = append(srvTargets, mt)
			}
		}
		if len(srvTargets) == 0 {
			continue
		}
		base, _ := substHome(m.Spec, home).(map[string]any)
		srv := map[string]any{}
		usesFromEnv := false
		tr := "stdio"
		if s, ok := base["transport"].(string); ok && s != "" {
			tr = s
		}
		if tr == "http" {
			srv["transport"] = "streamable-http"
			srv["url"] = fmt.Sprint(base["url"])
			if btv, ok := base["bearer_token_env_var"].(string); ok && btv != "" {
				srv["bearerToken"] = map[string]any{"fromEnv": btv}
				usesFromEnv = true
			}
			if hdrs, ok := base["env_http_headers"].(map[string]any); ok && len(hdrs) > 0 {
				outH := map[string]any{}
				for k, v := range hdrs {
					outH[k] = map[string]any{"fromEnv": fmt.Sprint(v)}
				}
				srv["headers"] = outH
				usesFromEnv = true
			}
		} else {
			srv["command"] = fmt.Sprint(base["command"])
			if args, ok := base["args"].([]any); ok && len(args) > 0 {
				sa := make([]string, len(args))
				for i, a := range args {
					sa[i] = fmt.Sprint(a)
				}
				srv["args"] = sa
			}
			if evs, ok := base["env_vars"].([]any); ok && len(evs) > 0 {
				envMap := map[string]any{}
				for _, ev := range evs {
					v := fmt.Sprint(ev)
					envMap[v] = map[string]any{"fromEnv": v}
				}
				srv["env"] = envMap
				usesFromEnv = true
			}
		}
		if usesFromEnv {
			var filtered []string
			for _, mt := range srvTargets {
				if !MCPNoFromEnv[mt] || sliceHas(m.Providers, mt) {
					filtered = append(filtered, mt)
				}
			}
			srvTargets = filtered
			if len(srvTargets) == 0 {
				continue
			}
		}
		if !sameStringSlice(srvTargets, mcpTargets) {
			srv["targets"] = srvTargets
		}
		servers[name] = srv
	}
	if len(servers) == 0 {
		return nil
	}
	return map[string]any{"targets": mcpTargets, "servers": servers}
}

func buildHooksSection(repo *model.Repo, t *model.Target, hookIDs, ssTargets []string, home string) map[string]any {
	if !t.SkillshareManageHooks || len(hookIDs) == 0 {
		return nil
	}
	sroot := strings.TrimRight(t.SkillshareRoot, "/")
	srootAbs := sroot
	if strings.HasPrefix(sroot, "~/") {
		srootAbs = home + sroot[1:]
	}
	var hookTargets []string
	seen := map[string]bool{}
	for _, st := range ssTargets {
		canon := st
		if a, ok := HookAliases[st]; ok {
			canon = a
		}
		if HookTargets[canon] && !seen[canon] {
			seen[canon] = true
			hookTargets = append(hookTargets, canon)
		}
	}
	if len(hookTargets) == 0 {
		return nil
	}
	entries := map[string]any{}
	for _, hid := range hookIDs {
		h, ok := repo.Hooks[hid]
		if !ok {
			continue
		}
		bindings := map[string]any{}
		for _, prov := range hookTargets {
			if !sliceHas(h.Providers, prov) {
				continue
			}
			sp := h.ForProvider(prov)
			sc, _ := sp["script"].(string)
			scriptAbs := ""
			if sc != "" {
				scriptAbs = srootAbs + "/hooks/scripts/" + sc
			}
			cmd, _ := sp["command"].(string)
			if cmd == "" && sc != "" {
				cmd = "{script}"
			}
			cmd = strings.ReplaceAll(cmd, "{script}", scriptAbs)
			cmd = strings.ReplaceAll(cmd, "{home}", home)
			handler := map[string]any{"type": "command", "command": cmd}
			if to, ok := sp["timeout"]; ok && to != nil {
				handler["timeout"] = to
			}
			grp := map[string]any{"hooks": []any{handler}}
			if mt, ok := sp["matcher"].(string); ok && mt != "" {
				grp["matcher"] = mt
			}
			evName := fmt.Sprint(sp["event"])
			bindings[prov] = map[string]any{"events": map[string]any{evName: []any{grp}}}
		}
		if len(bindings) > 0 {
			ed := map[string]any{"bindings": bindings}
			if h.Why != "" {
				ed["description"] = h.Why
			}
			entries[hid] = ed
		}
	}
	if len(entries) == 0 {
		return nil
	}
	return map[string]any{"entries": entries}
}

func sliceHas(list []string, val string) bool {
	for _, s := range list {
		if s == val {
			return true
		}
	}
	return false
}

// --- Local stock skillshare discovery & controller-side audit -----------------------------------

func FindLocalBinary(explicit string) string {
	if explicit != "" {
		p := model.ExpandHome(explicit)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode().Perm()&0o111 != 0 {
			return p
		}
		return ""
	}
	if p, err := exec.LookPath("skillshare"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{
		"/opt/homebrew/bin/skillshare",
		"/usr/local/bin/skillshare",
		filepath.Join(home, ".local", "bin", "skillshare"),
		filepath.Join(home, ".nix-profile", "bin", "skillshare"),
		"/run/current-system/sw/bin/skillshare",
	} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Mode().Perm()&0o111 != 0 {
			return c
		}
	}
	return ""
}

func LocalVersion(explicit string) (binPath, ver string) {
	binPath = FindLocalBinary(explicit)
	if binPath == "" {
		return "", ""
	}
	out, err := exec.Command(binPath, "--version").CombinedOutput()
	if err != nil {
		return binPath, ""
	}
	line := strings.TrimSpace(strings.Split(strings.TrimSpace(string(out)), "\n")[0])
	return binPath, line
}

type AuditReport struct {
	OK        bool             `json:"ok"`
	Installed bool             `json:"installed"`
	RC        int              `json:"rc"`
	Scope     string           `json:"scope"`
	Error     string           `json:"error,omitempty"`
	Summary   map[string]any   `json:"summary,omitempty"`
	Results   []map[string]any `json:"results,omitempty"`
}

func RunAudit(repo *model.Repo, targetID, threshold, profile, explicitBin string) AuditReport {
	bin := FindLocalBinary(explicitBin)
	if bin == "" {
		return AuditReport{
			OK: false, Installed: false, RC: 2,
			Error: "stock `skillshare` CLI not found on controller (install via `brew install skillshare`)",
		}
	}
	skillsToScan := map[string]string{}
	scopeLabel := "catalog skillshare/skills"
	cat := LoadCatalog(repo.Root)
	byName := map[string]string{}
	for _, rs := range cat.Skills {
		byName[rs.Name] = rs.Dir
	}
	if targetID != "" {
		t, err := repo.Target(targetID)
		if err != nil {
			return AuditReport{OK: false, Installed: true, RC: 2, Error: err.Error()}
		}
		enabledSkills, _ := repo.ResolveHostCategoryItems(t, "skills")
		for _, name := range enabledSkills {
			if dir, ok := byName[name]; ok {
				skillsToScan[name] = dir
			}
		}
		scopeLabel = "environment " + t.ID
	} else {
		for name, dir := range byName {
			skillsToScan[name] = dir
		}
	}

	if len(skillsToScan) == 0 {
		return AuditReport{
			OK: true, Installed: true, RC: 0, Scope: scopeLabel,
			Summary: map[string]any{"scanned": 0, "passed": 0, "warning": 0, "failed": 0, "riskLabel": "clean", "riskScore": 0},
			Results: []map[string]any{},
		}
	}

	td, err := os.MkdirTemp("", "livery-audit-*")
	if err != nil {
		return AuditReport{OK: false, Installed: true, RC: 1, Error: err.Error()}
	}
	defer os.RemoveAll(td)

	skillsDir := filepath.Join(td, "skills")
	_ = os.MkdirAll(skillsDir, 0o755)
	for name, src := range skillsToScan {
		if err := CopyTree(src, filepath.Join(skillsDir, name)); err != nil {
			return AuditReport{OK: false, Installed: true, RC: 1, Error: err.Error()}
		}
	}
	cfgPath := filepath.Join(td, "config.yaml")
	cfg := DumpsYAML(map[string]any{
		"mode":    "merge",
		"sources": map[string]any{"skills": skillsDir},
		"targets": map[string]any{},
	}, 0) + "\n"
	_ = os.WriteFile(cfgPath, []byte(cfg), 0o644)

	args := []string{"audit", "--format", "json", "--no-tui"}
	if threshold != "" {
		args = append(args, "--threshold", threshold)
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "SKILLSHARE_CONFIG="+cfgPath, "NO_COLOR=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	rc := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		} else {
			rc = 1
		}
	}
	raw := strings.TrimSpace(stdout.String())
	start, end := strings.IndexByte(raw, '{'), strings.LastIndexByte(raw, '}')
	if start == -1 || end <= start {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = raw
		}
		return AuditReport{OK: false, Installed: true, RC: rc, Scope: scopeLabel, Error: msg}
	}
	var parsed struct {
		Summary map[string]any   `json:"summary"`
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
		return AuditReport{OK: false, Installed: true, RC: 1, Scope: scopeLabel, Error: err.Error()}
	}
	return AuditReport{
		OK:        rc == 0,
		Installed: true,
		RC:        rc,
		Scope:     scopeLabel,
		Summary:   parsed.Summary,
		Results:   parsed.Results,
	}
}

// CopyTree copies a skill directory skipping .git, __pycache__, and top-level skill.toml.
func CopyTree(src, dst string) error {
	srcAbs, _ := filepath.Abs(src)
	return filepath.WalkDir(srcAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcAbs, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		name := d.Name()
		if name == ".git" || name == "__pycache__" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Dir(p) == srcAbs && name == "skill.toml" {
			return nil
		}
		target := filepath.Join(dst, rel)
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
			return os.Symlink(lt, target)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		return os.WriteFile(target, b, lst.Mode().Perm())
	})
}
