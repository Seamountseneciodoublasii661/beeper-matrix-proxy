#!/bin/zsh
set -euo pipefail

export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export CGO_CFLAGS="-I/opt/homebrew/opt/libolm/include"
export CGO_LDFLAGS="-L/opt/homebrew/opt/libolm/lib -lolm"
export LOCAL_MATRIX_INITIAL_BACKFILL_LIMIT="${LOCAL_MATRIX_INITIAL_BACKFILL_LIMIT:-0}"
# Synapse advertises a very high media limit, but the HTTPS/proxy path currently
# returns HTTP 413 for larger uploads. Keep Beeper's room_features conservative.
export LOCAL_MATRIX_MAX_UPLOAD_SIZE="${LOCAL_MATRIX_MAX_UPLOAD_SIZE:-4194304}"

cd /Users/mh/Documents/Playground/sh-vcvm-matrix-bridgev2-src
exec /Users/mh/.local/bin/bbctl run \
  --type bridgev2 \
  --local-dev \
  --no-override-config \
  --custom-startup-command /Users/mh/Documents/Playground/sh-vcvm-matrix-bridgev2-src/sh-vcvm-matrix \
  sh-vcvm-matrix
