# aide — Workflow & Black-Box Design

## What it is

A lightweight bot daemon that bridges messaging channels to Claude CLI sessions running in persistent pseudo-terminals. One process, no containers, no databases.

---

## Core Concepts

| Concept | Description |
|---------|-------------|
| **Worker** | A named Claude instance. Has its own working directory, persistent PTY process, and optional persona (via `CLAUDE.md`). |
| **Channel** | A messaging platform connection (Telegram bot, Webex bot, etc.). Ingests messages and delivers replies. |
| **Wiring** | A link between a channel and a worker. Controls when the worker engages and how sessions are scoped. |
| **Session** | The active PTY process for a worker, scoped according to the wiring's `session_mode`. |

Workers and channels are defined independently — the wiring table joins them. This is the same separation nanoclaw uses between `agent_groups` and `messaging_groups`.

---

## Top-Level Flow

```
Messaging channel (Telegram / Webex)
         │
         │  inbound: (channel_id, room_id, sender_id, text)
         ▼
    Channel Adapter
         │
         │  emits to Router
         ▼
      Router
         │
         │  looks up wirings for this channel_id
         │  evaluates engage_mode for each wiring
         │
         ├──── wiring 1 engages ──▶  resolve_session(worker_1, ...) ──▶ PTY
         ├──── wiring 2 engages ──▶  resolve_session(worker_2, ...) ──▶ PTY
         └──── wiring 3 silent  ──▶  (skipped / not engaged)
                                              │
                                     write message to PTY
                                              │
                                     wait for response
                                              │
                                     Channel Adapter
                                              │
                                     deliver reply to room
```

A single inbound message can engage **multiple workers in parallel** (fan-out) if multiple wirings match. Each worker replies independently.

---

## Workers

Each worker is a named entity in config with:
- `id` — unique name (`"support"`, `"dev-assistant"`, etc.)
- `work_dir` — base directory; sessions are created as subdirectories
- Optionally: custom `claude_path`, `claude_args`, per-worker `CLAUDE.md`

A worker's PTY process is **not started until the first message arrives**. After that it stays alive, maintaining conversation context, until it idles out.

---

## Channels

Each channel entry in config has:
- `id` — unique name (`"my-telegram"`, `"work-webex"`)
- `type` — `telegram` or `webex`
- Type-specific credentials (`bot_token`, etc.)

Channels poll their platform independently. They emit all inbound messages to the Router regardless of which worker will handle them.

---

## Wirings

A wiring connects a channel to a worker and has two configuration knobs:

### engage_mode

Controls when the worker is activated for a given message.

| Mode | When the worker engages |
|------|------------------------|
| `always` | Every message in the wired channel |
| `mention` | Only when the bot is @mentioned (group rooms) |
| `pattern` | Only when message text matches `engage_pattern` (regex) |

Default: `always`.

### session_mode

Controls how the PTY session is scoped.

| Mode | Session key | Description |
|------|-------------|-------------|
| `worker-shared` | `{worker_id}` | One PTY for this worker across **all** channels and senders. Entire worker shares one context. |
| `shared` | `{worker_id}:{channel_id}` | One PTY per (worker, channel) pair. All senders in the same channel share context. |
| `per-user` | `{worker_id}:{channel_id}:{sender_id}` | One PTY per (worker, channel, sender). Each user gets an isolated conversation. |

Default: `per-user`.

---

## Session Resolution

```
resolve_session(worker_id, channel_id, sender_id, session_mode)
    │
    ├── session_mode = "worker-shared"
    │       key = worker_id
    │
    ├── session_mode = "shared"
    │       key = worker_id + ":" + channel_id
    │
    └── session_mode = "per-user"
            key = worker_id + ":" + channel_id + ":" + sender_id
                │
                ├── session exists and PTY alive? ──▶ reuse
                └── otherwise ──▶ spawn new PTY, wait for ready
```

Session lookup is O(1) (dict by key). Idle sessions are reaped after a configurable timeout.

---

## PTY Session Lifecycle

```
  SPAWNING
     │  launch Claude CLI in pseudo-terminal
     │  watch for bypass-permissions dialog → auto-accept
     │
  READY  (interactive prompt detected)
     │
  IDLE  ──── message arrives ────▶  PROCESSING
     │                                  │  write "[Message from <sender>]: <text>"
     │◀─────── response captured ───────│  accumulate PTY output
     │                                  │  find ---RESPONSE--- / ---END--- markers
  IDLE
     │
  (timeout) ──▶  DEAD  (process killed, slot freed)
     │
  (next message) ──▶  SPAWNING  (automatic respawn)
```

### Response extraction contract

Each worker's working directory contains a `CLAUDE.md` that instructs Claude to wrap every reply in:

```
---RESPONSE---
reply text here
---END---
```

