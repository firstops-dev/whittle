package router

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestProxy_ConcurrentRequestsWithLivePolicySwap stresses the hot-reload seam
// under concurrency.
//
// WHY: the proxy is shared by every in-flight Claude Code request while the policy
// can be hot-swapped (SetPolicy / ReloadFile) at any moment. The policy pointer,
// the session store, and the entitlement blocklist are the shared mutable state.
// This drives N concurrent requests across several sessions through ONE proxy
// while a swapper flips between two valid policies and reloads a file, all under
// -race. A torn read (non-atomic swap) or unsynchronised map access surfaces as a
// race report, a panic, or an empty/garbage verdict; a correct request always
// relays the upstream 200 and carries a valid tier verdict.
func TestProxy_ConcurrentRequestsWithLivePolicySwap(t *testing.T) {
	// Stateless upstream — no shared capture state, so concurrency is exercised in
	// the proxy, not in the test's mock.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "event: message_stop\ndata: {}\n\n")
	}))
	t.Cleanup(srv.Close)

	px := testProxy(t, proxyPolicy, srv)

	// Two valid policies that route "hello" to DIFFERENT tiers, so a torn swap
	// would be observable as a verdict that is neither.
	pol1, _, err := Load([]byte(proxyPolicy)) // hello -> fast
	if err != nil {
		t.Fatal(err)
	}
	altPolicy := strings.Replace(proxyPolicy, `"to":"fast"`, `"to":"smart"`, 1)
	pol2, _, err := Load([]byte(altPolicy)) // hello -> smart
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	reloadPath := filepath.Join(dir, "p.json")
	if err := os.WriteFile(reloadPath, []byte(proxyPolicy), 0o600); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var swapper sync.WaitGroup
	swapper.Add(1)
	go func() {
		defer swapper.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				switch i % 3 {
				case 0:
					px.SetPolicy(pol1)
				case 1:
					px.SetPolicy(pol2)
				case 2:
					_, _ = px.ReloadFile(reloadPath)
				}
			}
		}
	}()

	// N workers across 4 sessions, each firing a burst of requests.
	const workers, perWorker = 32, 25
	var work sync.WaitGroup
	for w := 0; w < workers; w++ {
		work.Add(1)
		go func(w int) {
			defer work.Done()
			sess := fmt.Sprintf("s%d", w%4)
			for j := 0; j < perWorker; j++ {
				// Requested model is NEITHER tier's model, so BOTH policies produce a
				// real rewrite (never a no-op): the verdict is always a concrete tier,
				// and an empty tier would betray a torn policy read.
				req := httptest.NewRequest(http.MethodPost, "/v1/messages",
					strings.NewReader(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hello"}]}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Claude-Code-Session-Id", sess)
				rec := httptest.NewRecorder()
				px.ServeHTTP(rec, req)
				res := rec.Result()
				if res.StatusCode != 200 {
					t.Errorf("request got status %d, want 200", res.StatusCode)
				}
				// "hello" always matches a route, so the verdict is always a real
				// tier — never empty (which a torn policy read could produce).
				if route := res.Header.Get("X-Whittle-Route"); route != "fast" && route != "smart" {
					t.Errorf("torn/invalid verdict tier %q (want fast or smart)", route)
				}
				res.Body.Close()
			}
		}(w)
	}

	work.Wait()    // all requests done
	close(stop)    // stop the swapper
	swapper.Wait() // and join it
}
