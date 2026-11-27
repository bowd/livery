package ui

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bowd/livery/internal/fleet"
	"github.com/bowd/livery/internal/herdr"
	"github.com/bowd/livery/internal/launcher"
	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/remote"
	"github.com/bowd/livery/internal/render"
	"github.com/bowd/livery/internal/skillshare"
	"github.com/bowd/livery/internal/version"
)

//go:embed assets/*
var uiAssets embed.FS

type SyncStatus struct {
	Target              string               `json:"target"`
	CheckedAt           int64                `json:"checked_at"`
	OutOfSync           bool                 `json:"out_of_sync"`
	PushCount           int                  `json:"push_count"`
	PullCount           int                  `json:"pull_count"`
	PushError           string               `json:"push_error,omitempty"`
	PushChanges         []fleet.Change       `json:"push_changes"`
	SkillshareNeedsSync bool                 `json:"skillshare_needs_sync"`
	SkillshareLines     []string             `json:"skillshare_lines"`
	PushFormatted       string               `json:"push_formatted,omitempty"`
	Pull                fleet.EnvDriftReport `json:"pull"`
}

type sseEvent struct {
	Event string
	Data  []byte
}

type Server struct {
	repoRoot   string
	host       string
	port       int
	csrfToken  string
	slots      *SlotManager
	httpServer *http.Server
	listener   net.Listener
	mu             sync.RWMutex
	probeCache     map[string]remote.HostProbe
	driftCache     map[string]fleet.EnvDriftReport
	syncCache      map[string]SyncStatus
	unmanagedCache map[string]fleet.UnmanagedReport

	sseMu      sync.Mutex
	sseClients map[chan sseEvent]struct{}
	slotTimers map[string]*time.Timer
	shutdownCh chan struct{}
	closeOnce  sync.Once
}

func (s *Server) invalidateSyncCache(tid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tid == "" {
		s.syncCache = map[string]SyncStatus{}
	} else {
		delete(s.syncCache, tid)
		delete(s.driftCache, tid)
	}
}

func (s *Server) broadcastSSE(event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ev := sseEvent{Event: event, Data: b}
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	for ch := range s.sseClients {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Server) scheduleSlotSyncCheck(slotID string) {
	tid := ""
	if strings.HasPrefix(slotID, "env/") {
		tid = strings.TrimPrefix(slotID, "env/")
	}
	s.broadcastSSE("slot-activity", map[string]any{
		"slot":     slotID,
		"target":   tid,
		"checking": true,
	})

	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	select {
	case <-s.shutdownCh:
		return
	default:
	}
	if prev, ok := s.slotTimers[slotID]; ok {
		prev.Stop()
	}
	s.slotTimers[slotID] = time.AfterFunc(450*time.Millisecond, func() {
		select {
		case <-s.shutdownCh:
			return
		default:
		}
		repo, err := model.LoadRepo(s.repoRoot)
		if err != nil {
			return
		}
		if tid != "" {
			t, err := repo.Target(tid)
			if err != nil {
				return
			}
			st := s.computeTargetSyncStatus(repo, t)
			s.broadcastSSE("sync-status", map[string]any{
				"target": tid,
				"status": st,
			})
			return
		}
		// Catalog mutated: refresh sync status for all hosts in parallel
		results := map[string]SyncStatus{}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, t := range repo.Targets {
			t := t
			wg.Add(1)
			go func() {
				defer wg.Done()
				st := s.computeTargetSyncStatus(repo, t)
				mu.Lock()
				results[t.ID] = st
				mu.Unlock()
			}()
		}
		wg.Wait()
		s.broadcastSSE("fleet-sync-status", map[string]any{
			"statuses": results,
		})
	})
}

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan sseEvent, 32)
	s.sseMu.Lock()
	select {
	case <-s.shutdownCh:
		s.sseMu.Unlock()
		return
	default:
	}
	s.sseClients[ch] = struct{}{}
	s.sseMu.Unlock()
	defer func() {
		s.sseMu.Lock()
		delete(s.sseClients, ch)
		s.sseMu.Unlock()
	}()

	_, _ = fmt.Fprintf(w, ": connected\nevent: hello\ndata: {\"version\":%q}\n\n", version.Version)
	flusher.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.shutdownCh:
			return
		case ev := <-ch:
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, ev.Data)
			flusher.Flush()
		case <-ping.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func loadOrCreateCSRFToken(ephemeral bool) string {
	gen := func() string {
		tokBytes := make([]byte, 24)
		_, _ = rand.Read(tokBytes)
		return hex.EncodeToString(tokBytes)
	}
	if ephemeral {
		return gen()
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return gen()
	}
	stateDir := filepath.Join(home, ".local", "state", "livery")
	tokFile := filepath.Join(stateDir, "ui-csrf.token")
	if b, err := os.ReadFile(tokFile); err == nil {
		s := strings.TrimSpace(string(b))
		if len(s) >= 32 {
			return s
		}
	}
	tok := gen()
	_ = os.MkdirAll(stateDir, 0o700)
	_ = os.WriteFile(tokFile, []byte(tok+"\n"), 0o600)
	return tok
}

func NewServer(repoRoot, host string, port int) (*Server, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	tok := loadOrCreateCSRFToken(port == 0)

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return nil, err
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port
	s := &Server{
		repoRoot:   repoRoot,
		host:       host,
		port:       actualPort,
		csrfToken:  tok,
		slots:      NewSlotManager(repoRoot, host, actualPort),
		listener:   ln,
		probeCache:     map[string]remote.HostProbe{},
		driftCache:     map[string]fleet.EnvDriftReport{},
		syncCache:      map[string]SyncStatus{},
		unmanagedCache: map[string]fleet.UnmanagedReport{},
		sseClients: map[chan sseEvent]struct{}{},
		slotTimers: map[string]*time.Timer{},
		shutdownCh: make(chan struct{}),
	}
	s.slots.onMutation = func(slotID string) {
		if slotID == "catalog" {
			s.invalidateSyncCache("")
		} else if strings.HasPrefix(slotID, "env/") {
			s.invalidateSyncCache(strings.TrimPrefix(slotID, "env/"))
		}
		s.scheduleSlotSyncCheck(slotID)
	}
	s.httpServer = &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 15 * time.Second,
	}
	return s, nil
}

