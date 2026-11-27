package remote

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/bowd/livery/internal/launcher"
	"github.com/bowd/livery/internal/render"
	"github.com/bowd/livery/internal/secrets"
	"github.com/bowd/livery/internal/skillshare"
	"gopkg.in/yaml.v3"
)

const AgentMark = "LIVERY-AGENT:"

type AgentRequest struct {
	Op           string              `json:"op"`
	Home         string              `json:"home,omitempty"`
	SecretBackend string             `json:"secret_backend,omitempty"`
	// state
	Paths        []string            `json:"paths,omitempty"`
	SkillDirs    map[string]string   `json:"skill_dirs,omitempty"`
	// apply
	Steps        []ApplyStep         `json:"steps,omitempty"`
	BackupRoot   string              `json:"backup_root,omitempty"`
	Stamp        string              `json:"stamp,omitempty"`
	// skillshare
	SkillshareBin string             `json:"skillshare_bin,omitempty"`
	ConfigPath   string              `json:"config_path,omitempty"`
	DryRun       bool                `json:"dry_run,omitempty"`
	// secrets
	NonceB64     string              `json:"nonce_b64,omitempty"`
	Refs         []SecretRefItem     `json:"refs,omitempty"`
	Secrets      map[string]string   `json:"secrets,omitempty"`
	// doctor
	BinDir       string              `json:"bin_dir,omitempty"`
	Profiles     []map[string]any    `json:"profiles,omitempty"`
	ProbeAuth    bool                `json:"probe_auth,omitempty"`
}

type SecretRefItem struct {
	Profile string `json:"profile,omitempty"`
	Key     string `json:"key,omitempty"`
	Ref     string `json:"ref"`
}

type ApplyStep struct {
	Op     string `json:"op"` // backup | mkdir | write | link | rm | backup_copy
	Path   string `json:"path"`
	Mode   string `json:"mode,omitempty"`
	B64    string `json:"b64,omitempty"`
	Target string `json:"target,omitempty"`
}

type LiveEntry struct {
	Type   string                        `json:"type"` // missing | file | dir | link | other
	SHA    string                        `json:"sha,omitempty"`
	Target string                        `json:"target,omitempty"`
	Files  map[string]render.TreeFileMeta `json:"files,omitempty"`
}

type AgentResponse struct {
	OK      bool           `json:"ok"`
	Error   string         `json:"error,omitempty"`
	Home    string         `json:"home,omitempty"`
	Result  map[string]any `json:"result,omitempty"`
}

// RunAgentStdio reads a single JSON AgentRequest from stdin and writes LIVERY-AGENT:<json> to stdout.
func RunAgentStdio() int {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		writeAgentResp(AgentResponse{OK: false, Error: err.Error()})
		return 0
	}
	var req AgentRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeAgentResp(AgentResponse{OK: false, Error: "invalid request JSON: " + err.Error()})
		return 0
	}
	resp := HandleAgentRequest(req)
	writeAgentResp(resp)
	return 0
}

func writeAgentResp(resp AgentResponse) {
	b, _ := json.Marshal(resp)
	fmt.Printf("\n%s%s\n", AgentMark, string(b))
}

func resolveAgentHome(override string) string {
	if override != "" {
		abs, _ := filepath.Abs(override)
		_ = os.Setenv("HOME", abs)
		_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(abs, ".config"))
		_ = os.Setenv("XDG_DATA_HOME", filepath.Join(abs, ".local", "share"))
		_ = os.Setenv("XDG_STATE_HOME", filepath.Join(abs, ".local", "state"))
		return abs
	}
	h, _ := os.UserHomeDir()
	return h
}

func expandWithHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// HandleAgentRequest executes one target-side operation (invoked directly locally or via `liv _remote` over SSH).
func HandleAgentRequest(req AgentRequest) AgentResponse {
	home := resolveAgentHome(req.Home)
	switch req.Op {
	case "state":
		return handleState(req, home)
	case "apply":
		return handleApply(req, home)
	case "skillshare_sync":
		return handleSkillshareSync(req, home)
	case "secrets_check":
		return handleSecretsCheck(req, home)
	case "secrets_put":
		return handleSecretsPut(req, home)
	case "doctor_probe":
		return handleDoctorProbe(req, home)
	default:
		return AgentResponse{OK: false, Home: home, Error: fmt.Sprintf("unknown agent op %q", req.Op)}
	}
}

