# CLI Reference

Livery ships as a single static binary: `liv`.

---

## Direct Harness Launching

```bash
liv <harness> <profile> [args...]
liv <harness>
liv run <id-or-harness/profile> [--] [args...]
```

- Supported harnesses: `claude`, `codex`, `opencode`, `openclaw`, `agy`.
- **Zero flag interception after `<profile>`**: every trailing flag and positional argument is passed verbatim to the underlying harness via `execve`.
- Calling `liv <harness>` with no profile lists all configured profiles for that harness along with auth mode, home mode, and secret readiness.

Examples:

```bash
liv claude                                            # List Claude profiles
liv claude gateway --dangerously-skip-permissions     # Launch gateway profile
liv claude personal --resume                          # Launch personal Max profile
liv codex zai --full-auto                             # Launch Codex custom provider
liv dry-run claude gateway --permission-mode plan     # Inspect resolved argv, env & home JSON
liv show claude gateway                               # Show profile metadata & secret status
liv login claude personal                             # Run interactive OAuth login inside profile home
```

---

## Onboarding, Profiles & Session Groups

| Command | Description |
|---|---|
| `liv onboard [--apply] [--json]` | Run guided setup & health checklist (harnesses, `skillshare`, secrets, Herdr mod). `--apply` interactively fixes pending items. |
| `liv status` | Print local `liv` version, secrets backend, loaded profile count, `skillshare` version, and Herdr mod SHA-256 status. |
| `liv list [--json] [--harness <h>]` | List all launcher profiles across harnesses. |
| `liv profile blueprints` | List built-in profile blueprints (Claude Gateway, Claude OAuth, Codex Custom Provider, OpenCode, OpenClaw, Antigravity). |
| `liv profile new [harness name] [flags]` | Interactive blueprint wizard (or flag-driven creation) writing `launchers/<id>.toml`. |
| `liv profile edit <id> [flags]` | Update an existing launcher profile. |
| `liv profile rm <id>` | Delete a launcher profile from `launchers/`. |
| `liv group list [--json]` | Inspect shared memory/session groups and disk usage across Shadow Homes. |
| `liv group set <harness> <profile> <group\|none>` | Assign or clear `session_group` on a profile (e.g. `work`, `personal`). |

---

## Fleet Control Plane

All control-plane commands resolve the repository via `--repo <dir>`, `$LIVERY_HOME`, `$LIVERY_REPO`, upward walk from `cwd`, `~/.local/share/livery`, or `~/.config/livery/repo`.

| Command | Description |
|---|---|
| `liv ui [--host 127.0.0.1] [--port 19400] [--no-open]` | Start the Livery Meta-UI server (auto-reloads seamlessly when `~/.local/bin/liv` is updated). |
| `liv check [--repo <dir>]` | Validate `inventory.toml`, profiles, prompt fragments, MCP/hook specs, and launcher profiles. |
| `liv plan [--target <id>]` | Compute and display pending `+` / `~` / `-` / `!` changes and `skillshare` diff per environment without writing. |
| `liv sync [--target <id>] [--allow-dirty]` | Compile & push Catalog, Matrix, Prompt Fragments, and Launchers to targets, run `skillshare sync`, update `provenance/lock.toml`, and advance each host's `livery-base` Git tag. |
| `liv drift [--target <id>] [--json]` | Inspect edits made directly on environments since their last `livery-base` snapshot. |
| `liv pull [--target <id>] [--dry-run] [--delete]` | Pull host-side Skillshare config and skill edits back into the control plane. |
| `liv audit [--target <id>] [--threshold <sev>] [--json]` | Run stock `skillshare audit` security checks across Catalog or target skills. |
| `liv doctor [--target <id>]` | End-to-end health check: repo validation, config drift, `skillshare` sync state, Herdr mod checksum, and cross-host secret parity. |
| `liv matrix` | Regenerate `provenance/matrix.md` coverage table. |
| `liv migrate [--from <dir>] [--to <dir>]` | Migrate a legacy agent-config repo into the `~/.local/share/livery` XDG layout. |

---

## Environment Discovery & Bootstrap

| Command | Description |
|---|---|
| `liv env discover [--json]` | Discover candidate hosts from the local machine, Herdr (`hc machine ls`), Tailscale (`tailscale status`), and `~/.ssh/config`. |
| `liv env probe <ssh-or-local> [--json]` | Run a zero-prereq POSIX SSH probe reporting OS/arch, NixOS detection, installed harnesses, `liv`/`skillshare` versions, and existing skills. |
| `liv env add <ssh-or-local> [--id <id>] [--profiles ...] [--targets ...]` | Probe and add an environment to `inventory.toml`. |
| `liv env bootstrap <target-id\|--all> [--skillshare]` | Cross-compile `CGO_ENABLED=0` `liv` for the remote OS/arch, stream atomically to `~/.local/bin/liv` over SSH, and optionally install stock `skillshare`. |
| `liv env remove <target-id>` | Remove an environment from `inventory.toml` without deleting host files. |

---

## Secrets & Companion Apps

| Command | Description |
|---|---|
| `liv secret set [--stdin] <name>` | Store `store:<name>` in macOS Keychain (on Darwin) or `0600` `~/.config/livery/secrets.json` (on Linux) without terminal echo. |
| `liv secret check <name-or-ref>` | Verify whether a secret reference resolves locally. |
| `liv secret list` | List managed `store:` secret names and backend status (never prints values). |
| `liv secret rm <name>` | Delete a managed secret from the local store. |
| `liv secret import-t3 [--dry-run]` | Import API keys configured in local T3 Code settings into `store:` secrets. |
| `liv secrets status [--target <id>]` | Verify cross-host secret existence AND value parity using salted `HMAC-SHA256` digests (raw secrets never cross the wire during status checks). |
| `liv secrets sync [--target <id>]` | Push required `store:` secrets from local Keychain/store to remote hosts over SSH stdin (`0600`). |
| `liv herdr <status\|install-mod\|check-mod\|checksum\|export-mod\|uninstall-mod>` | Manage the embedded `livery-launcher` Herdr mod (`~/.config/herdr-app/mods/livery-launcher` + `Cmd+Shift+N` keybinding). |
| `liv apps status [--json]` | Inspect local Herdr, T3 Code, and OpenClaw installations. |
| `liv apps herdr <list\|sync\|set\|rm>` | Sync or edit Herdr launcher programs & templates. |
| `liv apps t3 <list\|edit\|rm>` | Inspect or update T3 Code custom provider instances while preserving sensitive env keys. |
| `liv apps openclaw <list\|create-profile\|set-plugin\|set-mcp\|rm-mcp>` | Manage OpenClaw profiles, plugins, and MCP servers. |
