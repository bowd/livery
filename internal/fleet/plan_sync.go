package fleet

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bowd/livery/internal/herdr"
	"github.com/bowd/livery/internal/launcher"
	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/remote"
	"github.com/bowd/livery/internal/render"
	"github.com/bowd/livery/internal/secrets"
	"github.com/bowd/livery/internal/skillshare"
)

type Change struct {
	Sym    string           `json:"sym"` // + ~ - ! =
	Path   string           `json:"path"`
	Item   *render.Item     `json:"-"`
	Kind   string           `json:"kind"`
	Role   string           `json:"role,omitempty"`
	Note   string           `json:"note,omitempty"`
	Backup bool             `json:"backup,omitempty"`
	Lock   *model.LockEntry `json:"-"`
}

type TargetPlan struct {
	Target              *model.Target
	Rendered            *render.Rendered
	Changes             []Change
	Home                string
	Error               string
	SkillshareReport    map[string]any
	SkillshareLines     []string
	SkillshareNeedsSync bool
}

func (tp *TargetPlan) By(sym string) []Change {
	var out []Change
	for _, c := range tp.Changes {
		if c.Sym == sym {
			out = append(out, c)
		}
	}
	return out
}

func (tp *TargetPlan) Actionable() []Change {
	var out []Change
	for _, c := range tp.Changes {
		if c.Sym == "+" || c.Sym == "~" || c.Sym == "-" {
			out = append(out, c)
		}
	}
	return out
}

func (tp *TargetPlan) HasWork() bool {
	return len(tp.Actionable()) > 0 || tp.SkillshareNeedsSync
}

func PlanTarget(repo *model.Repo, t *model.Target) *TargetPlan {
	tp := &TargetPlan{Target: t}
	rend, err := render.RenderTarget(repo, t)
	if err != nil {
		tp.Error = err.Error()
		return tp
	}
	tp.Rendered = rend

	lockAll, err := model.LoadLock(repo.Root)
	if err != nil {
		tp.Error = err.Error()
		return tp
	}
	lockMap := map[string]model.LockEntry{}
	if lt := lockAll[t.ID]; lt != nil {
		for _, le := range lt.Items {
			lockMap[le.Path] = le
		}
	}

	pathSet := map[string]bool{}
	var paths []string
	for _, it := range rend.Items {
		if !it.IsEntry() && !pathSet[it.Path] {
			pathSet[it.Path] = true
			paths = append(paths, it.Path)
		}
	}
	for p, le := range lockMap {
		if !isEntryKind(le.Kind) && !pathSet[p] {
			pathSet[p] = true
			paths = append(paths, p)
		}
	}

	resp, err := remote.RunAgent(t, remote.AgentRequest{
		Op:        "state",
		Paths:     paths,
		SkillDirs: t.ProviderSkillDirs,
	}, 60*time.Second)
	if err != nil {
		tp.Error = err.Error()
		return tp
	}
	if !resp.OK {
		tp.Error = resp.Error
		return tp
	}
	tp.Home = resp.Home
	render.FinalizeOverlayMCP(rend.Items, tp.Home)
	render.FinalizeSkillshareConfig(repo, t, rend.Items, tp.Home)

	livePaths := map[string]remote.LiveEntry{}
	if rawPaths, err := json.Marshal(resp.Result["paths"]); err == nil {
		_ = json.Unmarshal(rawPaths, &livePaths)
	}

	// Normalize lock paths (expand/collapse ~/ consistently so /Users/.../Workspace/music matches ~/Workspace/music)
	normLock := map[string]model.LockEntry{}
	for p, le := range lockMap {
		normLock[collapseHome(p, tp.Home)] = le
	}

	wanted := map[string]bool{}
	for _, it := range rend.Items {
		if it.IsEntry() {
			continue
		}
		normP := collapseHome(it.Path, tp.Home)
		wanted[it.Path] = true
		wanted[normP] = true
		live := livePaths[it.Path]
		_, locked := lockMap[it.Path]
		if !locked {
			_, locked = normLock[normP]
		}
		sym, note, backup := diffItem(it, live, locked, tp.Home, t.Adopt)
		tp.Changes = append(tp.Changes, Change{
			Sym: sym, Path: it.Path, Item: it, Kind: it.Kind, Role: it.Role, Note: note, Backup: backup,
		})
	}

	// Removals for previously locked items no longer rendered (e.g. retired per-profile shims!)
	// Never retire skills explicitly preserved in env-overrides/<host>.yaml local_skills.
	ov := skillshare.LoadEnvOverride(repo.Root, t)
	localSkillPaths := map[string]bool{}
	sroot := strings.TrimRight(t.SkillshareRoot, "/")
	for _, ls := range ov.LocalSkills {
		localSkillPaths[sroot+"/skills/"+ls] = true
		localSkillPaths[collapseHome(sroot+"/skills/"+ls, tp.Home)] = true
	}
	var orphanPaths []string
	for p := range lockMap {
		if localSkillPaths[p] || localSkillPaths[collapseHome(p, tp.Home)] {
			continue
		}
		if !wanted[p] && !wanted[collapseHome(p, tp.Home)] && !isEntryKind(lockMap[p].Kind) {
			orphanPaths = append(orphanPaths, p)
		}
	}
	sort.Strings(orphanPaths)
	for _, p := range orphanPaths {
		le := lockMap[p]
		live := livePaths[p]
		if live.Type == "missing" {
			continue
		}
		tp.Changes = append(tp.Changes, Change{
			Sym: "-", Path: p, Kind: le.Kind, Role: le.Role, Note: "retired (was managed)", Backup: true, Lock: &le,
		})
	}

	cfgPath := strings.TrimRight(t.SkillshareRoot, "/") + "/config.yaml"
	ssResp, err := remote.RunAgent(t, remote.AgentRequest{
		Op:            "skillshare_sync",
		SkillshareBin: t.SkillshareBin,
		ConfigPath:    cfgPath,
		DryRun:        true,
	}, 120*time.Second)
	if err != nil {
		tp.SkillshareLines = []string{"ERROR: " + err.Error()}
	} else {
		tp.SkillshareReport = ssResp.Result
		tp.SkillshareNeedsSync, tp.SkillshareLines = SummarizeSkillshare(ssResp.Result)
	}

	_, _ = render.WriteBuild(repo, t, rend.Items)
	return tp
}

func isEntryKind(k string) bool {
	for _, ek := range model.EntryKinds {
		if ek == k {
			return true
		}
	}
	return false
}

