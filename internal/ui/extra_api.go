package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/bowd/livery/internal/apps"
	"github.com/bowd/livery/internal/fleet"
	"github.com/bowd/livery/internal/launcher"
	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/secrets"
)

func (s *Server) handleExtraAPI(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == "/api/onboard":
		writeJSON(w, http.StatusOK, fleet.ComputeOnboarding(s.repoRoot))
		return true
	case r.Method == http.MethodPost && p == "/api/matrix/cell":
		s.apiMatrixCell(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/matrix/row":
		s.apiMatrixRow(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/matrix/profile-resolve":
		s.apiMatrixProfileResolve(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/projects/add":
		s.apiProjectAdd(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/profile-bundles/save":
		s.apiProfileBundleSave(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/profile-bundles/delete":
		s.apiProfileBundleDelete(w, r)
		return true
	case r.Method == http.MethodGet && p == "/api/prompts":
		s.apiPromptsList(w)
		return true
	case r.Method == http.MethodPost && p == "/api/prompts/save":
		s.apiPromptsSave(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/prompts/reorder":
		s.apiPromptsReorder(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/prompts/delete":
		s.apiPromptsDelete(w, r)
		return true
	case r.Method == http.MethodGet && p == "/api/profiles":
		s.apiProfilesList(w)
		return true
	case r.Method == http.MethodPost && p == "/api/profiles/save":
		s.apiProfilesSave(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/profiles/delete":
		s.apiProfilesDelete(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/groups/set":
		s.apiGroupsSet(w, r)
		return true
	case r.Method == http.MethodGet && p == "/api/apps":
		writeJSON(w, http.StatusOK, map[string]any{
			"herdr":    apps.LoadHerdrOverview("", ""),
			"t3":       apps.LoadT3Overview(""),
			"openclaw": apps.LoadOpenClawOverview(""),
		})
		return true
	case r.Method == http.MethodPost && p == "/api/apps/herdr/sync":
		s.apiAppsHerdrSync(w)
		return true
	case r.Method == http.MethodPost && p == "/api/apps/herdr/upsert":
		s.apiAppsHerdrUpsert(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/apps/herdr/delete":
		s.apiAppsHerdrDelete(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/apps/t3/upsert":
		s.apiAppsT3Upsert(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/apps/t3/delete":
		s.apiAppsT3Delete(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/apps/openclaw/create-profile":
		s.apiAppsOpenClawCreateProfile(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/apps/openclaw/plugin":
		s.apiAppsOpenClawPlugin(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/apps/openclaw/mcp":
		s.apiAppsOpenClawMCP(w, r)
		return true
	case r.Method == http.MethodPost && p == "/api/apps/openclaw/mcp-delete":
		s.apiAppsOpenClawMCPDelete(w, r)
		return true
	}
	return false
}

func (s *Server) apiMatrixCell(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Category string `json:"category"`
		Item     string `json:"item"`
		Env      string `json:"env"`
		Enabled  bool   `json:"enabled"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	conf, err := model.SetMatrixCell(s.repoRoot, in.Category, in.Item, in.Env, in.Enabled)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if conf != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "inherited_from_profile",
			"conflict": conf,
		})
		return
	}
	s.slots.InvalidatePreviewTarget(in.Env)
	s.invalidateSyncCache(in.Env)
	s.scheduleSlotSyncCheck("env/" + in.Env)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiMatrixRow(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Category  string `json:"category"`
		Item      string `json:"item"`
		EnableAll bool   `json:"enable_all"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	updated, skipped, err := model.SetMatrixRow(s.repoRoot, in.Category, in.Item, in.EnableAll)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.slots.InvalidatePreviewEnvs()
	s.invalidateSyncCache("")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"updated":          nonNilStrings(updated),
		"skipped_profiles": nonNilStrings(skipped),
	})
}

func (s *Server) apiMatrixProfileResolve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action     string `json:"action"` // "detach" | "duplicate"
		Env        string `json:"env"`
		Profile    string `json:"profile"`
		NewProfile string `json:"new_profile"`
		Category   string `json:"category"`
		Item       string `json:"item"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	var err error
	switch in.Action {
	case "detach":
		err = model.DetachProfileFromEnv(s.repoRoot, in.Env, in.Profile)
	case "duplicate":
		err = model.DuplicateProfileWithoutItem(s.repoRoot, in.Env, in.Profile, in.NewProfile, in.Category, in.Item)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "action must be detach or duplicate"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.slots.InvalidatePreviewTarget(in.Env)
	s.invalidateSyncCache(in.Env)
	s.scheduleSlotSyncCheck("env/" + in.Env)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiProjectAdd(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Target string `json:"target"`
		Slug   string `json:"slug"`
		Path   string `json:"path"`
		Why    string `json:"why"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := model.CreateProjectEnvironment(s.repoRoot, in.Target, in.Slug, in.Path, in.Why); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": model.ProjectEnvID(in.Target, in.Slug)})
}

func (s *Server) apiProfileBundleSave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID          string   `json:"id"`
		Description string   `json:"description"`
		Extends     []string `json:"extends"`
		Fragments   []string `json:"fragments"`
		Skills      []string `json:"skills"`
		MCP         []string `json:"mcp"`
		Hooks       []string `json:"hooks"`
		Agents      []string `json:"agents"`
		Extras      []string `json:"extras"`
		Plugins     []string `json:"plugins"`
		Launchers   []string `json:"launchers"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := model.SaveProfile(s.repoRoot, in.ID, in.Description, in.Extends, in.Fragments, in.Skills, in.MCP, in.Hooks, in.Agents, in.Extras, in.Plugins, in.Launchers); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.slots.InvalidatePreviewEnvs()
	s.invalidateSyncCache("")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": in.ID})
}

func (s *Server) apiProfileBundleDelete(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	if conf, _ := body["confirm"].(bool); !conf {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "delete requires confirm: true"})
		return
	}
	id := strings.TrimSpace(fmt.Sprint(body["id"]))
	if err := model.DeleteProfile(s.repoRoot, id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.slots.InvalidatePreviewEnvs()
	s.invalidateSyncCache("")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": id})
}

func (s *Server) apiPromptsList(w http.ResponseWriter) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	var pkeys []string
	for k := range repo.Profiles {
		pkeys = append(pkeys, k)
	}
	sort.Strings(pkeys)
	fragToProfiles := map[string][]string{}
	profileList := make([]map[string]any, 0, len(pkeys))
	for _, k := range pkeys {
		pr := repo.Profiles[k]
		for _, fid := range pr.Fragments {
			fragToProfiles[fid] = append(fragToProfiles[fid], k)
		}
		profileList = append(profileList, map[string]any{
			"id":          pr.ID,
			"description": pr.Description,
			"fragments":   nonNilStrings(pr.Fragments),
		})
	}
	fragToTargets := map[string][]string{}
	for _, t := range repo.Targets {
		frags, _, err := repo.Resolve(t)
		if err != nil {
			continue
		}
		for _, fid := range frags {
			fragToTargets[fid] = append(fragToTargets[fid], t.ID)
		}
	}

	areaSet := map[string]bool{}
	orderedIDs := repo.SortedFragmentIDs()
	items := make([]map[string]any, 0, len(orderedIDs))
	for idx, fid := range orderedIDs {
		f := repo.Fragments[fid]
		area := model.FragmentArea(f.ID)
		areaSet[area] = true
		items = append(items, map[string]any{
			"id":        f.ID,
			"rank":      idx + 1,
			"area":      area,
			"providers": nonNilStrings(f.Providers),
			"scope":     f.Scope,
			"why":       f.Why,
			"since":     f.Since,
			"body":      f.Body,
			"words":     len(strings.Fields(f.Body)),
			"profiles":  nonNilStrings(fragToProfiles[f.ID]),
			"targets":   nonNilStrings(fragToTargets[f.ID]),
		})
	}
	areas := make([]string, 0, len(areaSet))
	for a := range areaSet {
		areas = append(areas, a)
	}
	sort.Strings(areas)

	writeJSON(w, http.StatusOK, map[string]any{
		"fragments": items,
		"areas":     areas,
		"profiles":  profileList,
		"scopes":    model.Scopes,
		"providers": model.Providers,
	})
}

func (s *Server) apiPromptsSave(w http.ResponseWriter, r *http.Request) {
	var in model.PromptFragmentInput
	b, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err := json.Unmarshal(b, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid prompt fragment JSON: " + err.Error()})
		return
	}
	id, err := model.SavePromptFragment(s.repoRoot, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	// If prompts/order.toml exists, ensure new fragment is appended to order.toml cleanly
	if repo, err := model.LoadRepo(s.repoRoot); err == nil && len(repo.FragmentOrder) > 0 {
		_, _ = model.SavePromptOrder(s.repoRoot, repo.SortedFragmentIDs())
	}
	s.slots.InvalidatePreviewEnvs()
	s.invalidateSyncCache("")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (s *Server) apiPromptsReorder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Order []string `json:"order"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()})
		return
	}
	saved, err := model.SavePromptOrder(s.repoRoot, in.Order)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.slots.InvalidatePreviewEnvs()
	s.invalidateSyncCache("")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "order": saved})
}

func (s *Server) apiPromptsDelete(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	if conf, _ := body["confirm"].(bool); !conf {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "delete requires confirm: true"})
		return
	}
	id := strings.TrimSpace(fmt.Sprint(body["id"]))
	if err := model.DeletePromptFragment(s.repoRoot, id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": id})
}

func (s *Server) apiProfilesList(w http.ResponseWriter) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	store, _ := secrets.NewStore()
	loaded := launcher.LoadAllProfiles(s.repoRoot)
	rows := launcher.ListProfileRows(loaded, store, "")
	rowByID := map[string]launcher.ProfileListRow{}
	for _, r := range rows {
		rowByID[r.ID] = r
	}

	var ids []string
	for id := range repo.Launchers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		sp := repo.Launchers[id]
		row := rowByID[id]
		hcfg, _ := sp.Raw[sp.Harness].(map[string]any)
		herdrTbl, _ := sp.Raw["herdr"].(map[string]any)
		t3Tbl, _ := sp.Raw["t3"].(map[string]any)
		items = append(items, map[string]any{
			"id":               sp.ID,
			"harness":          sp.Harness,
			"profile":          sp.Profile,
			"command":          fmt.Sprintf("liv %s %s", sp.Harness, sp.Profile),
			"description":      fmt.Sprint(orEmpty(sp.Raw["description"])),
			"why":              sp.Why,
			"mode":             fmt.Sprint(orDefault(sp.Raw["mode"], "direct")),
			"auth_mode":        fmt.Sprint(orEmpty(sp.Raw["auth_mode"])),
			"session_group":    sp.SessionGroup,
			"shared_home":      fmt.Sprint(orEmpty(sp.Raw["shared_home"])),
			"secrets_ok":       row.SecretsOK,
			"missing_secrets":  nonNilStrings(row.MissingSecrets),
			"base_url":         fmt.Sprint(orEmpty(mapField(hcfg, "base_url"), mapField(hcfg, "gateway_url"))),
			"model":            fmt.Sprint(orEmpty(mapField(hcfg, "model"))),
			"token_secret":     fmt.Sprint(orEmpty(mapField(hcfg, "token_secret"))),
			"provider_id":      fmt.Sprint(orEmpty(mapField(hcfg, "provider_id"))),
			"provider_name":    fmt.Sprint(orEmpty(mapField(hcfg, "provider_name"))),
			"env_key":          fmt.Sprint(orEmpty(mapField(hcfg, "env_key"))),
			"openclaw_profile": fmt.Sprint(orEmpty(mapField(hcfg, "profile_name"))),
			"herdr_name":       fmt.Sprint(orEmpty(mapField(herdrTbl, "name"))),
			"herdr_icon":       fmt.Sprint(orEmpty(mapField(herdrTbl, "icon"))),
			"t3_instance_id":   fmt.Sprint(orEmpty(mapField(t3Tbl, "instance_id"))),
			"t3_display_name":  fmt.Sprint(orEmpty(mapField(t3Tbl, "display_name"))),
			"t3_accent_color":  fmt.Sprint(orEmpty(mapField(t3Tbl, "accent_color"))),
			"t3_custom_models": anyToStrings(mapField(t3Tbl, "custom_models")),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"profiles":   items,
		"blueprints": model.LauncherBlueprints(),
		"groups":     launcher.SummarizeGroups(loaded),
	})
}

func (s *Server) apiProfilesSave(w http.ResponseWriter, r *http.Request) {
	var in model.LauncherProfileInput
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid profile JSON: " + err.Error()})
		return
	}
	id, err := model.SaveLauncherProfile(s.repoRoot, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	var followups []string
	if in.TokenSecret != "" {
		if st, err := secrets.NewStore(); err == nil && !st.Exists(in.TokenSecret, nil) {
			_, sname, _ := secrets.ParseRef(in.TokenSecret)
			if sname != "" {
				followups = append(followups, "liv secret set "+sname)
			}
		}
	}
	prof := model.DeriveProfileName(id, in.Harness, in.Profile)
	followups = append(followups, fmt.Sprintf("liv dry-run %s %s", in.Harness, prof))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"id":        id,
		"command":   fmt.Sprintf("liv %s %s", in.Harness, prof),
		"followups": followups,
	})
}

func (s *Server) apiProfilesDelete(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	if conf, _ := body["confirm"].(bool); !conf {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "delete requires confirm: true"})
		return
	}
	id := strings.TrimSpace(fmt.Sprint(body["id"]))
	if err := model.DeleteLauncherProfile(s.repoRoot, id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": id})
}

