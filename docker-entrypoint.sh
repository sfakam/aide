#!/bin/sh
# Runs as root. Installs any mounted custom CA certs, locates the claude binary,
# then drops privileges to the aide user via gosu.
set -e

# ── Install custom CA certs ───────────────────────────────────────────────────
# Certs mounted at /home/aide/.claude-certs are copied into the system CA store
# so that both https (Node TLS) and fetch/undici (OpenSSL) trust them.
# NODE_OPTIONS=--use-openssl-ca (set in compose) makes Node use this store.
CERT_DIR=/home/aide/.claude-certs
if [ -d "$CERT_DIR" ]; then
  changed=0
  for cert in "$CERT_DIR"/*.pem "$CERT_DIR"/*.crt; do
    [ -f "$cert" ] || continue
    name=$(basename "$cert" | sed 's/\.[^.]*$/.crt/')
    cp "$cert" "/usr/local/share/ca-certificates/${name}"
    changed=1
  done
  if [ "$changed" = "1" ]; then
    update-ca-certificates >/dev/null 2>&1 || true
    echo "aide: installed custom CA certs from ${CERT_DIR}"
  fi
fi

# ── Locate the claude binary ──────────────────────────────────────────────────
CLAUDE_VERSIONS=/home/aide/.local/share/claude/versions
LOCAL_BIN=/home/aide/.local/bin

if command -v claude > /dev/null 2>&1; then
  echo "aide: using claude at $(command -v claude)"
elif [ -d "$CLAUDE_VERSIONS" ]; then
  LATEST=$(ls -v "$CLAUDE_VERSIONS" 2>/dev/null | tail -1)
  if [ -n "$LATEST" ]; then
    mkdir -p "$LOCAL_BIN"
    ln -sf "${CLAUDE_VERSIONS}/${LATEST}" "${LOCAL_BIN}/claude"
    chown aide:aide "$LOCAL_BIN/claude" 2>/dev/null || true
    echo "aide: using claude ${LATEST}"
  else
    echo "aide: warning — no claude binary found in ${CLAUDE_VERSIONS}" >&2
  fi
else
  echo "aide: warning — claude not found; set claude_path in config.yaml" >&2
fi

# ── Drop to aide user ─────────────────────────────────────────────────────────
exec gosu aide "$@"
