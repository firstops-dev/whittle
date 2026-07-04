package server

// POST /hook — Claude Code PostToolUse over HTTP: receives the hook event,
// returns hookSpecificOutput.updatedToolOutput when compression wins, or an
// empty 200 body (fail-open: Claude Code proceeds with the original). No
// stdout-capture size cap applies on this path, unlike command hooks.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/firstops-dev/whittle/compress"
)

func hookHandler(p *compress.Pipeline) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // ALWAYS 200: a hook must never error a tool call
		var ev struct {
			HookEventName string          `json:"hook_event_name"`
			ToolName      string          `json:"tool_name"`
			ToolOutput    string          `json:"tool_output"`
			ToolResponse  json.RawMessage `json:"tool_response"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil || json.Unmarshal(body, &ev) != nil || ev.HookEventName != "PostToolUse" {
			return
		}
		text := ev.ToolOutput
		if text == "" {
			text, _ = extractHookText(ev.ToolResponse)
		}
		if len(text) < 256 {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		out := p.Compress(ctx, compress.Input{Content: text, ToolName: ev.ToolName, MinTokens: -1})
		if out.Action != "compressed" || len(out.Output) >= len(text) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":     "PostToolUse",
				"updatedToolOutput": out.Output,
			},
		})
	}
}

func extractHookText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return "", false
	}
	for _, k := range []string{"stdout", "output", "result"} {
		if v, ok := obj[k]; ok && json.Unmarshal(v, &s) == nil && s != "" {
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
