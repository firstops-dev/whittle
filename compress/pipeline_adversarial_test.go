package compress

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// recordingCompressor is a spy: it records whether Compress was ever invoked.
// Registered as the prose chain it lets us assert the prose-safety guard never
// lets code/structured content reach the (prose) compressor.
type recordingCompressor struct{ calls int32 }

func (r *recordingCompressor) Name() string             { return "spy" }
func (r *recordingCompressor) Handles(ContentType) bool { return true }
func (r *recordingCompressor) Compress(_ context.Context, in Input) (Result, error) {
	atomic.AddInt32(&r.calls, 1)
	// Return a short output so, absent the guard, the call would "succeed".
	return Result{Output: "x", Strategy: "spy"}, nil
}

// bigEnough pads s with copies of itself until it clears the gate's min-token
// floor (>=64 tokens ~= >=256 chars) but stays under MaxChars.
func bigEnough(s string) string {
	for len(s) < 400 {
		s += "\n" + s
	}
	return s
}

// ---------------------------------------------------------------------------
// 1. Fail-open: PANIC. The pipeline claims it NEVER returns an error and always
// fails open. A panicking compressor is the adversarial probe: does the pipeline
// recover, or does the panic propagate to the caller? (No recover() exists in
// pipeline.go, so this documents the gap.)
// ---------------------------------------------------------------------------
func TestPipeline_PanicInCompressor_NotRecovered(t *testing.T) {
	panicky := fakeCompressor{name: "panic", fn: func(in Input) (Result, error) {
		panic("kaboom from compressor")
	}}
	p := newPipelineWith(panicky)

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("BUG: pipeline did NOT recover a compressor panic; it propagated to the caller (%v). "+
				"The fail-open contract ('Compress NEVER returns an error') is violated by panics.", r)
		}
	}()

	out := p.Compress(context.Background(), proseInput())
	// If we reach here the pipeline recovered — assert fail-open semantics.
	if out.Action != "skipped" {
		t.Errorf("after recovered panic want action=skipped, got %q", out.Action)
	}
	if out.Output != proseInput().Content {
		t.Errorf("after recovered panic want original passthrough")
	}
}

// ---------------------------------------------------------------------------
// 1b. Fail-open: a later stage in a multi-compressor chain errors. The pipeline
// must discard the partial progress of the earlier stage and return the
// ORIGINAL content, not the half-transformed intermediate.
// ---------------------------------------------------------------------------
func TestPipeline_MidChainError_ReturnsOriginalNotPartial(t *testing.T) {
	in := proseInput()
	ok := fakeCompressor{name: "stage1", fn: func(in Input) (Result, error) {
		return Result{Output: "PARTIAL-TRANSFORMED-" + in.Content[:10]}, nil
	}}
	boom := fakeCompressor{name: "stage2", fn: func(in Input) (Result, error) {
		return Result{}, errors.New("stage2 boom")
	}}
	chains := map[ContentType][]Compressor{TypeLog: {ok, boom}}
	p := NewPipeline(NewRegistry(chains), DefaultGateConfig(), nil)

	out := p.Compress(context.Background(), in)
	if out.Action != "skipped" || out.SkipReason != "error" {
		t.Fatalf("want skipped/error, got %q/%q", out.Action, out.SkipReason)
	}
	if out.Output != in.Content {
		t.Fatalf("fail-open must return ORIGINAL, not partial transform; got %q", out.Output)
	}
	if out.OutChars != len(in.Content) {
		t.Fatalf("OutChars must reset to original length, got %d want %d", out.OutChars, len(in.Content))
	}
}

