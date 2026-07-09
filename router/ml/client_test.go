package ml

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeSidecar serves the given handler and returns a Client pointed at it.
func fakeSidecar(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestClient_DomainSuccess(t *testing.T) {
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/route/domain" {
			t.Errorf("domain hit wrong path %q", r.URL.Path)
		}
		var got domainReq
		json.NewDecoder(r.Body).Decode(&got)
		if got.Text != "prove every finite integral domain is a field" {
			t.Errorf("domain text not forwarded: %q", got.Text)
		}
		io.WriteString(w, `{"label":"math","confidence":0.94}`)
	})
	label, conf, _, err := c.Domain("prove every finite integral domain is a field")
	if err != nil {
		t.Fatal(err)
	}
	if label != "math" || conf != 0.94 {
		t.Errorf("got (%q, %v), want (math, 0.94)", label, conf)
	}
}

func TestClient_EmbeddingSuccess(t *testing.T) {
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/route/embedding" {
			t.Errorf("embedding hit wrong path %q", r.URL.Path)
		}
		var got embeddingReq
		json.NewDecoder(r.Body).Decode(&got)
		if got.Text == "" || len(got.Candidates) == 0 {
			t.Errorf("embedding body missing text/candidates: %+v", got)
		}
		io.WriteString(w, `{"score":0.71}`)
	})
	score, err := c.EmbeddingScore("design a distributed queue",
		[]string{"architect a system", "design an API"})
	if err != nil {
		t.Fatal(err)
	}
	if score != 0.71 {
		t.Errorf("got %v, want 0.71", score)
	}
}

func TestClient_ComplexitySuccess(t *testing.T) {
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/route/complexity" {
			t.Errorf("complexity hit wrong path %q", r.URL.Path)
		}
		var got complexityReq
		json.NewDecoder(r.Body).Decode(&got)
		if got.Text == "" || len(got.Hard) == 0 || len(got.Easy) == 0 {
			t.Errorf("complexity body missing text/hard/easy: %+v", got)
		}
		io.WriteString(w, `{"margin":0.21}`)
	})
	margin, err := c.ComplexityMargin("reindex the sharded store under load",
		[]string{"design a fault-tolerant system"}, []string{"say hello"})
	if err != nil {
		t.Fatal(err)
	}
	if margin != 0.21 {
		t.Errorf("got %v, want 0.21", margin)
	}
}

func TestClient_Non200IsError(t *testing.T) {
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // sidecar shed load
	})
	if _, err := c.EmbeddingScore("x", nil); err == nil {
		t.Fatal("a non-200 sidecar response must be an error (caller fails open)")
	}
}

func TestClient_MalformedJSONIsError(t *testing.T) {
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"label":`) // truncated
	})
	if _, _, _, err := c.Domain("x"); err == nil {
		t.Fatal("a malformed reply must be an error")
	}
}

func TestClient_TransportErrorIsError(t *testing.T) {
	c := New("http://127.0.0.1:1") // nothing listening
	if _, _, _, err := c.Domain("x"); err == nil {
		t.Fatal("a transport failure must be an error")
	}
}

func TestClient_TimeoutIsError(t *testing.T) {
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // exceed the tightened test timeout
		io.WriteString(w, `{"score":1}`)
	})
	c.http.Timeout = 40 * time.Millisecond // tighten for a fast test
	if _, err := c.EmbeddingScore("x", nil); err == nil {
		t.Fatal("a slow sidecar must time out to an error, not stall the request path")
	}
}

// TrimsTrailingSlash: New must not produce a double slash that some muxes 404.
func TestClient_TrimsTrailingSlash(t *testing.T) {
	if got := New("http://x:1/").baseURL; got != "http://x:1" {
		t.Errorf("baseURL = %q, want no trailing slash", got)
	}
}
