#!/bin/sh
# Locate the latest claude binary from the host-mounted versions directory
# and symlink it into the aide user's local bin so it's on PATH.
# Re-runs on every container start, so host auto-updates are picked up on restart.
set -e

CLAUDE_VERSIONS=/home/aide/.local/share/claude/versions
LOCAL_BIN=/home/aide/.local/bin

# Fast path: binary already available on PATH (mounted directly from host).
if command -v claude > /dev/null 2>&1; then
  echo "aide: using claude at $(command -v claude)"
# Slow path: Linux-style versioned install under ~/.local/share/claude/versions.
elif [ -d "$CLAUDE_VERSIONS" ]; then
  LATEST=$(ls -v "$CLAUDE_VERSIONS" 2>/dev/null | tail -1)
  if [ -n "$LATEST" ]; then
    mkdir -p "$LOCAL_BIN"
    ln -sf "${CLAUDE_VERSIONS}/${LATEST}" "${LOCAL_BIN}/claude"
    echo "aide: using claude ${LATEST}"
  else
    echo "aide: warning — no claude binary found in ${CLAUDE_VERSIONS}" >&2
  fi
else
  echo "aide: warning — claude not found; set claude_path in config.yaml" >&2
fi

exec "$@"