func (s *Server) apiGroupsSet(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	id := strings.TrimSpace(fmt.Sprint(body["id"]))
	group := strings.TrimSpace(fmt.Sprint(body["group"]))
	if group == "<nil>" {
		group = ""
	}
	if err := model.UpdateLauncherGroup(s.repoRoot, id, group); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "group": group})
}

func (s *Server) apiAppsHerdrSync(w http.ResponseWriter) {
	repo, err := model.LoadRepo(s.repoRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	n, err := apps.SyncHerdrFromRepo(repo, "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "synced": n})
}

func (s *Server) apiAppsHerdrUpsert(w http.ResponseWriter, r *http.Request) {
	var p apps.HerdrProgram
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := apps.UpsertHerdrProgram("", p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": p.Key})
}

func (s *Server) apiAppsHerdrDelete(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	key := strings.TrimSpace(fmt.Sprint(body["key"]))
	if err := apps.DeleteHerdrProgram("", key); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": key})
}

func (s *Server) apiAppsT3Upsert(w http.ResponseWriter, r *http.Request) {
	var patch apps.T3ProviderPatch
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := apps.UpsertT3Provider("", patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "instance_id": patch.InstanceID})
}

func (s *Server) apiAppsT3Delete(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	if conf, _ := body["confirm"].(bool); !conf {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "delete requires confirm: true"})
		return
	}
	id := strings.TrimSpace(fmt.Sprint(body["instance_id"]))
	if err := apps.DeleteT3Provider("", id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": id})
}

func (s *Server) apiAppsOpenClawCreateProfile(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	name := strings.TrimSpace(fmt.Sprint(body["name"]))
	cfg, err := apps.CreateOpenClawProfile("", name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name, "config_path": cfg})
}

