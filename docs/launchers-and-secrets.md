# Multi-Profile Launchers, Secrets & Herdr

AI coding tools typically assume one account, one home directory (`~/.claude`, `~/.codex`), and one set of credentials per OS user. Livery's launcher subsystem lets you run **multiple named profiles per harness** (`gateway`, `personal`, `zai`, `work`, `studio`) with zero wrapper-script clutter, shared skills/prompts, isolated credentials, and native **Herdr** integration.

---

## Direct `execve` Launching

```bash
liv claude gateway --permission-mode plan
liv claude personal --continue
liv codex zai --full-auto
liv agy studio
```

When the first argument to `liv` is a registered harness (`claude`, `codex`, `opencode`, `openclaw`, `agy`), Livery resolves `launchers/<harness>-<profile>.toml`, materializes its home directory and secret helpers, scrubs conflicting caller environment variables (like stray `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` in your shell), and replaces its own process via `syscall.Exec`.

---

## Home Isolation Modes & Session Groups

Each launcher profile chooses one of three `mode` values:

| Mode | How it works | When to use |
|---|---|---|
| `direct` | Runs directly against your default shared harness home (`~/.claude`, `~/.codex`, etc.). | Single-account tools or default native profile. |
| `shadow` | Creates a lightweight profile home under `~/.local/share/agent-config/homes/<harness>/<id>` that **symlinks** shared `skills/`, `CLAUDE.md` / `AGENTS.md`, hooks, MCP servers, and optional session/memory directories while keeping OAuth tokens, `settings.json` `apiKeyHelper`, and provider configs **strictly isolated**. | Running **Claude Enterprise Gateway + Personal Max** or **OpenAI OAuth + Custom Coding Provider** side-by-side without duplicating skills or losing shared memory! |
| `isolated` | Clean standalone home directory with zero symlinks into `~/.claude` or `~/.codex`. | Untrusted experiments, clean-room evals, or strict tenant separation. |

### Shared Memory & Session Groups (`session_group`)

By default, two `shadow` profiles keep separate conversation histories, or you can pool transcripts, project memories, and history across selected profiles using `session_group`:

```bash
liv group list
liv group set claude gateway work
liv group set claude personal personal
```

Profiles in the `work` group share `~/.local/share/agent-config/homes/groups/work/...` session/memory state while keeping separate auth tokens and model defaults.

---

## Zero-Leak Secrets Architecture

Secrets are **never** stored in `inventory.toml`, `launchers/*.toml`, or Git. Launcher profiles reference credentials symbolically:

| Scheme | Example | Resolution |
|---|---|---|
| `store:<name>` | `store:gateway-api-key` | Managed by Livery: **macOS Keychain** (`security` generic password) on macOS, or `0600` `~/.config/livery/secrets.json` on headless Linux. |
| `keychain:<service>` | `keychain:anthropic-prod` | Direct macOS Keychain lookup. |
| `file:<path>` | `file:~/.secrets/zai.key` | Trimmed contents of a local file (`0600` recommended). |
| `env:<VAR>` | `env:CI_LLM_TOKEN` | Caller environment variable. |

### `apiKeyHelper` & Fleet Secret Parity

- For Claude Gateway/API profiles, Livery wires Claude's `apiKeyHelper` to `liv secret-helper store:<name>` inside the Shadow Home's `settings.json`, so credentials are fetched on demand rather than baked into plaintext config files.
- **`liv secrets status`** generates a random one-time nonce per check and computes `HMAC-SHA256(nonce, secret_value)` locally and on each SSH target via `liv _remote`. It verifies not only that `store:gateway-api-key` exists on `ops@linux-node`, but that its **exact byte value matches** your workstation — without transmitting the secret during checks!
- **`liv secrets sync`** streams required `store:` secrets from your workstation store directly into remote `liv _remote` stdin over SSH (`BatchMode=yes`), writing `0600` files atomically with zero command-line argv exposure.

---

## Herdr Integration (`livery-launcher`)

Livery embeds a first-party **Herdr** mod (`livery-launcher`) for launching any local or remote agent profile in 2 keystrokes (`Cmd+Shift+N`):

```bash
liv herdr install-mod
liv apps herdr sync
```

What `liv herdr install-mod` sets up:
1. Installs `~/.config/herdr-app/mods/livery-launcher` (verified by SHA-256 bundle manifest in `liv status` and `liv doctor`).
2. Binds `cmd-shift-n` → `mod.open:livery-launcher` in `~/.config/herdr-app/keys.json`.
3. Maintains an `agent-launcher` compatibility symlink so companion mods like `agent-folders` continue working seamlessly.

### The 3-Step Wizard (`Space → Profile → Launch`)
1. **Space**: Pick any active or recent local/remote Herdr space — or create a new space on any connected machine.
2. **Profile**: Live-queries `liv list --json` on the target machine to present all `liv <harness> <profile>` profiles (`Claude · gateway`, `Claude · personal`, `Codex · zai`, saved presets, `knownAgents`, and shell). Press **`Cmd+Enter`** to quick-launch immediately with saved defaults.
3. **Launch**: Set an initial task prompt, custom tab title, harness-aware mode chips (`Skip perms`, `Plan`, `Full auto`, `YOLO`, `Continue`, `Resume`), placement (`New tab`, `Background`, `Split`, `New worktree…`), and `agent-folders` filing.

Livery also manages **T3 Code** custom provider instances (`liv apps t3`, `liv secret import-t3`) and **OpenClaw** profiles (`liv apps openclaw`) from the CLI and **Catalog → Local Apps** tab.