func inspectPath(p string) LiveEntry {
	lst, err := os.Lstat(p)
	if err != nil {
		return LiveEntry{Type: "missing"}
	}
	if lst.Mode()&os.ModeSymlink != 0 {
		tgt, _ := os.Readlink(p)
		return LiveEntry{Type: "link", Target: tgt}
	}
	if lst.IsDir() {
		files, _ := render.WalkTree(p, nil)
		return LiveEntry{Type: "dir", SHA: render.TreeSHA(files), Files: files}
	}
	if lst.Mode().IsRegular() {
		b, err := os.ReadFile(p)
		if err != nil {
			return LiveEntry{Type: "other"}
		}
		sum := sha256.Sum256(b)
		return LiveEntry{Type: "file", SHA: hex.EncodeToString(sum[:])}
	}
	return LiveEntry{Type: "other"}
}

func handleState(req AgentRequest, home string) AgentResponse {
	paths := map[string]LiveEntry{}
	for _, p := range req.Paths {
		paths[p] = inspectPath(expandWithHome(p, home))
	}
	seenSkills := map[string][]string{}
	for prov, d := range req.SkillDirs {
		exp := expandWithHome(d, home)
		entries, err := os.ReadDir(exp)
		if err != nil {
			continue
		}
		var names []string
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		seenSkills[prov] = names
	}
	return AgentResponse{
		OK:   true,
		Home: home,
		Result: map[string]any{
			"paths":       paths,
			"seen_skills": seenSkills,
		},
	}
}

func handleApply(req AgentRequest, home string) AgentResponse {
	var done []map[string]any
	broot := expandWithHome(req.BackupRoot, home)
	if broot == "" {
		broot = filepath.Join(home, ".local", "share", "agent-config", "backup")
	}
	stamp := req.Stamp
	if stamp == "" {
		stamp = "backup"
	}

	for _, st := range req.Steps {
		abs := expandWithHome(st.Path, home)
		switch st.Op {
		case "backup":
			if _, err := os.Lstat(abs); err == nil {
				rel := strings.TrimPrefix(st.Path, "~/")
				if !strings.HasPrefix(st.Path, "~/") {
					rel = strings.TrimPrefix(abs, "/")
				}
				dst := filepath.Join(broot, stamp, filepath.FromSlash(rel))
				_ = os.MkdirAll(filepath.Dir(dst), 0o755)
				if err := os.Rename(abs, dst); err != nil {
					return AgentResponse{OK: false, Home: home, Error: fmt.Sprintf("backup %s: %v", st.Path, err), Result: map[string]any{"done": done}}
				}
				done = append(done, map[string]any{"op": "backup", "path": st.Path, "to": dst})
			}
		case "mkdir":
			if err := os.MkdirAll(abs, 0o755); err != nil {
				return AgentResponse{OK: false, Home: home, Error: err.Error(), Result: map[string]any{"done": done}}
			}
			done = append(done, map[string]any{"op": "mkdir", "path": st.Path})
		case "write":
			data, err := base64.StdEncoding.DecodeString(st.B64)
			if err != nil {
				return AgentResponse{OK: false, Home: home, Error: err.Error(), Result: map[string]any{"done": done}}
			}
			modeVal := os.FileMode(0o644)
			if st.Mode != "" {
				if parsed, err := strconv.ParseUint(st.Mode, 8, 32); err == nil {
					modeVal = os.FileMode(parsed)
				}
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return AgentResponse{OK: false, Home: home, Error: err.Error(), Result: map[string]any{"done": done}}
			}
			tmp, err := os.CreateTemp(filepath.Dir(abs), ".livery-w-*")
			if err != nil {
				return AgentResponse{OK: false, Home: home, Error: err.Error(), Result: map[string]any{"done": done}}
			}
			tmpName := tmp.Name()
			_, werr := tmp.Write(data)
			_ = tmp.Chmod(modeVal)
			cerr := tmp.Close()
			if werr != nil || cerr != nil {
				os.Remove(tmpName)
				return AgentResponse{OK: false, Home: home, Error: fmt.Sprintf("write %s failed", st.Path), Result: map[string]any{"done": done}}
			}
			if err := os.Rename(tmpName, abs); err != nil {
				os.Remove(tmpName)
				return AgentResponse{OK: false, Home: home, Error: err.Error(), Result: map[string]any{"done": done}}
			}
			done = append(done, map[string]any{"op": "write", "path": st.Path})
		case "link":
			tgt := expandWithHome(st.Target, home)
			_ = os.MkdirAll(filepath.Dir(abs), 0o755)
			if _, err := os.Lstat(abs); err == nil {
				_ = os.RemoveAll(abs)
			}
			if err := os.Symlink(tgt, abs); err != nil {
				return AgentResponse{OK: false, Home: home, Error: err.Error(), Result: map[string]any{"done": done}}
			}
			done = append(done, map[string]any{"op": "link", "path": st.Path, "target": tgt})
		case "rm":
			_ = os.RemoveAll(abs)
			done = append(done, map[string]any{"op": "rm", "path": st.Path})
		}
	}
	return AgentResponse{OK: true, Home: home, Result: map[string]any{"done": done}}
}

