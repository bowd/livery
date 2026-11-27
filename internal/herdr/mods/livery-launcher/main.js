// Livery Launcher Wizard: stepped keyboard-first launcher for starting an agent
// or shell thread in any space across local and remote machines.
// Managed by Livery (`liv herdr install-mod`).
//
// Steps:
//   1. Space   — pick an existing space (any machine) or create a new space
//   2. Profile — flat, searchable list of `liv <harness> <profile>` profiles,
//                saved presets, installed agents (`knownAgents`), or shell
//   3. Launch  — optional prompt (`task`), tab name, harness flag chips / extra
//                args, placement (tab, background, split, worktree), and
//                optional `agent-folders` folder assignment
//
// Keyboard:
//   ↑/↓          move selection
//   Enter        advance step (Step 1/2) or launch (Step 3)
//   Cmd+Enter    quick-launch immediately from Step 2 or Step 3
//   Shift+Tab / Alt+Left   go back a step
//   Esc          close wizard

import { View, div, image, svg } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, ScrollHandle } from "gpui-base";
import {
  agents,
  spaces,
  panes,
  run,
  exec,
  hc,
  knownAgents,
  pickFolder,
  home,
  theme,
  shared,
  setShared,
  openOverlay,
  closeOverlay,
  props,
  window,
  checking,
  nextCommand,
} from "herdr";

const MARK = {
  ansi6: "#22a99a",
  ansi3: "#e3814f",
  ansi4: "#5b8def",
  ansi5: "#8b7cf6",
  ansi2: "#3fae6a",
  ansi1: "#d9649a",
};
const SPACE_PALETTE = ["ansi6", "ansi3", "ansi4", "ansi5", "ansi2", "ansi1"];

// Keep shared storage keys backward-compatible with previous `agent-launcher` state
// so existing per-profile defaults, presets, and recents carry over seamlessly.
const SHARED_CACHE = "agentLauncher.cache";
const SHARED_PRESETS = "agentLauncher.presets";
const SHARED_DEFAULTS = "agentLauncher.defaults";
const SHARED_RECENT = "agentLauncher.recent";
const SHARED_NEW_MENU = "new-menu.agents";

const I = (name) => `mods/workspace-chrome/${name}.svg`;

// Map harness names to Herdr's shipped icons and human-friendly display titles.
const HARNESS_META = {
  claude: { label: "Claude", icon: "icons/claude.svg", tint: false },
  codex: { label: "Codex", icon: "icons/codex.svg", tint: false },
  opencode: { label: "OpenCode", icon: "icons/opencode.svg", tint: true },
  openclaw: { label: "OpenClaw", icon: "icons/agent.svg", tint: true },
  agy: { label: "Antigravity", icon: "icons/antigravity.svg", tint: false },
  antigravity: { label: "Antigravity", icon: "icons/antigravity.svg", tint: false },
  pi: { label: "Pi", icon: "icons/pi.svg", tint: true },
  omp: { label: "OMP", icon: "icons/omp.svg", tint: true },
  gemini: { label: "Gemini", icon: "icons/gemini.svg", tint: true },
  cursor: { label: "Cursor", icon: "icons/cursor.svg", tint: true },
  copilot: { label: "Copilot", icon: "icons/copilot.svg", tint: true },
  amp: { label: "Amp", icon: "icons/amp.svg", tint: false },
  goose: { label: "Goose", icon: "icons/goose.svg", tint: true },
  shell: { label: "Shell", icon: "icons/shell.svg", tint: true },
};

// Harness-specific quick launch mode chips shown on Step 3.
const HARNESS_MODES = {
  claude: [
    { id: "default", label: "Default", args: [] },
    { id: "yolo", label: "Skip perms", args: ["--dangerously-skip-permissions"] },
    { id: "plan", label: "Plan", args: ["--permission-mode", "plan"] },
    { id: "continue", label: "Continue", args: ["--continue"] },
    { id: "resume", label: "Resume", args: ["--resume"] },
  ],
  codex: [
    { id: "default", label: "Default", args: [] },
    { id: "auto", label: "Full auto", args: ["--full-auto"] },
    { id: "yolo", label: "YOLO", args: ["--dangerously-bypass-approvals-and-sandbox"] },
    { id: "resume", label: "Resume last", args: ["resume", "--last"] },
  ],
  agy: [
    { id: "default", label: "Default", args: [] },
    { id: "continue", label: "Continue", args: ["--continue"] },
  ],
  antigravity: [
    { id: "default", label: "Default", args: [] },
    { id: "continue", label: "Continue", args: ["--continue"] },
  ],
  opencode: [
    { id: "default", label: "Default", args: [] },
    { id: "continue", label: "Continue", args: ["--continue"] },
  ],
  default: [
    { id: "default", label: "Default", args: [] },
    { id: "continue", label: "Continue", args: ["--continue"] },
  ],
};

function harnessMeta(harness) {
  const key = (harness || "").toLowerCase();
  if (HARNESS_META[key]) return HARNESS_META[key];
  const label = key ? key[0].toUpperCase() + key.slice(1) : "Agent";
  return { label, icon: "icons/agent.svg", tint: true };
}

function deriveProfileName(p) {
  if (typeof p.profile === "string" && p.profile.trim()) return p.profile.trim();
  const id = String(p.id || "");
  const h = String(p.harness || "").toLowerCase();
  if (h && id.toLowerCase().startsWith(h + "-")) {
    return id.slice(h.length + 1);
  }
  return id;
}

function livePlace(space, allPanes) {
  if (!space) return "";
  const tabIds = new Set((space.tabs ?? []).map((t) => t.id));
  return allPanes.find((p) => tabIds.has(p.tab) && p.cwd)?.cwd || space.folder || "";
}

function tilde(path, local) {
  if (!path) return "";
  if (!local) return path;
  const h = home().replace(/\/+$/, "");
  return path === h ? "~" : path.startsWith(h + "/") ? `~${path.slice(h.length)}` : path;
}

function spaceColor(t, s) {
  if (!s || s.name === "~") return t.dim;
  const stored = shared("space.colors") ?? {};
  const c = stored[s.id] ?? SPACE_PALETTE[Math.max(0, (s.index ?? 1) - 1) % SPACE_PALETTE.length];
  return c.startsWith("#") ? c : (MARK[c] ?? t[c] ?? t.accent);
}

// Shell-style argument tokenizer supporting single quotes, double quotes, and escapes.
function parseArgs(text) {
  const s = (text ?? "").trim();
  if (!s) return [];
  const out = [];
  let cur = "";
  let quote = null;
  let escaped = false;
  let hasToken = false;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    if (escaped) {
      cur += ch;
      escaped = false;
      hasToken = true;
      continue;
    }
    if (ch === "\\" && quote !== "'") {
      escaped = true;
      hasToken = true;
      continue;
    }
    if (quote) {
      if (ch === quote) quote = null;
      else cur += ch;
      continue;
    }
    if (ch === "'" || ch === '"') {
      quote = ch;
      hasToken = true;
      continue;
    }
    if (/\s/.test(ch)) {
      if (hasToken) {
        out.push(cur);
        cur = "";
        hasToken = false;
      }
      continue;
    }
    cur += ch;
    hasToken = true;
  }
  if (hasToken) out.push(cur);
  return out;
}

function shellQuote(arg) {
  return /^[a-zA-Z0-9_@%+=:,./-]+$/.test(arg) ? arg : `'${arg.replace(/'/g, "'\\''")}'`;
}

function renderIcon(iconPath, tint, size, color) {
  if (!iconPath) return svg("icons/agent.svg").size(size).flex_none().text_color(color);
  return tint
    ? svg(iconPath).size(size).flex_none().text_color(color)
    : image(iconPath).w(size).h(size).flex_none();
}

function readPresets() {
  const list = shared(SHARED_PRESETS);
  if (!Array.isArray(list)) return [];
  return list.filter((p) => p && typeof p.id === "string" && typeof p.name === "string" && Array.isArray(p.argv) && p.argv.length > 0);
}

function readDefaults() {
  const map = shared(SHARED_DEFAULTS);
  return map && typeof map === "object" && !Array.isArray(map) ? map : {};
}

function lookupDefault(item) {
  if (!item || item.kind === "shell") return null;
  const defs = readDefaults();
  return defs[item.key] ?? (item.legacyKey ? defs[item.legacyKey] : null) ?? null;
}

function readRecent() {
  const list = shared(SHARED_RECENT);
  return Array.isArray(list) ? list : [];
}

function readFolders() {
  const list = shared("agentFolders.list");
  return Array.isArray(list) ? list.filter((f) => f && typeof f.id === "string" && typeof f.name === "string") : [];
}

