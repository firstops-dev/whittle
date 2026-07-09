// Package ml is the router's opt-in smart-mode surface: a thin HTTP client to the
// model sidecar, implementing router.Classifier. It is constructed only when a
// sidecar URL is configured; when it isn't, the engine uses the noop classifier
// and this package is never touched.
//
// The classifier lives behind an HTTP boundary on purpose (docs/ROUTER_DESIGN.md):
// the models (~200-300MB of ONNX) stay in the Python sidecar where the compressor
// already hosts model weight, so the Go daemon carries no model runtime and no
// heavy dependency — this package is stdlib-only.
//
// Every method FAILS OPEN: a timeout, a non-200, or any transport error returns
// an error, and the engine degrades to heuristics (intent leaf → false) or the
// static default (classify → default). Smart mode never blocks or breaks routing.
package ml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultTimeout bounds a single classify/intent call. The classifier is on the
// request path (the user is waiting on the routing decision), and it is fail-open,
// so a slow sidecar must degrade to the default quickly rather than stall the
// request. Local embedding of a short text is tens of ms; 2s is generous headroom
// that still trips fast on a wedged sidecar.
const defaultTimeout = 2 * time.Second

// maxRespBytes caps the response we read — a classify/intent reply is a few bytes.
const maxRespBytes = 1 << 20

// Client talks to the router ML sidecar over localhost HTTP. The zero value is not
// usable; construct with New.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client for the sidecar at baseURL (e.g. "http://127.0.0.1:45872").
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: defaultTimeout},
	}
}

// Intent classifies text into a single category label with a confidence. On any
// sidecar error the engine's intent leaf simply evaluates false (the route won't
// fire) — so the error is returned verbatim, not swallowed.
func (c *Client) Intent(text string) (string, float64, error) {
	var out struct {
		Label      string  `json:"label"`
		Confidence float64 `json:"confidence"`
	}
	if err := c.post("/v1/route/intent", intentReq{Text: text}, &out); err != nil {
		return "", 0, err
	}
	return out.Label, out.Confidence, nil
}

// Classify returns the best tier for text by few-shot nearest-example over the
// per-tier examples, plus the cosine confidence. The examples are sent every call;
// the sidecar caches their embeddings by content hash (compute + cache where the
// model lives), so re-embedding is amortized across requests, not repeated here.
func (c *Client) Classify(text string, examples map[string][]string) (string, float64, error) {
	var out struct {
		Tier       string  `json:"tier"`
		Confidence float64 `json:"confidence"`
	}
	if err := c.post("/v1/route/classify", classifyReq{Text: text, Examples: examples}, &out); err != nil {
		return "", 0, err
	}
	return out.Tier, out.Confidence, nil
}

type intentReq struct {
	Text string `json:"text"`
}

type classifyReq struct {
	Text     string              `json:"text"`
	Examples map[string][]string `json:"examples"`
}

// post sends body as JSON to path and decodes a JSON reply into out. A non-200 is
// an error (the caller fails open). The response is size-bounded.
func (c *Client) post(path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("ml %s: marshal: %w", path, err)
	}
	resp, err := c.http.Post(c.baseURL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("ml %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ml %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRespBytes)).Decode(out); err != nil {
		return fmt.Errorf("ml %s: decode: %w", path, err)
	}
	return nil
}
