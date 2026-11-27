package apps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bowd/livery/internal/model"
)

// Safe atomic JSON writer with one-time .livery.bak backup.
func writeJSONAtomic(path string, doc any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if orig, err := os.ReadFile(path); err == nil {
		bak := path + ".livery.bak"
		if _, statErr := os.Stat(bak); os.IsNotExist(statErr) {
			_ = os.WriteFile(bak, orig, 0o600)
		}
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".livery-app-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
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

func readJSONMap(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

// ================================================================================================
// 1. HERDR (`~/.config/herdr-app/programs.json` + `~/.config/herdr-core/templates/*.md`)
// ================================================================================================

type HerdrProgram struct {
	Key   string   `json:"key"`
	Name  string   `json:"name"`
	Icon  string   `json:"icon"`
	Tint  *bool    `json:"tint,omitempty"`
	Color string   `json:"color,omitempty"`
	Run   []string `json:"run"`
}

type HerdrOverview struct {
	ProgramsFile  string         `json:"programs_file"`
	TemplatesDir  string         `json:"templates_dir"`
	Present       bool           `json:"present"`
	Programs      []HerdrProgram `json:"programs"`
	TemplateNames []string       `json:"template_names"`
}

func DefaultHerdrProgramsFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr-app", "programs.json")
}

func DefaultHerdrTemplatesDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr-core", "templates")
}

func LoadHerdrOverview(programsFile, templatesDir string) HerdrOverview {
	if programsFile == "" {
		programsFile = DefaultHerdrProgramsFile()
	}
	if templatesDir == "" {
		templatesDir = DefaultHerdrTemplatesDir()
	}
	ov := HerdrOverview{
		ProgramsFile:  programsFile,
		TemplatesDir:  templatesDir,
		Programs:      []HerdrProgram{},
		TemplateNames: []string{},
	}
	if doc, err := readJSONMap(programsFile); err == nil {
		ov.Present = true
		if progs, ok := doc["programs"].(map[string]any); ok {
			keys := make([]string, 0, len(progs))
			for k := range progs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				m, _ := progs[k].(map[string]any)
				if m == nil {
					continue
				}
				hp := HerdrProgram{
					Key:   k,
					Name:  strOf(m["name"]),
					Icon:  strOf(m["icon"]),
					Color: strOf(m["color"]),
					Run:   strSliceOf(m["run"]),
				}
				if b, ok := m["tint"].(bool); ok {
					hp.Tint = &b
				}
				ov.Programs = append(ov.Programs, hp)
			}
		}
	}
	if entries, err := os.ReadDir(templatesDir); err == nil {
		ov.Present = true
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				ov.TemplateNames = append(ov.TemplateNames, strings.TrimSuffix(e.Name(), ".md"))
			}
		}
		sort.Strings(ov.TemplateNames)
	}
	return ov
}

func UpsertHerdrProgram(programsFile string, p HerdrProgram) error {
	if programsFile == "" {
		programsFile = DefaultHerdrProgramsFile()
	}
	p.Key = strings.TrimSpace(p.Key)
	if p.Key == "" || len(p.Run) == 0 {
		return fmt.Errorf("herdr program requires key and non-empty run command")
	}
	doc, err := readJSONMap(programsFile)
	if err != nil {
		doc = map[string]any{}
	}
	progs, _ := doc["programs"].(map[string]any)
	if progs == nil {
		progs = map[string]any{}
		doc["programs"] = progs
	}
	entry := map[string]any{
		"name": p.Name,
		"icon": p.Icon,
		"run":  p.Run,
	}
	if p.Name == "" {
		entry["name"] = p.Key
	}
	if p.Icon == "" {
		entry["icon"] = "icons/agent.svg"
	}
	if p.Tint != nil {
		entry["tint"] = *p.Tint
	}
	if p.Color != "" {
		entry["color"] = p.Color
	}
	progs[p.Key] = entry
	return writeJSONAtomic(programsFile, doc)
}

func DeleteHerdrProgram(programsFile, key string) error {
	if programsFile == "" {
		programsFile = DefaultHerdrProgramsFile()
	}
	doc, err := readJSONMap(programsFile)
	if err != nil {
		return err
	}
	progs, _ := doc["programs"].(map[string]any)
	if progs != nil {
		delete(progs, key)
	}
	return writeJSONAtomic(programsFile, doc)
}

// SyncHerdrFromRepo syncs repo launcher specs into Herdr programs.json & templates using `liv <harness> <profile>`.
func SyncHerdrFromRepo(repo *model.Repo, programsFile, templatesDir string) (int, error) {
	if programsFile == "" {
		programsFile = DefaultHerdrProgramsFile()
	}
	if templatesDir == "" {
		templatesDir = DefaultHerdrTemplatesDir()
	}
	doc, err := readJSONMap(programsFile)
	if err != nil {
		doc = map[string]any{}
	}
	progs, _ := doc["programs"].(map[string]any)
	if progs == nil {
		progs = map[string]any{}
		doc["programs"] = progs
	}
	_ = os.MkdirAll(templatesDir, 0o755)
	count := 0
	var ids []string
	for id := range repo.Launchers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		sp := repo.Launchers[id]
		hcfg, _ := sp.Raw["herdr"].(map[string]any)
		if hcfg != nil {
			if en, ok := hcfg["enabled"].(bool); ok && !en {
				continue
			}
		}
		runCmd := []string{"liv", sp.Harness, sp.Profile}
		if sp.Harness == "codex" {
			noDaemon := true
			if hcfg != nil {
				if v, ok := hcfg["no_daemon"].(bool); ok {
					noDaemon = v
				}
			}
			if noDaemon {
				runCmd = append(runCmd, "--no-daemon")
			}
		}
		name := strOf(sp.Raw["description"])
		icon := defaultHerdrIcon(sp.Harness)
		if hcfg != nil {
			if v := strOf(hcfg["name"]); v != "" {
				name = v
			}
			if v := strOf(hcfg["icon"]); v != "" {
				icon = v
			}
		}
		if name == "" {
			name = id
		}
		entry := map[string]any{"name": name, "icon": icon, "run": runCmd}
		if hcfg != nil {
			for _, k := range []string{"tint", "color", "tile"} {
				if v, ok := hcfg[k]; ok {
					entry[k] = v
				}
			}
		}
		progs[id] = entry

		// Also write Herdr template ~/.config/herdr-core/templates/<id>.md
		tplContent := fmt.Sprintf("---\ncommand: %s\ndescription: %s\n---\n", strings.Join(runCmd, " "), name)
		_ = os.WriteFile(filepath.Join(templatesDir, id+".md"), []byte(tplContent), 0o644)
		count++
	}
	if err := writeJSONAtomic(programsFile, doc); err != nil {
		return count, err
	}
	return count, nil
}

func defaultHerdrIcon(h string) string {
	switch h {
	case "claude":
		return "icons/claude.svg"
	case "codex":
		return "icons/codex.svg"
	case "opencode":
		return "icons/opencode.svg"
	case "agy":
		return "icons/antigravity.svg"
	default:
		return "icons/agent.svg"
	}
}

// ================================================================================================
// 2. T3 CODE (`~/.t3/userdata/settings.json` `providerInstances`) — NEVER reads or leaks secrets!
// ================================================================================================

type T3EnvView struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"` // omitted when Sensitive == true
	Sensitive bool   `json:"sensitive"`
}

type T3ProviderView struct {
	InstanceID   string      `json:"instance_id"`
	Driver       string      `json:"driver"`
	DisplayName  string      `json:"display_name"`
	Enabled      bool        `json:"enabled"`
	AccentColor  string      `json:"accent_color,omitempty"`
	BinaryPath   string      `json:"binary_path,omitempty"`
	HomePath     string      `json:"home_path,omitempty"`
	LaunchArgs   string      `json:"launch_args,omitempty"`
	CustomModels []string    `json:"custom_models"`
	Environment  []T3EnvView `json:"environment"`
}

type T3Overview struct {
	SettingsFile string           `json:"settings_file"`
	Present      bool             `json:"present"`
	Providers    []T3ProviderView `json:"providers"`
}

type T3ProviderPatch struct {
	InstanceID   string   `json:"instance_id"`
	Driver       string   `json:"driver,omitempty"`
	DisplayName  *string  `json:"display_name,omitempty"`
	Enabled      *bool    `json:"enabled,omitempty"`
	AccentColor  *string  `json:"accent_color,omitempty"`
	BinaryPath   *string  `json:"binary_path,omitempty"`
	HomePath     *string  `json:"home_path,omitempty"`
	LaunchArgs   *string  `json:"launch_args,omitempty"`
	CustomModels []string `json:"custom_models,omitempty"`
}

func DefaultT3SettingsFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".t3", "userdata", "settings.json")
}

func isSecretEnvName(name string) bool {
	u := strings.ToUpper(name)
	for _, w := range []string{"KEY", "TOKEN", "SECRET", "PASS", "AUTH"} {
		if strings.Contains(u, w) {
			return true
		}
	}
	return false
}

func LoadT3Overview(settingsFile string) T3Overview {
	if settingsFile == "" {
		settingsFile = DefaultT3SettingsFile()
	}
	ov := T3Overview{SettingsFile: settingsFile, Providers: []T3ProviderView{}}
	doc, err := readJSONMap(settingsFile)
	if err != nil {
		return ov
	}
	ov.Present = true
	pis, _ := doc["providerInstances"].(map[string]any)
	keys := make([]string, 0, len(pis))
	for k := range pis {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, id := range keys {
		m, _ := pis[id].(map[string]any)
		if m == nil {
			continue
		}
		cfg, _ := m["config"].(map[string]any)
		en := true
		if b, ok := m["enabled"].(bool); ok {
			en = b
		}
		var envViews []T3EnvView
		if rawEnv, ok := m["environment"].([]any); ok {
			for _, re := range rawEnv {
				em, _ := re.(map[string]any)
				if em == nil {
					continue
				}
				name := strOf(em["name"])
				sens, _ := em["sensitive"].(bool)
				if isSecretEnvName(name) {
					sens = true
				}
				ev := T3EnvView{Name: name, Sensitive: sens}
				if !sens {
					ev.Value = strOf(em["value"])
				}
				envViews = append(envViews, ev)
			}
		}
		if envViews == nil {
			envViews = []T3EnvView{}
		}
		view := T3ProviderView{
			InstanceID:   id,
			Driver:       strOf(m["driver"]),
			DisplayName:  strOf(m["displayName"]),
			Enabled:      en,
			AccentColor:  strOf(m["accentColor"]),
			BinaryPath:   strOf(cfg["binaryPath"]),
			HomePath:     strOf(cfg["homePath"]),
			LaunchArgs:   strOf(cfg["launchArgs"]),
			CustomModels: strSliceOf(cfg["customModels"]),
			Environment:  envViews,
		}
		if view.DisplayName == "" {
			view.DisplayName = id
		}
		ov.Providers = append(ov.Providers, view)
	}
	return ov
}

// UpsertT3Provider edits or creates one providerInstances.<id> entry while preserving all sensitive env entries untouched!
func UpsertT3Provider(settingsFile string, patch T3ProviderPatch) error {
	if settingsFile == "" {
		settingsFile = DefaultT3SettingsFile()
	}
	patch.InstanceID = strings.TrimSpace(patch.InstanceID)
	if patch.InstanceID == "" {
		return fmt.Errorf("instance_id is required")
	}
	doc, err := readJSONMap(settingsFile)
	if err != nil {
		doc = map[string]any{}
	}
	pis, _ := doc["providerInstances"].(map[string]any)
	if pis == nil {
		pis = map[string]any{}
		doc["providerInstances"] = pis
	}
	entry, _ := pis[patch.InstanceID].(map[string]any)
	if entry == nil {
		entry = map[string]any{"enabled": true, "config": map[string]any{}}
		if patch.Driver != "" {
			entry["driver"] = patch.Driver
		} else {
			entry["driver"] = "claudeAgent"
		}
	} else if patch.Driver != "" {
		entry["driver"] = patch.Driver
	}
	cfg, _ := entry["config"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
		entry["config"] = cfg
	}
	if patch.DisplayName != nil {
		entry["displayName"] = *patch.DisplayName
	}
	if patch.Enabled != nil {
		entry["enabled"] = *patch.Enabled
	}
	if patch.AccentColor != nil {
		if *patch.AccentColor == "" {
			delete(entry, "accentColor")
		} else {
			entry["accentColor"] = *patch.AccentColor
		}
	}
	if patch.BinaryPath != nil {
		cfg["binaryPath"] = *patch.BinaryPath
	}
	if patch.HomePath != nil {
		cfg["homePath"] = *patch.HomePath
	}
	if patch.LaunchArgs != nil {
		cfg["launchArgs"] = *patch.LaunchArgs
	}
	if patch.CustomModels != nil {
		cfg["customModels"] = patch.CustomModels
	}
	pis[patch.InstanceID] = entry
	return writeJSONAtomic(settingsFile, doc)
}

func DeleteT3Provider(settingsFile, instanceID string) error {
	if settingsFile == "" {
		settingsFile = DefaultT3SettingsFile()
	}
	doc, err := readJSONMap(settingsFile)
	if err != nil {
		return err
	}
	if pis, ok := doc["providerInstances"].(map[string]any); ok {
		delete(pis, instanceID)
	}
	return writeJSONAtomic(settingsFile, doc)
}

// ================================================================================================
// 3. OPENCLAW (`~/.openclaw/openclaw.json` + `~/.openclaw-<profile>/openclaw.json`)
// ================================================================================================

type OpenClawMCPView struct {
	Name    string   `json:"name"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args"`
	URL     string   `json:"url,omitempty"`
}

type OpenClawPluginView struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Allowed bool   `json:"allowed"`
}