// Query `liv list --json` (falling back to legacy `agent-launch` / `al`) on local or remote machine.
async function fetchLiveryProfiles(machine) {
  const cmd =
    'for b in liv "$HOME/.local/bin/liv" /opt/homebrew/bin/liv /usr/local/bin/liv "$HOME/.nix-profile/bin/liv" /run/current-system/sw/bin/liv; do ' +
    'if command -v "$b" >/dev/null 2>&1; then printf "CLI=liv\n"; exec "$b" list --json; fi; ' +
    'if [ -x "$b" ]; then printf "CLI=liv\n"; exec "$b" list --json; fi; ' +
    "done; " +
    'for b in agent-launch al "$HOME/.local/bin/agent-launch" "$HOME/.local/bin/al"; do ' +
    'if command -v "$b" >/dev/null 2>&1; then printf "CLI=agent-launch\n"; exec "$b" list --json; fi; ' +
    'if [ -x "$b" ]; then printf "CLI=agent-launch\n"; exec "$b" list --json; fi; ' +
    "done; exit 127";
  try {
    const r = await exec("sh", ["-lc", cmd], { on: machine ?? null, timeout: 8000 });
    if (r.code !== 0) return { installed: false, cli: null, profiles: [] };
    const out = String(r.stdout || "");
    const nl = out.indexOf("\n");
    const first = nl >= 0 ? out.slice(0, nl).trim() : "";
    const cli = first.startsWith("CLI=") ? first.slice(4) : "liv";
    const jsonText = first.startsWith("CLI=") && nl >= 0 ? out.slice(nl + 1) : out;
    const parsed = JSON.parse(jsonText || "[]");
    return { installed: true, cli, profiles: Array.isArray(parsed) ? parsed : [] };
  } catch {
    return { installed: false, cli: null, profiles: [] };
  }
}

export default class LiveryLauncher extends View {
  init(_props, cx) {
    const p = props() ?? {};
    this.handle = cx.focus_handle();
    this.spaceScroll = ScrollHandle.new();
    this.profileScroll = ScrollHandle.new();

    // Inputs — created once in init, never in render.
    this.spaceFilter = "";
    this.spaceInput = InputState.new({ placeholder: "Filter spaces, folders, or machines…" });
    this.spaceInput.on("change", (_e, cx) => {
      this.spaceFilter = this.spaceInput.value();
      this.spaceAt = 0;
      this.revealedSpace = -1;
      cx.notify();
    });
    this.spaceInput.on("submit", (_e, cx) => this.selectCurrentSpace(cx));

    this.profileFilter = "";
    this.profileInput = InputState.new({ placeholder: "Filter profiles, harnesses, or commands… (e.g. claude gateway)" });
    this.profileInput.on("change", (_e, cx) => {
      this.profileFilter = this.profileInput.value();
      this.profileAt = 0;
      this.revealedProfile = -1;
      cx.notify();
    });
    this.profileInput.on("submit", (e, cx) => {
      const item = this.profileRows()[this.profileAt];
      if (!item) return;
      this.pickProfile(item, Boolean(e?.secondary), cx);
    });

    this.promptText = "";
    this.promptInput = InputState.new({ placeholder: "Optional first message / task (Enter launches)…" });
    this.promptInput.on("change", (_e, cx) => { this.promptText = this.promptInput.value(); cx.notify(); });
    this.promptInput.on("submit", (_e, cx) => this.launch(cx));

    this.nameText = "";
    this.nameInput = InputState.new({ placeholder: "Optional tab name (auto if blank)" });
    this.nameInput.on("change", (_e, cx) => { this.nameText = this.nameInput.value(); cx.notify(); });
    this.nameInput.on("submit", (_e, cx) => this.launch(cx));

    this.argsText = "";
    this.argsInput = InputState.new({ placeholder: "Extra flags, e.g. --model opus" });
    this.argsInput.on("change", (_e, cx) => {
      this.argsText = this.argsInput.value();
      this.presetFeedback = "";
      cx.notify();
    });
    this.argsInput.on("submit", (_e, cx) => this.launch(cx));

    this.folderText = "";
    this.folderInput = InputState.new({ placeholder: "Folder path on target machine (default: home)" });
    this.folderInput.on("change", (_e, cx) => { this.folderText = this.folderInput.value(); cx.notify(); });
    this.folderInput.on("submit", (_e, cx) => this.launch(cx));

    this.spaceNameText = "";
    this.spaceNameInput = InputState.new({ placeholder: "Optional space name (auto from folder)" });
    this.spaceNameInput.on("change", (_e, cx) => { this.spaceNameText = this.spaceNameInput.value(); cx.notify(); });
    this.spaceNameInput.on("submit", (_e, cx) => this.launch(cx));

    this.presetNameText = "";
    this.presetNameInput = InputState.new({ placeholder: "Preset name (e.g. Claude · gateway · YOLO)" });
    this.presetNameInput.on("change", (_e, cx) => { this.presetNameText = this.presetNameInput.value(); cx.notify(); });
    this.presetNameInput.on("submit", (_e, cx) => this.savePreset(cx));

    this.focusedField = null;
    const track = (name, state) => {
      state.on("focus", (_e, cx) => { this.focusedField = name; cx.notify(); });
      state.on("blur", (_e, cx) => { if (this.focusedField === name) { this.focusedField = null; cx.notify(); } });
    };
    track("space", this.spaceInput);
    track("profile", this.profileInput);
    track("prompt", this.promptInput);
    track("name", this.nameInput);
    track("args", this.argsInput);
    track("folder", this.folderInput);
    track("spaceName", this.spaceNameInput);
    track("presetName", this.presetNameInput);

    // Wizard state
    this.connectedMachines = [];
    this.machineData = {}; // machineKey -> { loading, agents, al, alInstalled, cli, error }
    this.showUninstalled = false;
    this.savingPreset = false;
    this.presetFeedback = "";
    this.modeId = "default";
    this.placement = "tab";
    this.folderId = typeof p.folder === "string" ? p.folder : null;

    // Optional scoping when opened from a Project header in agent-folders
    this.projectSpaces = Array.isArray(p.projectSpaces) && p.projectSpaces.length ? p.projectSpaces : null;
    this.projectName = typeof p.projectName === "string" ? p.projectName : null;
    this.scopeToProject = Boolean(this.projectSpaces && this.projectSpaces.length > 1);

    this.spaceAt = 0;
    this.profileAt = 0;
    this.revealedSpace = -1;
    this.revealedProfile = -1;
    this.target = null;
    this.profile = null;
    this.step = "space";

    // Pre-select a space if explicitly requested (or single-space project).
    const preSpaceId = p.space ?? (this.projectSpaces?.length === 1 ? this.projectSpaces[0] : null);
    if (preSpaceId) {
      const s = spaces().find((x) => x.id === preSpaceId);
      if (s) {
        this.target = this.toSpaceTarget(s, panes());
        this.step = "profile";
      }
    }

    // Seed machine caches from shared storage so Step 2 renders immediately.
    const cache = shared(SHARED_CACHE) ?? {};
    const nmCache = shared(SHARED_NEW_MENU) ?? {};
    for (const key of new Set([...Object.keys(cache), ...Object.keys(nmCache)])) {
      this.machineData[key] = {
        loading: false,
        agents: cache[key]?.agents ?? nmCache[key] ?? [],
        al: cache[key]?.al ?? [],
        alInstalled: cache[key]?.alInstalled ?? null,
        cli: cache[key]?.cli ?? null,
        error: null,
      };
    }

    if (!checking()) {
      this.loadMachines(cx);
      const initialMachine = this.target ? this.target.machine : (spaces().find((s) => s.active)?.machine ?? null);
      this.loadMachineData(initialMachine, cx);
      cx.spawn(async (cx) => {
        for (;;) {
          const cmd = await nextCommand();
          if (cmd === "next") {
            if (this.step === "space") this.selectCurrentSpace(cx);
            else if (this.step === "profile") {
              const item = this.profileRows()[this.profileAt];
              if (item) this.pickProfile(item, false, cx);
            }
          } else if (cmd === "back") {
            this.goBack(cx);
          } else if (cmd === "launch") {
            this.launch(cx);
          }
          cx.notify();
        }
      });
    }

    this.focusStep(cx);
  }

  toSpaceTarget(s, allPanes) {
    return {
      kind: "space",
      id: s.id,
      name: s.name || "~",
      machine: s.machine ?? null,
      folder: livePlace(s, allPanes),
      branch: s.branch ?? null,
      active: s.active,
      status: s.status,
      tabCount: (s.tabs ?? []).length,
      index: s.index,
    };
  }

