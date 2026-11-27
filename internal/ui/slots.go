package ui

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/skillshare"
)

type SlotInfo struct {
	SlotID         string `json:"slot_id"`
	URL            string `json:"url"`
	Port           int    `json:"port"`
	Mode           string `json:"mode"` // "catalog" | "live" | "preview"
	Remote         bool   `json:"remote,omitempty"`
	ReadOnly       bool   `json:"readonly"`
	ConfigPath     string `json:"config_path"`
	TargetID       string `json:"target_id,omitempty"`
	StartedAt      int64  `json:"started_at"`
	DisplaySource  string `json:"display_source"`
	FallbackReason string `json:"fallback_reason,omitempty"`
}

type slotProc struct {
	info        SlotInfo
	cmd         *exec.Cmd
	stdinCloser io.Closer
	done        chan struct{}
	waitErr     error
	tempDir     string
	proxy       *httputil.ReverseProxy
}

type SlotManager struct {
	repoRoot   string
	metaHost   string
	metaPort   int
	mu         sync.Mutex
	slots      map[string]*slotProc
	onMutation func(slotID string)
}

func NewSlotManager(repoRoot, metaHost string, metaPort int) *SlotManager {
	return &SlotManager{
		repoRoot: repoRoot,
		metaHost: metaHost,
		metaPort: metaPort,
		slots:    map[string]*slotProc{},
	}
}

func (m *SlotManager) trustedOrigin() string {
	return fmt.Sprintf("http://%s:%d", m.metaHost, m.metaPort)
}

func (m *SlotManager) metaHostPort() string {
	return fmt.Sprintf("%s:%d", m.metaHost, m.metaPort)
}

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func randomRemotePort() int {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return 29100 + int(binary.BigEndian.Uint16(b[:])%8000)
}

func waitForHTTPOrExit(port int, basePath string, timeout time.Duration, procDone <-chan struct{}, procErr *error) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 800 * time.Millisecond}
	probeURL := fmt.Sprintf("http://127.0.0.1:%d%s/", port, basePath)
	for time.Now().Before(deadline) {
		if procDone != nil {
			select {
			case <-procDone:
				if procErr != nil && *procErr != nil {
					return *procErr
				}
				return fmt.Errorf("process exited before becoming ready")
			default:
			}
		}
		resp, err := client.Get(probeURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		time.Sleep(120 * time.Millisecond)
	}
	return fmt.Errorf("skillshare ui did not become ready on port %d within %s", port, timeout)
}

func (sp *slotProc) isAlive() bool {
	if sp == nil || sp.done == nil {
		return false
	}
	select {
	case <-sp.done:
		return false
	default:
		return true
	}
}

func (m *SlotManager) stopLocked(slotID string) {
	sp, ok := m.slots[slotID]
	if !ok {
		return
	}
	delete(m.slots, slotID)
	// Closing stdin first triggers the remote SSH watchdog to cleanly stop remote skillshare ui immediately.
	if sp.stdinCloser != nil {
		_ = sp.stdinCloser.Close()
	}
	if sp.cmd != nil && sp.cmd.Process != nil {
		_ = sp.cmd.Process.Signal(os.Interrupt)
		if sp.done != nil {
			select {
			case <-sp.done:
			case <-time.After(2 * time.Second):
				_ = sp.cmd.Process.Kill()
				<-sp.done
			}
		}
	}
	if sp.tempDir != "" {
		_ = os.RemoveAll(sp.tempDir)
	}
}

func (m *SlotManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.slots {
		m.stopLocked(id)
	}
}

func (m *SlotManager) InvalidateTarget(targetID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked("env/" + targetID)
}

// InvalidatePreviewTarget stops only read-only compiled preview slots for targetID,
// leaving live local or SSH-tunneled Skillshare sessions warm and intact.
func (m *SlotManager) InvalidatePreviewTarget(targetID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	slotID := "env/" + targetID
	if sp, ok := m.slots[slotID]; ok && sp.info.Mode != "live" {
		m.stopLocked(slotID)
	}
}

func (m *SlotManager) InvalidateAllEnvs() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.slots {
		if strings.HasPrefix(id, "env/") {
			m.stopLocked(id)
		}
	}
}

