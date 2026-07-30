#!/bin/sh

set -eu

SERVICE_NAME="sub2api-guardian"
SERVICE_USER="guardian"
INSTALL_DIR="/opt/sub2api-guardian"
DATA_DIR="/var/lib/sub2api-guardian"
ENV_FILE="/etc/sub2api-guardian.env"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
DEFAULT_PORT="8787"
PORT="${GUARDIAN_PORT:-$DEFAULT_PORT}"
LISTEN_HOST="${GUARDIAN_LISTEN:-127.0.0.1}"
BINARY_PATH="${GUARDIAN_BINARY:-}"
GITHUB_REPO="${GUARDIAN_GITHUB_REPO:-codermyxiaoc/sub2api-guardian}"
VERSION="${GUARDIAN_VERSION:-latest}"
TMP_DIR=""
TMP_ENV=""
EXISTING_ADDR=""
PRESERVE_ADDR=0
if [ -n "${GUARDIAN_PORT:-}" ]; then
  PORT_SET=1
else
  PORT_SET=0
fi
if [ -n "${GUARDIAN_LISTEN:-}" ]; then
  LISTEN_SET=1
else
  LISTEN_SET=0
fi
if [ -n "$BINARY_PATH" ]; then
  BINARY_SET=1
else
  BINARY_SET=0
fi

cleanup() {
  if [ -n "$TMP_ENV" ]; then
    rm -f "$TMP_ENV"
  fi
  if [ -n "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup EXIT HUP INT TERM

usage() {
  cat <<'EOF'
Sub2API Guardian Linux installer

Usage:
  curl -fsSL https://raw.githubusercontent.com/codermyxiaoc/sub2api-guardian/main/install.sh | sudo bash
  sudo bash install.sh [options]

Options:
  --port PORT       Web port (default: 8787; prompts when run in a terminal)
  --listen HOST     Listen address (default: 127.0.0.1; use 0.0.0.0 carefully)
  --version VERSION Release tag to install (default: latest, for example v1.0.0)
  --repo OWNER/REPO GitHub repository (default: codermyxiaoc/sub2api-guardian)
  --binary PATH     Use a local Guardian Linux binary instead of downloading
  -h, --help        Show this help

Environment alternatives:
  GUARDIAN_PORT, GUARDIAN_LISTEN, GUARDIAN_VERSION,
  GUARDIAN_GITHUB_REPO, GUARDIAN_BINARY
EOF
}

case "${1:-}" in
  -h|--help)
    usage
    exit 0
    ;;
esac

if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null 2>&1; then
    if [ -f "$0" ]; then
      exec sudo -- sh "$0" "$@"
    fi
    echo "error: pipe this installer through 'sudo bash'" >&2
    exit 1
  fi
  echo "error: run this installer as root (or install sudo)" >&2
  exit 1
fi

while [ "$#" -gt 0 ]; do
  case "$1" in
    --port)
      [ "$#" -ge 2 ] || { echo "error: --port requires a value" >&2; exit 2; }
      PORT="$2"
      PORT_SET=1
      shift 2
      ;;
    --listen)
      [ "$#" -ge 2 ] || { echo "error: --listen requires a value" >&2; exit 2; }
      LISTEN_HOST="$2"
      LISTEN_SET=1
      shift 2
      ;;
    --binary)
      [ "$#" -ge 2 ] || { echo "error: --binary requires a value" >&2; exit 2; }
      BINARY_PATH="$2"
      BINARY_SET=1
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || { echo "error: --version requires a value" >&2; exit 2; }
      VERSION="$2"
      shift 2
      ;;
    --repo)
      [ "$#" -ge 2 ] || { echo "error: --repo requires a value" >&2; exit 2; }
      GITHUB_REPO="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "error: unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [ "$PORT_SET" -eq 0 ] && [ "$LISTEN_SET" -eq 0 ] && [ -f "$ENV_FILE" ]; then
  EXISTING_ADDR=$(awk '/^GUARDIAN_ADDR=/ { sub(/^[^=]*=/, ""); value=$0 } END { print value }' "$ENV_FILE")
  if [ -n "$EXISTING_ADDR" ]; then
    PRESERVE_ADDR=1
  fi
fi

