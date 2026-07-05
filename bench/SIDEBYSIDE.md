# Side-by-side: headroom vs whittle (identical frozen bytes)

Ten files in `bench/corpus_headroom/*.json` (headroom's own benchmark-generator
output, frozen with `random.seed(42)` — see `corpus_headroom/PROVENANCE.md`)
were compressed by both tools on byte-identical input, with no per-tool tuning.

## Results

| file | input_tokens | headroom_out | whittle_out | headroom_reduction | whittle_reduction | winner |
|---|---:|---:|---:|---:|---:|---|
| agentic_conversation_anthropic.json | 5,336 | 4,985 | 4,902 | 6.58% | 8.13% | whittle |
| agentic_conversation_openai.json | 8,306 | 8,306 | 7,955 | 0.00% | 4.23% | whittle |
| api_responses.json | 11,084 | 6,621 | 5,734 | 40.27% | 48.27% | whittle |
| database_rows_mixed.json | 12,953 | 6,464 | 8,738 | 50.10% | 32.54% | headroom |
| file_search_data.json | 14,968 | 6,317 | 7,238 | 57.80% | 51.64% | headroom |
| log_data.json | 16,183 | 8,887 | 10,454 | 45.08% | 35.40% | headroom |
| log_entries.json | 14,255 | 8,442 | 8,850 | 40.78% | 37.92% | headroom |
| metrics_data.json | 17,874 | 8,287 | 9,501 | 53.64% | 46.84% | headroom |
| rag_conversation.json | 2,780 | 2,780 | 2,684 | 0.00% | 3.45% | whittle |
| search_results.json | 12,769 | 6,742 | 7,963 | 47.20% | 37.64% | headroom |
| **TOTAL** | **116,508** | **67,831** | **74,019** | **41.79%** | **36.48%** | headroom (aggregate) |

Headroom wins 6/10 files (all the plain-JSON tool-output arrays: api_responses,
database_rows_mixed, file_search_data, log_data, log_entries, metrics_data,
search_results — 7 files actually route to headroom's SmartCrusher, of which
6 beat whittle and 1 — api_responses — whittle edges out). Whittle wins on the
3 already-conversation-shaped files (both agentic_conversation_* and
rag_conversation), where headroom's default `protect_recent` /
`skip_user_messages` behavior leaves most or all of the conversation
untouched (2 of the 3 show **zero** headroom compression at default config),
while whittle still compresses the JSON payload embedded in the tool-result
turns.

No file errored in either tool. No fabricated numbers — every cell above is
a direct tiktoken count of a captured tool output.

## Methodology

**Date of run:** 2026-07-05

