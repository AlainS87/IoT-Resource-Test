#!/usr/bin/env bash
set -euo pipefail

MOSQ_CONF="${MOSQ_CONF:-/etc/mosquitto/mosquitto.conf}"
BROKER_LISTEN="${BROKER_LISTEN:-1883}"
APP_BIN="${APP_BIN:-/root/app/main}"

# 1) Ensure a mosquitto config exists (dev-friendly default)
if [ ! -f "$MOSQ_CONF" ]; then
  echo "[wrapper] No mosquitto.conf found, writing a permissive dev config to $MOSQ_CONF"
  mkdir -p "$(dirname "$MOSQ_CONF")"
  cat > "$MOSQ_CONF" <<EOF
listener ${BROKER_LISTEN} 0.0.0.0
allow_anonymous true
persistence true
persistence_location /var/lib/mosquitto/
EOF
fi

# 2) Start mosquitto in background
echo "[wrapper] Starting mosquitto with $MOSQ_CONF"
mkdir -p /var/lib/mosquitto
mosquitto -c "$MOSQ_CONF" -v &
MOSQ_PID=$!

# Cleanup function on exit
term() {
  echo "[wrapper] Caught signal, stopping mosquitto (pid=$MOSQ_PID)"
  kill -TERM "$MOSQ_PID" 2>/dev/null || true
  wait "$MOSQ_PID" 2>/dev/null || true
}
trap term SIGINT SIGTERM

# 3) Wait for broker port to be ready
echo "[wrapper] Waiting for broker to accept connections on 127.0.0.1:${BROKER_LISTEN} ..."
for i in $(seq 1 50); do
  if bash -c ":</dev/tcp/127.0.0.1/${BROKER_LISTEN}" 2>/dev/null; then
    echo "[wrapper] Broker is ready."
    break
  fi
  sleep 0.2
  if ! kill -0 "$MOSQ_PID" 2>/dev/null; then
    echo "[wrapper] mosquitto exited unexpectedly." >&2
    exit 1
  fi
  if [ "$i" -eq 50 ]; then
    echo "[wrapper] Timeout waiting for broker." >&2
    exit 1
  fi
done

# 4) Export local broker URL for the Go app (if not already set)
export BROKER_URL="${BROKER_URL:-tcp://127.0.0.1:${BROKER_LISTEN}}"
echo "[wrapper] BROKER_URL=$BROKER_URL"

# 5) Run the Go controller (exec to make it PID 1 child of tini/systemd)
echo "[wrapper] Starting app: $APP_BIN $*"
exec "$APP_BIN" "$@"