func (s *Server) URL() string {
	return fmt.Sprintf("http://%s:%d", s.host, s.port)
}

func (s *Server) Port() int {
	return s.port
}

func (s *Server) CSRFToken() string {
	return s.csrfToken
}

func (s *Server) Serve() error {
	return s.httpServer.Serve(s.listener)
}

func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		close(s.shutdownCh)
	})
	s.sseMu.Lock()
	for id, t := range s.slotTimers {
		t.Stop()
		delete(s.slotTimers, id)
	}
	s.sseMu.Unlock()
	s.slots.StopAll()
	return s.httpServer.Close()
}

func (s *Server) routes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/ss/") {
			s.slots.ServeProxy(w, r)
			return
		}
		if p == "/" || p == "/index.html" {
			s.serveIndex(w, r)
			return
		}
		if p == "/favicon.ico" {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/assets/favicon.ico"
			s.serveAsset(w, r2)
			return
		}
		if strings.HasPrefix(p, "/assets/") {
			s.serveAsset(w, r)
			return
		}
		if strings.HasPrefix(p, "/api/") {
			if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
				if !s.verifyMutation(w, r) {
					return
				}
			}
			s.handleAPI(w, r)
			return
		}
		// SPA client-side routes (/environments, /catalog/*, /matrix/*, /env/*) serve index.html on refresh
		if r.Method == http.MethodGet && (p == "/environments" || strings.HasPrefix(p, "/catalog") || strings.HasPrefix(p, "/matrix") || strings.HasPrefix(p, "/env/")) {
			s.serveIndex(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func (s *Server) verifyMutation(w http.ResponseWriter, r *http.Request) bool {
	tok := r.Header.Get("X-Livery-CSRF")
	if tok == "" {
		tok = r.Header.Get("X-Agentcfg-CSRF")
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(s.csrfToken)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid or missing CSRF token"})
		return false
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		allowed := s.URL()
		alt := fmt.Sprintf("http://localhost:%d", s.port)
		if origin != allowed && origin != alt {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "cross-origin mutation blocked"})
			return false
		}
	}
	return true
}

func (s *Server) serveIndex(w http.ResponseWriter, _ *http.Request) {
	b, err := uiAssets.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, "missing index.html", http.StatusInternalServerError)
		return
	}
	inject := fmt.Sprintf(`<script>window.__LIVERY_CSRF__ = %q;</script>`, s.csrfToken)
	html := strings.Replace(string(b), "</head>", inject+"\n</head>", 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, html)
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	if strings.Contains(name, "..") || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	b, err := uiAssets.ReadFile("assets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".png"):
		w.Header().Set("Content-Type", "image/png")
	case strings.HasSuffix(name, ".ico"):
		w.Header().Set("Content-Type", "image/x-icon")
	case strings.HasSuffix(name, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	if s.handleExtraAPI(w, r) {
		return
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == "/api/events":
		s.apiEvents(w, r)
	case r.Method == http.MethodGet && p == "/api/state":
		s.apiState(w)
	case r.Method == http.MethodGet && p == "/api/git/diff":
		s.apiGitDiff(w)
	case r.Method == http.MethodPost && p == "/api/git/commit":
		s.apiGitCommit(w, r)
	case r.Method == http.MethodPost && p == "/api/git/discard":
		s.apiGitDiscard(w, r)
	case r.Method == http.MethodGet && p == "/api/env/discover":
		s.apiEnvDiscover(w)
	case r.Method == http.MethodPost && p == "/api/env/probe":
		s.apiEnvProbe(w, r)
	case r.Method == http.MethodPost && p == "/api/env/add":
		s.apiEnvAdd(w, r)
	case r.Method == http.MethodPost && p == "/api/herdr/install-mod":
		s.apiHerdrInstallMod(w, r)
	case r.Method == http.MethodPost && p == "/api/ss/catalog/ensure":
		s.apiEnsureCatalog(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/api/ss/env/") && strings.HasSuffix(p, "/ensure"):
		tid := decodeSeg(strings.TrimSuffix(strings.TrimPrefix(p, "/api/ss/env/"), "/ensure"))
		s.apiEnsureEnv(w, r, tid)
	case r.Method == http.MethodPost && p == "/api/fleet/probe":
		s.apiFleetProbe(w)
	case r.Method == http.MethodPost && p == "/api/fleet/drift":
		s.apiFleetDrift(w)
	case r.Method == http.MethodPost && p == "/api/fleet/sync-status":
		s.apiFleetSyncStatus(w)
	case r.Method == http.MethodPost && p == "/api/fleet/plan":
		s.apiFleetPlan(w)
	case r.Method == http.MethodPost && p == "/api/fleet/sync":
		s.apiFleetSync(w, r)
	case r.Method == http.MethodPost && p == "/api/fleet/audit":
		s.apiFleetAudit(w)
	case r.Method == http.MethodPost && p == "/api/fleet/doctor":
		s.apiFleetDoctor(w)
	case r.Method == http.MethodPost && p == "/api/fleet/secrets-status":
		s.apiFleetSecrets(w)
	case strings.HasPrefix(p, "/api/targets/"):
		s.routeTargetAPI(w, r, strings.TrimPrefix(p, "/api/targets/"))
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown endpoint: " + p})
	}
}

func (s *Server) routeTargetAPI(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "invalid target route"})
		return
	}
	tid := decodeSeg(parts[0])
	action := parts[1]
	switch {
	case r.Method == http.MethodGet && action == "bundle":
		s.apiTargetBundle(w, tid)
	case r.Method == http.MethodPost && action == "config":
		s.apiTargetConfig(w, r, tid)
	case r.Method == http.MethodPost && action == "probe":
		s.apiTargetProbe(w, tid)
	case r.Method == http.MethodPost && action == "plan":
		s.apiTargetPlan(w, tid)
	case r.Method == http.MethodPost && action == "sync-preview":
		s.apiTargetSyncPreview(w, tid)
	case r.Method == http.MethodPost && action == "sync":
		s.apiTargetSync(w, r, tid)
	case r.Method == http.MethodPost && action == "pull":
		s.apiTargetPull(w, r, tid)
	case r.Method == http.MethodPost && action == "drift":
		s.apiTargetDrift(w, tid)
	case r.Method == http.MethodPost && action == "backward-sync":
		s.apiTargetBackwardSync(w, r, tid)
	case r.Method == http.MethodPost && action == "unmanaged":
		s.apiTargetUnmanaged(w, tid)
	case r.Method == http.MethodPost && action == "unmanaged/apply":
		s.apiTargetUnmanagedApply(w, r, tid)
	case r.Method == http.MethodPost && action == "env-override":
		s.apiTargetEnvOverride(w, r, tid)
	case r.Method == http.MethodPost && action == "audit":
		s.apiTargetAudit(w, tid)
	case r.Method == http.MethodPost && action == "doctor":
		s.apiTargetDoctor(w, tid)
	case r.Method == http.MethodPost && action == "secrets-status":
		s.apiTargetSecrets(w, tid)
	case r.Method == http.MethodPost && action == "install-skillshare":
		s.apiTargetInstallSkillshare(w, r, tid)
	case r.Method == http.MethodPost && action == "bootstrap-liv":
		s.apiTargetBootstrapLiv(w, r, tid)
	case r.Method == http.MethodPost && action == "remove":
		s.apiTargetRemove(w, r, tid)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown target action: " + action})
	}
}

