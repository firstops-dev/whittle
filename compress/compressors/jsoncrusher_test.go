package compressors

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
)

func crush(t *testing.T, in string) compress.Result {
	t.Helper()
	res, err := NewJSONCrusher().Compress(context.Background(), compress.Input{Content: in, ContentType: compress.TypeJSON})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A uniform array whose repeated keys dominate takes the lossless columnar path:
// every row survives (keys hoisted), and it reconstructs to the exact input.
func TestJSONCrusherColumnarLossless(t *testing.T) {
	items := make([]string, 100)
	for i := range items {
		items[i] = fmt.Sprintf(`{"id":%d,"status":"ok","value":%d}`, i, i*3)
	}
	in := "[" + strings.Join(items, ",") + "]"

	res := crush(t, in)
	if res.OutChars >= res.InChars {
		t.Fatalf("expected shrink: in=%d out=%d", res.InChars, res.OutChars)
	}
	if !strings.HasPrefix(res.Output, `{"__schema__"`) {
		t.Fatalf("expected a columnar envelope, got: %s", truncate(res.Output))
	}
	// Every one of the 100 rows reconstructs exactly (whichever of JSON-rows / CSV
	// the encoder picked as smallest).
	assertNoMutation(t, in, res.Output)
}

func TestJSONCrusherPassthroughSmall(t *testing.T) {
	in := `[{"id":1},{"id":2},{"id":3}]`
	res := crush(t, in)
	if res.Output != in {
		t.Fatalf("small array must pass through: %q", res.Output)
	}
}

func TestJSONCrusherPassthroughNonArray(t *testing.T) {
	// Already-compact object: nothing to strip, so it passes through (the pipeline
	// then skips it as a non-win). Crusher must not expand it.
	in := `{"id":1,"name":"x"}`
	res := crush(t, in)
	if res.Output != in {
		t.Fatalf("already-compact object must pass through: %q", res.Output)
	}
}

func TestJSONCrusherMinifiesObject(t *testing.T) {
	// Pretty-printed object (the common tool-output shape): lossless minify wins.
	in := "{\n  \"id\": 1,\n  \"name\": \"alice\",\n  \"roles\": [\n    \"admin\",\n    \"user\"\n  ]\n}"
	res := crush(t, in)
	if res.OutChars >= res.InChars {
		t.Fatalf("pretty object must shrink: in=%d out=%d", res.InChars, res.OutChars)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Output), &got); err != nil {
		t.Fatalf("minified output not valid JSON: %v\n%s", err, res.Output)
	}
	if got["name"] != "alice" {
		t.Fatalf("minify must be lossless: %v", got)
	}
}

func TestJSONCrusherMinifiesPrettySmallArray(t *testing.T) {
	// Pretty array under the sampling threshold (<5): still gets the minify baseline.
	in := "[\n  { \"id\": 1 },\n  { \"id\": 2 },\n  { \"id\": 3 }\n]"
	res := crush(t, in)
	if res.OutChars >= res.InChars {
		t.Fatalf("pretty small array must shrink via minify: in=%d out=%d", res.InChars, res.OutChars)
	}
}

// When columnar isn't eligible (mixed key sets bail out of the reshape) and the
// minify win is small, the LOSSY path still runs and dedups identical items down
// to a representative subset. This pins the lossy dedup path that lossless-first
// leaves in place for non-uniform arrays.
func TestJSONCrusherLossyDedupWhenNotColumnar(t *testing.T) {
	t.Skip("lossy row-sampling is disabled — see the TODO in jsoncrusher.go Compress; re-enable this test when it is uncommented")
	// Alternate two DIFFERENT shapes so columnarEncode bails (not a single key set),
	// forcing the lossy sampler; the repeated identical items must dedup.
	items := make([]string, 40)
	for i := range items {
		if i%2 == 0 {
			items[i] = `{"status":"ok"}`
		} else {
			items[i] = `{"status":"ok","note":"x"}`
		}
	}
	in := "[" + strings.Join(items, ",") + "]"
	res := crush(t, in)
	var got []map[string]any
	if err := json.Unmarshal([]byte(res.Output), &got); err != nil {
		t.Fatalf("expected a JSON array (lossy path), got: %v\n%s", err, res.Output)
	}
	if len(got) >= 40 {
		t.Fatalf("identical rows not deduped: %d", len(got))
	}
}
