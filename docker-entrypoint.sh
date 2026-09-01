#!/bin/sh
# Locate the latest claude binary from the host-mounted versions directory
# and symlink it into the aide user's local bin so it's on PATH.
# Re-runs on every container start, so host auto-updates are picked up on restart.
set -e

CLAUDE_VERSIONS=/home/aide/.local/share/claude/versions
LOCAL_BIN=/home/aide/.local/bin

if [ -d "$CLAUDE_VERSIONS" ]; then
  # Sort by version (ls -v) and take the newest
  LATEST=$(ls -v "$CLAUDE_VERSIONS" 2>/dev/null | tail -1)
  if [ -n "$LATEST" ]; then
    mkdir -p "$LOCAL_BIN"
    ln -sf "${CLAUDE_VERSIONS}/${LATEST}" "${LOCAL_BIN}/claude"
    echo "aide: using claude ${LATEST}"
  else
    echo "aide: warning — no claude binary found in ${CLAUDE_VERSIONS}" >&2
  fi
else
  echo "aide: warning — ${CLAUDE_VERSIONS} not mounted; set claude_path in config.yaml" >&2
fi

exec "$@"
