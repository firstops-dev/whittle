# The 10k hook-output cap (P0.7)

Claude Code caps hook-replaced output (updatedToolOutput) at 10,000 characters for BOTH command and http hooks (docs-verified); larger output is
diverted to a file and replaced with a preview. whittle therefore fails open
(no replacement) when the COMPRESSED output exceeds ~9.5k chars — the largest
tool outputs, where savings are biggest, currently pass through via the hook.

Options, in preference order:
1. Upstream ask: raise/remove the cap for hookSpecificOutput.updatedToolOutput
   (it replaces existing content — it does not grow the transcript). Filed with
   the Claude Code team.
2. Two-step replacement: emit a <=9.5k head + a marker; agent Reads the rest
   from a whittle-served file. Changes semantics; needs design.
3. Segmented compression: compress only until the result fits the cap
   (partial win, honest marker). Cheap; candidate for next release.

Until resolved: documented in GUARANTEES.md as a known limitation.