func decodeSeg(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

func readJSONBody(r *http.Request) map[string]any {
	out := map[string]any{}
	b, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err == nil && len(bytes.TrimSpace(b)) > 0 {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) gitStatusLines() []string {
	cmd := exec.Command("git", "-C", s.repoRoot, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, ln := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	return lines
}

func (s *Server) apiState(w http.ResponseWriter) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	ssBin, ssVer := skillshare.LocalVersion("")
	gitLines := s.gitStatusLines()
	lockAll, _ := model.LoadLock(repo.Root)
	catSS := skillshare.LoadCatalog(repo.Root)

	s.mu.RLock()
	probes := make(map[string]remote.HostProbe, len(s.probeCache))
	for k, v := range s.probeCache {
		probes[k] = v
	}
	drifts := make(map[string]fleet.EnvDriftReport, len(s.driftCache))
	for k, v := range s.driftCache {
		drifts[k] = v
	}
	syncs := make(map[string]SyncStatus, len(s.syncCache))
	for k, v := range s.syncCache {
		syncs[k] = v
	}
	s.mu.RUnlock()

	var targetRows []map[string]any
	fragToTargets := map[string][]string{}
	for _, t := range repo.Targets {
		frags, _ := repo.ResolveHostCategoryItems(t, "prompts")
		skills, _ := repo.ResolveHostCategoryItems(t, "skills")
		mcps, _ := repo.ResolveHostCategoryItems(t, "mcp")
		hooks, _ := repo.ResolveHostCategoryItems(t, "hooks")
		agents, _ := repo.ResolveHostCategoryItems(t, "agents")
		extras, _ := repo.ResolveHostCategoryItems(t, "extras")
		plugins, _ := repo.ResolveHostCategoryItems(t, "plugins")
		launchers, _ := repo.ResolveHostCategoryItems(t, "launchers")
		for _, fid := range frags {
			fragToTargets[fid] = append(fragToTargets[fid], t.ID)
		}
		ov := skillshare.LoadEnvOverride(repo.Root, t)
		var envTargets []string
		for tn := range ov.Targets {
			envTargets = append(envTargets, tn)
		}
		sort.Strings(envTargets)

		var prObj any
		if pr, ok := probes[t.ID]; ok {
			prObj = pr
		}
		var drObj any
		if dr, ok := drifts[t.ID]; ok {
			drObj = dr
		}
		var syncObj any
		if sy, ok := syncs[t.ID]; ok {
			syncObj = sy
		}
		lastAt := ""
		if lt := lockAll[t.ID]; lt != nil {
			lastAt = lt.At
		}
		targetRows = append(targetRows, map[string]any{
			"id":                           t.ID,
			"ssh":                          t.SSH,
			"is_local":                     t.IsLocal(),
			"providers":                    t.Providers,
			"profiles":                     t.Profiles,
			"effective_skillshare_targets": nonNilStrings(envTargets),
			"skillshare_mode":              ov.Mode,
			"projects_count":               len(ov.Projects),
			"local_override_count":         len(ov.LocalSkills) + len(ov.LocalMCP) + len(ov.LocalHooks) + len(ov.LocalExtras) + len(ov.ExtraConfig),
			"last_synced_at":               lastAt,
			"probe":                        prObj,
			"drift":                        drObj,
			"sync_status":                  syncObj,
			"counts": map[string]int{
				"prompts":   len(frags),
				"skills":    len(skills),
				"mcp":       len(mcps),
				"hooks":     len(hooks),
				"agents":    len(agents),
				"extras":    len(extras),
				"plugins":   len(plugins),
				"launchers": len(launchers),
			},
		})
	}

	var projectRows []map[string]any
	for _, ov := range repo.Overlays {
		projectRows = append(projectRows, map[string]any{
			"id":     model.ProjectEnvID(ov.Target, ov.Slug),
			"slug":   ov.Slug,
			"target": ov.Target,
			"path":   ov.Path,
			"why":    ov.Why,
			"files":  nonNilStrings(ov.Files),
			"mcp":    nonNilStrings(ov.MCP),
			"skills": nonNilStrings(ov.Skills),
		})
	}

	var catProfiles []map[string]any
	var pkeys []string
	for k := range repo.Profiles {
		pkeys = append(pkeys, k)
	}
	sort.Strings(pkeys)
	fragToProfiles := map[string][]string{}
	profDesc := map[string]string{}
	for _, k := range pkeys {
		pr := repo.Profiles[k]
		profDesc[k] = pr.Description
		for _, fid := range pr.Fragments {
			fragToProfiles[fid] = append(fragToProfiles[fid], k)
		}
		var normSkills []string
		for _, s := range pr.Skills {
			normSkills = append(normSkills, model.NormalizeSkillName(s))
		}
		catProfiles = append(catProfiles, map[string]any{
			"id":          pr.ID,
			"description": pr.Description,
			"extends":     nonNilStrings(pr.Extends),
			"fragments":   nonNilStrings(pr.Fragments),
			"skills":      nonNilStrings(normSkills),
			"mcp":         nonNilStrings(pr.MCP),
			"hooks":       nonNilStrings(pr.Hooks),
			"agents":      nonNilStrings(pr.Agents),
			"extras":      nonNilStrings(pr.Extras),
			"plugins":     nonNilStrings(pr.Plugins),
			"launchers":   nonNilStrings(pr.Launchers),
		})
	}

	var catPrompts []map[string]any
	promptIDs := repo.SortedFragmentIDs()
	promptDesc := map[string]string{}
	for idx, fid := range promptIDs {
		f := repo.Fragments[fid]
		promptDesc[fid] = fmt.Sprintf("#%d · %s", idx+1, f.Why)
		if f.Why == "" {
			promptDesc[fid] = fmt.Sprintf("#%d", idx+1)
		}
		catPrompts = append(catPrompts, map[string]any{
			"id":        f.ID,
			"rank":      idx + 1,
			"area":      model.FragmentArea(f.ID),
			"providers": nonNilStrings(f.Providers),
			"scope":     f.Scope,
			"why":       f.Why,
			"since":     f.Since,
			"words":     len(strings.Fields(f.Body)),
			"profiles":  nonNilStrings(fragToProfiles[f.ID]),
			"targets":   nonNilStrings(fragToTargets[f.ID]),
		})
	}

	var lkeys []string
	launchDesc := map[string]string{}
	for k := range repo.Launchers {
		lkeys = append(lkeys, k)
	}
	sort.Strings(lkeys)
	for _, k := range lkeys {
		sp := repo.Launchers[k]
		launchDesc[k] = fmt.Sprintf("liv %s %s", sp.Harness, sp.Profile)
	}

	// Build unified catalog name/desc maps for interactive Matrix (union of Skillshare Catalog + legacy repo MCP/Hooks)
	ssNames := catSS.ItemNamesByCategory()
	skillDesc := map[string]string{}
	for _, rs := range catSS.Skills {
		skillDesc[rs.Name] = rs.Description
	}
	mcpSet := map[string]bool{}
	mcpDesc := map[string]string{}
	for _, m := range catSS.MCP {
		mcpSet[m.Name] = true
		mcpDesc[m.Name] = m.Summary
	}
	for name, m := range repo.MCP {
		if !mcpSet[name] {
			mcpSet[name] = true
			ssNames["mcp"] = append(ssNames["mcp"], name)
			mcpDesc[name] = m.Why
		}
	}
	sort.Strings(ssNames["mcp"])

	hookSet := map[string]bool{}
	hookDesc := map[string]string{}
	for _, h := range catSS.Hooks {
		hookSet[h.Name] = true
		hookDesc[h.Name] = h.Description
	}
	for hid, h := range repo.Hooks {
		if !hookSet[hid] {
			hookSet[hid] = true
			ssNames["hooks"] = append(ssNames["hooks"], hid)
			hookDesc[hid] = h.Why
		}
	}
	sort.Strings(ssNames["hooks"])

	agentDesc := map[string]string{}
	for _, a := range catSS.Agents {
		agentDesc[a.Name] = a.Description
	}
	extraDesc := map[string]string{}
	for _, e := range catSS.Extras {
		extraDesc[e.Name] = e.Summary
	}
	pluginDesc := map[string]string{}
	for _, pl := range catSS.Plugins {
		pluginDesc[pl.Name] = pl.Summary
	}

	matrixCategories := map[string][]model.MatrixRow{
		"profiles":  repo.BuildMatrixRows("profiles", pkeys, profDesc),
		"skills":    repo.BuildMatrixRows("skills", ssNames["skills"], skillDesc),
		"mcp":       repo.BuildMatrixRows("mcp", ssNames["mcp"], mcpDesc),
		"hooks":     repo.BuildMatrixRows("hooks", ssNames["hooks"], hookDesc),
		"extras":    repo.BuildMatrixRows("extras", ssNames["extras"], extraDesc),
		"agents":    repo.BuildMatrixRows("agents", ssNames["agents"], agentDesc),
		"plugins":   repo.BuildMatrixRows("plugins", ssNames["plugins"], pluginDesc),
		"prompts":   repo.BuildMatrixRows("prompts", promptIDs, promptDesc),
		"launchers": repo.BuildMatrixRows("launchers", lkeys, launchDesc),
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"repo": map[string]any{
			"root":                       repo.Root,
			"catalog_dir":                catSS.Dir,
			"version":                    version.Version,
			"dirty":                      len(gitLines) > 0,
			"git_status_count":           len(gitLines),
			"local_skillshare_installed": ssBin != "",
			"local_skillshare_bin":       ssBin,
			"local_skillshare_version":   ssVer,
		},
		"herdr":    herdr.Status(""),
		"onboard":  fleet.ComputeOnboarding(s.repoRoot),
		"targets":  nonNilSliceAny(targetRows),
		"projects": nonNilSliceAny(projectRows),
		"catalog": map[string]any{
			"skillshare":       catSS,
			"skills":           catSS.Skills,
			"prompts":          nonNilSliceAny(catPrompts),
			"prompt_scopes":    model.Scopes,
			"providers":        model.Providers,
			"profiles":         nonNilSliceAny(catProfiles),
			"launchers_count":  len(lkeys),
			"skillshare_modes": model.SkillshareModes,
			"known_targets":    skillshare.KnownTargets,
		},
		"matrix": map[string]any{
			"envs":       repo.BuildMatrixEnvs(),
			"categories": matrixCategories,
		},
	})
}

func (s *Server) buildBundleJSON(repo *model.Repo, t *model.Target) (map[string]any, error) {
	frags, fragSrc := repo.ResolveHostCategoryItems(t, "prompts")
	skills, skillSrc := repo.ResolveHostCategoryItems(t, "skills")
	mcps, _ := repo.ResolveHostCategoryItems(t, "mcp")
	hooks, _ := repo.ResolveHostCategoryItems(t, "hooks")
	agents, _ := repo.ResolveHostCategoryItems(t, "agents")
	extras, _ := repo.ResolveHostCategoryItems(t, "extras")
	plugins, _ := repo.ResolveHostCategoryItems(t, "plugins")
	lids, _ := repo.ResolveHostCategoryItems(t, "launchers")

	promptItems, _ := render.RenderPrompts(repo, t, frags)
	var prompts []map[string]any
	for _, pi := range promptItems {
		prompts = append(prompts, map[string]any{"path": pi.Path, "content": string(pi.Content)})
	}
	fragRanks := repo.FragmentRankMap()
	var promptFrags []map[string]any
	for _, fid := range frags {
		f := repo.Fragments[fid]
		if f == nil {
			continue
		}
		promptFrags = append(promptFrags, map[string]any{
			"id":        f.ID,
			"rank":      fragRanks[f.ID],
			"area":      model.FragmentArea(f.ID),
			"providers": nonNilStrings(f.Providers),
			"scope":     f.Scope,
			"why":       f.Why,
			"words":     len(strings.Fields(f.Body)),
			"source":    fragSrc[fid],
		})
	}

	catSS := skillshare.LoadCatalog(repo.Root)
	catSkillByName := map[string]skillshare.RepoSkill{}
	for _, rs := range catSS.Skills {
		catSkillByName[rs.Name] = rs
	}
	var skillList []map[string]any
	for _, name := range skills {
		rs := catSkillByName[name]
		skillList = append(skillList, map[string]any{
			"name": name, "description": rs.Description, "source": skillSrc[name],
		})
	}

	var launchList []map[string]any
	for _, lid := range lids {
		if sp, ok := repo.Launchers[lid]; ok {
			var srefs []map[string]string
			if drv := launcher.Drivers[sp.Harness]; drv != nil {
				for k, ref := range drv.RequiredSecrets(sp.Raw) {
					srefs = append(srefs, map[string]string{"key": k, "ref": ref})
				}
			}
			mode, _ := sp.Raw["mode"].(string)
			if mode == "" {
				mode = "direct"
			}
			launchList = append(launchList, map[string]any{
				"id":          sp.ID,
				"profile":     sp.Profile,
				"harness":     sp.Harness,
				"command":     fmt.Sprintf("liv %s %s", sp.Harness, sp.Profile),
				"mode":        mode,
				"secret_refs": nonNilSliceMap(srefs),
			})
		}
	}
	var ovList []map[string]any
	for _, ov := range repo.Overlays {
		if ov.Target == t.ID {
			ovList = append(ovList, map[string]any{"slug": ov.Slug, "path": ov.Path, "why": ov.Why, "mcp": nonNilStrings(ov.MCP), "skills": nonNilStrings(ov.Skills)})
		}
	}

	envOv := skillshare.LoadEnvOverride(repo.Root, t)
	enabled := skillshare.EnvEnabledItems{
		Skills: skills, Agents: agents, Extras: extras, MCP: mcps, Hooks: hooks, Plugins: plugins,
	}
	cfgBytes, _, _, _ := skillshare.CompileEnvSkillshareConfig(repo.Root, t, enabled, "~")

	s.mu.RLock()
	pr, hasPr := s.probeCache[t.ID]
	dr, hasDr := s.driftCache[t.ID]
	sy, hasSy := s.syncCache[t.ID]
	un, hasUn := s.unmanagedCache[t.ID]
	s.mu.RUnlock()
	var prObj, drObj, syObj, unObj any
	if hasPr {
		prObj = pr
	}
	if hasDr {
		drObj = dr
	}
	if hasSy {
		syObj = sy
	}
	if hasUn {
		unObj = un
	}

	return map[string]any{
		"id":                     t.ID,
		"ssh":                    t.SSH,
		"is_local":               t.IsLocal(),
		"probe":                  prObj,
		"drift":                  drObj,
		"sync_status":            syObj,
		"unmanaged":              unObj,
		"env_override":           envOv,
		"prompts":                nonNilSliceAny(prompts),
		"prompt_fragments":       nonNilSliceAny(promptFrags),
		"skills":                 nonNilSliceAny(skillList),
		"mcp":                    nonNilStrings(mcps),
		"hooks":                  nonNilStrings(hooks),
		"agents":                 nonNilStrings(agents),
		"extras":                 nonNilStrings(extras),
		"plugins":                nonNilStrings(plugins),
		"launchers":              nonNilSliceAny(launchList),
		"overlays":               nonNilSliceAny(ovList),
		"skillshare_config_yaml": string(cfgBytes),
		"editable": map[string]any{
			"providers":            nonNilStrings(t.Providers),
			"profiles":             nonNilStrings(t.Profiles),
			"add":                  nonNilStrings(t.Add),
			"remove":               nonNilStrings(t.Remove),
			"emit_shims":           t.EmitShims,
			"sync_herdr_programs":  t.SyncHerdrPrograms,
			"sync_herdr_templates": t.SyncHerdrTemplates,
			"sync_t3_providers":    t.SyncT3Providers,
		},
	}, nil
}

func (s *Server) apiTargetBundle(w http.ResponseWriter, tid string) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	b, err := s.buildBundleJSON(repo, t)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) apiTargetConfig(w http.ResponseWriter, r *http.Request, tid string) {
	body := readJSONBody(r)
	patch, ok := body["patch"].(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "patch object required"})
		return
	}
	changed, err := model.UpdateTargetInInventory(s.repoRoot, tid, patch)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.slots.InvalidatePreviewTarget(tid)
	s.invalidateSyncCache(tid)
	s.scheduleSlotSyncCheck("env/" + tid)
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, _ := repo.Target(tid)
	b, err := s.buildBundleJSON(repo, t)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed_keys": changed, "bundle": b})
}