The daemon reads PTY output until it finds `---END---`, then extracts the body between the markers. This is the only parsing contract — no ANSI stripping required for the response body.

---

## Configuration Shape

```
{
  "work_dir":     parent directory for all session subdirectories,
  "claude_path":  path to claude binary (default: "claude"),

  "channels": [
    { "id": "...", "type": "telegram", "bot_token": "..." },
    { "id": "...", "type": "webex",    "bot_token": "...", "poll_interval_secs": 5 }
  ],

  "workers": [
    {
      "id":       "...",
      "work_dir": "optional override",
      "session_timeout_minutes": 60
    }
  ],

  "wirings": [
    {
      "channel_id":    "...",
      "worker_id":     "...",
      "engage_mode":   "always" | "mention" | "pattern",
      "engage_pattern": "regex (required when mode=pattern)",
      "session_mode":  "per-user" | "shared" | "worker-shared"
    }
  ]
}
```

---

## Data Flow Diagram

```
┌───────────────────────────────────────────────────────────────────┐
│                          aide daemon                           │
│                                                                   │
│  ┌─────────────┐   ┌─────────────┐   ┌─────────────┐            │
│  │  Telegram   │   │    Webex    │   │  (future)   │  channels  │
│  │  Adapter    │   │  Adapter    │   │   Adapter   │            │
│  └──────┬──────┘   └──────┬──────┘   └──────┬──────┘            │
│         └─────────────────┴─────────────────┘                    │
│                            │  inbound event                       │
│                            ▼                                      │
│                         Router                                    │
│                      (wiring table)                               │
│                            │                                      │
│              ┌─────────────┼─────────────┐                       │
│              ▼             ▼             ▼   (fan-out)            │
│        [worker A]    [worker B]    [worker C]                     │
│        PTY sessions  PTY sessions  PTY sessions                  │
│        per session   per session   per session                    │
│        mode          mode          mode                           │
└───────────────────────────────────────────────────────────────────┘
```

---

## Example Wiring Scenarios

**Single assistant, both channels, per-user sessions**
```
channels: telegram-personal, work-webex
workers:  assistant
wirings:
  telegram-personal → assistant  (per-user)   ← each Telegram user gets their own claude
  work-webex        → assistant  (per-user)   ← each Webex DM gets their own claude
```

**Shared team assistant (one context for the whole room)**
```
wirings:
  team-webex-room → assistant  (shared)   ← whole room shares one claude PTY
```

**Specialist workers, pattern-routed**
```
workers:  dev-worker, support-worker
wirings:
  telegram → dev-worker      (pattern: "^/dev ")
  telegram → support-worker  (pattern: "^/support ")
  telegram → support-worker  (engage: mention, session_mode: shared)
```

**One worker, all channels share its context**
```
wirings:
  telegram → god-worker  (worker-shared)
  webex    → god-worker  (worker-shared)
  ← both channels feed the same single claude PTY
```

---

## Persistence (SQLite)

A single lightweight SQLite file (`aide.db`) stores only what is needed to survive daemon restarts without causing duplicate responses to users.

### What is persisted

**`channel_state`** — one row per channel, stores the deduplication cursor so polling resumes from the right position after a restart.

```
channel_state
  channel_id  TEXT  PRIMARY KEY   -- e.g. "my-telegram", "work-webex"
  cursor      TEXT                -- Telegram: last update_id
                                  -- Webex: JSON { room_id: last_iso_timestamp }
```

That is the entire schema.

### What is NOT persisted

| Thing | Why not |
|-------|---------|
| PTY session state | The PTY process *is* the session; restarting it gives users a fresh context, which is expected and acceptable |
| Message history | Out of scope; PTY terminal scrollback covers debugging needs |
| Workers / channels / wirings config | Lives in `config.json` — human-editable, version-controllable |
| User identities / roles | No multi-tenant model |

### Restart behaviour

| Event | Effect |
|-------|--------|
| Daemon restart | Channels resume from saved cursor — no duplicate messages |
| PTY session dies (crash / idle timeout) | Next message spawns a fresh PTY; conversation context resets |
| `config.json` edited | New workers/wirings take effect on next restart |

---

## What aide deliberately omits

| nanoclaw feature | Why omitted |
|-----------------|-------------|
| Docker containers | PTY replaces them — Claude CLI runs directly on the host |
| SQLite session DBs | No persistence layer; state lives in the PTY process memory |
| Agent-runner / host split | No separation needed without containers |
| User roles and permissions | Single-operator; no multi-tenant access control |
| Approval flows | No privileged action gating |
| Skills install system | `CLAUDE.md` in the worker's work dir covers customisation |
| Service daemon setup | Caller's responsibility (systemd, launchd, tmux, etc.) |
| Credential vault (OneCLI) | Claude CLI's own auth is used directly |