  focusStep(cx) {
    this.handle.focus();
    const targetInput = () => {
      if (this.step === "space") return this.spaceInput;
      if (this.step === "profile") return this.profileInput;
      if (this.savingPreset) return this.presetNameInput;
      if (this.target?.kind === "new-space" && !this.folderText) return this.folderInput;
      if (this.profile?.kind === "shell") return this.nameInput;
      return this.promptInput;
    };
    targetInput().focus();
    cx.timer.after(80, () => targetInput().focus());
    cx.timer.after(260, () => targetInput().focus());
  }

  close() {
    closeOverlay(props()?.overlay);
  }

  loadMachines(cx) {
    cx.spawn(async (cx) => {
      try {
        const r = await hc(["machine", "ls", "--json"]);
        if (r.code === 0) {
          this.connectedMachines = (JSON.parse(r.stdout).machines ?? [])
            .filter((m) => m.state === "connected" && !m.temporary)
            .map((m) => m.name);
        }
      } catch {}
      cx.notify();
    });
  }

  loadMachineData(machine, cx) {
    const key = machine ?? "";
    const cur = this.machineData[key] ?? { loading: false, agents: [], al: [], alInstalled: null, cli: null, error: null };
    if (cur.loading) return;
    this.machineData[key] = { ...cur, loading: true, error: null };
    cx.notify();
    cx.spawn(async (cx) => {
      const [agentsRes, alRes] = await Promise.allSettled([
        knownAgents({ on: machine ?? null }),
        fetchLiveryProfiles(machine ?? null),
      ]);
      const prev = this.machineData[key] ?? {};
      const nextAgents = agentsRes.status === "fulfilled" ? agentsRes.value : (prev.agents ?? []);
      const nextAl = alRes.status === "fulfilled" ? alRes.value.profiles : (prev.al ?? []);
      const alInstalled = alRes.status === "fulfilled" ? alRes.value.installed : prev.alInstalled;
      const cli = alRes.status === "fulfilled" ? alRes.value.cli : prev.cli;
      const error = agentsRes.status === "rejected" && !nextAgents.length ? "Could not check installed agents on this machine" : null;
      this.machineData[key] = { loading: false, agents: nextAgents, al: nextAl, alInstalled, cli, error };
      try {
        const prevCache = shared(SHARED_CACHE) ?? {};
        setShared(SHARED_CACHE, {
          ...prevCache,
          [key]: { agents: nextAgents, al: nextAl, alInstalled, cli, at: Date.now() },
        });
      } catch {}
      cx.notify();
    });
  }

  // Build ordered list of Step 1 rows (existing spaces + "New space on …" rows).
  spaceRows() {
    const f = this.spaceFilter.trim().toLowerCase();
    const allSpaces = spaces();
    const allPanes = panes();
    const allAgents = agents();
    const recent = readRecent();
    const recentRank = new Map();
    recent.forEach((r, idx) => {
      if (r.targetKey && !recentRank.has(r.targetKey)) recentRank.set(r.targetKey, idx);
    });

    const agentCountBySpace = new Map();
    for (const a of allAgents) agentCountBySpace.set(a.space, (agentCountBySpace.get(a.space) ?? 0) + 1);

    const scopeSet = this.scopeToProject && this.projectSpaces ? new Set(this.projectSpaces) : null;
    const spaceItems = allSpaces
      .filter((s) => !scopeSet || scopeSet.has(s.id))
      .map((s) => ({
        ...this.toSpaceTarget(s, allPanes),
        agentCount: agentCountBySpace.get(s.id) ?? 0,
      }))
      .sort((a, b) => {
        if (a.active !== b.active) return a.active ? -1 : 1;
        const ra = recentRank.get(`space:${a.id}`) ?? 9999;
        const rb = recentRank.get(`space:${b.id}`) ?? 9999;
        if (ra !== rb) return ra - rb;
        const ma = a.machine ?? "";
        const mb = b.machine ?? "";
        if (ma !== mb) return ma.localeCompare(mb);
        return a.index - b.index;
      })
      .filter((s) => {
        if (!f) return true;
        return [s.name, s.machine ?? "local this computer", s.folder, s.branch ?? ""].join(" ").toLowerCase().includes(f);
      });

    const remoteMachines = [...new Set([...allSpaces.map((s) => s.machine).filter(Boolean), ...this.connectedMachines])].sort();
    const newItems = scopeSet
      ? []
      : [null, ...remoteMachines]
          .map((m) => ({
            kind: "new-space",
            id: `new:${m ?? "local"}`,
            machine: m,
            name: m ? `New space on ${m}` : "New space on This computer",
            folder: "",
          }))
          .filter((item) => !f || `${item.name} new space ${item.machine ?? "local"}`.toLowerCase().includes(f));

    return [...spaceItems, ...newItems];
  }

  // Build ordered list of Step 2 rows for `this.target.machine`.
  profileRows() {
    const f = this.profileFilter.trim().toLowerCase();
    const machineKey = this.target?.machine ?? "";
    const data = this.machineData[machineKey] ?? { loading: false, agents: [], al: [], alInstalled: null, cli: null };
    const recent = readRecent();
    const targetKey = this.target?.kind === "space" ? `space:${this.target.id}` : `new:${machineKey || "local"}`;
    const recentScore = new Map();
    recent.forEach((r, idx) => {
      if (!r.profileKey) return;
      const bonus = r.targetKey === targetKey ? 0 : 100;
      const score = bonus + idx;
      if (!recentScore.has(r.profileKey) || score < recentScore.get(r.profileKey)) recentScore.set(r.profileKey, score);
    });

    const items = [];
    const cliName = data.cli || "liv";

    // 1. Livery (`liv <harness> <profile>`) profiles — flat, consistent, searchable
    const alKeys = new Set();
    for (const p of data.al ?? []) {
      const h = (p.harness || "").toLowerCase();
      const hm = harnessMeta(h);
      const profName = deriveProfileName(p);
      const useLiv = cliName === "liv" || Array.isArray(p.command);
      const argv = Array.isArray(p.command) && p.command.length
        ? p.command
        : useLiv
          ? ["liv", h, profName]
          : p.shim
            ? [p.shim]
            : ["agent-launch", "run", p.id];

      alKeys.add(p.id);
      if (p.shim) alKeys.add(p.shim);
      const displayName = `${hm.label} · ${profName}`;
      const legacyKey = `al:${p.id}`;
      items.push({
        kind: "livery",
        section: cliName === "liv" ? "Livery profiles" : "Launch profiles",
        key: `liv:${h}:${profName}`,
        legacyKey,
        id: p.id,
        harnessLabel: hm.label,
        profileName: profName,
        name: displayName,
        harness: h || "agent",
        argv,
        needsDashDash: !useLiv && argv[0] === "agent-launch",
        shim: p.shim || "",
        authMode: p.auth_mode || "",
        isolationMode: p.mode || "",
        sessionGroup: p.session_group || "",
        source: p.source || "",
        secretsOk: p.secrets_ok !== false,
        missingSecrets: p.missing_secrets ?? [],
        description: p.description || "",
        icon: hm.icon,
        tint: hm.tint,
        installed: true,
      });
    }

    // 2. User-saved presets
    for (const pr of readPresets()) {
      const h = (pr.harness || "").toLowerCase();
      const hm = harnessMeta(h);
      items.push({
        kind: "preset",
        section: "Saved presets",
        key: `preset:${pr.id}`,
        id: pr.id,
        name: pr.name,
        harness: h || "default",
        argv: pr.argv,
        description: pr.argv.join(" "),
        icon: pr.icon || hm.icon,
        tint: pr.tint ?? hm.tint,
        installed: true,
      });
    }

    // 3. Installed Herdr knownAgents on this machine (deduped against shims)
    const allKnown = (data.agents ?? []).filter((a) => !alKeys.has(a.key) && !alKeys.has(a.run?.[0]));
    const installedAgents = allKnown.filter((a) => a.installed);
    const uninstalledAgents = this.showUninstalled ? allKnown.filter((a) => !a.installed) : [];
    for (const a of [...installedAgents, ...uninstalledAgents]) {
      items.push({
        kind: "agent",
        section: a.installed ? "Installed agents" : "Not installed",
        key: `agent:${a.key}`,
        id: a.key,
        name: a.name,
        harness: a.key,
        argv: a.run,
        description: (a.run ?? []).join(" "),
        icon: a.icon,
        tint: a.tint,
        installed: Boolean(a.installed),
      });
    }

    // 4. Plain shell
    items.push({
      kind: "shell",
      section: "Shell",
      key: "shell",
      id: "shell",
      name: "Terminal (default shell)",
      harness: "shell",
      argv: null,
      description: "Open a plain interactive shell",
      icon: "icons/shell.svg",
      tint: true,
      installed: true,
    });

    const secRank = {
      "Livery profiles": 0,
      "Launch profiles": 0,
      "Saved presets": 1,
      "Installed agents": 2,
      Shell: 3,
      "Not installed": 4,
    };
    const tokens = f ? f.split(/\s+/).filter(Boolean) : [];
    const filtered = items.filter((item) => {
      if (!tokens.length) return true;
      const hay = [
        item.name,
        item.id,
        item.harness,
        item.profileName ?? "",
        item.authMode ?? "",
        item.isolationMode ?? "",
        item.sessionGroup ?? "",
        item.description ?? "",
        (item.argv ?? []).join(" "),
      ]
        .join(" ")
        .toLowerCase();
      return tokens.every((tok) => hay.includes(tok));
    });
    const scoreOf = (item) => {
      const a = recentScore.get(item.key);
      const b = item.legacyKey ? recentScore.get(item.legacyKey) : undefined;
      if (a !== undefined && b !== undefined) return Math.min(a, b);
      return a ?? b ?? 9999;
    };
    filtered.sort((a, b) => {
      const sa = secRank[a.section] ?? 9;
      const sb = secRank[b.section] ?? 9;
      if (sa !== sb) return sa - sb;
      const ra = scoreOf(a);
      const rb = scoreOf(b);
      if (ra !== rb) return ra - rb;
      return 0;
    });
    return filtered;
  }

