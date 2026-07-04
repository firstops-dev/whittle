package compressors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
)

// fakeSidecar returns an httptest server that "compresses" by applying fn to the
// received content and echoing the llmlingua response shape.
func fakeSidecar(t *testing.T, fn func(content string) (string, string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Content string `json:"content"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		compressed, action := fn(req.Content)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"compressed": compressed, "action": action, "skip_reason": nil,
		})
	}))
}

const mdDoc = `# Guide

This introduction paragraph explains the system for readers in detail today.

` + "```go\nfunc secret() { panic(\"never touch\") }\n```" + `

## Operations

Operators should drain the queue before restarting anything in production.

| col | type |
| id  | int  |

Final paragraph of prose closes out the document with more details here.`

// TestMarkdownStructured_MasksAndRestores: verbatim blocks never reach the
// sidecar; they are restored byte-exact; only prose is compressed.
func TestMarkdownStructured_MasksAndRestores(t *testing.T) {
	var sidecarSaw string
	srv := fakeSidecar(t, func(content string) (string, string) {
		sidecarSaw = content
		// crude "compression": drop the word "in detail" etc — keep sentinels
		out := strings.ReplaceAll(content, " for readers in detail today", "")
		out = strings.ReplaceAll(out, " with more details here", "")
		return out, "compressed"
	})
	defer srv.Close()
	m := NewMarkdownStructuredWith(NewLLMLinguaAdapterWithURL(srv.URL))

	res, err := m.Compress(context.Background(), compress.Input{Content: mdDoc, ContentType: compress.TypeDocRead})
	if err != nil || res.Skipped {
		t.Fatalf("err=%v skipped=%v reason=%s", err, res.Skipped, res.SkipReason)
	}
	// the model must never have seen the code, heading text is fine to mask too
	if strings.Contains(sidecarSaw, "secret()") || strings.Contains(sidecarSaw, "panic") {
		t.Fatalf("code leaked to the model: %q", sidecarSaw)
	}
	if !strings.Contains(sidecarSaw, MDBlockSentinel) {
		t.Fatalf("masked doc carries no sentinels: %q", sidecarSaw)
	}
	// verbatim blocks restored byte-exact
	for _, block := range []string{"```go\nfunc secret() { panic(\"never touch\") }\n```", "# Guide", "## Operations", "| col | type |\n| id  | int  |"} {
		if !strings.Contains(res.Output, block) {
			t.Fatalf("verbatim block not restored byte-exact: %q\nout: %q", block, res.Output)
		}
	}
	// prose was compressed
	if strings.Contains(res.Output, "for readers in detail today") {
		t.Fatalf("prose not compressed: %q", res.Output)
	}
	if !strings.Contains(res.Output, "This introduction paragraph explains the system") {
		t.Fatalf("prose lost: %q", res.Output)
	}
}

// TestMarkdownStructured_SentinelLossFailsOpen: if the model eats a sentinel the
// whole compression must fail open (count check), never mis-splice.
func TestMarkdownStructured_SentinelLossFailsOpen(t *testing.T) {
	srv := fakeSidecar(t, func(content string) (string, string) {
		return strings.Replace(content, MDBlockSentinel, "", 1), "compressed" // drop one
	})
	defer srv.Close()
	m := NewMarkdownStructuredWith(NewLLMLinguaAdapterWithURL(srv.URL))
	res, err := m.Compress(context.Background(), compress.Input{Content: mdDoc, ContentType: compress.TypeDocRead})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.SkipReason != "md_structure_guard" {
		t.Fatalf("want skipped/md_structure_guard, got skipped=%v reason=%q", res.Skipped, res.SkipReason)
	}
	if res.Output != mdDoc {
		t.Fatal("fail-open must return the original")
	}
}

// TestMarkdownStructured_SentinelCollision: input already containing the sentinel
// cannot be masked safely -> clean skip.
func TestMarkdownStructured_SentinelCollision(t *testing.T) {
	m := NewMarkdownStructuredWith(NewLLMLinguaAdapterWithURL("http://127.0.0.1:1")) // never called
	in := "# T\n\nprose mentioning " + MDBlockSentinel + " verbatim.\n"
	res, err := m.Compress(context.Background(), compress.Input{Content: in, ContentType: compress.TypeDocRead})
	if err != nil || !res.Skipped || res.SkipReason != "md_sentinel_collision" {
		t.Fatalf("want md_sentinel_collision, got err=%v reason=%q", err, res.SkipReason)
	}
}

// TestMarkdownStructured_SidecarSkipPropagates: a sidecar decline is a clean skip.
func TestMarkdownStructured_SidecarSkipPropagates(t *testing.T) {
	srv := fakeSidecar(t, func(content string) (string, string) { return content, "skipped" })
	defer srv.Close()
	m := NewMarkdownStructuredWith(NewLLMLinguaAdapterWithURL(srv.URL))
	res, err := m.Compress(context.Background(), compress.Input{Content: mdDoc, ContentType: compress.TypeDocRead})
	if err != nil || !res.Skipped {
		t.Fatalf("want clean skip, got err=%v skipped=%v", err, res.Skipped)
	}
	if res.Output != mdDoc {
		t.Fatal("skip must return original")
	}
}

// TestMarkdownStructured_GluedSentinelRestores: detokenizer glue around a
// sentinel must not break restoration or corrupt block boundaries.
func TestMarkdownStructured_GluedSentinelRestores(t *testing.T) {
	srv := fakeSidecar(t, func(content string) (string, string) {
		// glue the first sentinel to the previous word and drop the newline
		i := strings.Index(content, "\n"+MDBlockSentinel)
		if i >= 0 {
			content = content[:i] + MDBlockSentinel + content[i+1+len(MDBlockSentinel):]
		}
		return content, "compressed"
	})
	defer srv.Close()
	m := NewMarkdownStructuredWith(NewLLMLinguaAdapterWithURL(srv.URL))
	res, err := m.Compress(context.Background(), compress.Input{Content: mdDoc, ContentType: compress.TypeDocRead})
	if err != nil || res.Skipped {
		t.Fatalf("err=%v skipped=%v reason=%s", err, res.Skipped, res.SkipReason)
	}
	if !strings.Contains(res.Output, "func secret() { panic(\"never touch\") }") {
		t.Fatalf("block lost under glue: %q", res.Output)
	}
	// the restored block must sit on its own line, not glued into prose
	for _, ln := range strings.Split(res.Output, "\n") {
		if strings.Contains(ln, "```go") && ln != "```go" {
			t.Fatalf("fence line corrupted by glue: %q", ln)
		}
	}
}
