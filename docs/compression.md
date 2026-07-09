# Compression: reference

The full per-content-type contract, the ML prose path, architecture, performance, and limitations.

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
[model/](../model/)):

```
cd model && python -m venv .venv && .venv/bin/pip install -r requirements.txt
.venv/bin/uvicorn app:app --port 45872
export WHITTLE_MODEL_URL=http://127.0.0.1:45872
```


## Architecture

```
Claude Code ──PostToolUse (HTTP)──▶ whittle daemon  (:45871, launchd-kept)
                                       │
                                       ├─ dispatch ▶ json · log · terminal · markdown
                                       │            (deterministic, in-process)
                                       └─ prose ─▶ model sidecar (:45872, optional GPU)
                                       │
   whittle_get(id) ◀──MCP tool────────┘  reduced originals, retrievable on demand

  ── opt-in, separate process, off by default ──
Claude Code ──ANTHROPIC_BASE_URL──▶ whittle route (:45873) ──▶ Anthropic API
                                      policy-based model-tier routing;
                                      rewrites the model, not history; fail-open
```

The compression surfaces are one resident daemon, three ways in (hook · HTTP ·
MCP) - compression happens where output is born, off the model-request path
entirely. The model router is a **separate, opt-in** process on the request path;
you run it only when you want tier routing.


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
inputs see the Benchmarks side-by-side above and [`bench/SIDEBYSIDE.md`](../bench/SIDEBYSIDE.md).) The optional ML
prose path is capped by a fail-open budget (default 1.5 s) and never blocks
beyond it.


## Known limitations

- **Replacements must match the tool's output shape.** Claude Code schema-validates
  `updatedToolOutput` and silently keeps the original on mismatch; whittle rebuilds
  the tool's own response shape around the compressed text (verified live on Claude
  Code 2.1.203 — see [docs/hook-output-cap.md](hook-output-cap.md), which also
  documents why the once-assumed 10k output cap does NOT apply to replacements).
- **Prose needs the sidecar.** Without it, prose and markdown docs pass through
  unchanged; deterministic strategies are unaffected.
- **launchd is macOS-only.** Linux runs the daemon under systemd (unit in the README install notes).
- **Prose latency ceiling** (default 100000 chars) trades a hard cap for predictable
  in-path latency, sized for GPU/MPS inference (measured ~0.07s + 0.04s/KB on Apple
  silicon: 100KB ≈ 4s, within the 8s prose timeout). CPU-only machines run ~8x
  slower (~0.3s/KB) and should lower `WHITTLE_PROSE_MAX_CHARS` to ~12000, or large
  prose burns the timeout and passes through unchanged.

