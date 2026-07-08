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

	"github.com/firstops-dev/whittle/server"
)

type hookEvent struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolOutput    string          `json:"tool_output"`   // legacy string-only field; real events omit it
	ToolResponse  json.RawMessage `json:"tool_response"` // primary: carries the tool's output shape
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
	// Shape-preserving extraction: Claude Code schema-validates updatedToolOutput
	// and silently keeps the original on mismatch - see server/toolresponse.go.
	text, rebuild, ok := server.ExtractHookText(ev.ToolResponse, ev.ToolOutput)
	if !ok || len(text) < 256 { // unknown shape / tiny output: not worth the round-trip
		return
	}

	body, _ := json.Marshal(map[string]any{"content": text, "tool_name": ev.ToolName})
	client := http.Client{Timeout: 9 * time.Second} // > adapter's 8s: its skip fires first
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
	// No size cap on the replacement - see docs/hook-output-cap.md.
	logStat(ev.ToolName, out.Strategy, out.OriginalTokens, out.CompressedTokens)
	emitReplacement(rebuild(out.Compressed))
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

// emitReplacement prints the PostToolUse hook output that replaces the persisted
// tool output. `compressed` is the rebuilt value in the tool's own output
// shape, not necessarily a string; the envelope is server.HookReply, shared
// with the HTTP path so the wire contract lives in one place.
func emitReplacement(compressed any) {
	b, _ := json.Marshal(server.HookReply(compressed))
	fmt.Println(string(b))
}
