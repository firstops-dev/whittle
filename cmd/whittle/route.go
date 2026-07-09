package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/firstops-dev/whittle/router"
)

// cmdRoute runs the model-router daemon in the foreground: it sits on
// ANTHROPIC_BASE_URL and routes each request to the cheapest capable tier per the
// policy file. Opt-in and independent of the compress hook. A missing/invalid
// policy is non-fatal (transparent passthrough), so pointing Claude Code here is
// always safe.
func cmdRoute(args []string) {
	fs := flag.NewFlagSet("route", flag.ExitOnError)
	addr := fs.String("addr", router.DefaultAddr, "listen address")
	policyPath := fs.String("policy", filepath.Join(whittleHome(), "router.json"), "policy file path")
	_ = fs.Parse(args)

	lg := log.New(os.Stderr, "", log.LstdFlags)
	if err := router.ListenAndServe(*addr, *policyPath, lg); err != nil {
		fmt.Fprintln(os.Stderr, "whittle:", err)
		os.Exit(1)
	}
}