func (s *Server) apiEnsureCatalog(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	restart, _ := body["restart"].(bool)
	info, err := s.slots.EnsureCatalog(restart)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) apiEnsureEnv(w http.ResponseWriter, r *http.Request, tid string) {
	body := readJSONBody(r)
	mode, _ := body["mode"].(string)
	restart, _ := body["restart"].(bool)
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	info, err := s.slots.EnsureEnv(repo, t, mode, restart)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) apiTargetProbe(w http.ResponseWriter, tid string) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	pr := remote.ProbeSSHHost(t.SSH, t.Home)
	s.mu.Lock()
	s.probeCache[t.ID] = pr
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": pr.OK, "probes": map[string]remote.HostProbe{t.ID: pr}})
}

func (s *Server) apiFleetProbe(w http.ResponseWriter) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	probes := map[string]remote.HostProbe{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, t := range repo.Targets {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			pr := remote.ProbeSSHHost(t.SSH, t.Home)
			mu.Lock()
			probes[t.ID] = pr
			mu.Unlock()
		}()
	}
	wg.Wait()
	s.mu.Lock()
	for k, v := range probes {
		s.probeCache[k] = v
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "probes": probes})
}

func (s *Server) apiTargetPlan(w http.ResponseWriter, tid string) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	tp := fleet.PlanTarget(repo, t)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": tp.Error == "",
		"plans": []map[string]any{{
			"target":    t.ID,
			"error":     tp.Error,
			"has_work":  tp.HasWork(),
			"formatted": fleet.FormatPlan(tp),
		}},
	})
}

