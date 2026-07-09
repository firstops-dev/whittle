# whittle benchmarks

The receipts behind the README's numbers. Every figure is regenerable from this repo.

Three tiers, in increasing order of realism - every number regenerable from
this repo (`go run ./bench` for the deterministic rows; the prose row needs the
model sidecar), reductions on an estimated-token basis (labeled).

### 1. Synthetic corpus (ours - headline per content class)

Authored fixtures in [`corpus/`](corpus/), designed to exercise each
strategy and its guarantees. Full table: [`REPORT.md`](REPORT.md).

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
corpora, so we froze what their numbers are computed on; `corpus_headroom/`,
PROVENANCE.md). Both tools ran on identical bytes, defaults only, measured with
the same tokenizer. Full table + methodology: [`SIDEBYSIDE.md`](SIDEBYSIDE.md).

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

### 3. Real-world: customer-service agents (two independent evaluations)

Customer-service agents are whittle's strong-fit workload - their tool outputs
are structured JSON on essentially every call, so the compressor engages on
**100% of tool outputs**, all losslessly. Two evaluations, corroborating at
different scales:

**Breadth** - [Salesforce APIGen-MT-5k](https://huggingface.co/datasets/Salesforce/APIGen-MT-5k),
5,000 verified multi-turn sessions: **22% tool-output reduction (o200k) at zero
measured information loss**, verified two ways - mechanically lossless on
**15,846 / 15,846** compressed items, and a blinded 4-judge panel finding
**0 / 120** material loss on the lossy prose path (honeypot-validated, Gwet AC2
0.96-1.00). [`datasets/salesforce_customer_support/`](datasets/salesforce_customer_support/)

**Depth** - Sierra's [τ-bench](datasets/tau_bench/) (retail + airline),
counterfactual replay of reference trajectories: **22.0% / 23.3% reduction, every
record reconstructed field-for-field** (all 16 flight-search results recovered
exactly). This eval isolates whittle's real structural value-add - on
**multi-record results the columnar re-encoding goes beyond compact JSON: +24%
on small flight searches, +45% on an 80-row result**, and the advantage grows
with result size. [`datasets/tau_bench/`](datasets/tau_bench/)

The product metric is **token/context reduction**: fewer tool-output tokens,
removed from every later turn's context, compounding as the session grows.
Dollar impact is a separate, caching-dependent question both reports publish in
full: under prompt caching the same cut is ~3-5% of session cost (cheap
cache-reads dominate the bill), so token savings and dollar savings are not the
same thing.

*(Both measured on the shared compression engine `content-aware-router` v0.1.0;
whittle's `json_crusher` is lossless-only. See each report's provenance note.)*

