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
    "git_dirty": bool(run(["git", "status", "--porcelain"])),
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

python3 - "$RESULTS_DIR/bench.txt" "$RESULTS_DIR/benchmark-summary.json" <<'PY'
import json
import re
import statistics
import sys

pattern = re.compile(
    r'^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+([0-9.]+)\s+ns/op\s+([0-9.]+)\s+B/op\s+([0-9.]+)\s+allocs/op$'
)
benchmarks = {}
with open(sys.argv[1], encoding="utf-8") as f:
    for line in f:
        match = pattern.match(line.strip())
        if not match:
            continue
        name, ns, bytes_per_op, allocs = match.groups()
        item = benchmarks.setdefault(name, {"ns_per_op": [], "bytes_per_op": [], "allocs_per_op": []})
        item["ns_per_op"].append(float(ns))
        item["bytes_per_op"].append(float(bytes_per_op))
        item["allocs_per_op"].append(float(allocs))

summary = {}
for name, values in benchmarks.items():
    summary[name] = {
        "runs": len(values["ns_per_op"]),
        "ns_per_op_mean": statistics.fmean(values["ns_per_op"]),
        "ns_per_op_min": min(values["ns_per_op"]),
        "ns_per_op_max": max(values["ns_per_op"]),
        "bytes_per_op_mean": statistics.fmean(values["bytes_per_op"]),
        "allocs_per_op_mean": statistics.fmean(values["allocs_per_op"]),
    }

with open(sys.argv[2], "w", encoding="utf-8") as f:
    json.dump(summary, f, indent=2, sort_keys=True)
    f.write("\n")
PY

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
  python3 - "$RESULTS_DIR/synapse-e2e.txt" "$RESULTS_DIR/synapse-summary.json" <<'PY'
import json
import re
import sys

def duration_ms(raw):
    raw = raw.strip()
    match = re.fullmatch(r'([0-9.]+)(ns|µs|us|ms|s|m)', raw)
    if not match:
        return None
    value = float(match.group(1))
    unit = match.group(2)
    if unit == "ns":
        return value / 1_000_000
    if unit in {"µs", "us"}:
        return value / 1_000
    if unit == "ms":
        return value
    if unit == "s":
        return value * 1_000
    if unit == "m":
        return value * 60_000
    return None

burst_re = re.compile(
    r'synapse burst sync delivered (\d+)/(\d+) messages; send_duration=([^ ]+) sync_duration=([^ ]+)'
)
mixed_re = re.compile(
    r'synapse mixed modality sync counts=(map\[[^\]]+\]) msgtypes=(map\[[^\]]+\]) send_duration=([^ ]+) sync_duration=([^ ]+)'
)
summary = {"bursts": [], "mixed_modality": None}
with open(sys.argv[1], encoding="utf-8") as f:
    for line in f:
        if match := burst_re.search(line):
            delivered, expected, send, sync = match.groups()
            summary["bursts"].append({
                "delivered": int(delivered),
                "expected": int(expected),
                "send_ms": duration_ms(send),
                "sync_ms": duration_ms(sync),
            })
            continue
        if match := mixed_re.search(line):
            counts, msgtypes, send, sync = match.groups()
            summary["mixed_modality"] = {
                "counts": counts,
                "msgtypes": msgtypes,
                "send_ms": duration_ms(send),
                "sync_ms": duration_ms(sync),
            }

with open(sys.argv[2], "w", encoding="utf-8") as f:
    json.dump(summary, f, indent=2, sort_keys=True)
    f.write("\n")
PY
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
