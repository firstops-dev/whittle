# Guarantees — and the tests that pin them

Every claim below is enforced by an executable test in this repo. If a test
below fails, the guarantee is broken and the build is red. Issues that report a
violated guarantee become pinned regression tests before they are closed.

| guarantee | pinned by |
|---|---|
| JSON compression is lossless: reconstruct == input, byte-exact values, no row ever dropped | `compress/compressors`: `assertNoMutation` reconstruction fuzzing; `TestJSONCrusher_UnionSparseLossless`, `TestJSONCrusher_CSVEncodingLossless`, `TestJSONCrusher_NumericPrecisionByteExact`, `TestJSONCrusher_ConstantFactoring` |
| absent key vs present-null vs empty string are always distinguished | `TestJSONCrusher_UnionSparseLossless`, CSV null-column tests |
| log compression never drops lines silently — omissions are marked and exactly accounted | `TestLogCompressor_OmissionMarker` (kept + omitted == input lines) |
| source code never reaches the lossy prose model | `TestLineNumbered_NeverFiresOnCode` (18-language suite incl. prosey YAML/Dockerfile/Makefile variants); `TestSegmentMarkdown_VerbatimClasses` (unfenced SQL/C/lisp) |
| markdown compression restores code fences/tables/lists byte-exact, or fails open | `TestMarkdownStructured_MasksAndRestores`, `TestMarkdownStructured_SentinelLossFailsOpen` |
| terminal CR collapse never corrupts UTF-8 and never destroys `\r`-delimited data | `TestCROverwriteCollapse_UTF8` (utf8.ValidString), `TestCROverwriteCollapse_DataRecordsSafe` |
| output is never larger than input — in bytes AND estimated tokens | pipeline guardrail tests incl. `TestPipelineTokenGuardrail` |
| every failure fails open: original bytes, never an error, never partial output | pipeline fail-open/empty-output/panic-recovery contract tests; hook exits 0 silently on any problem |
| the Claude Code hook can never break a tool call | `cmd/whittle` hook: fail-open on malformed events, router downtime, oversized output (Claude Code's 10k hook-stdout cap) |
| stats are local-only | `whittle stats` reads `~/.whittle/stats.jsonl`; nothing is transmitted, ever (see PLAN.md non-goals) |

Known, documented limitations: compressed outputs larger than ~9.5k chars are
not replaced via the hook (upstream 10k hook-output cap); the ML prose path is
lossy by design and opt-in.
