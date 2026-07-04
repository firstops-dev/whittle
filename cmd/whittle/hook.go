package main

// whittle hook — the Claude Code PostToolUse hook command. Reads the hook event
// on stdin, compresses the tool output through the local whittle router, and
// emits the hook JSON that replaces the persisted tool output.
//
// FAIL-OPEN CONTRACT: on ANY problem (router down, non-text output, no win,
// malformed event) the hook exits 0 with no stdout — Claude Code proceeds with
// the original output. A compression hook must never break a tool call.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type hookEvent struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolResponse  json.RawMessage `json:"tool_response"`
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
	text, ok := extractText(ev.ToolResponse)
	if !ok || len(text) < 256 { // tiny outputs are never worth the round-trip
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
		Compressed string `json:"compressed"`
		Action     string `json:"action"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out) != nil ||
		out.Action != "compressed" || out.Compressed == "" || len(out.Compressed) >= len(text) {
		return
	}

	emitReplacement(out.Compressed)
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
// tool output (hookSpecificOutput contract; see README "Claude Code hook").
func emitReplacement(compressed string) {
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PostToolUse",
			"updatedOutput": compressed,
		},
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}
