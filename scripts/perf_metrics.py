#!/usr/bin/env python3
import argparse
import json
import os
import platform
import re
import statistics
import subprocess
import sys
from datetime import datetime, timezone


BENCH_RE = re.compile(
    r"^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+([0-9.]+)\s+ns/op\s+([0-9.]+)\s+B/op\s+([0-9.]+)\s+allocs/op$"
)
BURST_RE = re.compile(
    r"synapse burst sync delivered (\d+)/(\d+) messages; send_duration=([^ ]+) sync_duration=([^ ]+)"
)
MIXED_RE = re.compile(
    r"synapse mixed modality sync counts=(map\[[^\]]+\]) msgtypes=(map\[[^\]]+\]) send_duration=([^ ]+) sync_duration=([^ ]+)"
)

DEFAULT_BENCH_GATES = {
    "BenchmarkCloneMessageContent": {"max_ns_per_op_mean": 2000, "max_allocs_per_op_mean": 8},
    "BenchmarkGeneratedFallbackAvatarFromMXC": {"max_ns_per_op_mean": 1000, "max_allocs_per_op_mean": 0},
    "BenchmarkCloneRawMap": {"max_ns_per_op_mean": 10000, "max_allocs_per_op_mean": 20},
}
DEFAULT_SYNAPSE_GATES = {
    "max_burst_sync_ms": 500,
    "max_mixed_modality_sync_ms": 500,
}


def run(cmd: list[str]) -> str:
    try:
        return subprocess.check_output(cmd, text=True).strip()
    except Exception:
        return ""


def write_json(path: str, data: object) -> None:
    with open(path, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2, sort_keys=True)
        f.write("\n")


def duration_ms(raw: str) -> float | None:
    match = re.fullmatch(r"([0-9.]+)(ns|µs|us|ms|s|m)", raw.strip())
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


def generate_metadata(output: str, bench_count: str, bench_regex: str) -> None:
    metadata = {
        "timestamp_utc": datetime.now(timezone.utc).isoformat(),
        "git_commit": run(["git", "rev-parse", "HEAD"]),
        "git_branch": run(["git", "branch", "--show-current"]),
        "git_dirty": bool(run(["git", "status", "--porcelain"])),
        "go_version": run(["go", "version"]),
        "platform": platform.platform(),
        "machine": platform.machine(),
        "bench_count": bench_count,
        "bench_regex": bench_regex,
        "run_synapse_e2e": os.environ.get("RUN_SYNAPSE_E2E", "0"),
        "synapse_bursts": os.environ.get("LOCAL_SYNAPSE_E2E_BURSTS", os.environ.get("LOCAL_SYNAPSE_E2E_BURST", "")),
        "perf_profile": os.environ.get("PERF_PROFILE", "0"),
        "perf_enforce_gates": os.environ.get("PERF_ENFORCE_GATES", "0"),
    }
    write_json(output, metadata)


def summarize_bench(input_path: str) -> dict[str, dict[str, float | int]]:
    benchmarks: dict[str, dict[str, list[float]]] = {}
    with open(input_path, encoding="utf-8") as f:
        for line in f:
            match = BENCH_RE.match(line.strip())
            if not match:
                continue
            name, ns, bytes_per_op, allocs = match.groups()
            item = benchmarks.setdefault(name, {"ns_per_op": [], "bytes_per_op": [], "allocs_per_op": []})
            item["ns_per_op"].append(float(ns))
            item["bytes_per_op"].append(float(bytes_per_op))
            item["allocs_per_op"].append(float(allocs))

    summary: dict[str, dict[str, float | int]] = {}
    for name, values in benchmarks.items():
        summary[name] = {
            "runs": len(values["ns_per_op"]),
            "ns_per_op_mean": statistics.fmean(values["ns_per_op"]),
            "ns_per_op_min": min(values["ns_per_op"]),
            "ns_per_op_max": max(values["ns_per_op"]),
            "bytes_per_op_mean": statistics.fmean(values["bytes_per_op"]),
            "allocs_per_op_mean": statistics.fmean(values["allocs_per_op"]),
        }
    return summary


