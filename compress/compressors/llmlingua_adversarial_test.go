package compressors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/firstops-dev/whittle/compress"
)

// prose builds a long prose body that routes to TypeProse with klass=prose, so
// it reaches the LLMLingua adapter (the prose guard does NOT fire).
func proseBody() string {
	return strings.Repeat("the team reviewed the quarterly plan and agreed on the next milestone for the product today. ", 6)
}

// prosePipeline wires a pipeline whose ONLY chain is the prose adapter at url.
func prosePipeline(url string) *compress.Pipeline {
	chains := map[compress.ContentType][]compress.Compressor{
		compress.TypeProse: {NewLLMLinguaAdapterWithURL(url)},
	}
	return compress.NewPipeline(compress.NewRegistry(chains), compress.DefaultGateConfig(), nil)
}

// --- Adapter-level unit tests: every failure mode must return an error so the
// pipeline can fail open. ---

func TestLLMLingua_AdapterErrorModes(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }},
		{"404", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }},
		{"garbage_body", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "not json at all") }},
		{"empty_body", func(w http.ResponseWriter, _ *http.Request) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			a := NewLLMLinguaAdapterWithURL(srv.URL)
			_, err := a.Compress(context.Background(), compress.Input{Content: proseBody()})
			if err == nil {
				t.Fatalf("expected error for %s, got nil (pipeline would NOT fail open)", tc.name)
			}
		})
	}
}

// A sidecar action != "compressed" (gate skip, load-shed "busy", guardrail) is a
// CLEAN skip, not an error: the adapter returns Skipped=true with the reason and no
// error, so legitimate skips do not count toward the prose error rate.
func TestLLMLingua_AdapterSkipIsNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"action":"skipped","skip_reason":"busy"}`)
	}))
	defer srv.Close()
	a := NewLLMLinguaAdapterWithURL(srv.URL)
	res, err := a.Compress(context.Background(), compress.Input{Content: proseBody()})
	if err != nil {
		t.Fatalf("sidecar skip must NOT be an error: %v", err)
	}
	if !res.Skipped || res.SkipReason != "busy" {
		t.Fatalf("want Skipped=true reason=busy, got skipped=%v reason=%q", res.Skipped, res.SkipReason)
	}
}

func TestLLMLingua_DeadEndpoint(t *testing.T) {
	// Connection refused: spin a server, capture URL, close it.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	a := NewLLMLinguaAdapterWithURL(url)
	if _, err := a.Compress(context.Background(), compress.Input{Content: proseBody()}); err == nil {
		t.Fatal("expected error from dead endpoint")
	}
}

func TestLLMLingua_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = io.WriteString(w, `{"compressed":"x","action":"compressed"}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled
	a := NewLLMLinguaAdapterWithURL(srv.URL)
	if _, err := a.Compress(ctx, compress.Input{Content: proseBody()}); err == nil {
		t.Fatal("expected error from canceled context")
	}
}

// --- Pipeline-level fail-open integration: inject a failing/erroring/slow real
// adapter and assert action=="skipped", output==ORIGINAL, no error returned. ---

func TestPipeline_FailOpen_ProseAdapterErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"upstream_500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }},
		{"upstream_garbage", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "}{") }},
	}
	body := proseBody()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			p := prosePipeline(srv.URL)
			out := p.Compress(context.Background(), compress.Input{Content: body, MinTokens: compress.DefaultMinTokens})
			if out.Action != "skipped" || out.SkipReason != "error" {
				t.Fatalf("want skipped/error, got %q/%q (detected=%q)", out.Action, out.SkipReason, out.Detected)
			}
			if out.Output != body {
				t.Fatalf("fail-open must return ORIGINAL content")
			}
		})
	}
}

