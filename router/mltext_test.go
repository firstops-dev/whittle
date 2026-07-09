package router

import "testing"

// ML signals must classify the LATEST user turn, not the joined inspect window —
// averaging turns dilutes the classifier (measured live: a hard turn's margin
// fell from +0.171 to +0.025 when joined with two earlier trivial turns,
// suppressing mid-session escalation). Keywords keep the window on purpose.
func TestMLSignals_ClassifyLatestTurnNotWindow(t *testing.T) {
	p, _ := mustLoad(t, `{
	  "version":1,
	  "tiers":[{"name":"main","model":"claude-sonnet-4-5"},{"name":"smart","model":"claude-opus-4-8"}],
	  "default":"main","inspect":{"scope":"recent_turns","turns":3},
	  "signals":{"complexity":[{"name":"r","threshold":0.15,"hard":["x"],"easy":["y"]}]},
	  "routes":[{"name":"up","when":{"complexity":"r:hard"},"to":"smart"}]
	}`)
	cl := &textSpyClassifier{margin: 0.5}
	sig := Signals{
		LastUserText: "dive deep and synthesize the architecture",
		RecentText:   "what is the date. explain this file. dive deep and synthesize the architecture",
		SessionID:    "s",
	}
	Decide(sig, p, cl, nil, "")
	if cl.sawText != sig.LastUserText {
		t.Fatalf("ML signal must classify the latest user turn, got %q", cl.sawText)
	}

	// Fallback: a turn with no user text (tool-result-only) uses the window.
	cl2 := &textSpyClassifier{margin: 0.5}
	Decide(Signals{LastUserText: "", RecentText: "window text", SessionID: "s"}, p, cl2, nil, "")
	if cl2.sawText != "window text" {
		t.Fatalf("empty last turn must fall back to the window, got %q", cl2.sawText)
	}
}

// Keywords, by contrast, keep scanning the whole window (persistence is the point).
func TestKeywords_StillScanWindow(t *testing.T) {
	p, _ := mustLoad(t, `{
	  "version":1,
	  "tiers":[{"name":"main","model":"claude-sonnet-4-5"},{"name":"smart","model":"claude-opus-4-8"}],
	  "default":"main","inspect":{"scope":"recent_turns","turns":3},
	  "routes":[{"name":"up","when":{"keywords":["deadlock"]},"to":"smart"}]
	}`)
	sig := Signals{
		LastUserText: "keep going please",
		RecentText:   "debug the deadlock in the pool. keep going please",
		SessionID:    "s",
	}
	d := Decide(sig, p, nil, nil, "")
	if d.Tier != "smart" {
		t.Fatalf("keyword from an earlier window turn must still protect, got %s (%s)", d.Tier, d.Reason)
	}
}

// textSpyClassifier records the text it was asked to classify.
type textSpyClassifier struct {
	sawText string
	margin  float64
}

func (c *textSpyClassifier) Domain(text string) (string, float64, map[string]float64, error) {
	c.sawText = text
	return "", 0, nil, ErrMLDisabled
}
func (c *textSpyClassifier) EmbeddingScore(text string, _ []string) (float64, error) {
	c.sawText = text
	return 0, ErrMLDisabled
}
func (c *textSpyClassifier) ComplexityMargin(text string, _, _ []string) (float64, error) {
	c.sawText = text
	return c.margin, nil
}