def summarize_synapse(input_path: str) -> dict[str, object]:
    summary: dict[str, object] = {"bursts": [], "mixed_modality": None}
    with open(input_path, encoding="utf-8") as f:
        for line in f:
            if match := BURST_RE.search(line):
                delivered, expected, send, sync = match.groups()
                summary["bursts"].append(
                    {
                        "delivered": int(delivered),
                        "expected": int(expected),
                        "send_ms": duration_ms(send),
                        "sync_ms": duration_ms(sync),
                    }
                )
                continue
            if match := MIXED_RE.search(line):
                counts, msgtypes, send, sync = match.groups()
                summary["mixed_modality"] = {
                    "counts": counts,
                    "msgtypes": msgtypes,
                    "send_ms": duration_ms(send),
                    "sync_ms": duration_ms(sync),
                }
    return summary


def load_json(path: str) -> object:
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def check_bench_gates(summary: dict[str, object], gates: dict[str, object]) -> list[str]:
    failures: list[str] = []
    for name, gate in gates.items():
        data = summary.get(name)
        if not isinstance(data, dict):
            failures.append(f"missing benchmark {name}")
            continue
        for key, limit in gate.items():
            metric = key.removeprefix("max_")
            value = data.get(metric)
            if isinstance(value, (int, float)) and value > limit:
                failures.append(f"{name}.{metric}={value:.3f} exceeds {limit}")
    return failures


def check_synapse_gates(summary: dict[str, object], gates: dict[str, float]) -> list[str]:
    failures: list[str] = []
    max_burst_sync = gates.get("max_burst_sync_ms")
    if max_burst_sync is not None:
        for item in summary.get("bursts", []):
            sync_ms = item.get("sync_ms") if isinstance(item, dict) else None
            if isinstance(sync_ms, (int, float)) and sync_ms > max_burst_sync:
                failures.append(f"burst sync_ms={sync_ms:.3f} exceeds {max_burst_sync}")
            if isinstance(item, dict) and item.get("delivered") != item.get("expected"):
                failures.append(f"burst delivered {item.get('delivered')}/{item.get('expected')}")
    max_mixed_sync = gates.get("max_mixed_modality_sync_ms")
    mixed = summary.get("mixed_modality")
    if max_mixed_sync is not None and isinstance(mixed, dict):
        sync_ms = mixed.get("sync_ms")
        if isinstance(sync_ms, (int, float)) and sync_ms > max_mixed_sync:
            failures.append(f"mixed modality sync_ms={sync_ms:.3f} exceeds {max_mixed_sync}")
    return failures


def check_gates(bench_summary_path: str, synapse_summary_path: str | None, gates_path: str | None) -> int:
    gates = {
        "benchmarks": DEFAULT_BENCH_GATES,
        "synapse": DEFAULT_SYNAPSE_GATES,
    }
    if gates_path:
        custom = load_json(gates_path)
        if isinstance(custom, dict):
            gates.update(custom)

    failures = check_bench_gates(load_json(bench_summary_path), gates.get("benchmarks", {}))
    if synapse_summary_path and os.path.exists(synapse_summary_path):
        failures.extend(check_synapse_gates(load_json(synapse_summary_path), gates.get("synapse", {})))

    if failures:
        for failure in failures:
            print(f"PERF_GATE_FAIL: {failure}", file=sys.stderr)
        return 1
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="cmd", required=True)

    metadata = sub.add_parser("metadata")
    metadata.add_argument("output")
    metadata.add_argument("--bench-count", required=True)
    metadata.add_argument("--bench-regex", required=True)

    bench = sub.add_parser("bench-summary")
    bench.add_argument("input")
    bench.add_argument("output")

    synapse = sub.add_parser("synapse-summary")
    synapse.add_argument("input")
    synapse.add_argument("output")

    gates = sub.add_parser("check-gates")
    gates.add_argument("bench_summary")
    gates.add_argument("--synapse-summary")
    gates.add_argument("--gates")

    args = parser.parse_args()
    if args.cmd == "metadata":
        generate_metadata(args.output, args.bench_count, args.bench_regex)
    elif args.cmd == "bench-summary":
        write_json(args.output, summarize_bench(args.input))
    elif args.cmd == "synapse-summary":
        write_json(args.output, summarize_synapse(args.input))
    elif args.cmd == "check-gates":
        return check_gates(args.bench_summary, args.synapse_summary, args.gates)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
