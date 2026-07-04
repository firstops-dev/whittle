# Contributing

whittle's bar: **guarantees are executable**. A PR that adds or changes
compression behavior must (1) keep GUARANTEES.md true, (2) pin new behavior
with tests (losslessness = reconstruction test; routing = corpus case), and
(3) fail open on every new error path.

Good first issues: agent adapters (Cursor/Codex/OpenCode hook shims), Linux
systemd parity, detection corpus cases. Non-goals (won't merge): proxy mode,
model hosting, telemetry.

Dev: `make build test lint` · benchmarks: `go test -bench . ./compress/`.
