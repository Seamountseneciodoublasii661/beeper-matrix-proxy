import tempfile
import unittest
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
import perf_metrics


class PerfMetricsTest(unittest.TestCase):
    def test_summarize_bench(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "bench.txt"
            path.write_text(
                "\n".join(
                    [
                        "BenchmarkCloneMessageContent-14    10    100.0 ns/op    576 B/op    5 allocs/op",
                        "BenchmarkCloneMessageContent-14    10    120.0 ns/op    576 B/op    5 allocs/op",
                    ]
                ),
                encoding="utf-8",
            )

            summary = perf_metrics.summarize_bench(str(path))

        self.assertEqual(summary["BenchmarkCloneMessageContent"]["runs"], 2)
        self.assertEqual(summary["BenchmarkCloneMessageContent"]["ns_per_op_mean"], 110.0)
        self.assertEqual(summary["BenchmarkCloneMessageContent"]["allocs_per_op_mean"], 5.0)

    def test_summarize_synapse_includes_msgtypes_and_durations(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "synapse.txt"
            path.write_text(
                "\n".join(
                    [
                        "synapse burst sync delivered 100/100 messages; send_duration=1.5s sync_duration=20.5ms",
                        "synapse mixed modality sync counts=map[m.room.message:9] msgtypes=map[m.image:1 m.text:2] send_duration=180us sync_duration=3ms",
                        "synapse multi-room burst sync rooms=2 per_room=5 total=10 send_duration=200ms sync_duration=12ms",
                        "synapse dual-user sync counts=map[m.room.message:2] msgtypes=map[m.text:2] senders=map[@peer:example:1 @proxy:example:1]",
                        "synapse media upload/download msgtypes=map[m.file:1] bytes=29",
                        "synapse upload limit small_bytes=1024 large_bytes=2097152 oversized_status=413",
                        "synapse room state profile counts=map[m.room.avatar:1 m.room.name:1 m.room.topic:1]",
                        "synapse relations reply=true thread=true",
                        "synapse poll lifecycle counts=map[org.matrix.msc3381.poll.end:1 org.matrix.msc3381.poll.response:1 org.matrix.msc3381.poll.start:1]",
                        "synapse ephemeral sync typing=true receipt=true",
                    ]
                ),
                encoding="utf-8",
            )

            summary = perf_metrics.summarize_synapse(str(path))

        self.assertEqual(summary["bursts"][0]["send_ms"], 1500.0)
        self.assertEqual(summary["bursts"][0]["sync_ms"], 20.5)
        self.assertEqual(summary["mixed_modality"]["sync_ms"], 3.0)
        self.assertIn("m.image:1", summary["mixed_modality"]["msgtypes"])
        self.assertEqual(summary["multi_room"][0]["total"], 10)
        self.assertIn("@peer:example:1", summary["dual_user"][0]["senders"])
        self.assertEqual(summary["media"][0]["bytes"], 29)
        self.assertEqual(summary["upload_limit"][0]["oversized_status"], 413)
        self.assertIn("m.room.avatar:1", summary["room_state"][0]["counts"])
        self.assertTrue(summary["relations"][0]["reply"])
        self.assertTrue(summary["relations"][0]["thread"])
        self.assertIn("poll.start:1", summary["poll_lifecycle"][0]["counts"])
        self.assertTrue(summary["ephemeral"][0]["typing"])
        self.assertTrue(summary["ephemeral"][0]["receipt"])

    def test_check_bench_gates_reports_regressions(self):
        failures = perf_metrics.check_bench_gates(
            {
                "BenchmarkCloneMessageContent": {
                    "ns_per_op_mean": 3000,
                    "allocs_per_op_mean": 5,
                }
            },
            {
                "BenchmarkCloneMessageContent": {
                    "max_ns_per_op_mean": 2000,
                    "max_allocs_per_op_mean": 8,
                }
            },
        )

        self.assertEqual(len(failures), 1)
        self.assertIn("exceeds", failures[0])

    def test_check_synapse_gates_reports_missing_and_slow_modalities(self):
        failures = perf_metrics.check_synapse_gates(
            {"bursts": [], "mixed_modality": None},
            {"max_burst_sync_ms": 500, "max_mixed_modality_sync_ms": 500},
        )

        self.assertIn("missing burst E2E measurements", failures)
        self.assertIn("missing mixed modality E2E measurement", failures)

        failures = perf_metrics.check_synapse_gates(
            {
                "bursts": [{"delivered": 9, "expected": 10, "sync_ms": 600}],
                "mixed_modality": {"sync_ms": 750},
            },
            {"max_burst_sync_ms": 500, "max_mixed_modality_sync_ms": 500},
        )

        self.assertEqual(len(failures), 3)
        self.assertTrue(any("burst delivered 9/10" in item for item in failures))
        self.assertTrue(any("mixed modality sync_ms" in item for item in failures))

    def test_merge_custom_gates_preserves_default_benchmark_gates(self):
        merged = perf_metrics.merge_gates(
            {
                "benchmarks": {
                    "BenchmarkCloneMessageContent": {
                        "max_ns_per_op_mean": 1,
                    }
                }
            }
        )

        self.assertEqual(
            merged["benchmarks"]["BenchmarkCloneMessageContent"]["max_ns_per_op_mean"],
            1,
        )
        self.assertIn("BenchmarkCloneRawMap", merged["benchmarks"])
        self.assertIn("max_mixed_modality_sync_ms", merged["synapse"])


if __name__ == "__main__":
    unittest.main()
