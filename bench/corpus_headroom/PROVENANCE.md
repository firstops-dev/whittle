# Provenance: Headroom benchmark-generator corpus (frozen)

This directory contains **frozen outputs** of Headroom's own benchmark data
generators, captured once so that whittle and headroom can be benchmarked
side-by-side on byte-identical inputs. Nothing here is hand-authored; every
file is the literal (JSON-serialized) return value of an unmodified upstream
generator function, run with `random.seed(42)`.

## Source

- Repo: `github.com/headroomlabs-ai/headroom`
- Commit: `e8151f059b4a9ba3fa43c7c67a7d310af08c1f3d`
- License: Apache-2.0 (per upstream repo)
- Fetched via: `gh api repos/headroomlabs-ai/headroom/contents/<path>?ref=e8151f059b4a9ba3fa43c7c67a7d310af08c1f3d`

Files fetched (verbatim, unmodified except where noted below):
- `benchmarks/scenarios/tool_outputs.py`
- `benchmarks/scenarios/conversations.py`
- `benchmarks/compression_benchmark.py` (inline data generators only — see below)

## Dependency check

`tool_outputs.py` and `conversations.py` import only stdlib (`random`, `uuid`,
`json`, `datetime`, `typing`) plus each other (`conversations.py` imports
`generate_search_results`, `generate_log_entries`, `generate_database_rows`,
`generate_api_responses` from `tool_outputs.py`, a sibling module in the same
package — not the installed `headroom` package). Both files were run
**unmodified** with no need to install headroom's package.

`compression_benchmark.py` imports `openai` and the `headroom` package itself
under `try/except ImportError` guards for its LLM-calling and
transform-comparison code, but its three synthetic **data generators**
(`generate_log_data`, `generate_file_search_data`, `generate_metrics_data`)
only use stdlib (`hashlib`, `random`, `dataclasses`, `typing`). Those three
functions — plus the small `Question` dataclass they're type-annotated
against (also stdlib-only, defined in the same file) — were copied
byte-for-byte into a standalone script. Everything else in that file (OpenAI
client calls, headroom/kompress transform invocations, benchmark-runner
loop) was omitted since none of it is needed to produce the data and none of
it was installed or run.

**Nothing from the `headroom` package (the tool under benchmark) was
imported, installed, or executed to produce this corpus** — only the
benchmark repo's own stdlib-only data-generation code.

## Generation

- Python 3.13.5, generation date: 2026-07-04
- `random.seed(42)` set immediately before each generator call (so each file
  is independently reproducible from that seed; UUIDs via `uuid.uuid4()` are
  not seeded by `random.seed` — they use OS randomness — so the specific
  UUID strings in `search_results.json`, `*_conversation*.json`, and
  `log_entries.json` (trace/call/tool-use IDs) will differ across runs, but
  the overall structure/shape/size is what's being frozen and benchmarked).
- Serialization: every generator returns a plain Python `list[dict]` (or
  `list[dict]` for messages). Each was serialized with
  `json.dumps(obj, indent=2)` and written as-is — no reformatting, key
  reordering, or content editing after generation.
- Scratch driver scripts (not part of this corpus, kept only in the
  ephemeral scratch dir used to generate these files):
  `gen_tool_outputs.py`, `gen_compression_benchmark.py`.

## Files and their generator

| File | Generator function | Source file | Params |
|---|---|---|---|
| `search_results.json` | `generate_search_results` | `tool_outputs.py` | `n=120, include_uuid_needles=3, include_errors=2` |
| `log_entries.json` | `generate_log_entries` | `tool_outputs.py` | `n=150, include_errors=8, include_critical=2` |
| `api_responses.json` | `generate_api_responses` | `tool_outputs.py` | `n=90` |
| `database_rows_mixed.json` | `generate_database_rows` | `tool_outputs.py` | `n=180, table_type="mixed"` |
| `agentic_conversation_openai.json` | `generate_agentic_conversation` | `conversations.py` | `turns=5, tool_calls_per_turn=1, items_per_tool_response=10` (OpenAI message format; internally calls `tool_outputs.py` generators for tool responses) |
| `agentic_conversation_anthropic.json` | `generate_anthropic_agentic_conversation` | `conversations.py` | `turns=4, tool_calls_per_turn=1, items_per_tool_response=8` (Anthropic content-block format) |
| `rag_conversation.json` | `generate_rag_conversation` | `conversations.py` | `context_tokens=1800, num_queries=4` |
| `log_data.json` | `generate_log_data` | `compression_benchmark.py` (data list only) | `n_entries=260` |
| `file_search_data.json` | `generate_file_search_data` | `compression_benchmark.py` (data list only) | `n_files=300` |
| `metrics_data.json` | `generate_metrics_data` | `compression_benchmark.py` (data list only) | `n_points=300` |

Note on `log_data.json` / `file_search_data.json` / `metrics_data.json`:
these three generators in upstream `compression_benchmark.py` return a tuple
`(data: list[dict], questions: list[Question])`. Only the `data` list — the
tool-output-shaped payload that would actually be compressed — is frozen
here. The `questions` list is upstream's own benchmark-scoring metadata
(ground-truth Q&A used to grade LLM answer quality), not tool-output data,
so it is out of scope for this corpus and was discarded after generation.

## Why this matters

These bytes are **frozen**: both whittle and headroom's own compressors can
now be run against the exact same corpus files, byte-for-byte, so
compression-ratio / fidelity comparisons in whittle's side-by-side benchmark
are not confounded by different random seeds, package versions, or
generator drift between the two tools.

## Skips

None. All targeted generators (`generate_search_results`,
`generate_log_entries`, `generate_api_responses`, `generate_database_rows`,
`generate_agentic_conversation`, `generate_anthropic_agentic_conversation`,
`generate_rag_conversation`, `generate_log_data`, `generate_file_search_data`,
`generate_metrics_data`) ran standalone without needing the `headroom`
package installed, so nothing was skipped.
