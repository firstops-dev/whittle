package compress

import (
	"strings"
	"testing"
)

func TestDecide(t *testing.T) {
	prose := strings.Repeat("the meeting covered roadmap planning and hiring across the team. ", 8)
	jsonObj := `{"a":1,"b":2,"c":3,"d":4}`

	tests := []struct {
		name         string
		content      string
		toolName     string
		mime         string
		contentClass string
		minTokens    int
		wantAction   string
		wantReason   string
		wantKlass    string
	}{
		// New contract: the gate skips ONLY on size (too_short / too_large). Structured
		// content returns "compress" and is ROUTED downstream (JSON -> JSONCrusher);
		// the prose-safety skip for code-as-prose now lives in the pipeline. klass is
		// still tagged "code_structured" for the pipeline guard (see wantKlass).
		{"prose_compresses", prose, "", "", "", 64, "compress", "", "prose"},
		{"too_short", "tiny bit of text", "", "", "", 64, "skip", "too_short", "prose"},
		{"structured_json_routes", jsonObj + jsonObj + jsonObj, "", "", "", 0, "compress", "", "code_structured"},
		{"mime_struct_routes", prose, "", "application/json", "", 0, "compress", "", "code_structured"},
		{"tool_struct_routes", prose, "Read", "", "", 0, "compress", "", "code_structured"},
		{"override_prose_wins", jsonObj + jsonObj + jsonObj, "", "", "prose", 0, "compress", "", "prose"},
		{"override_code_routes", prose, "", "", "code_structured", 0, "compress", "", "code_structured"},
		{"too_short_beats_structured", jsonObj, "", "", "", 64, "skip", "too_short", "code_structured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, klass, _, reason := Decide(tt.content, estTokens(tt.content), tt.toolName, tt.mime, tt.contentClass, tt.minTokens)
			if action != tt.wantAction || reason != tt.wantReason {
				t.Fatalf("Decide()=(%q,%q), want (%q,%q)", action, reason, tt.wantAction, tt.wantReason)
			}
			if klass != tt.wantKlass {
				t.Fatalf("Decide() klass=%q, want %q", klass, tt.wantKlass)
			}
		})
	}
}

func TestClassify_ProseWithIncidentalCodeTokens(t *testing.T) {
	// A long, genuinely-prose session summary that mentions files/commands. Weak
	// codeSignal tokens (rank.py, the CSV, a git command) used to flag it
	// code_structured and skip it; with the proseRatio gate it stays prose.
	prose := "This session is being continued from a previous conversation that ran out of context. " +
		"The summary covers exec dinner planning for the summer series. We completed the pipeline that " +
		"fetched PostHog rows, enriched them via HubSpot and Apollo, ran rank.py, and produced the final " +
		"output.csv with a CSM column. The user asked why some rows still had no title and we explained " +
		"that the enrichment only ran on a subset, then drafted outreach copy and confirmed the next steps. "
	prose = strings.Repeat(prose, 3)
	klass, signal := classify(prose, "", "")
	if klass != "prose" {
		t.Fatalf("genuine prose flagged %q (signal=%s); should stay prose", klass, signal)
	}

	// A symbol-heavy code snippet (low prose ratio) must still be caught.
	code := strings.Repeat("const a=1;const b=2;function f(){return a+b};var d={x:1,y:2};\n", 5)
	if k, _ := classify(code, "", ""); k != "code_structured" {
		t.Fatalf("symbol-heavy code must stay code_structured, got %q", k)
	}
}

func TestLooksStructured(t *testing.T) {
	cases := map[string]bool{
		`{"a":1}`:                       true,
		`[1,2,3]`:                       true,
		"<html><body>x</body></html>":   true,
		`{"a": "b", "c": "d", "e": "f"`: true, // truncated but JSON-shaped (3+ ":)
		"just some plain prose here":    false,
		"":                              false,
	}
	for in, want := range cases {
		if got := looksStructured(in); got != want {
			t.Errorf("looksStructured(%q)=%v want %v", in, got, want)
		}
	}
}
