package compressors

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestJSONCrusher_ConstantFactoring pins opportunity #2: columns byte-identical in
// every row are stored once in "__const__", losslessly (decoder re-adds them).
func TestJSONCrusher_ConstantFactoring(t *testing.T) {
	t.Run("mixed_constant_and_varying", func(t *testing.T) {
		var items []string
		for i := 0; i < 40; i++ {
			items = append(items, fmt.Sprintf(
				`{"name":"pod-%03d","namespace":"production","status":"Running","restarts":0}`, i))
		}
		in := "[" + strings.Join(items, ",") + "]"
		res := crush(t, in)
		if !strings.Contains(res.Output, `"__const__"`) {
			t.Fatalf("expected __const__ in envelope: %s", truncate(res.Output))
		}
		// the varying column stays; the constants leave the schema
		var env struct {
			Schema []string                   `json:"__schema__"`
			Consts map[string]json.RawMessage `json:"__const__"`
		}
		if err := json.Unmarshal([]byte(res.Output), &env); err != nil {
			t.Fatal(err)
		}
		if len(env.Schema) != 1 || env.Schema[0] != "name" {
			t.Fatalf("schema should be just [name], got %v", env.Schema)
		}
		if string(env.Consts["namespace"]) != `"production"` || string(env.Consts["restarts"]) != "0" {
			t.Fatalf("constants wrong: %v", env.Consts)
		}
		assertNoMutation(t, in, res.Output)
	})

	t.Run("near_constant_not_factored", func(t *testing.T) {
		// one deviating row -> the column must stay in the matrix, lossless.
		var items []string
		for i := 0; i < 30; i++ {
			st := "Running"
			if i == 17 {
				st = "Pending"
			}
			items = append(items, fmt.Sprintf(`{"id":%d,"status":"%s"}`, i, st))
		}
		in := "[" + strings.Join(items, ",") + "]"
		res := crush(t, in)
		if strings.Contains(res.Output, `"__const__"`) {
			t.Fatalf("near-constant column must NOT factor: %s", truncate(res.Output))
		}
		assertNoMutation(t, in, res.Output)
	})

	t.Run("absent_somewhere_not_constant", func(t *testing.T) {
		// same value wherever present, but absent in one row -> NOT constant.
		var items []string
		for i := 0; i < 30; i++ {
			if i == 9 {
				items = append(items, fmt.Sprintf(`{"id":%d}`, i))
			} else {
				items = append(items, fmt.Sprintf(`{"id":%d,"env":"prod"}`, i))
			}
		}
		in := "[" + strings.Join(items, ",") + "]"
		res := crush(t, in)
		assertNoMutation(t, in, res.Output) // absent row must round-trip WITHOUT env
	})

	t.Run("constant_flattened_dotted_column", func(t *testing.T) {
		// nested uniform object whose inner field is constant: flatten -> factor.
		var items []string
		for i := 0; i < 30; i++ {
			items = append(items, fmt.Sprintf(
				`{"id":%d,"meta":{"region":"us-east-1","tier":"t%d"}}`, i, i%3))
		}
		in := "[" + strings.Join(items, ",") + "]"
		res := crush(t, in)
		if !strings.Contains(res.Output, `"meta.region"`) || !strings.Contains(res.Output, `"__const__"`) {
			t.Fatalf("expected flattened constant meta.region in __const__: %s", truncate(res.Output))
		}
		assertNoMutation(t, in, res.Output)
	})

	t.Run("all_constant_columns", func(t *testing.T) {
		var items []string
		for i := 0; i < 25; i++ {
			items = append(items, `{"status":"ok","code":200}`)
		}
		in := "[" + strings.Join(items, ",") + "]"
		res := crush(t, in)
		assertNoMutation(t, in, res.Output) // 25 identical rows must all reconstruct
	})
}
