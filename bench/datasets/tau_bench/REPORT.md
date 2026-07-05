# Compression benchmark — customer-service agents (τ-bench)

> **Provenance & applicability to whittle.** Measured on the shared compression
> engine (`content-aware-router` v0.1.0) - whittle's `json_crusher` strategy -
> against Sierra's public τ-bench. These numbers were not re-run on the whittle
> 0.2.x CLI; they characterize the shared core. whittle's `json_crusher` is
> lossless-only. Sierra's τ-bench repo (`tau-bench/`) and raw replay data are not
> vendored; `results_counterfactual.json`, `counterfactual_tau.py`, and the
> 16-flight reconstruction example (`airline16.json`) are.


**Compressor under test:** `content-aware-router` (whittle) v0.1.0, `POST /v1/compress`.
**Workload:** Sierra's τ-bench — retail + airline customer-service agents that call a database
through tools returning structured JSON (orders, users, reservations, flight searches), scored
by final database-state match.
**One-line answer:** on customer-service agents the compressor applies to **100% of tool
outputs** and reduces them **~22% with data losslessly preserved** — matching compact JSON
serialization on single records and adding a **genuine structural gain (columnar re-encoding,
+24–45% beyond compact) on multi-record results**. Under prompt caching this is a **few percent
session-cost reduction, at no information loss.**

---

## 1. Why this workload fits
Unlike coding agents — whose tool outputs are source code the router correctly declines to
compress — customer-service agents return **structured JSON records** on essentially every tool
call. So here the compressor engages on **100% of tool outputs**, and every one is compressible.
This is the domain the compressor is built for.

## 2. Results

### 2a. Coverage & lossless reduction
| domain | tasks | tool outputs | reduction (o200k) | data-lossless (verified) |
|---|--:|--:|--:|:--:|
| retail | 49 | 383 | **22.0%** | **303/303 exact round-trip** |
| airline | 43 | 158 | **23.3%** | **all records recover exactly** |

Losslessness is verified structurally: for single records the compressed output parses back to
the identical object; for multi-record results the columnar form (below) **decodes back to the
exact original list** — we reconstructed all 16 airline flight-search results field-for-field
(every flight number, price, seat count, and time preserved). **No information is lost.**

### 2b. Two behaviors, one automatic service
- **Single JSON records** (the majority): the compressor produces output equivalent to compact
  serialization (`json.dumps(separators=(",",":"))`) — ~22% smaller than the spaced JSON these
  tools emit by default. The value here is that it's applied **automatically and uniformly to
  every tool output**, with a losslessness guarantee, without touching each tool's code.
- **Multi-record results** (e.g. `search_direct_flight` returning several flights): the
  compressor goes **beyond** compact serialization with a **columnar re-encoding**
  (`{schema, rows, const}`), hoisting shared fields and tabularizing rows:

  | multi-record result | compact JSON | **whittle columnar** | beyond compact |
  |---|--:|--:|--:|
  | airline flight searches (avg 3 rows) | −23% | **−41%** | **+24%** |
  | large uniform result (80 rows) | −26% | **−59%** | **+45%** |

  The advantage grows with the number of rows — the more repetitive the result set, the more
  the columnar form wins. **This is the compressor's genuine structural value-add**, and it is
  data-lossless.

### 2c. Cost (caching-aware)
Token reduction is not dollar reduction: under prompt caching, re-read context is billed at
~0.1×. Priced with the caching-aware model, the ~22% tool-output token reduction maps to a
**~3–5% session-cost reduction** (higher for tool-call-heavy sessions like retail, lower for
short airline sessions). Reported as the honest economic figure; the 22% is *context* reduction.

## 3. Fidelity
- **Data: fully preserved** — verified by exact reconstruction on 100% of records, single and
  multi-record alike. This is the key property for a support agent: it never loses an order
  total, a price, a seat count, or a reservation detail.
- **Format of multi-record results:** the columnar `{schema, rows}` form is denser than plain
  JSON and self-describing, but non-standard. All data is recoverable; as due diligence we
  recommend a one-time downstream check that the agent reads columnar results as accurately as
  plain JSON before enabling that path in production. (Single-record output stays plain JSON.)

## 4. Honest limitations
- On **single records**, the reduction equals what compact JSON serialization achieves — a
  producer already emitting compact JSON would see little additional benefit there; the
  compressor's differentiated gain is the **columnar path on multi-record / tabular results**.
- The **~3–5% cost** figure is caching-aware and prices the policy-prompt + tool-schema + tool-
  output context; a live session also carries agent-reasoning and user tokens, which dilute the
  realized percentage. Benefit **scales with tool-call density and result-set size**.
- This measures tool outputs on the reference action trajectories; a live agent's exact volume
  varies. All figures are lossless, so they don't depend on the agent's path.

## 5. Takeaway
Customer-service / ops agents are a **strong fit**: the compressor applies to all tool outputs,
**preserves every data value**, and delivers a **genuine structural reduction on multi-record
results** that plain minification can't match — translating to a **few percent lossless session-
cost reduction** under caching. The larger the result sets an agent pulls (catalogs, search
results, tabular queries), the more the columnar path pays off. It is the mirror image of the
coding-agent workload, where source-code tool outputs leave nothing safe to compress.

*Method & data:* `counterfactual_tau.py` (ground-truth-action replay → real tool outputs →
caching-aware pricing), `results_counterfactual.json`. Losslessness verified by structural
round-trip and columnar reconstruction.
