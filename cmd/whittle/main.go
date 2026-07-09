// Command whittle is the CLI: compress files or stdin, or run the HTTP service.
//
//	whittle compress output.json          # compressed text to stdout
//	cat tool-output.txt | whittle compress -stats
//	whittle serve -addr :45871             # POST /v1/compress
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/firstops-dev/whittle"
	"github.com/firstops-dev/whittle/compress"
	"github.com/firstops-dev/whittle/server"
)

// version is injected by goreleaser (-X main.version=...); dev builds show the
// last released baseline.
var version = "0.2.1"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "compress":
		cmdCompress(os.Args[2:])
	case "serve":
		cmdServe(os.Args[2:])
	case "route":
		cmdRoute(os.Args[2:])
	case "setup":
		cmdSetup(os.Args[2:])
	case "daemon":
		cmdDaemon(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "cleanup":
		cmdCleanup(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "stats":
		cmdStats(os.Args[2:])
	case "mcp":
		cmdMCP(os.Args[2:])
	case "hook":
		cmdHook(os.Args[2:])
	case "version":
		fmt.Println("whittle", version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `whittle - carves agent tool outputs down to what matters

usage:
  whittle setup                     install: model sidecar, Claude Code hook,
                                    background service (launchd) - one command
  whittle status                    health of router, sidecar, hook
  whittle stats [-days 7]           local savings report (tokens whittled)
  whittle stop                      stop background services
  whittle cleanup                   stop + remove hook + unregister service
  whittle compress [flags] [file]   compress a file (or stdin) to stdout
  whittle serve    [flags]          run the compress HTTP API in the foreground
  whittle route    [flags]          run the model-router daemon (opt-in) on
                                    ANTHROPIC_BASE_URL
  whittle version

compress flags:
  -rate float      prose keep-rate 0.1-1.0 (default 0.6; uses the running daemon)
  -min-tokens int  skip inputs shorter than this (default 64; 0 disables)
  -stats           print action/strategy/token stats to stderr

serve flags:
  -addr string     listen address (default ":45871")

route flags:
  -addr string     listen address (default "127.0.0.1:45873")
  -policy string   policy file path (default ~/.whittle/router.json)
                   missing/invalid → transparent passthrough (never bricks Claude Code)
  -install         register the router as a background launchd agent (opt-in), then exit
  -uninstall       stop + unregister the router launchd agent, then exit
  env WHITTLE_ROUTER_UPSTREAM    upstream API (default api.anthropic.com)
  env WHITTLE_ROUTER_MODEL_URL   classifier sidecar URL (unset → smart mode off)

env:
  WHITTLE_MODEL_URL        enable the ML prose path (model sidecar URL)
  WHITTLE_MAX_CHARS        global size ceiling (default 262144)
  WHITTLE_PROSE_MAX_CHARS  prose-path ceiling (default 100000; lower on CPU-only machines)`)
}

func cmdCompress(args []string) {
	fs := flag.NewFlagSet("compress", flag.ExitOnError)
	rate := fs.Float64("rate", 0.6, "prose keep-rate")
	minTokens := fs.Int("min-tokens", -1, "minimum input tokens (-1 = default 64, 0 = no floor)")
	stats := fs.Bool("stats", false, "print stats to stderr")
	_ = fs.Parse(args)

	var data []byte
	var err error
	if fs.NArg() > 0 && fs.Arg(0) != "-" {
		data, err = os.ReadFile(fs.Arg(0))
	} else {
		data, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "whittle:", err)
		os.Exit(1)
	}

	// Prefer the running daemon so `compress` matches what the hook actually does
	// (the daemon has the prose sidecar wired). Fall back to an in-process engine
	// when no daemon is up. Without this, bare `whittle compress` silently does
	// deterministic-only and passes prose through unchanged - a real footgun.
	res, ok := compressViaDaemon(string(data), *rate, *minTokens)
	if !ok {
		eng := whittle.New(whittle.Options{
			ModelURL:  os.Getenv("WHITTLE_MODEL_URL"),
			MinTokens: *minTokens,
		})
		r := eng.CompressInput(context.Background(), compress.Input{
			Content: string(data), Rate: *rate, MinTokens: *minTokens,
		})
		res = cliResult{Output: r.Output, Action: r.Action, Detected: string(r.Detected),
			Strategy: r.Strategy, SkipReason: r.SkipReason}
	}
	fmt.Print(res.Output)
	if *stats {
		in, out := whittle.EstimateTokens(string(data)), whittle.EstimateTokens(res.Output)
		via := "in-process"
		if ok {
			via = "daemon"
		}
		fmt.Fprintf(os.Stderr, "\nwhittle: action=%s detected=%s strategy=%s tokens=%d->%d via=%s",
			res.Action, res.Detected, res.Strategy, in, out, via)
		if res.SkipReason != "" {
			fmt.Fprintf(os.Stderr, " skip_reason=%s", res.SkipReason)
		}
		fmt.Fprintln(os.Stderr)
	}
}

type cliResult struct{ Output, Action, Detected, Strategy, SkipReason string }

// compressViaDaemon POSTs to a locally running whittle daemon; ok=false if none
// is reachable (caller falls back to an in-process engine).
func compressViaDaemon(content string, rate float64, minTokens int) (cliResult, bool) {
	body, _ := json.Marshal(map[string]any{"content": content, "rate": rate, "min_tokens": minTokens})
	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post("http://"+routerAddr+"/v1/compress", "application/json", bytes.NewReader(body))
	if err != nil {
		return cliResult{}, false
	}
	defer resp.Body.Close()
	var out struct {
		Compressed string `json:"compressed"`
		Action     string `json:"action"`
		Detected   string `json:"detected"`
		Strategy   string `json:"strategy"`
		SkipReason string `json:"skip_reason"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out) != nil {
		return cliResult{}, false
	}
	return cliResult{Output: out.Compressed, Action: out.Action, Detected: out.Detected,
		Strategy: out.Strategy, SkipReason: out.SkipReason}, true
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":45871", "listen address")
	_ = fs.Parse(args)
	if err := server.ListenAndServe(*addr); err != nil {
		fmt.Fprintln(os.Stderr, "whittle:", err)
		os.Exit(1)
	}
}
