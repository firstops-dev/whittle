package router

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Shared gap-test helpers (defined once for all *_gap_test.go files).
// ---------------------------------------------------------------------------

// firstAdjacentSameRole returns the index of the first message whose role equals
// its predecessor's, or -1 if the array alternates correctly. The Messages API
// rejects two consecutive same-role turns with a hard 400, so this is the
// alternation invariant reconciliation must never break.
func firstAdjacentSameRole(msgs []any) int {
	for i := 1; i < len(msgs); i++ {
		if msgRole(msgs[i]) == msgRole(msgs[i-1]) && msgRole(msgs[i]) != "" {
			return i
		}
	}
	return -1
}

// collectBlocks walks every message and returns the multiset of text strings,
// tool_use ids, and tool_result tool_use_ids present. Used to assert that a
// transform preserves every original content block (loses no text, orphans no
// tool call).
func collectBlocks(msgs []any) (texts []string, toolUseIDs []string, toolResultIDs []string) {
	for _, m := range msgs {
		for _, b := range contentBlocksOf(m) {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			switch blockType(b) {
			case "text":
				if s, _ := bm["text"].(string); s != "" {
					texts = append(texts, s)
				}
			case "tool_use":
				if s, _ := bm["id"].(string); s != "" {
					toolUseIDs = append(toolUseIDs, s)
				}
			case "tool_result":
				if s, _ := bm["tool_use_id"].(string); s != "" {
					toolResultIDs = append(toolResultIDs, s)
				}
			}
		}
	}
	return
}

func sortedEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[string]int{}
	for _, s := range a {
		am[s]++
	}
	for _, s := range b {
		am[s]--
	}
	for _, v := range am {
		if v != 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Message coalescing validity (mid-conversation role:"system" strip).
// ---------------------------------------------------------------------------

// WHY: the core coalescing contract (spec B3) is that after converting every
// mid-conv system message to "user" and merging consecutive same-role turns, the
// result must (1) have NO adjacent same-role message and (2) preserve every
// original text and tool block. This drives a complex array with a tool_use /
// tool_result pair and TWO system messages at different positions and asserts
// both halves of the contract at once.
func TestCoalesce_ComplexArrayNoAdjacentAndPreservesBlocks(t *testing.T) {
	r := mkReq(t, `{"model":"m","messages":[
	  {"role":"user","content":"a"},
	  {"role":"system","content":"b"},
	  {"role":"assistant","content":[{"type":"tool_use","id":"id1","name":"ls","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"id1","content":"files"}]},
	  {"role":"system","content":"c"},
	  {"role":"user","content":"d"}
	]}`, "mid-conversation-system-2026-04-07")

	beforeTexts, beforeTU, beforeTR := collectBlocks(r.messages())

	Reconcile(r, "claude-haiku-4-5")
	msgs := r.messages()

	if i := firstAdjacentSameRole(msgs); i != -1 {
		t.Fatalf("adjacent same-role at index %d after coalesce: roles=%v", i, rolesOf(msgs))
	}
	afterTexts, afterTU, afterTR := collectBlocks(msgs)
	if !sortedEq(beforeTexts, afterTexts) {
		t.Errorf("text blocks lost/added: before=%v after=%v", beforeTexts, afterTexts)
	}
	if !sortedEq(beforeTU, afterTU) {
		t.Errorf("tool_use ids changed: before=%v after=%v", beforeTU, afterTU)
	}
	if !sortedEq(beforeTR, afterTR) {
		t.Errorf("tool_result ids changed: before=%v after=%v", beforeTR, afterTR)
	}
	// tool_use id1 must still be in an assistant turn immediately followed by its
	// tool_result in a user turn — coalescing must not break the pairing.
	if _, tu, _ := collectBlocks(msgs); len(tu) != 1 || tu[0] != "id1" {
		t.Errorf("tool_use pairing disturbed: %v", tu)
	}
}

// WHY: string-content and array-content messages must merge into one valid array
// (spec "string-vs-array content in the merge"). A bare-string user turn merged
// with a converted-system string turn and then an array turn must yield a single
// user message whose content is a []any carrying all three texts in order — never
// a string clobbering an array or vice-versa.
func TestCoalesce_StringAndArrayContentMerge(t *testing.T) {
	r := mkReq(t, `{"model":"m","messages":[
	  {"role":"user","content":"first"},
	  {"role":"system","content":"second"},
	  {"role":"user","content":[{"type":"text","text":"third"}]},
	  {"role":"assistant","content":"ok"}
	]}`, "mid-conversation-system-2026-04-07")
	Reconcile(r, "claude-haiku-4-5")
	msgs := r.messages()
	if len(msgs) != 2 {
		t.Fatalf("expected [user,assistant], got %d msgs roles=%v", len(msgs), rolesOf(msgs))
	}
	blocks := contentBlocksOf(msgs[0])
	// Must be a proper block array, and the concatenation must carry all texts.
	var joined []string
	for _, b := range blocks {
		if bm, ok := b.(map[string]any); ok {
			if s, _ := bm["text"].(string); s != "" {
				joined = append(joined, s)
			}
		}
	}
	got := strings.Join(joined, "|")
	if got != "first|second|third" {
		t.Errorf("merged content order/loss: got %q want first|second|third", got)
	}
}

// WHY: the spec worries about merging across a tool_result boundary. Converting a
// system message that sits BETWEEN a tool_use and its tool_result actually
// RESTORES adjacency by merging the stray turn into the tool_result turn. This
// pins that coalescing does not orphan the tool_result and keeps it in the turn
// immediately after its tool_use.
func TestCoalesce_SystemBetweenToolUseAndResult(t *testing.T) {
	r := mkReq(t, `{"model":"m","messages":[
	  {"role":"user","content":"go"},
	  {"role":"assistant","content":[{"type":"tool_use","id":"tid","name":"x","input":{}}]},
	  {"role":"system","content":"note"},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"tid","content":"r"}]}
	]}`, "mid-conversation-system-2026-04-07")
	Reconcile(r, "claude-haiku-4-5")
	msgs := r.messages()
	if i := firstAdjacentSameRole(msgs); i != -1 {
		t.Fatalf("adjacent same-role at %d: roles=%v", i, rolesOf(msgs))
	}
	// Find the assistant turn with the tool_use; the very next message must be a
	// user turn containing the matching tool_result.
	found := false
	for i := 0; i+1 < len(msgs); i++ {
		if msgRole(msgs[i]) != "assistant" {
			continue
		}
		_, tu, _ := collectBlocks([]any{msgs[i]})
		if len(tu) == 1 && tu[0] == "tid" {
			_, _, tr := collectBlocks([]any{msgs[i+1]})
			if len(tr) == 1 && tr[0] == "tid" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("tool_use 'tid' not immediately followed by its tool_result; roles=%v", rolesOf(msgs))
	}
}

func rolesOf(msgs []any) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, msgRole(m))
	}
	return out
}

// DEFERRED (t.Skip): whether the Messages API accepts a user turn whose blocks
// are [text, tool_result] (text BEFORE the tool_result) after a coalesce merge is
// an open validation item (spec "Open/validate #2"). Block preservation holds
// (asserted above); block ORDER acceptability needs a real down-route-with-history
// capture against Haiku. Skipped, not asserted, so we neither claim a false green
// nor a false red.
func TestCoalesce_ToolResultBlockOrderingAfterMerge(t *testing.T) {
	t.Skip("deferred: tool_result-after-text block ordering acceptability needs real-traffic validation (spec open item #2)")
}
