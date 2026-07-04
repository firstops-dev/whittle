# whittle — Phase 0/1 execution plan (maintainer)

## Phase 0 — truth solid (public-flip criteria at bottom)
- [x] P0.1 `whittle stats` — local JSONL event log (hook writes, stats reads); the retention loop
- [x] P0.2 GUARANTEES.md — every fidelity claim -> the executable test that pins it
- [x] P0.3 goreleaser config + brew tap plan (signed binaries; brew is the real front door)
- [x] P0.4 CONTRIBUTING + issue templates + non-goals
- [ ] P0.5 CI live (blocked: `gh auth refresh -h github.com -s workflow`, then push .github/workflows/ci.yml)
- [ ] P0.6 supervised clean-machine `whittle setup` run (needs operator)
- [ ] P0.7 10k hook-cap: document limitation + upstream feature request; engineering options doc

## Publish-ready order (2026-07-04, supersedes queue below)
1. [ ] v0.2.1 release (contains snippet-misroute fix)
2. [ ] bench/ harness — corpus + runner + fidelity-verify + report; CI-wired
3. [ ] launch post draft (founder edits)
4. [ ] sweep: cost_api port, review nits (O2/O7/O9), README stale-claim pass, brew rehearsal post-flip
5. [ ] dogfood week closes clean (fidelity incidents 0; retrieval rate sane)
6. [ ] FOUNDER: README pass · 2-3 design partners · flip public · Show HN

## Active queue (maintainer order, 2026-07-04)
- [ ] Q1 adversarial review of retrieval/hint surface (in flight) -> pinned tests
- [x] Q2 debt sweep: port comment, MCP visibility in status, GUARANTEES store entry, README thesis fix
- [ ] Q3 tag v0.2.0 once Q1 clean; dogfood week concludes (watch retrieval rate + incidents)
- [ ] Q4 benchmark harness (bench/: corpus, runner, fidelity verify, report)
- [ ] Q5 launch post draft for founder edit
- [ ] Q6 founder: README pass + 2-3 design partners
- [ ] Q7 flip public + Show HN

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
