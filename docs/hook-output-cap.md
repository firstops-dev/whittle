# Claude Code hook-output limits: what is actually true (P0.7, resolved)

Verified 2026-07-08 against Claude Code 2.1.203 (binary inspection + live
end-to-end test), after an audit question about the assumed "10k cap".

## The 10k cap does NOT apply to `updatedToolOutput`

Claude Code does cap hook output at 10,000 chars — but only on the
**context-injection** channels: `additionalContext`, `systemMessage`, and plain
stdout shown as hook context. Output above 10k on those channels is persisted
to a file and replaced with a preview.

`hookSpecificOutput.updatedToolOutput` is different: the hook's stdout JSON is
parsed **raw** (before any truncation) and the replacement is applied whole.
Empirically verified: a 20,643-char replacement landed intact in the transcript
and was read by the model, sentinels at both ends. The official hooks docs
specify no size limit for `updatedToolOutput` (checked: hooks reference, hooks
guide, Agent SDK reference, changelog).

whittle therefore emits replacements with **no size cap** (the win must still be
strictly smaller than the original in bytes and estimated tokens — that
invariant is unchanged, `TestFinalizeReplacement_PostHintInvariant`).

The earlier "docs-verified 10k cap on replacements" claim this file used to
carry was wrong — it conflated the context-injection cap with the replacement
channel. Pinned now by `TestHookHandler_BashShapePreserved_NoSizeCap`, which
pushes a >9.5k compressed replacement through the /hook handler.

## The REAL constraint: replacements are schema-validated

Claude Code validates `updatedToolOutput` against the tool's **output schema**
and, on mismatch, silently keeps the original output (an
`hook_error_during_execution` note appears in the transcript: "does not match
<Tool>'s output shape; using original output"). A bare string is rejected for
any tool with object-shaped output — Bash (`{stdout, stderr, interrupted,
isImage, noOutputExpected}`), Read (`{type, file:{filePath, content, ...}}`).

whittle handles this by rebuilding the tool's own response shape around the
compressed text: `server.ExtractToolText` returns the text plus a `rebuild`
function that swaps only the text-carrying field and passes every sibling field
through byte-exact (`server/toolresponse.go`). Both hook paths (HTTP `/hook`
and the `whittle hook` command) share it.

Watch item: shapes were captured from real 2.1.203 events; if a future Claude
Code version reshapes a tool's output, extraction fails closed (no replacement,
original preserved) — never a corrupting one — and the daemon logs
"no known text carrier … shape drift?" so the drift is visible.

Adjacent mechanism, not ours: Claude Code's Bash tool inlines at most ~30k
chars of stdout in tool_response; larger output is persisted to a file and
stdout becomes a preview with persistedOutputPath/persistedOutputSize
siblings. The preview is already truncated context, whittle's gate skips it —
so when testing whittle end-to-end, keep payloads under 30k or the hook never
sees the real content.
