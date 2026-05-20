#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE="${SYNAPSE_IMAGE:-matrixdotorg/synapse:latest}"
SERVER_NAME="${SYNAPSE_SERVER_NAME:-localhost}"
BURST="${LOCAL_SYNAPSE_E2E_BURST:-40}"
BURSTS="${LOCAL_SYNAPSE_E2E_BURSTS:-$BURST}"
SYNC_TIMELINE_LIMIT="${LOCAL_MATRIX_SYNC_TIMELINE_LIMIT:-}"
PORT="${LOCAL_SYNAPSE_E2E_PORT:-}"
DATA_DIR="${LOCAL_SYNAPSE_E2E_DATA_DIR:-}"
CONTAINER="${LOCAL_SYNAPSE_E2E_CONTAINER:-beeper-matrix-proxy-synapse-$$}"

if [[ -z "$PORT" ]]; then
  PORT="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
fi

if [[ -z "$DATA_DIR" ]]; then
  DATA_DIR="$(mktemp -d "${TMPDIR:-/tmp}/beeper-matrix-proxy-synapse.XXXXXX")"
fi

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  if [[ "${LOCAL_SYNAPSE_E2E_KEEP_DATA:-0}" != "1" ]]; then
    rm -rf "$DATA_DIR"
  fi
}
trap cleanup EXIT

echo "==> Generating Synapse config in $DATA_DIR"
docker run --rm \
  -v "$DATA_DIR:/data" \
  -e "SYNAPSE_SERVER_NAME=$SERVER_NAME" \
  -e "SYNAPSE_REPORT_STATS=no" \
  "$IMAGE" generate >/dev/null

cat >>"$DATA_DIR/homeserver.yaml" <<'YAML'

registration_shared_secret: "beeper-matrix-proxy-test-secret"
enable_registration: false
rc_message:
  per_second: 1000
  burst_count: 1000
rc_room_creation:
  per_second: 100
  burst_count: 100
YAML

echo "==> Starting Synapse on http://127.0.0.1:$PORT"
docker run -d --name "$CONTAINER" \
  -v "$DATA_DIR:/data" \
  -p "127.0.0.1:$PORT:8008" \
  "$IMAGE" >/dev/null

for _ in {1..120}; do
  if curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -fsS "http://127.0.0.1:$PORT/_matrix/client/versions" >/dev/null

echo "==> Registering test user"
docker exec "$CONTAINER" register_new_matrix_user \
  -u proxy \
  -p proxy-pass \
  -a \
  -c /data/homeserver.yaml \
  "http://localhost:8008" >/dev/null

LOGIN_JSON="$(
  curl -fsS -X POST "http://127.0.0.1:$PORT/_matrix/client/v3/login" \
    -H 'Content-Type: application/json' \
    --data '{"type":"m.login.password","identifier":{"type":"m.id.user","user":"proxy"},"password":"proxy-pass","device_id":"BEEPER_PROXY_E2E"}'
)"
ACCESS_TOKEN="$(printf '%s' "$LOGIN_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')"
USER_ID="$(printf '%s' "$LOGIN_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["user_id"])')"

IFS=',' read -r -a BURST_LIST <<< "$BURSTS"
if [[ -z "$SYNC_TIMELINE_LIMIT" ]]; then
  SYNC_TIMELINE_LIMIT=100
  for burst_value in "${BURST_LIST[@]}"; do
    burst_value="$(printf '%s' "$burst_value" | xargs)"
    [[ -n "$burst_value" ]] || continue
    if (( burst_value > SYNC_TIMELINE_LIMIT )); then
      SYNC_TIMELINE_LIMIT="$burst_value"
    fi
  done
fi
for burst_value in "${BURST_LIST[@]}"; do
  burst_value="$(printf '%s' "$burst_value" | xargs)"
  [[ -n "$burst_value" ]] || continue
  echo "==> Running Go Synapse E2E burst test with $burst_value messages"
  (
    cd "$ROOT"
    LOCAL_SYNAPSE_E2E_HS="http://127.0.0.1:$PORT" \
    LOCAL_SYNAPSE_E2E_USER_ID="$USER_ID" \
    LOCAL_SYNAPSE_E2E_ACCESS_TOKEN="$ACCESS_TOKEN" \
    LOCAL_SYNAPSE_E2E_BURST="$burst_value" \
    LOCAL_MATRIX_SYNC_TIMELINE_LIMIT="$SYNC_TIMELINE_LIMIT" \
    CGO_CFLAGS="${CGO_CFLAGS:-"-I/opt/homebrew/opt/libolm/include"}" \
    CGO_LDFLAGS="${CGO_LDFLAGS:-"-L/opt/homebrew/opt/libolm/lib -lolm"}" \
    go test -tags synapse_e2e ./connector -run TestSynapseBurstSyncE2E -count=1 -v
  )
done

if [[ "${LOCAL_SYNAPSE_E2E_MODALITIES:-1}" == "1" ]]; then
  echo "==> Running Go Synapse E2E mixed modality test"
  (
    cd "$ROOT"
    LOCAL_SYNAPSE_E2E_HS="http://127.0.0.1:$PORT" \
    LOCAL_SYNAPSE_E2E_USER_ID="$USER_ID" \
    LOCAL_SYNAPSE_E2E_ACCESS_TOKEN="$ACCESS_TOKEN" \
    LOCAL_MATRIX_SYNC_TIMELINE_LIMIT="$SYNC_TIMELINE_LIMIT" \
    CGO_CFLAGS="${CGO_CFLAGS:-"-I/opt/homebrew/opt/libolm/include"}" \
    CGO_LDFLAGS="${CGO_LDFLAGS:-"-L/opt/homebrew/opt/libolm/lib -lolm"}" \
    go test -tags synapse_e2e ./connector -run TestSynapseMixedModalitySyncE2E -count=1 -v
  )
fi
