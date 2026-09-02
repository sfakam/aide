#!/bin/sh
# Runs as the aide user (set by USER directive in Dockerfile).
# Builds a combined CA bundle if custom certs are mounted, then execs aide.
set -e

CLAUDE_VERSIONS=/home/aide/.local/share/claude/versions
LOCAL_BIN=/home/aide/.local/bin
COMBINED_CA=/home/aide/.aide/combined-ca.crt

# ── Build combined CA bundle ───────────────────────────────────────────────────
# Merge system CAs with any custom certs so that Node.js (both https and
# fetch/undici) trusts them when launched with NODE_OPTIONS=--use-openssl-ca.
# OpenSSL reads SSL_CERT_FILE; we export it before exec so aide + claude inherit it.
CERT_DIR=/home/aide/.claude-certs
if [ -d "$CERT_DIR" ]; then
  custom_count=0
  for cert in "$CERT_DIR"/*.pem "$CERT_DIR"/*.crt; do
    [ -f "$cert" ] && custom_count=$((custom_count + 1))
  done
  if [ "$custom_count" -gt 0 ]; then
    mkdir -p "$(dirname "$COMBINED_CA")"
    cat /etc/ssl/certs/ca-certificates.crt "$CERT_DIR"/*.pem "$CERT_DIR"/*.crt \
        2>/dev/null > "$COMBINED_CA" || true
    export SSL_CERT_FILE="$COMBINED_CA"
    echo "aide: combined CA bundle (${custom_count} custom cert(s)) -> ${COMBINED_CA}"
  fi
fi

# ── Locate the claude binary ───────────────────────────────────────────────────
if command -v claude > /dev/null 2>&1; then
  echo "aide: using claude at $(command -v claude)"
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
