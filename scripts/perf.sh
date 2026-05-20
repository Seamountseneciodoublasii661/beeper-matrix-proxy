#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BENCH_COUNT="${BENCH_COUNT:-5}"

cd "$ROOT"

echo "==> Go hot-path benchmarks"
CGO_CFLAGS="${CGO_CFLAGS:-"-I/opt/homebrew/opt/libolm/include"}" \
CGO_LDFLAGS="${CGO_LDFLAGS:-"-L/opt/homebrew/opt/libolm/lib -lolm"}" \
go test ./connector \
  -run '^$' \
  -bench 'BenchmarkCloneMessageContent' \
  -benchmem \
  -count "$BENCH_COUNT"

if [[ "${RUN_SYNAPSE_E2E:-0}" == "1" ]]; then
  echo
  echo "==> Synapse E2E performance test"
  "$ROOT/e2e/synapse/run.sh"
else
  echo
  echo "Set RUN_SYNAPSE_E2E=1 to also start a local Synapse and run the burst sync E2E test."
fi