if [ "$PORT_SET" -eq 0 ] && [ "$PRESERVE_ADDR" -eq 0 ] && [ -t 0 ]; then
  printf 'Guardian port [%s]: ' "$PORT"
  read -r INPUT_PORT
  if [ -n "$INPUT_PORT" ]; then
    PORT="$INPUT_PORT"
  fi
fi

case "$PORT" in
  ''|*[!0-9]*)
    echo "error: port must be an integer between 1 and 65535" >&2
    exit 2
    ;;
esac
if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
  echo "error: port must be between 1 and 65535" >&2
  exit 2
fi
case "$LISTEN_HOST" in
  ''|*[[:space:]]*|*/*)
    echo "error: invalid listen address: $LISTEN_HOST" >&2
    exit 2
    ;;
esac
if [ "$LISTEN_HOST" = "::" ]; then
  LISTEN_HOST="[::]"
fi

command -v systemctl >/dev/null 2>&1 || {
  echo "error: systemd is required for this installer" >&2
  exit 1
}

case "$GITHUB_REPO" in
  */*/*|/*|*/|'')
    echo "error: GitHub repository must use OWNER/REPO format" >&2
    exit 2
    ;;
  */*) ;;
  *)
    echo "error: GitHub repository must use OWNER/REPO format" >&2
    exit 2
    ;;
esac
case "$GITHUB_REPO$VERSION" in
  *[[:space:]]*)
    echo "error: repository and version cannot contain whitespace" >&2
    exit 2
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "error: unsupported architecture: $(uname -m); only amd64 and arm64 are supported" >&2
    exit 1
    ;;
esac
ASSET="guardian-linux-$ARCH"

download_file() {
  DOWNLOAD_URL="$1"
  DOWNLOAD_TARGET="$2"
  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --silent --show-error --retry 3 --connect-timeout 15 \
      --output "$DOWNLOAD_TARGET" "$DOWNLOAD_URL"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --tries=3 --timeout=30 -O "$DOWNLOAD_TARGET" "$DOWNLOAD_URL"
  else
    echo "error: curl or wget is required to download Guardian" >&2
    return 1
  fi
}

download_optional() {
  DOWNLOAD_URL="$1"
  DOWNLOAD_TARGET="$2"
  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --silent --retry 2 --connect-timeout 10 \
      --output "$DOWNLOAD_TARGET" "$DOWNLOAD_URL" 2>/dev/null
  elif command -v wget >/dev/null 2>&1; then
    wget -q --tries=2 --timeout=20 -O "$DOWNLOAD_TARGET" "$DOWNLOAD_URL" 2>/dev/null
  else
    return 1
  fi
}

SCRIPT_DIR=""
case "$0" in
  */*) SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) ;;
esac
if [ -z "$BINARY_PATH" ] && [ -n "$SCRIPT_DIR" ]; then
  for CANDIDATE in \
    "$SCRIPT_DIR/dist/$ASSET" \
    "$SCRIPT_DIR/$ASSET" \
    "$SCRIPT_DIR/guardian"; do
    if [ -f "$CANDIDATE" ]; then
      BINARY_PATH="$CANDIDATE"
      break
    fi
  done
fi

if [ "$BINARY_SET" -eq 1 ] && [ ! -f "$BINARY_PATH" ]; then
  echo "error: local Guardian binary not found: $BINARY_PATH" >&2
  exit 1
fi

