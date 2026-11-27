---
name: aidectl
description: Interactive assistant for setting up, configuring, and managing aide — the Claude-powered bot. Covers initial setup, config.yaml, channel/bot wiring (Webex and Telegram), workers, wirings, tasks.yaml (AI tasks, script tasks, pre/post scripts, schedules), and aide-agents (external agent processes, their unix-socket API, and optional aide-managed scheduled/supervised task types with heartbeats). Invoke with /aidectl.
---

# aidectl — Aide Setup & Configuration Assistant

You are helping the user configure and manage **aide**.

## What is aide?

**aide** is a persistent bot daemon that bridges Webex and Telegram to one or more Claude Code workers. Here is what it does:

- **Inbound**: polls Webex spaces or Telegram chats for new messages
- **Routes**: matches each message against wiring rules (which channel → which Claude worker, with session and engagement rules)
- **Processes**: sends the message to Claude Code running in a PTY session and streams tool-call status updates back to the channel while Claude works
- **Replies**: posts Claude's response back to the same room

The result is a bot in your team's Webex space (or personal Telegram chat) that behaves like a persistent Claude Code session — full tool use, file access, long-running tasks — without anyone needing to open a terminal.

aide also exposes a second, separate surface for **aide-agents**: small external programs (any language) that call aide over a unix socket to trigger Claude inference and optionally relay messages — driven by their own process, not by inbound chat. See Area E.

**Credentials you will need:**

| Credential | What it's for | Where to get it |
|---|---|---|
| Claude Code authenticated | Powers all AI workers | `claude` CLI — must be logged in before aide starts |
| Webex Bot Token | Lets aide poll and post to Webex | developer.webex.com → My Webex Apps |
| Telegram Bot Token | Lets aide poll and post to Telegram | @BotFather in Telegram |

You only need the tokens for the platforms you actually want to use (Webex, Telegram, or both).

---

## Step 1 — Discover current state

Before doing anything else, run these checks:

```bash
# Platform
uname -s

# Claude binary — must be installed and authenticated
claude --version
claude whoami 2>/dev/null || echo "NOT LOGGED IN"

# Repo state
ls ~/.aide/config.yaml 2>/dev/null && echo "config exists" || echo "no config yet"
ls ~/.aide/tasks.yaml  2>/dev/null && echo "tasks exists"  || echo "no tasks yet"
ls /usr/local/bin/aide 2>/dev/null && echo "binary installed" || echo "binary not installed"
ls ~/.aide/agents.sock 2>/dev/null && echo "agent API socket is up" || echo "agent API socket not found (aide not running, or agents_socket_path is different)"
```

If `claude whoami` fails, **stop and tell the user to authenticate first**:
```bash
claude login
```
aide spawns Claude Code as a subprocess — if the CLI is not authenticated, all workers will fail silently.

Then ask the user what they need help with if not already stated:

> What would you like to do?
> 1. **Initial setup** — first-time install, build, and run
> 2. **Config** — add/edit channels, workers, or wirings in `config.yaml`
> 3. **Bot setup** — create a Webex or Telegram bot and get its token
> 4. **Tasks** — add or edit scheduled tasks in `tasks.yaml`
> 5. **Agents** — register an aide-agent, test its unix-socket API, or have aide launch/supervise its process

---

## Area A — Initial Setup

### Detect platform first

```bash
uname -s
```

Use this to pick the right service target:
- `Linux` → `make service` (systemd user service)
- `Darwin` → `make service-macos` (launchd user agent)

### Build + install as a system service (recommended)

**Linux:**
```bash
make service
# installs binary to /usr/local/bin/aide
# creates ~/.aide/config.yaml and ~/.aide/tasks.yaml from examples (never overwrites)
# installs ~/.config/systemd/user/aide.service
# enables the service (does NOT start it yet — edit config first)
```

After editing `~/.aide/config.yaml`:
```bash
systemctl --user start aide
journalctl --user -fu aide    # follow logs
```

**macOS:**
```bash
make service-macos
# installs binary to /usr/local/bin/aide
# creates ~/.aide/config.yaml and ~/.aide/tasks.yaml from examples (never overwrites)
# installs ~/Library/LaunchAgents/local.aide.plist and loads it
```

After editing `~/.aide/config.yaml`:
```bash
launchctl start local.aide
tail -f /tmp/aide.err         # follow logs
```

