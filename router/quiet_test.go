package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/router/ml"
)

// The zero-config contract: an ABSENT sidecar (connection refused) reads as
// smart-mode-off (ErrMLDisabled — quiet, no ml-degraded), while a PRESENT-but-
// broken sidecar keeps surfacing as a real error (loud). Absence is expected;
// breakage is not.
func TestQuietClassifier_AbsentVsBroken(t *testing.T) {
	// Absent: nothing listening → refused → ErrMLDisabled.
	q := quietClassifier{ml.New("http://127.0.0.1:1")}
	if _, err := q.EmbeddingScore("x", nil); !errors.Is(err, ErrMLDisabled) {
		t.Fatalf("refused connection must read as ErrMLDisabled, got %v", err)
	}
	// Broken: listening but 500 → a real error, NOT ErrMLDisabled.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	qb := quietClassifier{ml.New(srv.URL)}
	if _, err := qb.EmbeddingScore("x", nil); err == nil || errors.Is(err, ErrMLDisabled) {
		t.Fatalf("a broken sidecar must stay a real error, got %v", err)
	}
}

// End-to-end consequence: with the quiet wrapper, a down sidecar routes to the
// default WITHOUT the ml-degraded tag (it is not a degradation, it's absence).
func TestQuietClassifier_AbsenceIsNotDegraded(t *testing.T) {
	p, _ := mustLoad(t, fullPolicy)
	d := Decide(Signals{RecentText: "just a normal ask", ContextTokens: 100, SessionID: "s"},
		p, quietClassifier{ml.New("http://127.0.0.1:1")}, NewMemSessionStore(), "")
	if d.Tier != "main" {
		t.Fatalf("must fall to default, got %q (%s)", d.Tier, d.Reason)
	}
	if strings.Contains(d.Reason, "ml-degraded") {
		t.Fatalf("absence must not tag ml-degraded: %q", d.Reason)
	}
}