func parseJSONObject(s string) map[string]any {
	raw := strings.TrimSpace(s)
	start, end := strings.IndexByte(raw, '{'), strings.LastIndexByte(raw, '}')
	if start == -1 || end <= start {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw[start:end+1]), &m) != nil {
		return nil
	}
	return m
}

func configHasPlugins(cfgPath string) bool {
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return false
	}
	var doc struct {
		Plugins struct {
			Packages map[string]any `yaml:"packages"`
		} `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return false
	}
	return len(doc.Plugins.Packages) > 0
}

func handleSkillshareSync(req AgentRequest, home string) AgentResponse {
	bin := skillshare.FindLocalBinary(req.SkillshareBin)
	if bin == "" {
		return AgentResponse{
			OK: true, Home: home,
			Result: map[string]any{
				"ok": false, "installed": false,
				"error": "skillshare binary not found on target",
			},
		}
	}
	_, ver := skillshare.LocalVersion(bin)
	cfgPath := expandWithHome(req.ConfigPath, home)
	if _, err := os.Stat(cfgPath); err != nil {
		return AgentResponse{
			OK: true, Home: home,
			Result: map[string]any{
				"ok": true, "installed": true, "bin": bin, "version": ver, "config_missing": true,
			},
		}
	}
	// Always ensure sources.skills dir exists even for 0-skill targets
	_ = os.MkdirAll(filepath.Join(filepath.Dir(cfgPath), "skills"), 0o755)
	env := append(os.Environ(), "SKILLSHARE_CONFIG="+cfgPath, "NO_COLOR=1")
	hasPlugins := configHasPlugins(cfgPath)

	if req.DryRun {
		diffCmd := exec.Command(bin, "diff", "-g", "--json")
		diffCmd.Env = env
		diffOut, diffErr := diffCmd.Output()

		syncCmd := exec.Command(bin, "sync", "-g", "--all", "--dry-run", "--json")
		syncCmd.Env = env
		syncOut, syncErr := syncCmd.Output()

		res := map[string]any{
			"ok":        diffErr == nil && syncErr == nil,
			"installed": true,
			"bin":       bin,
			"version":   ver,
			"diff":      parseJSONObject(string(diffOut)),
			"data":      parseJSONObject(string(syncOut)),
		}
		if hasPlugins {
			plCmd := exec.Command(bin, "sync", "plugins", "-g", "--dry-run", "--json")
			plCmd.Env = env
			plOut, _ := plCmd.CombinedOutput()
			if parsed := parseJSONObject(string(plOut)); parsed != nil {
				res["plugins"] = parsed
			}
		}
		return AgentResponse{OK: true, Home: home, Result: res}
	}

	syncCmd := exec.Command(bin, "sync", "-g", "--all", "--json")
	syncCmd.Env = env
	syncOut, syncErr := syncCmd.CombinedOutput()
	if syncErr != nil {
		// If stock skillshare blocked because multiple pre-existing native hooks need adoption at once,
		// adopt each conflicting hook entry incrementally with `--replace` (+ Codex `--trust -y`) and retry!
		if healedOut, healedErr := autoAdoptConflictingHooks(bin, cfgPath, env, syncOut); healedOut != nil {
			syncOut = healedOut
			syncErr = healedErr
		}
	}
	res := map[string]any{
		"ok":        syncErr == nil,
		"installed": true,
		"bin":       bin,
		"version":   ver,
		"data":      parseJSONObject(string(syncOut)),
	}
	if syncErr != nil {
		res["stderr"] = strings.TrimSpace(string(syncOut))
		return AgentResponse{OK: true, Home: home, Result: res}
	}
	// Ensure any Skillshare-owned Codex hooks also have trust recorded cleanly
	_ = exec.Command(bin, "hooks", "sync", "-g", "--trust", "-y", "--json")
	trustCmd := exec.Command(bin, "hooks", "sync", "-g", "--trust", "-y", "--json")
	trustCmd.Env = env
	_ = trustCmd.Run()

	if hasPlugins {
		plCmd := exec.Command(bin, "sync", "plugins", "-g", "--json")
		plCmd.Env = env
		plOut, plErr := plCmd.CombinedOutput()
		if parsed := parseJSONObject(string(plOut)); parsed != nil {
			res["plugins"] = parsed
		}
		if plErr != nil {
			res["ok"] = false
			res["stderr"] = strings.TrimSpace(string(plOut))
		}
	}
	return AgentResponse{OK: true, Home: home, Result: res}
}

// autoAdoptConflictingHooks resolves stock skillshare's multi-hook conflict deadlock
// ("hooks conflicts found; no resources synchronized") by staging conflicting hook entries
// into config.yaml one at a time and running `skillshare hooks sync <name> --replace -g --json`.
func autoAdoptConflictingHooks(bin, cfgPath string, env []string, syncOut []byte) ([]byte, error) {
	parsed := parseJSONObject(string(syncOut))
	if parsed == nil {
		return nil, nil
	}
	hooksMap, _ := parsed["hooks"].(map[string]any)
	planMap, _ := hooksMap["plan"].(map[string]any)
	changes, _ := planMap["changes"].([]any)
	if len(changes) == 0 {
		return nil, nil
	}
	conflictSet := map[string]bool{}
	var conflictNames []string
	for _, rc := range changes {
		cm, _ := rc.(map[string]any)
		if cm == nil {
			continue
		}
		if fmt.Sprint(cm["action"]) == "conflict" {
			name := strings.TrimSpace(fmt.Sprint(cm["name"]))
			if name != "" && !conflictSet[name] {
				conflictSet[name] = true
				conflictNames = append(conflictNames, name)
			}
		}
	}
	if len(conflictNames) == 0 {
		return nil, nil
	}
	sort.Strings(conflictNames)

	origBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, nil
	}
	defer func() {
		_ = os.WriteFile(cfgPath, origBytes, 0o644)
	}()

	var doc map[string]any
	if err := yaml.Unmarshal(origBytes, &doc); err != nil || doc == nil {
		return nil, nil
	}
	hSec, _ := doc["hooks"].(map[string]any)
	allEntries, _ := hSec["entries"].(map[string]any)
	if len(allEntries) == 0 {
		return nil, nil
	}

	// Start with only non-conflicting entries, then add & adopt each conflicting entry one by one
	working := map[string]any{}
	for k, v := range allEntries {
		if !conflictSet[k] {
			working[k] = v
		}
	}
	for _, name := range conflictNames {
		entry, ok := allEntries[name]
		if !ok {
			continue
		}
		working[name] = entry
		doc["hooks"] = map[string]any{"entries": working}
		if stepBytes, err := yaml.Marshal(doc); err == nil {
			_ = os.WriteFile(cfgPath, stepBytes, 0o644)
			repCmd := exec.Command(bin, "hooks", "sync", name, "--replace", "-g", "--json")
			repCmd.Env = env
			_ = repCmd.Run()
		}
	}

	// Restore exact compiled config.yaml, record Codex hook trust, and re-run full sync
	_ = os.WriteFile(cfgPath, origBytes, 0o644)
	trustCmd := exec.Command(bin, "hooks", "sync", "-g", "--trust", "-y", "--json")
	trustCmd.Env = env
	_ = trustCmd.Run()

	retryCmd := exec.Command(bin, "sync", "-g", "--all", "--json")
	retryCmd.Env = env
	out, retryErr := retryCmd.CombinedOutput()
	return out, retryErr
}

func openAgentStore(req AgentRequest) (*secrets.Store, error) {
	var opts []secrets.StoreOption
	if req.SecretBackend != "" {
		opts = append(opts, secrets.WithBackend(req.SecretBackend))
	}
	return secrets.NewStore(opts...)
}

func handleSecretsCheck(req AgentRequest, home string) AgentResponse {
	nonce, err := base64.StdEncoding.DecodeString(req.NonceB64)
	if err != nil {
		return AgentResponse{OK: false, Home: home, Error: "invalid nonce"}
	}
	store, err := openAgentStore(req)
	if err != nil {
		return AgentResponse{OK: false, Home: home, Error: err.Error()}
	}
	callerEnv := launcher.EnvMap()
	results := map[string]any{}
	for _, item := range req.Refs {
		if _, seen := results[item.Ref]; seen {
			continue
		}
		val, err := store.Resolve(item.Ref, callerEnv)
		if err != nil {
			msg := err.Error()
			st := "missing"
			if strings.Contains(msg, "permissive") || strings.Contains(msg, "symlink") || strings.Contains(msg, "insecure") {
				st = "insecure"
			}
			results[item.Ref] = map[string]any{"status": st, "error": msg}
		} else {
			results[item.Ref] = map[string]any{"status": "present", "hmac": secrets.ComputeHMAC(nonce, val)}
		}
	}
	return AgentResponse{
		OK: true, Home: home,
		Result: map[string]any{"backend": store.Backend, "refs": results},
	}
}

func handleSecretsPut(req AgentRequest, home string) AgentResponse {
	nonce, err := base64.StdEncoding.DecodeString(req.NonceB64)
	if err != nil {
		return AgentResponse{OK: false, Home: home, Error: "invalid nonce"}
	}
	store, err := openAgentStore(req)
	if err != nil {
		return AgentResponse{OK: false, Home: home, Error: err.Error()}
	}
	stored := map[string]string{}
	for name, val := range req.Secrets {
		if err := store.SetManaged(name, val); err != nil {
			return AgentResponse{OK: false, Home: home, Error: err.Error()}
		}
		v, err := store.Resolve("store:"+name, nil)
		if err != nil {
			return AgentResponse{OK: false, Home: home, Error: err.Error()}
		}
		stored[name] = secrets.ComputeHMAC(nonce, v)
	}
	return AgentResponse{
		OK: true, Home: home,
		Result: map[string]any{"backend": store.Backend, "stored": stored},
	}
}

func handleDoctorProbe(req AgentRequest, home string) AgentResponse {
	nonce, _ := base64.StdEncoding.DecodeString(req.NonceB64)
	store, err := openAgentStore(req)
	if err != nil {
		return AgentResponse{OK: false, Home: home, Error: err.Error()}
	}
	callerEnv := launcher.EnvMap()
	bdir := expandWithHome(req.BinDir, home)
	if bdir == "" {
		bdir = filepath.Join(home, ".local", "bin")
	}
	livOK := false
	if p, err := exec.LookPath("liv"); err == nil && p != "" {
		livOK = true
	} else if st, err := os.Stat(filepath.Join(bdir, "liv")); err == nil && st.Mode().Perm()&0o111 != 0 {
		livOK = true
	}
	secretChecks := map[string]map[string]any{}
	var profs []map[string]any
	for _, pspec := range req.Profiles {
		h := fmt.Sprint(pspec["harness"])
		drv := launcher.Drivers[h]
		if drv == nil {
			continue
		}
		binName := drv.DefaultBinary()
		if b, ok := pspec["binary"].(string); ok && b != "" {
			binName = b
		}
		resBin, binErr := launcher.ResolveBinary(binName, callerEnv)
		reqSecs := drv.RequiredSecrets(pspec)
		var secList []map[string]string
		for k, ref := range reqSecs {
			secList = append(secList, map[string]string{"key": k, "ref": ref})
			if _, seen := secretChecks[ref]; !seen {
				val, err := store.Resolve(ref, callerEnv)
				if err != nil {
					secretChecks[ref] = map[string]any{"status": "missing", "error": err.Error()}
				} else {
					secretChecks[ref] = map[string]any{"status": "present", "hmac": secrets.ComputeHMAC(nonce, val)}
				}
			}
		}
		mode, _, eff := drv.ResolveHome(pspec)
		row := map[string]any{
			"id":      pspec["id"],
			"harness": h,
			"mode":    mode,
			"home":    eff,
			"binary":  resBin,
			"secrets": secList,
		}
		if binErr != nil {
			row["binary_error"] = binErr.Error()
		}
		profs = append(profs, row)
	}
	return AgentResponse{
		OK: true, Home: home,
		Result: map[string]any{
			"home":           home,
			"os":             runtime.GOOS,
			"arch":           runtime.GOARCH,
			"secret_backend": store.Backend,
			"liv_ok":         livOK,
			"profiles":       profs,
			"secrets":        secretChecks,
		},
	}
}