  modesFor(profile) {
    if (!profile || profile.kind === "shell") return [];
    const h = (profile.harness || "").toLowerCase();
    return HARNESS_MODES[h] ?? HARNESS_MODES.default;
  }

  buildArgvFor(profile, modeId, argsText) {
    if (!profile || profile.kind === "shell") return null;
    const modes = this.modesFor(profile);
    const modeArgs = modes.find((m) => m.id === modeId)?.args ?? [];
    const extraArgs = parseArgs(argsText);
    const combinedExtra = [...modeArgs, ...extraArgs];
    if (!combinedExtra.length) return [...profile.argv];
    if (profile.needsDashDash) {
      return [...profile.argv, "--", ...combinedExtra];
    }
    return [...profile.argv, ...combinedExtra];
  }

  buildArgv() {
    return this.buildArgvFor(this.profile, this.modeId, this.argsText);
  }

  selectCurrentSpace(cx) {
    const list = this.spaceRows();
    const chosen = list[this.spaceAt];
    if (!chosen) return;
    this.pickSpace(chosen, cx);
  }

  pickSpace(item, cx) {
    this.target = item;
    this.step = "profile";
    this.profileFilter = "";
    this.profileInput.set_value("");
    this.profileAt = 0;
    this.revealedProfile = -1;
    this.placement = "tab";
    if (!checking()) this.loadMachineData(item.machine, cx);
    this.focusStep(cx);
    cx.notify();
  }

  pickProfile(item, quickLaunch, cx) {
    if (!item.installed) return;
    this.profile = item;
    const saved = lookupDefault(item);
    const modes = this.modesFor(item);
    this.modeId = saved?.modeId && modes.some((m) => m.id === saved.modeId) ? saved.modeId : "default";
    this.argsText = typeof saved?.argsText === "string" ? saved.argsText : "";
    this.argsInput.set_value(this.argsText);
    this.savingPreset = false;
    this.presetFeedback = "";
    if (quickLaunch && this.target?.kind === "space") {
      this.launch(cx);
      return;
    }
    this.step = "options";
    this.focusStep(cx);
    cx.notify();
  }

  saveAsDefault(cx) {
    if (!this.profile || this.profile.kind === "shell") return;
    const modeId = this.modeId || "default";
    const argsText = this.argsText.trim();
    const next = { ...readDefaults() };
    if (modeId === "default" && !argsText) {
      delete next[this.profile.key];
      if (this.profile.legacyKey) delete next[this.profile.legacyKey];
      this.presetFeedback = "Cleared default";
    } else {
      next[this.profile.key] = { modeId, argsText };
      if (this.profile.legacyKey) delete next[this.profile.legacyKey];
      this.presetFeedback = "Saved as default";
    }
    setShared(SHARED_DEFAULTS, next);
    cx.notify();
  }

  clearDefault(cx) {
    if (!this.profile) return;
    const next = { ...readDefaults() };
    delete next[this.profile.key];
    if (this.profile.legacyKey) delete next[this.profile.legacyKey];
    setShared(SHARED_DEFAULTS, next);
    this.modeId = "default";
    this.argsText = "";
    this.argsInput.set_value("");
    this.presetFeedback = "Reset default";
    cx.notify();
  }

  goToStep(next, cx) {
    if (next === "profile" && !this.target) return;
    if (next === "options" && (!this.target || !this.profile)) return;
    this.savingPreset = false;
    this.step = next;
    this.focusStep(cx);
    cx.notify();
  }

  goBack(cx) {
    if (this.savingPreset) {
      this.savingPreset = false;
      this.focusStep(cx);
      cx.notify();
      return;
    }
    if (this.step === "options") this.goToStep("profile", cx);
    else if (this.step === "profile") this.goToStep("space", cx);
  }

  deletePreset(id, cx) {
    const next = readPresets().filter((p) => p.id !== id);
    setShared(SHARED_PRESETS, next);
    cx.notify();
  }

  savePreset(cx) {
    const name = this.presetNameText.trim();
    const argv = this.buildArgv();
    if (!name || !argv?.length) return;
    const id = `pr_${Date.now().toString(36)}${Math.floor(Math.random() * 1296).toString(36)}`;
    const next = [
      ...readPresets(),
      {
        id,
        name,
        harness: this.profile?.harness ?? "default",
        argv,
        icon: this.profile?.icon ?? null,
        tint: this.profile?.tint ?? true,
      },
    ];
    setShared(SHARED_PRESETS, next);
    this.savingPreset = false;
    this.presetNameText = "";
    this.presetNameInput.set_value("");
    this.presetFeedback = `Saved "${name}"`;
    this.focusStep(cx);
    cx.notify();
  }

  browseFolder(cx) {
    if (this.target?.machine) return;
    cx.spawn(async (cx) => {
      const picked = await pickFolder({ prompt: "Choose Space Folder" }).catch(() => null);
      if (picked) {
        this.folderText = picked;
        this.folderInput.set_value(picked);
        cx.notify();
      }
      this.focusStep(cx);
    });
  }

  recordRecent() {
    if (!this.target || !this.profile) return;
    const targetKey = this.target.kind === "space" ? `space:${this.target.id}` : `new:${this.target.machine ?? "local"}`;
    const entry = { targetKey, profileKey: this.profile.key, at: Date.now() };
    const prev = readRecent().filter(
      (r) => !(r.targetKey === entry.targetKey && (r.profileKey === entry.profileKey || (this.profile.legacyKey && r.profileKey === this.profile.legacyKey))),
    );
    setShared(SHARED_RECENT, [entry, ...prev].slice(0, 25));
  }

  recordPendingFolder() {
    if (!this.folderId || !this.target || this.profile?.kind === "shell") return;
    const now = Date.now();
    const existing = Array.isArray(shared("agentFolders.pending")) ? shared("agentFolders.pending") : [];
    const kept = existing.filter((x) => x && now - (x.at ?? 0) < 60000);
    const before = agents().map((a) => a.address);
    kept.push({
      space: this.target.kind === "space" ? this.target.id : null,
      machine: this.target.machine ?? null,
      folderId: this.folderId,
      before,
      at: now,
    });
    setShared("agentFolders.pending", kept);
  }

  launch(cx) {
    if (!this.target || !this.profile) return;
    const finalArgv = this.buildArgv();
    const tabName = this.nameText.trim();
    const task = finalArgv ? this.promptText.trim() : "";
    const spaceName = this.spaceNameText.trim();
    const folder = this.folderText.trim();

    this.recordRecent();
    this.recordPendingFolder();
    this.close();

    if (this.target.kind === "new-space") {
      run("space.new", {
        ...(spaceName ? { name: spaceName } : {}),
        ...(folder ? { folder } : {}),
        ...(tabName ? { tab: tabName } : {}),
        ...(finalArgv ? { argv: finalArgv } : {}),
        ...(task ? { task } : {}),
        machine: this.target.machine ?? "",
        ...(this.placement === "background" ? { focus: false } : {}),
      });
      return;
    }

    if (this.placement === "worktree") {
      openOverlay({
        mod: "worktree",
        kind: "dialog",
        width: 460,
        height: 280,
        props: {
          space: this.target.id,
          folder: this.target.folder || undefined,
          action: "new",
          ...(tabName ? { tab: tabName } : {}),
          ...(finalArgv ? { argv: finalArgv } : {}),
        },
      });
      return;
    }

    if (this.placement === "split-right" || this.placement === "split-down") {
      run("pane.split", {
        direction: this.placement === "split-right" ? "right" : "down",
        ...(finalArgv ? { argv: finalArgv } : {}),
      });
      return;
    }

    run("tab.new", {
      space: this.target.id,
      ...(tabName ? { name: tabName } : {}),
      ...(finalArgv ? { argv: finalArgv } : {}),
      ...(task ? { task } : {}),
      ...(this.placement === "background" ? { focus: false } : {}),
    });
  }

