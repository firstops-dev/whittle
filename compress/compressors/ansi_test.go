package compressors

import (
	"context"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
)

func TestANSIStrip(t *testing.T) {
	in := "\x1b[31mred error\x1b[0m line\n\n\n\nafter four blanks\n\x1b[1;32mgreen\x1b[0m"
	res, err := NewANSIStrip().Compress(context.Background(), compress.Input{Content: in})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "\x1b") {
		t.Fatalf("escape codes not stripped: %q", res.Output)
	}
	if strings.Contains(res.Output, "\n\n\n") {
		t.Fatalf("blank-line runs not collapsed: %q", res.Output)
	}
	if !strings.Contains(res.Output, "red error") || !strings.Contains(res.Output, "green") {
		t.Fatalf("visible text lost: %q", res.Output)
	}
	if res.OutChars >= res.InChars {
		t.Fatalf("expected shrink: in=%d out=%d", res.InChars, res.OutChars)
	}
}
