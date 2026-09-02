#!/usr/bin/env bash
# Install aide — Claude Code bot daemon
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/sfakam/aide/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/sfakam/aide/main/install.sh | bash -s -- --service
#   curl -fsSL https://raw.githubusercontent.com/sfakam/aide/main/install.sh | bash -s -- --version v1.2.3
set -euo pipefail

REPO="sfakam/aide"
BINARY="aide"
INSTALL_DIR="/usr/local/bin"
SETUP_SERVICE=false
DOCKER_MODE=false
PINNED_VERSION=""

# ── Parse flags ───────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --service)   SETUP_SERVICE=true; shift ;;
    --docker)    DOCKER_MODE=true; shift ;;
    --version)   PINNED_VERSION="$2"; shift 2 ;;
    -h|--help)
      echo "Usage: install.sh [--service] [--docker] [--version vX.Y.Z]"
      echo "  --service   Install native binary + system service (systemd/launchd)"
      echo "  --docker    Install Docker launcher (launchdocker.sh + template) — no binary"
      echo "  --version   Install a specific release tag (default: latest)"
      exit 0 ;;
    *) echo "Unknown flag: $1" >&2; exit 1 ;;
  esac
done

# ── Docker mode — download launcher + template, skip native binary ────────────
if [[ "$DOCKER_MODE" == "true" ]]; then
  RAW_BASE="https://raw.githubusercontent.com/${REPO}/main"
  mkdir -p ~/.aide

  echo "Downloading launchdocker.sh and compose.yml.j2..."
  curl -fsSL -o ~/.aide/launchdocker.sh  "${RAW_BASE}/launchdocker.sh"
  curl -fsSL -o ~/.aide/compose.yml.j2   "${RAW_BASE}/compose.yml.j2"
  chmod 755 ~/.aide/launchdocker.sh

  if [[ ! -f ~/.aide/config.yaml ]]; then
    curl -fsSL -o ~/.aide/config.yaml "${RAW_BASE}/config.example.yaml"
    echo "Created ~/.aide/config.yaml — fill in your credentials before starting"
  else
    echo "~/.aide/config.yaml already exists — not overwritten"
  fi

  if [[ ! -f ~/.aide/tasks.yaml ]]; then
    curl -fsSL -o ~/.aide/tasks.yaml "${RAW_BASE}/tasks.example.yaml"
  fi

  echo ""
  echo "aide Docker installer ready."
  echo ""
  echo "Next steps:"
  echo "  1. Edit ~/.aide/config.yaml — add your Webex/Telegram bot tokens"
  echo "     and optionally set docker.volumes for extra mount points"
  echo "  2. Start:   ~/.aide/launchdocker.sh"
  echo "  3. Logs:    docker compose -f ~/.aide/compose.generated.yml logs -f"
  echo "  4. Stop:    ~/.aide/launchdocker.sh --down"
  exit 0
fi

# ── Detect platform ───────────────────────────────────────────────────────────
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
case "$OS" in
  linux|darwin) ;;
  *) echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac

# ── Resolve version ───────────────────────────────────────────────────────────
if [[ -z "$PINNED_VERSION" ]]; then
  echo "Fetching latest release..."
  PINNED_VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | cut -d'"' -f4)
  if [[ -z "$PINNED_VERSION" ]]; then
    echo "Failed to resolve latest version — pass --version vX.Y.Z explicitly" >&2
    exit 1
  fi
fi

ASSET="aide-${OS}-${ARCH}"
URL="https://github.com/${REPO}/releases/download/${PINNED_VERSION}/${ASSET}"
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${PINNED_VERSION}/checksums.txt"

echo "Installing aide ${PINNED_VERSION} (${OS}/${ARCH})..."

# ── Download & verify ─────────────────────────────────────────────────────────
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

curl -fsSL -o "${TMP_DIR}/${ASSET}" "$URL"
curl -fsSL -o "${TMP_DIR}/checksums.txt" "$CHECKSUM_URL"

# Verify checksum (sha256sum on Linux, shasum on macOS)
if command -v sha256sum &>/dev/null; then
  (cd "$TMP_DIR" && grep "${ASSET}" checksums.txt | sha256sum --check --quiet)
elif command -v shasum &>/dev/null; then
  (cd "$TMP_DIR" && grep "${ASSET}" checksums.txt | shasum -a 256 --check --quiet)
else
  echo "Warning: no sha256sum/shasum found — skipping checksum verification"
fi

chmod 755 "${TMP_DIR}/${ASSET}"

# ── Install binary ────────────────────────────────────────────────────────────
if [[ -w "$INSTALL_DIR" ]]; then
  mv "${TMP_DIR}/${ASSET}" "${INSTALL_DIR}/${BINARY}"
else
  sudo mv "${TMP_DIR}/${ASSET}" "${INSTALL_DIR}/${BINARY}"
fi
echo "Installed: ${INSTALL_DIR}/${BINARY}"

# ── Optionally set up service ─────────────────────────────────────────────────
if [[ "$SETUP_SERVICE" == "true" ]]; then
  RAW_BASE="https://raw.githubusercontent.com/${REPO}/main"
  mkdir -p ~/.aide

  if [[ ! -f ~/.aide/config.yaml ]]; then
    curl -fsSL -o ~/.aide/config.yaml "${RAW_BASE}/config.example.yaml"
    echo "Created ~/.aide/config.yaml — fill in your credentials"
  else
    echo "~/.aide/config.yaml already exists — not overwritten"
  fi

  if [[ ! -f ~/.aide/tasks.yaml ]]; then
    curl -fsSL -o ~/.aide/tasks.yaml "${RAW_BASE}/tasks.example.yaml"
    echo "Created ~/.aide/tasks.yaml"
  else
    echo "~/.aide/tasks.yaml already exists — not overwritten"
  fi

  if [[ "$OS" == "linux" ]]; then
    mkdir -p ~/.config/systemd/user
    curl -fsSL -o ~/.config/systemd/user/aide.service "${RAW_BASE}/aide.service"
    systemctl --user daemon-reload
    systemctl --user enable aide
    echo ""
    echo "  Enabled systemd user service. Start it with:"
    echo "    systemctl --user start aide"
    echo "    journalctl --user -fu aide"
  elif [[ "$OS" == "darwin" ]]; then
    mkdir -p ~/Library/LaunchAgents
    curl -fsSL "${RAW_BASE}/aide.plist" \
      | sed "s|__HOME__|$HOME|g" \
      > ~/Library/LaunchAgents/local.aide.plist
    launchctl unload ~/Library/LaunchAgents/local.aide.plist 2>/dev/null || true
    launchctl load -w ~/Library/LaunchAgents/local.aide.plist
    echo ""
    echo "  Enabled launchd agent. Start it with:"
    echo "    launchctl start local.aide"
    echo "    tail -f /tmp/aide.err"
  fi
fi

# ── Done ──────────────────────────────────────────────────────────────────────
echo ""
echo "aide ${PINNED_VERSION} installed."
if [[ "$SETUP_SERVICE" != "true" ]]; then
  echo ""
  echo "Next steps:"
  echo "  1. Create ~/.aide/config.yaml (see https://github.com/${REPO})"
  if [[ "$OS" == "linux" ]]; then
    echo "  2. Run: curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash -s -- --service"
    echo "     or:  make service   (from the repo clone)"
  elif [[ "$OS" == "darwin" ]]; then
    echo "  2. Run: curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash -s -- --service"
    echo "     or:  make service-macos   (from the repo clone)"
  fi
fi
