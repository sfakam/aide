# AIde

**AI**de — an AI-powered aide daemon that connects Claude Code to Webex and Telegram as a persistent bot. Messages are routed to Claude workers and responses are posted back. Sessions persist across messages so Claude retains conversation context.

## Features

- **Webex and Telegram** channel support with typing indicators
- **Multiple workers** — route different channels or patterns to separate Claude instances
- **Flexible routing** — `always`, `mention`, or regex `pattern` engage modes; per-user, shared, or worker-shared sessions
- **Scheduled tasks** — cron-style jobs with two types:
  - `type: ai` — sends a prompt to Claude and posts the response
  - `type: script` — runs a shell command and posts stdout (no Claude involved)
- **Pre/post scripts** — shell commands that run before/after the main action; pre-script must exit 0 or the main action is skipped
- **Adaptive Cards** — Webex task notifications (start, result, errors) are posted as rich Adaptive Cards
- **Hot reload** — edits to `config.yaml` and `tasks.yaml` take effect within 10 seconds; no restart needed
- **Session persistence** — conversations survive restarts via `--continue`

---

## Quickstart

**Requires:** Go 1.22+, Claude Code installed and authenticated.

```bash
git clone https://github.com/sfakam/aide.git
cd aide
```

Then open Claude Code in this directory and run:

```
/aidectl
```

The `/aidectl` skill walks you through the full setup interactively — OS detection, building, service installation, bot creation, config, and tasks. It is the recommended entry point for all setup and configuration.

---

## /aidectl Skill

AIde ships with a Claude Code skill for interactive setup, config, and task management. It is loaded automatically when you open this repo in Claude Code (no plugin registration needed — `.claude/skills/` is discovered automatically).

**Invoke with:**
```
/aidectl
```

**What it covers:**

| Area | Description |
|---|---|
| Initial setup | Detects OS, runs `make service` (Linux/systemd) or `make service-macos` (macOS/launchd) |
| Config | Channels, workers, wirings, engage modes, session modes |
| Bot setup | Creating Webex and Telegram bots, finding room IDs |
| Tasks | AI tasks, script tasks, pre/post scripts, schedules |
| Troubleshooting | Common issues with channels, tasks, and service management |

The skill is defined in [`.claude/skills/aidectl/SKILL.md`](.claude/skills/aidectl/SKILL.md).

---

## Manual Setup Reference

If you prefer to set up AIde without the skill:

### Build + install service

```bash
make service          # Linux  — builds, installs to /usr/local/bin, sets up systemd user service
make service-macos    # macOS  — builds, installs to /usr/local/bin, sets up launchd user agent
```

Both targets are idempotent and create `~/.aide/config.yaml` and `~/.aide/tasks.yaml` from the bundled examples (never overwrites existing files).

### Edit config

```bash
$EDITOR ~/.aide/config.yaml
$EDITOR ~/.aide/tasks.yaml   # optional — for scheduled tasks
```

See [`config.example.yaml`](config.example.yaml) and [`tasks.example.yaml`](tasks.example.yaml) for the full reference.

### Start the service

**Linux**
```bash
systemctl --user start aide
journalctl --user -fu aide
```

**macOS**
```bash
launchctl start local.aide
tail -f /tmp/aide.err
```

### Service management

| Action | Linux | macOS |
|---|---|---|
| Start | `systemctl --user start aide` | `launchctl start local.aide` |
| Stop | `systemctl --user stop aide` | `launchctl stop local.aide` |
| Restart | `systemctl --user restart aide` | `launchctl stop local.aide && launchctl start local.aide` |
| Logs | `journalctl --user -fu aide` | `tail -f /tmp/aide.err` |
| Status | `systemctl --user status aide` | `launchctl list \| grep aide` |

---

## Other Make targets

```bash
make build      # build ./aide binary only
make install    # install to /usr/local/bin/aide
make test       # run tests
make clean      # remove binary
```

**Manual run (dev):**
```bash
./aide --config config.yaml --log-level debug
```

| Flag | Default | Description |
|---|---|---|
| `--config` | `config.yaml` | Config file path |
| `--db` | `aide.db` | SQLite database path |
| `--api` | `:8080` | REST API address (empty to disable) |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |
