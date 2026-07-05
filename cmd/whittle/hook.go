package main

// whittle hook - the Claude Code PostToolUse hook command. Reads the hook event
// on stdin, compresses the tool output through the local whittle router, and
// emits the hook JSON that replaces the persisted tool output.
//
// FAIL-OPEN CONTRACT: on ANY problem (router down, non-text output, no win,
// malformed event) the hook exits 0 with no stdout - Claude Code proceeds with
// the original output. A compression hook must never break a tool call.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type hookEvent struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolOutput    string          `json:"tool_output"`   // documented shape (string)
	ToolResponse  json.RawMessage `json:"tool_response"` // fallback: older/object shapes
}

func cmdHook(_ []string) {
	// Never let the hook be the reason a tool call fails.
	defer func() { _ = recover(); os.Exit(0) }()

	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
	if err != nil {
		return
	}
	var ev hookEvent
	if json.Unmarshal(raw, &ev) != nil || ev.HookEventName != "PostToolUse" {
		return
	}
	text := ev.ToolOutput
	if text == "" {
		text, _ = extractText(ev.ToolResponse)
	}
	if len(text) < 256 { // tiny outputs are never worth the round-trip
		return
	}

	body, _ := json.Marshal(map[string]any{"content": text, "tool_name": ev.ToolName})
	client := http.Client{Timeout: 4 * time.Second}
	resp, err := client.Post("http://"+routerAddr+"/v1/compress", "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var out struct {
		Compressed       string `json:"compressed"`
		Action           string `json:"action"`
		Strategy         string `json:"strategy"`
		OriginalTokens   int    `json:"original_tokens"`
		CompressedTokens int    `json:"compressed_tokens"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out) != nil ||
		out.Action != "compressed" || out.Compressed == "" || len(out.Compressed) >= len(text) {
		return
	}
	// Claude Code caps hook stdout at 10,000 chars - larger output is diverted to
	// a file and replaced with a preview, which would corrupt the result. Fail
	// open instead of emitting a replacement that cannot land intact.
	if len(out.Compressed) > 9500 {
		return
	}

	logStat(ev.ToolName, out.Strategy, out.OriginalTokens, out.CompressedTokens)
	emitReplacement(out.Compressed)
}

// logStat appends one compression event to ~/.whittle/stats.jsonl - LOCAL ONLY,
// never transmitted. This powers `whittle stats`. Best-effort: failures are
// ignored (the hook must never fail because bookkeeping did).
func logStat(tool, strategy string, inTok, outTok int) {
	_ = os.MkdirAll(whittleHome(), 0o755)
	f, err := os.OpenFile(filepath.Join(whittleHome(), "stats.jsonl"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]any{
		"ts": time.Now().Unix(), "tool": tool, "strategy": strategy,
		"in_tokens": inTok, "out_tokens": outTok,
	})
	f.Write(append(b, '\n'))
}

// extractText pulls the compressible text out of the tool_response, which may be
// a bare string or an object ({"stdout": ...}, {"file": {"content": ...}},
// {"content": [{"type":"text","text":...}]}). Anything else: not ours to touch.
func extractText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return "", false
	}
	for _, key := range []string{"stdout", "output", "result"} {
		if v, ok := obj[key]; ok && json.Unmarshal(v, &s) == nil && s != "" {
			return s, true
		}
	}
	if f, ok := obj["file"]; ok {
		var file struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(f, &file) == nil && file.Content != "" {
			return file.Content, true
		}
	}
	return "", false
}

// emitReplacement prints the PostToolUse hook output that replaces the persisted
// tool output (docs-verified contract: hookSpecificOutput.updatedToolOutput).
func emitReplacement(compressed string) {
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PostToolUse",
			"updatedToolOutput": compressed,
		},
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}