> **Config location when using service setup:** `~/.aide/config.yaml` and `~/.aide/tasks.yaml`
> These files are created from the repo examples and are never overwritten by re-running `make service`.

### Manual run (dev / no service)

```bash
go build -o aide .
./aide --config config.yaml
```

Flags:
| Flag | Default | Description |
|---|---|---|
| `--config` | `config.yaml` | Path to config file |
| `--db` | `aide.db` | SQLite database path |
| `--api` | `:8080` | REST API listen address (empty to disable) |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

### Service management

| Action | Linux | macOS |
|---|---|---|
| Start | `systemctl --user start aide` | `launchctl start local.aide` |
| Stop | `systemctl --user stop aide` | `launchctl stop local.aide` |
| Restart | `systemctl --user restart aide` | `launchctl stop local.aide && launchctl start local.aide` |
| Logs | `journalctl --user -fu aide` | `tail -f /tmp/aide.err` |
| Status | `systemctl --user status aide` | `launchctl list \| grep aide` |

---

## Area B — config.yaml Reference

The config has five top-level sections: `channels`, `workers`, `wirings`, `agents` (see Area E), and optional global paths.

### Global paths

```yaml
claude_path: claude                      # path to claude binary (default: "claude")
work_dir: ~/aide-workspace                # base directory for worker sessions
db_path: aide.db                          # SQLite database (also stores aide-agent sessions)
tasks_path: tasks.yaml                    # scheduled tasks file
agents_socket_path: ~/.aide/agents.sock   # unix socket for the aide-agents HTTP API
```

### Channels

Each channel is a messaging platform connection. `type` is `webex` or `telegram`.

**Webex channel:**
```yaml
channels:
  - id: my-webex
    type: webex
    bot_token: "YOUR_WEBEX_BOT_TOKEN"
    room_id: "Y2lzY29zcGFya..."   # optional: restrict to one room
    direct_with: "user@example.com" # optional: open/find 1:1 DM with this person
    direct: false                   # true for 1:1 rooms (no @mention required)
    poll_interval_secs: 5
```

**Telegram channel:**
```yaml
channels:
  - id: my-telegram
    type: telegram
    bot_token: "123456:ABC-DEF..."
    poll_interval_secs: 5
```

### Workers

Each worker is a named Claude instance with its own session directory.

```yaml
workers:
  - id: assistant
    work_dir: ~/aide-workspace/assistant  # optional; defaults to <work_dir>/<id>
    session_timeout_minutes: 60
```

### Wirings

Wirings connect a channel to a worker, with routing and session rules.

```yaml
wirings:
  - channel_id: my-webex
    worker_id: assistant
    engage_mode: mention        # always | mention | pattern
    engage_pattern: ""          # regex; required when engage_mode=pattern
    session_mode: per-user      # per-user | shared | worker-shared
    allowed_users:              # email or ID allowlist; empty = allow all
      - user@example.com
    status_batch_secs: 3        # batch tool-call updates (0 = send immediately)
```

**engage_mode:**
- `always` — respond to every message in the room
- `mention` — respond only when @mentioned
- `pattern` — respond when message matches `engage_pattern` regex

**session_mode:**
- `per-user` — each sender gets their own Claude session
- `shared` — all senders in a channel share one session
- `worker-shared` — single session shared across all channels for this worker

---

## Area C — Bot Setup

### What the bot does

The bot is the face of aide in your messaging platform. It:
- Receives messages sent to it (DMs, group spaces, or @mentions)
- Forwards them to a Claude worker
- Posts Claude's responses and status updates back to the same room
- For scheduled tasks: posts task results, pre/post script output, and error cards on its own schedule

The bot itself has no intelligence — it is purely a relay. All reasoning happens in the Claude worker.

---

### Webex Bot

