package server

// POST /hook - Claude Code PostToolUse over HTTP: receives the hook event,
// returns hookSpecificOutput.updatedToolOutput when compression wins, or an
// empty 200 body (fail-open: Claude Code proceeds with the original).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/firstops-dev/whittle/compress"
)

// lossyStrategy: strategies that REDUCE content (marked or model-lossy) get a
// retrieval hint; lossless transforms (json columnar, ansi strip) get NONE -
// nothing was lost, so nothing is offered (and no hint tokens are spent).
func lossyStrategy(strategy string) bool {
	for _, m := range []string{"llmlingua", "log_compressor", "md_structured"} {
		if strings.Contains(strategy, m) {
			return true
		}
	}
	return false
}

func hookHandler(p *compress.Pipeline, store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // ALWAYS 200: a hook must never error a tool call
		var ev struct {
			HookEventName string          `json:"hook_event_name"`
			SessionID     string          `json:"session_id"`
			ToolName      string          `json:"tool_name"`
			ToolOutput    string          `json:"tool_output"`
			ToolResponse  json.RawMessage `json:"tool_response"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil || json.Unmarshal(body, &ev) != nil || ev.HookEventName != "PostToolUse" {
			return
		}
		text, rebuild, ok := ExtractHookText(ev.ToolResponse, ev.ToolOutput)
		if !ok {
			// A big tool_response we cannot extract from is the signature of shape
			// drift in a new Claude Code version - if that ever happens compression
			// stops wholesale, so it must be loud in the daemon log, not silent.
			if len(ev.ToolResponse) >= 1024 {
				log.Printf("hook: no known text carrier in %d-byte tool_response (tool=%s) - shape drift? output not compressed", len(ev.ToolResponse), ev.ToolName)
			}
			return
		}
		if len(text) < 256 {
			return
		}
		// 9s: above the LLMLingua adapter's 8s (its timeout must fire first, see
		// llmlingua.go), below Claude Code's 10s hook timeout (setup.go).
		ctx, cancel := context.WithTimeout(r.Context(), 9*time.Second)
		defer cancel()
		out := p.Compress(ctx, compress.Input{Content: text, ToolName: ev.ToolName, MinTokens: -1})
		if out.Action != "compressed" || len(out.Output) >= len(text) {
			return
		}
		// Retrieval hint - ONLY where content was actually reduced. Copy is
		// deliberately discouraging: the summary is complete; raw is for
		// byte-exact needs. Alias integers cost ~2 tokens (measured).
		final, storeID := finalizeReplacement(text, out.Output, out.Strategy, store)
		if final == "" {
			return // no honest win once all costs are counted: fail open
		}
		logEvent(ev.SessionID, ev.ToolName, out.Strategy, storeID,
			compress.EstimateTokens(text), compress.EstimateTokens(final))
		_ = json.NewEncoder(w).Encode(HookReply(rebuild(final)))
	}
}

// getHandler serves originals back to the whittle_get MCP tool. Misses are
// honest 404s ("expired") - the agent can re-run the tool.
func getHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil || store == nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		content, ok := store.Get(id)
		if ok {
			logEvent("", "whittle_get", "retrieve", id, 0, 0)
		}
		if !ok {
			http.Error(w, "expired - re-run the tool for fresh output", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, content)
	}
}

// logEvent appends one compression record to ~/.whittle/stats.jsonl - the local,
// never-transmitted audit trail behind `whittle stats`. One line per whittled
// output: when, which session, which tool, which strategy, token delta, and the
// retrieval id (0 = lossless, nothing stored). Users can inspect it directly;
// originals of reduced outputs are retrievable via whittle_get(id) while cached.
func logEvent(session, tool, strategy string, storeID int64, inTok, outTok int) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, ".whittle", "stats.jsonl"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]any{
		"ts": time.Now().Unix(), "session": session, "tool": tool,
		"strategy": strategy, "id": storeID, "in_tokens": inTok, "out_tokens": outTok,
	})
	f.Write(append(b, '\n'))
}

const hintFmt = "\n… [trimmed; content above is complete in substance. Raw original only if strictly required: whittle_get(%d)]"

// finalizeReplacement enforces the POST-HINT invariant (review B1): whatever is
// emitted - with hint or without - must be strictly smaller than the original in
// BOTH bytes and estimated tokens. Order of preference: compressed+hint;
// compressed alone (marginal wins keep the win, just without retrieval);
// nothing (fail open). The store alias is only spent when the hint actually
// emits (review O6). There is deliberately NO size cap here: Claude Code's 10k
// hook-output cap applies to the context-injection channels (additionalContext,
// systemMessage, plain stdout), NOT to updatedToolOutput - verified live on
// 2.1.203 with a 20.6k replacement landing intact (docs/hook-output-cap.md).
func finalizeReplacement(text, output, strategy string, store *Store) (string, int64) {
	origTok := compress.EstimateTokens(text) // hot path: scan the original once
	fits := func(s string) bool {
		return len(s) < len(text) && compress.EstimateTokens(s) < origTok
	}
	if store != nil && lossyStrategy(strategy) {
		// probe with a worst-case-width alias before spending one
		if probe := output + fmt.Sprintf(hintFmt, int64(99999999)); fits(probe) {
			id := store.Put(text)
			final := output + fmt.Sprintf(hintFmt, id)
			if fits(final) {
				return final, id
			}
		}
	}
	if fits(output) {
		return output, 0
	}
	return "", 0
}
