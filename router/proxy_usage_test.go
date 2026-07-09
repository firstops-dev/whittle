package router

import "testing"

// The usage scanner pulls input/output token counts out of a streamed Anthropic
// SSE response for the cost-savings log fields.
func TestUsageScanner_SSE(t *testing.T) {
	var s usageScanner
	// message_start carries input + a starting output; message_delta accumulates.
	s.feed([]byte(`event: message_start\ndata: {"type":"message_start","message":{"usage":{"input_tokens":1200,"output_tokens":1}}}`))
	s.feed([]byte(`event: message_delta\ndata: {"type":"message_delta","usage":{"output_tokens":47}}`))
	if s.in != 1200 || s.out != 47 {
		t.Errorf("got in=%d out=%d, want in=1200 out=47", s.in, s.out)
	}
}

// A token count split across a chunk boundary is still captured (carry buffer).
func TestUsageScanner_SplitChunk(t *testing.T) {
	var s usageScanner
	s.feed([]byte(`{"usage":{"output_to`))
	s.feed([]byte(`kens":12345}}`))
	if s.out != 12345 {
		t.Errorf("split output_tokens not captured: got %d", s.out)
	}
}

func TestMaxIntField(t *testing.T) {
	b := []byte(`{"input_tokens":10,"nested":{"input_tokens": 999},"output_tokens":0}`)
	if v := maxIntField(b, "input_tokens"); v != 999 { // max across occurrences
		t.Errorf("maxIntField(input_tokens) = %d, want 999", v)
	}
	if v := maxIntField(b, "output_tokens"); v != 0 {
		t.Errorf("maxIntField(output_tokens) = %d, want 0", v)
	}
	if v := maxIntField(b, "absent"); v != 0 {
		t.Errorf("absent field should be 0, got %d", v)
	}
}
