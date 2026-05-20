#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BENCH_COUNT="${BENCH_COUNT:-5}"
BENCH_REGEX="${BENCH_REGEX:-Benchmark(CloneMessageContent|GeneratedFallbackAvatarFromMXC|CloneRawMap)}"
RESULTS_DIR="${PERF_RESULTS_DIR:-"$ROOT/perf-results/$(date -u +%Y%m%dT%H%M%SZ)"}"

cd "$ROOT"
mkdir -p "$RESULTS_DIR"

python3 "$ROOT/scripts/perf_metrics.py" metadata \
  "$RESULTS_DIR/metadata.json" \
  --bench-count "$BENCH_COUNT" \
  --bench-regex "$BENCH_REGEX"

echo "==> Go hot-path benchmarks"
CGO_CFLAGS="${CGO_CFLAGS:-"-I/opt/homebrew/opt/libolm/include"}" \
CGO_LDFLAGS="${CGO_LDFLAGS:-"-L/opt/homebrew/opt/libolm/lib -lolm"}" \
go test ./connector \
  -run '^$' \
  -bench "$BENCH_REGEX" \
  -benchmem \
  -count "$BENCH_COUNT" | tee "$RESULTS_DIR/bench.txt"

python3 "$ROOT/scripts/perf_metrics.py" bench-summary \
  "$RESULTS_DIR/bench.txt" \
  "$RESULTS_DIR/benchmark-summary.json"

echo "==> Writing machine-readable benchmark stream"
CGO_CFLAGS="${CGO_CFLAGS:-"-I/opt/homebrew/opt/libolm/include"}" \
CGO_LDFLAGS="${CGO_LDFLAGS:-"-L/opt/homebrew/opt/libolm/lib -lolm"}" \
go test -json ./connector \
  -run '^$' \
  -bench "$BENCH_REGEX" \
  -benchmem \
  -count 1 > "$RESULTS_DIR/bench.jsonl"

if [[ "${RUN_SYNAPSE_E2E:-0}" == "1" ]]; then
  echo
  echo "==> Synapse E2E performance test"
  "$ROOT/e2e/synapse/run.sh" | tee "$RESULTS_DIR/synapse-e2e.txt"
  python3 "$ROOT/scripts/perf_metrics.py" synapse-summary \
    "$RESULTS_DIR/synapse-e2e.txt" \
    "$RESULTS_DIR/synapse-summary.json"
else
  echo
  echo "Set RUN_SYNAPSE_E2E=1 to also start a local Synapse and run the burst sync E2E test."
fi

if [[ "${PERF_PROFILE:-0}" == "1" ]]; then
  echo
  echo "==> Writing Go CPU and memory profiles"
  CGO_CFLAGS="${CGO_CFLAGS:-"-I/opt/homebrew/opt/libolm/include"}" \
  CGO_LDFLAGS="${CGO_LDFLAGS:-"-L/opt/homebrew/opt/libolm/lib -lolm"}" \
  go test ./connector \
    -run '^$' \
    -bench "$BENCH_REGEX" \
    -benchmem \
    -count 1 \
    -cpuprofile "$RESULTS_DIR/cpu.pprof" \
    -memprofile "$RESULTS_DIR/mem.pprof" > "$RESULTS_DIR/profile-bench.txt"
  go tool pprof -top "$RESULTS_DIR/cpu.pprof" > "$RESULTS_DIR/cpu-top.txt"
  go tool pprof -top "$RESULTS_DIR/mem.pprof" > "$RESULTS_DIR/mem-top.txt"
fi

if [[ "${PERF_ENFORCE_GATES:-0}" == "1" ]]; then
  echo
  echo "==> Checking performance gates"
  gate_args=("$RESULTS_DIR/benchmark-summary.json")
  if [[ -f "$RESULTS_DIR/synapse-summary.json" ]]; then
    gate_args+=("--synapse-summary" "$RESULTS_DIR/synapse-summary.json")
  fi
  if [[ -n "${PERF_GATES_FILE:-}" ]]; then
    gate_args+=("--gates" "$PERF_GATES_FILE")
  fi
  python3 "$ROOT/scripts/perf_metrics.py" check-gates "${gate_args[@]}"
fi

echo
echo "Performance results written to $RESULTS_DIR"