// A sidecar that cleanly skips (the sidecar surfaces its own internal errors AS a
// skip with skip_reason) is passed through as a skip with the reason preserved, NOT
// the pipeline's error fail-open. Either way the ORIGINAL content survives.
func TestPipeline_SidecarSkip_IsCleanSkip(t *testing.T) {
	body := proseBody()
	for _, reason := range []string{"busy", "too_short", "error"} {
		t.Run(reason, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"action":"skipped","skip_reason":"`+reason+`"}`)
			}))
			defer srv.Close()
			out := prosePipeline(srv.URL).Compress(context.Background(), compress.Input{Content: body, MinTokens: compress.DefaultMinTokens})
			if out.Action != "skipped" || out.SkipReason != reason {
				t.Fatalf("want skipped/%s, got %q/%q", reason, out.Action, out.SkipReason)
			}
			if out.Output != body {
				t.Fatalf("must passthrough ORIGINAL content")
			}
		})
	}
}

func TestPipeline_FailOpen_SlowUpstreamTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: waits out a deliberate 1s deadline")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release // hang past the caller's deadline
	}))
	defer srv.Close()
	defer close(release)

	body := proseBody()
	p := prosePipeline(srv.URL)
	// Drive the deadline through the caller's ctx (as the hook handler does)
	// rather than waiting out the adapter's own 8s client timeout - the
	// contract under test is "any timeout maps to a clean budget skip",
	// whichever bound fires first.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	out := p.Compress(ctx, compress.Input{Content: body, MinTokens: compress.DefaultMinTokens})
	elapsed := time.Since(start)

	// A timeout is a latency-BUDGET outcome, not a failure: the adapter maps it
	// to a clean skip (reason prose_budget) so it never counts as an error.
	if out.Action != "skipped" || out.SkipReason != "prose_budget" {
		t.Fatalf("slow upstream must fail open as a budget skip; got %q/%q", out.Action, out.SkipReason)
	}
	if out.Output != body {
		t.Fatal("fail-open must return original")
	}
	if elapsed > 5*time.Second {
		t.Errorf("adapter did not honor its timeout: blocked %v", elapsed)
	}
}

// --- Concurrency: real DefaultChains-style pipeline (log + json + mocked prose)
// driven from many goroutines. Run with -race. ---

func TestPipeline_Concurrent_RealChains_NoRace(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"compressed":"short","action":"compressed"}`)
	}))
	defer mock.Close()

	chains := map[compress.ContentType][]compress.Compressor{
		compress.TypeLog:   {NewANSIStrip(), NewLogCompressor(DefaultLogConfig())},
		compress.TypeJSON:  {NewJSONCrusher()},
		compress.TypeProse: {NewLLMLinguaAdapterWithURL(mock.URL)},
	}
	p := compress.NewPipeline(compress.NewRegistry(chains), compress.DefaultGateConfig(), nil)

	logIn := strings.Repeat("INFO tick\nERROR boom\nWARN slow\n", 30)
	jsonIn := "[" + strings.Repeat(`{"id":1,"v":"x"},`, 40) + `{"id":2,"v":"y"}]`
	proseIn := proseBody()
	inputs := []string{logIn, jsonIn, proseIn}

	var wg sync.WaitGroup
	for g := 0; g < 48; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				out := p.Compress(context.Background(), compress.Input{
					Content: inputs[(g+i)%len(inputs)], MinTokens: compress.DefaultMinTokens,
				})
				_ = out.Output
			}
		}(g)
	}
	wg.Wait()
}

// TestPipeline_PureInfoLog_ReportsCompressedWithEmptyOutput is the end-to-end
// consequence of the LogCompressor empty-output finding: the pipeline reports a
// SUCCESSFUL compression (action=compressed) while having dropped 100% of the
// log. A consumer that trusts action=compressed loses all the data.
func TestPipeline_PureInfoLog_ReportsCompressedWithEmptyOutput(t *testing.T) {
	chains := map[compress.ContentType][]compress.Compressor{
		compress.TypeLog: {NewANSIStrip(), NewLogCompressor(DefaultLogConfig())},
	}
	p := compress.NewPipeline(compress.NewRegistry(chains), compress.DefaultGateConfig(), nil)
	in := strings.Repeat("2024-01-01 INFO routine heartbeat tick, nominal\n", 40)

	out := p.Compress(context.Background(), compress.Input{Content: in, MinTokens: compress.DefaultMinTokens})
	if out.Action == "compressed" && strings.TrimSpace(out.Output) == "" {
		t.Errorf("FINDING (end-to-end): pipeline reports action=%q but Output is EMPTY (in=%d out=%d). "+
			"Pure-INFO logs are silently and entirely deleted while signaling success.",
			out.Action, out.InChars, out.OutChars)
	}
}

