# whittle — Phase 0/1 execution plan (maintainer)

## Phase 0 — truth solid (public-flip criteria at bottom)
- [x] P0.1 `whittle stats` — local JSONL event log (hook writes, stats reads); the retention loop
- [x] P0.2 GUARANTEES.md — every fidelity claim -> the executable test that pins it
- [x] P0.3 goreleaser config + brew tap plan (signed binaries; brew is the real front door)
- [x] P0.4 CONTRIBUTING + issue templates + non-goals
- [ ] P0.5 CI live (blocked: `gh auth refresh -h github.com -s workflow`, then push .github/workflows/ci.yml)
- [ ] P0.6 supervised clean-machine `whittle setup` run (needs operator)
- [ ] P0.7 10k hook-cap: document limitation + upstream feature request; engineering options doc

## Phase 1 — launch
- [ ] P1.1 reproducible benchmark harness in-repo
- [ ] P1.2 launch post draft ("the compressor that can't lie") from measured session material
- [ ] P1.3 3 design partners running 1 week; incident channel
- [ ] P1.4 README gif (stats screenshot moment)

## Phase 2 — widen
- [ ] P2.1 Cursor adapter; then Codex/OpenCode
- [ ] P2.2 Linux systemd parity
- [ ] P2.3 envelope spec (versioned wire contract)
- [ ] P2.4 result cache (measured: 28% duplicate calls)

## Public-flip criteria
CI green badge · brew install works · stats screenshot · GUARANTEES.md · 1 week self-dogfood, zero incidents

## Non-goals (published)
No proxy mode · no model hosting · no telemetry (stats are local-only)
