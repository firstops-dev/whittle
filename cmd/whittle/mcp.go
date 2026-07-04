package main

// whittle mcp — a minimal stdio MCP server exposing one tool: whittle_get(id).
// Registered in Claude Code by `whittle setup` (claude mcp add), removed by
// cleanup. It proxies the resident daemon's /get endpoint.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func cmdMCP(_ []string) {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	out := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		reply := func(result any) {
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		switch req.Method {
		case "initialize":
			reply(map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "whittle", "version": version},
			})
		case "tools/list":
			reply(map[string]any{"tools": []any{map[string]any{
				"name":        "whittle_get",
				"description": "Retrieve the exact original bytes of a whittled (compressed) tool output. Use ONLY when the byte-exact original is strictly required — whittled summaries are complete in substance.",
				"inputSchema": map[string]any{"type": "object",
					"properties": map[string]any{"id": map[string]any{"type": "integer", "description": "the id from whittle_get(N) in the output"}},
					"required":   []string{"id"}},
			}}})
		case "tools/call":
			var p struct {
				Name string `json:"name"`
				Args struct {
					ID int64 `json:"id"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			text, isErr := fetchOriginal(p.Args.ID)
			reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isErr})
		case "notifications/initialized", "ping":
			if req.ID != nil {
				reply(map[string]any{})
			}
		}
	}
}

func fetchOriginal(id int64) (string, bool) {
	c := http.Client{Timeout: 5 * time.Second}
	r, err := c.Get(fmt.Sprintf("http://%s/get?id=%d", routerAddr, id))
	if err != nil {
		return "whittle daemon is not running; re-run the tool for fresh output", true
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if r.StatusCode != 200 {
		return string(b), true
	}
	return string(b), false
}
