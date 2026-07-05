package compress

import (
	"strings"
	"testing"
)

// TestDetect_CRProgressStreamIsTerminal: a colorless progress stream (no ANSI at
// all, many lone CRs, few newlines) must route to terminal so the CR-overwrite
// collapse applies - previously it fell through to prose.
func TestDetect_CRProgressStreamIsTerminal(t *testing.T) {
	var b strings.Builder
	for p := 1; p <= 60; p++ {
		b.WriteString("\rDownloading model.bin ")
		b.WriteString(strings.Repeat("#", p/3))
	}
	b.WriteString("\n")
	ct, _ := Detect(b.String())
	if ct != TypeTerminal {
		t.Fatalf("CR progress stream detected as %q, want terminal", ct)
	}
}

// TestDetect_StrayCRDoesNotHijack: ordinary multi-line content with a couple of
// stray CRs must NOT be claimed as terminal.
func TestDetect_StrayCRDoesNotHijack(t *testing.T) {
	in := "2024-01-01 INFO started\r\n2024-01-01 INFO tick\rx\n2024-01-01 ERROR failed to connect\n" +
		strings.Repeat("2024-01-01 INFO tick handler ok\n", 20)
	ct, _ := Detect(in)
	if ct == TypeTerminal {
		t.Fatalf("stray CR hijacked routing to terminal")
	}
}

// TestDetect_CRDataFileNotTerminal (reviewer B2): a `\r`-record data file (many
// lone CRs, few newlines, unrelated segments) must NOT route to terminal.
func TestDetect_CRDataFileNotTerminal(t *testing.T) {
	in := "alpha,1\rbravo,2\rcharlie,3\rdelta,4\recho,5\rfoxtrot,6\rgolf,7\rhotel,8\r"
	if ct, _ := Detect(in); ct == TypeTerminal {
		t.Fatalf("\\r-record data file routed to terminal (would be collapsed)")
	}
}
