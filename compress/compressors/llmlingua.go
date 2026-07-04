package compressors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/firstops-dev/whittle/compress"
)

// LLMLinguaAdapter delegates the prose path to the existing Python LLMLingua
// service. It sends content_class:"prose" so the Python gate compresses without
// re-gating. Any error / non-200 / action!=compressed is returned as an error so
// the pipeline fails open (passthrough). The client is reused across calls.
type LLMLinguaAdapter struct {
	url    string
	client *http.Client
}

func NewLLMLinguaAdapterWithURL(url string) *LLMLinguaAdapter {
	return &LLMLinguaAdapter{
		url: strings.TrimRight(url, "/"),
		// MUST stay under the edge hook's 2s budget: with both at 2s they race,
		// the edge cancels first, and a slow inference surfaces as a transport
		// error at the edge instead of this pipeline's clean skip path. 1.5s
		// leaves the pipeline time to answer inside the edge deadline.
		client: &http.Client{Timeout: 1500 * time.Millisecond},
	}
}

func (*LLMLinguaAdapter) Name() string { return "llmlingua" }

func (*LLMLinguaAdapter) Handles(ct compress.ContentType) bool {
	return ct == compress.TypeProse || ct == compress.TypeDocRead || ct == compress.TypeUnknown
}

func (a *LLMLinguaAdapter) Compress(ctx context.Context, in compress.Input) (compress.Result, error) {
	rate := in.Rate
	if rate <= 0 {
		rate = 0.6
	}
	// content_class "prose" tells the Python gate to compress without re-gating —
	// correct for router-detected prose. For doc_read, send NO override so the
	// sidecar's classify() runs its own independent code vetoes (structural, code
	// heuristics, prose-ratio floor) as a second line of defense behind
	// isMarkdownDoc (reviewer B2: with the override, that path had no code
	// detector anywhere downstream of the router).
	contentClass := "prose"
	if in.ContentType == compress.TypeDocRead {
		contentClass = ""
	}
	body, err := json.Marshal(struct {
		Content      string  `json:"content"`
		ContentClass string  `json:"content_class,omitempty"`
		Rate         float64 `json:"rate"`
	}{Content: in.Content, ContentClass: contentClass, Rate: rate})
	if err != nil {
		return compress.Result{}, fmt.Errorf("llmlingua: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url+"/v1/compress", bytes.NewReader(body))
	if err != nil {
		return compress.Result{}, fmt.Errorf("llmlingua: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		// A timeout is a LATENCY-BUDGET outcome, not a failure: the input was
		// simply too slow to compress within the inline budget (the gate's
		// ProseMaxChars bounds this, but load/queueing can still push a
		// borderline input over). Fail-open as a clean skip so dashboards
		// count it honestly; real transport errors below still error.
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			return compress.Result{Skipped: true, SkipReason: "prose_budget", Output: in.Content, InChars: len(in.Content), OutChars: len(in.Content)}, nil
		}
		return compress.Result{}, fmt.Errorf("llmlingua: upstream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return compress.Result{}, fmt.Errorf("llmlingua: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return compress.Result{}, fmt.Errorf("llmlingua: read: %w", err)
	}
	var out struct {
		Compressed string `json:"compressed"`
		Action     string `json:"action"`
		SkipReason string `json:"skip_reason"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return compress.Result{}, fmt.Errorf("llmlingua: decode: %w", err)
	}
	if out.Action != "compressed" {
		// The sidecar legitimately declined (gate skip, load-shed "busy",
		// guardrail). That is a clean skip, NOT a failure — surfacing it as an
		// error made ~30% of prose "errors" actually be skips. Real transport /
		// timeout / non-200 failures still return an error above.
		reason := out.SkipReason
		if reason == "" {
			reason = "skipped"
		}
		return compress.Result{Skipped: true, SkipReason: reason, Output: in.Content, InChars: len(in.Content), OutChars: len(in.Content)}, nil
	}
	return compress.Result{Output: out.Compressed, Strategy: a.Name(), InChars: len(in.Content), OutChars: len(out.Compressed)}, nil
}