**Packages installed** (fresh venv, `/tmp/hr-bench`):
- `headroom-ai==0.30.0` (`pip install headroom-ai`; this is the package that
  installs the importable `headroom` module — the bare `headroom` PyPI
  name was not tried since `headroom-ai` succeeded on the first attempt and
  is what upstream's own README documents as the pip package name)
- `tiktoken==0.13.0` (pulled in transitively by `headroom-ai`'s own
  tokenizer dependency; also pinned explicitly)

**Whittle:** binary self-reports `whittle 0.1.0` via `whittle version` and
the running daemon's `/v1/compress` response echoes `"version":"0.1.0"`.
`git describe --tags` on the whittle repo resolves to `v0.2.1-3-g725ad42`
(3 commits past the `v0.2.1` tag; the in-binary version string has not been
bumped past `0.1.0`). Daemon was already running locally on
`127.0.0.1:45871` (`whittle daemon`, launched via `whittle setup`) —
reused as-is, not restarted or reconfigured for this benchmark.

**Headroom API used — exact call, no tuning:**

Headroom's one-function library API is `headroom.compress(messages, model=...)`
(the "Quick Start" / "simplest way to use Headroom" entry point documented at
the top of `headroom/compress.py` and re-exported from `headroom/__init__.py`).
No `CompressConfig` was constructed and no kwargs were passed besides `model`
— i.e. every default (`compress_user_messages=False`, `protect_recent=4`,
`target_ratio=None`, `min_tokens_to_compress=250`, etc.) is headroom's
out-of-the-box default.

Two of the ten corpus files (`agentic_conversation_anthropic.json`,
`agentic_conversation_openai.json`) and one (`rag_conversation.json`) are
already JSON arrays of `{"role": ..., "content": ...}` dicts — i.e. already
in headroom's native message-array shape. For those 3 files the parsed
array was passed directly:

```python
messages = json.loads(raw_file_text)
result = compress(messages, model="claude-sonnet-4-5-20250929")
```

The other 7 files are plain JSON data arrays (log entries, DB rows, search
hits, etc — not message arrays). For those, headroom has no bare
"compress this string" function; the closest legitimate, documented
entry point is the tool-output message shape shown in `compress.py`'s own
docstring (`messages = [{"role": "user", "content": "..."}, {"role": "tool",
"content": big_data}]`). Each raw file's exact bytes were wrapped as a single
tool-output message, unmodified:

```python
messages = [{"role": "tool", "content": raw_file_text}]
result = compress(messages, model="claude-sonnet-4-5-20250929")
```

`model="claude-sonnet-4-5-20250929"` was passed only because `compress()`
requires a `model` argument for its internal token counting / context-limit
logic (it defaults to this same value); it does not change compression
behavior, gate no `CompressConfig` field on model identity beyond that. No
other kwarg was set.

**Output-text extraction (for tokenizing headroom's output):**
- Conversation-shaped files: `json.dumps(result.messages, ensure_ascii=False,
  indent=2)`. Verified the frozen input files are themselves exactly
  `json.dumps(data, indent=2)` (byte-identical round-trip checked
  programmatically), so the output was serialized the same way — this
  avoids "compression" that would otherwise just be an artifact of
  stripping/adding whitespace.
- Wrapped tool-output files: `result.messages[0]["content"]` directly (a
  plain string in every case observed — SmartCrusher returned compact
  schema+row text, not restructured content blocks).

**Tokenizer:** `tiktoken.get_encoding("o200k_base")`, applied identically to:
(a) the raw frozen file bytes, decoded as UTF-8 text, for `input_tokens`;
(b) the headroom output text as extracted above, for `headroom_out_tokens`;
(c) the `"compressed"` field of whittle's JSON response, for
`whittle_out_tokens`. Same tokenizer, same encoding, all three counts.

**Whittle call:** `POST http://127.0.0.1:45871/v1/compress`, JSON body
`{"content": <raw file text, verbatim>, "min_tokens": 0}`. `compressed` field
used as the output text; `action` / `skip_reason` recorded verbatim in
`sidebyside.json`. No file was skipped by whittle (`min_tokens=0` disables
the length gate; all 10 came back `"action":"compressed"`).

**Reduction formula (identical for both tools):**
`reduction = (input_tokens - out_tokens) / input_tokens`, using the single
shared `input_tokens` value per file (same frozen bytes, same tokenizer) as
the denominator for both tools' reduction — so the two reduction columns are
directly comparable per row.

**Honesty notes:**
- Both packages installed cleanly on the first attempt; no fallback or
  substitution was needed.
- All 10 files produced a valid response from both tools; no cell in
  `sidebyside.json` is empty or fabricated.
- Headroom's own internal token accounting (its own tokenizer, reported on
  `CompressResult.tokens_before` / `tokens_after`) is recorded verbatim in
  each file's `notes` field in `sidebyside.json` for cross-reference; it
  differs somewhat from the external tiktoken o200k_base counts used for the
  headline numbers above (different tokenizer + different text scope: its
  internal counter sums per-message `content` lengths, not the full
  serialized JSON envelope) — this is expected and both tools' headline
  numbers in this report use the *same* external tiktoken methodology, not
  either tool's self-reported numbers.
- On `agentic_conversation_openai.json` and `rag_conversation.json`, headroom
  applied **zero** transforms at default config
  (`router:protected:user_message` / `router:protected:error_output` on
  every message) — reported as 0.00% reduction, not omitted, per the
  honesty rule against silently dropping unfavorable rows.

## Latency

Both tools measured **in-process, as libraries** — no HTTP, no daemon, no
subprocess — on the same 10 frozen files, same machine, 1 warmup call + 5
timed calls per file, median wall-clock reported.

| file | input_tokens | headroom-ai 0.30.0 median ms | whittle 0.2.1 median ms |
|---|---:|---:|---:|
| agentic_conversation_anthropic.json | 5,336 | 0.225 | 1.358 |
| agentic_conversation_openai.json | 8,306 | 0.167 | 1.666 |
| api_responses.json | 11,084 | 2.702 | 2.727 |
| database_rows_mixed.json | 12,953 | 3.001 | 2.732 |
| file_search_data.json | 14,968 | 3.202 | 2.782 |
| log_data.json | 16,183 | 3.188 | 2.623 |
| log_entries.json | 14,255 | 2.921 | 2.115 |
| metrics_data.json | 17,874 | 2.947 | 2.308 |
| rag_conversation.json | 2,780 | 0.063 | 0.331 |
| search_results.json | 12,769 | 3.074 | 2.409 |
| **SUM (10 files)** | **116,508** | **21.490** | **21.051** |
| **MEDIAN-of-medians** | — | **2.934** | **2.359** |
| **MEAN** | — | **2.149** | **2.105** |

Aggregate latency is close to parity — whittle's sum across all 10 files
(21.051 ms) is about 2% lower than headroom's (21.490 ms) — but the shape
differs by file class:

- On the 3 already-conversation-shaped files (both `agentic_conversation_*`
  and `rag_conversation`), **headroom is faster**, often by an order of
  magnitude (e.g. 0.063 ms vs 0.331 ms on `rag_conversation.json`). These are
  the same files where headroom's router hits its `protected:*` fast path at
  default config (see Results above) and does little or no transform work —
  the low latency and the near-zero reduction on these files are the same
  phenomenon. whittle's per-call floor (JSON detection + gate + strategy
  dispatch) does not have an equivalently cheap early-exit for small inputs,
  so it pays roughly the same ~1.3–1.7 ms on these small files as on inputs
  several times their size.
