# aide

A lightweight daemon that connects Claude Code to Webex and Telegram as a persistent bot.

Messages arrive via polling, are routed to a Claude Code session, and the response is posted back. Sessions persist across messages so Claude retains conversation context.

## Features

- **Webex and Telegram** channel support
- **Multiple workers** — route different channels or patterns to separate Claude instances
- **Scheduled tasks** — cron-style prompts sent to Claude on a schedule
- **Session persistence** — conversations survive restarts via `--continue`
- **Docker support** — runs containerised with minimal host access

## Installation

### Native binary (Linux / macOS)

```bash
# Install binary only
curl -fsSL https://raw.githubusercontent.com/sfakam/aide/main/install.sh | bash

# Install binary + system service (auto-starts on login)
curl -fsSL https://raw.githubusercontent.com/sfakam/aide/main/install.sh | bash -s -- --service
```

### Docker (recommended for macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/sfakam/aide/main/install.sh | bash -s -- --docker
```

This downloads `launchdocker.sh` and the compose template to `~/.aide/`. The container image is pulled from GHCR — no local build needed.

## Configuration

Edit `~/.aide/config.yaml` (created automatically during install):

```yaml
claude_path: claude          # path to the claude CLI binary

channels:
  - id: my-webex
    type: webex
    bot_token: "YOUR_BOT_TOKEN"
    room_id: "ROOM_ID"
    poll_interval_secs: 30

workers:
  - id: assistant
    session_timeout_minutes: 60

wirings:
  - channel_id: my-webex
    worker_id: assistant
    engage_mode: always
    session_mode: shared
```

See [`config.example.yaml`](config.example.yaml) for all options including Telegram, per-user sessions, pattern routing, and scheduled tasks.

### Extra Docker mounts

Add a `docker.volumes` section to mount additional host directories into the container:

```yaml
docker:
  volumes:
    - ~/.certs:/home/aide/.certs:ro
    - ~/git:/home/aide/git:rw
```

## Running

**Native — Linux (systemd)**
```bash
systemctl --user start aide
journalctl --user -fu aide
```

**Native — macOS (launchd)**
```bash
launchctl start local.aide
tail -f /tmp/aide.err
```

**Docker**
```bash
~/.aide/launchdocker.sh          # start
~/.aide/launchdocker.sh --down   # stop
~/.aide/launchdocker.sh --build  # rebuild image from source
docker compose -f ~/.aide/compose.generated.yml logs -f
```

## Building from source

Requires Go 1.22+.

```bash
make build      # build ./aide binary
make install    # install to /usr/local/bin/aide
make test       # run tests
```
