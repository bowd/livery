package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"

	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/secrets"
)

func LauncherRoot() string {
	for _, k := range []string{"LIVERY_ROOT", "AGENT_LAUNCH_ROOT"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return secrets.ExpandPath(v)
		}
	}
	home, _ := os.UserHomeDir()
	liv := filepath.Join(home, ".local", "share", "livery")
	leg := filepath.Join(home, ".local", "share", "agent-config", "launchers")
	if _, err := os.Stat(filepath.Join(liv, "profiles")); err == nil {
		return liv
	}
	if _, err := os.Stat(filepath.Join(leg, "profiles")); err == nil {
		return leg
	}
	return liv
}

func LocalConfigDirs() []string {
	var dirs []string
	for _, k := range []string{"LIVERY_CONFIG_DIR", "AGENT_LAUNCH_CONFIG_DIR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			dirs = append(dirs, secrets.ExpandPath(v))
		}
	}
	if len(dirs) > 0 {
		return dirs
	}
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	} else {
		base = secrets.ExpandPath(base)
	}
	return []string{
		filepath.Join(base, "agent-config", "launchers"),
		filepath.Join(base, "livery", "launchers"),
	}
}

func HomesRoot() string {
	for _, k := range []string{"LIVERY_HOMES_ROOT", "AGENT_LAUNCH_HOMES_ROOT"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return secrets.ExpandPath(v)
		}
	}
	home, _ := os.UserHomeDir()
	// Reuse existing agent-config/homes if already populated so OAuth sessions persist across migration!
	leg := filepath.Join(home, ".local", "share", "agent-config", "homes")
	if _, err := os.Stat(leg); err == nil {
		return leg
	}
	return filepath.Join(home, ".local", "share", "livery", "homes")
}