func collapseHome(p, home string) string {
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

func diffItem(it *render.Item, live remote.LiveEntry, locked bool, home string, adopt []string) (sym, note string, backup bool) {
	if live.Type == "missing" {
		return "+", "", false
	}
	wantSHA := it.SHA()
	if it.Kind == "file" && live.Type == "file" && live.SHA == wantSHA {
		return "=", "", false
	}
	if it.Kind == "tree" && live.Type == "dir" && live.SHA == wantSHA {
		return "=", "", false
	}
	if it.Kind == "link" && live.Type == "link" {
		if collapseHome(live.Target, home) == collapseHome(it.Target, home) {
			return "=", "", false
		}
	}
	adopted := false
	for _, a := range adopt {
		if a == it.Path || (it.Skill != "" && a == "skill:"+it.Skill) {
			adopted = true
			break
		}
	}
	if locked || adopted {
		if adopted && !locked {
			return "~", "adopt (will back up)", true
		}
		return "~", "differs", true
	}
	return "!", "unmanaged path already exists (add to adopt to take over)", false
}

func SummarizeSkillshare(res map[string]any) (bool, []string) {
	if res == nil {
		return false, nil
	}
	if inst, _ := res["installed"].(bool); !inst {
		return false, []string{"MISSING binary: " + fmt.Sprint(res["error"])}
	}
	ver := fmt.Sprint(res["version"])
	if ver == "" {
		ver = "skillshare"
	}
	if miss, _ := res["config_missing"].(bool); miss {
		return true, []string{ver + ": bundle not yet synced; sync will initialize skillshare"}
	}
	needs := false
	var parts []string
	var details []string
	if diff, ok := res["diff"].(map[string]any); ok {
		if targets, ok := diff["targets"].([]any); ok {
			for _, raw := range targets {
				td, _ := raw.(map[string]any)
				if td == nil {
					continue
				}
				name := fmt.Sprint(td["name"])
				synced, _ := td["synced"].(bool)
				items, _ := td["items"].([]any)
				var pending []map[string]any
				for _, ri := range items {
					if m, ok := ri.(map[string]any); ok {
						if isSync, has := m["is_sync"].(bool); !has || isSync {
							pending = append(pending, m)
						}
					}
				}
				if synced && len(pending) == 0 {
					parts = append(parts, name+"(synced)")
				} else {
					needs = true
					parts = append(parts, fmt.Sprintf("%s(%d change)", name, len(pending)))
					for i, m := range pending {
						if i >= 6 {
							break
						}
						details = append(details, fmt.Sprintf("  %s [%s] %s", m["action"], name, m["name"]))
					}
				}
			}
		}
	}
	if pl, ok := res["plugins"].(map[string]any); ok {
		if changes, ok := pl["changes"].([]any); ok {
			var pendingPl []map[string]any
			for _, rc := range changes {
				m, ok := rc.(map[string]any)
				if !ok {
					continue
				}
				act := strings.ToLower(strings.TrimSpace(fmt.Sprint(m["action"])))
				if act == "" || act == "noop" || act == "unchanged" || act == "none" {
					continue
				}
				pendingPl = append(pendingPl, m)
			}
			if len(pendingPl) > 0 {
				needs = true
				parts = append(parts, fmt.Sprintf("plugins(%d change)", len(pendingPl)))
				for i, m := range pendingPl {
					if i >= 6 {
						break
					}
					line := fmt.Sprintf("  plugin %s [%s] %s", m["action"], m["target"], m["name"])
					if msg, _ := m["message"].(string); msg != "" {
						line += " — " + msg
					}
					details = append(details, line)
				}
			}
		}
	}
	head := ver + ": " + strings.Join(parts, "; ")
	if len(parts) == 0 {
		head = ver + ": ok"
	}
	return needs, append([]string{head}, details...)
}

func FormatPlan(tp *TargetPlan) string {
	where := tp.Target.SSH
	if tp.Target.IsLocal() {
		where = "local"
	}
	lines := []string{fmt.Sprintf("== %s (%s)", tp.Target.ID, where)}
	if tp.Error != "" {
		lines = append(lines, "  ERROR: "+tp.Error)
		return strings.Join(lines, "\n")
	}
	for _, c := range tp.Changes {
		if c.Sym == "=" {
			continue
		}
		note := ""
		if c.Note != "" {
			note = "  (" + c.Note + ")"
		}
		lines = append(lines, fmt.Sprintf("  %s %-5s %s%s", c.Sym, c.Kind, c.Path, note))
	}
	for _, sl := range tp.SkillshareLines {
		lines = append(lines, "  [skillshare] "+sl)
	}
	if !tp.HasWork() && len(tp.By("!")) == 0 {
		lines = append(lines, fmt.Sprintf("  in sync (%d managed items)", len(tp.By("="))))
	}
	return strings.Join(lines, "\n")
}

func SyncTarget(repo *model.Repo, tp *TargetPlan, commit string) error {
	if tp.Error != "" {
		return model.Errf("%s: %s", tp.Target.ID, tp.Error)
	}
	if len(tp.By("!")) > 0 {
		return model.Errf("%s: %d blocked conflict(s)", tp.Target.ID, len(tp.By("!")))
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	var pre []remote.ApplyStep
	var trees []Change
	var post []remote.ApplyStep

	for _, c := range tp.Changes {
		if c.Sym != "+" && c.Sym != "~" && c.Sym != "-" {
			continue
		}
		if c.Backup {
			pre = append(pre, remote.ApplyStep{Op: "backup", Path: c.Path})
		}
		if c.Sym == "-" {
			if !c.Backup {
				post = append(post, remote.ApplyStep{Op: "rm", Path: c.Path})
			}
			continue
		}
		if c.Item == nil {
			continue
		}
		switch c.Item.Kind {
		case "tree":
			pre = append(pre, remote.ApplyStep{Op: "mkdir", Path: c.Path})
			trees = append(trees, c)
		case "file":
			post = append(post, remote.ApplyStep{
				Op: "write", Path: c.Path, Mode: c.Item.Mode,
				B64: base64.StdEncoding.EncodeToString(c.Item.Content),
			})
		case "link":
			post = append(post, remote.ApplyStep{Op: "link", Path: c.Path, Target: c.Item.Target})
		}
	}

	if len(pre) > 0 {
		resp, err := remote.RunAgent(tp.Target, remote.AgentRequest{
			Op: "apply", Steps: pre, BackupRoot: tp.Target.BackupRoot, Stamp: stamp,
		}, 120*time.Second)
		if err != nil {
			return err
		}
		if !resp.OK {
			return model.Errf("%s: %s", tp.Target.ID, resp.Error)
		}
	}
	for _, c := range trees {
		absDst := c.Path
		if strings.HasPrefix(absDst, "~/") {
			absDst = filepath.Join(tp.Home, absDst[2:])
		}
		if err := remote.RsyncTree(tp.Target, c.Item.Src, absDst); err != nil {
			return err
		}
	}
	if len(post) > 0 {
		resp, err := remote.RunAgent(tp.Target, remote.AgentRequest{
			Op: "apply", Steps: post, BackupRoot: tp.Target.BackupRoot, Stamp: stamp,
		}, 120*time.Second)
		if err != nil {
			return err
		}
		if !resp.OK {
			return model.Errf("%s: %s", tp.Target.ID, resp.Error)
		}
	}

	cfgPath := strings.TrimRight(tp.Target.SkillshareRoot, "/") + "/config.yaml"
	resp, err := remote.RunAgent(tp.Target, remote.AgentRequest{
		Op:            "skillshare_sync",
		SkillshareBin: tp.Target.SkillshareBin,
		ConfigPath:    cfgPath,
		DryRun:        false,
	}, 180*time.Second)
	if err != nil {
		return err
	}
	if !resp.OK {
		return model.Errf("%s: skillshare sync: %s", tp.Target.ID, resp.Error)
	}
	if ok, _ := resp.Result["ok"].(bool); !ok {
		return model.Errf("%s: skillshare sync failed: %v", tp.Target.ID, resp.Result["stderr"])
	}

	// Commit outward-synced state inside the environment's own standalone skillshare git repo & tag `livery-base`
	_, _ = remote.CommitEnvSkillshareBase(tp.Target, tp.Target.SkillshareRoot, commit)

	// Update provenance/lock.toml atomically
	return updateLockForTarget(repo, tp, commit, time.Now().UTC().Format(time.RFC3339))
}

func updateLockForTarget(repo *model.Repo, tp *TargetPlan, commit, when string) error {
	lockAll, err := model.LoadLock(repo.Root)
	if err != nil {
		return err
	}
	var items []model.LockEntry
	if tp.Rendered != nil {
		for _, it := range tp.Rendered.Items {
			if it.IsEntry() {
				continue
			}
			items = append(items, model.LockEntry{
				Target:       tp.Target.ID,
				Path:         it.Path,
				Kind:         it.Kind,
				SHA256:       it.SHA(),
				Sources:      it.Sources,
				LinkTarget:   it.Target,
				Commit:       commit,
				AppliedAt:    when,
				Role:         it.Role,
				SubmoduleSHA: it.SubmoduleSHA,
			})
		}
	}
	// Preserve existing entry-kind lock records for this target
	if prev := lockAll[tp.Target.ID]; prev != nil {
		for _, le := range prev.Items {
			if isEntryKind(le.Kind) {
				items = append(items, le)
			}
		}
	}
	lockAll[tp.Target.ID] = &model.LockTarget{
		Commit: commit,
		At:     when,
		Items:  items,
	}
	// Write deterministic flat [[entry]] format (100% compatible with provenance/lock.toml!)
	var tids []string
	for tid := range lockAll {
		tids = append(tids, tid)
	}
	sort.Strings(tids)
	var allEntries []map[string]any
	for _, tid := range tids {
		lt := lockAll[tid]
		sortedItems := append([]model.LockEntry(nil), lt.Items...)
		sort.Slice(sortedItems, func(i, j int) bool { return sortedItems[i].Path < sortedItems[j].Path })
		for _, le := range sortedItems {
			m := map[string]any{
				"target":  le.Target,
				"path":    le.Path,
				"kind":    le.Kind,
				"sha256":  le.SHA256,
				"sources": le.Sources,
			}
			if le.LinkTarget != "" {
				m["link_target"] = le.LinkTarget
			}
			if le.Commit != "" {
				m["commit"] = le.Commit
			} else if lt.Commit != "" {
				m["commit"] = lt.Commit
			}
			if le.AppliedAt != "" {
				m["applied_at"] = le.AppliedAt
			} else if lt.At != "" {
				m["applied_at"] = lt.At
			}
			if le.Role != "" {
				m["role"] = le.Role
			}
			if le.File != "" {
				m["file"] = le.File
			}
			if le.Entry != "" {
				m["entry"] = le.Entry
			}
			if le.Provider != "" {
				m["provider"] = le.Provider
			}
			if le.Event != "" {
				m["event"] = le.Event
			}
			if le.Command != "" {
				m["command"] = le.Command
			}
			if le.RetireAfter != "" {
				m["retire_after"] = le.RetireAfter
			}
			if le.SubmoduleSHA != "" {
				m["submodule_sha"] = le.SubmoduleSHA
			}
			allEntries = append(allEntries, m)
		}
	}
	doc := map[string]any{"entry": allEntries}
	hdr := "# Written by `liv sync`: every file, skill copy and link livery installed, per target.\n"
	lockPath := filepath.Join(repo.Root, "provenance", "lock.toml")
	_ = os.MkdirAll(filepath.Dir(lockPath), 0o755)
	return os.WriteFile(lockPath, []byte(hdr+model.DumpsTOML(doc)), 0o644)
}

// --- Cross-host Secrets HMAC Parity (NEVER leaks secret values!) --------------------------------

type SecretStatusRow struct {
	Profile string `json:"profile"`
	Key     string `json:"key"`
	Ref     string `json:"ref"`
	Status  string `json:"status"`
	Label   string `json:"label"`
	OK      bool   `json:"ok"`
}

type TargetSecretsStatus struct {
	Target  string            `json:"target"`
	OK      bool              `json:"ok"`
	Backend string            `json:"backend"`
	Error   string            `json:"error,omitempty"`
	Refs    []SecretStatusRow `json:"refs"`
}

func TargetSecretRefs(repo *model.Repo, t *model.Target) []remote.SecretRefItem {
	pids, err := repo.ExpandProfiles(t.Profiles)
	if err != nil {
		return nil
	}
	lids, err := repo.ResolveLaunchers(t, pids)
	if err != nil {
		return nil
	}
	var out []remote.SecretRefItem
	seen := map[string]bool{}
	for _, lid := range lids {
		sp := repo.Launchers[lid]
		drv := launcher.Drivers[sp.Harness]
		if drv == nil {
			continue
		}
		for k, ref := range drv.RequiredSecrets(sp.Raw) {
			key := lid + "|" + ref
			if !seen[key] {
				seen[key] = true
				out = append(out, remote.SecretRefItem{Profile: lid, Key: k, Ref: ref})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Profile != out[j].Profile {
			return out[i].Profile < out[j].Profile
		}
		return out[i].Ref < out[j].Ref
	})
	return out
}

func CheckSecretsStatus(repo *model.Repo, targets []*model.Target) []TargetSecretsStatus {
	store, _ := secrets.NewStore()
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	nonceB64 := base64.StdEncoding.EncodeToString(nonce)

	var out []TargetSecretsStatus
	for _, t := range targets {
		refs := TargetSecretRefs(repo, t)
		if len(refs) == 0 {
			out = append(out, TargetSecretsStatus{Target: t.ID, OK: true, Backend: "-", Refs: []SecretStatusRow{}})
			continue
		}
		resp, err := remote.RunAgent(t, remote.AgentRequest{
			Op: "secrets_check", Refs: refs, NonceB64: nonceB64,
		}, 60*time.Second)
		if err != nil || !resp.OK {
			msg := ""
			if err != nil {
				msg = err.Error()
			} else {
				msg = resp.Error
			}
			out = append(out, TargetSecretsStatus{Target: t.ID, OK: false, Error: msg, Refs: []SecretStatusRow{}})
			continue
		}
		remoteRefs, _ := resp.Result["refs"].(map[string]any)
		allOK := true
		var rows []SecretStatusRow
		for _, item := range refs {
			rec, _ := remoteRefs[item.Ref].(map[string]any)
			st := "missing"
			if rec != nil {
				st = fmt.Sprint(rec["status"])
			}
			scheme, targetName, _ := secrets.ParseRef(item.Ref)
			var localHMAC string
			if scheme == "store" && store != nil {
				if val, err := store.Resolve(item.Ref, nil); err == nil {
					localHMAC = secrets.ComputeHMAC(nonce, val)
				}
			}
			label := "present"
			ok := true
			switch st {
			case "insecure":
				label = fmt.Sprintf("INSECURE (%v)", rec["error"])
				ok = false
			case "missing":
				ok = false
				if scheme == "store" && localHMAC != "" {
					label = "MISSING (present locally -> run `liv secrets sync`)"
				} else if scheme == "store" {
					label = fmt.Sprintf("MISSING (run `liv secret set %s`)", targetName)
				} else {
					label = "MISSING"
				}
			default:
				if scheme == "store" && localHMAC != "" {
					if fmt.Sprint(rec["hmac"]) == localHMAC {
						label = "in-sync"
					} else {
						label = "DIFFERS from controller (run `liv secrets sync`)"
						ok = false
					}
				}
			}
			if !ok {
				allOK = false
			}
			rows = append(rows, SecretStatusRow{
				Profile: item.Profile, Key: item.Key, Ref: item.Ref, Status: st, Label: label, OK: ok,
			})
		}
		out = append(out, TargetSecretsStatus{
			Target: t.ID, OK: allOK, Backend: fmt.Sprint(resp.Result["backend"]), Refs: rows,
		})
	}
	return out
}

func RunDoctorReport(repo *model.Repo, targets []*model.Target) (string, bool) {
	var lines []string
	issues := 0
	unbootstrapped := 0
	errs := model.CheckRepo(repo.Root)
	if len(errs) > 0 {
		lines = append(lines, "== repo check")
		for _, e := range errs {
			lines = append(lines, "  [ERR] "+e)
		}
		issues += len(errs)
	} else {
		lines = append(lines, "== repo check: ok")
	}

	plansByTarget := map[string]*TargetPlan{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, t := range targets {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			tp := PlanTarget(repo, t)
			mu.Lock()
			plansByTarget[t.ID] = tp
			mu.Unlock()
		}()
	}
	wg.Wait()

	var readyForSecrets []*model.Target
	for _, t := range targets {
		if tp := plansByTarget[t.ID]; tp != nil && tp.Error == "" {
			readyForSecrets = append(readyForSecrets, t)
		}
	}
	secByTarget := map[string]TargetSecretsStatus{}
	if len(readyForSecrets) > 0 {
		for _, r := range CheckSecretsStatus(repo, readyForSecrets) {
			secByTarget[r.Target] = r
		}
	}

	for _, t := range targets {
		where := t.SSH
		if t.IsLocal() {
			where = "local"
		}
		lines = append(lines, fmt.Sprintf("== %s (%s)", t.ID, where))
		tp := plansByTarget[t.ID]
		if tp.Error != "" {
			if strings.Contains(tp.Error, remote.ErrRemoteLivMissing) {
				lines = append(lines, "  [WARN] "+tp.Error)
				unbootstrapped++
			} else {
				lines = append(lines, "  [ERR] "+tp.Error)
				issues++
			}
			continue
		}
		if !tp.HasWork() && len(tp.By("!")) == 0 {
			lines = append(lines, fmt.Sprintf("  [OK ] config in sync (%d managed items)", len(tp.By("="))))
		} else {
			lines = append(lines, fmt.Sprintf("  [ERR] config drift (+%d ~%d -%d !%d)  (run `liv plan --target %s`)", len(tp.By("+")), len(tp.By("~")), len(tp.By("-")), len(tp.By("!")), t.ID))
			issues++
		}
		if len(tp.SkillshareLines) > 0 {
			tag := "OK "
			line := tp.SkillshareLines[0]
			if strings.HasPrefix(line, "MISSING") {
				tag = "ERR"
				issues++
				line += fmt.Sprintf(" -> run `liv env bootstrap %s --skillshare`", t.ID)
			} else if strings.HasPrefix(line, "ERROR") {
				tag = "ERR"
				issues++
			}
			lines = append(lines, fmt.Sprintf("  [%s] skillshare (%s)", tag, line))
		}
		sr := secByTarget[t.ID]
		if sr.Error != "" {
			lines = append(lines, "  [ERR] secrets: "+sr.Error)
			issues++
		} else if len(sr.Refs) > 0 {
			for _, r := range sr.Refs {
				tag := "OK "
				if !r.OK {
					tag = "ERR"
					issues++
				}
				lines = append(lines, fmt.Sprintf("  [%s] secret %s (%s): %s", tag, r.Ref, r.Profile, r.Label))
			}
		}
	}
	if hs := herdr.Status(""); hs.HerdrPresent {
		shortExp := hs.ExpectedSHA256
		if len(shortExp) > 12 {
			shortExp = shortExp[:12]
		}
		if hs.Installed && hs.UpToDate && hs.KeyBound {
			lines = append(lines, fmt.Sprintf("== herdr mod: ok (livery-launcher sha256:%s)", shortExp))
		} else if hs.Installed && !hs.UpToDate {
			lines = append(lines, fmt.Sprintf("== herdr mod: [ERR] outdated/drifted (expected sha256:%s; run `liv herdr install-mod`)", shortExp))
			issues++
		} else {
			lines = append(lines, "== herdr mod: [ERR] not installed (run `liv herdr install-mod`)")
			issues++
		}
	}

	if unbootstrapped > 0 {
		lines = append(lines, "", fmt.Sprintf("hint: %d remote host(s) need `liv` installed -> run `liv env bootstrap --all`", unbootstrapped))
	}
	if issues == 0 && unbootstrapped == 0 {
		lines = append(lines, "", "doctor: all checks passed")
	} else if issues == 0 {
		lines = append(lines, "", fmt.Sprintf("doctor: local checks passed (%d remote host(s) not bootstrapped yet)", unbootstrapped))
	} else {
		lines = append(lines, "", fmt.Sprintf("doctor: %d issue(s) found", issues))
	}
	return strings.Join(lines, "\n"), issues == 0
}
