# Meta-UI & Nested Skillshare Guide

Run `liv ui` to launch Livery's local web control plane on `http://127.0.0.1:19400`:

```bash
liv ui
```

All mutating HTTP endpoints are bound to `127.0.0.1` by default and protected with per-boot CSRF tokens. If you rebuild or upgrade `~/.local/bin/liv` while `liv ui` is running, the server detects the binary replacement and seamlessly `execve`-reloads in place.

---

## Navigation Architecture

Livery wraps stock upstream [`runkids/skillshare`](https://github.com/runkids/skillshare) as an embedded engine rather than forking it, adding fleet orchestration on top:

### 1. Environments (`/environments`)
- Live overview of every local and SSH host in `inventory.toml`, showing skill/prompt/MCP/hook/profile/project counts, sync badges (`synced ✓` or `Sync ↑pull ↓push`), and Herdr mod status.
- **+ Add host**: Scans Herdr connected machines, Tailscale peers, and `~/.ssh/config`, runs a live POSIX SSH probe (`OS/arch`, NixOS detection, installed harnesses), and adds the host in one click.
- Fleet operations bar: **Probe hosts**, **Plan all**, **Audit skills** (`skillshare audit`), **Doctor**, and **Secrets parity**.

### 2. Catalog (`/catalog/*`)
Divided cleanly into items managed **inside Skillshare** vs **outside Skillshare**:
- **Skillshare Library (`/catalog/skillshare`)**: Runs an isolated, sandboxed `skillshare ui` instance backed by `~/.local/share/livery/catalog/skillshare/config.yaml` (`targets: {}`). Browse, install from GitHub, audit, and edit skills, agents, extras, MCP servers, hooks, and plugins in your central library without touching your live `~/.claude` or `~/.codex` directories.
- **Prompts (`/catalog/prompts`)**: Create, edit, search, and **globally rank (`#1`..`#N`)** `+++` prompt fragments, with live per-fragment word counts and provider badges.
- **Launchers (`/catalog/launchers`)**: Create and edit `liv <harness> <profile>` launch profiles, Shadow Homes, session groups, and `store:` secrets.
- **Profiles (`/catalog/profiles`)**: Manage composable profile bundles (`base`, `workstation`, `server-ops`).
- **Local Apps (`/catalog/apps/*`)**: Inspect and edit **T3 Code**, **Herdr**, and **OpenClaw** integrations.

### 3. Composable Matrix (`/matrix/*`)
- Interactive `Items × Environments` grid across **Profiles**, **Skills**, **MCP**, **Hooks**, **Extras**, **Agents**, **Plugins**, **Prompts**, and **Launchers**.
- Distinguishes **inherited profile inclusions** (blue checkmarks — clicking opens a guardrail modal to edit or detach the parent profile) from **direct host overrides** (solid checkmarks).
- Click any profile name to enter **Profile Matrix mode** (`/matrix/profile/<id>`) and toggle `extends`, host assignments, and included items across all categories in one view.

### 4. Per-Environment Overview & Live SSH-Tunneled Skillshare (`/env/<id>/*`)
Select any host from the topbar switcher or Environments list:
- **Overview (`/env/<id>/overview`)**:
  - Toggle assigned **Profiles**, **Harnesses** (`claude`, `codex`, `opencode`, `antigravity-cli`, `cursor`, `pi`), sync mode (`merge` / `symlink` / `copy`), and Herdr/T3 integrations.
  - Inspect compiled `CLAUDE.md` / `AGENTS.md` prompts and generated `skillshare/config.yaml`.
  - **Native & External Scanner**: Automatically scans the host (locally or over SSH) for unmanaged skills, MCP servers, hooks, and plugins sitting in `~/.claude` or `~/.codex`. Import them into the shared Catalog, keep them as host-local overrides, or mark them `external` in `inventory.toml`.
- **Nested Skillshare (`/env/<id>/skillshare`)**:
  - **Local host**: Mounts the host's live Skillshare repo Read/Write.
  - **Remote SSH host**: Opens an ephemeral SSH loopback tunnel (`-L 127.0.0.1:<local>:127.0.0.1:<remote>`) with a stdin-EOF watchdog (zero orphan processes when closed!) and reverse-proxies the remote machine's own live `skillshare ui` seamlessly inside Livery!
  - **Offline fallback**: If a remote host is offline or unreachable, Livery automatically builds a temporary compiled preview and displays a clear **Read-Only · No live access** banner.
  - **Live SSE Mutation Detection**: Whenever you install, edit, or toggle anything inside a nested Skillshare view, Livery's proxy detects the mutation, diffs against `livery-base` in the background, and lights up **`Sync · N`** in the topbar via Server-Sent Events!

### 5. Built-In Control Plane Git Modal
Click the **`clean` / `N changed`** pill in the topbar at any time to open Livery's built-in Git workflow:
- Color-coded unified diff with line numbers and `+`/`−` stats across modified and newly added files.
- One-click per-file **Revert**, two-step **Discard all**, or inline **Commit** with recent commit history.