// InvalidatePreviewEnvs stops only non-live preview env slots across the fleet.
func (m *SlotManager) InvalidatePreviewEnvs() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, sp := range m.slots {
		if strings.HasPrefix(id, "env/") && sp.info.Mode != "live" {
			m.stopLocked(id)
		}
	}
}

func (m *SlotManager) Get(slotID string) *slotProc {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.slots[slotID]
}

func (m *SlotManager) buildProxy(port int, displaySource string) *httputil.ReverseProxy {
	targetURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	hostPort := m.metaHostPort()
	origin := m.trustedOrigin()

	rp := httputil.NewSingleHostReverseProxy(targetURL)
	origDirector := rp.Director
	rp.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = hostPort
		if req.Header.Get("Origin") != "" {
			req.Header.Set("Origin", origin)
		}
		if strings.HasSuffix(req.URL.Path, "/api/overview") {
			req.Header.Del("Accept-Encoding")
		}
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		if displaySource == "" || resp.StatusCode != http.StatusOK {
			return nil
		}
		if !strings.HasSuffix(resp.Request.URL.Path, "/api/overview") {
			return nil
		}
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if !strings.Contains(ct, "application/json") {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		var doc map[string]any
		if json.Unmarshal(body, &doc) == nil {
			doc["configDir"] = displaySource
			if out, err := json.Marshal(doc); err == nil {
				body = out
			}
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		resp.Header.Del("Content-Encoding")
		return nil
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "skillshare ui upstream error: " + err.Error()})
	}
	return rp
}