  onKeyDown(e, cx) {
    const k = e.key;
    const mod = e.modifiers ?? {};
    const cmdOrCtrl = Boolean(mod.platform || mod.control);

    if (k === "escape") {
      cx.stop_propagation();
      if (this.savingPreset) {
        this.savingPreset = false;
        this.focusStep(cx);
        cx.notify();
        return;
      }
      return this.close();
    }

    if ((k === "tab" && mod.shift) || (k === "left" && mod.alt)) {
      if (this.step !== "space" || this.savingPreset) {
        cx.stop_propagation();
        this.goBack(cx);
      }
      return;
    }

    if (this.step === "space") {
      const list = this.spaceRows();
      if (!list.length) return;
      if (k === "down") {
        this.spaceAt = (this.spaceAt + 1) % list.length;
      } else if (k === "up") {
        this.spaceAt = (this.spaceAt + list.length - 1) % list.length;
      } else if (k === "enter" || (k === "tab" && !mod.shift)) {
        this.selectCurrentSpace(cx);
      } else {
        return;
      }
      cx.stop_propagation();
      cx.notify();
      return;
    }

    if (this.step === "profile") {
      const list = this.profileRows();
      if (k === "down" && list.length) {
        this.profileAt = (this.profileAt + 1) % list.length;
      } else if (k === "up" && list.length) {
        this.profileAt = (this.profileAt + list.length - 1) % list.length;
      } else if (k === "enter" && list.length) {
        const item = list[this.profileAt];
        if (item) this.pickProfile(item, cmdOrCtrl, cx);
      } else if (k === "tab" && !mod.shift && list.length) {
        const item = list[this.profileAt];
        if (item) this.pickProfile(item, false, cx);
      } else {
        return;
      }
      cx.stop_propagation();
      cx.notify();
      return;
    }

    if (this.step === "options") {
      if (k === "enter" && cmdOrCtrl) {
        cx.stop_propagation();
        if (this.savingPreset) this.savePreset(cx);
        else this.launch(cx);
      }
    }
  }

  chip(t, id, label, active, onClick, disabled = false) {
    return h_flex()
      .id(id)
      .h(26)
      .px(9)
      .gap(5)
      .items_center()
      .rounded(7)
      .border_1()
      .border_color(active ? t.accent : t.border)
      .bg(active ? t.selected : t.raised)
      .text_size(11.8)
      .text_color(disabled ? t.dim : active ? t.foreground : t.muted)
      .when(!disabled, (el) => el.cursor_pointer().hover((s) => s.bg(t.hover).text_color(t.foreground)).on_click(onClick))
      .when(disabled, (el) => el.opacity(0.45))
      .child(label);
  }

  stepper(t) {
    const steps = [
      { id: "space", num: "1", label: this.target ? this.target.name : "Space", done: this.step !== "space" && Boolean(this.target) },
      { id: "profile", num: "2", label: this.profile ? this.profile.name : "Profile", done: this.step === "options" && Boolean(this.profile) },
      { id: "options", num: "3", label: "Launch", done: false },
    ];
    const items = [];
    steps.forEach((s, idx) => {
      const current = this.step === s.id;
      const clickable = s.id === "space" || (s.id === "profile" && Boolean(this.target)) || (s.id === "options" && Boolean(this.target && this.profile));
      if (idx > 0) items.push(div().text_size(11).text_color(t.dim).child("›"));
      items.push(
        h_flex()
          .id(`step-${s.id}`)
          .h(24)
          .px(8)
          .gap(5)
          .items_center()
          .rounded(6)
          .border_1()
          .border_color(current ? t.accent : t.border)
          .bg(current ? t.selected : s.done ? t.raised : t.popover)
          .when(clickable && !current, (el) => el.cursor_pointer().hover((el2) => el2.bg(t.hover)))
          .when(clickable, (el) => el.on_click((_e, cx) => this.goToStep(s.id, cx)))
          .child(
            h_flex()
              .size(15)
              .rounded_full()
              .items_center()
              .justify_center()
              .bg(current ? t.accent : s.done ? t.ansi2 : t.border)
              .text_size(9.5)
              .font_bold()
              .text_color(current ? t.accent_foreground : "#ffffff")
              .child(s.done ? "✓" : s.num),
          )
          .child(div().max_w(135).truncate().text_size(11.5).font_medium().text_color(current ? t.foreground : s.done ? t.muted : t.dim).child(s.label)),
      );
    });
    return h_flex().gap(6).items_center().children(items);
  }

  renderSpaceStep(t) {
    const list = this.spaceRows();
    if (this.spaceAt >= list.length) this.spaceAt = Math.max(0, list.length - 1);

    const children = [];
    let selectedChildIdx = -1;
    let addedNewHeader = false;
    let addedSpaceHeader = false;

    list.forEach((item, idx) => {
      const isNew = item.kind === "new-space";
      if (!isNew && !addedSpaceHeader) {
        addedSpaceHeader = true;
        children.push(
          h_flex()
            .px(8)
            .pt(4)
            .pb(4)
            .items_center()
            .justify_between()
            .text_size(11)
            .text_color(t.dim)
            .child(this.scopeToProject && this.projectName ? `Project: ${this.projectName}` : "Spaces across all machines")
            .when(this.scopeToProject, (el) =>
              el.child(
                div()
                  .id("show-all-spaces")
                  .px(6)
                  .py(2)
                  .rounded(5)
                  .text_color(t.accent)
                  .cursor_pointer()
                  .hover((s) => s.bg(t.hover))
                  .on_click((_e, cx) => {
                    this.scopeToProject = false;
                    this.spaceAt = 0;
                    cx.notify();
                  })
                  .child("Show all spaces"),
              ),
            ),
        );
      }
      if (isNew && !addedNewHeader) {
        addedNewHeader = true;
        children.push(
          div()
            .px(8)
            .pt(addedSpaceHeader ? 10 : 4)
            .pb(4)
            .text_size(11)
            .text_color(t.dim)
            .child("Create new space"),
        );
      }

      const on = idx === this.spaceAt;
      if (on) selectedChildIdx = children.length;

      if (isNew) {
        children.push(
          h_flex()
            .id(`sp-row-${item.id}`)
            .h(36)
            .px(10)
            .gap(10)
            .flex_none()
            .items_center()
            .rounded(8)
            .cursor_pointer()
            .when(on, (el) => el.bg(t.selected))
            .when(!on, (el) => el.hover((s) => s.bg(t.hover)))
            .on_click((_e, cx) => this.pickSpace(item, cx))
            .child(
              h_flex()
                .size(22)
                .flex_none()
                .rounded(6)
                .bg(t.raised)
                .border_1()
                .border_color(t.border)
                .items_center()
                .justify_center()
                .child(svg(I("plus")).size(12).text_color(t.muted)),
            )
            .child(div().flex_1().min_w(0).truncate().text_color(t.foreground).child(item.name))
            .child(
              h_flex()
                .gap(5)
                .items_center()
                .text_size(11)
                .text_color(t.dim)
                .child(svg(I(item.machine ? "server" : "monitor")).size(12).text_color(t.dim))
                .child(item.machine ?? "local"),
            ),
        );
        return;
      }

      const path = tilde(item.folder, !item.machine);
      const subtitle = [path, item.branch && item.branch !== "main" && item.branch !== "master" ? `⎇ ${item.branch}` : ""].filter(Boolean).join(" · ");
      const statusColor = item.status ? (t[item.status] ?? t.dim) : null;

      children.push(
        h_flex()
          .id(`sp-row-${item.id}`)
          .h(46)
          .px(10)
          .gap(10)
          .flex_none()
          .items_center()
          .rounded(8)
          .cursor_pointer()
          .when(on, (el) => el.bg(t.selected))
          .when(!on, (el) => el.hover((s) => s.bg(t.hover)))
          .on_click((_e, cx) => this.pickSpace(item, cx))
          .child(
            h_flex()
              .size(22)
              .flex_none()
              .rounded(6)
              .bg(spaceColor(t, item))
              .items_center()
              .justify_center()
              .text_size(11)
              .font_bold()
              .text_color("#ffffff")
              .child([...item.name][0]?.toUpperCase() ?? "?"),
          )
          .child(
            v_flex()
              .flex_1()
              .min_w(0)
              .overflow_hidden()
              .justify_center()
              .gap(1)
              .child(
                h_flex()
                  .w_full()
                  .min_w(0)
                  .gap(6)
                  .items_center()
                  .child(div().min_w(0).truncate().font_medium().text_color(t.foreground).child(item.name))
                  .when(item.active, (el) =>
                    el.child(div().flex_none().px(5).py(1).rounded(4).bg(t.accent + "26").text_size(10).font_medium().text_color(t.accent).child("active")),
                  ),
              )
              .when(Boolean(subtitle), (el) => el.child(div().w_full().truncate().text_size(11.3).text_color(t.dim).child(subtitle))),
          )
          .child(
            h_flex()
              .flex_none()
              .gap(7)
              .items_center()
              .when(Boolean(item.machine), (el) =>
                el.child(
                  h_flex()
                    .px(6)
                    .h(20)
                    .gap(4)
                    .items_center()
                    .rounded(5)
                    .bg(t.raised)
                    .border_1()
                    .border_color(t.border)
                    .text_size(10.5)
                    .text_color(t.muted)
                    .child(svg(I("server")).size(10).text_color(t.dim))
                    .child(item.machine),
                ),
              )
              .when(!item.machine, (el) =>
                el.child(div().text_size(10.5).text_color(t.dim).child("local")),
              )
              .child(
                div()
                  .text_size(11)
                  .text_color(t.dim)
                  .child(item.agentCount ? `${item.agentCount} ag` : `${item.tabCount} tab${item.tabCount === 1 ? "" : "s"}`),
              )
              .when(Boolean(statusColor), (el) =>
                el.child(
                  item.status === "working"
                    ? svg(I("spinner")).size(11).text_color(t.working).spin(900)
                    : div().size(7).rounded_full().bg(statusColor),
                ),
              ),
          ),
      );
    });

    if (selectedChildIdx >= 0 && selectedChildIdx !== this.revealedSpace) {
      this.revealedSpace = selectedChildIdx;
      this.spaceScroll.scroll_to_item(selectedChildIdx);
    }

    return v_flex()
      .flex_1()
      .min_h(0)
      .gap(10)
      .child(
        h_flex()
          .h(36)
          .flex_none()
          .px(10)
          .gap(8)
          .items_center()
          .rounded(9)
          .border_1()
          .border_color(this.focusedField === "space" ? t.accent : t.input)
          .bg(t.background)
          .child(svg(I("search")).size(15).text_color(t.dim))
          .child(Input.new(this.spaceInput).flex_1().h_full().text_size(12.8).text_color(t.foreground)),
      )
      .child(
        v_flex()
          .id("al-space-list")
          .flex_1()
          .min_h(0)
          .pr(10)
          .gap(2)
          .overflow_y_scrollbar()
          .mode("always")
          .track_scroll(this.spaceScroll)
          .children(children)
          .when(list.length === 0, (el) => el.child(div().p(12).text_size(12.5).text_color(t.dim).child("No matching spaces."))),
      );
  }

