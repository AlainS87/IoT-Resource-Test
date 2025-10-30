#!/usr/bin/env bash
set -euo pipefail

# -----------------------------
# Config (env with sane defaults)
# -----------------------------
APP_BIN="${APP_BIN:-/root/app/main}"                 # subscriber binary path
BROKER="${BROKER:-tcp://127.0.0.1:1883}"             # satellite broker URL
CLIENT_ID="${CLIENT_ID:-marine_subscriber}"          # MQTT client id
SUB_TOPIC="${SUB_TOPIC:-buoy_sensors_data_prediction}" # (for logging only; code has it hardcoded)
SAVE_DIR="${SAVE_DIR:-/root/bin/msg_box}"            # your subscriber writes here (hardcoded in code)
EXTRA_ARGS="${EXTRA_ARGS:-}"                         # additional flags for your binary

# -----------------------------
# Helpers
# -----------------------------
log() { echo "[subscriber-wrapper] $*"; }

# Parse "tcp://host:port" -> host port
parse_broker() {
  local url="$1"
  local hp="${url#*://}"           # strip scheme
  local host="${hp%%:*}"           # before ':'
  local port="${hp##*:}"           # after  ':'
  if [[ "$host" == "$port" ]]; then
    port="1883"
  fi
  echo "$host" "$port"
}

wait_for_broker() {
  local host="$1" port="$2" tries="${3:-50}" delay="${4:-0.2}"
  log "Waiting for broker ${host}:${port} ..."
  for i in $(seq 1 "$tries"); do
    if bash -c ":</dev/tcp/${host}/${port}" 2>/dev/null; then
      log "Broker is ready."
      return 0
    fi
    sleep "$delay"
  done
  log "Timeout waiting for broker ${host}:${port}"
  return 1
}

# -----------------------------
# Pre-flight
# -----------------------------
if [[ ! -x "$APP_BIN" ]]; then
  log "ERROR: APP_BIN not executable: $APP_BIN"
  exit 1
fi

# Ensure save dir exists (subscriber writes here)
mkdir -p "$SAVE_DIR"

read -r BROKER_HOST BROKER_PORT < <(parse_broker "$BROKER")

log "Config:"
log "  APP_BIN     = $APP_BIN"
log "  BROKER      = $BROKER  (host=$BROKER_HOST port=$BROKER_PORT)"
log "  CLIENT_ID   = $CLIENT_ID"
log "  SUB_TOPIC   = $SUB_TOPIC   # for log only; code uses internal default"
log "  SAVE_DIR    = $SAVE_DIR"
[[ -n "$EXTRA_ARGS" ]] && log "  EXTRA_ARGS  = $EXTRA_ARGS"

# -----------------------------
# Wait for the satellite broker
# -----------------------------
wait_for_broker "$BROKER_HOST" "$BROKER_PORT" 50 0.2

# -----------------------------
# Run subscriber
# -----------------------------
term() {
  log "Caught signal, forwarding to subscriber..."
  kill -TERM "$SUB_PID" 2>/dev/null || true
  wait "$SUB_PID" 2>/dev/null || true
}
trap term SIGINT SIGTERM

# Your subscriber supports --client_id and --broker (per the modified code).
# It hardcodes sub-topic & saveDir internally; we only ensure SAVE_DIR exists.
log "Starting subscriber..."
"$APP_BIN" \
  --client_id "$CLIENT_ID" \
  --broker "$BROKER" \
  $EXTRA_ARGS &
SUB_PID=$!

wait "$SUB_PID"
EXIT_CODE=$?
log "Subscriber exited with code $EXIT_CODE"
exit "$EXIT_CODE"
