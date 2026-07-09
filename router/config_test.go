package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- config lifecycle ---

func TestLoadPolicyFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "p.json")
	if err := os.WriteFile(good, []byte(proxyPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPolicyFile(good); err != nil {
		t.Fatalf("valid file should load: %v", err)
	}
	if _, _, err := LoadPolicyFile(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing file should error")
	}
}

// A bad hot-reload keeps the currently-running policy (never break a live proxy).
func TestReloadFile_KeepsOldOnError(t *testing.T) {
	srv, cap := mockUpstream(t)
	px := testProxy(t, proxyPolicy, srv)

	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.json")
	os.WriteFile(badPath, []byte(`{"version":1,"tiers":[]}`), 0o600) // invalid: no tiers

	if _, err := px.ReloadFile(badPath); err == nil {
		t.Fatal("reloading an invalid policy should error")
	}
	// The OLD policy still routes: "hello" → fast (haiku), down-route from opus.
	doPost(t, px, "/v1/messages",
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`, "")
	if cap.model != "claude-haiku-4-5" {
		t.Errorf("old policy must still be active after a failed reload; upstream model=%q", cap.model)
	}
}

// --- session eviction ---

func newClockedStore(ttl time.Duration, max int, clk *time.Time) *MemSessionStore {
	return &MemSessionStore{
		m: map[string]sessEntry{}, ttl: ttl, max: max,
		now: func() time.Time { return *clk },
	}
}

func TestMemSessionStore_TTLExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newClockedStore(30*time.Minute, 100, &now)
	s.Set("sess", "smart")
	if tier, ok := s.Current("sess"); !ok || tier != "smart" {
		t.Fatalf("fresh entry should be present: %q %v", tier, ok)
	}
	now = now.Add(31 * time.Minute) // past TTL
	if _, ok := s.Current("sess"); ok {
		t.Fatal("entry should have expired after TTL")
	}
}

func TestMemSessionStore_LRUEviction(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newClockedStore(time.Hour, 2, &now) // cap = 2
	s.Set("a", "fast")
	now = now.Add(time.Second)
	s.Set("b", "main")
	now = now.Add(time.Second)
	s.Set("c", "smart") // over cap → oldest ("a") evicted
	if _, ok := s.Current("a"); ok {
		t.Error("oldest entry 'a' should have been evicted")
	}
	if _, ok := s.Current("c"); !ok {
		t.Error("newest entry 'c' should be present")
	}
}

// --- observability ---

type capLogger struct{ lines []string }

func (c *capLogger) Printf(f string, a ...any) { c.lines = append(c.lines, fmt.Sprintf(f, a...)) }

// The per-request log line carries the verdict + outcome, and NEVER the prompt.
func TestProxy_LogLineNoPromptText(t *testing.T) {
	srv, _ := mockUpstream(t)
	pol, _, err := Load([]byte(proxyPolicy))
	if err != nil {
		t.Fatal(err)
	}
	lg := &capLogger{}
	px := NewProxy(pol, nil, NewMemSessionStore(), lg)
	px.baseURL = srv.URL

	doPost(t, px, "/v1/messages",
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello SECRET_PROMPT_XYZ"}]}`, "")

	if len(lg.lines) != 1 {
		t.Fatalf("expected exactly one log line, got %d", len(lg.lines))
	}
	line := lg.lines[0]
	if strings.Contains(line, "SECRET_PROMPT_XYZ") {
		t.Fatalf("LOG LEAKED PROMPT TEXT: %s", line)
	}
	for _, want := range []string{`"tier":"fast"`, `"status":200`, `"reason":`, `"latency_ms":`, `"ctx_tokens":`} {
		if !strings.Contains(line, want) {
			t.Errorf("log line missing %s: %s", want, line)
		}
	}
}
