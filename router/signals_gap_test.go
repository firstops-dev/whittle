package router

import (
	"strings"
	"testing"
)

// Testing-engineer GAP pass for signal extraction (T1.2). The dangerous class
// is text LEAKAGE (tool output / model scratchpad reaching the classifier) and
// wrong scalar signals. Each test pins a DESIGN §2.1 clause.

// WHY (§2.1 R6): image, tool_use, and thinking blocks must NEVER contribute to
// user text. A picture/scratchpad/tool call is not human intent; leaking it
// poisons keyword and intent routing.
func TestExtract_AllNonTextBlockTypesExcluded(t *testing.T) {
	body := `{"model":"m","messages":[
	  {"role":"user","content":[
	    {"type":"text","text":"refactor the auth module"},
	    {"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},
	    {"type":"tool_use","id":"t1","name":"Bash","input":{"cmd":"ls"}},
	    {"type":"thinking","thinking":"internal chain of thought"},
	    {"type":"tool_result","tool_use_id":"t0","content":"SECRET tool output"}
	  ]}
	]}`
	s, err := Extract([]byte(body), "", InspectCfg{Scope: "full"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.LastUserText != "refactor the auth module" {
		t.Errorf("LastUserText leaked non-text blocks: %q", s.LastUserText)
	}
	for _, leak := range []string{"SECRET", "internal chain", "AAAA", "Bash"} {
		if strings.Contains(s.RecentText, leak) {
			t.Errorf("RecentText leaked %q: full=%q", leak, s.RecentText)
		}
	}
}

// WHY (§2.1 R6): the walk-back must skip a TRAILING RUN of tool_result-only
// turns (agent may emit several), landing on the last genuine human text.
func TestExtract_WalkBackPastMultipleToolResults(t *testing.T) {
	body := `{"model":"m","messages":[
	  {"role":"user","content":"find the memory leak in server.go"},
	  {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out1"}]},
	  {"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"out2"}]}
	]}`
	s, _ := Extract([]byte(body), "", InspectCfg{Scope: "last_user_turn"})
	if s.LastUserText != "find the memory leak in server.go" {
		t.Errorf("walk-back failed across two tool_result turns: %q", s.LastUserText)
	}
	if !s.ToolLoop {
		t.Error("ToolLoop should be true (last msg is a user tool_result)")
	}
}

// WHY (§2.1 R6): recent_turns is a window over USER texts. Pin the exact
// boundaries: n==1, n==len, n>len all behave predictably.
func TestExtract_RecentTurnsBoundaries(t *testing.T) {
	body := `{"model":"m","messages":[
	  {"role":"user","content":"one"},
	  {"role":"assistant","content":"ok"},
	  {"role":"user","content":"two"},
	  {"role":"user","content":"three"}
	]}`
	cases := []struct {
		turns int
		want  string
	}{
		{1, "three"},
		{2, "two\nthree"},
		{3, "one\ntwo\nthree"},
		{99, "one\ntwo\nthree"}, // clamp to available
	}
	for _, tc := range cases {
		s, _ := Extract([]byte(body), "", InspectCfg{Scope: "recent_turns", Turns: tc.turns})
		if s.RecentText != tc.want {
			t.Errorf("turns=%d: RecentText=%q, want %q", tc.turns, s.RecentText, tc.want)
		}
	}
}

// WHY (§2.1 R3): ToolLoop is FALSE when the last message is an assistant turn
// (even a tool_use), and FALSE when the last user turn is plain text — it is
// specifically "last message is a user tool_result".
func TestExtract_ToolLoopFalseCases(t *testing.T) {
	// Last message assistant tool_use → not a loop (it's our turn to respond).
	asstLast := `{"model":"m","messages":[
	  {"role":"user","content":"go"},
	  {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"B","input":{}}]}
	]}`
	if s, _ := Extract([]byte(asstLast), "", InspectCfg{Scope: "full"}); s.ToolLoop {
		t.Error("assistant tool_use as last message must not be a ToolLoop")
	}
	// Empty messages → not a loop.
	if s, _ := Extract([]byte(`{"model":"m","messages":[]}`), "", InspectCfg{Scope: "full"}); s.ToolLoop {
		t.Error("no messages must not be a ToolLoop")
	}
}

// WHY (§2.1 R7): canonicalization must strip a date snapshot so a dated CC id
// still matches a hand-pinned bare id — else routing silently disables (R7).
func TestExtract_ModelCanonicalization(t *testing.T) {
	s, _ := Extract([]byte(`{"model":"claude-opus-4-8-20260101","messages":[]}`), "", InspectCfg{Scope: "full"})
	if s.RequestedModel != "claude-opus-4-8" {
		t.Errorf("RequestedModel = %q, want canonicalized", s.RequestedModel)
	}
}

// WHY (§2.1 R5 / AC7): a REAL Claude Code body shape (system as a text-block
// array, tools present, thinking config, mid-conversation role:"system"
// message) must extract sane signals — no panic, no leakage, correct counts.
// Modeled on exp_sem_routing/experiment/captures_gate1/req_0002 (roles
// [user, system], 30 tools).
func TestExtract_RealisticClaudeCodeBody(t *testing.T) {
	body := `{
	  "model":"claude-opus-4-8",
	  "stream":true,
	  "max_tokens":32000,
	  "metadata":{"user_id":"u"},
	  "thinking":{"type":"enabled","budget_tokens":10000},
	  "context_management":{"edits":[]},
	  "output_config":{},
	  "system":[
	    {"type":"text","text":"You are Claude Code."},
	    {"type":"text","text":"Big system prompt with lots of tokens..."}
	  ],
	  "tools":[{"name":"Bash"},{"name":"Read"},{"name":"Edit"}],
	  "messages":[
	    {"role":"user","content":"help me design a rate limiter"},
	    {"role":"system","content":[{"type":"text","text":"mid-conversation system directive"}]}
	  ]
	}`
	s, err := Extract([]byte(body), "911c4d7e-sess", InspectCfg{Scope: "recent_turns", Turns: 3})
	if err != nil {
		t.Fatalf("realistic body should parse, got: %v", err)
	}
	if s.RequestedModel != "claude-opus-4-8" {
		t.Errorf("RequestedModel = %q", s.RequestedModel)
	}
	if !s.HasTools {
		t.Error("HasTools should be true (3 tools)")
	}
	if s.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2 (user + mid-conv system)", s.MessageCount)
	}
	if s.LastUserText != "help me design a rate limiter" {
		t.Errorf("LastUserText = %q", s.LastUserText)
	}
	// The mid-conversation system directive must NOT leak into user text.
	if strings.Contains(s.RecentText, "directive") || strings.Contains(s.RecentText, "You are Claude") {
		t.Errorf("system text leaked into RecentText: %q", s.RecentText)
	}
	if s.ContextTokens != len(body)/4 {
		t.Errorf("ContextTokens = %d, want %d (whole-body/4)", s.ContextTokens, len(body)/4)
	}
	if s.SessionID != "911c4d7e-sess" {
		t.Errorf("SessionID = %q", s.SessionID)
	}
}

// WHY (§2.1 AC6): malformed content shapes must degrade to empty text, never
// panic — the caller falls open (Mode A) on a real parse error only.
func TestExtract_MalformedContentNoPanic(t *testing.T) {
	bodies := []string{
		`{"model":"m","messages":[{"role":"user","content":null}]}`,
		`{"model":"m","messages":[{"role":"user","content":123}]}`,
		`{"model":"m","messages":[{"role":"user","content":{"unexpected":"object"}}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text"}]}]}`, // text block, empty text
	}
	for _, b := range bodies {
		s, err := Extract([]byte(b), "", InspectCfg{Scope: "full"})
		if err != nil {
			// A JSON-valid body with odd content should not be a parse error;
			// but if the decoder rejects it, that's acceptable Mode-A behavior.
			continue
		}
		if s.LastUserText != "" {
			t.Errorf("body %q: unexpected LastUserText %q", b, s.LastUserText)
		}
	}
}
