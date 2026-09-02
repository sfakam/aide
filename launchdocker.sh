#!/usr/bin/env bash
# Render compose.yml.j2 from config.yaml then run docker compose.
#
# Usage:
#   ./launchdocker.sh [--config PATH] [--build] [--down] [-- <docker compose args>]
#
#   --config PATH   config.yaml to read (default: ~/.aide/config.yaml)
#   --build         pass --build to docker compose up
#   --down          run docker compose down instead of up -d
#   --              everything after is forwarded to docker compose up/down
#
# The rendered compose file is written to ~/.aide/compose.generated.yml
# and is safe to inspect or pass to docker compose manually.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="${HOME}/.aide/config.yaml"
OUTPUT="${HOME}/.aide/compose.generated.yml"
BUILD_FLAG=""
MODE="up"
EXTRA_ARGS=()

# Template search order: ~/.aide/ first (installed via install.sh --docker),
# then the script's own directory (repo clone).
if [[ -f "${HOME}/.aide/compose.yml.j2" ]]; then
  TEMPLATE="${HOME}/.aide/compose.yml.j2"
else
  TEMPLATE="${SCRIPT_DIR}/compose.yml.j2"
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --config)  CONFIG="$2"; shift 2 ;;
    --build)   BUILD_FLAG="--build"; shift ;;
    --down)    MODE="down"; shift ;;
    --)        shift; EXTRA_ARGS=("$@"); break ;;
    *)         echo "Unknown flag: $1" >&2; exit 1 ;;
  esac
done

if [[ ! -f "$CONFIG" ]]; then
  echo "Config not found: $CONFIG" >&2
  echo "Create ~/.aide/config.yaml (see config.example.yaml for the template)." >&2
  exit 1
fi

if [[ ! -f "$TEMPLATE" ]]; then
  echo "Template not found: $TEMPLATE" >&2
  exit 1
fi

# ── Resolve Python interpreter ────────────────────────────────────────────────
# Prefer uv (ephemeral env, works on macOS without touching system Python).
# Fall back to a dedicated venv at ~/.aide/.venv.
VENV="${HOME}/.aide/.venv"

if command -v uv &>/dev/null; then
  PYTHON=(uv run --quiet --with PyYAML --with jinja2 python3 -)
else
  if [[ ! -d "$VENV" ]]; then
    echo "Creating Python venv at ${VENV}..."
    python3 -m venv "$VENV"
    "$VENV/bin/pip" install --quiet PyYAML jinja2
  fi
  PYTHON=("$VENV/bin/python" -)
fi

# ── Render the Jinja2 template via an inline Python script ────────────────────
"${PYTHON[@]}" <<PYEOF
import os, yaml
from jinja2 import Environment, FileSystemLoader

config_path   = os.path.expanduser("${CONFIG}")
template_path = os.path.expanduser("${TEMPLATE}")
template_dir  = os.path.dirname(template_path)
template_file = os.path.basename(template_path)
output_path   = os.path.expanduser("${OUTPUT}")

with open(config_path) as f:
    cfg = yaml.safe_load(f) or {}

home = os.path.expanduser("~")
uid  = os.getuid()
import grp, pwd
gid  = pwd.getpwuid(uid).pw_gid

# Expand ~ in host-side paths of each extra volume entry.
raw_volumes = (cfg.get("docker") or {}).get("volumes") or []
extra_volumes = []
for v in raw_volumes:
    parts = v.split(":", 2)
    parts[0] = os.path.expanduser(parts[0])
    extra_volumes.append(":".join(parts))

env = Environment(
    loader=FileSystemLoader(template_dir),
    trim_blocks=True,
    lstrip_blocks=True,
)
tmpl = env.get_template(template_file)
rendered = tmpl.render(
    home=home,
    uid=uid,
    gid=gid,
    build_context=template_dir,
    extra_volumes=extra_volumes,
)

os.makedirs(os.path.dirname(output_path), exist_ok=True)
with open(output_path, "w") as f:
    f.write(rendered)

print(f"Rendered compose file: {output_path}")
if extra_volumes:
    print(f"Extra volumes ({len(extra_volumes)}):")
    for v in extra_volumes:
        print(f"  {v}")
PYEOF

# ── Pre-create host-side mount source directories ────────────────────────────
# Docker will error with "permission denied on chown" if a bind-mount source
# does not exist. Create the dirs now so Docker never has to.
mkdir -p \
  "${HOME}/.aide" \
  "${HOME}/.claude" \
  "${HOME}/.local/share/claude"

# ── Resolve docker compose command (v2 plugin vs v1 standalone) ──────────────
if ! command -v docker &>/dev/null; then
  echo "Error: 'docker' not found on PATH." >&2
  echo "  Install Docker Desktop: https://www.docker.com/products/docker-desktop/" >&2
  exit 1
fi

if docker compose version &>/dev/null 2>&1; then
  COMPOSE_CMD=(docker compose -f "$OUTPUT")
elif command -v docker-compose &>/dev/null; then
  COMPOSE_CMD=(docker-compose -f "$OUTPUT")
else
  echo "Error: Docker Compose not found." >&2
  echo "  Docker Desktop (recommended): https://www.docker.com/products/docker-desktop/" >&2
  echo "  Or install Compose standalone: brew install docker-compose" >&2
  echo ""
  echo "  'docker' was found but 'docker compose' is not available." >&2
  echo "  If you installed docker via 'brew install docker' you only got the CLI —" >&2
  echo "  you also need a container runtime (Docker Desktop, OrbStack, or Colima)." >&2
  exit 1
fi

if [[ "$MODE" == "down" ]]; then
  echo "Stopping aide container..."
  if [[ ${#EXTRA_ARGS[@]} -gt 0 ]]; then
    "${COMPOSE_CMD[@]}" down "${EXTRA_ARGS[@]}"
  else
    "${COMPOSE_CMD[@]}" down
  fi
else
  echo "Starting aide container..."
  if [[ ${#EXTRA_ARGS[@]} -gt 0 ]]; then
    "${COMPOSE_CMD[@]}" up -d $BUILD_FLAG "${EXTRA_ARGS[@]}"
  else
    "${COMPOSE_CMD[@]}" up -d $BUILD_FLAG
  fi
  echo ""
  echo "Logs:  docker compose -f ${OUTPUT} logs -f"
  echo "Stop:  $0 --down"
fi