func BinDir() string {
	for _, k := range []string{"LIVERY_BIN_DIR", "AGENT_LAUNCH_BIN_DIR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return secrets.ExpandPath(v)
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin")
}

func loadSpecFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if strings.HasSuffix(path, ".json") {
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else {
		if _, err := toml.Decode(string(b), &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

// LoadAllProfiles loads synced managed profiles + host-local overrides (and optionally repo launchers as fallback).
func LoadAllProfiles(repoRoot string) map[string]map[string]any {
	out := map[string]map[string]any{}
	home, _ := os.UserHomeDir()
	// 1. Optional repo launchers (lowest priority, handy when running `liv` straight inside repo before first sync)
	if repoRoot != "" {
		loadDirInto(filepath.Join(repoRoot, "launchers"), "repo", out)
	}
	// 2. Synced legacy + livery managed profile dirs
	candidates := []string{
		filepath.Join(home, ".local", "share", "agent-config", "launchers", "profiles"),
		filepath.Join(home, ".local", "share", "livery", "profiles"),
		filepath.Join(LauncherRoot(), "profiles"),
	}
	seenDir := map[string]bool{}
	for _, d := range candidates {
		abs, _ := filepath.Abs(d)
		if seenDir[abs] {
			continue
		}
		seenDir[abs] = true
		loadDirInto(abs, "managed", out)
	}
	// 3. Host-local profiles (highest priority)
	for _, d := range LocalConfigDirs() {
		loadDirInto(d, "local", out)
	}
	return out
}

func loadDirInto(dir, source string, dst map[string]map[string]any) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".toml") {
			continue
		}
		p := filepath.Join(dir, name)
		spec, err := loadSpecFile(p)
		if err != nil {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(name, ".json"), ".toml")
		id := strVal(spec, "id", stem)
		harness := strVal(spec, "harness", "")
		if _, ok := Drivers[harness]; !ok {
			continue
		}
		spec["id"] = id
		spec["profile"] = model.DeriveProfileName(id, harness, strVal(spec, "profile", ""))
		spec["_source"] = source
		spec["_path"] = p
		dst[id] = spec
	}
}

// ResolveHarnessProfile resolves `liv <harness> <profile>` (matching short profile name OR full id).
func ResolveHarnessProfile(profiles map[string]map[string]any, harness, profile string) (map[string]any, error) {
	// Direct match on (harness, profile)
	for _, spec := range profiles {
		if strVal(spec, "harness", "") == harness && strVal(spec, "profile", "") == profile {
			return spec, nil
		}
	}
	// Also accept full ID (e.g. `liv claude claude-gateway`)
	if spec, ok := profiles[profile]; ok && strVal(spec, "harness", "") == harness {
		return spec, nil
	}
	if spec, ok := profiles[harness+"-"+profile]; ok && strVal(spec, "harness", "") == harness {
		return spec, nil
	}
	var avail []string
	for _, spec := range profiles {
		if strVal(spec, "harness", "") == harness {
			avail = append(avail, strVal(spec, "profile", strVal(spec, "id", "")))
		}
	}
	sort.Strings(avail)
	if len(avail) == 0 {
		return nil, fmt.Errorf("no %s profiles configured", harness)
	}
	return nil, fmt.Errorf("unknown %s profile %q (available: %s)", harness, profile, strings.Join(avail, ", "))
}

// ResolveByID resolves either a full profile ID ("claude-gateway") or "harness/profile" ("claude/gateway").
func ResolveByID(profiles map[string]map[string]any, query string) (map[string]any, error) {
	if h, p, ok := strings.Cut(query, "/"); ok {
		return ResolveHarnessProfile(profiles, h, p)
	}
	if spec, ok := profiles[query]; ok {
		return spec, nil
	}
	// Unique short profile name match
	var matches []map[string]any
	for _, spec := range profiles {
		if strVal(spec, "profile", "") == query {
			matches = append(matches, spec)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		var ids []string
		for _, m := range matches {
			ids = append(ids, fmt.Sprintf("%s %s", strVal(m, "harness", ""), strVal(m, "profile", "")))
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("profile %q is ambiguous (%s); specify harness too", query, strings.Join(ids, ", "))
	}
	return nil, fmt.Errorf("unknown profile %q", query)
}

type ProfileListRow struct {
	ID             string   `json:"id"`
	Harness        string   `json:"harness"`
	Profile        string   `json:"profile"`
	AuthMode       string   `json:"auth_mode"`
	Mode           string   `json:"mode"`
	SessionGroup   string   `json:"session_group,omitempty"`
	Shim           string   `json:"shim"`
	Command        []string `json:"command"`
	Source         string   `json:"source"`
	SecretsOK      bool     `json:"secrets_ok"`
	MissingSecrets []string `json:"missing_secrets"`
	Description    string   `json:"description"`
}

func ListProfileRows(profiles map[string]map[string]any, store *secrets.Store, harnessFilter string) []ProfileListRow {
	ids := make([]string, 0, len(profiles))
	for id := range profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	callerEnv := EnvMap()
	var rows []ProfileListRow
	for _, id := range ids {
		spec := profiles[id]
		h := strVal(spec, "harness", "")
		if harnessFilter != "" && h != harnessFilter {
			continue
		}
		drv := Drivers[h]
		req := map[string]string{}
		if drv != nil {
			req = drv.RequiredSecrets(spec)
		}
		var missing []string
		for _, ref := range req {
			if !store.Exists(ref, callerEnv) {
				missing = append(missing, ref)
			}
		}
		sort.Strings(missing)
		prof := strVal(spec, "profile", model.DeriveProfileName(id, h, ""))
		rows = append(rows, ProfileListRow{
			ID:             id,
			Harness:        h,
			Profile:        prof,
			AuthMode:       strVal(spec, "auth_mode", ""),
			Mode:           strVal(spec, "mode", "direct"),
			SessionGroup:   strVal(spec, "session_group", ""),
			Shim:           strVal(spec, "shim", ""),
			Command:        []string{"liv", h, prof},
			Source:         strVal(spec, "_source", ""),
			SecretsOK:      len(missing) == 0,
			MissingSecrets: missing,
			Description:    strVal(spec, "description", ""),
		})
	}
	return rows
}

func EnvMap() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

func AssembleEnv(plan *LaunchPlan, callerEnv map[string]string, store *secrets.Store) ([]string, error) {
	env := make(map[string]string, len(callerEnv)+len(plan.EnvSet)+len(plan.EnvSecretRefs))
	for k, v := range callerEnv {
		env[k] = v
	}
	for _, k := range plan.EnvUnset {
		delete(env, k)
	}
	for k, v := range plan.EnvSet {
		env[k] = v
	}
	for k, ref := range plan.EnvSecretRefs {
		val, err := store.Resolve(ref, callerEnv)
		if err != nil {
			return nil, fmt.Errorf("resolving secret %s (%s): %w", k, ref, err)
		}
		env[k] = val
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out, nil
}

// ExecProfile replaces the current process with the configured harness (clean TTY & signal handling!).
func ExecProfile(spec map[string]any, userArgs []string, store *secrets.Store) error {
	h := strVal(spec, "harness", "")
	drv, ok := Drivers[h]
	if !ok {
		return fmt.Errorf("unsupported harness %q", h)
	}
	callerEnv := EnvMap()
	plan, err := drv.BuildPlan(spec, userArgs, callerEnv, false)
	if err != nil {
		return err
	}
	envSlice, err := AssembleEnv(plan, callerEnv, store)
	if err != nil {
		return err
	}
	return syscall.Exec(plan.Binary, plan.Argv, envSlice)
}

// RunLogin runs the harness interactive OAuth login flow for a profile.
func RunLogin(spec map[string]any, store *secrets.Store) error {
	h := strVal(spec, "harness", "")
	drv := Drivers[h]
	loginArgs := drv.LoginArgs(spec)
	if len(loginArgs) == 0 {
		req := drv.RequiredSecrets(spec)
		if len(req) > 0 {
			fmt.Printf("Profile %s %s uses secret-backed auth (no interactive OAuth login).\n", h, strVal(spec, "profile", ""))
			for k, ref := range req {
				st := "MISSING"
				if store.Exists(ref, nil) {
					st = "present"
				}
				fmt.Printf("  %s: %s (%s)\n", k, ref, st)
			}
			return nil
		}
		fmt.Printf("Profile %s %s has no dedicated login subcommand.\n", h, strVal(spec, "profile", ""))
		return nil
	}
	callerEnv := EnvMap()
	plan, err := drv.BuildPlan(spec, loginArgs, callerEnv, false)
	if err != nil {
		return err
	}
	envSlice, err := AssembleEnv(plan, callerEnv, store)
	if err != nil {
		return err
	}
	cmd := exec.Command(plan.Binary, plan.Argv[1:]...)
	cmd.Env = envSlice
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
