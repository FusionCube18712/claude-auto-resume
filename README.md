<div align="center">

# claude-auto-resume

**Never lose a task to a usage limit again.**

Wrap your AI coding CLI, walk away, and it picks up exactly where it left off the moment the limit resets.

[![CI](https://github.com/FusionCube18712/claude-auto-resume/actions/workflows/ci.yml/badge.svg)](https://github.com/FusionCube18712/claude-auto-resume/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/FusionCube18712/claude-auto-resume)](https://goreportcard.com/report/github.com/FusionCube18712/claude-auto-resume)
[![Release](https://img.shields.io/github/v/release/FusionCube18712/claude-auto-resume?sort=semver)](https://github.com/FusionCube18712/claude-auto-resume/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

</div>

---

You're three files deep into a refactor when your AI assistant stops cold:

```
✗ 5-hour limit reached ∙ resets 2am
```

So you set an alarm, come back at 2am, and type `continue`. **`claude-auto-resume` is that alarm — and it types `continue` for you.**

It runs your CLI *through* a tiny supervisor that watches for the limit message, reads when the limit resets, waits, and resumes the session automatically. Works with **Claude Code**, **Codex**, **Gemini**, and **any other CLI** via a configurable profile.

```console
$ claude-auto-resume claude
… you use Claude Code exactly as normal …

⏸  Usage limit reached — resuming at Tue 2:00 AM PST
⏳  Resuming in 03:41:12  ·  at 2:00 AM  ·  Ctrl-C to cancel
▶  Resuming (wrap)…
… and your task continues, untouched …
```

## Why this one

There are a handful of "wait for the reset" scripts out there. They tend to break in the same two places, and only work with one tool. This one is built to not:

- **🎯 Correct reset times.** Reset messages give a wall-clock time *in some timezone* ("resets 2pm (UTC)", "resets 11:30am (Asia/Calcutta)"). The reset math is done with the real IANA timezone database — **DST and all** — instead of the local-offset guesswork that puts other tools up to 12 hours off. Relative ("try again in 45s") and epoch formats are handled too.
- **🛟 Self-correcting.** If a parse is wrong or the vendor changes the wording, the supervisor just re-detects the limit and waits again — it never gets *stuck*. A `--check-parse` command lets you see exactly what it would do with any message.
- **🧩 Any CLI, not just Claude.** Built-in profiles for Claude Code, Codex, and Gemini; a `generic` profile (`--limit-regex`, `--resume`) for everything else.
- **🖥 One static binary.** No tmux, no `/dev/pts` tricks, no Python/Node runtime. macOS, Linux, and Windows. It passes your terminal straight through, so your TUI looks and behaves *identically*.
- **🔒 Sees nothing.** It only pattern-matches the output stream for the limit message. Your code and prompts are never read, stored, or sent anywhere.

## How it works

```
            ┌──────────────────────── claude-auto-resume ────────────────────────┐
            │                                                                     │
 your   ───▶│  PTY passthrough ─▶  your CLI (Claude/Codex/…)  ─▶ your terminal    │
 keys       │        ▲                      │                                     │
            │        │                      ▼                                     │
            │        │              watch output stream                          │
            │        │                      │                                     │
            │        │            limit message detected?                        │
            │        │                      │ yes                                 │
            │        │                      ▼                                     │
            │        │     parse reset time ─▶ wait (live countdown)              │
            │        │                      │                                     │
            │        └──── type "continue" ◀┘  (or re-run, if headless)           │
            └─────────────────────────────────────────────────────────────────────┘
```

Two resume modes, chosen automatically:

- **wrap** — for interactive TUIs (Claude Code): the session stays open; on reset, `continue` is typed into it.
- **exec** — for headless commands (`claude -p`, `codex exec`): the command is re-run with the tool's resume flag once the limit lifts.

## Install

**Homebrew** (macOS / Linux)
```bash
brew install FusionCube18712/tap/claude-auto-resume
```

**Scoop** (Windows)
```powershell
scoop bucket add FusionCube18712 https://github.com/FusionCube18712/scoop-bucket
scoop install claude-auto-resume
```

**Go**
```bash
go install github.com/FusionCube18712/claude-auto-resume/cmd/claude-auto-resume@latest
```

**Binaries** — grab one from [Releases](https://github.com/FusionCube18712/claude-auto-resume/releases) and put it on your `PATH`.

The command is `claude-auto-resume`; most people alias it:
```bash
alias car=claude-auto-resume
```

## Quick start

```bash
# Wrap an interactive Claude Code session — just prefix your normal command:
claude-auto-resume claude

# Headless / scripted runs:
claude-auto-resume --tool codex -- codex exec "add tests for the parser"

# Any other CLI: describe the limit message and how to resume:
claude-auto-resume --tool generic \
  --limit-regex 'quota exhausted' --resume 'continue' -- my-cli run

# Not sure it'll read a message right? Ask it — no waiting, no process:
claude-auto-resume --check-parse "5-hour limit reached ∙ resets 2am"
```

> Flags go **before** the wrapped command. Everything after the command (or a literal `--`) is passed through untouched.

## Supported tools

| Profile | Detects | Resumes by |
|---|---|---|
| `claude` *(default)* | `5-hour limit reached`, `weekly limit reached`, `you've hit your limit`, `out of extra usage`, `rate_limit_error` | typing `continue` (TUI) or `claude --continue` (headless) |
| `codex` | `rate limit reached`, `rate_limit_exceeded`, `try again in …s` | re-running the command¹ |
| `gemini` | `resource_exhausted`, `rate limit`, `quota exceeded`, `429` | re-running the command¹ |
| `generic` | your `--limit-regex` | `--resume` text / `--resume-cmd` |

The profile is inferred from the command name (`codex …` → `codex`); override with `--tool`. Extend any profile with extra `--limit-regex` patterns — no rebuild needed.

¹ Codex and Gemini have no native session-resume yet, so the command is re-run. Track their resume support and switch with `--resume-cmd` when it lands.

## Flags

| Flag | Default | What it does |
|---|---|---|
| `-tool` | inferred | `claude` \| `codex` \| `gemini` \| `generic` |
| `-mode` | `auto` | `auto` (inject into a live TUI, else re-spawn) \| `wrap` \| `exec` |
| `-resume` | profile's | text sent on resume (usually `continue`) |
| `-resume-cmd` | — | headless resume command, overriding the profile |
| `-limit-regex` | — | extra detection regex (repeatable) |
| `-buffer` | `30s` | extra wait after the reset, so you never resume a hair early |
| `-max-wait` | `8h` | cap on a single wait (weekly limits can be days out) |
| `-default-wait` | `1h` | wait used when no reset time can be parsed |
| `-max-retries` | `5` | abort after N immediate re-limits, so it can't loop forever |
| `-no-pty` | `false` | use pipes instead of a PTY (CI / non-interactive) |
| `-notify` | `false` | bell + desktop notification on limit and resume |
| `-quiet` / `-json` | — | suppress status / emit JSON events on stderr |
| `-check-parse` | — | parse a limit string, print the reset time, and exit |

## How reset detection works

The parser tries the most explicit signal first and falls back gracefully:

1. **Epoch** — `…usage limit reached|1739491200`
2. **Relative** — `Please try again in 45.6s`
3. **Clock + timezone** — `resets 2pm (UTC)` / `resets 11:30am (Asia/Calcutta)` → resolved against the IANA tz database, with the next occurrence rolled forward and **DST handled correctly**
4. **Fallback** — nothing parsed? estimate the next window boundary (Claude's 5-hour cadence) or wait `--default-wait`, then re-check

When a vendor inevitably tweaks the wording, you'll see it immediately:

```console
$ claude-auto-resume --check-parse "You've hit your limit · resets 2pm (UTC)"
input:      "You've hit your limit · resets 2pm (UTC)"
matched:    yes  (strategy: clock)
reset at:   Tue 2026-06-17 2:00:00 PM UTC  (14:00:00 UTC)
resume at:  Tue 2026-06-17 2:00:30 PM UTC  (+30s buffer)
wait:       5h38m from now
```

## FAQ

**Does it read my code or prompts?** No. It only scans the output stream for the limit message. Nothing is stored or sent anywhere.

**What if it parses the time wrong?** It self-corrects. If it resumes too early and the tool limits again, it simply re-reads and waits again. `--max-wait` caps any single wait and `--max-retries` stops a hammering loop.

**Weekly limits?** Those can reset days away and the message only gives a clock time. It'll wait up to `--max-wait` (8h by default) and re-check; raise `--max-wait` if you want it to ride out a multi-day reset in one go.

**Will it loop forever?** No — `--max-retries` (default 5) aborts if the tool keeps limiting immediately without making progress.

**Does Ctrl-C still work?** Yes. During a wait, Ctrl-C cancels cleanly and restores your terminal.

**Windows?** Yes — it uses ConPTY. Headless mode is rock-solid; interactive resume works, with best-effort window-resize handling.

## Contributing

Adding a tool is just data — a new entry in [`internal/profiles/profiles.go`](internal/profiles/profiles.go) with its limit patterns and a resume strategy. PRs with real limit-message samples (run `--check-parse` and paste the output) are especially welcome.

```bash
go test ./...        # unit + real-process integration tests
go build ./...
```

## License

[MIT](LICENSE)
