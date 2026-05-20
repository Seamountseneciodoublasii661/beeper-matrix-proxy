# Changelog

All notable changes to `beeper-matrix-proxy` are documented here.

The project does not have tagged releases yet. Commit hashes below refer to the
public `main` branch.

## Unreleased

### Added

- GitHub Actions CI for tests, vet, connector race tests, and a performance
  smoke benchmark.
- README performance snapshot with measured hot-path improvements.
- README link to this changelog.
- Mixed-modality local Synapse E2E coverage for text, edits, stickers,
  reactions, redactions, polls, room state, and call invites.
- Optional performance profiling artifacts via `PERF_PROFILE=1 ./scripts/perf.sh`
  (`cpu.pprof`, `mem.pprof`, and top reports).
- `metadata.json` in every performance result directory with commit, Go version,
  platform, benchmark regex, and E2E/profile settings.
- `benchmark-summary.json` and `synapse-summary.json` for quick regression
  comparison without parsing human-readable logs.

### Performance

- Replace JSON round-tripping in `cloneRawMap` with targeted recursive cloning
  for poll/raw-event payloads, reducing the measured test case from roughly 60
  allocations to 12 allocations per clone.
- Cache the complete generated fallback avatar object, reducing repeated
  fallback avatar calls from 3 allocations to 0 allocations in the benchmark.

## 2026-05-20

### Performance

- `a3c00d9` - Cache generated fallback avatars for repeated stale Matrix media
  requests.
- `a3c00d9` - Raise the default remote Matrix `/sync` timeline window from 50 to
  100 events.
- `a3c00d9` - Add `LOCAL_MATRIX_SYNC_TIMELINE_LIMIT` so larger deployments can
  tune burst tolerance without code changes.
- `a3c00d9` - Teach the local Synapse E2E runner to raise the test sync window
  to the largest configured burst automatically.
- `80fb7ce` - Replace JSON round-tripping in the message clone hot path with
  targeted deep copies.
- `80fb7ce` - Bound the sent-event echo suppression cache to avoid unbounded
  growth during remote outages.
- `80fb7ce` - Reduce Matrix sync payload size with lazy-loaded members and a
  bridge-focused state filter.

### Testing

- `98d2c47` - Write benchmark output to `perf-results/<timestamp>/bench.txt`.
- `98d2c47` - Write machine-readable benchmark output to
  `perf-results/<timestamp>/bench.jsonl`.
- `98d2c47` - Record local Synapse E2E output in
  `perf-results/<timestamp>/synapse-e2e.txt`.
- `98d2c47` - Add multi-burst local Synapse E2E testing via
  `LOCAL_SYNAPSE_E2E_BURSTS=10,25,40`.
- `80fb7ce` - Add a disposable Docker Synapse E2E harness that registers a test
  user, uploads the bridge sync filter, sends message bursts, and verifies that
  all burst messages arrive through `/sync`.

### Compatibility

- `9f04820` - Harden direct media handling and reaction redactions.
- `71a971c` - Persist Matrix sync filter and next-batch state safely.
- `1dc602d` - Harden Matrix sync and media proxying.
- `f03ba39` - Bridge Matrix typing notifications, read receipts, and call
  notices.

## Current Benchmark Reference

Latest measured local run on Apple M4 Pro:

| Benchmark | Result |
|---|---:|
| `BenchmarkCloneMessageContent` | ~150 ns/op, 576 B/op, 5 allocs/op |
| `BenchmarkGeneratedFallbackAvatarFromMXC` | ~5.9 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkCloneRawMap` | ~664 ns/op, 1736 B/op, 12 allocs/op |
| Local Synapse burst E2E | 10/10, 40/40, and 100/100 messages delivered |
| Local Synapse mixed-modality E2E | text, edit, sticker, reaction, redaction, poll, room state, and call invite delivered |
