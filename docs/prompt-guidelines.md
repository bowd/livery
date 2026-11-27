# Prompt & Skill Authoring Guidelines

Practical rules for authoring user-level prompt fragments (`prompts/fragments/`), skills (`catalog/skillshare/skills/`), role definitions, and per-project `AGENTS.md` / `CLAUDE.md` files across Claude Code and Codex CLI.

---

## A. Always-Loaded Instruction Files (`CLAUDE.md`, `AGENTS.md`)

1. **Include only what the agent cannot infer from the repo** or would otherwise rediscover every session. Every always-loaded line competes for attention on every turn.
2. **Keep user-level rendered files lean (under ~60 lines)** and project files under 200 lines. Livery shows live word counts on every fragment in **Catalog → Prompts** so budget creep stays visible.
3. **Keep a repo's `AGENTS.md` chain under 32 KiB combined** from repo root to working directory (`project_doc_max_bytes` limit in Codex).
4. **Make every rule concrete enough to verify**: name the exact command, path, or convention (`Run make check before committing` rather than `Test your changes`).
5. **Write plain imperative sentences** without `IMPORTANT`, `MUST`, or ALL-CAPS shouting. Modern frontier models follow plain instructions reliably and over-index on emphatic phrasing.
6. **Include a brief "why" on the same line** when non-obvious so the model generalizes accurately to edge cases.
7. **Prefer positive instructions** (what to do) over negative prohibitions, reserving `never` for true security or data-loss invariants.
8. **Enforce zero-exception invariants with hooks or sandbox rules**, not prose. Instruction files are context, not access control.
9. **State each rule once, in one fragment.** Duplicate or conflicting instructions across global and project files cause arbitrary tie-breaking or unnecessary clarification pauses.
10. **Move multi-step workflows into Skills**, and directory-specific rules into nested `AGENTS.md` or path-scoped `.claude/rules/`.
11. **In project repos, keep `AGENTS.md` canonical and set `CLAUDE.md` to `@AGENTS.md`** (with any Claude-specific notes below the import). Avoid `@path` imports inside `AGENTS.md`, which Codex does not expand.
12. **Verify what actually loaded** after structural changes: `/context` or `/memory` in Claude Code, and `codex debug prompt-input` in Codex CLI.

---

## B. Skills (`SKILL.md`)

1. **One job per skill.** Narrow, composable skills trigger accurately without flooding context.
2. **Front-load the `description`** with what the skill does and the exact user phrases/scenarios when it should trigger (1–2 sentences). Both Claude Code and Codex budget only 1–2% of context for skill listings and truncate long descriptions from the end.
3. **Gate side-effectful workflows (deploy, release, external messaging) to manual invocation** in both harnesses:
   - `SKILL.md` frontmatter: `disable-model-invocation: true` (Claude Code)
   - `agents/openai.yaml`: `policy.allow_implicit_invocation: false` (Codex CLI)
4. **Keep `SKILL.md` under ~500 lines** with core steps at the top; move detailed schemas and examples into `references/` and executable helpers into `scripts/`.
5. **Symlink skill directories, never bare `SKILL.md` files**, and avoid nested fixture `SKILL.md` files inside subdirectories (which Codex indexes as separate skills).
6. **Edit skills in Livery's Catalog (or pull host edits via Backward Sync)** rather than leaving ad-hoc edits stranded on a single machine.

---

## C. Claude Code vs Codex CLI Quick Matrix

| Capability | Claude Code | Codex CLI |
|---|---|---|
| Global user prompt | `~/.claude/CLAUDE.md`, `~/.claude/rules/*.md` | `~/.codex/AGENTS.md` (`AGENTS.override.md` wins if non-empty) |
| Project prompt | `CLAUDE.md`, `.claude/CLAUDE.md`, `CLAUDE.local.md` (falls back to `AGENTS.md` if none exist) | `AGENTS.override.md`, else `AGENTS.md` from `.git` root down to `cwd` |
| `@path` imports | Expanded (up to 4 hops) | Not expanded |
| HTML comments | Stripped before model injection | Sent to the model |
| User skills | `~/.claude/skills` | `~/.codex/skills` and `~/.agents/skills` |
| Manual-only skill | `disable-model-invocation: true` | `agents/openai.yaml` → `allow_implicit_invocation: false` |
| Inspect loaded context | `/context`, `/memory` | `codex debug prompt-input` |