func (s *Server) apiAppsOpenClawPlugin(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	prof := strings.TrimSpace(fmt.Sprint(body["profile"]))
	if prof == "" || prof == "<nil>" {
		prof = "default"
	}
	name := strings.TrimSpace(fmt.Sprint(body["plugin"]))
	enabled, _ := body["enabled"].(bool)
	if err := apps.SetOpenClawPlugin("", prof, name, enabled); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiAppsOpenClawMCP(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	prof := strings.TrimSpace(fmt.Sprint(body["profile"]))
	if prof == "" || prof == "<nil>" {
		prof = "default"
	}
	var srv apps.OpenClawMCPView
	srv.Name = strings.TrimSpace(fmt.Sprint(body["name"]))
	if v, ok := body["command"].(string); ok {
		srv.Command = strings.TrimSpace(v)
	}
	if v, ok := body["url"].(string); ok {
		srv.URL = strings.TrimSpace(v)
	}
	srv.Args = anyToStrings(body["args"])
	if err := apps.SetOpenClawMCP("", prof, srv); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiAppsOpenClawMCPDelete(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	prof := strings.TrimSpace(fmt.Sprint(body["profile"]))
	if prof == "" || prof == "<nil>" {
		prof = "default"
	}
	name := strings.TrimSpace(fmt.Sprint(body["name"]))
	if err := apps.DeleteOpenClawMCP("", prof, name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func mapField(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

func orEmpty(vals ...any) any {
	for _, v := range vals {
		if v != nil && fmt.Sprint(v) != "" {
			return v
		}
	}
	return ""
}

func orDefault(v any, def string) any {
	if v == nil || fmt.Sprint(v) == "" {
		return def
	}
	return v
}

func anyToStrings(v any) []string {
	if v == nil {
		return []string{}
	}
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, el := range x {
			out = append(out, fmt.Sprint(el))
		}
		return out
	default:
		return []string{}
	}
}
