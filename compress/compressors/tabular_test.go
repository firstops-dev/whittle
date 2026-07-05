package compressors

import (
	"context"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
)

func crushTab(t *testing.T, in string) compress.Result {
	t.Helper()
	res, err := NewTabularCompressor().Compress(context.Background(), compress.Input{Content: in, ContentType: compress.TypeTabular})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestTabular_SpaceAlignedLosslessCollapse(t *testing.T) {
	in := "NAME                     READY   STATUS    RESTARTS   AGE\n" +
		"web-7d9f8c-abcde         1/1     Running   0          5d\n" +
		"api-5c6b7a-fghij         1/1     Running   2          3d\n" +
		"db-9a8b7c-klmno          0/1     Pending   0          1h"
	res := crushTab(t, in)
	if res.OutChars >= res.InChars {
		t.Fatalf("space-aligned table must shrink: in=%d out=%d", res.InChars, res.OutChars)
	}
	// Lossless: every cell value survives (tabs replace the alignment padding).
	for _, tok := range []string{"web-7d9f8c-abcde", "Running", "Pending", "RESTARTS", "5d", "0/1"} {
		if !strings.Contains(res.Output, tok) {
			t.Fatalf("dropped cell value %q from:\n%s", tok, res.Output)
		}
	}
	if strings.Contains(res.Output, "   ") {
		t.Fatalf("alignment padding not collapsed:\n%q", res.Output)
	}
}

func TestTabular_PsqlPipeTable(t *testing.T) {
	in := " id |  name  | score\n----+--------+-------\n  1 | alice  |    90\n  2 | bob    |    85\n  3 | carol  |    77"
	res := crushTab(t, in)
	if res.OutChars >= res.InChars {
		t.Fatalf("psql table must shrink: in=%d out=%d", res.InChars, res.OutChars)
	}
	if strings.Contains(res.Output, "---") {
		t.Fatalf("separator row not dropped:\n%s", res.Output)
	}
	for _, tok := range []string{"alice", "carol", "score", "90"} {
		if !strings.Contains(res.Output, tok) {
			t.Fatalf("dropped value %q from:\n%s", tok, res.Output)
		}
	}
}

func TestTabular_LongTableSampled(t *testing.T) {
	var b strings.Builder
	b.WriteString("ID     VALUE\n")
	for i := 0; i < 200; i++ {
		b.WriteString("row" + strings.Repeat("x", 3) + "   data" + strings.Repeat("y", 5) + "\n")
	}
	res := crushTab(t, b.String())
	if res.OutChars >= res.InChars {
		t.Fatalf("long table must shrink: in=%d out=%d", res.InChars, res.OutChars)
	}
	if !strings.Contains(res.Output, "rows omitted") {
		t.Fatalf("expected an omitted-rows marker:\n%s", res.Output)
	}
	if !strings.HasPrefix(res.Output, "ID\tVALUE") {
		t.Fatalf("header must be preserved first:\n%s", res.Output)
	}
}

func TestTabular_AlreadyMinimalPassthrough(t *testing.T) {
	// Compact CSV has no padding to strip - must not expand it.
	in := "id,name,score\n1,alice,90\n2,bob,85"
	res := crushTab(t, in)
	if res.Output != in {
		t.Fatalf("compact CSV must pass through unchanged: %q", res.Output)
	}
}
