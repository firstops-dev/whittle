package router

import (
	"strings"
	"testing"
)

// AC1/AC2/AC5: basic scalar signals from a simple request.
func TestExtract_BasicSignals(t *testing.T) {
	body := `{
	  "model": "claude-opus-4-8-20260101",
	  "messages": [{"role":"user","content":"hello world"}],
	  "tools": [{"name":"Bash"}]
	}`
	s, err := Extract([]byte(body), "sess-abc", InspectCfg{Scope: "full"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.RequestedModel != "claude-opus-4-8" {
		t.Errorf("RequestedModel = %q, want canonicalized claude-opus-4-8", s.RequestedModel)
	}
	if !s.HasTools {
		t.Error("HasTools should be true")
	}
	if s.MessageCount != 1 {
		t.Errorf("MessageCount = %d, want 1", s.MessageCount)
	}
	if s.SessionID != "sess-abc" {
		t.Errorf("SessionID = %q", s.SessionID)
	}
	if s.LastUserText != "hello world" {
		t.Errorf("LastUserText = %q", s.LastUserText)
	}
	if s.ContextTokens != len(body)/4 {
		t.Errorf("ContextTokens = %d, want %d", s.ContextTokens, len(body)/4)
	}
}

// AC3: ToolLoop is precise — true ONLY when the last message is a user
// tool_result, false when an assistant merely emitted a tool_use earlier.
func TestExtract_ToolLoopPrecise(t *testing.T) {
	// Last message is a user tool_result → loop.
	loop := `{"model":"m","messages":[
	  {"role":"user","content":"do it"},
	  {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}
	]}`
	s, _ := Extract([]byte(loop), "", InspectCfg{Scope: "full"})
	if !s.ToolLoop {
		t.Error("expected ToolLoop=true when last msg is a user tool_result")
	}

	// Assistant emitted a tool_use, but the LAST message is a normal user turn.
	notLoop := `{"model":"m","messages":[
	  {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},
	  {"role":"user","content":"now do something else"}
	]}`
	s2, _ := Extract([]byte(notLoop), "", InspectCfg{Scope: "full"})
	if s2.ToolLoop {
		t.Error("expected ToolLoop=false when last msg is a plain user turn")
	}
}

// AC4: text extraction excludes tool_result/thinking blocks and walks back past
// a trailing tool_result turn to the last genuine human text.
func TestExtract_TextExcludesNonUserBlocks(t *testing.T) {
	body := `{"model":"m","messages":[
	  {"role":"user","content":"fix the race condition in cart.go"},
	  {"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"on it"}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"FAIL: deadlock"}]}
	]}`
	s, _ := Extract([]byte(body), "", InspectCfg{Scope: "last_user_turn"})
	if s.LastUserText != "fix the race condition in cart.go" {
		t.Errorf("LastUserText should be the human text, walking past the tool_result turn; got %q", s.LastUserText)
	}
	if strings.Contains(s.RecentText, "FAIL") || strings.Contains(s.RecentText, "on it") {
		t.Errorf("RecentText leaked tool output or assistant text: %q", s.RecentText)
	}
}

// AC4: recent_turns window bounds how many user messages contribute.
func TestExtract_RecentTurnsWindow(t *testing.T) {
	body := `{"model":"m","messages":[
	  {"role":"user","content":"one"},
	  {"role":"user","content":"two"},
	  {"role":"user","content":"three"}
	]}`
	s, _ := Extract([]byte(body), "", InspectCfg{Scope: "recent_turns", Turns: 2})
	if s.RecentText != "two\nthree" {
		t.Errorf("recent_turns=2 should include the last two user texts; got %q", s.RecentText)
	}
	if s.LastUserText != "three" {
		t.Errorf("LastUserText = %q, want three", s.LastUserText)
	}
}

// AC6: malformed JSON returns an error (→ caller forwards original), never panics.
func TestExtract_MalformedReturnsError(t *testing.T) {
	_, err := Extract([]byte(`{"model": "m", "messages": [`), "", InspectCfg{Scope: "full"})
	if err == nil {
		t.Fatal("expected parse error on malformed body")
	}
	// ContextTokens is still populated from raw length even on parse failure.
	s, _ := Extract([]byte(`not json at all`), "", InspectCfg{Scope: "full"})
	if s.ContextTokens == 0 {
		t.Error("ContextTokens should reflect raw length even when parse fails")
	}
}

// String-form content (not blocks) is handled.
func TestExtract_StringContent(t *testing.T) {
	s, _ := Extract([]byte(`{"model":"m","messages":[{"role":"user","content":"plain string"}]}`), "", InspectCfg{Scope: "full"})
	if s.LastUserText != "plain string" {
		t.Errorf("string content not extracted: %q", s.LastUserText)
	}
}
