package ui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bowd/livery/internal/skillshare"
)

func copyRepoToTemp(t *testing.T) string {
	t.Helper()
	srcRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "demo-repo"))
	if err != nil {
		t.Fatalf("demo-repo path: %v", err)
	}
	dst := t.TempDir()
	for _, entry := range []string{"inventory.toml", "catalog", "profiles", "prompts", "skills", "skillshare", "mcp", "hooks", "launchers", "projects", "provenance"} {
		src := filepath.Join(srcRoot, entry)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == ".git" || d.Name() == "__pycache__") {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(src, p)
			target := filepath.Join(dst, entry)
			if rel != "." {
				target = filepath.Join(target, rel)
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
		if err != nil {
			t.Fatalf("copy %s: %v", entry, err)
		}
	}
	return dst
}

func startTestServer(t *testing.T, repoRoot string) *Server {
	t.Helper()
	srv, err := NewServer(repoRoot, "127.0.0.1", 0)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func doReq(t *testing.T, method, url, csrf, origin string, body any) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set("X-Livery-CSRF", csrf)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func TestMetaUIEndpointsSecurityAndEnvCRUD(t *testing.T) {
	root := copyRepoToTemp(t)
	srv := startTestServer(t, root)
	base := srv.URL()
	csrf := srv.CSRFToken()

	// 1. Index injects CSRF token, and SPA routes (including deep Skillshare sub-routes) + favicon serve cleanly on refresh
	for _, path := range []string{"/", "/environments", "/catalog/prompts", "/catalog/skillshare/skills", "/matrix/profiles", "/env/ops@server/skillshare", "/env/ops@server/skillshare/targets"} {
		resp, html := doReq(t, "GET", base+path, "", "", nil)
		if resp.StatusCode != 200 || !strings.Contains(string(html), csrf) {
			t.Fatalf("GET %s status=%d, missing CSRF injection", path, resp.StatusCode)
		}
	}
	resp, _ := doReq(t, "GET", base+"/favicon.ico", "", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/favicon.ico status=%d", resp.StatusCode)
	}

	// 2. State endpoint returns targets, catalog, matrix
	resp, body := doReq(t, "GET", base+"/api/state", "", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/api/state status=%d body=%s", resp.StatusCode, body)
	}
	var st map[string]any
	_ = json.Unmarshal(body, &st)
	if targets, _ := st["targets"].([]any); len(targets) == 0 {
		t.Fatalf("expected non-empty targets")
	}

	// 3. CSRF and Origin guards on POST
	resp, _ = doReq(t, "POST", base+"/api/targets/ops@server/sync", "", "", map[string]any{"confirm": true})
	if resp.StatusCode != 403 {
		t.Fatalf("missing CSRF expected 403, got %d", resp.StatusCode)
	}
	resp, _ = doReq(t, "POST", base+"/api/targets/ops@server/sync", csrf, "http://evil.example", map[string]any{"confirm": true})
	if resp.StatusCode != 403 {
		t.Fatalf("bad Origin expected 403, got %d", resp.StatusCode)
	}
	resp, _ = doReq(t, "POST", base+"/api/targets/ops@server/sync", csrf, base, map[string]any{})
	if resp.StatusCode != 400 {
		t.Fatalf("missing confirm:true expected 400, got %d", resp.StatusCode)
	}

	// 4. Bundle & Config edit
	resp, body = doReq(t, "GET", base+"/api/targets/ops@server/bundle", "", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("bundle status=%d", resp.StatusCode)
	}
	resp, body = doReq(t, "POST", base+"/api/targets/ops@server/config", csrf, base, map[string]any{
		"patch": map[string]any{"skillshare_targets": []string{"claude", "codex", "opencode"}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("config edit status=%d body=%s", resp.StatusCode, body)
	}

	// 5. Discover + Add + Remove environment
	resp, _ = doReq(t, "GET", base+"/api/env/discover", "", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("discover status=%d", resp.StatusCode)
	}
	resp, body = doReq(t, "POST", base+"/api/env/add", csrf, base, map[string]any{
		"id":                 "ci@testbox",
		"ssh":                "ci@testbox",
		"providers":          []string{"claude", "codex"},
		"profiles":           []string{"base"},
		"skillshare_targets": []string{"claude", "codex"},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("env add status=%d body=%s", resp.StatusCode, body)
	}
	resp, _ = doReq(t, "POST", base+"/api/targets/ci@testbox/remove", csrf, base, map[string]any{})
	if resp.StatusCode != 400 {
		t.Fatalf("remove without confirm expected 400, got %d", resp.StatusCode)
	}
	resp, body = doReq(t, "POST", base+"/api/targets/ci@testbox/remove", csrf, base, map[string]any{"confirm": true})
	if resp.StatusCode != 200 {
		t.Fatalf("remove status=%d body=%s", resp.StatusCode, body)
	}

	// 6. Onboard, Profiles, Groups & Apps APIs
	resp, _ = doReq(t, "GET", base+"/api/onboard", "", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/api/onboard status=%d", resp.StatusCode)
	}
	resp, _ = doReq(t, "GET", base+"/api/profiles", "", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/api/profiles status=%d", resp.StatusCode)
	}
	resp, _ = doReq(t, "GET", base+"/api/apps", "", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/api/apps status=%d", resp.StatusCode)
	}
	resp, body = doReq(t, "POST", base+"/api/profiles/save", csrf, base, map[string]any{
		"harness": "openclaw", "profile": "lab", "mode": "shadow", "auth_mode": "profile", "session_group": "work",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("profiles/save status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = doReq(t, "POST", base+"/api/groups/set", csrf, base, map[string]any{"id": "openclaw-lab", "group": "personal"})
	if resp.StatusCode != 200 {
		t.Fatalf("groups/set status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = doReq(t, "POST", base+"/api/profiles/delete", csrf, base, map[string]any{"id": "openclaw-lab", "confirm": true})
	if resp.StatusCode != 200 {
		t.Fatalf("profiles/delete status=%d body=%s", resp.StatusCode, body)
	}

	// 7. Prompts list + save + update profile attachment + delete
	resp, body = doReq(t, "GET", base+"/api/prompts", "", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/api/prompts status=%d body=%s", resp.StatusCode, body)
	}
	var pRes map[string]any
	_ = json.Unmarshal(body, &pRes)
	if frags, _ := pRes["fragments"].([]any); len(frags) == 0 {
		t.Fatalf("expected non-empty prompt fragments")
	}
	resp, body = doReq(t, "POST", base+"/api/prompts/save", csrf, base, map[string]any{
		"id":              "style/test-fragment",
		"providers":       []string{"claude", "codex"},
		"scope":           "user",
		"why":             "unit test fragment",
		"body":            "## Test\n\n- Keep it clean.",
		"profiles":        []string{"workstation"},
		"update_profiles": true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("/api/prompts/save status=%d body=%s", resp.StatusCode, body)
	}
	resp, _ = doReq(t, "POST", base+"/api/prompts/delete", csrf, base, map[string]any{"id": "style/test-fragment"})
	if resp.StatusCode != 400 {
		t.Fatalf("prompts/delete without confirm expected 400, got %d", resp.StatusCode)
	}
	resp, body = doReq(t, "POST", base+"/api/prompts/delete", csrf, base, map[string]any{"id": "style/test-fragment", "confirm": true})
	if resp.StatusCode != 200 {
		t.Fatalf("/api/prompts/delete status=%d body=%s", resp.StatusCode, body)
	}

	// 8. Interactive Matrix cell toggle, 409 Profile guardrail, and Select All row toggle
	resp, body = doReq(t, "POST", base+"/api/matrix/cell", csrf, base, map[string]any{
		"category": "skills", "item": "beta", "env": "ops@server", "enabled": true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("matrix cell enable status=%d body=%s", resp.StatusCode, body)
	}
	// Attempting to disable prompt inherited from `base` must return 409 Conflict!
	resp, body = doReq(t, "POST", base+"/api/matrix/cell", csrf, base, map[string]any{
		"category": "prompts", "item": "identity/owner", "env": "ops@server", "enabled": false,
	})
	if resp.StatusCode != 409 {
		t.Fatalf("expected 409 Conflict for profile-inherited prompt, got %d body=%s", resp.StatusCode, body)
	}
	resp, body = doReq(t, "POST", base+"/api/matrix/row", csrf, base, map[string]any{
		"category": "skills", "item": "beta", "enable_all": true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("matrix row select-all status=%d body=%s", resp.StatusCode, body)
	}

	// 9. Profile bundle create + delete via API
	resp, body = doReq(t, "POST", base+"/api/profile-bundles/save", csrf, base, map[string]any{
		"id": "qa-bundle", "description": "QA", "skills": []string{"alpha"}, "fragments": []string{"style/concise"},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("profile-bundles/save status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = doReq(t, "POST", base+"/api/profile-bundles/delete", csrf, base, map[string]any{
		"id": "qa-bundle", "confirm": true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("profile-bundles/delete status=%d body=%s", resp.StatusCode, body)
	}
}

func TestLiveBlackBoxSkillshareUIReverseProxy(t *testing.T) {
	if skillshare.FindLocalBinary("") == "" {
		t.Skip("stock skillshare binary not installed")
	}
	root := copyRepoToTemp(t)
	srv := startTestServer(t, root)
	base := srv.URL()
	csrf := srv.CSRFToken()

	// 1. Standalone Full Catalog slot: overview.configDir rewritten to "catalog/skillshare (Full Library)"
	//    and isolated inside its own .git repo so Skillshare Git Sync never sees parent files!
	resp, body := doReq(t, "POST", base+"/api/ss/catalog/ensure", csrf, base, map[string]any{})
	if resp.StatusCode != 200 {
		t.Fatalf("ensure catalog failed: %d %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(root, "catalog", "skillshare", ".git")); err != nil {
		t.Fatalf("expected standalone .git inside catalog/skillshare: %v", err)
	}
	resp, body = doReq(t, "GET", base+"/ss/catalog/api/overview", "", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("catalog overview status=%d body=%s", resp.StatusCode, body)
	}
	var ov map[string]any
	if err := json.Unmarshal(body, &ov); err != nil {
		t.Fatalf("invalid overview JSON: %v", err)
	}
	if ov["configDir"] != "catalog/skillshare (Full Library)" {
		t.Fatalf("catalog configDir = %v, want catalog/skillshare (Full Library)", ov["configDir"])
	}
	// Verify deep inner Skillshare SPA routes (e.g. /ss/catalog/skills) serve 200 directly for refresh restoration!
	resp, _ = doReq(t, "GET", base+"/ss/catalog/skills", "", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("deep skillshare route /ss/catalog/skills status=%d, want 200", resp.StatusCode)
	}

	// 2. Compiled remote preview slot (preview mode) + /api/mcp 200 + read-only 403 guard on preview
	for _, tid := range []string{"dev@macbook", "ops@server"} {
		resp, body = doReq(t, "POST", base+"/api/ss/env/"+tid+"/ensure", csrf, base, map[string]any{"mode": "preview"})
		if resp.StatusCode != 200 {
			t.Fatalf("ensure env %s failed: %d %s", tid, resp.StatusCode, body)
		}
		resp, body = doReq(t, "GET", base+"/ss/env/"+tid+"/api/overview", "", "", nil)
		if resp.StatusCode != 200 {
			t.Fatalf("env %s overview status=%d body=%s", tid, resp.StatusCode, body)
		}
		var envOv map[string]any
		_ = json.Unmarshal(body, &envOv)
		wantLabel := tid + " (compiled preview)"
		if envOv["configDir"] != wantLabel {
			t.Fatalf("env %s configDir = %v, want %q", tid, envOv["configDir"], wantLabel)
		}
		resp, _ = doReq(t, "GET", base+"/ss/env/"+tid+"/api/mcp", "", base, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("env %s /api/mcp status=%d, want 200", tid, resp.StatusCode)
		}
		resp, _ = doReq(t, "POST", base+"/ss/env/"+tid+"/api/sync", csrf, base, map[string]any{})
		if resp.StatusCode != 403 {
			t.Fatalf("env %s preview guard expected 403, got %d", tid, resp.StatusCode)
		}
	}

	// 3. Unreachable remote SSH target in auto mode gracefully falls back to Read-Only preview with explicit fallback_reason!
	resp, body = doReq(t, "POST", base+"/api/ss/env/ops@server/ensure", csrf, base, map[string]any{"mode": "auto", "restart": true})
	if resp.StatusCode != 200 {
		t.Fatalf("unreachable remote fallback status=%d body=%s", resp.StatusCode, body)
	}
	var fb SlotInfo
	if err := json.Unmarshal(body, &fb); err != nil {
		t.Fatalf("decode fallback SlotInfo: %v", err)
	}
	if fb.Mode != "preview" || !fb.ReadOnly || !fb.Remote || fb.FallbackReason == "" {
		t.Fatalf("expected preview+readonly+remote+fallback_reason for unreachable host, got %+v", fb)
	}
}

func TestSSEEventsAndPreviewInvalidation(t *testing.T) {
	root := copyRepoToTemp(t)
	srv := startTestServer(t, root)
	base := srv.URL()

	// 1. Subscribe to /api/events and verify slot-activity event arrives immediately on mutation
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/api/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	sc := bufio.NewScanner(resp.Body)
	// First line should be ": connected"
	if !sc.Scan() || !strings.HasPrefix(sc.Text(), ": connected") {
		t.Fatalf("expected initial SSE connected comment, got %q (err=%v)", sc.Text(), sc.Err())
	}

	// Trigger a slot mutation via onMutation callback (same path ServeProxy uses)
	srv.slots.onMutation("env/dev@macbook")

	gotEvent := ""
	gotData := ""
	curEvent := ""
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			curEvent = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") && curEvent == "slot-activity" {
			gotEvent = curEvent
			gotData = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if gotEvent != "slot-activity" || !strings.Contains(gotData, `"target":"dev@macbook"`) {
		t.Fatalf("SSE event=%q data=%q, want slot-activity for dev@macbook", gotEvent, gotData)
	}

	// 2. Verify InvalidatePreviewTarget stops preview slots but keeps live slots intact
	srv.slots.mu.Lock()
	srv.slots.slots["env/live-host"] = &slotProc{info: SlotInfo{Mode: "live"}}
	srv.slots.slots["env/preview-host"] = &slotProc{info: SlotInfo{Mode: "preview"}}
	srv.slots.mu.Unlock()

	srv.slots.InvalidatePreviewTarget("live-host")
	srv.slots.InvalidatePreviewTarget("preview-host")

	if srv.slots.Get("env/live-host") == nil {
		t.Fatalf("InvalidatePreviewTarget stopped live slot")
	}
	if srv.slots.Get("env/preview-host") != nil {
		t.Fatalf("InvalidatePreviewTarget did not stop preview slot")
	}
}

func TestStructuredGitDiffCommitAndDiscard(t *testing.T) {
	raw := `diff --git a/inventory.toml b/inventory.toml
index 9ab3be2..43fac82 100644
--- a/inventory.toml
+++ b/inventory.toml
@@ -42,3 +42,4 @@ [[target]]
 id = "dev@macbook"
-emit_shims = true
+emit_shims = false
+sync_t3_providers = true
`
	files := parseUnifiedDiff(raw)
	fc, ok := files["inventory.toml"]
	if !ok {
		t.Fatalf("expected inventory.toml in parsed diff: %+v", files)
	}
	if fc.Additions != 2 || fc.Deletions != 1 || len(fc.Hunks) != 1 {
		t.Fatalf("unexpected diff stats: +%d -%d hunks=%d", fc.Additions, fc.Deletions, len(fc.Hunks))
	}
	if fc.Hunks[0].Lines[1].Kind != "del" || fc.Hunks[0].Lines[1].OldNum != 43 {
		t.Fatalf("unexpected del line: %+v", fc.Hunks[0].Lines[1])
	}
	if fc.Hunks[0].Lines[2].Kind != "add" || fc.Hunks[0].Lines[2].NewNum != 43 {
		t.Fatalf("unexpected add line: %+v", fc.Hunks[0].Lines[2])
	}
}