// --- Benchmarks for the hot path: Pipeline.Compress on log/json/prose-skipped. ---

func benchLogStr(n int) string {
	var b strings.Builder
	// Date-only prefix (no HH:MM:SS) so the content routes to `log`, exercising the
	// LogCompressor rather than being misrouted to `search`.
	lines := []string{
		"2024-01-01 INFO starting subsystem and loading configuration values now",
		"2024-01-01 DEBUG cache lookup for key user 1234 returned a hit quickly here",
		"2024-01-01 WARN deprecated API used by caller at the module boundary here too",
		"2024-01-01 ERROR failed to connect to the database after three retries giving up",
	}
	for b.Len() < n {
		b.WriteString(lines[b.Len()%len(lines)])
		b.WriteByte('\n')
	}
	return b.String()[:n]
}

func benchJSONStr(n int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; b.Len() < n-2; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":123,"name":"item","status":"ok","value":123.45}`)
	}
	b.WriteByte(']')
	return b.String()
}

// proseSkippedStr is a big JSON OBJECT: routes to prose, klass=code_structured,
// so the guard skips it. Exercises Detect + gate (incl. looksStructured's full
// json.Unmarshal) without any HTTP call.
func proseSkippedStr(n int) string {
	var b strings.Builder
	b.WriteString(`{"meta":{"k":"v"},`)
	for i := 0; b.Len() < n-2; i++ {
		fmt.Fprintf(&b, `"key%d":"value with some words here",`, i)
	}
	b.WriteString(`"end":true}`)
	return b.String()
}

func benchPipeline(b *testing.B, content string) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"compressed":"short","action":"compressed"}`)
	}))
	defer mock.Close()
	chains := map[compress.ContentType][]compress.Compressor{
		compress.TypeLog:   {NewANSIStrip(), NewLogCompressor(DefaultLogConfig())},
		compress.TypeJSON:  {NewJSONCrusher()},
		compress.TypeProse: {NewLLMLinguaAdapterWithURL(mock.URL)},
	}
	p := compress.NewPipeline(compress.NewRegistry(chains), compress.DefaultGateConfig(), nil)
	in := compress.Input{Content: content, MinTokens: compress.DefaultMinTokens}
	b.ReportAllocs()
	b.SetBytes(int64(len(content)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Compress(context.Background(), in)
	}
}

func BenchmarkPipeline_Log5k(b *testing.B)        { benchPipeline(b, benchLogStr(5000)) }
func BenchmarkPipeline_Log29k(b *testing.B)       { benchPipeline(b, benchLogStr(29000)) }
func BenchmarkPipeline_JSON5k(b *testing.B)       { benchPipeline(b, benchJSONStr(5000)) }
func BenchmarkPipeline_JSON29k(b *testing.B)      { benchPipeline(b, benchJSONStr(29000)) }
func BenchmarkPipeline_ProseSkip5k(b *testing.B)  { benchPipeline(b, proseSkippedStr(5000)) }
func BenchmarkPipeline_ProseSkip29k(b *testing.B) { benchPipeline(b, proseSkippedStr(29000)) }

// BenchmarkPipeline_OversizeJSON_SkippedButParsed measures the cost of an
// oversized body: above the global MaxChars it is skipped (too_large) before any
// classify/parse, so the skip is cheap.
func BenchmarkPipeline_OversizeJSON_SkippedButParsed(b *testing.B) {
	content := proseSkippedStr(600000) // > global MaxChars (256 KiB) -> too_large
	chains := map[compress.ContentType][]compress.Compressor{}
	p := compress.NewPipeline(compress.NewRegistry(chains), compress.DefaultGateConfig(), nil)
	in := compress.Input{Content: content, MinTokens: compress.DefaultMinTokens}
	b.ReportAllocs()
	b.SetBytes(int64(len(content)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := p.Compress(context.Background(), in)
		if out.SkipReason != "too_large" {
			b.Fatalf("expected too_large, got %q", out.SkipReason)
		}
	}
}