  renderProfileStep(t) {
    const machineKey = this.target?.machine ?? "";
    const data = this.machineData[machineKey] ?? { loading: false, agents: [], al: [], alInstalled: null, cli: null };
    const list = this.profileRows();
    if (this.profileAt >= list.length) this.profileAt = Math.max(0, list.length - 1);

    const children = [];
    let selectedChildIdx = -1;
    let lastSection = null;

    list.forEach((item, idx) => {
      if (item.section !== lastSection) {
        const isFirst = lastSection === null;
        lastSection = item.section;
        children.push(
          div()
            .px(8)
            .pt(isFirst ? 4 : 10)
            .pb(4)
            .text_size(11)
            .text_color(t.dim)
            .child(item.section),
        );
      }

      const on = idx === this.profileAt;
      if (on) selectedChildIdx = children.length;

      const metaBadges = [];
      if (item.kind === "livery") {
        if (item.authMode) metaBadges.push(item.authMode);
        if (item.sessionGroup) metaBadges.push(`grp:${item.sessionGroup}`);
        else if (item.isolationMode && item.isolationMode !== "direct") metaBadges.push(item.isolationMode);
      }
      const savedDef = lookupDefault(item);
      const previewArgv = savedDef
        ? this.buildArgvFor(item, savedDef.modeId ?? "default", savedDef.argsText ?? "")
        : item.argv;
      const defaultModeLabel = savedDef?.modeId && savedDef.modeId !== "default"
        ? this.modesFor(item).find((m) => m.id === savedDef.modeId)?.label
        : null;
      const cmdPreview = previewArgv ? previewArgv.map(shellQuote).join(" ") : "";
      const subText = item.kind === "livery"
        ? [item.description, cmdPreview].filter(Boolean).join(" · ")
        : (savedDef ? cmdPreview : (item.description || cmdPreview));

      const titleEl = item.kind === "livery"
        ? h_flex()
            .min_w(0)
            .gap(5)
            .items_center()
            .child(div().flex_none().font_semibold().text_color(t.foreground).child(item.harnessLabel))
            .child(div().flex_none().text_color(t.dim).child("·"))
            .child(div().min_w(0).truncate().font_medium().text_color(t.foreground).child(item.profileName))
        : div().min_w(0).truncate().font_medium().text_color(item.installed ? t.foreground : t.dim).child(item.name);

      children.push(
        h_flex()
          .id(`pr-row-${item.key}`)
          .h(44)
          .px(10)
          .gap(10)
          .flex_none()
          .items_center()
          .rounded(8)
          .when(item.installed, (el) => el.cursor_pointer())
          .when(!item.installed, (el) => el.opacity(0.5))
          .when(on, (el) => el.bg(t.selected))
          .when(!on && item.installed, (el) => el.hover((s) => s.bg(t.hover)))
          .on_click((e, cx) => this.pickProfile(item, Boolean(e?.modifiers?.platform), cx))
          .child(renderIcon(item.icon, item.tint, 17, item.installed ? t.foreground : t.dim))
          .child(
            v_flex()
              .flex_1()
              .min_w(0)
              .overflow_hidden()
              .justify_center()
              .gap(1)
              .child(
                h_flex()
                  .w_full()
                  .min_w(0)
                  .gap(6)
                  .items_center()
                  .child(titleEl)
                  .children(
                    metaBadges.map((b) =>
                      div().flex_none().px(5).py(1).rounded(4).bg(t.raised).border_1().border_color(t.border).text_size(10).text_color(t.muted).child(b),
                    ),
                  )
                  .when(Boolean(savedDef), (el) =>
                    el.child(
                      div()
                        .flex_none()
                        .px(5)
                        .py(1)
                        .rounded(4)
                        .bg(t.accent + "22")
                        .text_size(10)
                        .font_medium()
                        .text_color(t.accent)
                        .child(defaultModeLabel ? `default: ${defaultModeLabel}` : "custom default"),
                    ),
                  )
                  .when(item.kind === "livery" && !item.secretsOk, (el) =>
                    el.child(
                      div()
                        .flex_none()
                        .px(5)
                        .py(1)
                        .rounded(4)
                        .bg(t.blocked + "26")
                        .text_size(10)
                        .font_medium()
                        .text_color(t.blocked)
                        .tooltip(`Missing secrets: ${(item.missingSecrets ?? []).join(", ")}. Run \`liv secret set ...\``)
                        .child("missing secret"),
                    ),
                  ),
              )
              .when(Boolean(subText), (el) => el.child(div().w_full().truncate().text_size(11.3).text_color(t.dim).child(subText))),
          )
          .when(item.kind === "preset", (el) =>
            el.child(
              div()
                .id(`del-preset-${item.id}`)
                .flex_none()
                .p(4)
                .rounded(5)
                .text_color(t.dim)
                .cursor_pointer()
                .tooltip("Delete preset")
                .hover((s) => s.bg(t.hover).text_color(t.blocked))
                .on_click((_e, cx) => {
                  cx.stop_propagation();
                  this.deletePreset(item.id, cx);
                })
                .child(svg(I("x")).size(12)),
            ),
          )
          .when(!item.installed, (el) => el.child(div().flex_none().text_size(11).text_color(t.dim).child("not installed"))),
      );
    });

    if (selectedChildIdx >= 0 && selectedChildIdx !== this.revealedProfile) {
      this.revealedProfile = selectedChildIdx;
      this.profileScroll.scroll_to_item(selectedChildIdx);
    }

    const uninstalledCount = (data.agents ?? []).filter((a) => !a.installed).length;
    const targetMachineLabel = this.target?.machine ?? "this computer";
    const cliLabel = data.cli === "agent-launch" ? "agent-launch" : "livery";

    return v_flex()
      .flex_1()
      .min_h(0)
      .gap(10)
      .child(
        h_flex()
          .flex_none()
          .px(10)
          .py(7)
          .gap(8)
          .items_center()
          .rounded(8)
          .bg(t.raised)
          .border_1()
          .border_color(t.border)
          .child(
            this.target?.kind === "space"
              ? h_flex()
                  .size(18)
                  .flex_none()
                  .rounded(5)
                  .bg(spaceColor(t, this.target))
                  .items_center()
                  .justify_center()
                  .text_size(10)
                  .font_bold()
                  .text_color("#ffffff")
                  .child([...this.target.name][0]?.toUpperCase() ?? "?")
              : svg(I("plus")).size(14).text_color(t.accent),
          )
          .child(div().font_medium().text_size(12.5).text_color(t.foreground).child(this.target?.name ?? ""))
          .child(
            h_flex()
              .px(5)
              .h(18)
              .gap(4)
              .items_center()
              .rounded(4)
              .bg(t.background)
              .text_size(10.5)
              .text_color(t.muted)
              .child(svg(I(this.target?.machine ? "server" : "monitor")).size(10).text_color(t.dim))
              .child(targetMachineLabel),
          )
          .child(div().flex_1())
          .when(data.loading, (el) =>
            el.child(
              h_flex()
                .gap(5)
                .items_center()
                .text_size(11)
                .text_color(t.dim)
                .child(svg(I("spinner")).size(11).text_color(t.working).spin(900))
                .child("Checking…"),
            ),
          )
          .child(
            div()
              .id("al-change-space")
              .px(7)
              .py(2)
              .rounded(5)
              .text_size(11.5)
              .text_color(t.muted)
              .cursor_pointer()
              .hover((s) => s.bg(t.hover).text_color(t.foreground))
              .on_click((_e, cx) => this.goToStep("space", cx))
              .child("Change"),
          ),
      )
      .child(
        h_flex()
          .h(36)
          .flex_none()
          .px(10)
          .gap(8)
          .items_center()
          .rounded(9)
          .border_1()
          .border_color(this.focusedField === "profile" ? t.accent : t.input)
          .bg(t.background)
          .child(svg(I("search")).size(15).text_color(t.dim))
          .child(Input.new(this.profileInput).flex_1().h_full().text_size(12.8).text_color(t.foreground)),
      )
      .when(Boolean(data.error), (el) => el.child(div().px(6).text_size(11.5).text_color(t.blocked).child(data.error)))
      .child(
        v_flex()
          .id("al-profile-list")
          .flex_1()
          .min_h(0)
          .pr(10)
          .gap(2)
          .overflow_y_scrollbar()
          .mode("always")
          .track_scroll(this.profileScroll)
          .children(children)
          .when(list.length === 0, (el) => el.child(div().p(12).text_size(12.5).text_color(t.dim).child("No matching profiles or agents."))),
      )
      .child(
        h_flex()
          .flex_none()
          .px(2)
          .items_center()
          .justify_between()
          .text_size(11)
          .text_color(t.dim)
          .child(
            data.alInstalled === false
              ? `No liv CLI on ${targetMachineLabel} (using installed agents & presets)`
              : data.alInstalled === true && (data.al?.length ?? 0) === 0
                ? `${cliLabel} found on ${targetMachineLabel} (0 profiles yet)`
                : data.alInstalled === true
                  ? `${data.al.length} ${cliLabel} profile${data.al.length === 1 ? "" : "s"} on ${targetMachineLabel}`
                  : "",
          )
          .when(uninstalledCount > 0, (el) =>
            el.child(
              div()
                .id("toggle-uninstalled")
                .px(6)
                .py(2)
                .rounded(5)
                .cursor_pointer()
                .hover((s) => s.bg(t.hover).text_color(t.foreground))
                .on_click((_e, cx) => {
                  this.showUninstalled = !this.showUninstalled;
                  cx.notify();
                })
                .child(this.showUninstalled ? "Hide uninstalled" : `Show ${uninstalledCount} uninstalled`),
            ),
          ),
      );
  }

