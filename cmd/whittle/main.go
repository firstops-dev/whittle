// Command whittle is the CLI: compress files or stdin, or run the HTTP service.
//
//	whittle compress output.json          # compressed text to stdout
//	cat tool-output.txt | whittle compress -stats
//	whittle serve -addr :8095             # POST /v1/compress
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/firstops-dev/whittle"
	"github.com/firstops-dev/whittle/compress"
	"github.com/firstops-dev/whittle/server"
)

const version = "0.1.0"

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
	fmt.Fprintln(os.Stderr, `whittle — carves agent tool outputs down to what matters

usage:
  whittle setup                     install: model sidecar, Claude Code hook,
                                    background service (launchd) — one command
  whittle status                    health of router, sidecar, hook
  whittle stop                      stop background services
  whittle cleanup                   stop + remove hook + unregister service
  whittle compress [flags] [file]   compress a file (or stdin) to stdout
  whittle serve    [flags]          run the HTTP API in the foreground
  whittle version

compress flags:
  -rate float      prose keep-rate 0.1-1.0 (default 0.6; needs WHITTLE_MODEL_URL)
  -min-tokens int  skip inputs shorter than this (default 64; 0 disables)
  -stats           print action/strategy/token stats to stderr

serve flags:
  -addr string     listen address (default ":8095")

env:
  WHITTLE_MODEL_URL        enable the ML prose path (model sidecar URL)
  WHITTLE_MAX_CHARS        global size ceiling (default 262144)
  WHITTLE_PROSE_MAX_CHARS  prose-path ceiling (default 4500)`)
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

	eng := whittle.New(whittle.Options{
		ModelURL:  os.Getenv("WHITTLE_MODEL_URL"),
		MinTokens: *minTokens,
	})
	res := eng.CompressInput(context.Background(), compress.Input{
		Content: string(data), Rate: *rate, MinTokens: *minTokens,
	})
	fmt.Print(res.Output)
	if *stats {
		in, out := whittle.EstimateTokens(string(data)), whittle.EstimateTokens(res.Output)
		fmt.Fprintf(os.Stderr, "\nwhittle: action=%s detected=%s strategy=%s tokens=%d->%d",
			res.Action, res.Detected, res.Strategy, in, out)
		if res.SkipReason != "" {
			fmt.Fprintf(os.Stderr, " skip_reason=%s", res.SkipReason)
		}
		fmt.Fprintln(os.Stderr)
	}
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8095", "listen address")
	_ = fs.Parse(args)
	if err := server.ListenAndServe(*addr); err != nil {
		fmt.Fprintln(os.Stderr, "whittle:", err)
		os.Exit(1)
	}
}
