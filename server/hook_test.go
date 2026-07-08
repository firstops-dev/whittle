package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
	"github.com/firstops-dev/whittle/compress/compressors"
)

// newHookMux wires the FULL default chain set (structural compressors
// included) with the prose path pointed at a mock URL - the /hook handler is
// exercised end-to-end exactly as the daemon runs it, minus the Python sidecar.
// HOME is redirected so logEvent's stats bookkeeping never touches the real
// ~/.whittle/stats.jsonl from a test run.
func newHookMux(t *testing.T, proseURL string) http.Handler {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	chains := compressors.ChainsWithModel(proseURL)
	p := compress.NewPipeline(compress.NewRegistry(chains), compress.DefaultGateConfig(), nil)
	return NewMux(p)
}

// postHook fires a PostToolUse event at /hook and returns the raw response
// body ("" = fail-open: Claude Code keeps the original output).
func postHook(t *testing.T, h http.Handler, event any) string {
	t.Helper()
	b, _ := json.Marshal(event)
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("hook must always answer 200, got %d", rr.Code)
	}
	return strings.TrimSpace(rr.Body.String())
}

// updatedOutput unwraps hookSpecificOutput.updatedToolOutput from a hook reply.
func updatedOutput(t *testing.T, body string) json.RawMessage {
	t.Helper()
	var resp struct {
		HookSpecificOutput struct {
			HookEventName     string          `json:"hookEventName"`
			UpdatedToolOutput json.RawMessage `json:"updatedToolOutput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("hook reply not JSON: %v\n%s", err, body)
	}
	if resp.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Fatalf("hookEventName=%q", resp.HookSpecificOutput.HookEventName)
	}
	return resp.HookSpecificOutput.UpdatedToolOutput
}

// bigJSONRecords builds a JSON array large enough that even its COMPRESSED
// (columnar) form exceeds the old 9.5k cap - pinning both the shape fix and
// the cap removal in one end-to-end pass.
func bigJSONRecords(n int) string {
	recs := make([]string, n)
	for i := range recs {
		recs[i] = fmt.Sprintf(`{"id":%d,"name":"user-%d","status":"active","region":"us-east-1","score":%d}`, i, i, i*7)
	}
	return "[" + strings.Join(recs, ",") + "]"
}

// The headline regression test for the schema-shape bug: a Bash event must be
// answered with an OBJECT replacement (stdout swapped, siblings untouched),
// never a bare string - Claude Code silently rejects shape mismatches, which
// made every prior Bash "win" a no-op.
func TestHookHandler_BashShapePreserved_NoSizeCap(t *testing.T) {
	h := newHookMux(t, "http://127.0.0.1:0") // structural path only; no prose upstream
	// 900 records compress to ~19k (measured) - comfortably past the old 9.5k
	// cap even if the columnar compressor improves substantially.
	stdout := bigJSONRecords(900)
	body := postHook(t, h, map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       "Bash",
		"session_id":      "s1",
		"tool_response": map[string]any{
			"stdout": stdout, "stderr": "", "interrupted": false,
			"isImage": false, "noOutputExpected": false,
		},
	})
	if body == "" {
		t.Fatal("expected a replacement, got fail-open")
	}
	var out struct {
		Stdout           string  `json:"stdout"`
		Stderr           *string `json:"stderr"`
		Interrupted      *bool   `json:"interrupted"`
		IsImage          *bool   `json:"isImage"`
		NoOutputExpected *bool   `json:"noOutputExpected"`
	}
	raw := updatedOutput(t, body)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("updatedToolOutput must be a Bash-shaped OBJECT, got: %.120s", raw)
	}
	if out.Stderr == nil || out.Interrupted == nil || out.IsImage == nil || out.NoOutputExpected == nil {
		t.Fatalf("sibling fields dropped: %.200s", raw)
	}
	if out.Stdout == "" || len(out.Stdout) >= len(stdout) {
		t.Fatalf("stdout not compressed: %d vs %d", len(out.Stdout), len(stdout))
	}
	// Cap removal: this win is bigger than the old 9.5k ceiling and must land.
	if len(out.Stdout) <= 9500 {
		t.Fatalf("test payload too small to pin the cap removal: %d", len(out.Stdout))
	}
}

func TestHookHandler_ReadFileShapePreserved(t *testing.T) {
	h := newHookMux(t, "http://127.0.0.1:0")
	content := strings.Repeat("2026-01-02 INFO tick handler ok\n", 40) + "ERROR boot failed\n"
	body := postHook(t, h, map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       "Read",
		"tool_response": map[string]any{
			"type": "text",
			"file": map[string]any{
				"filePath": "/tmp/boot.log", "content": content,
				"numLines": 41, "startLine": 1, "totalLines": 41,
			},
		},
	})
	if body == "" {
		t.Fatal("expected a replacement, got fail-open")
	}
	var out struct {
		Type string `json:"type"`
		File struct {
			FilePath string `json:"filePath"`
			Content  string `json:"content"`
			NumLines *int   `json:"numLines"`
		} `json:"file"`
	}
	raw := updatedOutput(t, body)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("updatedToolOutput must keep the Read shape, got: %.120s", raw)
	}
	if out.Type != "text" || out.File.FilePath != "/tmp/boot.log" || out.File.NumLines == nil {
		t.Fatalf("siblings corrupted: %.200s", raw)
	}
	if out.File.Content == "" || len(out.File.Content) >= len(content) {
		t.Fatalf("file.content not compressed: %d vs %d", len(out.File.Content), len(content))
	}
}

func TestHookHandler_BareStringResponseStaysString(t *testing.T) {
	h := newHookMux(t, "http://127.0.0.1:0")
	content := strings.Repeat("2026-01-02 INFO tick handler ok\n", 40) + "ERROR boot failed\n"
	body := postHook(t, h, map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       "WebFetch",
		"tool_response":   content,
	})
	if body == "" {
		t.Fatal("expected a replacement, got fail-open")
	}
	var s string
	raw := updatedOutput(t, body)
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("bare-string tool_response must be replaced with a string, got: %.120s", raw)
	}
	if s == "" || len(s) >= len(content) {
		t.Fatalf("not compressed: %d vs %d", len(s), len(content))
	}
}

// Review C1 regression at the handler level: tool_response null + legacy
// tool_output carrying the real text must still compress (as a bare string).
func TestHookHandler_ToolOutputFallback(t *testing.T) {
	h := newHookMux(t, "http://127.0.0.1:0")
	content := strings.Repeat("2026-01-02 INFO tick handler ok\n", 40) + "ERROR boot failed\n"
	body := postHook(t, h, map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       "SomeTool",
		"tool_response":   nil,
		"tool_output":     content,
	})
	if body == "" {
		t.Fatal("expected a replacement via the tool_output fallback, got fail-open")
	}
	var s string
	if err := json.Unmarshal(updatedOutput(t, body), &s); err != nil || len(s) >= len(content) {
		t.Fatalf("fallback replacement wrong: err=%v len=%d vs %d", err, len(s), len(content))
	}
}

func TestHookHandler_FailOpenCases(t *testing.T) {
	h := newHookMux(t, "http://127.0.0.1:0")
	for name, ev := range map[string]any{
		"wrong event":     map[string]any{"hook_event_name": "PreToolUse", "tool_response": strings.Repeat("x", 500)},
		"tiny output":     map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_response": map[string]any{"stdout": "short"}},
		"no text carrier": map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_response": map[string]any{"weird": true}},
	} {
		if body := postHook(t, h, ev); body != "" {
			t.Fatalf("%s: must fail open, got %.120s", name, body)
		}
	}
}
