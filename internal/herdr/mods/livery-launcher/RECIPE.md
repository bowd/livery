# Livery Launcher (`livery-launcher`)

Stepped, keyboard-first **New Agent** dialog wizard (`Space → Profile → Launch`) managed by **Livery (`liv`)**.

## Features

- **Step 1 — Space:** Pick any local or remote Herdr space (active and recently used first) or create a new space on any connected machine.
- **Step 2 — Profile:** Flat, searchable list of all `liv <harness> <profile>` profiles discovered live via `liv list --json` (falling back to legacy `agent-launch` / `al` on hosts not yet upgraded), formatted consistently as `Claude · gateway`, `Claude · personal`, `Codex · zai`, plus saved presets, installed `knownAgents`, and plain terminal shell.
- **Step 3 — Launch:** Optional initial prompt (`task`), custom tab name, harness-aware launch mode chips (`Skip perms`, `Plan`, `Full auto`, `YOLO`, `Continue`, `Resume`), extra flags, per-profile defaults (`Save as default` — also used by `Cmd+Enter` Quick launch), custom presets, placement (`New tab`, `Background`, `Split`, `New worktree…`), and `agent-folders` folder filing.

## Installation

Managed directly by `liv`:

```bash
liv herdr install-mod
liv herdr check-mod
```

Installs into `~/.config/herdr-app/mods/livery-launcher`, updates `cmd-shift-n` in `~/.config/herdr-app/keys.json` to `mod.open:livery-launcher`, and keeps an `agent-launcher` compatibility alias so `agent-folders` continues working seamlessly.