**Create the bot:**
1. Go to [developer.webex.com](https://developer.webex.com) → sign in → **My Webex Apps** → **Create a New App** → **Bot**
2. Fill in: Display Name, Bot Username (e.g. `aide-bot`), Icon
3. Click **Add Bot** — copy the **Bot Access Token** immediately (shown only once)
   - If you lose it, regenerate at developer.webex.com → My Webex Apps → your bot → **Regenerate Access Token**
4. This token is your `bot_token` in `config.yaml`

**No special scopes needed** — Webex bot tokens automatically have permission to send/receive messages in spaces they are added to.

**Add the bot to a space:**
- In Webex, open the target space → Add People → search for the bot username → Add
- For a 1:1 DM: set `direct_with: your@email.com` in config — aide will auto-create the DM on first start

**Find the room ID** (if using `room_id` to restrict to one space):
```bash
curl -s -H "Authorization: Bearer <BOT_TOKEN>" \
  "https://webexapis.com/v1/rooms?max=20" | python3 -m json.tool | grep -E '"title"|"id"'
```
Copy the `"id"` value for the target space. Or omit `room_id` entirely to let the bot respond in any space it is a member of.

---

### Telegram Bot

**Create the bot:**
1. Open Telegram → search for **@BotFather** → start a chat
2. Send `/newbot` → enter a display name → enter a username (must end in `bot`, e.g. `my_aide_bot`)
3. BotFather replies with the **API token** (format: `123456789:ABC-DEF1234...`)
4. This token is your `bot_token` in `config.yaml`

**Privacy mode** — by default Telegram bots in groups only receive messages that @mention them. aide respects this via the `engage_mode` wiring setting. If you want the bot to see all messages in a group (without @mention), disable privacy mode:
```
In @BotFather: /mybots → your bot → Bot Settings → Group Privacy → Turn off
```

**Find the chat ID:**
- Start a DM with the bot or add it to a group, then send any message
- Call:
  ```bash
  curl -s "https://api.telegram.org/bot<TOKEN>/getUpdates" | python3 -m json.tool
  ```
- The `"chat": {"id": ...}` value is your `room_id`
- Group/supergroup IDs are negative numbers (e.g. `-100123456789`); DM IDs are positive

---

## Area D — tasks.yaml Reference

Tasks are scheduled jobs. Each task has a `type`:
- `type: ai` — sends a prompt to Claude and posts the response
- `type: script` — runs a shell command and posts stdout (no Claude involved)
- `type: agent` — launches/supervises an external aide-agent process; see Area E

`ai` and `script` support `pre_script` and `post_script`. **The pre-script must exit 0 or the main action is skipped.**

### AI task

```yaml
tasks:
  - id: morning-standup
    name: Morning Standup Summary
    type: ai                        # calls Claude; requires worker_id + prompt
    enabled: true
    worker_id: assistant
    session_mode: task-isolated     # task-isolated | worker-shared | shared
    schedule: "0 9 * * 1-5"        # cron, or @hourly / @daily / @weekly
    timeout_secs: 120
    prompt: |
      Write a brief standup summary for the team.
      Keep it to 3–5 bullet points.
    pre_script:                     # optional; must exit 0 or prompt is skipped
      name: "check connectivity"
      cmd: "curl -sf https://api.example.com/health"
    post_script:                    # optional; runs after AI response is delivered
      name: "log run"
      cmd: "echo standup done >> /tmp/aide.log"
    outputs:
      - channel_id: my-webex
        room_id: "Y2lzY29zcGFya..."
      - channel_id: my-telegram
        room_id: "-100123456789"
```

### Script task

```yaml
  - id: db-vacuum
    name: DB Vacuum
    type: script                    # runs cmd directly; no Claude
    enabled: true
    schedule: "0 2 * * 0"          # 2:00 AM every Sunday
    timeout_secs: 300
    pre_script:                     # must exit 0 or cmd is skipped
      name: "checkpoint WAL"
      cmd: "sqlite3 aide.db 'PRAGMA wal_checkpoint(FULL);'"
    cmd:                            # required for type: script
      name: "vacuum database"
      cmd: "sqlite3 aide.db 'VACUUM; PRAGMA integrity_check;'"
    post_script:
      name: "log completion"
      cmd: "echo vacuum done >> /tmp/aide.log"
    outputs:
      - channel_id: my-webex
        room_id: "Y2lzY29zcGFya..."
```

### Schedule syntax

| Expression | Meaning |
|---|---|
| `@hourly` | Every hour |
| `@daily` | Every day at midnight |
| `@weekly` | Every Sunday at midnight |
| `"0 9 * * 1-5"` | 9:00 AM Mon–Fri |
| `"30 8 * * *"` | 8:30 AM every day |
| `"0 2 * * 0"` | 2:00 AM every Sunday |

### session_mode (AI tasks)

| Value | Behaviour |
|---|---|
| `task-isolated` | Own PTY session per task; never mixes with user conversations (default) |
| `worker-shared` | Reuses the worker's shared session |
| `shared` | Shared across all tasks on this worker |

---

## Area E — aide-agents (external agent processes)

### What this is

aide-agents are small, custom-built external programs — any language, Python and Go covered
today — that call aide over a **unix domain socket** (separate from the `--api` TCP port in Area
A) to trigger Claude inference and optionally relay messages through aide's channels. Unlike a
`worker`, an agent is driven by its own external process on its own schedule, not by inbound chat
messages, and it has no persistent PTY — each call is a stateless, resumable Claude invocation.

Full protocol, wire format, and rationale: the `aide-agents` repo (sibling checkout next to this
one), `docs/design.md`. Client library and a worked example: `aide-agents/python/`.

### Register an agent in config.yaml

```yaml
agents_socket_path: ~/.aide/agents.sock   # optional; this is the default

agents:
  - id: disk-usage-watcher
    token: "generate-a-random-secret-here"     # bearer token the agent process must present
    models_allowed: ["claude-sonnet-5", "claude-haiku-4-5"]  # optional; empty allowlist = any model
    default_model: "claude-sonnet-5"
    effort_allowed: ["low", "medium", "high", "xhigh", "max"]  # optional
    default_effort: "low"
    rate_limit:
      max_requests: 20
      window_minutes: 60
    max_turns_per_session: 50
    max_session_duration_minutes: 480
    idle_timeout_minutes: 1440
    # Only needed if this agent calls POST /messages to relay through aide's own bots —
    # it can equally well post to Webex/Telegram directly with its own bot token instead.
    channels_allowed:
      - channel_id: my-webex
        room_ids: ["Y2lzY29zcGFya..."]
```

Generate a token with `openssl rand -hex 32`. This whole section is fully hot-reloadable — unlike
`workers`/`channels`, editing it takes effect within 10s with no restart, because an agent has no
persistent process tied to its registration (see Hot Reload below).

### The agent HTTP API

| Method | Path | Purpose |
|---|---|---|
| `POST /api/v1/agents/{agent_id}/infer` | Run one inference call — `prompt` required; optional `session_id`, `session_ttl_minutes`, `model`, `effort` |
| `POST /api/v1/agents/{agent_id}/messages` | *(optional)* post `{channel_id, room_id, text}` through aide's own channels |
| `POST /api/v1/agents/{agent_id}/heartbeat` | *(optional)* report healthy — only read by `agent_mode: supervised` tasks with a heartbeat timeout, below |

Test it directly:
```bash
curl -s -X POST --unix-socket ~/.aide/agents.sock \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
  -d '{"prompt":"say hi"}' \
  http://x/api/v1/agents/disk-usage-watcher/infer
```

### Writing the agent itself

Minimal shape using the `aide-agents` repo's Python client (`aide_agent.AideAgentClient`):

```python
from aide_agent import AideAgentClient

with AideAgentClient(agent_id="disk-usage-watcher", token="...") as client:
    result = client.infer(prompt="...", session_id="main", session_ttl_minutes=0)
    print(result["response"])
```

Or the Go client (`aideagent.Client`), same shape:

```go
client := aideagent.New("disk-usage-watcher", token, aideagent.DefaultSocketPath)
result, err := client.Infer(ctx, aideagent.InferRequest{Prompt: "...", SessionID: "main"})
```

Any language works — it's plain HTTP+JSON over a unix socket. See `aide-agents/python/examples/`
and `aide-agents/go/examples/` for the full worked example in each language.

**Standard invocation convention** (use this in `cmd.cmd` below, or in your own cron/systemd unit):
- **Python**: `uv run --project <dir> <script.py>` — never a bare `python3 script.py`. `uv`
  resolves `pyproject.toml` and syncs the venv automatically.
- **Go**: build once (`cd go && make build` in the `aide-agents` repo), then run the resulting
  binary directly — never `go run`, which recompiles on every invocation.

### Optional: let aide launch and supervise the process

If you don't want to run the agent under systemd/cron/your own tooling, `tasks.yaml` has a
`type: agent` task — same file, same shape as `type: script`, with a new `agent_mode`:

```yaml
tasks:
  # agent_mode: scheduled — cron, run to completion, like type: script, but stdout/stderr
  # stream live into aide's own log tagged agent=<id> instead of only going to outputs.
  - id: disk-usage-watcher
    type: agent
    agent_mode: scheduled
    enabled: true
    schedule: "@hourly"
    cmd:
      name: disk-usage-watcher
      cmd: "uv run --project /opt/aide-agents/python /opt/aide-agents/python/examples/disk_usage_watcher.py"
    outputs: []   # optional for type: agent — omit or leave empty if nothing posts to a channel

  # agent_mode: supervised — no schedule; aide starts it once and keeps it running,
  # restarting with exponential backoff if it ever exits.
  - id: queue-poller
    type: agent
    agent_mode: supervised
    enabled: true
    agent_id: queue-poller          # must match agents[].id above, for /heartbeat auth
    heartbeat_timeout_secs: 120     # omit (or 0) if the process never calls /heartbeat
    restart_backoff_secs: 5         # default 5
    max_restart_backoff_secs: 60    # default 60
    cmd:
      name: queue-poller
      cmd: "/opt/aide-agents/go/bin/queue-poller"   # pre-built; see invocation convention above
```

Key differences from `ai`/`script` tasks:
- `scheduled` has no `schedule` restriction beyond the normal cron syntax; `supervised` must
  **not** set `schedule` at all — it runs continuously instead.
- `outputs` is optional for `type: agent` (both modes) — log tagging is the primary visibility
  mechanism; set `outputs` too if you still want a channel notification.
- `heartbeat_timeout_secs` requires `agent_id`. If a supervised process is alive but stuck (not
  crashed), a missed heartbeat within the timeout triggers a kill + restart just like a crash
  would — call `client.heartbeat()` from the agent's main loop on a timer well inside that window.

---

## Hot Reload

Both `config.yaml` and `tasks.yaml` are watched for changes every 10 seconds. No restart needed:
- **tasks.yaml changed** → cron-driven tasks (`ai`/`script`/`agent_mode: scheduled`) rebuild;
  `agent_mode: supervised` processes are diffed by task id — removed or changed ones restart,
  unchanged ones keep running uninterrupted
- **config.yaml changed** → router reloads wirings and the agent registry (`agents:`) reloads in
  full; channel/worker changes still need a restart

---

## Troubleshooting

| Symptom | Check |
|---|---|
| Bot not responding | Is `engage_mode` correct? Group rooms need `mention` or the bot must be @mentioned. Check `--log-level debug`. |
| Task not firing | Is `enabled: true`? Is the cron expression valid? Check logs for "task scheduled". |
| Pre-script blocking task | Pre-script exited non-zero — check the error card in the channel or logs. |
| Token expired (Webex) | Regenerate token at developer.webex.com; update `bot_token` in `~/.aide/config.yaml` then restart the service. |
| Service not starting | Check `journalctl --user -xe` (Linux) or `/tmp/aide.err` (macOS). Usually a bad config.yaml or missing claude binary. |
| `worker_id not found` | Wiring or task references a worker ID not defined in `workers:`. |
| Workers return empty responses | Run `claude whoami` — if it fails, run `claude login` and restart aide. |
| Webex token lost/expired | developer.webex.com → My Webex Apps → your bot → Regenerate Access Token. Update `bot_token` in config and restart. |
| Agent call returns 401 | Bearer token doesn't match `agents[].token` for that `agent_id`, or the `agent_id` in the URL is wrong. |
| Agent call returns 404 | Unknown `agent_id` — check it's in `config.yaml`'s `agents:` list and the config reload has had ~10s to pick it up. |
| Agent call returns 403 | Requested `model`/`effort` not in that agent's allowlist, or (for `/messages`) `channel_id`/`room_id` not in `channels_allowed`. |
| Agent call returns 429 | Rate limit or session wall-clock cap hit — check `rate_limit`/`max_session_duration_minutes` for that agent. |
| `agents.sock` not found | aide isn't running, or `agents_socket_path` points elsewhere — check `config.yaml` and `--log-level debug` for "agent API listening". |
| Supervised agent keeps restarting | Check logs tagged `agent=<task id>` for the actual stdout/stderr — the process itself is failing or exiting immediately. |
| Supervised agent restarts despite looking healthy | `heartbeat_timeout_secs` is set but the process never calls `POST /heartbeat`, or not often enough — logs show "heartbeat timeout, restarting agent". |
