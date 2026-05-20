#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BENCH_COUNT="${BENCH_COUNT:-5}"
BENCH_REGEX="${BENCH_REGEX:-Benchmark(CloneMessageContent|GeneratedFallbackAvatarFromMXC|CloneRawMap)}"
RESULTS_DIR="${PERF_RESULTS_DIR:-"$ROOT/perf-results/$(date -u +%Y%m%dT%H%M%SZ)"}"

cd "$ROOT"
mkdir -p "$RESULTS_DIR"

python3 - "$RESULTS_DIR/metadata.json" "$BENCH_COUNT" "$BENCH_REGEX" <<'PY'
import json
import os
import platform
import subprocess
import sys
from datetime import datetime, timezone

def run(cmd):
    try:
        return subprocess.check_output(cmd, text=True).strip()
    except Exception:
        return ""

metadata = {
    "timestamp_utc": datetime.now(timezone.utc).isoformat(),
    "git_commit": run(["git", "rev-parse", "HEAD"]),
    "git_branch": run(["git", "branch", "--show-current"]),
    "go_version": run(["go", "version"]),
    "platform": platform.platform(),
    "machine": platform.machine(),
    "bench_count": sys.argv[2],
    "bench_regex": sys.argv[3],
    "run_synapse_e2e": os.environ.get("RUN_SYNAPSE_E2E", "0"),
    "synapse_bursts": os.environ.get("LOCAL_SYNAPSE_E2E_BURSTS", os.environ.get("LOCAL_SYNAPSE_E2E_BURST", "")),
    "perf_profile": os.environ.get("PERF_PROFILE", "0"),
}
with open(sys.argv[1], "w", encoding="utf-8") as f:
    json.dump(metadata, f, indent=2, sort_keys=True)
    f.write("\n")
PY

echo "==> Go hot-path benchmarks"
CGO_CFLAGS="${CGO_CFLAGS:-"-I/opt/homebrew/opt/libolm/include"}" \
CGO_LDFLAGS="${CGO_LDFLAGS:-"-L/opt/homebrew/opt/libolm/lib -lolm"}" \
go test ./connector \
  -run '^$' \
  -bench "$BENCH_REGEX" \
  -benchmem \
  -count "$BENCH_COUNT" | tee "$RESULTS_DIR/bench.txt"

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

echo
echo "Performance results written to $RESULTS_DIR"