if [ ! -f "$BINARY_PATH" ]; then
  TMP_DIR=$(mktemp -d)
  BINARY_PATH="$TMP_DIR/$ASSET"
  if [ "$VERSION" = "latest" ]; then
    RELEASE_BASE="https://github.com/$GITHUB_REPO/releases/latest/download"
  else
    RELEASE_BASE="https://github.com/$GITHUB_REPO/releases/download/$VERSION"
  fi

  echo "Downloading $GITHUB_REPO $VERSION ($ARCH)..."
  if ! download_file "$RELEASE_BASE/$ASSET" "$BINARY_PATH"; then
    echo "error: failed to download $RELEASE_BASE/$ASSET" >&2
    echo "Make sure the GitHub Release contains an asset named $ASSET." >&2
    exit 1
  fi

  CHECKSUM_FILE="$TMP_DIR/checksums.txt"
  if download_optional "$RELEASE_BASE/checksums.txt" "$CHECKSUM_FILE"; then
    if command -v sha256sum >/dev/null 2>&1; then
      EXPECTED_SUM=$(awk -v name="$ASSET" '$2 == name || $2 == "*" name { print $1; exit }' "$CHECKSUM_FILE")
      if [ -z "$EXPECTED_SUM" ]; then
        echo "error: checksums.txt does not contain $ASSET" >&2
        exit 1
      fi
      ACTUAL_SUM=$(sha256sum "$BINARY_PATH" | awk '{print $1}')
      if [ "$ACTUAL_SUM" != "$EXPECTED_SUM" ]; then
        echo "error: SHA-256 verification failed for $ASSET" >&2
        exit 1
      fi
      echo "SHA-256 verified."
    else
      echo "warning: sha256sum is unavailable; skipping checksum verification" >&2
    fi
  else
    echo "warning: release has no checksums.txt; skipping checksum verification" >&2
  fi
fi

ELF_MAGIC=$(od -An -tx1 -N4 "$BINARY_PATH" 2>/dev/null | tr -d '[:space:]')
if [ "$ELF_MAGIC" != "7f454c46" ]; then
  echo "error: downloaded file is not a Linux ELF executable" >&2
  exit 1
fi

if ! getent group "$SERVICE_USER" >/dev/null 2>&1; then
  groupadd --system "$SERVICE_USER"
fi
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  NOLOGIN=$(command -v nologin || printf '%s' /usr/sbin/nologin)
  useradd --system --gid "$SERVICE_USER" --home-dir "$DATA_DIR" --shell "$NOLOGIN" "$SERVICE_USER"
fi

install -d -m 0755 "$INSTALL_DIR"
install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0700 "$DATA_DIR"
install -m 0755 "$BINARY_PATH" "$INSTALL_DIR/guardian.new"
mv -f "$INSTALL_DIR/guardian.new" "$INSTALL_DIR/guardian"

TMP_ENV=$(mktemp)
if [ -f "$ENV_FILE" ]; then
  awk '!/^GUARDIAN_ADDR=/ && !/^GUARDIAN_DATA_DIR=/' "$ENV_FILE" > "$TMP_ENV"
fi
{
  if [ "$PRESERVE_ADDR" -eq 1 ]; then
    printf 'GUARDIAN_ADDR=%s\n' "$EXISTING_ADDR"
  else
    printf 'GUARDIAN_ADDR=%s:%s\n' "$LISTEN_HOST" "$PORT"
  fi
  printf 'GUARDIAN_DATA_DIR=%s\n' "$DATA_DIR"
} >> "$TMP_ENV"
install -m 0600 "$TMP_ENV" "$ENV_FILE"

cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=Sub2API Guardian
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
EnvironmentFile=$ENV_FILE
ExecStart=$INSTALL_DIR/guardian
WorkingDirectory=$DATA_DIR
Restart=on-failure
RestartSec=5s
TimeoutStopSec=15s
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectHome=true
ProtectSystem=strict
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
ReadWritePaths=$DATA_DIR

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "$SERVICE_NAME" >/dev/null
systemctl restart "$SERVICE_NAME"

if ! systemctl is-active --quiet "$SERVICE_NAME"; then
  echo "error: service failed to start" >&2
  journalctl -u "$SERVICE_NAME" -n 30 --no-pager >&2 || true
  exit 1
fi

echo
echo "Sub2API Guardian installed successfully."
if [ "$PRESERVE_ADDR" -eq 1 ]; then
  FINAL_ADDR="$EXISTING_ADDR"
  echo "Listen:  $FINAL_ADDR (preserved)"
else
  FINAL_ADDR="$LISTEN_HOST:$PORT"
  echo "Listen:  $FINAL_ADDR"
fi
echo "Service: systemctl status $SERVICE_NAME"
echo "Logs:    journalctl -u $SERVICE_NAME -f"
echo "Data:    $DATA_DIR"
case "$FINAL_ADDR" in
  0.0.0.0:*|'[::]':*)
    echo "Warning: Guardian is listening on all interfaces. Restrict firewall access and use HTTPS."
    ;;
esac
