# Why write-time compression, not read-time

Whittle's core position, in full. (The [README](../README.md) carries the short version.)

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

**This argument is about compression, not about routing.** Whittle's opt-in
[model router](../README.md#model-routing-opt-in) *is* a request-path proxy - but it exists
for a different job (send each request to the cheapest capable tier), and it is
deliberately the minimal kind. It rewrites only the model field and reconciles
capabilities; it never rewrites conversation history, so it inherits none of the
prompt-cache surgery above. It forwards your credentials untouched rather than
terminating them, and it fails open to your original request, so an outage is a
passthrough, not an agent outage. The read-time-compression proxy is the pattern
whittle rejects; a minimal, history-preserving, fail-open router is a different
tool that happens to share the address bar.