  renderOptionsStep(t) {
    const isNewSpace = this.target?.kind === "new-space";
    const isShell = this.profile?.kind === "shell";
    const modes = this.modesFor(this.profile);
    const finalArgv = this.buildArgv();
    const previewCmd = finalArgv ? finalArgv.map(shellQuote).join(" ") : "(default shell)";
    const folders = readFolders();
    const canSplit = !isNewSpace && this.target?.active;

    const placements = isNewSpace
      ? [
          { id: "tab", label: "Focus new space" },
          { id: "background", label: "Background" },
        ]
      : [
          { id: "tab", label: "New tab" },
          { id: "background", label: "Background tab" },
          ...(canSplit
            ? [
                { id: "split-right", label: "Split right" },
                { id: "split-down", label: "Split down" },
              ]
            : []),
          { id: "worktree", label: "New worktree…" },
        ];

    const fieldLabel = (text, hint = "") =>
      h_flex()
        .items_center()
        .justify_between()
        .child(div().text_size(11.3).font_medium().text_color(t.dim).child(text))
        .when(Boolean(hint), (el) => el.child(div().text_size(10.8).text_color(t.dim).child(hint)));

    const fieldBox = (name, state, h = 34) =>
      Input.new(state)
        .h(h)
        .px(10)
        .items_center()
        .rounded(8)
        .border_1()
        .border_color(this.focusedField === name ? t.accent : t.input)
        .bg(t.background)
        .text_size(12.6)
        .text_color(t.foreground);

    return v_flex()
      .id("al-options-scroll")
      .flex_1()
      .min_h(0)
      .pr(10)
      .gap(11)
      .overflow_y_scrollbar()
      .mode("always")
      .child(
        v_flex()
          .flex_none()
          .p(10)
          .gap(6)
          .rounded(9)
          .bg(t.raised)
          .border_1()
          .border_color(t.border)
          .child(
            h_flex()
              .w_full()
              .min_w(0)
              .gap(8)
              .items_center()
              .child(renderIcon(this.profile?.icon, this.profile?.tint, 16, t.foreground))
              .child(div().flex_none().font_semibold().text_size(13).text_color(t.foreground).child(this.profile?.name ?? ""))
              .child(div().flex_none().text_size(11.5).text_color(t.dim).child("in"))
              .child(div().flex_1().min_w(0).truncate().font_medium().text_size(12.5).text_color(t.foreground).child(this.target?.name ?? ""))
              .child(
                h_flex()
                  .flex_none()
                  .px(5)
                  .h(18)
                  .gap(4)
                  .items_center()
                  .rounded(4)
                  .bg(t.background)
                  .text_size(10.5)
                  .text_color(t.muted)
                  .child(svg(I(this.target?.machine ? "server" : "monitor")).size(10).text_color(t.dim))
                  .child(this.target?.machine ?? "local"),
              ),
          )
          .child(
            h_flex()
              .px(8)
              .py(5)
              .rounded(6)
              .bg(t.background)
              .border_1()
              .border_color(t.border)
              .items_center()
              .gap(6)
              .child(div().text_size(11).text_color(t.dim).child("$"))
              .child(div().flex_1().min_w(0).truncate().text_size(11.5).text_color(t.muted).child(previewCmd)),
          ),
      )
      .when(isNewSpace, (el) =>
        el
          .child(
            v_flex()
              .flex_none()
              .gap(5)
              .child(fieldLabel("Working folder", this.target?.machine ? `path on ${this.target.machine}` : "optional"))
              .child(
                h_flex()
                  .gap(6)
                  .items_center()
                  .child(fieldBox("folder", this.folderInput).flex_1())
                  .when(!this.target?.machine, (el2) =>
                    el2.child(
                      h_flex()
                        .id("al-browse")
                        .h(34)
                        .px(10)
                        .items_center()
                        .rounded(8)
                        .border_1()
                        .border_color(t.input)
                        .bg(t.raised)
                        .text_size(12)
                        .text_color(t.foreground)
                        .cursor_pointer()
                        .hover((s) => s.bg(t.hover))
                        .on_click((_e, cx) => this.browseFolder(cx))
                        .child("Browse…"),
                    ),
                  ),
              ),
          )
          .child(
            v_flex()
              .flex_none()
              .gap(5)
              .child(fieldLabel("Space name", "optional"))
              .child(fieldBox("spaceName", this.spaceNameInput)),
          ),
      )
      .when(!isShell && this.placement !== "worktree", (el) =>
        el.child(
          v_flex()
            .flex_none()
            .gap(5)
            .child(fieldLabel("First message / task", "optional · sent automatically"))
            .child(fieldBox("prompt", this.promptInput, 36)),
        ),
      )
      .child(
        v_flex()
          .flex_none()
          .gap(5)
          .child(fieldLabel("Tab name", "blank = auto-title from agent"))
          .child(fieldBox("name", this.nameInput)),
      )
      .when(!isShell, (el) => {
        const savedDef = lookupDefault(this.profile);
        const matchesDefault = Boolean(
          savedDef &&
          (savedDef.modeId ?? "default") === this.modeId &&
          (savedDef.argsText ?? "") === this.argsText.trim(),
        );
        return el.child(
          v_flex()
            .flex_none()
            .gap(6)
            .child(
              h_flex()
                .items_center()
                .justify_between()
                .child(div().text_size(11.3).font_medium().text_color(t.dim).child("Launch flags & extra args"))
                .child(
                  h_flex()
                    .gap(6)
                    .items_center()
                    .when(Boolean(this.presetFeedback), (el2) => el2.child(div().text_size(11).text_color(t.ansi2).child(this.presetFeedback)))
                    .when(matchesDefault, (el2) =>
                      el2
                        .child(div().px(5).py(2).rounded(5).bg(t.accent + "20").text_size(10.8).font_medium().text_color(t.accent).child("Default ✓"))
                        .child(
                          div()
                            .id("al-reset-default")
                            .px(5)
                            .py(2)
                            .rounded(5)
                            .text_size(11)
                            .text_color(t.dim)
                            .cursor_pointer()
                            .hover((s) => s.bg(t.hover).text_color(t.foreground))
                            .on_click((_e, cx) => this.clearDefault(cx))
                            .child("Reset"),
                        ),
                    )
                    .when(!matchesDefault, (el2) =>
                      el2.child(
                        div()
                          .id("al-save-default")
                          .px(6)
                          .py(2)
                          .rounded(5)
                          .text_size(11)
                          .text_color(t.muted)
                          .cursor_pointer()
                          .hover((s) => s.bg(t.hover).text_color(t.foreground))
                          .tooltip(`Save these flags as the default for ${this.profile?.name ?? "this profile"} (also used by Quick launch)`)
                          .on_click((_e, cx) => this.saveAsDefault(cx))
                          .child("Save as default"),
                      ),
                    )
                    .child(div().text_size(10.5).text_color(t.border).child("·"))
                    .child(
                      div()
                        .id("al-save-preset-toggle")
                        .px(6)
                        .py(2)
                        .rounded(5)
                        .text_size(11)
                        .text_color(t.accent)
                        .cursor_pointer()
                        .hover((s) => s.bg(t.hover))
                        .on_click((_e, cx) => {
                          this.savingPreset = !this.savingPreset;
                          if (this.savingPreset) {
                            const modeLabel = modes.find((m) => m.id === this.modeId && m.id !== "default")?.label;
                            const suggested = [this.profile?.name, modeLabel].filter(Boolean).join(" · ");
                            this.presetNameText = suggested;
                            this.presetNameInput.set_value(suggested);
                            this.presetNameInput.set_selected_range({ start: 0, end: suggested.length });
                          }
                          this.focusStep(cx);
                          cx.notify();
                        })
                        .child(this.savingPreset ? "Cancel save" : "+ Save as preset"),
                    ),
                ),
            )
            .when(modes.length > 0, (el2) =>
              el2.child(
                h_flex()
                  .flex_wrap()
                  .gap(6)
                  .children(
                    modes.map((m) =>
                      this.chip(t, `mode-${m.id}`, m.label, this.modeId === m.id, (_e, cx) => {
                        this.modeId = m.id;
                        this.presetFeedback = "";
                        cx.notify();
                      }),
                    ),
                  ),
              ),
            )
            .child(fieldBox("args", this.argsInput))
            .when(this.savingPreset, (el2) =>
              el2.child(
                h_flex()
                  .gap(6)
                  .items_center()
                  .child(fieldBox("presetName", this.presetNameInput, 32).flex_1())
                  .child(
                    h_flex()
                      .id("al-save-preset-confirm")
                      .h(32)
                      .px(12)
                      .items_center()
                      .rounded(8)
                      .bg(t.solid)
                      .text_color(t.solid_foreground)
                      .text_size(12)
                      .font_medium()
                      .cursor_pointer()
                      .hover((s) => s.opacity(0.88))
                      .on_click((_e, cx) => this.savePreset(cx))
                      .child("Save"),
                  ),
              ),
            ),
        );
      })
      .child(
        v_flex()
          .flex_none()
          .gap(6)
          .child(fieldLabel("Open in"))
          .child(
            h_flex()
              .flex_wrap()
              .gap(6)
              .children(
                placements.map((pl) =>
                  this.chip(t, `place-${pl.id}`, pl.label, this.placement === pl.id, (_e, cx) => {
                    this.placement = pl.id;
                    cx.notify();
                  }),
                ),
              ),
          ),
      )
      .when(!isShell && folders.length > 0, (el) =>
        el.child(
          v_flex()
            .flex_none()
            .gap(6)
            .child(fieldLabel("File in agent folder", "optional"))
            .child(
              h_flex()
                .flex_wrap()
                .gap(6)
                .child(
                  this.chip(t, "af-none", "None", !this.folderId, (_e, cx) => {
                    this.folderId = null;
                    cx.notify();
                  }),
                )
                .children(
                  folders.map((fo) =>
                    this.chip(t, `af-${fo.id}`, fo.name, this.folderId === fo.id, (_e, cx) => {
                      this.folderId = fo.id;
                      cx.notify();
                    }),
                  ),
                ),
            ),
        ),
      );
  }