// ---------------------------------------------------------------------------
// 2. Expansion guardrail: output EXACTLY equal in length to the input must
// trigger passthrough (>=, not >). Also assert the equal-length transformed
// string is discarded in favor of the original.
// ---------------------------------------------------------------------------
func TestPipeline_GuardrailExactEqualLength(t *testing.T) {
	in := proseInput()
	equalLen := fakeCompressor{name: "equal", fn: func(in Input) (Result, error) {
		// Same length, different bytes.
		return Result{Output: strings.Repeat("Z", len(in.Content))}, nil
	}}
	out := newPipelineWith(equalLen).Compress(context.Background(), in)
	if out.Action != "skipped" || out.SkipReason != "guardrail_expansion" {
		t.Fatalf("equal-length output must hit guardrail; got %q/%q", out.Action, out.SkipReason)
	}
	if out.Output != in.Content {
		t.Fatalf("guardrail must return ORIGINAL, not the equal-length transform")
	}
	if strings.Contains(out.Output, "Z") {
		t.Fatalf("equal-length transform leaked into output")
	}
}

// ---------------------------------------------------------------------------
// 3. Prose-safety guard: code/structured content the router lands on `prose`
// must NEVER reach the (prose) compressor. Spy adapter records calls.
// ---------------------------------------------------------------------------
func TestPipeline_ProseSafetyGuard_NeverCallsProseCompressorForCode(t *testing.T) {
	// Single-line JSON object (NOT an array): the router now correctly lands on
	// `json` (detectJSON matches objects, not just arrays). With no json chain
	// registered here it ends in no_compressor — the point being it routes AWAY
	// from the prose compressor (spy must never be called), which is the invariant.
	jsonObject := `{"id":42,"name":"alpha beta gamma","desc":"a fairly long human readable description that runs on for quite a while in order to clear the gate min-token floor without any trouble at all","nested":{"a":1,"b":2,"c":[1,2,3]},"flag":true,"score":99.5,"extra":"yet more and more padding words here so the token floor is comfortably met every time"}`
	codeFenced := bigEnough("```\nthis looks like ordinary prose inside a code fence but the fence is a structural signal\n```")
	minifiedJS := bigEnough(`const a=1;const b=2;function f(){return a+b};const c=f();var d={x:1,y:2,z:3};`)
	goSnippet := bigEnough("package main\n\nfunc main() {\n\tfor i := 0; i < 10; i++ {\n\t\treturn\n\t}\n}")

	cases := []struct {
		name       string
		content    string
		wantReason string // deterministic skip reason
	}{
		{"json_object_not_array", jsonObject, "no_compressor"}, // router -> json, no chain here
		{"code_fenced_block", codeFenced, "code_structured"},
		{"minified_js", minifiedJS, "no_compressor"}, // router -> code, no chain
		{"go_snippet", goSnippet, "no_compressor"},   // router -> code, no chain
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &recordingCompressor{}
			chains := map[ContentType][]Compressor{TypeProse: {spy}}
			p := NewPipeline(NewRegistry(chains), DefaultGateConfig(), nil)

			out := p.Compress(context.Background(), Input{Content: tc.content, MinTokens: DefaultMinTokens})

			if atomic.LoadInt32(&spy.calls) != 0 {
				t.Fatalf("BUG: prose compressor was invoked on %s content (detected=%q klass=%q) — corruption risk",
					tc.name, out.Detected, out.GateKlass)
			}
			if out.Action != "skipped" {
				t.Fatalf("want skipped, got %q", out.Action)
			}
			if out.SkipReason != tc.wantReason {
				t.Errorf("skip reason = %q, want %q (detected=%q klass=%q)",
					out.SkipReason, tc.wantReason, out.Detected, out.GateKlass)
			}
			if out.Output != tc.content {
				t.Errorf("skipped path must return original content unchanged")
			}
		})
	}
}