type OpenClawProfileView struct {
	Name       string               `json:"name"` // "default" or named profile
	Dir        string               `json:"dir"`
	ConfigPath string               `json:"config_path"`
	MCPServers []OpenClawMCPView    `json:"mcp_servers"`
	Plugins    []OpenClawPluginView `json:"plugins"`
	Skills     []string             `json:"skills"`
}

type OpenClawOverview struct {
	Present  bool                  `json:"present"`
	Profiles []OpenClawProfileView `json:"profiles"`
}

func openClawProfileDir(home, name string) string {
	if name == "" || name == "default" {
		return filepath.Join(home, ".openclaw")
	}
	return filepath.Join(home, ".openclaw-"+name)
}

func LoadOpenClawOverview(home string) OpenClawOverview {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	ov := OpenClawOverview{Profiles: []OpenClawProfileView{}}
	entries, err := os.ReadDir(home)
	if err != nil {
		return ov
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == ".openclaw" {
			names = append(names, "default")
		} else if strings.HasPrefix(e.Name(), ".openclaw-") {
			names = append(names, strings.TrimPrefix(e.Name(), ".openclaw-"))
		}
	}
	sort.Strings(names)
	for _, name := range names {
		dir := openClawProfileDir(home, name)
		cfgPath := filepath.Join(dir, "openclaw.json")
		doc, err := readJSONMap(cfgPath)
		if err != nil {
			doc = map[string]any{}
		}
		ov.Present = true
		view := OpenClawProfileView{
			Name:       name,
			Dir:        dir,
			ConfigPath: cfgPath,
			MCPServers: []OpenClawMCPView{},
			Plugins:    []OpenClawPluginView{},
			Skills:     []string{},
		}
		if mcp, ok := doc["mcp"].(map[string]any); ok {
			if srvs, ok := mcp["servers"].(map[string]any); ok {
				skeys := make([]string, 0, len(srvs))
				for k := range srvs {
					skeys = append(skeys, k)
				}
				sort.Strings(skeys)
				for _, k := range skeys {
					sm, _ := srvs[k].(map[string]any)
					if sm == nil {
						continue
					}
					view.MCPServers = append(view.MCPServers, OpenClawMCPView{
						Name:    k,
						Command: strOf(sm["command"]),
						Args:    strSliceOf(sm["args"]),
						URL:     strOf(sm["url"]),
					})
				}
			}
		}
		if plugs, ok := doc["plugins"].(map[string]any); ok {
			allowSet := map[string]bool{}
			for _, a := range strSliceOf(plugs["allow"]) {
				allowSet[a] = true
			}
			entriesMap, _ := plugs["entries"].(map[string]any)
			pSet := map[string]bool{}
			for k := range allowSet {
				pSet[k] = true
			}
			for k := range entriesMap {
				pSet[k] = true
			}
			pkeys := make([]string, 0, len(pSet))
			for k := range pSet {
				pkeys = append(pkeys, k)
			}
			sort.Strings(pkeys)
			for _, k := range pkeys {
				em, _ := entriesMap[k].(map[string]any)
				en := false
				if em != nil {
					if b, ok := em["enabled"].(bool); ok {
						en = b
					}
				}
				view.Plugins = append(view.Plugins, OpenClawPluginView{
					Name:    k,
					Enabled: en,
					Allowed: allowSet[k],
				})
			}
		}
		if skEntries, err := os.ReadDir(filepath.Join(dir, "skills")); err == nil {
			for _, se := range skEntries {
				if !strings.HasPrefix(se.Name(), ".") {
					view.Skills = append(view.Skills, se.Name())
				}
			}
			sort.Strings(view.Skills)
		}
		ov.Profiles = append(ov.Profiles, view)
	}
	return ov
}

