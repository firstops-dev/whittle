package router

import (
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// DefaultAddr is the router daemon's listen address. It is distinct from the
// compress service (:45871): the router is a separate front door that Claude Code
// points ANTHROPIC_BASE_URL at, while the compress service backs the PostToolUse
// hook. Running both is the full whittle install.
const DefaultAddr = "127.0.0.1:45872"

// ListenAndServe builds the router Proxy from the policy file at policyPath and
// serves it on addr. Cold-start safety (R3): a missing or invalid policy is NOT
// fatal — the proxy runs in transparent passthrough so Claude Code keeps working,
// and a later SIGHUP can install a valid policy without a restart. SIGHUP
// hot-reloads the policy (a bad edit keeps the live one). Blocks until the server
// stops. The classifier is nil here (heuristics-only); smart mode wires the
// sidecar classifier in at the call site once M4 lands.
func ListenAndServe(addr, policyPath string, lg Logger) error {
	if lg == nil {
		lg = discardLogger{}
	}
	pol, warns, err := LoadPolicyFile(policyPath)
	if err != nil {
		lg.Printf("router: no valid policy at %s (%v) — transparent passthrough until one loads", policyPath, err)
		pol = nil
	}
	for _, w := range warns {
		lg.Printf("router: policy warning: %s", w)
	}

	px := NewProxy(pol, nil, NewMemSessionStore(), lg)
	// Optional upstream override (default is the real Anthropic host). Lets an
	// operator point the router at a corporate Anthropic gateway — and lets a
	// smoke test drive it against a local fake upstream.
	if u := os.Getenv("WHITTLE_ROUTER_UPSTREAM"); u != "" {
		px.baseURL = u
		lg.Printf("router: upstream override = %s", u)
	}

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			warns, err := px.ReloadFile(policyPath)
			if err != nil {
				lg.Printf("router: reload failed, keeping live policy: %v", err)
				continue
			}
			for _, w := range warns {
				lg.Printf("router: policy warning: %s", w)
			}
			lg.Printf("router: policy reloaded from %s", policyPath)
		}
	}()

	srv := &http.Server{
		Addr:    addr,
		Handler: px,
		// Bound only the header read. No total/write timeout: a streamed
		// generation runs for minutes and any deadline would truncate it (R9).
		ReadHeaderTimeout: 10 * time.Second,
	}
	lg.Printf("router: listening on %s (policy: %s)", addr, policyPath)
	return srv.ListenAndServe()
}