func (s *Server) apiFleetPlan(w http.ResponseWriter) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	var plans []map[string]any
	allOK := true
	for _, t := range repo.Targets {
		tp := fleet.PlanTarget(repo, t)
		if tp.Error != "" {
			allOK = false
		}
		plans = append(plans, map[string]any{
			"target":    t.ID,
			"error":     tp.Error,
			"has_work":  tp.HasWork(),
			"formatted": fleet.FormatPlan(tp),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": allOK, "plans": plans})
}

func (s *Server) apiTargetSync(w http.ResponseWriter, r *http.Request, tid string) {
	body := readJSONBody(r)
	if conf, _ := body["confirm"].(bool); !conf {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "sync requires confirm: true"})
		return
	}
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	tp := fleet.PlanTarget(repo, t)
	if tp.Error != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": tp.Error})
		return
	}
	if err := fleet.SyncTarget(repo, tp, repo.HeadCommit()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.slots.InvalidatePreviewTarget(tid)
	s.invalidateSyncCache(tid)
	if updatedRepo, err := model.LoadRepo(s.repoRoot); err == nil {
		if ut, err := updatedRepo.Target(tid); err == nil {
			_ = s.computeTargetSyncStatus(updatedRepo, ut)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": fmt.Sprintf("Synced %s (%d actions)", tid, len(tp.Actionable())),
		"plan":    fleet.FormatPlan(tp),
	})
}

