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

func TestClient_IntentSuccess(t *testing.T) {
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/route/intent" {
			t.Errorf("intent hit wrong path %q", r.URL.Path)
		}
		var got intentReq
		json.NewDecoder(r.Body).Decode(&got)
		if got.Text != "fix the failing test" {
			t.Errorf("intent text not forwarded: %q", got.Text)
		}
		io.WriteString(w, `{"label":"debugging","confidence":0.83}`)
	})
	label, conf, err := c.Intent("fix the failing test")
	if err != nil {
		t.Fatal(err)
	}
	if label != "debugging" || conf != 0.83 {
		t.Errorf("got (%q, %v), want (debugging, 0.83)", label, conf)
	}
}

func TestClient_ClassifySuccess(t *testing.T) {
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/route/classify" {
			t.Errorf("classify hit wrong path %q", r.URL.Path)
		}
		var got classifyReq
		json.NewDecoder(r.Body).Decode(&got)
		if got.Text == "" || len(got.Examples["smart"]) == 0 {
			t.Errorf("classify body missing text/examples: %+v", got)
		}
		io.WriteString(w, `{"tier":"smart","confidence":0.71}`)
	})
	tier, conf, err := c.Classify("design a distributed queue", map[string][]string{
		"fast":  {"say hi"},
		"smart": {"architect a system", "design an API"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tier != "smart" || conf != 0.71 {
		t.Errorf("got (%q, %v), want (smart, 0.71)", tier, conf)
	}
}

func TestClient_Non200IsError(t *testing.T) {
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // sidecar shed load
	})
	if _, _, err := c.Classify("x", nil); err == nil {
		t.Fatal("a non-200 sidecar response must be an error (caller fails open)")
	}
}

func TestClient_MalformedJSONIsError(t *testing.T) {
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"label":`) // truncated
	})
	if _, _, err := c.Intent("x"); err == nil {
		t.Fatal("a malformed reply must be an error")
	}
}

func TestClient_TransportErrorIsError(t *testing.T) {
	c := New("http://127.0.0.1:1") // nothing listening
	if _, _, err := c.Intent("x"); err == nil {
		t.Fatal("a transport failure must be an error")
	}
}

func TestClient_TimeoutIsError(t *testing.T) {
	c := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // exceed the tightened test timeout
		io.WriteString(w, `{"tier":"fast","confidence":1}`)
	})
	c.http.Timeout = 40 * time.Millisecond // tighten for a fast test
	if _, _, err := c.Classify("x", nil); err == nil {
		t.Fatal("a slow sidecar must time out to an error, not stall the request path")
	}
}

// TrimsTrailingSlash: New must not produce a double slash that some muxes 404.
func TestClient_TrimsTrailingSlash(t *testing.T) {
	if got := New("http://x:1/").baseURL; got != "http://x:1" {
		t.Errorf("baseURL = %q, want no trailing slash", got)
	}
}
