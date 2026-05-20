#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE="${SYNAPSE_IMAGE:-matrixdotorg/synapse:latest}"
SERVER_COUNT="${LOCAL_SYNAPSE_E2E_SERVER_COUNT:-1}"
SERVER_NAME_PREFIX="${SYNAPSE_SERVER_NAME_PREFIX:-localhost}"
BURST="${LOCAL_SYNAPSE_E2E_BURST:-40}"
BURSTS="${LOCAL_SYNAPSE_E2E_BURSTS:-$BURST}"
SYNC_TIMELINE_LIMIT="${LOCAL_MATRIX_SYNC_TIMELINE_LIMIT:-}"
RUN_REGEX="${LOCAL_SYNAPSE_E2E_RUN:-TestSynapse.*E2E}"

CONTAINERS=()
DATA_DIRS=()
PORTS=()
USER_IDS=()
ACCESS_TOKENS=()
PEER_USER_IDS=()
PEER_ACCESS_TOKENS=()

free_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

json_field() {
  local field="$1"
  python3 -c 'import json,sys; print(json.load(sys.stdin)[sys.argv[1]])' "$field"
}

cleanup() {
  for container in "${CONTAINERS[@]:-}"; do
    docker rm -f "$container" >/dev/null 2>&1 || true
  done
  if [[ "${LOCAL_SYNAPSE_E2E_KEEP_DATA:-0}" != "1" ]]; then
    for data_dir in "${DATA_DIRS[@]:-}"; do
      rm -rf "$data_dir"
    done
  fi
}
trap cleanup EXIT

register_user() {
  local container="$1"
  local port="$2"
  local username="$3"
  local password="$4"
  local device="$5"

  docker exec "$container" register_new_matrix_user \
    -u "$username" \
    -p "$password" \
    -a \
    -c /data/homeserver.yaml \
    "http://localhost:8008" >/dev/null

  curl -fsS -X POST "http://127.0.0.1:$port/_matrix/client/v3/login" \
    -H 'Content-Type: application/json' \
    --data "{\"type\":\"m.login.password\",\"identifier\":{\"type\":\"m.id.user\",\"user\":\"$username\"},\"password\":\"$password\",\"device_id\":\"$device\"}"
}

start_synapse() {
  local index="$1"
  local server_name="${SERVER_NAME_PREFIX}-${index}"
  local port="${LOCAL_SYNAPSE_E2E_PORT:-}"
  local data_dir="${LOCAL_SYNAPSE_E2E_DATA_DIR:-}"
  local container="${LOCAL_SYNAPSE_E2E_CONTAINER:-beeper-matrix-proxy-synapse-${index}-$$}"

  if [[ "$SERVER_COUNT" != "1" || -z "$port" ]]; then
    port="$(free_port)"
  fi
  if [[ "$SERVER_COUNT" != "1" || -z "$data_dir" ]]; then
    data_dir="$(mktemp -d "${TMPDIR:-/tmp}/beeper-matrix-proxy-synapse-${index}.XXXXXX")"
  fi

  CONTAINERS+=("$container")
  DATA_DIRS+=("$data_dir")
  PORTS+=("$port")

  echo "==> Generating Synapse config for server $index in $data_dir"
  docker run --rm \
    -v "$data_dir:/data" \
    -e "SYNAPSE_SERVER_NAME=$server_name" \
    -e "SYNAPSE_REPORT_STATS=no" \
    "$IMAGE" generate >/dev/null

  cat >>"$data_dir/homeserver.yaml" <<'YAML'

registration_shared_secret: "beeper-matrix-proxy-test-secret"
enable_registration: false
rc_message:
  per_second: 1000
  burst_count: 1000
rc_room_creation:
  per_second: 100
  burst_count: 100
YAML

  echo "==> Starting Synapse server $index on http://127.0.0.1:$port"
  docker run -d --name "$container" \
    -v "$data_dir:/data" \
    -p "127.0.0.1:$port:8008" \
    "$IMAGE" >/dev/null

  for _ in {1..120}; do
    if curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  curl -fsS "http://127.0.0.1:$port/_matrix/client/versions" >/dev/null

  echo "==> Registering primary and peer users on server $index"
  local primary_login
  local peer_login
  primary_login="$(register_user "$container" "$port" "proxy$index" "proxy-pass" "BEEPER_PROXY_E2E_$index")"
  peer_login="$(register_user "$container" "$port" "peer$index" "proxy-pass" "BEEPER_PROXY_PEER_E2E_$index")"
  ACCESS_TOKENS+=("$(printf '%s' "$primary_login" | json_field access_token)")
  USER_IDS+=("$(printf '%s' "$primary_login" | json_field user_id)")
  PEER_ACCESS_TOKENS+=("$(printf '%s' "$peer_login" | json_field access_token)")
  PEER_USER_IDS+=("$(printf '%s' "$peer_login" | json_field user_id)")
}

compute_sync_limit() {
  if [[ -n "$SYNC_TIMELINE_LIMIT" ]]; then
    printf '%s\n' "$SYNC_TIMELINE_LIMIT"
    return
  fi
  local limit=100
  IFS=',' read -r -a burst_list <<< "$BURSTS"
  for burst_value in "${burst_list[@]}"; do
    burst_value="$(printf '%s' "$burst_value" | xargs)"
    [[ -n "$burst_value" ]] || continue
    if (( burst_value > limit )); then
      limit="$burst_value"
    fi
  done
  printf '%s\n' "$limit"
}

run_e2e_for_server() {
  local index="$1"
  local array_index=$((index - 1))
  local port="${PORTS[$array_index]}"
  local user_id="${USER_IDS[$array_index]}"
  local access_token="${ACCESS_TOKENS[$array_index]}"
  local peer_user_id="${PEER_USER_IDS[$array_index]}"
  local peer_access_token="${PEER_ACCESS_TOKENS[$array_index]}"
  local sync_limit
  sync_limit="$(compute_sync_limit)"

  echo "==> Running Go Synapse E2E matrix against server $index"
  (
    cd "$ROOT"
    LOCAL_SYNAPSE_E2E_SERVER_INDEX="$index" \
    LOCAL_SYNAPSE_E2E_HS="http://127.0.0.1:$port" \
    LOCAL_SYNAPSE_E2E_USER_ID="$user_id" \
    LOCAL_SYNAPSE_E2E_ACCESS_TOKEN="$access_token" \
    LOCAL_SYNAPSE_E2E_PEER_USER_ID="$peer_user_id" \
    LOCAL_SYNAPSE_E2E_PEER_ACCESS_TOKEN="$peer_access_token" \
    LOCAL_SYNAPSE_E2E_BURSTS="$BURSTS" \
    LOCAL_MATRIX_SYNC_TIMELINE_LIMIT="$sync_limit" \
    CGO_CFLAGS="${CGO_CFLAGS:-"-I/opt/homebrew/opt/libolm/include"}" \
    CGO_LDFLAGS="${CGO_LDFLAGS:-"-L/opt/homebrew/opt/libolm/lib -lolm"}" \
    go test -tags synapse_e2e ./connector -run "$RUN_REGEX" -count=1 -v
  )
}

for index in $(seq 1 "$SERVER_COUNT"); do
  start_synapse "$index"
done

for index in $(seq 1 "$SERVER_COUNT"); do
  run_e2e_for_server "$index"
done