func (s *Server) apiTargetPull(w http.ResponseWriter, r *http.Request, tid string) {
	body := readJSONBody(r)
	if conf, _ := body["confirm"].(bool); !conf {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pull requires confirm: true"})
		return
	}
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	res := fleet.PullTarget(repo, t, false, false)
	if !res.OK {
		writeJSON(w, http.StatusBadRequest, res)
		return
	}
	s.slots.InvalidateTarget(tid)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) computeTargetSyncStatus(repo *model.Repo, t *model.Target) SyncStatus {
	var tp *fleet.TargetPlan
	var dr fleet.EnvDriftReport
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		tp = fleet.PlanTarget(repo, t)
	}()
	go func() {
		defer wg.Done()
		dr = fleet.InspectEnvDrift(repo, t)
	}()
	wg.Wait()

	st := SyncStatus{
		Target:          t.ID,
		CheckedAt:       time.Now().Unix(),
		PushChanges:     []fleet.Change{},
		SkillshareLines: []string{},
		Pull:            dr,
	}
	if tp != nil {
		st.PushError = tp.Error
		st.SkillshareNeedsSync = tp.SkillshareNeedsSync
		if tp.SkillshareLines != nil {
			st.SkillshareLines = tp.SkillshareLines
		}
		st.PushFormatted = fleet.FormatPlan(tp)
		for _, c := range tp.Changes {
			if c.Sym != "=" {
				st.PushChanges = append(st.PushChanges, c)
			}
		}
		st.PushCount = len(st.PushChanges)
		if st.PushCount == 0 && tp.SkillshareNeedsSync {
			st.PushCount = 1
		}
	}
	if dr.OK && dr.HasDrift {
		st.PullCount = len(dr.Items)
	}
	st.OutOfSync = st.PushCount > 0 || st.PullCount > 0

	s.mu.Lock()
	s.driftCache[t.ID] = dr
	s.syncCache[t.ID] = st
	s.mu.Unlock()
	return st
}

