#!/bin/zsh
set -euo pipefail

export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export CGO_CFLAGS="-I/opt/homebrew/opt/libolm/include"
export CGO_LDFLAGS="-L/opt/homebrew/opt/libolm/lib -lolm"
export LOCAL_MATRIX_INITIAL_BACKFILL_LIMIT="${LOCAL_MATRIX_INITIAL_BACKFILL_LIMIT:-0}"
export BEEPER_MATRIX_PROXY_DIR="${BEEPER_MATRIX_PROXY_DIR:-$PWD}"
export BEEPER_MATRIX_PROXY_BINARY="${BEEPER_MATRIX_PROXY_BINARY:-$BEEPER_MATRIX_PROXY_DIR/beeper-matrix-proxy}"
export BEEPER_BRIDGE_NAME="${BEEPER_BRIDGE_NAME:-sh-vcvm-matrix}"
export BEEPER_BBCTL="${BEEPER_BBCTL:-bbctl}"
# Synapse advertises a very high media limit, but the HTTPS/proxy path currently
# returns HTTP 413 for larger uploads. Keep Beeper's room_features conservative.
export LOCAL_MATRIX_MAX_UPLOAD_SIZE="${LOCAL_MATRIX_MAX_UPLOAD_SIZE:-4194304}"

cd "$BEEPER_MATRIX_PROXY_DIR"
exec "$BEEPER_BBCTL" run \
  --type bridgev2 \
  --local-dev \
  --no-override-config \
  --custom-startup-command "$BEEPER_MATRIX_PROXY_BINARY" \
  "$BEEPER_BRIDGE_NAME"
