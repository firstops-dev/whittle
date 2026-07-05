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
