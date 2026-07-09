package router

import "testing"

// Claude Code injects <system-reminder> text blocks INSIDE user messages
// (captured live). They are framework context, not user-authored — signal text
// must exclude them, or the classifier scores the reminder instead of the human
// question and mid-session routing silently degrades to the default.
func TestSignals_SystemReminderBlocksExcluded(t *testing.T) {
	// The exact structure captured from real Claude Code traffic.
	body := `{"model":"claude-opus-4-8","messages":[
	  {"role":"user","content":[
	    {"type":"text","text":"<system-reminder>\nAs you answer, you can use the following context:\n# userEmail\nuser@example.com\n# currentDate\n2026-07-10\n</system-reminder>"},
	    {"type":"text","text":"what day is today?"}
	  ]}
	]}`
	s, err := Extract([]byte(body), "sess", InspectCfg{Scope: "recent_turns", Turns: 3})
	if err != nil {
		t.Fatal(err)
	}
	if s.LastUserText != "what day is today?" {
		t.Fatalf("LastUserText must be the human text only, got %q", s.LastUserText)
	}
	if s.RecentText != "what day is today?" {
		t.Fatalf("RecentText must exclude injected blocks (keyword input too), got %q", s.RecentText)
	}
}

// A user message that is ONLY a reminder contributes no user text (the turn is
// skipped, like a tool_result-only turn).
func TestSignals_ReminderOnlyTurnSkipped(t *testing.T) {
	body := `{"model":"m","messages":[
	  {"role":"user","content":[{"type":"text","text":"real question"}]},
	  {"role":"assistant","content":"answer"},
	  {"role":"user","content":[{"type":"text","text":"<system-reminder>nudge</system-reminder>"}]}
	]}`
	s, err := Extract([]byte(body), "sess", InspectCfg{Scope: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if s.LastUserText != "real question" {
		t.Fatalf("reminder-only turn must be skipped, got %q", s.LastUserText)
	}
}