  render() {
    const t = theme();
    const mac = window()?.platform === "macos";
    const modKey = mac ? "⌘" : "Ctrl+";

    const footerHint =
      this.step === "space"
        ? "↑↓ choose · ↵ next · Esc close"
        : this.step === "profile"
          ? `↑↓ choose · ↵ options · ${modKey}↵ quick launch · ⇧Tab back`
          : `↵ launch · ⇧Tab back · Esc close`;

    const primaryLabel =
      this.step === "space"
        ? "Next →"
        : this.step === "profile"
          ? "Configure →"
          : this.placement === "worktree"
            ? "Open worktree dialog"
            : "Launch";

    const primaryAction = (_e, cx) => {
      if (this.step === "space") this.selectCurrentSpace(cx);
      else if (this.step === "profile") {
        const item = this.profileRows()[this.profileAt];
        if (item) this.pickProfile(item, false, cx);
      } else {
        this.launch(cx);
      }
    };

    const button = (id, text, primary, action) =>
      h_flex()
        .id(id)
        .h(30)
        .px(13)
        .items_center()
        .rounded(8)
        .border_1()
        .border_color(primary ? t.solid : t.input)
        .bg(primary ? t.solid : t.raised)
        .text_color(primary ? t.solid_foreground : t.foreground)
        .text_size(12.2)
        .font_medium()
        .cursor_pointer()
        .hover((s) => s.opacity(0.88))
        .on_click(action)
        .child(text);

    return v_flex()
      .id("livery-launcher")
      .track_focus(this.handle)
      .on_key_down((e, cx) => this.onKeyDown(e, cx))
      .size_full()
      .min_h(0)
      .p(16)
      .gap(12)
      .bg(t.popover)
      .text_color(t.foreground)
      .text_size(13)
      .child(
        v_flex()
          .flex_none()
          .gap(10)
          .child(
            h_flex()
              .items_center()
              .gap(8)
              .child(svg(I("terminal")).size(16).text_color(t.accent))
              .child(div().flex_1().text_size(15).font_semibold().child("New Agent"))
              .child(
                div()
                  .id("al-esc")
                  .px(6)
                  .py(2)
                  .rounded(5)
                  .text_size(11)
                  .text_color(t.dim)
                  .cursor_pointer()
                  .hover((s) => s.bg(t.hover).text_color(t.foreground))
                  .on_click(() => this.close())
                  .child("Esc"),
              ),
          )
          .child(this.stepper(t)),
      )
      .child(
        this.step === "space"
          ? this.renderSpaceStep(t)
          : this.step === "profile"
            ? this.renderProfileStep(t)
            : this.renderOptionsStep(t),
      )
      .child(
        h_flex()
          .flex_none()
          .pt(8)
          .border_t(1)
          .border_color(t.border)
          .items_center()
          .gap(8)
          .when(this.step !== "space", (el) =>
            el.child(button("al-back", "← Back", false, (_e, cx) => this.goBack(cx))),
          )
          .child(div().flex_1().min_w(0).truncate().text_size(11).text_color(t.dim).child(footerHint))
          .when(this.step === "profile" && this.target?.kind === "space", (el) =>
            el.child(
              button("al-quick", "Quick launch", false, (_e, cx) => {
                const item = this.profileRows()[this.profileAt];
                if (item) this.pickProfile(item, true, cx);
              }),
            ),
          )
          .child(button("al-primary", primaryLabel, true, primaryAction)),
      );
  }
}