- On the 7 plain-JSON tool-output files, **whittle is faster** on 6 of 7
  (all but `api_responses.json`, which is a near-tie: 2.702 ms vs 2.727 ms).
  Both tools land in the same 2–3.2 ms band on these files; whittle's
  per-file cost stays flatter across the 11k–18k token range (2.1–2.8 ms)
  than headroom's (2.7–3.2 ms).

No file failed or was skipped by either tool during latency measurement.

### Latency methodology

**Machine:** Apple M-series (macOS 15.7.2, arm64). Same machine, same corpus
files, same run session as the reduction benchmark above.

**What was timed:** only the compress call itself — file reads, venv/module
import, and (for whittle) `Engine` construction happen once, outside the
timed loop, and are excluded. For each file: 1 untimed warmup call, then 5
timed calls; median of the 5 is reported. No HTTP was used on either side —
this differs from the reduction benchmark above, which called whittle over
its `/v1/compress` daemon endpoint; for latency, whittle was measured as a
Go library instead, to compare compute cost against compute cost rather than
compute cost against compute-plus-network-plus-daemon.

**headroom side (`headroom-ai==0.30.0`, reused venv at `/tmp/hr-bench`):**
same API and same message-wrapping rule as the reduction benchmark —
`headroom.compress(messages, model="claude-sonnet-4-5-20250929")`, default
`CompressConfig`. The 3 conversation-shaped files
(`agentic_conversation_anthropic.json`, `agentic_conversation_openai.json`,
`rag_conversation.json`) were passed as parsed message arrays directly; the
other 7 plain-JSON data files were wrapped as
`messages=[{"role": "tool", "content": <raw file bytes>}]`. Timed with
`time.perf_counter()`.

**whittle side (`v0.2.1`, git describe `v0.2.1-4-gb5b419f`):** a standalone
Go program at `/tmp/wbench/main.go` (module `wbench`, `go.mod` with
`replace github.com/firstops-dev/whittle => /Users/anshal/dev/whittle`,
built with `GOPATH=$HOME/go`) imports `github.com/firstops-dev/whittle`,
constructs **one** `whittle.Engine` via
`whittle.New(whittle.Options{ModelURL: "", MinTokens: 0})` (empty
`ModelURL` — no ML prose sidecar; `MinTokens: 0` disables the length floor,
matching the daemon call's `min_tokens: 0` in the reduction benchmark), and
reuses that single engine across every file and every call. Each file's raw
bytes are read once (`os.ReadFile`) and passed directly to
`eng.Compress(ctx, string(content))` — no proto/JSON envelope, no daemon
round trip. Timed with `time.Now()` / `time.Since()`.

**Honesty notes:**
- Both benchmarks completed with zero errors on all 10 files; every cell in
  the table above and in `sidebyside.json`'s `latency_ms` field is a
  measured value, not estimated or backfilled.
- A second full pass of both benchmarks (not the recorded run) showed
  run-to-run variance: headroom's medians shifted by roughly ±0.2 ms;
  whittle's shifted more on the two smallest, conversation-shaped files
  (down to ~0.7–1.1 ms on a second pass, vs 1.358–1.666 ms on the recorded
  pass) and one individual timed run (not the median) hit a ~6.6 ms outlier,
  almost certainly Go GC-related, on `agentic_conversation_anthropic.json`.
  The table above reports one designated run per tool (1 warmup + 5 timed,
  median of the 5) as specified, not the best or an average of repeated
  passes.
- Where headroom is faster, that is reported as-is (see the 3
  conversation-shaped files above); where whittle is faster, that is also
  reported as-is (6 of the 7 plain-JSON files). Neither tool wins across the
  board.

## Reproduce

```bash
python3 -m venv /tmp/hr-bench
source /tmp/hr-bench/bin/activate
pip install headroom-ai tiktoken requests

# whittle daemon must already be listening on :45871
# (whittle setup / whittle daemon)

python3 - <<'PY'
import json, requests, tiktoken
from headroom import compress

enc = tiktoken.get_encoding("o200k_base")
tok = lambda s: len(enc.encode(s, disallowed_special=()))

for fname in sorted(__import__("os").listdir("bench/corpus_headroom")):
    if not fname.endswith(".json"):
        continue
    raw = open(f"bench/corpus_headroom/{fname}").read()
    data = json.loads(raw)
    conv = isinstance(data, list) and all(isinstance(m, dict) and "role" in m for m in data)
    messages = data if conv else [{"role": "tool", "content": raw}]
    result = compress(messages, model="claude-sonnet-4-5-20250929")
    out_text = (json.dumps(result.messages, indent=2) if conv
                else result.messages[0]["content"])
    wt = requests.post("http://127.0.0.1:45871/v1/compress",
                        json={"content": raw, "min_tokens": 0}).json()
    print(fname, tok(raw), tok(out_text), tok(wt["compressed"]))
PY
```
