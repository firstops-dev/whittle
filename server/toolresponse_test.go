package server

import (
	"encoding/json"
	"testing"
)

// Shapes below are captured from real Claude Code 2.1.203 PostToolUse events
// (Bash, Read). The invariant under test: rebuild(c) re-emits the SAME shape
// with only the text field swapped - anything else is silently rejected by
// Claude Code's output-schema validation and the compression win is lost.

func rebuiltJSON(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal rebuilt value: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("rebuilt value is not an object: %s", b)
	}
	return m
}

func TestExtractToolText_BareString(t *testing.T) {
	text, rebuild, ok := ExtractToolText(json.RawMessage(`"raw tool text"`))
	if !ok || text != "raw tool text" {
		t.Fatalf("ok=%v text=%q", ok, text)
	}
	if got := rebuild("smaller"); got != "smaller" {
		t.Fatalf("bare string must rebuild to a bare string, got %T %v", got, got)
	}
}

func TestExtractToolText_BashShape_PreservesSiblings(t *testing.T) {
	// Verbatim field set from a captured Bash event, including a field
	// (noOutputExpected) that older whittle code never knew about.
	raw := json.RawMessage(`{"stdout":"hello there from the tool","stderr":"warn: x","interrupted":false,"isImage":false,"noOutputExpected":false}`)
	text, rebuild, ok := ExtractToolText(raw)
	if !ok || text != "hello there from the tool" {
		t.Fatalf("ok=%v text=%q", ok, text)
	}
	m := rebuiltJSON(t, rebuild("compressed!"))
	if string(m["stdout"]) != `"compressed!"` {
		t.Fatalf("stdout=%s", m["stdout"])
	}
	for k, want := range map[string]string{
		"stderr": `"warn: x"`, "interrupted": "false",
		"isImage": "false", "noOutputExpected": "false",
	} {
		if string(m[k]) != want {
			t.Fatalf("sibling %s corrupted: %s (want %s)", k, m[k], want)
		}
	}
	if len(m) != 5 {
		t.Fatalf("field count changed: %d", len(m))
	}
}

func TestExtractToolText_OutputAndResultKeys(t *testing.T) {
	for _, k := range []string{"output", "result"} {
		raw := json.RawMessage(`{"` + k + `":"some text","extra":42}`)
		text, rebuild, ok := ExtractToolText(raw)
		if !ok || text != "some text" {
			t.Fatalf("key %s: ok=%v text=%q", k, ok, text)
		}
		m := rebuiltJSON(t, rebuild("c"))
		if string(m[k]) != `"c"` || string(m["extra"]) != "42" {
			t.Fatalf("key %s: rebuilt %v", k, m)
		}
	}
}

func TestExtractToolText_ReadFileShape(t *testing.T) {
	// Verbatim shape from a captured Read event.
	raw := json.RawMessage(`{"type":"text","file":{"filePath":"/tmp/x.txt","content":"line one\nline two","numLines":2,"startLine":1,"totalLines":2}}`)
	text, rebuild, ok := ExtractToolText(raw)
	if !ok || text != "line one\nline two" {
		t.Fatalf("ok=%v text=%q", ok, text)
	}
	m := rebuiltJSON(t, rebuild("squeezed"))
	if string(m["type"]) != `"text"` {
		t.Fatalf("type sibling corrupted: %s", m["type"])
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(m["file"], &file); err != nil {
		t.Fatalf("file: %v", err)
	}
	if string(file["content"]) != `"squeezed"` {
		t.Fatalf("content=%s", file["content"])
	}
	for k, want := range map[string]string{
		"filePath": `"/tmp/x.txt"`, "numLines": "2", "startLine": "1", "totalLines": "2",
	} {
		if string(file[k]) != want {
			t.Fatalf("file sibling %s corrupted: %s (want %s)", k, file[k], want)
		}
	}
}

func TestExtractToolText_NonStringPreferredKeyFallsThrough(t *testing.T) {
	// stdout is a number: not compressible text; "output" is the real carrier.
	raw := json.RawMessage(`{"stdout":7,"output":"actual text"}`)
	text, _, ok := ExtractToolText(raw)
	if !ok || text != "actual text" {
		t.Fatalf("ok=%v text=%q", ok, text)
	}
}

func TestExtractToolText_UnrecognizedShapes(t *testing.T) {
	for _, raw := range []string{
		`{"weird":{"nested":true}}`, // no known text carrier
		`{"stdout":""}`,             // empty text: nothing to compress
		`[1,2,3]`,                   // array: not ours to touch
		`not json at all`,
		`null`, // JSON null decodes into a string as a no-op: MUST be !ok,
		`""`,   // else it shadows the tool_output fallback (review C1)
	} {
		if _, _, ok := ExtractToolText(json.RawMessage(raw)); ok {
			t.Fatalf("shape %s must not extract", raw)
		}
	}
}

// Review C1 regression: a null/empty tool_response must not block the legacy
// tool_output fallback - the pre-fix code compressed such events.
func TestExtractHookText_FallbackToToolOutput(t *testing.T) {
	for _, raw := range []string{`null`, `""`, ``, `{"weird":true}`} {
		text, rebuild, ok := ExtractHookText(json.RawMessage(raw), "the legacy output text")
		if !ok || text != "the legacy output text" {
			t.Fatalf("tool_response %q: ok=%v text=%q", raw, ok, text)
		}
		if got := rebuild("c"); got != "c" {
			t.Fatalf("fallback must rebuild as a bare string, got %T %v", got, got)
		}
	}
	// tool_response wins when it carries text; tool_output is ignored.
	text, _, ok := ExtractHookText(json.RawMessage(`{"stdout":"real text"}`), "stale")
	if !ok || text != "real text" {
		t.Fatalf("tool_response must take precedence: ok=%v text=%q", ok, text)
	}
	// nothing usable anywhere: fail closed.
	if _, _, ok := ExtractHookText(json.RawMessage(`null`), ""); ok {
		t.Fatal("no text anywhere must be !ok")
	}
}
