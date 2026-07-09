package ml

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// GAP pass for the smart-mode HTTP client. Each test pins a real invariant with a
// WHY comment. client_test.go covers success / non-200 / transport / malformed /
// timeout / trailing-slash; these cover the fail-open BOUNDARY behaviors the
// engine depends on to distinguish "signal below threshold" from "sidecar broken".

// ---------------------------------------------------------------------------
// 1. A well-formed but EMPTY 200 reply is NOT an error.
//
// WHY: the engine reads a returned score/margin against the policy threshold; a
// low/zero score means "signal did not fire", NOT "sidecar broken". route.py
// returns score/margin 0.0 for empty text/candidates. If the Go client turned a
// missing/empty field into an ERROR, that below-threshold signal would be
// mis-reported as a broken sidecar. So a 200 with an empty/absent field must
// decode to the zero value with NO error.
// ---------------------------------------------------------------------------
func TestClient_EmptyOrMissingFieldsAreNotError(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty object", `{}`},
		{"explicit zero score", `{"score":0}`},
		{"null field", `{"score":null}`},
	}
	for _, tc := range cases {
		t.Run("embedding/"+tc.name, func(t *testing.T) {
			c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, tc.body)
			})
			score, err := c.EmbeddingScore("x", []string{"hi"})
			if err != nil {
				t.Fatalf("a 200 with an empty/absent field must not be an error (it is a below-threshold signal, not a broken sidecar): %v", err)
			}
			if score != 0 {
				t.Fatalf("empty embedding reply should decode to 0, got %v", score)
			}
		})
	}
	// Domain mirrors the same contract on its own fields.
	t.Run("domain/empty object", func(t *testing.T) {
		c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{}`)
		})
		label, conf, _, err := c.Domain("x")
		if err != nil {
			t.Fatalf("empty domain reply must not error: %v", err)
		}
		if label != "" || conf != 0 {
			t.Fatalf("empty domain reply should decode to zero values, got (%q, %v)", label, conf)
		}
	})
}

// ---------------------------------------------------------------------------
// 2. The timeout bounds the WHOLE exchange, including a body that never
// finishes — not just the dial/header phase.
//
// WHY: the Classifier interface carries NO context (engine.go), so
// http.Client.Timeout is the ONLY bound on a call. A wedged sidecar that flushes
// 200 + headers and then hangs mid-body is the realistic failure (accepted the
// request, stuck in inference). If only the header phase were bounded, that would
// stall the request path forever. This flushes the status, then blocks, and
// asserts the call still errors.
// ---------------------------------------------------------------------------
func TestClient_BodyHangStillTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // 200 + headers are on the wire; body is not
		}
		<-r.Context().Done() // block until the client gives up (no goroutine leak)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	c.http.Timeout = 60 * time.Millisecond // tighten for a fast test

	start := time.Now()
	_, err := c.EmbeddingScore("x", []string{"hi"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a sidecar that flushes 200 then hangs mid-body must time out to an error, not stall the request path")
	}
	if elapsed > time.Second {
		t.Fatalf("timeout took %v; the 2s-class bound must trip fast on a wedged body", elapsed)
	}
}

// ---------------------------------------------------------------------------
// 3. Request wire contract: the client must send EXACTLY the JSON field names the
// Python sidecar's pydantic models bind (app.py: `text`, `candidates`).
//
// WHY: a silent rename of a struct json tag on the Go side would still compile
// and still pass the round-trip tests (which decode into the SAME Go structs),
// but the sidecar would then bind defaults and every request would score against
// an empty payload. Pin the raw wire keys, not just the Go round-trip.
// ---------------------------------------------------------------------------
func TestClient_EmbeddingRequestWireContract(t *testing.T) {
	var rawBody []byte
	var contentType string
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		contentType = r.Header.Get("Content-Type")
		io.WriteString(w, `{"score":0.5}`)
	})
	cands := []string{"hi", "design an API"}
	if _, err := c.EmbeddingScore("do the thing", cands); err != nil {
		t.Fatal(err)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (FastAPI binds JSON body)", contentType)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &wire); err != nil {
		t.Fatalf("request body is not valid JSON: %v (%s)", err, rawBody)
	}
	if _, ok := wire["text"]; !ok {
		t.Errorf("request missing %q key (sidecar binds .text); wire=%s", "text", rawBody)
	}
	if _, ok := wire["candidates"]; !ok {
		t.Errorf("request missing %q key (sidecar binds .candidates); wire=%s", "candidates", rawBody)
	}
	var gotCands []string
	if err := json.Unmarshal(wire["candidates"], &gotCands); err != nil {
		t.Fatalf("candidates did not serialize as []string: %v", err)
	}
	if len(gotCands) != 2 || gotCands[1] != "design an API" {
		t.Errorf("candidates corrupted on the wire: %+v", gotCands)
	}
}

// TestClient_ComplexityRequestWireContract pins the complexity path's wire keys
// (text/hard/easy) and margin reply.
func TestClient_ComplexityRequestWireContract(t *testing.T) {
	var wire map[string]json.RawMessage
	var path string
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &wire)
		io.WriteString(w, `{"margin":0.4}`)
	})
	margin, err := c.ComplexityMargin("why is this failing", []string{"debug a deadlock"}, []string{"say hi"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/route/complexity" {
		t.Errorf("complexity path = %q, want /v1/route/complexity", path)
	}
	for _, k := range []string{"text", "hard", "easy"} {
		if _, ok := wire[k]; !ok {
			t.Errorf("complexity request missing %q key", k)
		}
	}
	if margin != 0.4 {
		t.Errorf("complexity decoded margin=%v, want 0.4", margin)
	}
}

// TestClient_DomainRequestWireContract pins the domain path + reply fields.
func TestClient_DomainRequestWireContract(t *testing.T) {
	var path string
	var wire map[string]json.RawMessage
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &wire)
		io.WriteString(w, `{"label":"math","confidence":0.9}`)
	})
	label, conf, _, err := c.Domain("prove this theorem")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/route/domain" {
		t.Errorf("domain path = %q, want /v1/route/domain", path)
	}
	if _, ok := wire["text"]; !ok {
		t.Errorf("domain request missing %q key", "text")
	}
	if label != "math" || conf != 0.9 {
		t.Errorf("domain decoded (%q,%v), want (math,0.9) — reply fields are label/confidence", label, conf)
	}
}