func (s *Server) apiTargetSyncPreview(w http.ResponseWriter, tid string) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	st := s.computeTargetSyncStatus(repo, t)
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) apiFleetSyncStatus(w http.ResponseWriter) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	results := map[string]SyncStatus{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, t := range repo.Targets {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := s.computeTargetSyncStatus(repo, t)
			mu.Lock()
			results[t.ID] = st
			mu.Unlock()
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "statuses": results})
}

func (s *Server) apiTargetDrift(w http.ResponseWriter, tid string) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	rep := fleet.InspectEnvDrift(repo, t)
	s.mu.Lock()
	s.driftCache[t.ID] = rep
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) apiFleetDrift(w http.ResponseWriter) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	results := map[string]fleet.EnvDriftReport{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, t := range repo.Targets {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := fleet.InspectEnvDrift(repo, t)
			mu.Lock()
			results[t.ID] = r
			mu.Unlock()
		}()
	}
	wg.Wait()
	s.mu.Lock()
	for k, v := range results {
		s.driftCache[k] = v
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "drifts": results})
}

func (s *Server) apiFleetSync(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	if conf, _ := body["confirm"].(bool); !conf {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "fleet sync requires confirm: true"})
		return
	}
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	commit := repo.HeadCommit()
	var results []map[string]any
	allOK := true
	for _, t := range repo.Targets {
		tp := fleet.PlanTarget(repo, t)
		if tp.Error != "" {
			allOK = false
			results = append(results, map[string]any{"target": t.ID, "ok": false, "error": tp.Error})
			continue
		}
		if err := fleet.SyncTarget(repo, tp, commit); err != nil {
			allOK = false
			results = append(results, map[string]any{"target": t.ID, "ok": false, "error": err.Error()})
			continue
		}
		s.mu.Lock()
		delete(s.driftCache, t.ID)
		s.mu.Unlock()
		results = append(results, map[string]any{"target": t.ID, "ok": true, "actions": len(tp.Actionable())})
	}
	s.slots.InvalidatePreviewEnvs()
	s.invalidateSyncCache("")
	writeJSON(w, http.StatusOK, map[string]any{"ok": allOK, "results": results})
}

func (s *Server) apiTargetBackwardSync(w http.ResponseWriter, r *http.Request, tid string) {
	var payload struct {
		Confirm     bool                     `json:"confirm"`
		SkipRefresh bool                     `json:"skip_refresh"`
		Decisions   []fleet.BackwardDecision `json:"decisions"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err := json.Unmarshal(b, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()})
		return
	}
	if !payload.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "backward-sync requires confirm: true"})
		return
	}
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	rep := fleet.ApplyBackwardSync(repo, t, payload.Decisions)
	s.slots.InvalidatePreviewTarget(tid)
	s.invalidateSyncCache(tid)
	// Refresh sync + drift cache after applying backward sync (skip if a push immediately follows)
	if !payload.SkipRefresh {
		if updatedRepo, err := model.LoadRepo(s.repoRoot); err == nil {
			if ut, err := updatedRepo.Target(tid); err == nil {
				_ = s.computeTargetSyncStatus(updatedRepo, ut)
			}
		}
	}
	code := http.StatusOK
	if rep.Error != "" {
		code = http.StatusBadRequest
	} else if len(rep.Conflicts) > 0 {
		code = http.StatusConflict
	}
	writeJSON(w, code, rep)
}

func (s *Server) apiTargetEnvOverride(w http.ResponseWriter, r *http.Request, tid string) {
	var payload struct {
		Mode    string   `json:"mode"`
		Targets []string `json:"targets"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	ov := skillshare.LoadEnvOverride(repo.Root, t)
	if payload.Mode != "" {
		ov.Mode = payload.Mode
	}
	if payload.Targets != nil {
		next := map[string]any{}
		for _, tn := range payload.Targets {
			tn = strings.TrimSpace(tn)
			if tn == "" {
				continue
			}
			if existing, ok := ov.Targets[tn]; ok {
				next[tn] = existing
			} else if tn == "codex" {
				next[tn] = map[string]any{"skills": map[string]any{"path": "~/.codex/skills"}}
			} else {
				next[tn] = map[string]any{}
			}
		}
		ov.Targets = next
	}
	if err := skillshare.SaveEnvOverride(repo.Root, tid, ov); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.slots.InvalidatePreviewTarget(tid)
	s.invalidateSyncCache(tid)
	s.scheduleSlotSyncCheck("env/" + tid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "env_override": ov})
}

