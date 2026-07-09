package router

import "net/http"

// Adapter shapes a routed request for a concrete provider and points it at the
// right upstream. v1 ships only the Anthropic passthrough adapter; the interface
// is the extensibility seam for cross-provider routing (v2), where Reconcile
// grows into full protocol translation. Keeping it an interface now means the
// proxy depends on the seam, not the concrete adapter.
type Adapter interface {
	Name() string
	// Reconcile sets the target model and strips target-incompatible features
	// across body AND headers (see reconcile.go), returning stripped feature
	// names for the log line. Operates on the Request in place; the caller holds
	// the pristine original for Mode-B retry.
	Reconcile(req *Request, target string) (stripped []string)
	// Upstream returns the base URL to forward to and the outbound headers. v1
	// passes the client's own auth through untouched (GATE-0).
	Upstream(in http.Header) (baseURL string, out http.Header)
}

// AnthropicAdapter is the v1 same-protocol adapter: rewrite the model + reconcile
// capabilities, forward to api.anthropic.com with the client's own credentials.
type AnthropicAdapter struct{}

func (AnthropicAdapter) Name() string { return "anthropic" }

func (AnthropicAdapter) Reconcile(req *Request, target string) []string {
	return Reconcile(req, target)
}

// anthropicBaseURL is the upstream for the passthrough adapter.
const anthropicBaseURL = "https://api.anthropic.com"

func (AnthropicAdapter) Upstream(in http.Header) (string, http.Header) {
	// Passthrough: the client's auth and headers flow untouched. The proxy is
	// responsible for the hop-by-hop hygiene (Host, Content-Length, gzip) at the
	// transport layer (M3).
	return anthropicBaseURL, in
}

// For selects an adapter for a destination. v1 always returns the Anthropic
// adapter; the factory exists so cross-provider destinations slot in without
// touching call sites (extensibility seam).
func For(model string) Adapter {
	return AnthropicAdapter{}
}
