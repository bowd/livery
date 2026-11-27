/* Livery Meta-UI — Environments · Catalog · Matrix (calm, light-first control plane) */
(() => {
  let CSRF = window.__LIVERY_CSRF__ || "";

  const ICONS = {
    sun: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/></svg>`,
    moon: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>`,
    refresh: `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-2.64-6.36"/><polyline points="21 3 21 9 15 9"/></svg>`,
    check: `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>`,
  };

  const state = {
    data: null,
    promptsData: null,
    profilesData: null,
    appsData: null,
    scope: "fleet",             // "fleet" | "env"
    fleetTab: "environments",   // "environments" | "catalog" | "matrix"
    catalogSub: "skillshare",   // "skillshare" | "prompts" | "launchers" | "profiles" | "apps"
    appSub: "t3",               // "t3" | "herdr" | "openclaw"
    promptArea: "all",
    promptQuery: "",
    selectedTarget: null,
    envTab: "overview",         // "overview" | "skillshare"
    matrixTab: "skills",        // "profiles" | "skills" | "mcp" | "hooks" | "extras" | "agents" | "plugins" | "prompts" | "launchers"
    editingProfile: null,       // profile ID when editing a profile in Matrix view
    profileMatrixTab: "all",    // "all" | "skills" | "mcp" | "hooks" | "extras" | "agents" | "plugins" | "prompts" | "launchers"
    bundles: {},
    opsOutput: {},
    ssSlotMeta: {},
    ssSubPath: "",
    syncBootstrapped: false,
    syncChecking: false,
    checkingTargets: {},
    unmanagedScanning: {},
    lastVisCheck: {},
    modalLocked: false,
    bootVersion: "",
    menuOpen: false,
  };

  function targetSyncSummary(t) {
    const sy = t && t.sync_status;
    if (!sy) {
      const dr = t && t.drift;
      if (dr && dr.has_drift && Array.isArray(dr.items) && dr.items.length) {
        return { known: true, offline: false, outOfSync: true, total: dr.items.length, push: 0, pull: dr.items.length, error: "" };
      }
      return { known: false, offline: false, outOfSync: false, total: 0, push: 0, pull: 0, error: "" };
    }
    const push = sy.push_count || 0;
    const pull = sy.pull_count || 0;
    const err = sy.push_error || (sy.pull && !sy.pull.ok ? sy.pull.error : "") || "";
    const offline = Boolean(err && push === 0 && pull === 0);
    return { known: true, offline, outOfSync: Boolean(sy.out_of_sync), total: push + pull, push, pull, error: err };
  }

  function renderSyncPill(sum, compact = false) {
    if (!sum.known) return "";
    if (sum.offline) return `<span class="tag tag-bad" title="${esc(sum.error)}">offline</span>`;
    if (sum.outOfSync) {
      const parts = [];
      if (sum.pull) parts.push(`↑${sum.pull}`);
      if (sum.push) parts.push(`↓${sum.push}`);
      const txt = compact ? (parts.join(" ") || `${sum.total}`) : `out of sync · ${parts.join(" ")}`;
      return `<span class="tag tag-warn" title="${sum.pull ? `${sum.pull} local edit(s) on host` : ""}${sum.pull && sum.push ? " · " : ""}${sum.push ? `${sum.push} pending push change(s)` : ""}">${esc(txt)}</span>`;
    }
    return compact ? `<span class="dot ok" title="In sync"></span>` : `<span class="tag tag-ok">synced ✓</span>`;
  }

  function esc(v) {
    return String(v ?? "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function toast(msg, kind = "info") {
    const root = document.getElementById("toast-root");
    const el = document.createElement("div");
    el.className = `toast ${kind}`;
    el.textContent = msg;
    root.appendChild(el);
    setTimeout(() => el.remove(), 4200);
  }

  async function apiGet(path) {
    const res = await fetch(path, { credentials: "same-origin" });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
    return body;
  }

  async function refreshCSRFToken() {
    try {
      const res = await fetch("/", { credentials: "same-origin", cache: "no-store" });
      const html = await res.text();
      const m = html.match(/window\.__LIVERY_CSRF__\s*=\s*"([^"]+)"/);
      if (m && m[1]) {
        CSRF = m[1];
        return true;
      }
    } catch (_) {}
    return false;
  }

  async function apiPostRaw(path, payload = {}, retried = false) {
    const res = await fetch(path, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-Livery-CSRF": CSRF },
      body: JSON.stringify(payload),
    });
    const body = await res.json().catch(() => ({}));
    if (!retried && res.status === 403 && body && typeof body.error === "string" && body.error.toLowerCase().includes("csrf")) {
      if (await refreshCSRFToken()) {
        return apiPostRaw(path, payload, true);
      }
    }
    return { status: res.status, ok: res.ok, body };
  }

  async function apiPost(path, payload = {}) {
    const { ok, status, body } = await apiPostRaw(path, payload);
    if (!ok) throw new Error(body.error || `HTTP ${status}`);
    return body;
  }

  // --- Theme ------------------------------------------------------------------------------------

  function isDark() { return document.documentElement.classList.contains("dark"); }

  function syncThemeButton() {
    const btn = document.getElementById("theme-toggle-btn");
    btn.innerHTML = isDark() ? ICONS.sun : ICONS.moon;
    btn.title = isDark() ? "Light mode" : "Dark mode";
  }

  function syncIframeTheme(iframe) {
    if (!iframe) return;
    try {
      const doc = iframe.contentDocument;
      if (doc && doc.documentElement) doc.documentElement.classList.toggle("dark", isDark());
    } catch (_) {}
  }

  document.getElementById("theme-toggle-btn").onclick = () => {
    const nextDark = !isDark();
    document.documentElement.classList.toggle("dark", nextDark);
    localStorage.setItem("skillshare-theme-preference", nextDark ? "dark" : "light");
    syncThemeButton();
    for (const f of document.querySelectorAll("iframe.ss-iframe")) syncIframeTheme(f);
  };
  syncThemeButton();

  // --- Modal ------------------------------------------------------------------------------------

  function setModalLocked(locked) {
    state.modalLocked = Boolean(locked);
    const xBtn = document.getElementById("modal-close");
    if (xBtn) xBtn.disabled = state.modalLocked;
  }

  function openModal({ title, bodyHtml, buttons, wide = false }) {
    setModalLocked(false);
    document.querySelector(".modal-card").classList.toggle("modal-wide", Boolean(wide));
    document.getElementById("modal-title").textContent = title;
    document.getElementById("modal-body").innerHTML = bodyHtml;
    const footer = document.getElementById("modal-footer");
    footer.innerHTML = "";
    for (const b of buttons) {
      const btn = document.createElement("button");
      btn.className = b.cls || "btn";
      btn.textContent = b.label;
      btn.onclick = () => {
        if (b.close !== false) closeModal();
        if (b.onClick) b.onClick(btn);
      };
      footer.appendChild(btn);
    }
    document.getElementById("modal-backdrop").classList.remove("hidden");
  }

  function closeModal(force = false) {
    if (state.modalLocked && !force) return;
    setModalLocked(false);
    document.getElementById("modal-backdrop").classList.add("hidden");
  }
  document.getElementById("modal-close").onclick = () => closeModal();
  document.getElementById("modal-backdrop").onclick = (e) => { if (e.target.id === "modal-backdrop") closeModal(); };
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeModal(); });

  // --- State & Navigation -----------------------------------------------------------------------

  async function refreshState(silent = false) {
    try {
      state.data = await apiGet("/api/state");
      renderTopbar();
      renderWorkspace();
    } catch (err) {
      if (!silent) toast(`Failed to load state: ${err.message}`, "err");
    }
  }

  async function loadPrompts(force = false) {
    if (force || !state.promptsData) state.promptsData = await apiGet("/api/prompts");
    return state.promptsData;
  }

  async function loadProfilesAndApps(force = false) {
    if (force || !state.profilesData) state.profilesData = await apiGet("/api/profiles");
    if (force || !state.appsData) state.appsData = await apiGet("/api/apps");
  }

  async function loadBundle(tid, force = false) {
    if (!force && state.bundles[tid]) return state.bundles[tid];
    const b = await apiGet(`/api/targets/${encodeURIComponent(tid)}/bundle`);
    state.bundles[tid] = b;
    return b;
  }

  function closeMenu() {
    state.menuOpen = false;
    document.getElementById("env-switcher-menu").classList.add("hidden");
    document.getElementById("env-switcher-btn").setAttribute("aria-expanded", "false");
  }

  function activeSkillshareSlotKey() {
    if (state.scope === "env" && state.envTab === "skillshare" && state.selectedTarget) {
      return `env/${state.selectedTarget}`;
    }
    if (state.scope === "fleet" && state.fleetTab === "catalog" && state.catalogSub === "skillshare") {
      return "catalog";
    }
    return "";
  }

  function normalizeSubPath(p) {
    if (!p || p === "/") return "";
    if (p.startsWith("?") || p.startsWith("#")) return p;
    const clean = p.startsWith("/") ? p : "/" + p;
    return clean === "/" ? "" : clean;
  }

  function topSectionOnly(subPath) {
    const clean = normalizeSubPath(subPath);
    if (!clean || clean.startsWith("?") || clean.startsWith("#")) return "";
    const pathOnly = clean.split(/[?#]/)[0];
    const first = pathOnly.split("/").filter(Boolean)[0];
    return first ? "/" + first : "";
  }

  function buildSlotIframeURL(slotBaseUrl, subPath) {
    const base = String(slotBaseUrl || "").replace(/\/+$/, "");
    const rel = normalizeSubPath(subPath);
    if (!rel) return base + "/";
    if (rel.startsWith("?") || rel.startsWith("#")) return base + "/" + rel;
    return base + rel;
  }

  function extractIframeSubPath(loc, slotBaseUrl) {
    const base = String(slotBaseUrl || "").replace(/\/+$/, "");
    let path = loc.pathname || "";
    let decodedPath = path;
    let decodedBase = base;
    try { decodedPath = decodeURIComponent(path); } catch (_) {}
    try { decodedBase = decodeURIComponent(base); } catch (_) {}
    let rel = "";
    if (path.startsWith(base)) {
      rel = path.slice(base.length);
    } else if (decodedPath.startsWith(decodedBase)) {
      rel = decodedPath.slice(decodedBase.length);
    }
    rel = rel.replace(/\/+$/, "");
    if (rel === "/") rel = "";
    if (rel && !rel.startsWith("/")) rel = "/" + rel;
    return `${rel}${loc.search || ""}${loc.hash || ""}`;
  }

  function updateLiveSyncUI(tid) {
    if (!state.data) return;
    renderTopbar();
    if (state.menuOpen) renderSwitcherMenu();
    if (state.scope === "fleet" && state.fleetTab === "environments") {
      renderEnvironments();
    } else if (state.scope === "env" && tid && state.selectedTarget === tid && state.envTab === "overview") {
      const active = document.activeElement;
      const editingInput = active && active.closest && active.closest("#main-content") && /^(INPUT|TEXTAREA|SELECT)$/.test(active.tagName);
      if (!editingInput && state.bundles[tid]) renderEnvOverview(tid, state.bundles[tid]);
    }
  }

  function applyTargetSyncStatus(tid, st) {
    if (!state.data || !tid || !st) return;
    const t = (state.data.targets || []).find((x) => x.id === tid);
    if (t) {
      t.sync_status = st;
      if (st.pull) t.drift = st.pull;
    }
    if (state.bundles[tid] && st.pull) state.bundles[tid].drift = st.pull;
    delete state.checkingTargets[tid];
    state.lastVisCheck[tid] = Date.now();
    updateLiveSyncUI(tid);
  }

  function connectLiveEvents() {
    if (typeof EventSource === "undefined") return;
    try {
      const es = new EventSource("/api/events");
      es.addEventListener("hello", (ev) => {
        try {
          const d = JSON.parse(ev.data || "{}");
          if (d.version) {
            if (!state.bootVersion) state.bootVersion = d.version;
            else if (state.bootVersion !== d.version) window.location.reload();
          }
        } catch (_) {}
      });
      es.addEventListener("slot-activity", (ev) => {
        try {
          const d = JSON.parse(ev.data || "{}");
          if (d.target) {
            state.checkingTargets[d.target] = true;
            updateLiveSyncUI(d.target);
          } else if (d.slot === "catalog" && state.data) {
            for (const t of (state.data.targets || [])) state.checkingTargets[t.id] = true;
            updateLiveSyncUI("");
          }
        } catch (_) {}
      });
      es.addEventListener("sync-status", (ev) => {
        try {
          const d = JSON.parse(ev.data || "{}");
          if (d.target && d.status) applyTargetSyncStatus(d.target, d.status);
        } catch (_) {}
      });
      es.addEventListener("fleet-sync-status", (ev) => {
        try {
          const d = JSON.parse(ev.data || "{}");
          const map = d.statuses || {};
          state.checkingTargets = {};
          if (state.data) {
            for (const t of (state.data.targets || [])) {
              if (map[t.id]) {
                t.sync_status = map[t.id];
                if (map[t.id].pull) t.drift = map[t.id].pull;
              }
            }
          }
          updateLiveSyncUI("");
        } catch (_) {}
      });
    } catch (_) {}
  }

  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState !== "visible") return;
    if (state.scope !== "env" || !state.selectedTarget) return;
    const tid = state.selectedTarget;
    const last = state.lastVisCheck[tid] || 0;
    if (Date.now() - last < 5000) return;
    state.lastVisCheck[tid] = Date.now();
    apiPost(`/api/targets/${encodeURIComponent(tid)}/sync-preview`, {})
      .then((st) => applyTargetSyncStatus(tid, st))
      .catch(() => {});
  });

  function hookIframeRouting(frame, slotBaseUrl, slotKey) {
    try {
      const cw = frame.contentWindow;
      if (!cw || !cw.location) return;
      const syncFromIframe = () => {
        if (!frame.isConnected || activeSkillshareSlotKey() !== slotKey) return;
        try {
          const next = extractIframeSubPath(cw.location, slotBaseUrl);
          if (next !== state.ssSubPath) {
            state.ssSubPath = next;
            syncURL(true);
          }
        } catch (_) {}
      };
      syncFromIframe();
      if (!cw.__liveryRouteHooked) {
        cw.__liveryRouteHooked = true;
        const origPush = cw.history.pushState.bind(cw.history);
        const origRepl = cw.history.replaceState.bind(cw.history);
        cw.history.pushState = function (...args) {
          const res = origPush(...args);
          syncFromIframe();
          return res;
        };
        cw.history.replaceState = function (...args) {
          const res = origRepl(...args);
          syncFromIframe();
          return res;
        };
        cw.addEventListener("popstate", syncFromIframe);
        cw.addEventListener("hashchange", syncFromIframe);
      }
    } catch (_) {}
  }

  function currentRoutePath() {
    const sub = normalizeSubPath(state.ssSubPath);
    if (state.scope === "env" && state.selectedTarget) {
      const tid = encodeURIComponent(state.selectedTarget);
      return state.envTab === "skillshare" ? `/env/${tid}/skillshare${sub}` : `/env/${tid}/overview`;
    }
    if (state.fleetTab === "catalog") {
      if (state.catalogSub === "apps") return `/catalog/apps/${encodeURIComponent(state.appSub || "t3")}`;
      if (state.catalogSub === "skillshare") return `/catalog/skillshare${sub}`;
      return `/catalog/${encodeURIComponent(state.catalogSub || "skillshare")}`;
    }
    if (state.fleetTab === "matrix") {
      if (state.editingProfile) {
        const pid = encodeURIComponent(state.editingProfile);
        const pcat = state.profileMatrixTab && state.profileMatrixTab !== "all" ? `/${encodeURIComponent(state.profileMatrixTab)}` : "";
        return `/matrix/profile/${pid}${pcat}`;
      }
      return `/matrix/${encodeURIComponent(state.matrixTab || "skills")}`;
    }
    return "/environments";
  }

  function syncURL(replace = false) {
    const next = currentRoutePath();
    const cur = `${window.location.pathname}${window.location.search}${window.location.hash}`;
    if (cur === next) return;
    try {
      if (replace) window.history.replaceState(null, "", next);
      else window.history.pushState(null, "", next);
    } catch (_) {}
  }

  function applyRouteFromLocation() {
    const raw = (window.location.pathname || "/").replace(/\/+$/, "") || "/";
    const rawSegs = raw.split("/").filter(Boolean);
    const segs = rawSegs.map((s) => {
      try { return decodeURIComponent(s); } catch (_) { return s; }
    });
    const tailQS = `${window.location.search || ""}${window.location.hash || ""}`;
    state.ssSubPath = "";

    if (segs[0] === "env" && segs[1]) {
      state.scope = "env";
      state.selectedTarget = segs[1];
      state.envTab = segs[2] === "skillshare" ? "skillshare" : "overview";
      if (state.envTab === "skillshare") {
        const rest = rawSegs.slice(3).join("/");
        state.ssSubPath = normalizeSubPath((rest ? "/" + rest : "") + tailQS);
      }
      return;
    }
    state.scope = "fleet";
    if (segs[0] === "catalog") {
      state.fleetTab = "catalog";
      const validSubs = ["skillshare", "prompts", "launchers", "profiles", "apps"];
      state.catalogSub = validSubs.includes(segs[1]) ? segs[1] : "skillshare";
      if (state.catalogSub === "skillshare") {
        const rest = rawSegs.slice(2).join("/");
        state.ssSubPath = normalizeSubPath((rest ? "/" + rest : "") + tailQS);
      } else if (state.catalogSub === "apps" && ["t3", "herdr", "openclaw"].includes(segs[2])) {
        state.appSub = segs[2];
      }
      return;
    }
    if (segs[0] === "matrix") {
      state.fleetTab = "matrix";
      if (segs[1] === "profile" && segs[2]) {
        state.editingProfile = segs[2];
        const validPCats = ["all", "skills", "mcp", "hooks", "extras", "agents", "plugins", "prompts", "launchers"];
        state.profileMatrixTab = (segs[3] && validPCats.includes(segs[3])) ? segs[3] : "all";
        return;
      }
      state.editingProfile = null;
      const validCats = ["profiles", "skills", "mcp", "hooks", "extras", "agents", "plugins", "prompts", "launchers"];
      if (segs[1] && validCats.includes(segs[1])) state.matrixTab = segs[1];
      return;
    }
    state.editingProfile = null;
    state.fleetTab = "environments";
  }

  async function goFleet(tab = "environments", sub = null, pushUrl = true) {
    const wasCatalogSS = state.scope === "fleet" && state.fleetTab === "catalog" && state.catalogSub === "skillshare";
    state.scope = "fleet";
    state.fleetTab = tab;
    if (pushUrl && tab !== "matrix") state.editingProfile = null;
    if (pushUrl && tab === "matrix" && sub === null) state.editingProfile = null;
    if (tab === "catalog" && sub) state.catalogSub = sub;
    const isCatalogSS = tab === "catalog" && state.catalogSub === "skillshare";
    if (pushUrl && (!isCatalogSS || !wasCatalogSS)) state.ssSubPath = "";
    if (pushUrl) syncURL(false);
    closeMenu();
    renderTopbar();
    if (tab === "catalog") {
      if (state.catalogSub === "prompts") {
        try { await loadPrompts(true); } catch (err) { toast(err.message, "err"); }
      } else if (state.catalogSub === "launchers" || state.catalogSub === "apps") {
        try { await loadProfilesAndApps(true); } catch (err) { toast(err.message, "err"); }
      }
    }
    renderWorkspace();
    if (isCatalogSS) ensureCatalogSkillshare(false);
  }

  function openProfileMatrix(profileID, catTab = null) {
    state.scope = "fleet";
    state.fleetTab = "matrix";
    state.editingProfile = profileID;
    state.profileMatrixTab = catTab || "all";
    state.ssSubPath = "";
    syncURL(false);
    closeMenu();
    renderTopbar();
    renderWorkspace();
  }

  async function goEnv(tid, tab = "overview", pushUrl = true) {
    const wasInSS = activeSkillshareSlotKey() !== "";
    const sameHost = state.scope === "env" && state.selectedTarget === tid;
    state.scope = "env";
    state.selectedTarget = tid;
    if (tab) state.envTab = tab;
    if (pushUrl) {
      if (state.envTab !== "skillshare") {
        state.ssSubPath = "";
      } else if (!sameHost) {
        // Switching hosts while inside Skillshare keeps the top-level section (e.g. /targets, /skills, /mcp)
        state.ssSubPath = wasInSS ? topSectionOnly(state.ssSubPath) : "";
      }
      syncURL(false);
    }
    closeMenu();
    renderTopbar();
    if (!state.bundles[tid]) {
      document.getElementById("main-content").innerHTML = `<div class="empty-center">Loading ${esc(tid)}…</div>`;
    }
    try {
      const b = await loadBundle(tid, true);
      renderTopbar();
      renderWorkspace();
      if (state.envTab === "skillshare") ensureEnvSkillshare(tid, false);
      if (!b || !b.sync_status) {
        apiPost(`/api/targets/${encodeURIComponent(tid)}/sync-preview`, {})
          .then(() => refreshState(true))
          .catch(() => {});
      }
    } catch (err) {
      toast(`Failed to load ${tid}: ${err.message}`, "err");
    }
  }

  document.getElementById("brand-home").onclick = () => goFleet("environments");
  document.getElementById("env-switcher-btn").onclick = (e) => {
    e.stopPropagation();
    state.menuOpen = !state.menuOpen;
    const menu = document.getElementById("env-switcher-menu");
    menu.classList.toggle("hidden", !state.menuOpen);
    document.getElementById("env-switcher-btn").setAttribute("aria-expanded", String(state.menuOpen));
    if (state.menuOpen) renderSwitcherMenu();
  };
  document.addEventListener("click", (e) => {
    if (state.menuOpen && !document.getElementById("switcher-wrap").contains(e.target)) closeMenu();
  });

  function renderSwitcherMenu() {
    if (!state.data) return;
    const { targets } = state.data;
    const menu = document.getElementById("env-switcher-menu");
    menu.innerHTML = `
      <button class="menu-item ${state.scope === "fleet" && state.fleetTab === "environments" ? "active" : ""}" data-fl="environments">
        <span>Environments</span>
        <span class="xs subtle">${targets.length}</span>
      </button>
      <button class="menu-item ${state.scope === "fleet" && state.fleetTab === "catalog" ? "active" : ""}" data-fl="catalog">
        <span>Catalog</span>
      </button>
      <button class="menu-item ${state.scope === "fleet" && state.fleetTab === "matrix" ? "active" : ""}" data-fl="matrix">
        <span>Matrix</span>
      </button>
      <div class="menu-sep"></div>
      <div class="menu-heading">Host Environments</div>
      ${targets.map((t) => {
        const sum = targetSyncSummary(t);
        return `
        <button class="menu-item ${state.scope === "env" && state.selectedTarget === t.id ? "active" : ""}" data-env="${esc(t.id)}">
          <span class="mono small">${esc(t.id)}</span>
          ${renderSyncPill(sum, true)}
        </button>`;
      }).join("")}
      <div class="menu-sep"></div>
      <button class="menu-item" id="menu-add-env"><span>+ Add host environment…</span></button>
    `;
    for (const b of menu.querySelectorAll("[data-fl]")) b.onclick = () => goFleet(b.dataset.fl);
    for (const b of menu.querySelectorAll("[data-env]")) {
      b.onclick = () => {
        const stayInSS = (state.scope === "env" && state.envTab === "skillshare")
          || (state.scope === "fleet" && state.fleetTab === "catalog" && state.catalogSub === "skillshare");
        goEnv(b.dataset.env, stayInSS ? "skillshare" : "overview");
      };
    }
    document.getElementById("menu-add-env").onclick = () => { closeMenu(); openAddEnvModal(); };
  }

  function renderTopbar() {
    if (!state.data) return;
    const { repo } = state.data;

    const lbl = document.getElementById("switcher-label");
    if (state.scope === "fleet") {
      const titles = { environments: "Environments", catalog: "Catalog", matrix: "Matrix" };
      lbl.textContent = titles[state.fleetTab] || "Environments";
      lbl.className = "switcher-label";
    } else {
      lbl.textContent = state.selectedTarget || "";
      lbl.className = "switcher-label mono";
    }

    const tabs = state.scope === "fleet"
      ? [["environments", "Environments"], ["catalog", "Catalog"], ["matrix", "Matrix"]]
      : [["overview", "Overview"], ["skillshare", "Skillshare"]];
    const active = state.scope === "fleet" ? state.fleetTab : state.envTab;

    const tabsEl = document.getElementById("topbar-tabs");
    tabsEl.innerHTML = tabs
      .map(([id, text]) => `<button class="seg-tab ${active === id ? "active" : ""}" data-tab="${id}">${text}</button>`)
      .join("");
    for (const b of tabsEl.querySelectorAll("[data-tab]")) {
      b.onclick = () => {
        if (state.scope === "fleet") goFleet(b.dataset.tab);
        else goEnv(state.selectedTarget, b.dataset.tab);
      };
    }

    // Inside a host environment, show ONLY the single unified Sync button (+ reload icon on Skillshare)
    const slotCtl = document.getElementById("topbar-slot-controls");
    if (state.scope === "env" && state.selectedTarget) {
      const tid = state.selectedTarget;
      const tInfo = (state.data.targets || []).find((x) => x.id === tid);
      const sum = targetSyncSummary(tInfo);
      const isChecking = Boolean(state.checkingTargets[tid]);
      const isCTA = sum.outOfSync;
      const syncLabel = isCTA ? `Sync · ${sum.total}` : "Sync";
      const iconHtml = isChecking
        ? `<span class="spinner"></span>`
        : isCTA
          ? `<span class="dot warn"></span>`
          : "";
      slotCtl.classList.remove("hidden");
      slotCtl.innerHTML = `
        <button id="tb-sync-modal" class="btn btn-sm ${isCTA ? "btn-pri" : ""}" title="${isChecking ? "Checking Skillshare changes…" : isCTA ? "Changes ready to sync — click to review & apply" : "Check & sync changes in either direction"}">
          ${iconHtml}${esc(syncLabel)}
        </button>
        ${state.envTab === "skillshare" ? `<button id="tb-ss-reload" class="icon-btn" title="Reconnect Skillshare">${ICONS.refresh}</button>` : ""}
      `;
      document.getElementById("tb-sync-modal").onclick = () => openSyncModal(tid);
      const rel = document.getElementById("tb-ss-reload");
      if (rel) rel.onclick = () => ensureEnvSkillshare(tid, true, "auto");
    } else {
      slotCtl.classList.add("hidden");
      slotCtl.innerHTML = "";
    }

    const gitBtn = document.getElementById("git-status-btn");
    const gitDot = document.getElementById("git-dot");
    const gitLbl = document.getElementById("git-status-label");
    gitBtn.classList.toggle("warn", Boolean(repo.dirty));
    gitDot.className = `dot ${repo.dirty ? "warn" : "ok"}`;
    gitLbl.textContent = repo.dirty ? `${repo.git_status_count} changed` : "clean";
  }

  function renderWorkspace() {
    if (!state.data) return;
    if (state.scope === "fleet") {
      if (state.fleetTab === "environments") renderEnvironments();
      else if (state.fleetTab === "catalog") renderCatalog();
      else if (state.fleetTab === "matrix") renderMatrix();
    } else if (state.selectedTarget) {
      const tid = state.selectedTarget;
      const b = state.bundles[tid];
      if (!b) return;
      if (state.envTab === "skillshare") renderEnvSkillshareFrame(tid);
      else renderEnvOverview(tid, b);
    }
  }

  // ==============================================================================================
  // 1. ENVIRONMENTS (Hosts + Projects)
  // ==============================================================================================

  function renderEnvironments() {
    const { targets, herdr, onboard, repo } = state.data;
    const op = state.opsOutput["fleet"];
    const actionSteps = (onboard?.steps || []).filter((s) => s.status === "action");
    const shortSha = (herdr?.expected_sha256 || "").slice(0, 10);

    const main = document.getElementById("main-content");
    main.innerHTML = `
      <div class="page-scroll">
        <div class="page-narrow">
          <div class="page-head">
            <div>
              <h1 class="page-title">Environments</h1>
              <p class="page-sub">Host destinations receiving items from your Catalog &amp; Matrix. Project folders are managed inside each host's Skillshare view.</p>
            </div>
            <div class="row">
              <button class="btn" id="fl-sync-status" ${state.syncChecking ? "disabled" : ""}>${state.syncChecking ? "Checking…" : "Refresh status"}</button>
              <button class="btn btn-pri" id="fl-sync-all" title="Push compiled Catalog &amp; Matrix rules to all hosts">Sync all</button>
            </div>
          </div>

          ${actionSteps.length ? `
            <details class="fold" open>
              <summary>
                <span class="row" style="gap:8px"><span class="dot warn"></span><span>Setup &amp; follow-up (${actionSteps.length})</span></span>
                <span class="xs subtle">checklist</span>
              </summary>
              <div>
                ${actionSteps.map((s) => `
                  <div class="list-row" style="padding:12px 20px">
                    <div>
                      <div style="font-weight:600;font-size:14px">${esc(s.title)}</div>
                      <div class="small muted">${esc(s.detail)}</div>
                      ${s.command ? `<div class="xs mono subtle" style="margin-top:3px">$ ${esc(s.command)}</div>` : ""}
                    </div>
                    ${s.action_id === "herdr-install-mod" ? `<button class="btn btn-sm btn-pri" data-onb-act="herdr-install-mod">Update mod</button>` : ""}
                    ${s.action_id === "open-add-host" ? `<button class="btn btn-sm" data-onb-act="open-add-host">Discover</button>` : ""}
                  </div>`).join("")}
              </div>
            </details>` : ""}

          <div class="card">
            <div class="card-head">
              <h2 class="card-title">Hosts (${targets.length})</h2>
              <button class="btn btn-sm" id="fl-add-host">+ Add host</button>
            </div>
            ${targets.map((t) => {
              const p = t.probe;
              const sum = targetSyncSummary(t);
              const bits = [
                `${t.counts.skills} skills`,
                `${t.counts.prompts} prompts`,
                t.counts.mcp ? `${t.counts.mcp} mcp` : "",
                t.counts.hooks ? `${t.counts.hooks} hooks` : "",
                `${t.profiles.length} profiles`,
                t.projects_count ? `${t.projects_count} projects` : "",
                (t.effective_skillshare_targets || []).join(", "),
              ].filter(Boolean);
              let statusPills = "";
              if (sum.outOfSync) {
                const dirParts = [];
                if (sum.pull) dirParts.push(`↑${sum.pull}`);
                if (sum.push) dirParts.push(`↓${sum.push}`);
                statusPills += `<button class="btn btn-sm btn-pri" data-open-sync="${esc(t.id)}" title="Review &amp; sync changes">Sync ${esc(dirParts.join(" "))}</button>`;
              } else if (sum.offline) {
                statusPills += `<span class="tag tag-bad" title="${esc(sum.error)}">offline</span>`;
              } else if (sum.known) {
                statusPills += `<span class="tag tag-ok">synced ✓</span>`;
              }
              if (p && !p.installed && !p.error) {
                statusPills += `<button class="btn btn-sm" data-install="${esc(t.id)}">Install skillshare</button>`;
              }
              return `
                <div class="list-row clickable" data-open="${esc(t.id)}">
                  <div>
                    <div class="row" style="gap:8px">
                      <span class="host-name">${esc(t.id)}</span>
                      ${t.is_local ? `<span class="tag">local</span>` : ""}
                      ${t.local_override_count ? `<span class="tag tag-accent">${t.local_override_count} local extra</span>` : ""}
                    </div>
                    <div class="host-meta">${esc(bits.join(" · "))}</div>
                  </div>
                  <div class="row">
                    ${statusPills}
                    <button class="btn btn-sm btn-ghost" data-ss="${esc(t.id)}">Skillshare</button>
                    <button class="btn btn-sm" data-ov="${esc(t.id)}">Open →</button>
                  </div>
                </div>`;
            }).join("")}
          </div>

          <div class="row-between" style="margin-bottom:18px">
            <div class="row">
              ${herdr && herdr.herdr_present ? `
                <button class="btn btn-sm btn-ghost" id="fl-herdr" title="Bundled sha256: ${esc(herdr.expected_sha256 || "")}">
                  Herdr mod: ${herdr.installed && herdr.up_to_date ? `verified ✓ (${esc(shortSha)})` : herdr.installed ? "update available" : "install"}
                </button>` : ""}
            </div>
            <div class="row">
              <button class="btn btn-sm btn-ghost" id="fl-probe">Probe hosts</button>
              <button class="btn btn-sm btn-ghost" id="fl-plan">Plan all</button>
              <button class="btn btn-sm btn-ghost" id="fl-audit">Audit skills</button>
              <button class="btn btn-sm btn-ghost" id="fl-doctor">Doctor</button>
              <button class="btn btn-sm btn-ghost" id="fl-secrets">Secrets parity</button>
            </div>
          </div>

          ${op ? renderOpCard(op) : ""}
        </div>
      </div>
    `;

    document.getElementById("fl-sync-status").onclick = () => runFleetSyncStatus(false);
    document.getElementById("fl-probe").onclick = () => runFleetOp("probe", "/api/fleet/probe", "Host Check");
    document.getElementById("fl-sync-all").onclick = () => confirmFleetSync();
    document.getElementById("fl-add-host").onclick = () => openAddEnvModal();
    document.getElementById("fl-plan").onclick = () => runFleetOp("plan", "/api/fleet/plan", "Fleet Plan");
    document.getElementById("fl-audit").onclick = () => runFleetOp("audit", "/api/fleet/audit", "Skill Security Audit");
    document.getElementById("fl-doctor").onclick = () => runFleetOp("doctor", "/api/fleet/doctor", "Fleet Doctor");
    document.getElementById("fl-secrets").onclick = () => runFleetOp("secrets", "/api/fleet/secrets-status", "Secrets Parity");
    const hbtn = document.getElementById("fl-herdr");
    if (hbtn) hbtn.onclick = () => installHerdrMod();
    for (const b of main.querySelectorAll("[data-onb-act]")) {
      b.onclick = () => {
        if (b.dataset.onbAct === "herdr-install-mod") installHerdrMod();
        else if (b.dataset.onbAct === "open-add-host") openAddEnvModal();
      };
    }
    for (const row of main.querySelectorAll("[data-open]")) {
      row.onclick = (e) => { if (!e.target.closest("button")) goEnv(row.dataset.open, "overview"); };
    }
    for (const b of main.querySelectorAll("[data-ov]")) b.onclick = () => goEnv(b.dataset.ov, "overview");
    for (const b of main.querySelectorAll("[data-open-sync]")) b.onclick = () => openSyncModal(b.dataset.openSync);
    for (const b of main.querySelectorAll("[data-ss]")) b.onclick = () => goEnv(b.dataset.ss, "skillshare");
    for (const b of main.querySelectorAll("[data-install]")) b.onclick = () => confirmInstallSkillshare(b.dataset.install);
  }

  // ==============================================================================================
  // 2. CATALOG (Full Library: Inside Skillshare + Outside Skillshare)
  // ==============================================================================================

  function renderCatalog() {
    const cat = state.data.catalog || {};
    const ss = cat.skillshare || {};
    const ssTotal = (ss.skills || []).length + (ss.mcp || []).length + (ss.hooks || []).length + (ss.extras || []).length + (ss.agents || []).length + (ss.plugins || []).length;

    const subs = [
      ["skillshare", `Skillshare Library (${ssTotal})`],
      ["prompts", `Prompts (${(cat.prompts || []).length})`],
      ["launchers", `Launchers (${cat.launchers_count || 0})`],
      ["profiles", `Profiles (${(cat.profiles || []).length})`],
      ["apps", "Local Apps"],
    ];

    const headerBar = `
      <div class="catalog-bar">
        <div class="seg-control">
          ${subs.map(([id, lbl]) => `<button class="seg-opt ${state.catalogSub === id ? "active" : ""}" data-csub="${id}">${esc(lbl)}</button>`).join("")}
        </div>
        <div class="row" style="gap:8px">
          <span class="xs subtle">
            ${state.catalogSub === "skillshare"
              ? `Inside Skillshare: Skills · Agents · Extras · MCP · Hooks · Plugins`
              : `Outside Skillshare: managed directly by Livery`}
          </span>
          ${state.catalogSub === "skillshare" ? `<button class="icon-btn" id="cat-reload-ss" title="Reload Skillshare Catalog">${ICONS.refresh}</button>` : ""}
        </div>
      </div>
    `;

    const main = document.getElementById("main-content");
    if (state.catalogSub === "skillshare") {
      const slot = state.ssSlotMeta["catalog"];
      if (!state.data.repo.local_skillshare_installed) {
        main.innerHTML = `${headerBar}<div class="empty-center"><p>Install <code>skillshare</code> (<code>brew install skillshare</code>) to use the embedded Skillshare Catalog.</p></div>`;
      } else if (!slot) {
        main.innerHTML = `${headerBar}<div class="empty-center">Starting standalone Skillshare Catalog…</div>`;
      } else {
        const existingFrame = main.querySelector("iframe.ss-iframe[data-slot-key='catalog']");
        if (existingFrame && existingFrame.dataset.slotBase === slot.url) {
          const curBar = main.querySelector(".catalog-bar");
          if (curBar) curBar.outerHTML = headerBar;
        } else {
          const iframeSrc = buildSlotIframeURL(slot.url, state.ssSubPath);
          main.innerHTML = `<div class="iframe-stage">${headerBar}<iframe class="ss-iframe" data-slot-key="catalog" data-slot-base="${esc(slot.url)}" src="${esc(iframeSrc)}"></iframe></div>`;
          const frame = main.querySelector("iframe.ss-iframe");
          frame.addEventListener("load", () => {
            syncIframeTheme(frame);
            hookIframeRouting(frame, slot.url, "catalog");
          });
        }
      }
      bindCatalogChrome(main);
      return;
    }

    let bodyHtml = "";
    if (state.catalogSub === "prompts") bodyHtml = renderCatalogPromptsBody();
    else if (state.catalogSub === "launchers") bodyHtml = renderCatalogLaunchersBody();
    else if (state.catalogSub === "profiles") bodyHtml = renderCatalogProfilesBody();
    else if (state.catalogSub === "apps") bodyHtml = renderCatalogAppsBody();

    main.innerHTML = `
      ${headerBar}
      <div class="page-scroll"><div class="page-narrow">${bodyHtml}</div></div>
    `;
    bindCatalogChrome(main);
    if (state.catalogSub === "prompts") bindPromptsHandlers(main);
    else if (state.catalogSub === "launchers") bindLaunchersHandlers(main);
    else if (state.catalogSub === "profiles") bindProfileBundlesHandlers(main);
    else if (state.catalogSub === "apps") bindAppsHandlers(main);
  }

  function bindCatalogChrome(root) {
    const toM = root.querySelector("#cat-to-matrix");
    if (toM) toM.onclick = () => goFleet("matrix");
    const rel = root.querySelector("#cat-reload-ss");
    if (rel) rel.onclick = () => { ensureCatalogSkillshare(true); refreshState(true); };
    for (const b of root.querySelectorAll("[data-csub]")) {
      b.onclick = () => goFleet("catalog", b.dataset.csub);
    }
  }

  // --- Catalog: Prompts ---

  function renderCatalogPromptsBody() {
    const pd = state.promptsData || { fragments: [], areas: [] };
    const all = pd.fragments || [];
    const areas = pd.areas || [];
    const q = (state.promptQuery || "").trim().toLowerCase();
    const canReorder = state.promptArea === "all" && !q;
    const filtered = all.filter((f) => {
      if (state.promptArea !== "all" && f.area !== state.promptArea) return false;
      if (!q) return true;
      return `${f.id} ${f.why || ""} ${f.body || ""} ${(f.profiles || []).join(" ")}`.toLowerCase().includes(q);
    });
    const firstLine = (body) => {
      const ln = String(body || "").split("\n").map((s) => s.trim()).find((s) => s && !s.startsWith("#")) || "";
      return ln.replace(/^[-*]\s+/, "");
    };
    return `
      <div class="page-head">
        <div>
          <h1 class="page-title">Prompt Fragments</h1>
          <p class="page-sub">Compiled into <code>~/.claude/CLAUDE.md</code> and <code>~/.codex/AGENTS.md</code> in global rank order (<code>#1</code> → <code>#${all.length}</code>) across all profiles and hosts.</p>
        </div>
        <button class="btn btn-pri" id="pm-new">+ New fragment</button>
      </div>
      <div class="row-between" style="margin-bottom:16px">
        <div class="chip-wrap">
          <button type="button" class="chip ${state.promptArea === "all" ? "on" : ""}" data-parea="all">all (${all.length})</button>
          ${areas.map((a) => `<button type="button" class="chip ${state.promptArea === a ? "on" : ""}" data-parea="${esc(a)}">${esc(a)} (${all.filter((f) => f.area === a).length})</button>`).join("")}
        </div>
        <input id="pm-search" class="input" style="width:210px" placeholder="Filter fragments…" value="${esc(state.promptQuery || "")}" />
      </div>
      <div class="card" id="prompts-list-card">
        ${filtered.map((f, idx) => `
          <div class="list-row clickable" data-open-frag="${esc(f.id)}" data-frag-idx="${idx}" ${canReorder ? `draggable="true"` : ""}>
            <div class="row" style="gap:12px;align-items:flex-start;flex-wrap:nowrap;min-width:0;flex:1">
              <div class="row" style="gap:6px;flex-wrap:nowrap;padding-top:2px">
                ${canReorder ? `<span class="drag-grip" title="Drag to reorder">⋮⋮</span>` : ""}
                <span class="rank-badge" title="${canReorder ? "Global compilation rank" : "Switch to 'all' and clear filter to reorder"}">#${f.rank || idx + 1}</span>
              </div>
              <div style="min-width:0;flex:1">
                <div class="row" style="gap:6px">
                  <span class="host-name">${esc(f.id)}</span>
                  ${(f.providers || []).map((p) => `<span class="tag">${esc(p)}</span>`).join("")}
                  ${f.scope && f.scope !== "user" ? `<span class="tag tag-accent">${esc(f.scope)}</span>` : ""}
                  <span class="xs subtle">${f.words || 0} words · ${(f.targets || []).length} hosts</span>
                </div>
                <div class="host-meta">${(f.profiles || []).length ? `In profiles: ${f.profiles.map((pr) => `<span class="mono">${esc(pr)}</span>`).join(", ")}` : `<span class="subtle">Direct assignment only</span>`}</div>
                <div class="prompt-preview">${esc(firstLine(f.body))}</div>
              </div>
            </div>
            <div class="row" style="flex-shrink:0">
              ${canReorder ? `
                <button type="button" class="icon-btn" data-move-frag="${esc(f.id)}" data-move-dir="-1" ${idx === 0 ? "disabled" : ""} title="Move earlier">↑</button>
                <button type="button" class="icon-btn" data-move-frag="${esc(f.id)}" data-move-dir="1" ${idx === filtered.length - 1 ? "disabled" : ""} title="Move later">↓</button>
              ` : ""}
              <button class="btn btn-sm" data-edit-frag="${esc(f.id)}">Edit</button>
              <button class="btn btn-sm btn-ghost" data-del-frag="${esc(f.id)}">Delete</button>
            </div>
          </div>`).join("") || `<div class="card-body small muted">No matching prompt fragments.</div>`}
      </div>
    `;
  }

  function bindPromptsHandlers(main) {
    const all = state.promptsData?.fragments || [];
    const reload = async () => { state.bundles = {}; await loadPrompts(true); await refreshState(true); };
    const persistOrder = async (nextIds) => {
      // Optimistic local update for snappy UI
      if (state.promptsData && Array.isArray(state.promptsData.fragments)) {
        const byId = Object.fromEntries(state.promptsData.fragments.map((x) => [x.id, x]));
        state.promptsData.fragments = nextIds.map((id, i) => ({ ...byId[id], rank: i + 1 })).filter((x) => x && x.id);
        renderCatalog();
      }
      try {
        await apiPost("/api/prompts/reorder", { order: nextIds });
        state.bundles = {};
        await loadPrompts(true);
        await refreshState(true);
      } catch (err) {
        toast(err.message, "err");
        await reload();
      }
    };

    const newBtn = main.querySelector("#pm-new");
    if (newBtn) newBtn.onclick = () => openPromptModal(null, reload);
    for (const b of main.querySelectorAll("[data-parea]")) b.onclick = () => { state.promptArea = b.dataset.parea; renderCatalog(); };
    const search = main.querySelector("#pm-search");
    if (search) {
      search.oninput = (e) => {
        const pos = e.target.selectionStart;
        state.promptQuery = e.target.value;
        renderCatalog();
        const again = document.getElementById("pm-search");
        if (again) { again.focus(); again.setSelectionRange(pos, pos); }
      };
    }

    for (const b of main.querySelectorAll("[data-move-frag]")) {
      b.onclick = (e) => {
        e.stopPropagation();
        const id = b.dataset.moveFrag;
        const dir = Number(b.dataset.moveDir);
        const ids = all.map((x) => x.id);
        const idx = ids.indexOf(id);
        const targetIdx = idx + dir;
        if (idx < 0 || targetIdx < 0 || targetIdx >= ids.length) return;
        ids.splice(idx, 1);
        ids.splice(targetIdx, 0, id);
        persistOrder(ids);
      };
    }

    let dragId = null;
    for (const row of main.querySelectorAll("[data-open-frag]")) {
      const f = all.find((x) => x.id === row.dataset.openFrag);
      if (f) {
        row.onclick = (e) => {
          if (e.target.closest("button") || e.target.closest(".drag-grip")) return;
          openPromptModal(f, reload);
        };
      }
      if (row.getAttribute("draggable") === "true") {
        row.addEventListener("dragstart", (e) => {
          dragId = row.dataset.openFrag;
          row.classList.add("dragging");
          if (e.dataTransfer) {
            e.dataTransfer.effectAllowed = "move";
            e.dataTransfer.setData("text/plain", dragId);
          }
        });
        row.addEventListener("dragend", () => {
          dragId = null;
          row.classList.remove("dragging");
          for (const r of main.querySelectorAll(".list-row")) r.classList.remove("drag-over-top", "drag-over-bottom");
        });
        row.addEventListener("dragover", (e) => {
          if (!dragId || dragId === row.dataset.openFrag) return;
          e.preventDefault();
          const rect = row.getBoundingClientRect();
          const before = (e.clientY - rect.top) < rect.height / 2;
          row.classList.toggle("drag-over-top", before);
          row.classList.toggle("drag-over-bottom", !before);
        });
        row.addEventListener("dragleave", () => {
          row.classList.remove("drag-over-top", "drag-over-bottom");
        });
        row.addEventListener("drop", (e) => {
          e.preventDefault();
          row.classList.remove("drag-over-top", "drag-over-bottom");
          const targetId = row.dataset.openFrag;
          if (!dragId || !targetId || dragId === targetId) return;
          const rect = row.getBoundingClientRect();
          const before = (e.clientY - rect.top) < rect.height / 2;
          const ids = all.map((x) => x.id).filter((x) => x !== dragId);
          let insertAt = ids.indexOf(targetId);
          if (insertAt < 0) return;
          if (!before) insertAt += 1;
          ids.splice(insertAt, 0, dragId);
          dragId = null;
          persistOrder(ids);
        });
      }
    }
    for (const b of main.querySelectorAll("[data-edit-frag]")) {
      const f = all.find((x) => x.id === b.dataset.editFrag);
      if (f) b.onclick = () => openPromptModal(f, reload);
    }
    for (const b of main.querySelectorAll("[data-del-frag]")) {
      b.onclick = () => {
        const id = b.dataset.delFrag;
        openModal({
          title: `Delete prompt fragment ${id}`,
          bodyHtml: `<p style="margin:0">Delete <code>prompts/fragments/${esc(id)}.md</code> and detach from profiles/hosts?</p>`,
          buttons: [
            { label: "Cancel", cls: "btn" },
            {
              label: "Delete", cls: "btn btn-bad",
              onClick: async () => {
                try {
                  await apiPost("/api/prompts/delete", { id, confirm: true });
                  toast(`Deleted ${id}`, "ok");
                  await reload();
                } catch (err) { toast(err.message, "err"); }
              },
            },
          ],
        });
      };
    }
  }

  function openPromptModal(existing, onSaved) {
    const pd = state.promptsData || { profiles: [], scopes: ["user", "host", "role-pointer"], providers: ["claude", "codex"] };
    const isEdit = Boolean(existing);
    const f = existing || { id: "", providers: ["claude", "codex"], scope: "user", why: "", body: "", profiles: [] };
    const selectedProvs = new Set(f.providers || ["claude", "codex"]);
    const selectedProfs = new Set(f.profiles || []);
    const allProfs = (state.data?.catalog?.profiles || []).map((p) => p.id);

    openModal({
      title: isEdit ? `Edit fragment · ${f.id}` : "New prompt fragment",
      wide: true,
      bodyHtml: `
        <div class="field-grid">
          <div><label class="field-label">Fragment ID (<code>&lt;area&gt;/&lt;name&gt;</code>)</label><input id="pf-id" class="input mono" style="width:100%" value="${esc(f.id)}" placeholder="style/typescript-rules" ${isEdit ? "disabled" : ""} /></div>
          <div><label class="field-label">Scope</label><select id="pf-scope" class="input" style="width:100%">${(pd.scopes || ["user", "host", "role-pointer"]).map((sc) => `<option value="${esc(sc)}" ${f.scope === sc ? "selected" : ""}>${esc(sc)}</option>`).join("")}</select></div>
        </div>
        <div class="field-grid">
          <div><label class="field-label">Providers</label><div class="chip-wrap">${(pd.providers || ["claude", "codex"]).map((pr) => `<button type="button" class="chip ${selectedProvs.has(pr) ? "on" : ""}" data-pf-prov="${esc(pr)}">${esc(pr)}</button>`).join("")}</div></div>
          <div><label class="field-label">Why / provenance</label><input id="pf-why" class="input" style="width:100%" value="${esc(f.why || "")}" /></div>
        </div>
        <div style="margin-bottom:14px">
          <label class="field-label">Included in profiles</label>
          <div class="chip-wrap">${allProfs.map((pid) => `<button type="button" class="chip ${selectedProfs.has(pid) ? "on" : ""}" data-pf-prof="${esc(pid)}">${esc(pid)}</button>`).join("")}</div>
        </div>
        <div><label class="field-label">Markdown body</label><textarea id="pf-body" class="input mono" style="width:100%;min-height:240px;line-height:1.5">${esc(f.body || "")}</textarea></div>
      `,
      buttons: [
        { label: "Cancel", cls: "btn" },
        {
          label: isEdit ? "Save fragment" : "Create fragment", cls: "btn btn-pri", close: false,
          onClick: async () => {
            const id = isEdit ? f.id : document.getElementById("pf-id").value.trim();
            try {
              await apiPost("/api/prompts/save", {
                id, providers: [...selectedProvs], scope: document.getElementById("pf-scope").value,
                why: document.getElementById("pf-why").value.trim(), body: document.getElementById("pf-body").value,
                profiles: [...selectedProfs], update_profiles: true,
              });
              closeModal();
              state.bundles = {};
              toast(`Saved ${id}`, "ok");
              if (onSaved) await onSaved();
            } catch (err) { toast(err.message, "err"); }
          },
        },
      ],
    });
    for (const b of document.querySelectorAll("[data-pf-prov]")) {
      b.onclick = () => { const pr = b.dataset.pfProv; selectedProvs.has(pr) ? selectedProvs.delete(pr) : selectedProvs.add(pr); b.classList.toggle("on", selectedProvs.has(pr)); };
    }
    for (const b of document.querySelectorAll("[data-pf-prof]")) {
      b.onclick = () => { const pid = b.dataset.pfProf; selectedProfs.has(pid) ? selectedProfs.delete(pid) : selectedProfs.add(pid); b.classList.toggle("on", selectedProfs.has(pid)); };
    }
  }

  // --- Catalog: Launchers & Session Groups ---

  function renderCatalogLaunchersBody() {
    const pd = state.profilesData || { profiles: [], blueprints: [], groups: [] };
    return `
      <div class="page-head">
        <div>
          <h1 class="page-title">Launchers &amp; Session Groups</h1>
          <p class="page-sub"><code>liv &lt;harness&gt; &lt;profile&gt;</code> specs with auth isolation and shared memory groups.</p>
        </div>
        <button class="btn btn-pri" id="pr-new">+ New launcher</button>
      </div>
      <div class="card">
        ${pd.profiles.map((p) => `
          <div class="list-row">
            <div>
              <div class="row" style="gap:8px">
                <span class="host-name">${esc(p.command)}</span>
                ${p.auth_mode ? `<span class="tag">${esc(p.auth_mode)}</span>` : ""}
                ${p.session_group ? `<span class="tag tag-accent">grp:${esc(p.session_group)}</span>` : `<span class="tag">${esc(p.mode || "direct")}</span>`}
                ${!p.secrets_ok ? `<span class="tag tag-bad" title="${esc((p.missing_secrets || []).join(", "))}">missing secret</span>` : ""}
              </div>
              <div class="host-meta">${esc(p.description || p.id)}</div>
            </div>
            <div class="row">
              <button class="btn btn-sm" data-edit-prof="${esc(p.id)}">Edit</button>
              <button class="btn btn-sm btn-ghost" data-del-prof="${esc(p.id)}">Delete</button>
            </div>
          </div>`).join("") || `<div class="card-body small muted">No launchers yet.</div>`}
      </div>
      <div class="card">
        <div class="card-head"><h2 class="card-title">Memory &amp; Session Groups</h2><span class="xs subtle">~/.local/share/livery/groups</span></div>
        ${pd.profiles.map((p) => `
          <div class="list-row">
            <div><div class="mono small" style="font-weight:600">${esc(p.command)}</div><div class="xs muted">${esc(p.description || p.id)}</div></div>
            <div class="row">
              <input class="input mono" style="width:150px" placeholder="default" value="${esc(p.session_group || "")}" data-grp-in="${esc(p.id)}" />
              <button class="btn btn-sm" data-grp-save="${esc(p.id)}">Set</button>
            </div>
          </div>`).join("")}
      </div>
    `;
  }

  function bindLaunchersHandlers(main) {
    const pd = state.profilesData || { profiles: [], blueprints: [] };
    const newBtn = main.querySelector("#pr-new");
    if (newBtn) newBtn.onclick = () => openLauncherModal(null, pd.blueprints || []);
    for (const b of main.querySelectorAll("[data-edit-prof]")) {
      const item = pd.profiles.find((x) => x.id === b.dataset.editProf);
      b.onclick = () => openLauncherModal(item, pd.blueprints || []);
    }
    for (const b of main.querySelectorAll("[data-del-prof]")) {
      b.onclick = () => {
        const id = b.dataset.delProf;
        openModal({
          title: `Delete launcher ${id}`,
          bodyHtml: `<p style="margin:0">Remove <code>launchers/${esc(id)}.toml</code>?</p>`,
          buttons: [
            { label: "Cancel", cls: "btn" },
            {
              label: "Delete", cls: "btn btn-bad",
              onClick: async () => {
                try {
                  await apiPost("/api/profiles/delete", { id, confirm: true });
                  toast(`Deleted ${id}`, "ok");
                  await loadProfilesAndApps(true);
                  await refreshState(true);
                } catch (err) { toast(err.message, "err"); }
              },
            },
          ],
        });
      };
    }
    for (const b of main.querySelectorAll("[data-grp-save]")) {
      b.onclick = async () => {
        const id = b.dataset.grpSave;
        const val = main.querySelector(`[data-grp-in="${CSS.escape(id)}"]`)?.value.trim() || "";
        try {
          await apiPost("/api/groups/set", { id, group: val });
          toast(val ? `${id} → group "${val}"` : `${id} → default pool`, "ok");
          await loadProfilesAndApps(true);
          renderCatalog();
        } catch (err) { toast(err.message, "err"); }
      };
    }
  }

  function openLauncherModal(existing, blueprints) {
    const isEdit = Boolean(existing);
    const p = existing || { harness: "claude", mode: "shadow", auth_mode: "gateway" };
    openModal({
      title: isEdit ? `Edit ${p.command}` : "Onboard new launcher",
      bodyHtml: `
        ${!isEdit && blueprints.length ? `<div class="chip-wrap" style="margin-bottom:14px">${blueprints.map((bp, i) => `<button type="button" class="chip" data-bp="${i}">${esc(bp.title)}</button>`).join("")}</div>` : ""}
        <div class="field-grid">
          <div><label class="field-label">Harness</label><select id="lp-harness" class="input" style="width:100%" ${isEdit ? "disabled" : ""}>${["claude", "codex", "opencode", "openclaw", "agy"].map((h) => `<option value="${h}" ${p.harness === h ? "selected" : ""}>${h}</option>`).join("")}</select></div>
          <div><label class="field-label">Short profile name</label><input id="lp-prof" class="input mono" style="width:100%" value="${esc(p.profile || "")}" placeholder="gateway / work" ${isEdit ? "disabled" : ""} /></div>
        </div>
        <div class="field-grid">
          <div><label class="field-label">Auth mode</label><input id="lp-auth" class="input mono" style="width:100%" value="${esc(p.auth_mode || "")}" /></div>
          <div><label class="field-label">Isolation mode</label><select id="lp-mode" class="input" style="width:100%">${["shadow", "isolated", "direct"].map((m) => `<option value="${m}" ${p.mode === m ? "selected" : ""}>${m}</option>`).join("")}</select></div>
        </div>
        <div class="field-grid">
          <div><label class="field-label">Session group</label><input id="lp-group" class="input mono" style="width:100%" value="${esc(p.session_group || "")}" /></div>
          <div><label class="field-label">Description</label><input id="lp-desc" class="input" style="width:100%" value="${esc(p.description || "")}" /></div>
        </div>
        <div class="field-grid">
          <div><label class="field-label">Base URL</label><input id="lp-url" class="input mono" style="width:100%" value="${esc(p.base_url || "")}" /></div>
          <div><label class="field-label">Secret ref (<code>store:&lt;name&gt;</code>)</label><input id="lp-sec" class="input mono" style="width:100%" value="${esc(p.token_secret || "")}" /></div>
        </div>
        <div class="field-grid">
          <div><label class="field-label">Env key</label><input id="lp-envk" class="input mono" style="width:100%" value="${esc(p.env_key || "")}" /></div>
          <div><label class="field-label">Default model</label><input id="lp-model" class="input mono" style="width:100%" value="${esc(p.model || "")}" /></div>
        </div>
      `,
      buttons: [
        { label: "Cancel", cls: "btn" },
        {
          label: isEdit ? "Save" : "Create", cls: "btn btn-pri", close: false,
          onClick: async () => {
            try {
              const res = await apiPost("/api/profiles/save", {
                id: isEdit ? p.id : "",
                harness: document.getElementById("lp-harness").value,
                profile: document.getElementById("lp-prof").value.trim(),
                auth_mode: document.getElementById("lp-auth").value.trim(),
                mode: document.getElementById("lp-mode").value,
                session_group: document.getElementById("lp-group").value.trim(),
                description: document.getElementById("lp-desc").value.trim(),
                base_url: document.getElementById("lp-url").value.trim(),
                token_secret: document.getElementById("lp-sec").value.trim(),
                env_key: document.getElementById("lp-envk").value.trim(),
                model: document.getElementById("lp-model").value.trim(),
              });
              closeModal();
              toast(`Saved ${res.command}`, "ok");
              await loadProfilesAndApps(true);
              await refreshState(true);
            } catch (err) { toast(err.message, "err"); }
          },
        },
      ],
    });
    for (const b of document.querySelectorAll("[data-bp]")) {
      const bp = blueprints[Number(b.dataset.bp)];
      b.onclick = () => {
        document.getElementById("lp-harness").value = bp.harness;
        document.getElementById("lp-auth").value = bp.auth_mode;
        document.getElementById("lp-mode").value = bp.mode;
        if (bp.base_url_hint) document.getElementById("lp-url").value = bp.base_url_hint;
        if (bp.secret_hint) document.getElementById("lp-sec").value = bp.secret_hint;
        if (bp.env_key_hint) document.getElementById("lp-envk").value = bp.env_key_hint;
      };
    }
  }

  // --- Catalog: Profiles (Upper layer of Matrix!) ---

  function renderCatalogProfilesBody() {
    const profs = state.data.catalog?.profiles || [];
    return `
      <div class="page-head">
        <div>
          <h1 class="page-title">Profiles</h1>
          <p class="page-sub">Higher-level Matrix presets bundling skills, MCPs, hooks, prompts, and launchers. Also editable directly in <strong>Matrix → Profiles</strong>.</p>
        </div>
        <button class="btn btn-pri" id="pb-new">+ New profile</button>
      </div>
      <div class="card">
        ${profs.map((pr) => {
          const bits = [
            pr.extends?.length ? `extends: ${pr.extends.join(", ")}` : "",
            pr.skills?.length ? `${pr.skills.length} skills` : "",
            pr.mcp?.length ? `${pr.mcp.length} mcp` : "",
            pr.hooks?.length ? `${pr.hooks.length} hooks` : "",
            pr.agents?.length ? `${pr.agents.length} agents` : "",
            pr.extras?.length ? `${pr.extras.length} extras` : "",
            pr.plugins?.length ? `${pr.plugins.length} plugins` : "",
            pr.fragments?.length ? `${pr.fragments.length} prompts` : "",
            pr.launchers?.length ? `${pr.launchers.length} launchers` : "",
          ].filter(Boolean);
          return `
            <div class="list-row clickable" data-open-pb="${esc(pr.id)}">
              <div>
                <div class="host-name">${esc(pr.id)}</div>
                <div class="small muted">${esc(pr.description || "")}</div>
                <div class="xs mono subtle" style="margin-top:3px">${esc(bits.join(" · ") || "empty")}</div>
              </div>
              <div class="row">
                <button class="btn btn-sm" data-edit-pb="${esc(pr.id)}">Edit</button>
                <button class="btn btn-sm btn-ghost" data-del-pb="${esc(pr.id)}">Delete</button>
              </div>
            </div>`;
        }).join("")}
      </div>
    `;
  }

  function bindProfileBundlesHandlers(main) {
    const newBtn = main.querySelector("#pb-new");
    if (newBtn) newBtn.onclick = () => openNewProfileModal();
    for (const row of main.querySelectorAll("[data-open-pb]")) {
      row.onclick = (e) => { if (!e.target.closest("button")) openProfileMatrix(row.dataset.openPb); };
    }
    for (const b of main.querySelectorAll("[data-edit-pb]")) {
      b.onclick = () => openProfileMatrix(b.dataset.editPb);
    }
    for (const b of main.querySelectorAll("[data-del-pb]")) {
      b.onclick = () => confirmDeleteProfileBundle(b.dataset.delPb);
    }
  }

  function confirmDeleteProfileBundle(id) {
    openModal({
      title: `Delete profile "${id}"`,
      bodyHtml: `<p style="margin:0">Delete <code>profiles/${esc(id)}.toml</code> and detach it from any environments or profiles extending it?</p>`,
      buttons: [
        { label: "Cancel", cls: "btn" },
        {
          label: "Delete profile", cls: "btn btn-bad",
          onClick: async () => {
            try {
              await apiPost("/api/profile-bundles/delete", { id, confirm: true });
              state.bundles = {};
              if (state.editingProfile === id) {
                state.editingProfile = null;
                state.matrixTab = "profiles";
                syncURL(true);
              }
              toast(`Deleted profile ${id}`, "ok");
              await refreshState(true);
            } catch (err) { toast(err.message, "err"); }
          },
        },
      ],
    });
  }

  function openNewProfileModal() {
    const allProfs = (state.data.catalog?.profiles || []).map((x) => x.id);
    const exts = new Set();
    openModal({
      title: "New profile",
      bodyHtml: `
        <div class="field-grid">
          <div><label class="field-label">Profile ID</label><input id="np-id" class="input mono" style="width:100%" placeholder="dev-workstation" /></div>
          <div><label class="field-label">Description</label><input id="np-desc" class="input" style="width:100%" placeholder="Optional summary" /></div>
        </div>
        ${allProfs.length ? `
          <div>
            <label class="field-label">Extends profiles (optional)</label>
            <div class="chip-wrap">${allProfs.map((id) => `<button type="button" class="chip" data-np-ext="${esc(id)}">${esc(id)}</button>`).join("")}</div>
          </div>` : ""}
      `,
      buttons: [
        { label: "Cancel", cls: "btn" },
        {
          label: "Create & edit in Matrix", cls: "btn btn-pri", close: false,
          onClick: async () => {
            const id = document.getElementById("np-id").value.trim();
            if (!id) { toast("Enter a profile ID", "warn"); return; }
            try {
              await apiPost("/api/profile-bundles/save", {
                id,
                description: document.getElementById("np-desc").value.trim(),
                extends: [...exts],
                skills: [], mcp: [], hooks: [], agents: [], extras: [], plugins: [], fragments: [], launchers: [],
              });
              closeModal();
              state.bundles = {};
              toast(`Created profile ${id}`, "ok");
              await refreshState(true);
              openProfileMatrix(id);
            } catch (err) { toast(err.message, "err"); }
          },
        },
      ],
    });
    for (const b of document.querySelectorAll("[data-np-ext]")) {
      b.onclick = () => {
        const v = b.dataset.npExt;
        exts.has(v) ? exts.delete(v) : exts.add(v);
        b.classList.toggle("on", exts.has(v));
      };
    }
  }

  // --- Catalog: Local Apps (T3 Code / Herdr / OpenClaw) ---

  function renderCatalogAppsBody() {
    const ad = state.appsData || { t3: { providers: [] }, herdr: { programs: [] }, openclaw: { profiles: [] } };
    const modSt = state.data?.herdr;
    const shortSha = (modSt?.expected_sha256 || "").slice(0, 12);
    return `
      <div class="page-head">
        <div>
          <h1 class="page-title">Local Companion Apps</h1>
          <p class="page-sub">Live local editors for T3 Code, Herdr (with mod checksum verification), and OpenClaw.</p>
        </div>
        <div class="seg-control">
          ${[["t3", `T3 Code (${(ad.t3?.providers || []).length})`], ["herdr", `Herdr (${(ad.herdr?.programs || []).length})`], ["openclaw", `OpenClaw (${(ad.openclaw?.profiles || []).length})`]].map(([id, lbl]) =>
            `<button class="seg-opt ${state.appSub === id ? "active" : ""}" data-asub="${id}">${esc(lbl)}</button>`).join("")}
        </div>
      </div>

      ${state.appSub === "t3" ? `
        <div class="card">
          <div class="card-head"><h2 class="card-title">T3 Code Providers</h2><span class="xs mono subtle">${esc(ad.t3?.settings_file || "")}</span></div>
          ${(ad.t3?.providers || []).map((p) => `
            <div class="list-row">
              <div>
                <div class="row" style="gap:8px"><span class="host-name">${esc(p.instance_id)}</span><span class="tag">${esc(p.driver)}</span><span class="tag ${p.enabled ? "tag-ok" : "tag-warn"}">${p.enabled ? "enabled" : "disabled"}</span></div>
                <div class="host-meta">${esc(p.display_name)}${p.binary_path ? ` · ${esc(p.binary_path)}` : ""}</div>
              </div>
              <button class="btn btn-sm" data-edit-t3="${esc(p.instance_id)}">Edit</button>
            </div>`).join("") || `<div class="card-body small muted">No T3 Code settings found.</div>`}
        </div>` : ""}

      ${state.appSub === "herdr" ? `
        ${modSt && modSt.herdr_present ? `
          <div class="card"><div class="list-row">
            <div>
              <div class="row" style="gap:8px"><span class="host-name">livery-launcher</span><span class="tag ${modSt.installed && modSt.up_to_date ? "tag-ok" : "tag-warn"}">${modSt.installed && modSt.up_to_date ? "checksum verified ✓" : "update available"}</span></div>
              <div class="xs mono subtle">sha256: ${esc(shortSha)} · Cmd+Shift+N → ${esc(modSt.key_value || "unbound")}</div>
            </div>
            <div class="row"><button class="btn btn-sm" id="hr-install-mod">Reinstall mod</button><button class="btn btn-sm btn-pri" id="hr-sync-all">Sync from launchers</button></div>
          </div></div>` : ""}
        <div class="card">
          <div class="card-head"><h2 class="card-title">Herdr Programs</h2><span class="xs mono subtle">${esc(ad.herdr?.programs_file || "")}</span></div>
          ${(ad.herdr?.programs || []).map((p) => `
            <div class="list-row">
              <div><div class="host-name">${esc(p.key)}</div><div class="host-meta">${esc(p.name)} · <span class="mono">${esc((p.run || []).join(" "))}</span></div></div>
              <button class="btn btn-sm btn-ghost" data-del-hr="${esc(p.key)}">Delete</button>
            </div>`).join("") || `<div class="card-body small muted">No Herdr programs.</div>`}
        </div>` : ""}

      ${state.appSub === "openclaw" ? `
        <div style="margin-bottom:14px"><button class="btn btn-pri btn-sm" id="oc-new-prof">+ New OpenClaw profile</button></div>
        ${(ad.openclaw?.profiles || []).map((p) => `
          <div class="card">
            <div class="card-head"><h2 class="card-title mono">openclaw · ${esc(p.name)}</h2><span class="xs mono subtle">${esc(p.config_path)}</span></div>
            <div class="card-body">
              <div class="chip-wrap">${(p.plugins || []).map((pl) => `<button type="button" class="chip ${pl.enabled ? "on" : ""}" data-oc-plug-prof="${esc(p.name)}" data-oc-plug="${esc(pl.name)}" data-oc-plug-en="${pl.enabled ? "1" : "0"}">${esc(pl.name)} ${pl.enabled ? "✓" : ""}</button>`).join("") || `<span class="xs subtle">no plugins</span>`}</div>
            </div>
          </div>`).join("") || `<div class="card"><div class="card-body small muted">No OpenClaw profiles found.</div></div>`}` : ""}
    `;
  }

  function bindAppsHandlers(main) {
    const ad = state.appsData || {};
    for (const b of main.querySelectorAll("[data-asub]")) b.onclick = () => { state.appSub = b.dataset.asub; syncURL(false); renderCatalog(); };
    const hi = main.querySelector("#hr-install-mod");
    if (hi) hi.onclick = () => installHerdrMod();
    const hs = main.querySelector("#hr-sync-all");
    if (hs) hs.onclick = async () => {
      try { const res = await apiPost("/api/apps/herdr/sync", {}); toast(`Synced ${res.synced} Herdr programs`, "ok"); await loadProfilesAndApps(true); renderCatalog(); }
      catch (err) { toast(err.message, "err"); }
    };
    for (const b of main.querySelectorAll("[data-del-hr]")) {
      b.onclick = async () => {
        try { await apiPost("/api/apps/herdr/delete", { key: b.dataset.delHr }); toast(`Removed ${b.dataset.delHr}`, "ok"); await loadProfilesAndApps(true); renderCatalog(); }
        catch (err) { toast(err.message, "err"); }
      };
    }
    for (const b of main.querySelectorAll("[data-edit-t3]")) {
      const prov = (ad.t3?.providers || []).find((x) => x.instance_id === b.dataset.editT3);
      if (prov) b.onclick = () => openT3Modal(prov);
    }
    const ocNew = main.querySelector("#oc-new-prof");
    if (ocNew) ocNew.onclick = () => openModal({
      title: "New OpenClaw profile",
      bodyHtml: `<label class="field-label">Profile name</label><input id="oc-pname" class="input mono" style="width:100%" placeholder="work" />`,
      buttons: [
        { label: "Cancel", cls: "btn" },
        {
          label: "Create", cls: "btn btn-pri",
          onClick: async () => {
            const name = document.getElementById("oc-pname").value.trim();
            if (!name) return;
            try { await apiPost("/api/apps/openclaw/create-profile", { name }); toast(`Created ${name}`, "ok"); await loadProfilesAndApps(true); renderCatalog(); }
            catch (err) { toast(err.message, "err"); }
          },
        },
      ],
    });
    for (const b of main.querySelectorAll("[data-oc-plug]")) {
      b.onclick = async () => {
        const nextEn = b.dataset.ocPlugEn !== "1";
        try { await apiPost("/api/apps/openclaw/plugin", { profile: b.dataset.ocPlugProf, plugin: b.dataset.ocPlug, enabled: nextEn }); await loadProfilesAndApps(true); renderCatalog(); }
        catch (err) { toast(err.message, "err"); }
      };
    }
  }

  function openT3Modal(p) {
    openModal({
      title: `Edit T3 provider · ${p.instance_id}`,
      bodyHtml: `
        <div class="field-grid">
          <div><label class="field-label">Display name</label><input id="t3-dn" class="input" style="width:100%" value="${esc(p.display_name)}" /></div>
          <div><label class="field-label">Binary path</label><input id="t3-bin" class="input mono" style="width:100%" value="${esc(p.binary_path)}" /></div>
        </div>
        <div style="margin-bottom:12px"><label class="field-label">Custom models (comma-separated)</label><input id="t3-models" class="input mono" style="width:100%" value="${esc((p.custom_models || []).join(", "))}" /></div>
        <label class="toggle-row"><span>Enabled</span><span class="switch"><input type="checkbox" id="t3-en" ${p.enabled ? "checked" : ""} /><span class="switch-track"></span></span></label>
      `,
      buttons: [
        { label: "Cancel", cls: "btn" },
        {
          label: "Save", cls: "btn btn-pri",
          onClick: async () => {
            try {
              await apiPost("/api/apps/t3/upsert", {
                instance_id: p.instance_id,
                display_name: document.getElementById("t3-dn").value.trim(),
                binary_path: document.getElementById("t3-bin").value.trim(),
                home_path: p.home_path, accent_color: p.accent_color, launch_args: p.launch_args,
                custom_models: document.getElementById("t3-models").value.split(",").map((x) => x.trim()).filter(Boolean),
                enabled: document.getElementById("t3-en").checked,
              });
              toast(`Updated ${p.instance_id}`, "ok");
              await loadProfilesAndApps(true);
              renderCatalog();
            } catch (err) { toast(err.message, "err"); }
          },
        },
      ],
    });
  }

  // ==============================================================================================
  // 3. INTERACTIVE EDITABLE MATRIX (Hosts × Catalog · Select All + Profile Guardrails + Inline Edit)
  // ==============================================================================================

  function renderMatrix() {
    if (state.editingProfile) {
      const catProfiles = state.data.catalog?.profiles || [];
      const pr = catProfiles.find((x) => x.id === state.editingProfile);
      if (pr) {
        renderProfileMatrix(pr);
        return;
      }
      state.editingProfile = null;
      syncURL(true);
    }

    const matrix = state.data.matrix || { envs: [], categories: {} };
    const envs = matrix.envs || [];
    const catsMap = matrix.categories || {};
    const catList = [
      ["profiles", "Profiles"],
      ["skills", "Skills"],
      ["mcp", "MCP"],
      ["hooks", "Hooks"],
      ["extras", "Extras"],
      ["agents", "Agents"],
      ["plugins", "Plugins"],
      ["prompts", "Prompts"],
      ["launchers", "Launchers"],
    ];
    const rows = catsMap[state.matrixTab] || [];
    const isProfilesTab = state.matrixTab === "profiles";

    const main = document.getElementById("main-content");
    main.innerHTML = `
      <div class="page-scroll">
        <div class="page-wide">
          <div class="page-head">
            <div>
              <h1 class="page-title">Matrix</h1>
              <p class="page-sub">${isProfilesTab
                ? `Assign reusable Profile bundles across hosts, or click a profile to open its Matrix.`
                : `Click any cell to enable/disable per host, or click <strong>All</strong> to toggle the whole row. Blue cells come from a Profile.`}</p>
            </div>
            <div class="row">
              ${isProfilesTab ? `<button class="btn" id="mx-new-profile">+ New profile</button>` : ""}
              <button class="btn btn-pri" id="mx-sync-all" title="Push compiled Catalog &amp; Matrix rules to all hosts">Push to all hosts</button>
            </div>
          </div>

          <div style="margin-bottom:18px;overflow-x:auto">
            <div class="seg-control">
              ${catList.map(([id, label]) => {
                const n = (catsMap[id] || []).length;
                return `<button class="seg-opt ${state.matrixTab === id ? "active" : ""}" data-mtab="${id}">${label} <span class="xs subtle">${n}</span></button>`;
              }).join("")}
            </div>
          </div>

          <div class="card">
            ${rows.length ? `
              <div class="table-wrap">
                <table class="matrix-table matrix-grid">
                  <thead>
                    <tr>
                      <th class="m-col-all">All</th>
                      <th class="m-col-item">${isProfilesTab ? "Profile" : "Item"}</th>
                      ${envs.map((env) => `
                        <th class="col-t" data-jump-env="${esc(env.id)}" title="${esc(env.id)}">
                          <div class="col-t-user">${esc(env.label)}</div>
                          <div class="col-t-host">${esc(env.sublabel || "")}</div>
                        </th>`).join("")}
                    </tr>
                  </thead>
                  <tbody>
                    ${rows.map((r) => `
                      <tr>
                        <td class="m-all m-col-all">
                          <button type="button" class="m-btn ${r.all_selected ? "m-all-on" : ""}" data-mx-row="${esc(r.id)}" data-mx-row-next="${r.all_selected ? "0" : "1"}" title="${r.all_selected ? "Clear all direct assignments" : "Enable on all hosts"}">
                            ${r.all_selected ? ICONS.check : "All"}
                          </button>
                        </td>
                        <td class="m-col-item">
                          <div class="row-between" style="gap:8px;flex-wrap:nowrap">
                            <div style="min-width:0;${isProfilesTab ? "cursor:pointer" : ""}" ${isProfilesTab ? `data-mx-edit-prof="${esc(r.id)}"` : ""} title="${esc(r.description ? `${r.label} — ${r.description}` : r.label)}">
                              <div class="mono" style="font-weight:500;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(r.label)}</div>
                              ${r.description ? `<div class="xs subtle" style="max-width:230px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(r.description)}</div>` : ""}
                            </div>
                            ${isProfilesTab ? `<button type="button" class="btn btn-sm btn-ghost" data-mx-edit-prof="${esc(r.id)}">Edit →</button>` : ""}
                          </div>
                        </td>
                        ${envs.map((env) => {
                          const c = (r.cells && r.cells[env.id]) || { supported: false };
                          if (!c.supported) return `<td class="m-cell"><span class="m-unsupported">—</span></td>`;
                          if (c.source === "profile") {
                            return `<td class="m-cell"><button type="button" class="m-btn m-profile" data-mx-cell="1" data-mx-item="${esc(r.id)}" data-mx-env="${esc(env.id)}" data-mx-next="0" title="Inherited from profile '${esc(c.profile)}' — click for options">${ICONS.check}</button></td>`;
                          }
                          if (c.enabled) {
                            return `<td class="m-cell"><button type="button" class="m-btn m-direct" data-mx-cell="1" data-mx-item="${esc(r.id)}" data-mx-env="${esc(env.id)}" data-mx-next="0" title="Directly enabled — click to disable">${ICONS.check}</button></td>`;
                          }
                          return `<td class="m-cell"><button type="button" class="m-btn" data-mx-cell="1" data-mx-item="${esc(r.id)}" data-mx-env="${esc(env.id)}" data-mx-next="1" title="Click to enable on ${esc(env.id)}">·</button></td>`;
                        }).join("")}
                      </tr>`).join("")}
                  </tbody>
                </table>
              </div>` : `<div class="card-body small muted">No items in Catalog for <strong>${esc(state.matrixTab)}</strong> yet. Add them in the <strong>Catalog</strong> tab first.</div>`}
          </div>
        </div>
      </div>
    `;

    document.getElementById("mx-sync-all").onclick = () => confirmFleetSync();
    const newProfBtn = document.getElementById("mx-new-profile");
    if (newProfBtn) newProfBtn.onclick = () => openNewProfileModal();
    for (const b of main.querySelectorAll("[data-mx-edit-prof]")) {
      b.onclick = () => openProfileMatrix(b.dataset.mxEditProf);
    }
    for (const b of main.querySelectorAll("[data-mtab]")) b.onclick = () => { state.matrixTab = b.dataset.mtab; syncURL(false); renderMatrix(); };
    for (const th of main.querySelectorAll("th[data-jump-env]")) th.onclick = () => goEnv(th.dataset.jumpEnv, "overview");

    for (const b of main.querySelectorAll("[data-mx-cell]")) {
      b.onclick = async () => {
        const category = state.matrixTab;
        const item = b.dataset.mxItem;
        const env = b.dataset.mxEnv;
        const enabled = b.dataset.mxNext === "1";
        const { ok, status, body } = await apiPostRaw("/api/matrix/cell", { category, item, env, enabled });
        if (status === 409 && body.conflict) {
          openProfileGuardrailModal(body.conflict);
          return;
        }
        if (!ok) {
          toast(body.error || "Update failed", "err");
          return;
        }
        state.bundles = {};
        await refreshState(true);
      };
    }

    for (const b of main.querySelectorAll("[data-mx-row]")) {
      b.onclick = async () => {
        const category = state.matrixTab;
        const item = b.dataset.mxRow;
        const enableAll = b.dataset.mxRowNext === "1";
        try {
          const res = await apiPost("/api/matrix/row", { category, item, enable_all: enableAll });
          state.bundles = {};
          const skipMsg = (res.skipped_profiles || []).length ? ` (${res.skipped_profiles.length} inherited via profile kept)` : "";
          toast(`${enableAll ? "Enabled" : "Cleared"} ${item} across ${(res.updated || []).length} env(s)${skipMsg}`, "ok");
          await refreshState(true);
        } catch (err) { toast(err.message, "err"); }
      };
    }
  }

  function renderProfileMatrix(pr) {
    const matrix = state.data.matrix || { envs: [], categories: {} };
    const envs = matrix.envs || [];
    const catsMap = matrix.categories || {};
    const allProfiles = state.data.catalog?.profiles || [];
    const profById = Object.fromEntries(allProfiles.map((x) => [x.id, x]));

    const catDefs = [
      { id: "skills", field: "skills", label: "Skills" },
      { id: "plugins", field: "plugins", label: "Plugins" },
      { id: "mcp", field: "mcp", label: "MCP" },
      { id: "hooks", field: "hooks", label: "Hooks" },
      { id: "agents", field: "agents", label: "Agents" },
      { id: "extras", field: "extras", label: "Extras" },
      { id: "prompts", field: "fragments", label: "Prompts" },
      { id: "launchers", field: "launchers", label: "Launchers" },
    ];

    // Resolve items inherited recursively from extended parent profiles
    const inheritedByCat = {};
    for (const c of catDefs) inheritedByCat[c.id] = {};
    const walkExt = (parentId, topParent, seen) => {
      if (seen.has(parentId)) return;
      seen.add(parentId);
      const p = profById[parentId];
      if (!p) return;
      for (const e of (p.extends || [])) walkExt(e, topParent, seen);
      for (const c of catDefs) {
        for (const item of (p[c.field] || [])) {
          if (!inheritedByCat[c.id][item]) inheritedByCat[c.id][item] = topParent;
        }
      }
    };
    for (const extId of (pr.extends || [])) walkExt(extId, extId, new Set([pr.id]));

    const directSets = {};
    for (const c of catDefs) directSets[c.id] = new Set(pr[c.field] || []);

    // Which hosts currently use this profile?
    const profRow = (catsMap.profiles || []).find((r) => r.id === pr.id);
    const assignedEnvs = new Set();
    for (const env of envs) {
      const cell = profRow && profRow.cells && profRow.cells[env.id];
      if (cell && cell.enabled) assignedEnvs.add(env.id);
    }

    const otherProfiles = allProfiles.filter((x) => x.id !== pr.id);
    const extSet = new Set(pr.extends || []);

    const itemsForCat = (c) => {
      const catRows = catsMap[c.id] || [];
      const rowById = Object.fromEntries(catRows.map((r) => [r.id, r]));
      const ids = Array.from(new Set([...catRows.map((r) => r.id), ...directSets[c.id], ...Object.keys(inheritedByCat[c.id])]));
      return ids.map((id) => ({
        id,
        cat: c,
        label: (rowById[id] && rowById[id].label) || id,
        description: (rowById[id] && rowById[id].description) || "",
        direct: directSets[c.id].has(id),
        inheritedFrom: inheritedByCat[c.id][id] || "",
      }));
    };

    const allItemsByCat = {};
    let totalEnabled = 0;
    let totalItems = 0;
    for (const c of catDefs) {
      const list = itemsForCat(c);
      allItemsByCat[c.id] = list;
      totalItems += list.length;
      totalEnabled += list.filter((x) => x.direct || x.inheritedFrom).length;
    }

    const activeCat = state.profileMatrixTab || "all";
    const visibleItems = activeCat === "all"
      ? catDefs.flatMap((c) => allItemsByCat[c.id])
      : (allItemsByCat[activeCat] || []);

    const saveProfilePatch = async (patch) => {
      const next = {
        id: pr.id,
        description: patch.description !== undefined ? patch.description : (pr.description || ""),
        extends: patch.extends !== undefined ? patch.extends : [...extSet],
        skills: patch.skills !== undefined ? patch.skills : [...directSets.skills],
        mcp: patch.mcp !== undefined ? patch.mcp : [...directSets.mcp],
        hooks: patch.hooks !== undefined ? patch.hooks : [...directSets.hooks],
        agents: patch.agents !== undefined ? patch.agents : [...directSets.agents],
        extras: patch.extras !== undefined ? patch.extras : [...directSets.extras],
        plugins: patch.plugins !== undefined ? patch.plugins : [...directSets.plugins],
        fragments: patch.fragments !== undefined ? patch.fragments : [...directSets.prompts],
        launchers: patch.launchers !== undefined ? patch.launchers : [...directSets.launchers],
      };
      try {
        await apiPost("/api/profile-bundles/save", next);
        state.bundles = {};
        await refreshState(true);
      } catch (err) {
        toast(err.message, "err");
      }
    };

    const main = document.getElementById("main-content");
    main.innerHTML = `
      <div class="page-scroll">
        <div class="page-wide">
          <div style="margin-bottom:10px">
            <button type="button" class="btn btn-sm btn-ghost" id="pm-back" style="padding-left:4px">← All Profiles</button>
          </div>
          <div class="page-head">
            <div style="min-width:0;flex:1">
              <div class="row" style="gap:10px;align-items:baseline">
                <h1 class="page-title mono">${esc(pr.id)}</h1>
                <span class="tag">${assignedEnvs.size} host${assignedEnvs.size === 1 ? "" : "s"}</span>
              </div>
              <div style="margin-top:8px;max-width:520px">
                <input id="pm-desc" class="input" style="width:100%" value="${esc(pr.description || "")}" placeholder="Add a short description for this profile…" />
              </div>
            </div>
            <div class="row">
              <button class="btn btn-sm btn-bad" id="pm-delete">Delete profile</button>
              <button class="btn btn-pri" id="pm-sync-all">Push to all hosts</button>
            </div>
          </div>

          <div class="card">
            <div class="cfg-section">
              <div class="cfg-head">
                <span class="cfg-label">Hosts using <span class="mono">${esc(pr.id)}</span></span>
                <span class="xs subtle">click to assign or remove</span>
              </div>
              <div class="chip-wrap">
                ${envs.map((env) => `<button type="button" class="chip ${assignedEnvs.has(env.id) ? "on" : ""}" data-pm-env="${esc(env.id)}">${esc(env.id)}</button>`).join("")}
              </div>
            </div>
            ${otherProfiles.length ? `
              <div class="cfg-section">
                <div class="cfg-head">
                  <span class="cfg-label">Extends profiles</span>
                  <span class="xs subtle">inherited items show in blue below</span>
                </div>
                <div class="chip-wrap">
                  ${otherProfiles.map((op) => `<button type="button" class="chip ${extSet.has(op.id) ? "on" : ""}" data-pm-ext="${esc(op.id)}">${esc(op.id)}</button>`).join("")}
                </div>
              </div>` : ""}
          </div>

          <div style="margin-bottom:16px;overflow-x:auto">
            <div class="seg-control">
              <button class="seg-opt ${activeCat === "all" ? "active" : ""}" data-pm-cat="all">All <span class="xs subtle">${totalEnabled}/${totalItems}</span></button>
              ${catDefs.map((c) => {
                const list = allItemsByCat[c.id];
                const en = list.filter((x) => x.direct || x.inheritedFrom).length;
                return `<button class="seg-opt ${activeCat === c.id ? "active" : ""}" data-pm-cat="${c.id}">${c.label} <span class="xs subtle">${en}/${list.length}</span></button>`;
              }).join("")}
            </div>
          </div>

          <div class="card">
            ${visibleItems.length ? `
              <div class="table-wrap">
                <table class="matrix-table">
                  <thead>
                    <tr>
                      <th style="width:130px;text-align:center">In <span class="mono">${esc(pr.id)}</span></th>
                      <th>Item</th>
                      ${activeCat === "all" ? `<th style="width:110px">Category</th>` : ""}
                      <th style="width:130px;text-align:right">
                        ${activeCat !== "all" ? `<button type="button" class="btn btn-sm btn-ghost" id="pm-toggle-cat-all">${visibleItems.every((x) => x.direct || x.inheritedFrom) ? "Clear direct" : "Select all"}</button>` : "Status"}
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    ${visibleItems.map((it) => `
                      <tr>
                        <td class="m-cell">
                          ${it.direct
                            ? `<button type="button" class="m-btn m-direct" data-pm-cell="${esc(it.cat.id)}" data-pm-item="${esc(it.id)}" data-pm-next="0" title="Included directly in ${esc(pr.id)} — click to remove">${ICONS.check}</button>`
                            : it.inheritedFrom
                              ? `<button type="button" class="m-btn m-profile" data-pm-inh-cat="${esc(it.cat.id)}" data-pm-inh-item="${esc(it.id)}" data-pm-inh-from="${esc(it.inheritedFrom)}" title="Inherited from extended profile '${esc(it.inheritedFrom)}' — click for options">${ICONS.check}</button>`
                              : `<button type="button" class="m-btn" data-pm-cell="${esc(it.cat.id)}" data-pm-item="${esc(it.id)}" data-pm-next="1" title="Click to add to ${esc(pr.id)}">·</button>`}
                        </td>
                        <td>
                          <div class="mono" style="font-weight:500">${esc(it.label)}</div>
                          ${it.description ? `<div class="xs subtle" style="max-width:520px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(it.description)}</div>` : ""}
                        </td>
                        ${activeCat === "all" ? `<td><span class="tag">${esc(it.cat.label)}</span></td>` : ""}
                        <td style="text-align:right">
                          ${it.direct
                            ? `<span class="xs subtle">direct</span>`
                            : it.inheritedFrom
                              ? `<span class="xs subtle">via ${esc(it.inheritedFrom)}</span>`
                              : `<span class="xs subtle">—</span>`}
                        </td>
                      </tr>`).join("")}
                  </tbody>
                </table>
              </div>` : `<div class="card-body small muted">No items in Catalog for <strong>${esc(activeCat)}</strong> yet. Add them in the <strong>Catalog</strong> tab first.</div>`}
          </div>
        </div>
      </div>
    `;

    document.getElementById("pm-back").onclick = () => {
      state.editingProfile = null;
      state.matrixTab = "profiles";
      syncURL(false);
      renderMatrix();
    };
    document.getElementById("pm-delete").onclick = () => confirmDeleteProfileBundle(pr.id);
    document.getElementById("pm-sync-all").onclick = () => confirmFleetSync();

    const descEl = document.getElementById("pm-desc");
    if (descEl) {
      const commitDesc = () => {
        const v = descEl.value.trim();
        if (v !== (pr.description || "")) saveProfilePatch({ description: v });
      };
      descEl.onblur = commitDesc;
      descEl.onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); descEl.blur(); } };
    }

    for (const b of main.querySelectorAll("[data-pm-env]")) {
      b.onclick = async () => {
        const envId = b.dataset.pmEnv;
        const nextEnabled = !assignedEnvs.has(envId);
        b.disabled = true;
        try {
          await apiPost("/api/matrix/cell", { category: "profiles", item: pr.id, env: envId, enabled: nextEnabled });
          state.bundles = {};
          await refreshState(true);
        } catch (err) {
          b.disabled = false;
          toast(err.message, "err");
        }
      };
    }

    for (const b of main.querySelectorAll("[data-pm-ext]")) {
      b.onclick = () => {
        const ext = b.dataset.pmExt;
        const next = new Set(extSet);
        next.has(ext) ? next.delete(ext) : next.add(ext);
        saveProfilePatch({ extends: [...next] });
      };
    }

    for (const b of main.querySelectorAll("[data-pm-cat]")) {
      b.onclick = () => {
        state.profileMatrixTab = b.dataset.pmCat;
        syncURL(false);
        renderProfileMatrix(pr);
      };
    }

    for (const b of main.querySelectorAll("[data-pm-cell]")) {
      b.onclick = () => {
        const catId = b.dataset.pmCell;
        const def = catDefs.find((c) => c.id === catId);
        if (!def) return;
        const item = b.dataset.pmItem;
        const nextEn = b.dataset.pmNext === "1";
        const next = new Set(directSets[catId]);
        if (nextEn) next.add(item);
        else next.delete(item);
        saveProfilePatch({ [def.field]: [...next] });
      };
    }

    for (const b of main.querySelectorAll("[data-pm-inh-from]")) {
      b.onclick = () => {
        const catId = b.dataset.pmInhCat;
        const item = b.dataset.pmInhItem;
        const parentProf = b.dataset.pmInhFrom;
        openModal({
          title: `From profile · ${parentProf}`,
          bodyHtml: `<p style="margin:0"><code class="mono">${esc(item)}</code> is included in <strong class="mono">${esc(pr.id)}</strong> because it extends <strong class="mono">${esc(parentProf)}</strong>.</p>`,
          buttons: [
            { label: "Close", cls: "btn" },
            {
              label: "Detach profile",
              cls: "btn btn-bad",
              onClick: () => {
                const next = new Set(extSet);
                next.delete(parentProf);
                saveProfilePatch({ extends: [...next] });
              },
            },
            {
              label: "Edit profile →",
              cls: "btn btn-pri",
              onClick: () => openProfileMatrix(parentProf, catId),
            },
          ],
        });
      };
    }

    const catAllBtn = document.getElementById("pm-toggle-cat-all");
    if (catAllBtn && activeCat !== "all") {
      const def = catDefs.find((c) => c.id === activeCat);
      if (def) {
        catAllBtn.onclick = () => {
          const allOn = visibleItems.every((x) => x.direct || x.inheritedFrom);
          if (allOn) {
            saveProfilePatch({ [def.field]: [] });
          } else {
            const next = new Set(directSets[activeCat]);
            for (const x of visibleItems) {
              if (!x.inheritedFrom) next.add(x.id);
            }
            saveProfilePatch({ [def.field]: [...next] });
          }
        };
      }
    }
  }

  function openProfileGuardrailModal(conf) {
    openModal({
      title: `From profile · ${conf.profile}`,
      bodyHtml: `
        <p style="margin:0 0 8px"><code class="mono">${esc(conf.item)}</code> is enabled on <strong class="mono">${esc(conf.env)}</strong> via the <strong class="mono">${esc(conf.profile)}</strong> profile.</p>
        <p class="small muted" style="margin:0">Edit the profile to change what it includes across hosts, or detach it from <span class="mono">${esc(conf.env)}</span>.</p>
      `,
      buttons: [
        { label: "Close", cls: "btn" },
        {
          label: "Detach profile",
          cls: "btn btn-bad",
          onClick: async () => {
            try {
              await apiPost("/api/matrix/profile-resolve", { action: "detach", env: conf.env, profile: conf.profile });
              state.bundles = {};
              toast(`Detached ${conf.profile} from ${conf.env}`, "ok");
              await refreshState(true);
            } catch (err) { toast(err.message, "err"); }
          },
        },
        {
          label: "Edit profile →",
          cls: "btn btn-pri",
          onClick: () => openProfileMatrix(conf.profile, conf.category),
        },
      ],
    });
  }

  // ==============================================================================================
  // 4. PER-ENVIRONMENT VIEW (Overview + Backward Sync + Local Overrides + Skillshare)
  // ==============================================================================================

  function driftActionLabels(tid) {
    return {
      import_to_catalog: `Add to Catalog & enable on ${tid}`,
      update_catalog: "Update Catalog item (fan out to others on push)",
      fork_to_catalog: `Fork as host copy & enable on ${tid}`,
      disable_in_matrix: `Disable on ${tid} in Matrix`,
      keep_local: `Keep as local-only on ${tid}`,
      save_env_override: `Save as ${tid} host override`,
      ignore: "Ignore (revert on next push)",
    };
  }

  function unmanagedActionLabels(tid) {
    return {
      import_to_catalog: `Add to Catalog & enable on ${tid}`,
      keep_local: `Save as ${tid} local override`,
      mark_external: "Mark external (keep native)",
      unmark_external: "Unmark external",
      ignore: "Skip / leave as-is",
    };
  }

  async function scanTargetUnmanaged(tid, silent = false) {
    if (state.unmanagedScanning[tid]) return;
    state.unmanagedScanning[tid] = true;
    if (state.scope === "env" && state.selectedTarget === tid && state.envTab === "overview" && state.bundles[tid]) {
      renderEnvOverview(tid, state.bundles[tid]);
    }
    try {
      const rep = await apiPost(`/api/targets/${encodeURIComponent(tid)}/unmanaged`, {});
      if (state.bundles[tid]) state.bundles[tid].unmanaged = rep;
      if (!silent) {
        const unCount = (rep.items || []).filter((x) => !x.external).length;
        toast(unCount ? `Found ${unCount} unmanaged native item(s) on ${tid}` : `Scanned ${tid}`, "ok");
      }
    } catch (err) {
      if (!silent) toast(err.message, "err");
    } finally {
      delete state.unmanagedScanning[tid];
      if (state.scope === "env" && state.selectedTarget === tid && state.envTab === "overview" && state.bundles[tid]) {
        const active = document.activeElement;
        const editingInput = active && active.closest && active.closest("#main-content") && /^(INPUT|TEXTAREA|SELECT)$/.test(active.tagName);
        if (!editingInput) renderEnvOverview(tid, state.bundles[tid]);
      }
    }
  }

  function renderEnvOverview(tid, bundle) {
    const tInfo = state.data.targets.find((x) => x.id === tid);
    const p = (tInfo && tInfo.probe) || bundle.probe;
    const sum = targetSyncSummary(tInfo || bundle);
    const op = state.opsOutput[`env:${tid}`];
    const ov = bundle.env_override || { mode: "merge", targets: {}, projects: {}, local_skills: [], local_mcp: {}, local_hooks: {}, extra_config: {} };
    const editable = bundle.editable || {};
    const activeProfiles = new Set(editable.profiles || []);
    const allProfiles = (state.data.catalog && state.data.catalog.profiles) || [];
    const activeTargets = Object.keys(ov.targets || {}).sort();
    const projectEntries = Object.entries(ov.projects || {}).sort(([a], [b]) => a.localeCompare(b));
    const commonHarnesses = Array.from(new Set(["claude", "codex", "opencode", "antigravity-cli", "cursor", "pi", ...activeTargets]));

    const localExtrasSummary = [
      ...(ov.local_skills || []).map((s) => `skill:${s}`),
      ...Object.keys(ov.local_mcp || {}).map((m) => `mcp:${m}`),
      ...Object.keys(ov.local_hooks || {}).map((h) => `hook:${h}`),
      ...Object.keys(ov.local_plugins || {}).map((pl) => `plugin:${pl}`),
      ...Object.keys(ov.extra_config || {}).map((k) => `config:${k}`),
    ];

    const integDefs = [
      { key: "emit_shims", label: "Emit shims", hint: "Install ~/.local/bin launcher shims", show: true },
      { key: "sync_herdr_programs", label: "Herdr programs", hint: "Sync launcher programs into Herdr", show: bundle.is_local || Boolean(editable.sync_herdr_programs) },
      { key: "sync_herdr_templates", label: "Herdr templates", hint: "Sync session templates into Herdr", show: bundle.is_local || Boolean(editable.sync_herdr_templates) },
      { key: "sync_t3_providers", label: "T3 providers", hint: "Sync provider settings into T3 Code", show: bundle.is_local || Boolean(editable.sync_t3_providers) },
    ].filter((x) => x.show);

    const skills = bundle.skills || [];
    const mcps = bundle.mcp || [];
    const hooks = bundle.hooks || [];
    const agents = bundle.agents || [];
    const extras = bundle.extras || [];
    const plugins = bundle.plugins || [];
    const promptFrags = bundle.prompt_fragments || [];
    const prompts = bundle.prompts || [];
    const launchers = bundle.launchers || [];

    const summaryBits = [];
    if (skills.length) summaryBits.push(`${skills.length} skill${skills.length === 1 ? "" : "s"}`);
    if (mcps.length) summaryBits.push(`${mcps.length} MCP`);
    if (hooks.length) summaryBits.push(`${hooks.length} hook${hooks.length === 1 ? "" : "s"}`);
    if (agents.length) summaryBits.push(`${agents.length} agent${agents.length === 1 ? "" : "s"}`);
    if (extras.length) summaryBits.push(`${extras.length} extra${extras.length === 1 ? "" : "s"}`);
    if (plugins.length) summaryBits.push(`${plugins.length} plugin${plugins.length === 1 ? "" : "s"}`);
    if (promptFrags.length) summaryBits.push(`${promptFrags.length} prompt${promptFrags.length === 1 ? "" : "s"}`);
    if (launchers.length) summaryBits.push(`${launchers.length} launcher${launchers.length === 1 ? "" : "s"}`);

    const subBits = [bundle.is_local ? "Local machine" : `SSH · ${esc(bundle.ssh)}`];
    if (p && p.ok && p.os) subBits.push(esc(`${p.os}/${p.arch}`));
    if (p && p.installed && p.version) subBits.push(esc(p.version));
    const syncPill = renderSyncPill(sum, false);

    const extraCats = [
      ["MCP", mcps],
      ["Hooks", hooks],
      ["Agents", agents],
      ["Extras", extras],
      ["Plugins", plugins],
    ].filter(([, items]) => items.length > 0);

    const main = document.getElementById("main-content");
    main.innerHTML = `
      <div class="page-scroll">
        <div class="page-narrow">
          <div class="page-head">
            <div>
              <h1 class="page-title mono">${esc(tid)}</h1>
              <p class="page-sub">
                ${subBits.join(" · ")}${syncPill ? ` · ${syncPill}` : ""}
              </p>
            </div>
            <div class="row">
              <button class="btn btn-sm" id="st-doctor">Doctor</button>
              <button class="btn btn-sm" id="st-secrets">Secrets</button>
              <button class="btn btn-sm" id="st-probe">Check host</button>
            </div>
          </div>

          ${p && p.ok && !p.installed ? `
            <div class="card drift-card">
              <div class="card-body row-between">
                <div>
                  <div style="font-weight:600;margin-bottom:2px">skillshare is not installed on ${esc(tid)}</div>
                  <div class="small muted">Install upstream <code>skillshare</code> on this host to enable live editing and syncing.</div>
                </div>
                <button class="btn btn-sm btn-pri" id="st-install-missing">Install skillshare</button>
              </div>
            </div>` : ""}

          ${sum.outOfSync ? `
            <div class="card drift-card">
              <div class="card-body row-between">
                <div>
                  <div style="font-weight:600;margin-bottom:2px">Out of sync with Catalog &amp; Matrix</div>
                  <div class="small muted">
                    ${[
                      sum.pull ? `${sum.pull} local edit${sum.pull === 1 ? "" : "s"} on host` : "",
                      sum.push ? `${sum.push} pending push update${sum.push === 1 ? "" : "s"}` : "",
                    ].filter(Boolean).join(" · ")}
                  </div>
                </div>
                <button class="btn btn-sm btn-pri" id="st-review-sync">Review &amp; Sync</button>
              </div>
            </div>` : ""}

          ${op ? renderOpCard(op, `env:${tid}`) : ""}

          <div class="card">
            <div class="card-head">
              <h2 class="card-title">Configuration</h2>
              <button class="btn btn-sm btn-ghost" id="st-open-mx">Edit in Matrix →</button>
            </div>

            ${allProfiles.length ? `
              <div class="cfg-section">
                <div class="cfg-head">
                  <span class="cfg-label">Profiles</span>
                  <span class="xs subtle">click to toggle on this host</span>
                </div>
                <div class="chip-wrap">
                  ${allProfiles.map((pr) => {
                    const on = activeProfiles.has(pr.id);
                    const tip = pr.description || `${(pr.skills || []).length} skills · ${(pr.fragments || []).length} prompts · ${(pr.launchers || []).length} launchers`;
                    return `<button type="button" class="chip ${on ? "on" : ""}" data-env-prof="${esc(pr.id)}" title="${esc(tip)}">${esc(pr.id)}</button>`;
                  }).join("")}
                </div>
              </div>` : ""}

            <div class="cfg-section">
              <div class="cfg-head">
                <span class="cfg-label">Harnesses</span>
                <select id="env-ov-mode" class="input" style="padding:4px 10px;font-size:12.5px" title="Skillshare sync mode on this host">
                  ${["merge", "symlink", "copy"].map((m) => `<option value="${m}" ${ov.mode === m ? "selected" : ""}>mode: ${m}</option>`).join("")}
                </select>
              </div>
              <div class="chip-wrap">
                ${commonHarnesses.map((ht) => `<button type="button" class="chip ${activeTargets.includes(ht) ? "on" : ""}" data-env-ht="${esc(ht)}">${esc(ht)}</button>`).join("")}
              </div>
              ${localExtrasSummary.length ? `
                <div class="row" style="margin-top:12px;gap:6px">
                  <span class="xs subtle">Host-only overrides:</span>
                  ${localExtrasSummary.map((x) => `<span class="tag tag-accent mono">${esc(x)}</span>`).join("")}
                </div>` : ""}
            </div>

            <div class="cfg-section">
              <div class="cfg-head">
                <span class="cfg-label">Host Integrations</span>
              </div>
              <div class="chip-wrap">
                ${integDefs.map((ig) => `<button type="button" class="chip ${editable[ig.key] ? "on" : ""}" data-env-integ="${esc(ig.key)}" title="${esc(ig.hint)}">${esc(ig.label)}</button>`).join("")}
              </div>
            </div>
          </div>

          <div class="card">
            <div class="card-head">
              <h2 class="card-title">Assigned to ${esc(tid)}</h2>
              <span class="xs subtle">${esc(summaryBits.join(" · ") || "nothing assigned yet")}</span>
            </div>

            <div class="ov-row">
              <div class="ov-key">Skills (${skills.length})</div>
              <div class="ov-val">
                ${skills.length
                  ? `<div class="chip-wrap">${skills.map((s) => `<button type="button" class="ov-pill clickable mono" data-jump-skill="${esc(s.name)}" title="${esc(s.description || s.name)}"><span>${esc(s.name)}</span>${s.source ? `<span class="ov-pill-src">${esc(s.source)}</span>` : ""}</button>`).join("")}</div>`
                  : `<span class="small muted">None assigned</span>`}
              </div>
            </div>

            ${extraCats.map(([label, items]) => `
              <div class="ov-row">
                <div class="ov-key">${esc(label)} (${items.length})</div>
                <div class="ov-val chip-wrap">
                  ${items.map((name) => `<span class="ov-pill mono">${esc(name)}</span>`).join("")}
                </div>
              </div>`).join("")}

            <div class="ov-row">
              <div class="ov-key">Prompts (${promptFrags.length})</div>
              <div class="ov-val">
                <div class="row-between" style="align-items:flex-start;gap:10px">
                  <div class="chip-wrap" style="flex:1">
                    ${promptFrags.length
                      ? promptFrags.map((pf) => `<span class="ov-pill mono" title="${esc(pf.why || pf.id)} (${pf.words || 0} words)">${pf.rank ? `<span class="ov-pill-src">#${pf.rank}</span>` : ""}<span>${esc(pf.id)}</span>${pf.source ? `<span class="ov-pill-src">${esc(pf.source)}</span>` : ""}</span>`).join("")
                      : `<span class="small muted">None assigned</span>`}
                  </div>
                  ${prompts.length ? `<button type="button" class="btn btn-sm" id="st-preview-prompts">Preview compiled</button>` : ""}
                </div>
              </div>
            </div>

            <div class="ov-row">
              <div class="ov-key">Launchers (${launchers.length})</div>
              <div class="ov-val">
                ${launchers.length
                  ? launchers.map((l) => `
                    <div class="row-between" style="padding:5px 0;gap:10px">
                      <div>
                        <span class="mono small" style="font-weight:600">${esc(l.command)}</span>
                        <span class="xs subtle" style="margin-left:6px">${esc(l.mode)}</span>
                      </div>
                      <div class="row" style="gap:5px">${(l.secret_refs || []).map((r) => `<span class="tag tag-accent mono">${esc(r.ref)}</span>`).join("")}</div>
                    </div>`).join("")
                  : `<span class="small muted">None assigned</span>`}
              </div>
            </div>

            ${projectEntries.length ? `
              <div class="ov-row">
                <div class="ov-key">Projects (${projectEntries.length})</div>
                <div class="ov-val">
                  ${projectEntries.map(([pRoot, pCfg]) => {
                    const tgts = (pCfg && Array.isArray(pCfg.targets)) ? pCfg.targets : [];
                    return `<div class="row-between" style="padding:4px 0"><span class="mono small">${esc(pRoot)}</span><div class="row" style="gap:4px">${tgts.map((tg) => `<span class="tag">${esc(tg)}</span>`).join("")}</div></div>`;
                  }).join("")}
                </div>
              </div>` : ""}
          </div>

          ${(() => {
            const unRep = bundle.unmanaged;
            const scanning = Boolean(state.unmanagedScanning[tid]);
            const allUn = (unRep && Array.isArray(unRep.items)) ? unRep.items : [];
            const unItems = allUn.filter((x) => !x.external);
            const extItems = allUn.filter((x) => x.external);
            const uLabels = unmanagedActionLabels(tid);

            return `
              <div class="card">
                <div class="card-head">
                  <div class="row" style="gap:8px">
                    <h2 class="card-title">Native &amp; External on ${esc(tid)}</h2>
                    ${unRep && unItems.length ? `<span class="tag tag-warn">${unItems.length} unmanaged</span>` : ""}
                    ${unRep && !unItems.length && extItems.length ? `<span class="tag">${extItems.length} external</span>` : ""}
                  </div>
                  <div class="row" style="gap:8px">
                    ${unItems.length ? `<button type="button" class="btn btn-sm btn-pri" id="st-apply-unmanaged">Apply selected</button>` : ""}
                    <button type="button" class="btn btn-sm" id="st-scan-unmanaged" ${scanning ? "disabled" : ""}>
                      ${scanning ? `<span class="spinner"></span><span>Scanning…</span>` : "Rescan"}
                    </button>
                  </div>
                </div>
                ${!unRep && scanning ? `
                  <div class="card-body row small muted" style="gap:8px"><span class="spinner"></span><span>Scanning native harnesses on <span class="mono">${esc(tid)}</span>…</span></div>
                ` : unRep && !unRep.ok ? `
                  <div class="card-body small" style="color:var(--bad)">Scan failed: ${esc(unRep.error || "unreachable")}</div>
                ` : !unRep ? `
                  <div class="card-body small muted">Click <strong>Rescan</strong> to inspect native skills, MCP servers, hooks, and plugins installed outside Livery.</div>
                ` : !unItems.length && !extItems.length ? `
                  <div class="card-body small muted">No unmanaged native skills, MCP servers, hooks, or plugins detected on this host.</div>
                ` : `
                  ${unItems.map((it) => `
                    <div class="list-row">
                      <div>
                        <div class="row" style="gap:6px">
                          <span class="tag tag-warn">${esc(it.category)}</span>
                          <span class="mono" style="font-weight:600">${esc(it.name)}</span>
                          ${(it.harnesses || []).map((h) => `<span class="tag">${esc(h)}</span>`).join("")}
                        </div>
                        ${it.summary ? `<div class="xs mono subtle" style="margin-top:3px">${esc(it.summary)}</div>` : ""}
                        ${it.note ? `<div class="xs" style="margin-top:3px;color:var(--warn)">${esc(it.note)}</div>` : ""}
                      </div>
                      <select class="input" style="max-width:270px" data-unmg-id="${esc(it.id)}">
                        ${(it.actions || []).map((a) => `<option value="${esc(a)}" ${a === it.default_act ? "selected" : ""}>${esc(uLabels[a] || a)}</option>`).join("")}
                      </select>
                    </div>`).join("")}
                  ${extItems.length ? `
                    <div class="cfg-section">
                      <div class="cfg-head" style="margin-bottom:8px">
                        <span class="cfg-label">External · managed outside Livery (${extItems.length})</span>
                        <span class="xs subtle">matched by <code>external</code> in inventory.toml</span>
                      </div>
                      <div class="chip-wrap">
                        ${extItems.map((it) => `
                          <button type="button" class="ov-pill clickable mono" data-ext-pull="${esc(it.id)}" data-ext-name="${esc(it.name)}" data-ext-cat="${esc(it.category)}" data-ext-sum="${esc(it.summary || "")}" data-ext-def="${it.default_external ? "1" : "0"}" title="${esc((it.summary || it.name) + " — click to import or manage")}">
                            <span class="ov-pill-src">${esc(it.category)}</span>
                            <span>${esc(it.name)}</span>
                            ${(it.harnesses || []).length ? `<span class="ov-pill-src">(${esc(it.harnesses.join(", "))})</span>` : ""}
                          </button>`).join("")}
                      </div>
                    </div>` : ""}
                `}
              </div>`;
          })()}

          <details class="fold">
            <summary><span class="muted">Maintenance &amp; Advanced</span><span class="xs subtle">expand</span></summary>
            <div class="fold-body">
              ${!bundle.is_local ? `
                <div class="maint-row">
                  <div>
                    <div class="small" style="font-weight:600">Bootstrap / update liv</div>
                    <div class="xs subtle">Cross-compile and install <code>~/.local/bin/liv</code> on ${esc(tid)}.</div>
                  </div>
                  <button class="btn btn-sm" id="st-boot-liv">Install liv</button>
                </div>` : ""}
              <div class="maint-row">
                <div>
                  <div class="small" style="font-weight:600">Install / update skillshare</div>
                  <div class="xs subtle">Install stock upstream <code>skillshare</code> on ${esc(tid)}.</div>
                </div>
                <button class="btn btn-sm" id="st-install">Install skillshare</button>
              </div>
              <div class="maint-row">
                <div>
                  <div class="small" style="font-weight:600">Skill security audit</div>
                  <div class="xs subtle">Run <code>skillshare audit</code> against skills deployed on ${esc(tid)}.</div>
                </div>
                <button class="btn btn-sm" id="st-audit">Run audit</button>
              </div>
              <div class="maint-row">
                <div>
                  <div class="small" style="font-weight:600">Compiled skillshare config.yaml</div>
                  <div class="xs subtle">Inspect the generated config that Livery pushes to this host.</div>
                </div>
                <button class="btn btn-sm" id="st-preview-cfg">View config.yaml</button>
              </div>
              <div class="maint-row">
                <div>
                  <div class="small" style="font-weight:600;color:var(--bad)">Remove host from inventory</div>
                  <div class="xs subtle">Removes <code>${esc(tid)}</code> from Livery without deleting files on the host.</div>
                </div>
                <button class="btn btn-sm btn-bad" id="st-remove">Remove host</button>
              </div>
            </div>
          </details>
        </div>
      </div>
    `;

    const bindOpBtn = (id, fn) => {
      const el = document.getElementById(id);
      if (el) el.onclick = () => fn(el);
    };
    bindOpBtn("st-doctor", (btn) => runTargetOp(tid, "doctor", btn));
    bindOpBtn("st-secrets", (btn) => runTargetOp(tid, "secrets-status", btn));
    bindOpBtn("st-probe", (btn) => probeOne(tid, btn));
    bindOpBtn("st-audit", (btn) => runTargetOp(tid, "audit", btn));

    const dismissOp = main.querySelector("[data-dismiss-op]");
    if (dismissOp) {
      dismissOp.onclick = () => {
        delete state.opsOutput[dismissOp.dataset.dismissOp];
        renderWorkspace();
      };
    }

    const revSync = document.getElementById("st-review-sync");
    if (revSync) revSync.onclick = () => openSyncModal(tid);
    const missInstall = document.getElementById("st-install-missing");
    if (missInstall) missInstall.onclick = () => confirmInstallSkillshare(tid);
    const openMx = document.getElementById("st-open-mx");
    if (openMx) openMx.onclick = () => goFleet("matrix");
    const bootLiv = document.getElementById("st-boot-liv");
    if (bootLiv) bootLiv.onclick = () => confirmBootstrapLiv(tid);
    document.getElementById("st-install").onclick = () => confirmInstallSkillshare(tid);
    document.getElementById("st-remove").onclick = () => confirmRemoveHost(tid);

    // Auto-scan native/external items the first time Overview opens for a host
    if (!bundle.unmanaged && !state.unmanagedScanning[tid]) {
      setTimeout(() => scanTargetUnmanaged(tid, true), 30);
    }
    const scanUnBtn = document.getElementById("st-scan-unmanaged");
    if (scanUnBtn) scanUnBtn.onclick = () => scanTargetUnmanaged(tid, false);

    const applyUnBtn = document.getElementById("st-apply-unmanaged");
    if (applyUnBtn) {
      applyUnBtn.onclick = async () => {
        const selects = [...main.querySelectorAll("[data-unmg-id]")];
        const decisions = selects.map((el) => ({ id: el.dataset.unmgId, action: el.value })).filter((d) => d.action && d.action !== "ignore");
        if (!decisions.length) {
          toast("All items set to Skip — select an action to apply", "warn");
          return;
        }
        applyUnBtn.disabled = true;
        for (const s of selects) s.disabled = true;
        applyUnBtn.innerHTML = `<span class="spinner"></span><span>Applying…</span>`;
        try {
          const res = await apiPost(`/api/targets/${encodeURIComponent(tid)}/unmanaged/apply`, { confirm: true, decisions });
          delete state.ssSlotMeta["catalog"];
          toast(`Applied ${(res.applied || []).length} item(s) — click Sync to push to ${tid}`, "ok");
          await loadBundle(tid, true);
          await refreshState(true);
        } catch (err) {
          applyUnBtn.disabled = false;
          for (const s of selects) s.disabled = false;
          applyUnBtn.textContent = "Apply selected";
          toast(err.message, "err");
        }
      };
    }

    for (const b of main.querySelectorAll("[data-ext-pull]")) {
      b.onclick = () => {
        const itemId = b.dataset.extPull;
        const name = b.dataset.extName;
        const cat = b.dataset.extCat;
        const sumTxt = b.dataset.extSum;
        const isDef = b.dataset.extDef === "1";
        const btns = [{ label: "Cancel", cls: "btn" }];
        if (!isDef) {
          btns.push({
            label: "Unmark external",
            cls: "btn",
            onClick: async () => {
              try {
                await apiPost(`/api/targets/${encodeURIComponent(tid)}/unmanaged/apply`, { confirm: true, decisions: [{ id: itemId, action: "unmark_external" }] });
                await loadBundle(tid, true);
                await refreshState(true);
              } catch (err) { toast(err.message, "err"); }
            },
          });
        }
        btns.push(
          {
            label: `Save as ${tid} local override`,
            cls: "btn",
            onClick: async () => {
              try {
                await apiPost(`/api/targets/${encodeURIComponent(tid)}/unmanaged/apply`, { confirm: true, decisions: [{ id: itemId, action: "keep_local" }] });
                toast(`Saved ${name} as local override on ${tid}`, "ok");
                await loadBundle(tid, true);
                await refreshState(true);
              } catch (err) { toast(err.message, "err"); }
            },
          },
          {
            label: "Pull into Catalog & Matrix",
            cls: "btn btn-pri",
            onClick: async () => {
              try {
                await apiPost(`/api/targets/${encodeURIComponent(tid)}/unmanaged/apply`, { confirm: true, decisions: [{ id: itemId, action: "import_to_catalog" }] });
                delete state.ssSlotMeta["catalog"];
                toast(`Imported ${name} into Catalog`, "ok");
                await loadBundle(tid, true);
                await refreshState(true);
              } catch (err) { toast(err.message, "err"); }
            },
          },
        );
        openModal({
          title: `External ${cat} · ${name}`,
          bodyHtml: `
            <p style="margin-top:0"><code class="mono">${esc(name)}</code> is currently marked <code>external</code> (${isDef ? "via <code>[defaults].external</code> in inventory.toml" : `on <span class="mono">${esc(tid)}</span>`}), so Livery leaves it untouched.</p>
            ${sumTxt ? `<pre class="code" style="margin-bottom:12px">${esc(sumTxt)}</pre>` : ""}
            <p class="small muted" style="margin-bottom:0">You can pull it into Livery's shared Catalog, save it as a host-local Skillshare override, or leave it externally managed.</p>
          `,
          buttons: btns,
        });
      };
    }

    const prevPrompts = document.getElementById("st-preview-prompts");
    if (prevPrompts) {
      prevPrompts.onclick = () => {
        openModal({
          title: `Compiled Prompts · ${tid}`,
          wide: true,
          bodyHtml: prompts.map((pr) => `<div style="margin-bottom:16px"><div class="mono small" style="font-weight:600;margin-bottom:6px">${esc(pr.path)}</div><pre class="code">${esc(pr.content)}</pre></div>`).join(""),
          buttons: [{ label: "Close", cls: "btn" }],
        });
      };
    }
    const prevCfg = document.getElementById("st-preview-cfg");
    if (prevCfg) {
      prevCfg.onclick = () => {
        openModal({
          title: `Compiled skillshare config.yaml · ${tid}`,
          wide: true,
          bodyHtml: `<pre class="code">${esc(bundle.skillshare_config_yaml || "")}</pre>`,
          buttons: [{ label: "Close", cls: "btn" }],
        });
      };
    }

    for (const b of main.querySelectorAll("[data-jump-skill]")) {
      b.onclick = () => {
        state.ssSubPath = "/skills";
        goEnv(tid, "skillshare");
      };
    }

    for (const b of main.querySelectorAll("[data-env-prof]")) {
      b.onclick = async () => {
        const prof = b.dataset.envProf;
        const nextEnabled = !activeProfiles.has(prof);
        b.disabled = true;
        try {
          await apiPost("/api/matrix/cell", { category: "profiles", item: prof, env: tid, enabled: nextEnabled });
          toast(`${nextEnabled ? "Enabled" : "Disabled"} profile ${prof} on ${tid}`, "ok");
          await loadBundle(tid, true);
          await refreshState(true);
        } catch (err) {
          b.disabled = false;
          toast(err.message, "err");
        }
      };
    }

    const saveOverride = async (nextTargets, nextMode) => {
      try {
        await apiPost(`/api/targets/${encodeURIComponent(tid)}/env-override`, { targets: nextTargets, mode: nextMode });
        toast(`Updated ${tid} harnesses/mode`, "ok");
        await loadBundle(tid, true);
        await refreshState(true);
      } catch (err) { toast(err.message, "err"); }
    };
    for (const b of main.querySelectorAll("[data-env-ht]")) {
      b.onclick = () => {
        const ht = b.dataset.envHt;
        const next = activeTargets.includes(ht) ? activeTargets.filter((x) => x !== ht) : [...activeTargets, ht];
        saveOverride(next, ov.mode);
      };
    }
    const mSel = document.getElementById("env-ov-mode");
    if (mSel) mSel.onchange = (e) => saveOverride(activeTargets, e.target.value);

    for (const b of main.querySelectorAll("[data-env-integ]")) {
      b.onclick = async () => {
        const key = b.dataset.envInteg;
        const nextVal = !Boolean(editable[key]);
        b.disabled = true;
        try {
          const res = await apiPost(`/api/targets/${encodeURIComponent(tid)}/config`, { patch: { [key]: nextVal } });
          if (res.bundle) state.bundles[tid] = res.bundle;
          else await loadBundle(tid, true);
          toast(`Updated ${tid}`, "ok");
          await refreshState(true);
        } catch (err) {
          b.disabled = false;
          toast(err.message, "err");
        }
      };
    }
  }

  function renderEnvSkillshareFrame(tid) {
    const slot = state.ssSlotMeta[`env/${tid}`];
    const tInfo = (state.data?.targets || []).find((x) => x.id === tid);
    const isLive = slot && slot.mode === "live";
    const main = document.getElementById("main-content");
    if (!state.data.repo.local_skillshare_installed) {
      main.innerHTML = `<div class="empty-center"><p>Install <code>skillshare</code> to use the embedded dashboard.</p></div>`;
      return;
    }
    if (!slot) {
      const isLocalTarget = Boolean(tInfo && tInfo.is_local);
      main.innerHTML = `<div class="empty-center">${isLocalTarget ? `Starting Skillshare for ${esc(tid)}…` : `Connecting to <span class="mono">${esc(tid)}</span> via SSH…`}</div>`;
      return;
    }

    // When Live (R/W): ZERO banner above Skillshare — the single Sync button sits cleanly in the topbar!
    // When Read-Only fallback (no live access): loud crimson banner explaining exactly why and how to retry.
    const roBannerHtml = isLive ? "" : `
      <div class="slot-banner-ro">
        <div style="min-width:0;flex:1">
          <div class="slot-ro-title">
            <span class="slot-ro-badge">⚠ Read-only · No live access</span>
            <span>Couldn't open live Skillshare on <span class="mono">${esc(tid)}</span></span>
          </div>
          <div class="small">
            Showing a <strong>local compiled preview</strong> of what Catalog &amp; Matrix would push. Direct edits, installs, and sync inside Skillshare are disabled until live access is restored.
          </div>
          ${slot.fallback_reason ? `<div class="slot-ro-reason">${esc(slot.fallback_reason)}</div>` : ""}
        </div>
        <div class="row" style="flex-shrink:0">
          <button class="btn btn-sm btn-pri" id="sb-retry">Retry live connection</button>
          <button class="btn btn-sm" id="sb-probe">Check host</button>
        </div>
      </div>`;

    const slotKey = `env/${tid}`;
    const existingFrame = main.querySelector("iframe.ss-iframe");
    if (existingFrame && existingFrame.dataset.slotKey === slotKey && existingFrame.dataset.slotBase === slot.url) {
      const curRO = main.querySelector(".slot-banner-ro");
      if (isLive && curRO) curRO.remove();
      else if (!isLive && !curRO) existingFrame.insertAdjacentHTML("beforebegin", roBannerHtml);
      else if (!isLive && curRO) curRO.outerHTML = roBannerHtml;
    } else {
      const iframeSrc = buildSlotIframeURL(slot.url, state.ssSubPath);
      main.innerHTML = `<div class="iframe-stage">${roBannerHtml}<iframe class="ss-iframe" data-slot-key="${esc(slotKey)}" data-slot-base="${esc(slot.url)}" src="${esc(iframeSrc)}"></iframe></div>`;
      const frame = main.querySelector("iframe.ss-iframe");
      frame.addEventListener("load", () => {
        syncIframeTheme(frame);
        hookIframeRouting(frame, slot.url, slotKey);
      });
    }
    const retryBtn = main.querySelector("#sb-retry");
    if (retryBtn) retryBtn.onclick = () => ensureEnvSkillshare(tid, true, "live");
    const probeBtn = main.querySelector("#sb-probe");
    if (probeBtn) probeBtn.onclick = () => probeOne(tid);
  }

  async function openSyncModal(tid) {
    openModal({
      title: `Sync · ${tid}`,
      wide: true,
      bodyHtml: `<div class="row small muted" style="gap:8px"><span class="spinner"></span><span>Checking changes between <strong>Catalog &amp; Matrix</strong> and <strong class="mono">${esc(tid)}</strong>…</span></div>`,
      buttons: [{ label: "Cancel", cls: "btn" }],
    });
    let st;
    try {
      st = await apiPost(`/api/targets/${encodeURIComponent(tid)}/sync-preview`, {});
      await refreshState(true);
    } catch (err) {
      openModal({
        title: `Sync · ${tid}`,
        bodyHtml: `<div class="small" style="color:var(--bad)">Failed to check sync status: ${esc(err.message)}</div>`,
        buttons: [{ label: "Close", cls: "btn" }],
      });
      return;
    }

    const pullItems = (st.pull && st.pull.ok && st.pull.has_drift && Array.isArray(st.pull.items)) ? st.pull.items : [];
    const pushChanges = Array.isArray(st.push_changes) ? st.push_changes : [];
    const hasPull = pullItems.length > 0;
    const hasPush = pushChanges.length > 0 || Boolean(st.skillshare_needs_sync);
    const hasErr = Boolean(st.push_error);
    const actionLabels = driftActionLabels(tid);

    let bodyHtml = "";
    if (hasErr) {
      bodyHtml += `<div class="card drift-card"><div class="card-body small"><strong>Connection error:</strong> <span class="mono">${esc(st.push_error)}</span></div></div>`;
    }
    if (!hasErr && !hasPull && !hasPush) {
      bodyHtml += `
        <div class="card" style="margin-bottom:0">
          <div class="card-body">
            <div class="row" style="gap:8px;margin-bottom:4px">
              <span class="tag tag-ok">Everything in sync ✓</span>
              ${st.pull && st.pull.base_sha ? `<span class="xs mono subtle">baseline ${esc(st.pull.base_sha)}</span>` : ""}
            </div>
            <div class="small muted"><strong class="mono">${esc(tid)}</strong> matches your Catalog &amp; Matrix with no pending changes in either direction.</div>
          </div>
        </div>`;
    }
    if (hasPull) {
      bodyHtml += `
        <div class="card drift-card" style="margin-bottom:${hasPush ? "14px" : "0"}">
          <div class="card-head">
            <h2 class="card-title"><span class="mono">${esc(tid)}</span> → Catalog (${pullItems.length} local edit${pullItems.length === 1 ? "" : "s"})</h2>
            <span class="xs subtle">made directly on host</span>
          </div>
          ${pullItems.map((it) => `
            <div class="list-row">
              <div>
                <div class="row" style="gap:6px">
                  <span class="tag tag-warn">${esc(it.category)} · ${esc(it.kind)}</span>
                  <span class="mono" style="font-weight:600">${esc(it.name)}</span>
                </div>
                <div class="small muted" style="margin-top:2px">${esc(it.detail)}</div>
              </div>
              <select class="input" style="max-width:280px" data-sync-pull-id="${esc(it.id)}">
                ${(it.actions || []).map((a) => `<option value="${esc(a)}" ${a === it.default_act ? "selected" : ""}>${esc(actionLabels[a] || a)}</option>`).join("")}
              </select>
            </div>`).join("")}
        </div>`;
    }
    if (hasPush) {
      bodyHtml += `
        <div class="card" style="margin-bottom:0">
          <div class="card-head">
            <h2 class="card-title">Catalog &amp; Matrix → <span class="mono">${esc(tid)}</span> (${pushChanges.length || 1} update${pushChanges.length === 1 ? "" : "s"})</h2>
            <span class="xs subtle">will be written &amp; synced</span>
          </div>
          <div class="card-body">
            ${pushChanges.map((c) => {
              const cls = c.sym === "+" ? "tag-ok" : c.sym === "-" ? "tag-bad" : c.sym === "!" ? "tag-bad" : "tag-accent";
              return `<div class="list-row" style="padding:7px 0"><div class="row" style="gap:8px"><span class="tag ${cls} mono">${esc(c.sym)} ${esc(c.kind)}</span><span class="mono small">${esc(c.path)}</span></div>${c.note ? `<span class="xs muted">${esc(c.note)}</span>` : ""}</div>`;
            }).join("")}
            ${(st.skillshare_lines || []).length ? `<div class="xs mono subtle" style="margin-top:8px">${st.skillshare_lines.map((l) => esc(l)).join("<br/>")}</div>` : ""}
          </div>
        </div>`;
    }

    bodyHtml += `<div id="sync-progress" class="row small muted hidden" style="margin-top:12px;gap:8px"></div>`;

    const buttons = [{ label: "Close", cls: "btn" }];
    if (!hasErr && (hasPull || hasPush)) {
      const applyLabel = hasPull && hasPush
        ? `Apply sync (Pull ${pullItems.length}, then Push)`
        : hasPull
          ? `Apply ${pullItems.length} edit(s) to Catalog`
          : `Push changes to ${tid}`;
      buttons.push({
        label: applyLabel,
        cls: "btn btn-pri",
        close: false,
        onClick: async (applyBtn) => {
          const footerBtns = [...document.querySelectorAll("#modal-footer button")];
          const selects = [...document.querySelectorAll("[data-sync-pull-id]")];
          const prog = document.getElementById("sync-progress");
          const setBusy = (msg) => {
            setModalLocked(true);
            for (const b of footerBtns) b.disabled = true;
            for (const s of selects) s.disabled = true;
            if (applyBtn) applyBtn.innerHTML = `<span class="spinner"></span><span>${esc(msg)}</span>`;
            if (prog) {
              prog.classList.remove("hidden");
              prog.style.color = "";
              prog.innerHTML = `<span class="spinner"></span><span>${esc(msg)}</span>`;
            }
          };
          const setErr = (msg) => {
            setModalLocked(false);
            for (const b of footerBtns) b.disabled = false;
            for (const s of selects) s.disabled = false;
            if (applyBtn) applyBtn.textContent = applyLabel;
            if (prog) {
              prog.classList.remove("hidden");
              prog.style.color = "var(--bad)";
              prog.textContent = msg;
            }
            toast(msg, "err");
          };
          try {
            if (hasPull) {
              setBusy(`Pulling ${pullItems.length} local edit(s) into Catalog…`);
              const decisions = selects.map((el) => ({
                id: el.dataset.syncPullId, action: el.value,
              }));
              const { ok, status, body } = await apiPostRaw(`/api/targets/${encodeURIComponent(tid)}/backward-sync`, {
                confirm: true, skip_refresh: hasPush, decisions,
              });
              if (status === 409 && body.conflicts && body.conflicts.length) {
                closeModal(true);
                openProfileGuardrailModal(body.conflicts[0]);
                return;
              }
              if (!ok) { setErr(body.error || "Pull failed"); return; }
              delete state.ssSlotMeta["catalog"];
            }
            if (hasPush) {
              setBusy(`Pushing updates to ${tid}…`);
              await apiPost(`/api/targets/${encodeURIComponent(tid)}/sync`, { confirm: true });
              const curSlot = state.ssSlotMeta[`env/${tid}`];
              if (!curSlot || curSlot.mode !== "live") {
                delete state.ssSlotMeta[`env/${tid}`];
              }
            }
            setBusy("Refreshing status…");
            await loadBundle(tid, true);
            await refreshState(true);
            closeModal(true);
            toast(`Synced ${tid}`, "ok");
            if (state.scope === "env" && state.selectedTarget === tid && state.envTab === "skillshare") {
              const curSlot = state.ssSlotMeta[`env/${tid}`];
              if (curSlot && curSlot.mode === "live") {
                const frame = document.querySelector(`iframe.ss-iframe[data-slot-key="env/${CSS.escape(tid)}"]`);
                try { if (frame && frame.contentWindow) frame.contentWindow.location.reload(); } catch (_) {}
              } else {
                ensureEnvSkillshare(tid, false, "auto");
              }
            }
          } catch (err) {
            setErr(err.message || "Sync failed");
          }
        },
      });
    }
    openModal({ title: `Sync · ${tid}`, wide: true, bodyHtml, buttons });
  }

  async function runFleetSyncStatus(silent = false) {
    if (state.syncChecking) return;
    state.syncChecking = true;
    if (!silent && state.scope === "fleet" && state.fleetTab === "environments") renderEnvironments();
    try {
      const res = await apiPost("/api/fleet/sync-status", {});
      const outCount = Object.values(res.statuses || {}).filter((s) => s.out_of_sync).length;
      if (!silent) toast(outCount ? `${outCount} host(s) have changes to sync` : "All hosts are in sync", outCount ? "warn" : "ok");
      await refreshState(true);
    } catch (err) {
      if (!silent) toast(err.message, "err");
    } finally {
      state.syncChecking = false;
      if (state.scope === "fleet" && state.fleetTab === "environments") renderEnvironments();
    }
  }

  async function ensureCatalogSkillshare(restart = false) {
    if (!restart && state.ssSlotMeta["catalog"]) return;
    if (restart) {
      delete state.ssSlotMeta["catalog"];
      if (state.scope === "fleet" && state.fleetTab === "catalog" && state.catalogSub === "skillshare") renderCatalog();
    }
    try {
      state.ssSlotMeta["catalog"] = await apiPost("/api/ss/catalog/ensure", { restart });
      if (state.scope === "fleet" && state.fleetTab === "catalog" && state.catalogSub === "skillshare") { renderTopbar(); renderCatalog(); }
    } catch (err) { toast(err.message, "err"); }
  }

  async function ensureEnvSkillshare(tid, restart = false, mode = "auto") {
    const key = `env/${tid}`;
    if (!restart && state.ssSlotMeta[key]) return;
    if (restart) {
      delete state.ssSlotMeta[key];
      if (state.scope === "env" && state.selectedTarget === tid && state.envTab === "skillshare") renderEnvSkillshareFrame(tid);
    }
    try {
      state.ssSlotMeta[key] = await apiPost(`/api/ss/env/${encodeURIComponent(tid)}/ensure`, { mode, restart });
      if (state.scope === "env" && state.selectedTarget === tid && state.envTab === "skillshare") { renderTopbar(); renderEnvSkillshareFrame(tid); }
    } catch (err) { toast(err.message, "err"); }
  }

  // --- Shared Operations & Modals ---------------------------------------------------------------

  function renderOpCard(op, dismissKey = "") {
    const p = op.payload;
    let body = "";
    if (op.kind === "plan" && p && Array.isArray(p.plans)) {
      body = p.plans.map((pl) => `<pre class="code" style="margin-bottom:10px">${esc(pl.formatted || pl.error || "")}</pre>`).join("");
    } else if (op.kind === "audit" && p) {
      const s = p.summary || {};
      const results = p.results || [];
      body = `
        <div class="row" style="margin-bottom:12px">
          <span class="tag ${p.ok ? "tag-ok" : "tag-bad"}">${p.ok ? "Passed" : "Blocked"}</span>
          <span class="small muted">${s.scanned ?? results.length} scanned · ${s.passed ?? 0} clean · ${s.warning ?? 0} warn · ${s.failed ?? 0} failed</span>
        </div>
        ${p.error ? `<pre class="code">${esc(p.error)}</pre>` : results.map((r) => {
          const fs = r.findings || [];
          return `<div class="list-row" style="padding:8px 0"><span class="mono">${esc(r.skillName || "?")}</span><span class="tag ${!fs.length ? "tag-ok" : "tag-warn"}">${!fs.length ? "clean" : `${fs.length} finding(s)`}</span></div>`;
        }).join("")}`;
    } else if ((op.kind === "secrets" || op.kind === "secrets-status") && p && Array.isArray(p.targets)) {
      body = p.targets.map((row) => `
        <div style="margin-bottom:10px">
          <div class="mono small" style="font-weight:600;margin-bottom:4px">${esc(row.target)}</div>
          ${row.error ? `<span class="tag tag-bad">${esc(row.error)}</span>` : row.refs.map((r) => `
            <div class="list-row" style="padding:6px 0"><span class="mono small">${esc(r.ref)}</span><span class="tag ${r.ok ? "tag-ok" : "tag-bad"}">${esc(r.label)}</span></div>`).join("")}
        </div>`).join("");
    } else if (op.kind === "doctor" && p) {
      body = `<pre class="code">${esc(p.output || "")}</pre>`;
    } else {
      body = `<pre class="code">${esc(JSON.stringify(p, null, 2))}</pre>`;
    }
    return `
      <div class="card">
        <div class="card-head">
          <h2 class="card-title">${esc(op.title)}</h2>
          <div class="row" style="gap:8px">
            <span class="xs subtle">${esc(op.time)}</span>
            ${dismissKey ? `<button type="button" class="icon-btn" data-dismiss-op="${esc(dismissKey)}" title="Dismiss">✕</button>` : ""}
          </div>
        </div>
        <div class="card-body">${body}</div>
      </div>`;
  }

  async function installHerdrMod() {
    try {
      const res = await apiPost("/api/herdr/install-mod", { symlink: false });
      toast(`Installed livery-launcher mod (sha256:${(res.status?.expected_sha256 || "").slice(0, 12)})`, "ok");
      await refreshState(true);
    } catch (err) { toast(err.message, "err"); }
  }

  async function runFleetDrift() {
    try {
      toast("Checking local git drift across environments…");
      const res = await apiPost("/api/fleet/drift", {});
      const drifted = Object.values(res.drifts || {}).filter((d) => d.has_drift).length;
      toast(drifted ? `Local changes detected on ${drifted} host(s)` : "All hosts match livery-base", drifted ? "warn" : "ok");
      await refreshState(true);
    } catch (err) { toast(err.message, "err"); }
  }

  async function runTargetDrift(tid, btn = null) {
    const origLabel = btn ? btn.textContent : "";
    if (btn) { btn.disabled = true; btn.textContent = "Checking…"; }
    try {
      const rep = await apiPost(`/api/targets/${encodeURIComponent(tid)}/drift`, {});
      if (!rep.ok) { toast(rep.error || "Check failed", "err"); return; }
      if (rep.has_drift) {
        toast(`Found ${rep.items.length} local edit(s) on ${tid}`, "warn");
      } else if (!rep.has_base) {
        toast(`${tid}: no baseline yet — push once to establish livery-base`, "warn");
      } else {
        toast(`${tid}: no local edits (matches ${rep.base_sha})`, "ok");
      }
      await loadBundle(tid, true);
      await refreshState(true);
      if (rep.has_drift && state.scope === "env" && state.selectedTarget === tid && state.envTab === "skillshare") {
        openPullDriftModal(tid, rep);
      }
    } catch (err) {
      toast(err.message, "err");
    } finally {
      if (btn && btn.isConnected) { btn.disabled = false; btn.textContent = origLabel; }
    }
  }

  async function runFleetOp(kind, url, title) {
    try {
      toast(`Running ${title}…`);
      const payload = await apiPost(url);
      state.opsOutput["fleet"] = { title, kind, payload, time: new Date().toLocaleTimeString() };
      if (kind === "probe") { toast("Host check complete", "ok"); await refreshState(true); return; }
      toast(`${title} finished`, payload.ok === false ? "warn" : "ok");
      renderWorkspace();
    } catch (err) { toast(err.message, "err"); }
  }

  async function probeOne(tid, btn = null) {
    const orig = btn ? btn.textContent : "";
    if (btn) { btn.disabled = true; btn.innerHTML = `<span class="spinner"></span><span>Checking…</span>`; }
    try {
      const res = await apiPost(`/api/targets/${encodeURIComponent(tid)}/probe`);
      const pr = res.probes && res.probes[tid];
      toast(pr && pr.ok ? `${tid}: ${pr.os}/${pr.arch}` : `${tid}: unreachable`, pr && pr.ok ? "ok" : "err");
      await refreshState(true);
    } catch (err) {
      toast(err.message, "err");
    } finally {
      if (btn && btn.isConnected) { btn.disabled = false; btn.textContent = orig; }
    }
  }

  async function runTargetOp(tid, kind, btn = null) {
    const titles = { plan: `Plan · ${tid}`, audit: `Audit · ${tid}`, doctor: `Doctor · ${tid}`, "secrets-status": `Secrets · ${tid}` };
    const orig = btn ? btn.textContent : "";
    if (btn) { btn.disabled = true; btn.innerHTML = `<span class="spinner"></span><span>${esc(orig)}…</span>`; }
    try {
      const payload = await apiPost(`/api/targets/${encodeURIComponent(tid)}/${kind}`);
      state.opsOutput[`env:${tid}`] = { title: titles[kind], kind, payload, time: new Date().toLocaleTimeString() };
      toast(`${titles[kind]} complete`, payload.ok === false ? "warn" : "ok");
      renderWorkspace();
    } catch (err) {
      toast(err.message, "err");
    } finally {
      if (btn && btn.isConnected) { btn.disabled = false; btn.textContent = orig; }
    }
  }

  function confirmFleetSync() {
    openModal({
      title: "Push Catalog & Matrix → All Hosts",
      bodyHtml: `<p style="margin:0">Compile Skillshare bundles, prompts, and launchers from your <strong>Catalog &amp; Matrix</strong> and push them to all configured hosts (updating each host's <code>livery-base</code> snapshot)?</p><div id="fleet-sync-progress" class="row small muted hidden" style="margin-top:12px;gap:8px"></div>`,
      buttons: [
        { label: "Cancel", cls: "btn" },
        {
          label: "Push to all hosts", cls: "btn btn-pri", close: false,
          onClick: async (btn) => {
            const footerBtns = [...document.querySelectorAll("#modal-footer button")];
            const prog = document.getElementById("fleet-sync-progress");
            setModalLocked(true);
            for (const b of footerBtns) b.disabled = true;
            if (btn) btn.innerHTML = `<span class="spinner"></span><span>Pushing to all hosts…</span>`;
            if (prog) {
              prog.classList.remove("hidden");
              prog.innerHTML = `<span class="spinner"></span><span>Pushing Catalog &amp; Matrix to all hosts…</span>`;
            }
            try {
              const res = await apiPost("/api/fleet/sync", { confirm: true });
              state.opsOutput["fleet"] = { title: "Push to All Hosts", kind: "sync", payload: res, time: new Date().toLocaleTimeString() };
              state.bundles = {};
              await refreshState(true);
              closeModal(true);
              toast(res.ok ? "All hosts updated" : "Completed with warnings", res.ok ? "ok" : "warn");
              runFleetSyncStatus(true);
            } catch (err) {
              setModalLocked(false);
              for (const b of footerBtns) b.disabled = false;
              if (btn) btn.textContent = "Push to all hosts";
              if (prog) { prog.style.color = "var(--bad)"; prog.textContent = err.message || "Fleet sync failed"; }
              toast(err.message, "err");
            }
          },
        },
      ],
    });
  }

  function confirmSync(tid) {
    const tInfo = (state.data?.targets || []).find((x) => x.id === tid);
    const dr = (tInfo && tInfo.drift) || (state.bundles[tid] && state.bundles[tid].drift);
    const hasDrift = Boolean(dr && dr.has_drift && dr.items && dr.items.length);
    const buttons = [{ label: "Cancel", cls: "btn" }];
    if (hasDrift) {
      buttons.push({
        label: "Review & Pull local edits", cls: "btn",
        onClick: () => goEnv(tid, "overview"),
      });
    }
    buttons.push({
      label: hasDrift ? "Push anyway" : `Push to ${tid}`, cls: "btn btn-pri",
      onClick: async () => {
        try {
          toast(`Pushing Catalog & Matrix → ${tid}…`);
          const res = await apiPost(`/api/targets/${encodeURIComponent(tid)}/sync`, { confirm: true });
          state.opsOutput[`env:${tid}`] = { title: `Push → ${tid}`, kind: "sync", payload: res, time: new Date().toLocaleTimeString() };
          toast(res.message || `Pushed to ${tid}`, "ok");
          delete state.ssSlotMeta[`env/${tid}`];
          await loadBundle(tid, true);
          await refreshState(true);
        } catch (err) { toast(err.message, "err"); }
      },
    });
    openModal({
      title: `Push Catalog & Matrix → ${tid}`,
      bodyHtml: `
        ${hasDrift ? `<div class="card drift-card" style="margin-bottom:12px"><div class="card-body small"><strong>${dr.items.length} local edit(s) not pulled yet.</strong> Pushing now will replace un-saved local changes on <span class="mono">${esc(tid)}</span> (saved host overrides are preserved).</div></div>` : ""}
        <p style="margin:0">Compile items assigned to <strong class="mono">${esc(tid)}</strong> in Catalog &amp; Matrix, write its Skillshare config + prompts + launchers, run <code>skillshare sync</code>, and record a fresh <code>livery-base</code> snapshot?</p>
      `,
      buttons,
    });
  }

  function confirmBootstrapLiv(tid) {
    openModal({
      title: `Install liv on ${tid}`,
      bodyHtml: `<p style="margin:0">Cross-compile static <code>liv</code> and install to <code>~/.local/bin/liv</code> on <strong class="mono">${esc(tid)}</strong>?</p>`,
      buttons: [
        { label: "Cancel", cls: "btn" },
        {
          label: "Install liv", cls: "btn btn-pri",
          onClick: async () => {
            try {
              toast(`Installing liv on ${tid}…`);
              const res = await apiPost(`/api/targets/${encodeURIComponent(tid)}/bootstrap-liv`, { confirm: true });
              toast(`Installed ${res.version || "liv"} on ${tid}`, "ok");
              await refreshState(true);
            } catch (err) { toast(err.message, "err"); }
          },
        },
      ],
    });
  }

  function confirmInstallSkillshare(tid) {
    openModal({
      title: `Install skillshare on ${tid}`,
      bodyHtml: `<p style="margin:0">Install stock upstream <code>skillshare</code> on <strong class="mono">${esc(tid)}</strong>?</p>`,
      buttons: [
        { label: "Cancel", cls: "btn" },
        {
          label: "Install", cls: "btn btn-pri",
          onClick: async () => {
            try {
              toast(`Installing skillshare on ${tid}…`);
              const res = await apiPost(`/api/targets/${encodeURIComponent(tid)}/install-skillshare`, { confirm: true, method: "auto" });
              toast(res.ok ? `Installed ${res.version}` : res.error, res.ok ? "ok" : "err");
              await refreshState(true);
            } catch (err) { toast(err.message, "err"); }
          },
        },
      ],
    });
  }

  function confirmRemoveHost(tid) {
    openModal({
      title: `Remove ${tid}`,
      bodyHtml: `<p style="margin:0">Remove <strong class="mono">${esc(tid)}</strong> from inventory? Files on the host are not deleted.</p>`,
      buttons: [
        { label: "Cancel", cls: "btn" },
        {
          label: "Remove", cls: "btn btn-bad",
          onClick: async () => {
            try {
              await apiPost(`/api/targets/${encodeURIComponent(tid)}/remove`, { confirm: true });
              delete state.bundles[tid];
              toast(`Removed ${tid}`, "ok");
              await refreshState(true);
              goFleet("environments");
            } catch (err) { toast(err.message, "err"); }
          },
        },
      ],
    });
  }

  async function openAddEnvModal() {
    openModal({ title: "Add host environment", bodyHtml: `<div class="muted small">Scanning Herdr, Tailscale, and ~/.ssh/config…</div>`, buttons: [{ label: "Close", cls: "btn" }] });
    let hosts = [];
    try { hosts = (await apiGet("/api/env/discover")).hosts || []; } catch (_) {}
    const uncfg = hosts.filter((h) => !h.configured);
    openModal({
      title: "Add host environment",
      bodyHtml: `
        ${uncfg.length ? `<div class="card" style="margin-bottom:14px">${uncfg.map((h, i) => `<div class="list-row" style="padding:10px 14px"><div><div class="mono small" style="font-weight:600">${esc(h.ssh)}</div><div class="xs subtle">${esc(h.sources.join(" · "))}</div></div><button type="button" class="btn btn-sm" data-disc-idx="${i}">Probe</button></div>`).join("")}</div>` : ""}
        <div class="field-grid">
          <div><label class="field-label">SSH target</label><div class="row" style="flex-wrap:nowrap"><input id="ae-ssh" class="input mono" style="width:100%" placeholder="user@host" /><button type="button" class="btn btn-sm" id="ae-probe-btn">Probe</button></div></div>
          <div><label class="field-label">Environment ID</label><input id="ae-id" class="input mono" style="width:100%" placeholder="user@host" /></div>
        </div>
        <div id="ae-probe-status" class="small muted" style="margin-bottom:12px"></div>
        <div class="field-grid">
          <div><label class="field-label">Profiles</label><input id="ae-profiles" class="input mono" style="width:100%" value="base" /></div>
          <div><label class="field-label">Harness targets</label><input id="ae-targets" class="input mono" style="width:100%" value="claude, codex" /></div>
        </div>
      `,
      buttons: [
        { label: "Cancel", cls: "btn" },
        {
          label: "Add environment", cls: "btn btn-pri", close: false,
          onClick: async () => {
            const csv = (v) => v.split(",").map((x) => x.trim()).filter(Boolean);
            const ssh = document.getElementById("ae-ssh").value.trim();
            const id = document.getElementById("ae-id").value.trim() || ssh;
            if (!ssh) return;
            try {
              await apiPost("/api/env/add", { id, ssh, providers: ["claude", "codex"], profiles: csv(document.getElementById("ae-profiles").value), skillshare_targets: csv(document.getElementById("ae-targets").value), external: [], roles: [] });
              closeModal();
              toast(`Added ${id}`, "ok");
              await refreshState(true);
              goEnv(id, "overview");
            } catch (err) { toast(err.message, "err"); }
          },
        },
      ],
    });
    const doProbe = async (sshVal, sugId) => {
      document.getElementById("ae-ssh").value = sshVal;
      if (sugId) document.getElementById("ae-id").value = sugId;
      document.getElementById("ae-probe-status").textContent = `Probing ${sshVal}…`;
      try {
        const res = await apiPost("/api/env/probe", { ssh: sshVal, id: sugId || "" });
        if (!res.probe?.ok) { document.getElementById("ae-probe-status").innerHTML = `<span class="tag tag-bad">${esc(res.probe?.error || "failed")}</span>`; return; }
        document.getElementById("ae-id").value = res.suggested?.id || sshVal;
        document.getElementById("ae-profiles").value = (res.suggested?.profiles || []).join(", ");
        document.getElementById("ae-targets").value = (res.suggested?.skillshare_targets || []).join(", ");
        document.getElementById("ae-probe-status").innerHTML = `<span class="tag tag-ok">${esc(res.probe.os)}/${esc(res.probe.arch)}</span>`;
      } catch (err) { document.getElementById("ae-probe-status").textContent = err.message; }
    };
    for (const b of document.querySelectorAll("[data-disc-idx]")) {
      const h = uncfg[Number(b.dataset.discIdx)];
      b.onclick = () => doProbe(h.ssh, h.suggested_id);
    }
    document.getElementById("ae-probe-btn").onclick = () => { const v = document.getElementById("ae-ssh").value.trim(); if (v) doProbe(v, document.getElementById("ae-id").value.trim()); };
  }

  async function openGitChangesModal() {
    let g;
    try {
      g = await apiGet("/api/git/diff");
    } catch (err) {
      toast(err.message, "err");
      return;
    }

    const files = Array.isArray(g.files) ? g.files : [];
    const commits = Array.isArray(g.recent_commits) ? g.recent_commits : [];
    const statusLabel = { M: ["Modified", "tag-warn"], A: ["Added", "tag-ok"], "?": ["New", "tag-ok"], D: ["Deleted", "tag-bad"], R: ["Renamed", "tag-accent"] };

    const renderFileDiff = (f) => {
      const [stText, stCls] = statusLabel[f.status] || ["Changed", ""];
      const statsHtml = `
        <span class="diff-stat">
          ${f.additions ? `<span class="diff-stat-add">+${f.additions}</span>` : ""}
          ${f.deletions ? `<span class="diff-stat-del">−${f.deletions}</span>` : ""}
        </span>`;
      let inner = "";
      if (f.binary) {
        inner = `<div class="card-body small muted">Binary file changed</div>`;
      } else if (!f.hunks || !f.hunks.length) {
        inner = `<div class="card-body small muted">${f.status === "D" ? "File deleted" : "No text diff"}</div>`;
      } else {
        inner = `
          <div class="diff-scroll">
            <table class="diff-table">
              <tbody>
                ${f.hunks.map((h) => `
                  <tr class="diff-hunk"><td colspan="4">${esc(h.header)}</td></tr>
                  ${(h.lines || []).map((ln) => {
                    const cls = ln.kind === "add" ? "diff-add" : ln.kind === "del" ? "diff-del" : "diff-ctx";
                    const sign = ln.kind === "add" ? "+" : ln.kind === "del" ? "−" : " ";
                    return `<tr class="${cls}"><td class="diff-ln">${ln.old_num || ""}</td><td class="diff-ln">${ln.new_num || ""}</td><td class="diff-sign">${sign}</td><td class="diff-code">${esc(ln.text)}</td></tr>`;
                  }).join("")}
                `).join("")}
              </tbody>
            </table>
          </div>`;
      }
      return `
        <div class="diff-file">
          <div class="diff-head">
            <div class="row" style="gap:8px;min-width:0">
              <span class="tag ${stCls}">${stText}</span>
              <span class="mono small" style="font-weight:600;overflow:hidden;text-overflow:ellipsis">${esc(f.path)}</span>
              ${statsHtml}
            </div>
            <button type="button" class="btn btn-sm btn-ghost" data-git-revert="${esc(f.path)}" title="Discard changes to ${esc(f.path)}">Revert</button>
          </div>
          ${inner}
        </div>`;
    };

    const totalAdd = files.reduce((s, f) => s + (f.additions || 0), 0);
    const totalDel = files.reduce((s, f) => s + (f.deletions || 0), 0);
    const defaultMsg = files.length === 1 ? `Update ${files[0].path}` : `Update ${files.length} control plane files`;

    const commitsHtml = commits.length ? `
      <div style="margin-top:${g.dirty ? "18px" : "12px"}">
        <div class="cfg-label" style="margin-bottom:8px">Recent commits</div>
        <div class="card" style="margin-bottom:0">
          ${commits.map((c) => `
            <div class="list-row" style="padding:9px 14px">
              <div class="row" style="gap:10px;flex-wrap:nowrap;min-width:0">
                <span class="tag mono">${esc(c.sha)}</span>
                <span class="small" style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(c.subject)}</span>
              </div>
              <span class="xs subtle" style="flex-shrink:0">${esc(c.when)}</span>
            </div>`).join("")}
        </div>
      </div>` : "";

    let bodyHtml = "";
    const buttons = [];

    if (g.dirty) {
      bodyHtml = `
        <div class="row-between" style="margin-bottom:12px">
          <span class="small muted">${files.length} file${files.length === 1 ? "" : "s"} changed in <span class="mono">${esc(g.repo_root || "control plane")}</span></span>
          <span class="diff-stat">
            ${totalAdd ? `<span class="diff-stat-add">+${totalAdd}</span>` : ""}
            ${totalDel ? `<span class="diff-stat-del">−${totalDel}</span>` : ""}
          </span>
        </div>
        ${files.map(renderFileDiff).join("")}
        <div class="diff-commit-bar">
          <input id="git-commit-msg" class="input" style="flex:1" value="${esc(defaultMsg)}" placeholder="Commit message…" />
        </div>
      `;
      buttons.push(
        {
          label: "Discard all",
          cls: "btn btn-bad",
          close: false,
          onClick: async (btn) => {
            if (btn.dataset.armed !== "1") {
              btn.dataset.armed = "1";
              btn.textContent = "Confirm discard all?";
              return;
            }
            setModalLocked(true);
            btn.disabled = true;
            btn.innerHTML = `<span class="spinner"></span><span>Discarding…</span>`;
            try {
              await apiPost("/api/git/discard", { confirm: true });
              state.bundles = {};
              closeModal(true);
              toast("Discarded all control plane changes", "ok");
              await refreshState(true);
            } catch (err) {
              setModalLocked(false);
              btn.disabled = false;
              btn.dataset.armed = "";
              btn.textContent = "Discard all";
              toast(err.message, "err");
            }
          },
        },
        { label: "Close", cls: "btn" },
        {
          label: "Commit changes",
          cls: "btn btn-pri",
          close: false,
          onClick: async (btn) => {
            const msgEl = document.getElementById("git-commit-msg");
            const message = msgEl ? msgEl.value.trim() : defaultMsg;
            setModalLocked(true);
            btn.disabled = true;
            btn.innerHTML = `<span class="spinner"></span><span>Committing…</span>`;
            try {
              const res = await apiPost("/api/git/commit", { message });
              closeModal(true);
              toast(`Committed ${res.sha || ""}: ${res.message || message}`, "ok");
              await refreshState(true);
            } catch (err) {
              setModalLocked(false);
              btn.disabled = false;
              btn.textContent = "Commit changes";
              toast(err.message, "err");
            }
          },
        },
      );
    } else {
      bodyHtml = `
        <div class="row" style="gap:8px;margin-bottom:4px">
          <span class="tag tag-ok">Working tree clean ✓</span>
          ${g.repo_root ? `<span class="xs mono subtle">${esc(g.repo_root)}</span>` : ""}
        </div>
        ${commitsHtml}
      `;
      buttons.push({ label: "Close", cls: "btn" });
    }

    openModal({
      title: g.dirty ? `Control plane changes (${files.length})` : "Control plane history",
      wide: true,
      bodyHtml,
      buttons,
    });

    for (const b of document.querySelectorAll("[data-git-revert]")) {
      b.onclick = async () => {
        const path = b.dataset.gitRevert;
        if (b.dataset.armed !== "1") {
          b.dataset.armed = "1";
          b.textContent = "Confirm revert?";
          b.classList.remove("btn-ghost");
          b.classList.add("btn-bad");
          return;
        }
        b.disabled = true;
        b.innerHTML = `<span class="spinner"></span>`;
        try {
          await apiPost("/api/git/discard", { confirm: true, path });
          state.bundles = {};
          toast(`Reverted ${path}`, "ok");
          await refreshState(true);
          openGitChangesModal();
        } catch (err) {
          b.disabled = false;
          b.dataset.armed = "";
          b.textContent = "Revert";
          toast(err.message, "err");
        }
      };
    }

    const msgInput = document.getElementById("git-commit-msg");
    if (msgInput) {
      msgInput.onkeydown = (e) => {
        if (e.key === "Enter") {
          const pri = document.querySelector("#modal-footer .btn-pri");
          if (pri) pri.click();
        }
      };
    }
  }

  document.getElementById("git-status-btn").onclick = () => openGitChangesModal();

  async function navigateFromCurrentURL() {
    applyRouteFromLocation();
    if (state.scope === "env" && state.selectedTarget) {
      const exists = (state.data?.targets || []).some((t) => t.id === state.selectedTarget);
      if (exists) {
        await goEnv(state.selectedTarget, state.envTab, false);
        return;
      }
      state.scope = "fleet";
      state.fleetTab = "environments";
      syncURL(true);
    }
    await goFleet(state.fleetTab, state.fleetTab === "catalog" ? state.catalogSub : null, false);
  }

  window.addEventListener("popstate", () => {
    if (state.data) navigateFromCurrentURL();
  });

  applyRouteFromLocation();
  connectLiveEvents();
  refreshState().then(async () => {
    await navigateFromCurrentURL();
    syncURL(true);
    if (!state.syncBootstrapped) {
      state.syncBootstrapped = true;
      runFleetSyncStatus(true);
    }
  });
})();