func (s *Server) apiTargetUnmanaged(w http.ResponseWriter, tid string) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	rep := fleet.InspectHostUnmanaged(repo, t)
	s.mu.Lock()
	s.unmanagedCache[t.ID] = rep
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) apiTargetUnmanagedApply(w http.ResponseWriter, r *http.Request, tid string) {
	var payload struct {
		Confirm   bool                      `json:"confirm"`
		Decisions []fleet.UnmanagedDecision `json:"decisions"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err := json.Unmarshal(b, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()})
		return
	}
	if !payload.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unmanaged/apply requires confirm: true"})
		return
	}
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	rep := fleet.ImportHostUnmanaged(repo, t, payload.Decisions)
	if rep.Error != "" {
		writeJSON(w, http.StatusBadRequest, rep)
		return
	}
	// Re-scan unmanaged state and schedule sync check so topbar Sync lights up immediately
	if updatedRepo, err := model.LoadRepo(s.repoRoot); err == nil {
		if ut, err := updatedRepo.Target(tid); err == nil {
			fresh := fleet.InspectHostUnmanaged(updatedRepo, ut)
			s.mu.Lock()
			s.unmanagedCache[tid] = fresh
			s.mu.Unlock()
		}
	}
	s.slots.InvalidatePreviewTarget(tid)
	s.invalidateSyncCache(tid)
	s.scheduleSlotSyncCheck("env/" + tid)
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) apiTargetAudit(w http.ResponseWriter, tid string) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	rep := skillshare.RunAudit(repo, tid, "", "", "")
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) apiFleetAudit(w http.ResponseWriter) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	rep := skillshare.RunAudit(repo, "", "", "", "")
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) apiTargetDoctor(w http.ResponseWriter, tid string) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	out, ok := fleet.RunDoctorReport(repo, []*model.Target{t})
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "output": out})
}

func (s *Server) apiFleetDoctor(w http.ResponseWriter) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out, ok := fleet.RunDoctorReport(repo, repo.Targets)
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "output": out})
}

func (s *Server) apiTargetSecrets(w http.ResponseWriter, tid string) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	rows := fleet.CheckSecretsStatus(repo, []*model.Target{t})
	ok := len(rows) > 0 && rows[0].OK
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "targets": rows})
}

func (s *Server) apiFleetSecrets(w http.ResponseWriter) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	rows := fleet.CheckSecretsStatus(repo, repo.Targets)
	allOK := true
	for _, r := range rows {
		if !r.OK {
			allOK = false
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": allOK, "targets": rows})
}

func (s *Server) apiTargetInstallSkillshare(w http.ResponseWriter, r *http.Request, tid string) {
	body := readJSONBody(r)
	if conf, _ := body["confirm"].(bool); !conf {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "install-skillshare requires confirm: true"})
		return
	}
	method, _ := body["method"].(string)
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	res := remote.InstallSkillshare(t, method)
	pr := remote.ProbeSSHHost(t.SSH, t.Home)
	s.mu.Lock()
	s.probeCache[t.ID] = pr
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) apiTargetBootstrapLiv(w http.ResponseWriter, r *http.Request, tid string) {
	body := readJSONBody(r)
	if conf, _ := body["confirm"].(bool); !conf {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bootstrap-liv requires confirm: true"})
		return
	}
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	t, err := repo.Target(tid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	res, err := remote.BootstrapLiv(s.repoRoot, t)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	pr := remote.ProbeSSHHost(t.SSH, t.Home)
	s.mu.Lock()
	s.probeCache[t.ID] = pr
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) apiTargetRemove(w http.ResponseWriter, r *http.Request, tid string) {
	body := readJSONBody(r)
	if conf, _ := body["confirm"].(bool); !conf {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "remove requires confirm: true"})
		return
	}
	if err := model.RemoveTargetFromInventory(s.repoRoot, tid); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.slots.InvalidateTarget(tid)
	s.mu.Lock()
	delete(s.probeCache, tid)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": tid})
}

func (s *Server) apiEnvDiscover(w http.ResponseWriter) {
	repo, _ := model.LoadRepo(s.repoRoot)
	hosts := fleet.DiscoverEnvironments(repo)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hosts": hosts})
}

func (s *Server) apiEnvProbe(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	sshTarget := strings.TrimSpace(fmt.Sprint(body["ssh"]))
	suggestedID := strings.TrimSpace(fmt.Sprint(body["id"]))
	if sshTarget == "" || sshTarget == "<nil>" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ssh target is required"})
		return
	}
	if suggestedID == "<nil>" {
		suggestedID = ""
	}
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	spec, pr := fleet.SuggestTargetFromProbe(repo, sshTarget, suggestedID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": pr.OK, "probe": pr, "suggested": spec})
}

func (s *Server) apiEnvAdd(w http.ResponseWriter, r *http.Request) {
	var spec model.NewTargetSpec
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &spec); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid target JSON: " + err.Error()})
		return
	}
	if err := model.AddTargetToInventory(s.repoRoot, spec); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if repo, err := model.LoadRepo(s.repoRoot); err == nil {
		if t, err := repo.Target(spec.ID); err == nil {
			_ = skillshare.SaveEnvOverride(s.repoRoot, t.ID, skillshare.LoadEnvOverride(s.repoRoot, t))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": spec.ID})
}

func (s *Server) apiHerdrInstallMod(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	symlink, _ := body["symlink"].(bool)
	st, err := herdr.InstallMod(herdr.InstallOptions{
		RepoRoot:    s.repoRoot,
		Symlink:     symlink,
		CompatAlias: true,
		UpdateKeys:  true,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": st})
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilSliceAny(s []map[string]any) []map[string]any {
	if s == nil {
		return []map[string]any{}
	}
	return s
}

func nonNilSliceMap(s []map[string]string) []map[string]string {
	if s == nil {
		return []map[string]string{}
	}
	return s
}
