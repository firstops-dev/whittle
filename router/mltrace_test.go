package router

import (
	"strings"
	"testing"
)

// The decision carries the computed signal values (for the log line): real values
// when smart mode is on, an explicit "off" marker when it isn't — so a policy
// silently running heuristics-only is visible on every line, not just at startup.
func TestMLTrace(t *testing.T) {
	pol := `{
	  "version":1,
	  "tiers":[{"name":"fast","model":"claude-haiku-4-5"},{"name":"smart","model":"claude-opus-4-8"}],
	  "default":"requested","inspect":{"scope":"full"},
	  "signals":{"complexity":[{"name":"r","threshold":0.15,"hard":["x"],"easy":["y"]}]},
	  "routes":[{"name":"up","when":{"complexity":"r:hard"},"to":"smart"}]
	}`
	p, _ := mustLoad(t, pol)

	// smart on: the trace carries the raw margin.
	d := Decide(Signals{RequestedModel: "claude-haiku-4-5", RecentText: "q", SessionID: "s"},
		p, &spyClassifier{complexMargin: 0.42}, nil, "")
	if !strings.Contains(d.MLTrace, "cplx:r=+0.420") {
		t.Errorf("trace should carry the computed margin, got %q", d.MLTrace)
	}

	// smart off: the trace says so explicitly.
	d = Decide(Signals{RequestedModel: "claude-haiku-4-5", RecentText: "q", SessionID: "s"},
		p, nil, nil, "")
	if !strings.Contains(d.MLTrace, "cplx:r=off") {
		t.Errorf("smart-off must be visible in the trace, got %q", d.MLTrace)
	}
}
