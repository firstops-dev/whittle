package router

import (
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/firstops-dev/whittle/router/ml"
)

// quietClassifier wraps the sidecar client so an ABSENT sidecar (connection
// refused — never installed, or not up yet) reads as smart-mode-off
// (ErrMLDisabled: signals trace "off", no ml-degraded noise, routing falls to
// heuristics/default), while a PRESENT-but-broken sidecar (timeout, 5xx,
// malformed) keeps surfacing as a real error. The distinction keeps the
// zero-config default honest: absence is expected, breakage is loud. The check
// is per-request and costs ~nothing (localhost refusal is immediate), so the
// moment `whittle setup` brings the sidecar up, signals activate with no restart.
type quietClassifier struct{ c *ml.Client }

func quiet(err error) error {
	if err != nil && errors.Is(err, syscall.ECONNREFUSED) {
		return ErrMLDisabled
	}
	return err
}

func (q quietClassifier) Domain(text string) (string, float64, map[string]float64, error) {
	l, c, p, err := q.c.Domain(text)
	return l, c, p, quiet(err)
}
func (q quietClassifier) EmbeddingScore(text string, cands []string) (float64, error) {
	s, err := q.c.EmbeddingScore(text, cands)
	return s, quiet(err)
}
func (q quietClassifier) ComplexityMargin(text string, hard, easy []string) (float64, error) {
	m, err := q.c.ComplexityMargin(text, hard, easy)
	return m, quiet(err)
}

// sidecarUp is a one-shot startup probe purely for an honest log line.
func sidecarUp(base string) bool {
	c := http.Client{Timeout: 800 * time.Millisecond}
	r, err := c.Get(base + "/health")
	if err != nil {
		return false
	}
	r.Body.Close()
	return r.StatusCode == 200
}

// ml.Client must satisfy Classifier — asserted here (at the import site) rather
// than in package ml, which would import router back and form a cycle.
var _ Classifier = (*ml.Client)(nil)

// DefaultModelURL is the classifier sidecar `whittle setup` installs — smart
// mode probes it automatically; no configuration required.
const DefaultModelURL = "http://127.0.0.1:45872"

// DefaultAddr is the router daemon's listen address. It is distinct from the
// compress service (:45871) and the prose model sidecar (:45872): the router is a
// separate front door that Claude Code points ANTHROPIC_BASE_URL at, while the
// compress service backs the PostToolUse hook. Running all three is the full
// whittle install.
const DefaultAddr = "127.0.0.1:45873"

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

	// Smart mode is AUTOMATIC: the router defaults to the standard sidecar
	// address (the one `whittle setup` installs and supervises) and degrades
	// gracefully when it isn't there — a refused connection reads as "smart mode
	// off" (quiet), not as a per-request error. WHITTLE_ROUTER_MODEL_URL overrides
	// the address; "off" disables ML entirely.
	var cl Classifier
	modelURL := os.Getenv("WHITTLE_ROUTER_MODEL_URL")
	if modelURL == "" {
		modelURL = DefaultModelURL
	}
	switch modelURL {
	case "off", "0", "none":
		lg.Printf("router: smart mode disabled by WHITTLE_ROUTER_MODEL_URL=%s — heuristic signals only", modelURL)
		if pol != nil && policyUsesML(pol) {
			lg.Printf("router: WARNING: the policy references ML signals, which will never fire in this mode — requests they guard fall to the default")
		}
	default:
		cl = quietClassifier{ml.New(modelURL)}
		if sidecarUp(modelURL) {
			lg.Printf("router: smart mode ON (classifier sidecar = %s)", modelURL)
		} else {
			lg.Printf("router: smart mode AUTO — sidecar not responding at %s yet; routing on heuristics until it appears (run `whittle setup` if it was never installed)", modelURL)
		}
	}

	px := NewProxy(pol, cl, NewMemSessionStore(), lg)
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
