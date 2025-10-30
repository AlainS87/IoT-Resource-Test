#!/usr/bin/env bash
set -euo pipefail

# -----------------------------
# Configuration (env with defaults)
# -----------------------------
APP_BIN="${APP_BIN:-/root/app/main}"             # path to publisher binary
BROKER="${BROKER:-tcp://127.0.0.1:1883}"         # satelite's embedded broker
BASE_FOLDER="${BASE_FOLDER:-/root/app/sample_msg}" # folder mounted with buoy folders
INTERVAL="${INTERVAL:-1}"                        # seconds between each file per buoy
CLIENT_ID="${CLIENT_ID:-EOS_publisher}"          # base client id (publisher will append _<buoy>)
EXTRA_ARGS="${EXTRA_ARGS:-}"                     # any extra flags to pass through

# -----------------------------
# Helpers
# -----------------------------
log() { echo "[publisher-wrapper] $*"; }

# Parse BROKER like tcp://host:port -> host, port
parse_broker() {
  local url="$1"
  # strip scheme
  local hp="${url#*://}"     # host:port or host
  local host="${hp%%:*}"     # before ':'
  local port="${hp##*:}"     # after  ':'
  if [[ "$host" == "$port" ]]; then
    port="1883"
  fi
  echo "$host" "$port"
}

wait_for_broker() {
  local host="$1" port="$2" tries="${3:-50}" delay="${4:-0.2}"
  log "Waiting for broker $host:$port ..."
  for i in $(seq 1 "$tries"); do
    if bash -c ":</dev/tcp/${host}/${port}" 2>/dev/null; then
      log "Broker is ready."
      return 0
    fi
    sleep "$delay"
  done
  log "Timeout waiting for broker $host:$port"
  return 1
}

# -----------------------------
# Pre-flight checks
# -----------------------------
if [[ ! -x "$APP_BIN" ]]; then
  log "ERROR: APP_BIN not executable: $APP_BIN"
  exit 1
fi

if [[ ! -d "$BASE_FOLDER" ]]; then
  log "ERROR: BASE_FOLDER does not exist: $BASE_FOLDER"
  exit 1
fi

read -r BROKER_HOST BROKER_PORT < <(parse_broker "$BROKER")

log "Config:"
log "  APP_BIN     = $APP_BIN"
log "  BROKER      = $BROKER  (host=$BROKER_HOST port=$BROKER_PORT)"
log "  BASE_FOLDER = $BASE_FOLDER"
log "  INTERVAL    = $INTERVAL"
log "  CLIENT_ID   = $CLIENT_ID"
[[ -n "$EXTRA_ARGS" ]] && log "  EXTRA_ARGS  = $EXTRA_ARGS"

# -----------------------------
# Wait for satellite broker
# -----------------------------
wait_for_broker "$BROKER_HOST" "$BROKER_PORT" 50 0.2

# -----------------------------
# Run publisher
# -----------------------------
term() {
  log "Caught signal, forwarding to publisher..."
  kill -TERM "$PUB_PID" 2>/dev/null || true
  wait "$PUB_PID" 2>/dev/null || true
}
trap term SIGINT SIGTERM

log "Starting publisher..."
# Pass flags the publisher expects; keep logs consistent with your program
"$APP_BIN" \
  --base_folder "$BASE_FOLDER" \
  --interval "$INTERVAL" \
  --client_id "$CLIENT_ID" \
  --broker "$BROKER" \
  $EXTRA_ARGS &
PUB_PID=$!

wait "$PUB_PID"
EXIT_CODE=$?
log "Publisher exited with code $EXIT_CODE"
exit "$EXIT_CODE"