func CreateOpenClawProfile(home, name string) (string, error) {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/\\ ") {
		return "", fmt.Errorf("invalid openclaw profile name %q", name)
	}
	dir := openClawProfileDir(home, name)
	cfgPath := filepath.Join(dir, "openclaw.json")
	if _, err := os.Stat(cfgPath); err == nil {
		return cfgPath, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		return "", err
	}
	return cfgPath, writeJSONAtomic(cfgPath, map[string]any{
		"mcp":     map[string]any{"servers": map[string]any{}},
		"plugins": map[string]any{"allow": []string{}, "entries": map[string]any{}},
	})
}

func SetOpenClawPlugin(home, profileName, pluginName string, enabled bool) error {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	cfgPath := filepath.Join(openClawProfileDir(home, profileName), "openclaw.json")
	doc, err := readJSONMap(cfgPath)
	if err != nil {
		doc = map[string]any{}
	}
	plugs, _ := doc["plugins"].(map[string]any)
	if plugs == nil {
		plugs = map[string]any{}
		doc["plugins"] = plugs
	}
	allow := strSliceOf(plugs["allow"])
	if enabled {
		found := false
		for _, a := range allow {
			if a == pluginName {
				found = true
			}
		}
		if !found {
			allow = append(allow, pluginName)
		}
	} else {
		var next []string
		for _, a := range allow {
			if a != pluginName {
				next = append(next, a)
			}
		}
		allow = next
	}
	plugs["allow"] = allow
	entriesMap, _ := plugs["entries"].(map[string]any)
	if entriesMap == nil {
		entriesMap = map[string]any{}
		plugs["entries"] = entriesMap
	}
	ent, _ := entriesMap[pluginName].(map[string]any)
	if ent == nil {
		ent = map[string]any{}
	}
	ent["enabled"] = enabled
	entriesMap[pluginName] = ent
	return writeJSONAtomic(cfgPath, doc)
}

func SetOpenClawMCP(home, profileName string, srv OpenClawMCPView) error {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	srv.Name = strings.TrimSpace(srv.Name)
	if srv.Name == "" {
		return fmt.Errorf("mcp server name required")
	}
	cfgPath := filepath.Join(openClawProfileDir(home, profileName), "openclaw.json")
	doc, err := readJSONMap(cfgPath)
	if err != nil {
		doc = map[string]any{}
	}
	mcp, _ := doc["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
		doc["mcp"] = mcp
	}
	srvs, _ := mcp["servers"].(map[string]any)
	if srvs == nil {
		srvs = map[string]any{}
		mcp["servers"] = srvs
	}
	rec := map[string]any{}
	if srv.URL != "" {
		rec["url"] = srv.URL
	} else {
		rec["command"] = srv.Command
		rec["args"] = srv.Args
	}
	srvs[srv.Name] = rec
	return writeJSONAtomic(cfgPath, doc)
}

func DeleteOpenClawMCP(home, profileName, serverName string) error {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	cfgPath := filepath.Join(openClawProfileDir(home, profileName), "openclaw.json")
	doc, err := readJSONMap(cfgPath)
	if err != nil {
		return err
	}
	if mcp, ok := doc["mcp"].(map[string]any); ok {
		if srvs, ok := mcp["servers"].(map[string]any); ok {
			delete(srvs, serverName)
		}
	}
	return writeJSONAtomic(cfgPath, doc)
}

func strOf(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return fmt.Sprint(v)
}

func strSliceOf(v any) []string {
	if v == nil {
		return []string{}
	}
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, el := range x {
			out = append(out, strOf(el))
		}
		return out
	default:
		return []string{}
	}
}
