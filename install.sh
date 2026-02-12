#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "Run this installer as root (sudo)." >&2
  exit 1
fi

if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemctl not found. This installer requires systemd." >&2
  exit 1
fi

need_cmd() {
  if command -v "$1" >/dev/null 2>&1; then
    return 0
  fi
  return 1
}

prompt() {
  local var="$1"
  local text="$2"
  local def="${3-}"
  local input
  if [[ -n "$def" ]]; then
    read -r -p "$text [$def]: " input || true
    if [[ -z "$input" ]]; then
      input="$def"
    fi
  else
    read -r -p "$text: " input || true
  fi
  printf -v "$var" '%s' "$input"
}

escape_env() {
  local s="$1"
  s=${s//\\/\\\\}
  s=${s//"/\\"}
  printf '"%s"' "$s"
}

echo "Server Monitoring installer"

BINARY_URL="${BINARY_URL:-}"
if [[ -z "$BINARY_URL" ]]; then
  prompt BINARY_URL "Binary URL (linux/amd64 или linux/arm64)" ""
fi
if [[ -z "$BINARY_URL" ]]; then
  echo "Binary URL is required." >&2
  exit 1
fi

API_URL_DEFAULT="https://acmen.ru/api/v1/telegram/"
ENV_PATH_DEFAULT="/etc/server-monitoring.env"

prompt API_URL "API URL" "$API_URL_DEFAULT"
prompt API_TOKEN "API token" ""
prompt CHAT_ID "Chat ID" ""
prompt MESSAGE_THREAD_ID "Message thread id (optional)" ""

prompt CPU_THRESHOLD "CPU threshold %" "80"
prompt RAM_THRESHOLD "RAM threshold %" "80"
prompt DISK_THRESHOLD "Disk threshold %" "80"

prompt ENV_PATH "Env file path" "$ENV_PATH_DEFAULT"

if [[ -z "$API_TOKEN" || -z "$CHAT_ID" ]]; then
  echo "API_TOKEN and CHAT_ID are required." >&2
  exit 1
fi

TMP_BIN="$(mktemp)"
if need_cmd curl; then
  curl -fsSL "$BINARY_URL" -o "$TMP_BIN"
elif need_cmd wget; then
  wget -qO "$TMP_BIN" "$BINARY_URL"
else
  echo "curl or wget is required to download the binary." >&2
  exit 1
fi

install -m 0755 "$TMP_BIN" /usr/local/bin/server-monitoring
rm -f "$TMP_BIN"

mkdir -p "$(dirname "$ENV_PATH")"
cat > "$ENV_PATH" <<__ENV__
API_URL=$(escape_env "$API_URL")
API_TOKEN=$(escape_env "$API_TOKEN")
CHAT_ID=$(escape_env "$CHAT_ID")
MESSAGE_THREAD_ID=$(escape_env "$MESSAGE_THREAD_ID")

CPU_THRESHOLD=$(escape_env "$CPU_THRESHOLD")
RAM_THRESHOLD=$(escape_env "$RAM_THRESHOLD")
DISK_THRESHOLD=$(escape_env "$DISK_THRESHOLD")
__ENV__

cat > /etc/systemd/system/server-monitoring.service <<__SERVICE__
[Unit]
Description=Server Monitoring (CPU/RAM/Disk)
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
EnvironmentFile=$ENV_PATH
ExecStart=/usr/local/bin/server-monitoring
User=root
Group=root
__SERVICE__

cat > /etc/systemd/system/server-monitoring.timer <<'__TIMER__'
[Unit]
Description=Run server-monitoring every minute

[Timer]
OnCalendar=*-*-* *:*:00
AccuracySec=1s
Persistent=true

[Install]
WantedBy=timers.target
__TIMER__

systemctl daemon-reload
systemctl enable --now server-monitoring.timer

systemctl status server-monitoring.timer --no-pager || true

echo "Done. Logs: journalctl -u server-monitoring.service -f"
