package compress

import (
	"strings"
	"testing"
)

// TestDetectANSI_PrecisionRecall pins the routing for the terminal/ANSI path.
// RECALL: escape-heavy, structure-less terminal output must route to TypeTerminal
// (so it gets stripped + log-compressed instead of skipped). PRECISION: prose,
// code, JSON, CSV, and lightly-colored prose must NOT be pulled into terminal.
func TestDetectANSI_PrecisionRecall(t *testing.T) {
	// A color-heavy TUI dump (the context-usage bar): many truecolor escapes around
	// braille blocks, no log keywords or structure.
	tui := "\x1b[1mContext Usage\x1b[22m\n" +
		strings.Repeat("\x1b[38;2;147;51;234m⛁ \x1b[39m", 50) + "\n" +
		"362.8k/1m tokens (36%)\n" +
		strings.Repeat("\x1b[38;2;153;153;153m⛶ \x1b[39m", 30)
	// A progress spinner redrawing itself with cursor + color codes.
	spinner := strings.Repeat("\x1b[2K\x1b[36m building \x1b[0m\x1b[33m45%\x1b[0m\r", 20)
	// jq -C colored JSON: detection strips ANSI first, so it correctly recovers the
	// JSON shape and routes to json (the json chain strips before crushing).
	coloredJSON := "\x1b[1;39m{\x1b[0m\n  \x1b[34;1m\"name\"\x1b[0m: \x1b[32m\"x\"\x1b[0m,\n" +
		"  \x1b[34;1m\"id\"\x1b[0m: \x1b[35m42\x1b[0m,\n  \x1b[34;1m\"k\"\x1b[0m: \x1b[32m\"v\"\x1b[0m\n\x1b[1;39m}\x1b[0m"

	prose := "The quarterly review went over the roadmap and the team aligned on the next milestone for the ingestion work."
	proseStray := "The quarterly review went \x1b[32mwell\x1b[0m and the team aligned on the roadmap for the next month entirely."
	goCode := "package main\nfunc main() {\n\tfor i := 0; i < 10; i++ {\n\t\treturn\n\t}\n}"
	jsonObj := `{"id":42,"name":"alpha","items":[1,2,3],"nested":{"a":1}}`
	csv := "id,name,score\n1,alice,90\n2,bob,85\n3,carol,77"

	cases := []struct {
		name string
		in   string
		want ContentType
		note string
	}{
		// --- recall: escape-heavy terminal output -> terminal ---
		{"tui_bar", tui, TypeTerminal, "color-heavy TUI dump"},
		{"progress_spinner", spinner, TypeTerminal, "redrawing spinner"},
		{"colored_json", coloredJSON, TypeJSON, "strip-then-detect recovers the json shape"},
		// --- precision: must NOT be terminal, and must hit the right type ---
		{"plain_prose", prose, TypeProse, "no escapes"},
		{"prose_one_color", proseStray, TypeProse, "single colored word -> below escape floor"},
		{"go_code", goCode, TypeCode, "no escapes"},
		{"json_object", jsonObj, TypeJSON, "uncolored json"},
		{"csv", csv, TypeTabular, "uncolored csv"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, conf := Detect(c.in)
			gotTerm, wantTerm := got == TypeTerminal, c.want == TypeTerminal
			if gotTerm != wantTerm {
				t.Fatalf("routing: %s -> %q (conf %.2f), want %q (%s)", c.name, got, conf, c.want, c.note)
			}
			if !wantTerm && got != c.want {
				t.Fatalf("precision: %s -> %q, want exactly %q (%s)", c.name, got, c.want, c.note)
			}
		})
	}
}

// TestDetectANSI_FractionFloor pins the precision floor: a long, mostly-prose body
// with a handful of color codes stays below the density threshold and is not
// classified as terminal.
func TestDetectANSI_FractionFloor(t *testing.T) {
	body := strings.Repeat("The team reviewed the plan and agreed on the next milestone for the project. ", 20)
	lightlyColored := "\x1b[1m" + body + "\x1b[0m \x1b[32mdone\x1b[0m"
	if got, _ := Detect(lightlyColored); got == TypeTerminal {
		t.Fatalf("a few escapes in a long prose body must not classify as terminal, got %q", got)
	}
}