// 3b. Documented GAP (informational, does not fail): plain YAML/key-value config
// is classified `prose` (default) by the gate and routed `prose` by the router,
// so the prose guard does NOT fire and it WOULD reach LLMLingua. This is the
// hole the guard does not close.
func TestPipeline_ProseGuard_YAMLConfigSlipsThroughToProse(t *testing.T) {
	yaml := bigEnough("service: textcompress\nreplicas: 3\nimage: foo/bar\nenv:\n  - name: PORT\n    value: high\n  - name: MODE\n    value: prod\ntimeout: thirty seconds please")
	spy := &recordingCompressor{}
	chains := map[ContentType][]Compressor{TypeProse: {spy}}
	p := NewPipeline(NewRegistry(chains), DefaultGateConfig(), nil)

	out := p.Compress(context.Background(), Input{Content: yaml, MinTokens: DefaultMinTokens})
	if atomic.LoadInt32(&spy.calls) > 0 {
		t.Logf("RISK (by design gap): YAML/config routed to prose compressor "+
			"(detected=%q klass=%q action=%q). The prose-safety guard only fires when klass==code_structured; "+
			"plain key:value config classifies as prose and is sent to the prose model.",
			out.Detected, out.GateKlass, out.Action)
	} else {
		t.Logf("YAML did NOT reach prose compressor (detected=%q klass=%q reason=%q) — guard or router caught it.",
			out.Detected, out.GateKlass, out.SkipReason)
	}
}

// ---------------------------------------------------------------------------
// 9. Concurrency: many goroutines through the SAME pipeline. Run with -race.
// ---------------------------------------------------------------------------
func TestPipeline_ConcurrentNoRace(t *testing.T) {
	shrink := fakeCompressor{name: "shrink", fn: func(in Input) (Result, error) {
		return Result{Output: in.Content[:len(in.Content)/2]}, nil
	}}
	p := newPipelineWith(shrink)
	inputs := []Input{
		proseInput(),
		{Content: bigEnough("ERROR boom\nINFO tick\nWARN slow"), MinTokens: DefaultMinTokens},
		{Content: "too short", MinTokens: DefaultMinTokens},
	}

	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = p.Compress(context.Background(), inputs[(g+i)%len(inputs)])
			}
		}(g)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// 8 (benchmarks for Detect — the hot path). Reports ns/op + allocs/op.
// Detect re-splits content per detector; looksStructured (in the gate, exercised
// via the pipeline benches) json.Unmarshals the whole body.
// ---------------------------------------------------------------------------
func benchLog(nChars int) string {
	var b strings.Builder
	// Date-only prefix (no HH:MM:SS) so the content routes to `log`, not `search`.
	lines := []string{
		"2024-01-01 INFO starting subsystem and loading configuration values now",
		"2024-01-01 DEBUG cache lookup for key user 1234 returned a hit quickly",
		"2024-01-01 WARN deprecated API used by caller at the module boundary here",
		"2024-01-01 ERROR failed to connect to database after 3 retries giving up now",
	}
	for b.Len() < nChars {
		b.WriteString(lines[b.Len()%len(lines)])
		b.WriteByte('\n')
	}
	return b.String()[:nChars]
}

func benchJSONArray(nChars int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; b.Len() < nChars-2; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":`)
		b.WriteString(strings.Repeat("9", 3))
		b.WriteString(`,"name":"item","status":"ok","value":123.45}`)
	}
	b.WriteByte(']')
	return b.String()
}

func benchProse(nChars int) string {
	out := strings.Repeat("the quarterly review covered hiring plans and roadmap commitments at length today. ", nChars/82+1)
	return out[:nChars]
}

func benchmarkDetect(b *testing.B, content string) {
	b.ReportAllocs()
	b.SetBytes(int64(len(content)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Detect(content)
	}
}

func BenchmarkDetect_Log5k(b *testing.B)   { benchmarkDetect(b, benchLog(5000)) }
func BenchmarkDetect_Log30k(b *testing.B)  { benchmarkDetect(b, benchLog(29000)) }
func BenchmarkDetect_JSON5k(b *testing.B)  { benchmarkDetect(b, benchJSONArray(5000)) }
func BenchmarkDetect_JSON30k(b *testing.B) { benchmarkDetect(b, benchJSONArray(29000)) }
func BenchmarkDetect_Prose5k(b *testing.B) { benchmarkDetect(b, benchProse(5000)) }
func BenchmarkDetect_Prose30k(b *testing.B) {
	benchmarkDetect(b, benchProse(29000))
}
