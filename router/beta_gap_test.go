package router

import (
	"net/http"
	"testing"
)

// realBetaHeader is the ACTUAL anthropic-beta token list Claude Code sends,
// captured verbatim in experiment/captures_gate1/req_0001.json.
const realBetaHeader = "claude-code-20250219,oauth-2025-04-20,context-1m-2025-08-07,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,advisor-tool-2026-03-01,effort-2025-11-24,structured-outputs-2025-12-15"

// betaWith builds a Request carrying only the given beta header value.
func betaWith(t *testing.T, beta string) *Request {
	t.Helper()
	h := http.Header{}
	h.Set("anthropic-beta", beta)
	r, err := ParseRequest([]byte(`{"model":"m","messages":[]}`), h)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return r
}

// WHY: table of adversarial beta-header shapes. Each asserts the EXACT resulting
// header (or its absence). Covers: two matching tokens, embedded empty tokens,
// surrounding whitespace, prefix-boundary safety (a bare "effort" must survive an
// "effort-" strip; "context-management" must survive a "context-1m" strip), and
// the header being deleted when the last token is removed.
func TestBeta_StripTable(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		prefix  string
		removed bool   // did removeBetaPrefix report a removal?
		want    string // "" means header expected ABSENT
	}{
		{"two-matching-only-tokens", "context-1m-x,context-1m-y", "context-1m", true, ""},
		{"two-matching-with-survivor", "keep,context-1m-x,context-1m-y", "context-1m", true, "keep"},
		{"embedded-empty-tokens", "a,,context-1m-z", "context-1m", true, "a"},
		{"surrounding-whitespace-normalized", " effort-1 , other ", "effort-", true, "other"},
		{"bare-effort-not-stripped-by-effort-dash", "effort,keep", "effort-", false, ""},
		{"context-1m-does-not-eat-context-management", "context-1m-2025-08-07,context-management-2025-06-27", "context-1m", true, "context-management-2025-06-27"},
		{"no-match-header-untouched", "a,,b", "zzz-", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := betaWith(t, tt.header)
			got := r.removeBetaPrefix(tt.prefix)
			if got != tt.removed {
				t.Fatalf("removed=%v want %v", got, tt.removed)
			}
			_, present := r.Headers["Anthropic-Beta"]
			if tt.name == "no-match-header-untouched" || tt.name == "bare-effort-not-stripped-by-effort-dash" {
				// No removal: original header value must remain byte-identical.
				if r.Headers.Get("anthropic-beta") != tt.header {
					t.Errorf("unmatched strip mutated header: got %q want %q", r.Headers.Get("anthropic-beta"), tt.header)
				}
				return
			}
			if tt.want == "" {
				if present {
					t.Errorf("header should be deleted, got %q", r.Headers.Get("anthropic-beta"))
				}
				return
			}
			if !present {
				t.Fatalf("header unexpectedly deleted, want %q", tt.want)
			}
			if v := r.Headers.Get("anthropic-beta"); v != tt.want {
				t.Errorf("header = %q, want %q", v, tt.want)
			}
		})
	}
}

// WHY: header-name case is HTTP-insensitive; the helpers must find and edit an
// "Anthropic-Beta" (canonical) or any-cased variant. http.Header canonicalizes on
// Set/Get, so this pins that a mixed-case set is still seen and stripped.
func TestBeta_HeaderNameCaseInsensitive(t *testing.T) {
	h := http.Header{}
	// A client that sends "ANTHROPIC-BETA"; net/http canonicalizes on Set, and the
	// helper's lowercase betaHeader constant must still find and edit it via Get.
	h.Set("ANTHROPIC-BETA", "context-1m-x,keep")
	r, err := ParseRequest([]byte(`{"model":"m"}`), h)
	if err != nil {
		t.Fatal(err)
	}
	if !r.hasBetaPrefix("context-1m") {
		t.Fatal("case-variant header not seen by helper")
	}
	r.removeBetaPrefix("context-1m")
	if r.Headers.Get("anthropic-beta") != "keep" {
		t.Errorf("case-variant strip failed: %q", r.Headers.Get("anthropic-beta"))
	}
}

// WHY: the flagship real-header test. Down-routing Opus->Haiku over the ACTUAL
// captured beta list must remove context-1m, effort-, mid-conversation-system,
// AND the two *thinking* betas (interleaved-thinking, thinking-token-count) —
// the M2-hardening decision strips a feature's betas atomically, by analogy to
// the observed context-1m beta 400 (a beta token alone 400s a non-supporting
// model). It preserves every UNRELATED token IN ORDER, and context-management
// must not be eaten by the context-1m prefix.
func TestBeta_RealHeaderDownrouteToHaiku(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-beta", realBetaHeader)
	r, err := ParseRequest([]byte(`{"model":"claude-opus-4-8","thinking":{"type":"enabled"},
	  "output_config":{"effort":"high"},
	  "messages":[{"role":"user","content":"hi"},{"role":"system","content":"be terse"},{"role":"assistant","content":"ok"}]}`), h)
	if err != nil {
		t.Fatal(err)
	}
	Reconcile(r, "claude-haiku-4-5")
	want := "claude-code-20250219,oauth-2025-04-20,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advisor-tool-2026-03-01,structured-outputs-2025-12-15"
	if got := r.Headers.Get("anthropic-beta"); got != want {
		t.Errorf("surviving beta tokens wrong:\n got  %q\n want %q", got, want)
	}
}

// LIMITATION (documented, not a claimed green): the helpers read the beta header
// via http.Header.Get, which returns only the FIRST value line. A client that
// splits anthropic-beta across MULTIPLE header lines would have tokens on the 2nd+
// lines neither detected nor stripped -> a paired 400 would persist. The captured
// Claude Code traffic sends a single comma-joined line, so this is low-realism,
// but it is a real robustness gap. This test PINS the current behavior so a future
// multi-line fix is a deliberate, visible change.
func TestBeta_MultiLineHeaderOnlyFirstProcessed_LIMITATION(t *testing.T) {
	h := http.Header{}
	h.Add("anthropic-beta", "keep-a")
	h.Add("anthropic-beta", "context-1m-2025-08-07") // second line
	r, err := ParseRequest([]byte(`{"model":"m"}`), h)
	if err != nil {
		t.Fatal(err)
	}
	// Current behavior: the context-1m token on the second line is invisible.
	if r.hasBetaPrefix("context-1m") {
		t.Skip("multi-line beta headers are now processed — update this limitation pin")
	}
	if !r.hasBetaPrefix("keep-a") {
		t.Fatal("first line should be visible")
	}
}