func (m *SlotManager) startSlotLocked(slotID, configPath, mode, targetID, tempDir, displaySource, fallbackReason string, remoteHost, readOnly bool) (SlotInfo, error) {
	bin := skillshare.FindLocalBinary("")
	if bin == "" {
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		return SlotInfo{}, fmt.Errorf("stock `skillshare` binary not found on controller (install via `brew install skillshare`)")
	}
	port, err := freeLocalPort()
	if err != nil {
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		return SlotInfo{}, err
	}
	basePath := "/ss/" + slotID
	cmd := exec.Command(bin, "ui", "-g", "--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"--base-path", basePath, "--trusted-origin", m.trustedOrigin(), "--no-open")

	realHome, _ := os.UserHomeDir()
	env := append([]string(nil), os.Environ()...)
	env = append(env, "SKILLSHARE_CONFIG="+configPath, "NO_COLOR=1")
	if os.Getenv("XDG_CACHE_HOME") == "" && realHome != "" {
		env = append(env, "XDG_CACHE_HOME="+filepath.Join(realHome, ".cache"))
	}
	// Isolate `catalog` and `preview` slots from the user's real ~/.claude & ~/.codex so the
	// library-only Catalog UI never sees host conflicts or modifies live local harnesses!
	if mode == "catalog" {
		catHome, catState := prepareCatalogSandboxHome(realHome)
		if catHome != "" {
			env = setEnvVar(env, "HOME", catHome)
			env = setEnvVar(env, "XDG_STATE_HOME", catState)
			// Pre-sync inside the isolated catalog sandbox so Catalog UI opens 100% clean
			prep := exec.Command(bin, "sync", "-g", "--all", "--json")
			prep.Env = env
			_ = prep.Run()
		}
	} else if mode == "preview" && tempDir != "" {
		prevHome := filepath.Join(tempDir, "home")
		prevState := filepath.Join(tempDir, "state")
		_ = os.MkdirAll(filepath.Join(prevHome, ".claude"), 0o755)
		_ = os.MkdirAll(filepath.Join(prevHome, ".codex"), 0o755)
		_ = os.MkdirAll(prevState, 0o755)
		env = setEnvVar(env, "HOME", prevHome)
		env = setEnvVar(env, "XDG_STATE_HOME", prevState)
	}
	cmd.Env = env
	var errBuf bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		return SlotInfo{}, err
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()

	if err := waitForHTTPOrExit(port, basePath, 10*time.Second, done, &waitErr); err != nil {
		_ = cmd.Process.Kill()
		<-done
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		msg := strings.TrimSpace(errBuf.String())
		if msg != "" {
			return SlotInfo{}, fmt.Errorf("%v (%s)", err, msg)
		}
		return SlotInfo{}, err
	}
	info := SlotInfo{
		SlotID:         slotID,
		URL:            basePath + "/",
		Port:           port,
		Mode:           mode,
		Remote:         remoteHost,
		ReadOnly:       readOnly,
		ConfigPath:     configPath,
		TargetID:       targetID,
		StartedAt:      time.Now().Unix(),
		DisplaySource:  displaySource,
		FallbackReason: fallbackReason,
	}
	m.slots[slotID] = &slotProc{
		info:    info,
		cmd:     cmd,
		done:    done,
		waitErr: waitErr,
		tempDir: tempDir,
		proxy:   m.buildProxy(port, displaySource),
	}
	return info, nil
}

func cleanSSHErr(raw string) string {
	var parts []string
	for _, ln := range strings.Split(raw, "\n") {
		s := strings.TrimSpace(ln)
		if s == "" || strings.Contains(s, "setlocale") || strings.HasPrefix(s, "Warning: Permanently added") || strings.Contains(s, "open failed: connect failed:") {
			continue
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

// startRemoteLiveSlotLocked launches the remote host's own stock `skillshare ui` over a managed
// SSH loopback tunnel (-L 127.0.0.1:<local>:127.0.0.1:<remote>) with a stdin-EOF watchdog so
// closing the slot immediately terminates the remote process with zero orphans.
func (m *SlotManager) startRemoteLiveSlotLocked(slotID string, t *model.Target, sroot string) (SlotInfo, error) {
	localPort, err := freeLocalPort()
	if err != nil {
		return SlotInfo{}, err
	}
	remotePort := randomRemotePort()
	basePath := "/ss/" + slotID
	origin := m.trustedOrigin()

	cfgExpr := sroot + "/config.yaml"
	if strings.HasPrefix(cfgExpr, "~/") {
		cfgExpr = "$HOME/" + strings.TrimPrefix(cfgExpr, "~/")
	}

	candidates := []string{
		"skillshare",
		`"$HOME/.local/bin/skillshare"`,
		"/opt/homebrew/bin/skillshare",
		"/usr/local/bin/skillshare",
		`"$HOME/.nix-profile/bin/skillshare"`,
		`"/etc/profiles/per-user/$USER/bin/skillshare"`,
		"/run/current-system/sw/bin/skillshare",
	}
	if strings.TrimSpace(t.SkillshareBin) != "" {
		candidates = append([]string{fmt.Sprintf("%q", t.SkillshareBin)}, candidates...)
	}

	script := fmt.Sprintf(`export LC_ALL=C.UTF-8 LANG=C.UTF-8 NO_COLOR=1
SS=""
for b in %s; do
  if [ -x "$b" ]; then SS="$b"; break; fi
  if command -v "$b" >/dev/null 2>&1; then SS="$(command -v "$b")"; break; fi
done
if [ -z "$SS" ]; then echo "skillshare binary not found on %s" >&2; exit 127; fi
CFG="%s"
if [ ! -f "$CFG" ]; then echo "remote config.yaml missing ($CFG) — push to host first" >&2; exit 126; fi
export SKILLSHARE_CONFIG="$CFG"
if [ -z "${XDG_CACHE_HOME:-}" ]; then export XDG_CACHE_HOME="$HOME/.cache"; fi
exec 3<&0
"$SS" ui -g --host 127.0.0.1 --port %d --base-path "%s" --trusted-origin "%s" --no-open </dev/null &
PID=$!
( cat <&3 >/dev/null 2>&1; kill -TERM "$PID" 2>/dev/null; sleep 1; kill -KILL "$PID" 2>/dev/null ) &
WPID=$!
wait "$PID"
RC=$?
kill "$WPID" 2>/dev/null || true
exit $RC`, strings.Join(candidates, " "), t.ID, cfgExpr, remotePort, basePath, origin)

	sshArgs := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=6",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort),
		t.SSH,
		"sh -c '" + strings.ReplaceAll(script, "'", `'"'"'`) + "'",
	}
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8", "LANG=C.UTF-8")
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return SlotInfo{}, err
	}
	var errBuf bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		return SlotInfo{}, err
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()

	if err := waitForHTTPOrExit(localPort, basePath, 20*time.Second, done, &waitErr); err != nil {
		_ = stdinPipe.Close()
		_ = cmd.Process.Kill()
		<-done
		if msg := cleanSSHErr(errBuf.String()); msg != "" {
			return SlotInfo{}, fmt.Errorf("%s", msg)
		}
		return SlotInfo{}, err
	}

	display := t.ID + " (live via SSH)"
	info := SlotInfo{
		SlotID:        slotID,
		URL:           basePath + "/",
		Port:          localPort,
		Mode:          "live",
		Remote:        true,
		ReadOnly:      false,
		ConfigPath:    sroot + "/config.yaml",
		TargetID:      t.ID,
		StartedAt:     time.Now().Unix(),
		DisplaySource: display,
	}
	m.slots[slotID] = &slotProc{
		info:        info,
		cmd:         cmd,
		stdinCloser: stdinPipe,
		done:        done,
		waitErr:     waitErr,
		proxy:       m.buildProxy(localPort, display),
	}
	return info, nil
}

func setEnvVar(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+val)
}

func prepareCatalogSandboxHome(realHome string) (catHome, catState string) {
	if realHome == "" {
		return "", ""
	}
	base := filepath.Join(realHome, ".local", "state", "livery")
	catHome = filepath.Join(base, "catalog-home")
	catState = filepath.Join(base, "catalog-state")
	_ = os.MkdirAll(filepath.Join(catHome, ".claude"), 0o700)
	_ = os.MkdirAll(filepath.Join(catHome, ".codex"), 0o700)
	_ = os.Remove(filepath.Join(catHome, ".codex", "config.toml"))
	_ = os.MkdirAll(catState, 0o700)
	// Symlink ~/.gitconfig and ~/.ssh so git commits & GitHub skill installs inside Catalog work seamlessly
	for _, name := range []string{".gitconfig", ".ssh"} {
		src := filepath.Join(realHome, name)
		dst := filepath.Join(catHome, name)
		if _, err := os.Stat(src); err == nil {
			if _, lerr := os.Lstat(dst); os.IsNotExist(lerr) {
				_ = os.Symlink(src, dst)
			}
		}
	}
	return catHome, catState
}

// EnsureCatalog starts/reuses the library-only Full Catalog Skillshare UI slot pointing directly at
// <dataHome>/catalog/skillshare/config.yaml (its own standalone git repo, targets: {}, never syncs!).
func (m *SlotManager) EnsureCatalog(restart bool) (SlotInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !restart {
		if cur, ok := m.slots["catalog"]; ok && cur.isAlive() {
			return cur.info, nil
		}
	}
	m.stopLocked("catalog")
	if err := skillshare.EnsureCatalogInitialized(m.repoRoot); err != nil {
		return SlotInfo{}, err
	}
	skillshare.NormalizeCatalogLibraryConfig(m.repoRoot)
	cfgPath := skillshare.CatalogConfigPath(m.repoRoot)
	return m.startSlotLocked("catalog", cfgPath, "catalog", "", "", "catalog/skillshare (Full Library)", "", false, false)
}

// EnsureEnv starts/reuses a per-environment Skillshare UI slot:
//   - Local host (e.g. dev@macbook): mounts its live standalone skillshare git repo Read/Write!
//   - Remote SSH host: launches the remote host's live skillshare ui over an SSH loopback tunnel (Read/Write)!
//   - Offline / unreachable / explicit preview: falls back to a compiled Read-Only preview with FallbackReason set.
func (m *SlotManager) EnsureEnv(repo *model.Repo, t *model.Target, mode string, restart bool) (SlotInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	slotID := "env/" + t.ID

	sroot := strings.TrimRight(t.SkillshareRoot, "/")
	if sroot == "" {
		sroot = "~/.local/share/agent-config/skillshare"
	}

	wantPreview := mode == "preview"
	if !restart {
		if cur, ok := m.slots[slotID]; ok && cur.isAlive() {
			if (wantPreview && cur.info.Mode == "preview") || (!wantPreview && cur.info.Mode == "live") || (mode == "auto" && cur.info.Mode == "preview") {
				return cur.info, nil
			}
		}
	}
	m.stopLocked(slotID)

	fallbackReason := ""
	if !wantPreview {
		if t.IsLocal() {
			home := t.Home
			if home == "" {
				home, _ = os.UserHomeDir()
			}
			liveCfg := filepath.Join(sroot, "config.yaml")
			if strings.HasPrefix(sroot, "~/") {
				liveCfg = filepath.Join(home, sroot[2:], "config.yaml")
			}
			if fileExists(liveCfg) {
				return m.startSlotLocked(slotID, liveCfg, "live", t.ID, "", t.ID+" (live git repo)", "", false, false)
			}
			fallbackReason = fmt.Sprintf("Local Skillshare config not found at %s — push to host first.", liveCfg)
		} else {
			info, err := m.startRemoteLiveSlotLocked(slotID, t, sroot)
			if err == nil {
				return info, nil
			}
			fallbackReason = err.Error()
		}
	} else {
		fallbackReason = "Read-only compiled preview mode"
	}

	// Fallback: build compiled read-only preview sandbox from Catalog + Matrix + env-overrides
	tmp, err := os.MkdirTemp("", "livery-ss-env-*")
	if err != nil {
		return SlotInfo{}, err
	}
	if err := buildCompiledEnvPreview(repo, t, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return SlotInfo{}, err
	}
	cfgPath := filepath.Join(tmp, "config.yaml")
	return m.startSlotLocked(slotID, cfgPath, "preview", t.ID, tmp, t.ID+" (compiled preview)", fallbackReason, !t.IsLocal(), true)
}

func buildCompiledEnvPreview(repo *model.Repo, t *model.Target, tmpDir string) error {
	skills, _ := repo.ResolveHostCategoryItems(t, "skills")
	agents, _ := repo.ResolveHostCategoryItems(t, "agents")
	extras, _ := repo.ResolveHostCategoryItems(t, "extras")
	mcps, _ := repo.ResolveHostCategoryItems(t, "mcp")
	hooks, _ := repo.ResolveHostCategoryItems(t, "hooks")
	plugins, _ := repo.ResolveHostCategoryItems(t, "plugins")

	enabled := skillshare.EnvEnabledItems{
		Skills: skills, Agents: agents, Extras: extras, MCP: mcps, Hooks: hooks, Plugins: plugins,
	}
	home, _ := os.UserHomeDir()
	cfgBytes, selectedSkills, selectedAgents, err := skillshare.CompileEnvSkillshareConfig(repo.Root, t, enabled, home)
	if err != nil {
		return err
	}
	skillsDir := filepath.Join(tmpDir, "skills")
	_ = os.MkdirAll(skillsDir, 0o755)
	for _, rs := range selectedSkills {
		_ = skillshare.CopyTree(rs.Dir, filepath.Join(skillsDir, rs.RelPath))
	}
	if len(selectedAgents) > 0 {
		agentsDir := filepath.Join(tmpDir, "agents")
		_ = os.MkdirAll(agentsDir, 0o755)
		for _, ag := range selectedAgents {
			if b, err := os.ReadFile(ag.Dir); err == nil {
				_ = os.WriteFile(filepath.Join(agentsDir, ag.RelPath), b, 0o644)
			}
		}
	}

	slotYAML, _, err := skillshare.PrepareSlotConfig(cfgBytes, skillsDir, tmpDir)
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, slotYAML, 0o644); err != nil {
		return err
	}
	if bin := skillshare.FindLocalBinary(""); bin != "" && len(selectedSkills) > 0 {
		cmd := exec.Command(bin, "sync", "-g")
		cmd.Env = append(os.Environ(), "SKILLSHARE_CONFIG="+cfgPath, "NO_COLOR=1")
		_ = cmd.Run()
	}
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// ServeProxy routes /ss/<slot>/... to the corresponding slot's black-box skillshare ui subprocess.
func (m *SlotManager) ServeProxy(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/ss/")
	var slotID string
	if strings.HasPrefix(rest, "catalog") && (rest == "catalog" || strings.HasPrefix(rest, "catalog/")) {
		slotID = "catalog"
	} else if strings.HasPrefix(rest, "env/") {
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) >= 2 && parts[1] != "" {
			if unesc, err := url.PathUnescape(parts[1]); err == nil {
				slotID = "env/" + unesc
			} else {
				slotID = "env/" + parts[1]
			}
		}
	}
	sp := m.Get(slotID)
	if sp == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "skillshare slot not running: " + slotID})
		return
	}
	isMut := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
	if sp.info.ReadOnly && isMut {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "This remote preview is read-only. Edit items in Catalog & Matrix, or use Backward Sync to import changes made directly on the host.",
		})
		return
	}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	sp.proxy.ServeHTTP(rec, r)
	if m.onMutation != nil && isMut && rec.status < 400 {
		m.onMutation(slotID)
	}
}
