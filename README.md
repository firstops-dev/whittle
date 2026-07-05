# whittle

**Carves your agent's tool outputs down to what matters. Never cuts what doesn't come back.**

[![CI](https://github.com/firstops-dev/whittle/actions/workflows/ci.yml/badge.svg)](https://github.com/firstops-dev/whittle/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/firstops-dev/whittle)](https://github.com/firstops-dev/whittle/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/firstops-dev/whittle.svg)](https://pkg.go.dev/github.com/firstops-dev/whittle)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

Whittle is a content-aware compressor for the text AI agents read: tool outputs,
file reads, logs, JSON, terminal streams. Long agent sessions drown in tokens -
but most compressors buy their ratio by silently destroying things agents need:
array rows vanish, file reads get gutted, identifiers come back mangled.

Whittle holds one hard line: **structural compression is lossless or clearly
marked, code never reaches a lossy model, and every anomaly fails open to the
original bytes.** The reduction number it reports is calibrated to real
tokenizer counts - not byte counts that overstate savings by up to 4×.

## See it

```
$ tail -n 6 build.log | whittle compress -stats
ERROR migrate failed: relation "users" does not exist
... [118 lines omitted]
2026-07-04 INFO  worker drained cleanly
ERROR shutdown: connection reset by peer

whittle: action=compressed detected=log strategy=log_compressor tokens=1904->47
```

Errors and the summary survive; 118 lines of INFO noise become one honest
marker. JSON reshapes losslessly, code passes through untouched, terminal
progress bars collapse to their final frame - see [Benchmarks](#benchmarks).

## Install

```
go install github.com/firstops-dev/whittle/cmd/whittle@latest
whittle setup
```

That's the whole thing. `setup`:

- installs the **Claude Code PostToolUse hook** - tool outputs your agent reads
  are whittled from now on (known limit: compressed results larger than ~9.5k
  chars pass through uncompressed, a Claude Code hook-output cap; see GUARANTEES.md) (Claude Code is the supported agent today;
  Cursor, Codex and OpenCode adapters are on the roadmap);
- materializes the ML prose sidecar (embedded in the binary) into `~/.whittle`,
  builds its venv, and uses your GPU automatically (CUDA > Apple MPS > CPU) -
  if `python3` is missing, whittle simply runs deterministic-only;
- registers a **launchd agent** (macOS) so the service starts at login and is
  kept alive.

On Linux (launchd is macOS-only), run the daemon under systemd:

```
# ~/.config/systemd/user/whittle.service
[Unit]
Description=whittle daemon
[Service]
ExecStart=%h/go/bin/whittle daemon
Restart=always
[Install]
WantedBy=default.target
```

Manage it with `whittle status`, `whittle stop`, and `whittle cleanup` (stops
the service and removes the hook). Everything fails open: if whittle is down,
your agent sees original outputs, never an error.

## Use

```
whittle compress output.json                 # compressed to stdout
cat build.log | whittle compress -stats      # stats to stderr
whittle serve -addr :45871                    # HTTP: POST /v1/compress
```

As a library:

```go
eng := whittle.New(whittle.Options{})
res := eng.Compress(ctx, toolOutput)
// res.Output, res.Action ("compressed"|"skipped"), res.SkipReason,
// res.Strategy, res.Detected
```

## What it does per content type

| detected | strategy | guarantee |
|---|---|---|
| JSON | minify + columnar reshape (union schema, typed CSV, nested flattening, constant factoring) | **lossless** - reconstructs byte-exact; rows are never dropped |
| logs / build output | keep errors, warnings, stack traces, summaries | lossy but **marked** - `... [N lines omitted]`, exact accounting |
| terminal | ANSI strip + CR-overwrite collapse (progress bars → final frame) | what the terminal actually displayed; rune-safe |
| markdown file reads | structure-aware: prose compressed by the model, **code fences / tables / lists / headings passed through byte-exact** | code never reaches the model |
| source code | untouched | routed away from every lossy path |
| prose | extractive model (optional) with fidelity guards: entity protection, whole-token deletion, negation preservation | fails open on any guard trip |

Every path is wrapped in fail-open guardrails: empty-output, expansion (both
byte- and token-honest), panic recovery. The worst case is always "not
compressed", never "corrupted".

## The ML prose path (installed by `whittle setup`, optional by design)

`whittle setup` installs and supervises the prose sidecar automatically: the
Python source ships inside the binary, setup builds its venv, and the daemon
keeps it running (GPU auto-selected: CUDA > Apple MPS > CPU). If `python3` is
missing, setup says so and whittle runs deterministic-only - nothing breaks.

"Optional" means the deterministic strategies never depend on it. Only if you
use whittle as a bare library or `whittle serve` **without** running setup do
you wire it manually (LLMLingua-2 + whittle's fidelity guards - see
[model/](model/)):

```
cd model && python -m venv .venv && .venv/bin/pip install -r requirements.txt
.venv/bin/uvicorn app:app --port 45872
export WHITTLE_MODEL_URL=http://127.0.0.1:45872
```

## Configuration

| env | default | meaning |
|---|---|---|
| `WHITTLE_MODEL_URL` | *(unset - prose off)* | model sidecar URL |
| `WHITTLE_MAX_CHARS` | 262144 | global size ceiling (skip before classify) |
| `WHITTLE_PROSE_MAX_CHARS` | 4500 | prose-path latency ceiling |



## Benchmarks

Three tiers, in increasing order of realism - every number regenerable from
this repo (`go run ./bench` for the deterministic rows; the prose row needs the
model sidecar), reductions on an estimated-token basis (labeled).

### 1. Synthetic corpus (ours - headline per content class)

Authored fixtures in [`bench/corpus/`](bench/corpus/), designed to exercise each
strategy and its guarantees. Full table: [`bench/REPORT.md`](bench/REPORT.md).

| class | representative result |
|---|---|
| JSON (uniform/sparse/nested) | 57% - lossless, byte-exact reconstruction |
| repetitive logs | 97% - omissions marked and exactly accounted |
| terminal progress streams | 99% - final frame, rune-safe |
| code / config (py, go, yaml) | **0% by design - skipped, never touched** |
| prose | 30-40% extractive, fidelity-guarded (needs the model sidecar; not part of the deterministic `go run ./bench` output) |

### 2. Side-by-side on headroom's data

Inputs frozen from [headroom](https://github.com/headroomlabs-ai/headroom)'s own
benchmark generators (Apache-2.0; pinned commit, seed 42 - they check in no
corpora, so we froze what their numbers are computed on; `bench/corpus_headroom/`,
PROVENANCE.md). Both tools ran on identical bytes, defaults only, measured with
the same tokenizer. Full table + methodology: [`bench/SIDEBYSIDE.md`](bench/SIDEBYSIDE.md).

| | headroom-ai 0.30.0 | whittle 0.2.1 |
|---|---|---|
| aggregate token reduction (10 files, 116.5k tokens) | **41.8%** | 36.5% |
| - conversation / agent-transcript JSON (3 files) | 2.1% | **5.4%** |
| - bulk data arrays (7 files) | **48.3%** | 41.6% |
| fidelity of that reduction | includes lossy row-dropping (recoverable via headroom's resident runtime) | **byte-exact lossless** on every file |
| median latency, in-process (same files) | 2.93 ms | 2.36 ms |

Read it straight: on the aggregate, headroom-ai's defaults compress ~5 points
more - by dropping rows whittle refuses to drop. The category split shows where
each position pays: on conversation-shaped content (the shape agent tool
outputs actually take) whittle leads while staying lossless; on bulk data
arrays headroom-ai's lossy sampling buys its margin. Latency is near parity.
Which trade you want is the whole point of this project.

### 3. Real-world datasets

Results on real agent-session tool outputs. *Coming - measured on production
traces; stats to be published with methodology.*

## Why whittle - compress at write-time, not read-time

Context compressors typically integrate with coding agents as **request-path
proxies**: your agent's base URL is redirected through a local server that
rewrites conversation history at *read time*, on every LLM call. That position
forces hard problems - prompt-cache stabilization (mutating history invalidates
cached prefixes), per-call re-compression, terminating your API traffic (keys,
system prompts and all), and a resident runtime that must stay up or your agent
goes down with it. It also makes lossy compression the default, backed by a
retrieval loop: the runtime is guaranteed present, so dropped content can be
served back on demand.

Whittle takes the other position: it is a **PostToolUse hook**. Each tool output
is compressed **once, at the moment it is born**, before it ever enters
conversation history. Everything else follows from that choice:

- **Savings compound.** A tool output lives in context for every subsequent
  turn. Tokens removed at write-time are removed from *every* later call -
  no per-call rework, no cache surgery, because history is never mutated.
- **No trust expansion.** A hook sees one tool output at a time, locally, with
  zero credentials. Nothing terminates your API traffic.
- **Failure is free.** The hook fails open; if whittle is down or declines, the
  agent proceeds with the original output. A gateway outage is an agent outage.
- **The loss budget is honest.** A read-time proxy can afford recoverable lossy
  compression - its resident runtime serves dropped content back when the model
  asks. Whittle keeps no runtime in your request path; reduced outputs carry a tiny
  retrieval pointer served by the local daemon (`whittle_get`), and lossless
  transforms carry nothing at all - lossless-or-marked stays the construction,
  recovery is the safety net, never the license.

The hook is whittle's default surface, not its only one: **library**
(`whittle.New`) → **HTTP service** (`whittle serve`) → **hook adapters**
(Claude Code PostToolUse today; Cursor, Codex, OpenCode adapters on the
roadmap) - and the same library embeds in gateways or pipelines if that is
where you need it. The position is the point: compression happens where output
is born, whatever surface delivers it there.

## Architecture

```
Claude Code ──PostToolUse (HTTP)──▶ whittle daemon  (:45871, launchd-kept)
                                       │
                                       ├─ router ─▶ json · log · terminal · markdown
                                       │            (deterministic, in-process)
                                       └─ prose ─▶ model sidecar (:45872, optional GPU)
                                       │
   whittle_get(id) ◀──MCP tool────────┘  reduced originals, retrievable on demand
```

One resident daemon, three surfaces (hook · HTTP · MCP). Compression happens
where output is born, off the model-request path entirely.

## Performance

Deterministic strategies are pure CPU, single static binary, zero allocatable
model state (Apple M-series, `go test -bench`):

| input | size | latency |
|---|---|---|
| JSON array, 200 rows, pretty-printed | ~21 KB | ~1.0 ms |
| terminal progress stream | ~12 KB | ~3.9 ms |
| build log, 800 lines | ~56 KB | ~21 ms |

The hook runs after the tool call completes, so this cost is **off the LLM
request path entirely** - model-call latency is unchanged. (These are absolute
in-path budgets on whittle's own corpus; for tool-vs-tool latency on identical
inputs see the Benchmarks side-by-side above and `bench/SIDEBYSIDE.md`.) The optional ML
prose path is capped by a fail-open budget (default 1.5 s) and never blocks
beyond it.

## Design principles

1. **Fail open.** A compressor that breaks your agent is worse than no compressor.
2. **Never silent loss.** Lossy paths mark what they removed and account for it exactly.
3. **Code is sacred.** File reads, fences, identifiers: byte-exact or untouched.
4. **Token-honest.** Accept gates and reported savings use calibrated token
   estimates (MAE ~8% vs `o200k_base`; regenerate with `bench/calibrate_tokens.py`),
   not bytes.
5. **Adversarially tested.** The invariants above are pinned by reconstruction
   fuzzing, per-language routing suites, and fail-open contract tests.


## Acknowledgments

Whittle's log-selection strategy, several content-detection heuristics, and the
tabular parser were adapted from [Headroom](https://github.com/headroomlabs-ai/headroom)
(Apache-2.0) - adapted portions are marked in source comments, and we think
their compaction work is excellent. Whittle exists because we wanted the other
position: a write-time PostToolUse hook instead of a read-time request-path
proxy, with the stricter fidelity contract that position requires. See NOTICE.

## Verify it yourself

Every claim here is checkable from a clone - that is the point.

```
make test                     # guarantees as executable tests (see GUARANTEES.md)
go run ./bench                # corpus reductions + fidelity, SHA-pinned, CI-gated
python bench/calibrate_tokens.py   # reproduces the token-estimator MAE (needs tiktoken)
```

[GUARANTEES.md](GUARANTEES.md) maps each fidelity promise to the test that pins it.

## Known limitations

- **10k hook-output cap.** Claude Code caps a hook's replacement at 10,000 chars;
  compressed results above ~9.5k pass through uncompressed today. Retrieval-backed
  chunking is the planned fix ([docs/hook-output-cap.md](docs/hook-output-cap.md)).
- **Prose needs the sidecar.** Without it, prose and markdown docs pass through
  unchanged; deterministic strategies are unaffected.
- **launchd is macOS-only.** Linux runs the daemon under systemd (unit above).
- **Prose latency ceiling** (default 4500 chars) trades a hard cap for predictable
  in-path latency; raise it with `WHITTLE_PROSE_MAX_CHARS`.

## Contributing

Whittle's bar is that guarantees are executable - see [CONTRIBUTING.md](CONTRIBUTING.md).
The highest-severity issue class is **fidelity**: if whittle ever changed the
meaning of an output, that is a bug we treat as urgent (use the fidelity issue
template). Good first issues: agent adapters (Cursor/Codex/OpenCode), Linux
packaging, detection corpus cases.

## License

Apache-2.0.
