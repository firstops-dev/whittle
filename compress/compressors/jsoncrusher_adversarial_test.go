package compressors

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
)

// canon compacts a JSON value for structural comparison (mirrors the crusher's
// internal canonicalization).
func canon(t *testing.T, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("input not valid JSON: %v\n%s", err, raw)
	}
	return buf.String()
}

// canonAny normalizes a JSON value order-independently (Go marshals map keys
// sorted), so object equality ignores key order and byte-level escaping.
func canonAny(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("canonAny: invalid JSON: %v\n%s", err, raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("canonAny: marshal: %v", err)
	}
	return string(b)
}

// reconstructIfColumnar detects the lossless columnar shape ({"__schema__","__rows__"})
// and rebuilds the original array (obj_i = {schema[j]: rows[i][j]}). ok=false when
// the output is not columnar (the ordinary subset-array path).
func reconstructIfColumnar(t *testing.T, out string) ([]json.RawMessage, bool) {
	t.Helper()
	var obj struct {
		Schema []string                   `json:"__schema__"`
		Rows   [][]json.RawMessage        `json:"__rows__"`
		Types  []string                   `json:"__types__"`
		CSV    string                     `json:"__csv__"`
		Absent map[string][]int           `json:"__absent__"`
		Nested map[string][]string        `json:"__nested__"`
		Consts map[string]json.RawMessage `json:"__const__"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil || obj.Schema == nil {
		return nil, false
	}

	// Materialize rows as [][]json.RawMessage regardless of the encoding.
	var rows [][]json.RawMessage
	switch {
	case obj.Types != nil: // CSV encoding
		if len(obj.Types) != len(obj.Schema) {
			t.Fatalf("csv __types__ arity %d != schema %d", len(obj.Types), len(obj.Schema))
		}
		r := csv.NewReader(strings.NewReader(obj.CSV))
		r.FieldsPerRecord = len(obj.Schema)
		recs, err := r.ReadAll()
		if err != nil {
			t.Fatalf("csv parse: %v\n%q", err, obj.CSV)
		}
		rows = make([][]json.RawMessage, len(recs))
		for i, rec := range recs {
			row := make([]json.RawMessage, len(rec))
			for c, f := range rec {
				if obj.Types[c] == "string" {
					b, err := json.Marshal(f)
					if err != nil {
						t.Fatalf("csv string re-encode: %v", err)
					}
					row[c] = b
				} else {
					row[c] = json.RawMessage(f) // int/float/bool/json render verbatim
				}
			}
			rows[i] = row
		}
	case obj.Rows != nil: // JSON-rows encoding
		rows = obj.Rows
	default:
		return nil, false
	}

	items := make([]json.RawMessage, len(rows))
	for i, row := range rows {
		if len(row) != len(obj.Schema) {
			t.Fatalf("columnar row %d arity %d != schema %d", i, len(row), len(obj.Schema))
		}
		absent := map[int]bool{}
		for _, c := range obj.Absent[strconv.Itoa(i)] {
			absent[c] = true
		}
		m := make(map[string]json.RawMessage, len(obj.Schema))
		for j, k := range obj.Schema {
			if absent[j] {
				continue // key was absent in this row (distinct from present-null)
			}
			m[k] = row[j]
		}
		// Re-add factored constants BEFORE un-nesting (a flattened dotted column
		// can itself be constant; the parent rebuild must see it).
		for k, v := range obj.Consts {
			m[k] = v
		}
		// Un-nest flattened columns: rebuild each parent object from EXACTLY the
		// recorded dotted columns, leaving any real dotted key untouched.
		for parent, inner := range obj.Nested {
			innerObj := make(map[string]json.RawMessage, len(inner))
			for _, k := range inner {
				dotted := parent + "." + k
				if v, ok := m[dotted]; ok {
					innerObj[k] = v
					delete(m, dotted)
				}
			}
			if len(innerObj) > 0 { // empty => parent was absent in this row
				nb, err := json.Marshal(innerObj)
				if err != nil {
					t.Fatalf("un-nest marshal: %v", err)
				}
				m[parent] = nb
			}
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("reconstruct marshal: %v", err)
		}
		items[i] = b
	}
	return items, true
}

// assertNoMutation is the core anti-mutation invariant across BOTH output shapes:
//   - columnar (lossless): reconstructs to EXACTLY the input array, in order - no
//     row dropped, no value changed.
//   - array (minify or lossy sample): every element is structurally one of the
//     input elements, nothing invented, count never grows.
func assertNoMutation(t *testing.T, in, out string) {
	t.Helper()
	if recon, ok := reconstructIfColumnar(t, out); ok {
		var inItems []json.RawMessage
		if err := json.Unmarshal([]byte(in), &inItems); err != nil {
			t.Fatalf("input not a JSON array: %v", err)
		}
		if len(recon) != len(inItems) {
			t.Fatalf("columnar is NOT lossless: input %d rows, reconstructed %d", len(inItems), len(recon))
		}
		for i := range inItems {
			if canonAny(t, inItems[i]) != canonAny(t, recon[i]) {
				t.Errorf("columnar row %d not lossless:\n in=%s\nout=%s", i, inItems[i], recon[i])
			}
		}
		return
	}
	assertValidSubset(t, in, out)
}

// assertValidSubset is the array-path invariant: output is valid JSON and every
// output element is structurally one of the input elements (nothing invented/mutated).
func assertValidSubset(t *testing.T, in, out string) {
	t.Helper()
	var inItems, outItems []json.RawMessage
	if err := json.Unmarshal([]byte(in), &inItems); err != nil {
		t.Fatalf("input not a JSON array: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &outItems); err != nil {
		t.Fatalf("output is NOT valid JSON array: %v\n%s", err, out)
	}
	inSet := map[string]int{}
	for _, it := range inItems {
		inSet[canon(t, it)]++
	}
	for i, it := range outItems {
		k := canon(t, it)
		if inSet[k] == 0 {
			t.Errorf("output item #%d not present in input (invented/mutated): %s", i, it)
		}
	}
	if len(outItems) > len(inItems) {
		t.Errorf("output has MORE items (%d) than input (%d)", len(outItems), len(inItems))
	}
}

// TestJSONCrusher_UnionSparseLossless pins SmartCrusher gap 1 (union/sparse
// schema): objects with DIFFERING key sets (an optional field) and a present-null
// must still take the columnar path AND round-trip exactly - an absent key and a
// present `null` are distinguished via "__absent__". Before this, any key-set
// difference made columnarEncode bail entirely.
func TestJSONCrusher_UnionSparseLossless(t *testing.T) {
	var items []string
	for i := 0; i < 60; i++ {
		switch i % 3 {
		case 0:
			items = append(items, fmt.Sprintf(`{"id":%d,"name":"n%d","status":"ok"}`, i, i))
		case 1: // optional extra field
			items = append(items, fmt.Sprintf(`{"id":%d,"name":"n%d","status":"ok","note":"x%d"}`, i, i, i))
		default: // present-null (NOT absent)
			items = append(items, fmt.Sprintf(`{"id":%d,"name":null,"status":"ok"}`, i))
		}
	}
	in := "[" + strings.Join(items, ",") + "]"
	res := crush(t, in)
	if !strings.HasPrefix(res.Output, `{"__schema__"`) {
		t.Fatalf("union-sparse array must take the columnar path, got: %s", truncate(res.Output))
	}
	if res.OutChars >= res.InChars {
		t.Fatalf("union-sparse columnar must shrink: in=%d out=%d", res.InChars, res.OutChars)
	}
	// Exact lossless round-trip: absent 'note' vs present-null 'name' both recover.
	assertNoMutation(t, in, res.Output)
}

// TestJSONCrusher_DisjointKeysNoBlowup: when every object carries a unique key,
// the union schema grows with N and the row matrix would be O(N²). columnarEncode
// must early-abort before allocating it (reviewer B1) - this array must pass
// through unchanged and the call must stay cheap (no OOM/hang).
func TestJSONCrusher_DisjointKeysNoBlowup(t *testing.T) {
	var items []string
	for i := 0; i < 4000; i++ {
		items = append(items, fmt.Sprintf(`{"k%d":%d}`, i, i))
	}
	in := "[" + strings.Join(items, ",") + "]"
	res := crush(t, in) // pre-fix this allocated ~1.6 GB; now early-aborts
	if res.Output != in {
		t.Fatalf("disjoint-key array must pass through unchanged (no columnar win possible)")
	}
}

// TestJSONCrusher_CSVEncodingLossless stresses the typed-CSV rendering (SmartCrusher
// gap 2-3): string cells shed their JSON quotes, so escaping (commas/quotes/newlines/
// empty/unicode) and the null/mixed -> "json" column fallback must be exactly
// reversible. Each array is padded to enough rows that the CSV rendering wins, then
// round-tripped through assertNoMutation.
func TestJSONCrusher_CSVEncodingLossless(t *testing.T) {
	pad := func(base []string) string {
		var all []string
		for r := 0; r < 12; r++ {
			all = append(all, base...)
		}
		return "[" + strings.Join(all, ",") + "]"
	}
	cases := []struct {
		name string
		base []string
	}{
		{"specials", []string{
			`{"a":"has,comma","b":"line1\nline2"}`,
			`{"a":"has\"quote","b":"tab\there"}`,
			`{"a":"","b":"plain"}`,      // empty string vs a real value
			`{"a":"café☕","b":"naïve"}`, // unicode
			`{"a":"123","b":"true"}`,    // strings that look like int/bool
		}},
		{"null_forces_json_col", []string{
			`{"a":"x","b":1}`,
			`{"a":null,"b":2}`, // null in 'a' -> column 'a' must render as raw json
			`{"a":"y","b":3}`,
			`{"a":"z","b":4}`,
			`{"a":"w","b":5}`,
		}},
		{"mixed_type_col", []string{
			`{"a":"x","b":"p"}`,
			`{"a":1,"b":"q"}`, // 'a' mixes string+int -> json column
			`{"a":true,"b":"r"}`,
			`{"a":"y","b":"s"}`,
			`{"a":"z","b":"t"}`,
		}},
		{"crlf", []string{ // reviewer B1: embedded CR must not be dropped
			`{"a":"line1\r\nline2","b":"p"}`,
			`{"a":"end\r","b":"q"}`, // trailing CR
			`{"a":"\r\n","b":"r"}`,  // just CRLF
			`{"a":"plain","b":"s"}`,
			`{"a":"mid\rdle","b":"t"}`, // bare CR
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := pad(tc.base)
			// End-to-end: whichever encoding wins the (token-based) adoption must
			// round-trip exactly. Adoption is by estimated tokens now, so a case may
			// legitimately ship as JSON-rows.
			res := crush(t, in)
			assertNoMutation(t, in, res.Output)

			// AND the CSV renderer itself must be lossless whenever it can render at
			// all - its escaping is exercised directly, independent of adoption.
			var items []json.RawMessage
			if err := json.Unmarshal([]byte(in), &items); err != nil {
				t.Fatal(err)
			}
			if m, ok := buildColumnarModel(items); ok {
				if enc, ok := renderColumnarCSV(m); ok {
					assertNoMutation(t, in, string(enc))
				}
			}
		})
	}
}

// TestJSONCrusher_NumericPrecisionByteExact guards value BYTE-exactness (reviewer
// C1): assertNoMutation's canonAny normalizes numbers to float64 and so cannot see
// a precision/formatting regression. Values are kept as raw tokens, so a big int64
// and a high-precision float must reconstruct byte-for-byte - checked directly here.
func TestJSONCrusher_NumericPrecisionByteExact(t *testing.T) {
	var items []string
	for i := 0; i < 20; i++ {
		items = append(items, fmt.Sprintf(
			`{"big":9223372036854775807,"flt":0.123456789012345678,"s":"r%d"}`, i))
	}
	in := "[" + strings.Join(items, ",") + "]"
	res := crush(t, in)
	recon, ok := reconstructIfColumnar(t, res.Output)
	if !ok {
		t.Fatalf("expected a columnar envelope, got: %s", truncate(res.Output))
	}
	for i, obj := range recon {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(obj, &m); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		if got := string(m["big"]); got != "9223372036854775807" {
			t.Errorf("row %d big int not byte-exact: %s", i, got)
		}
		if got := string(m["flt"]); got != "0.123456789012345678" {
			t.Errorf("row %d float not byte-exact: %s", i, got)
		}
	}
}

// TestJSONCrusher_NestedFlatteningLossless pins SmartCrusher gap 4: a column whose
// every present cell is an object with the same key set is flattened into dotted
// columns, recorded in "__nested__" so a real key containing a dot is never
// mis-nested. Every case must round-trip exactly.
func TestJSONCrusher_NestedFlatteningLossless(t *testing.T) {
	mk := func(tmpl func(i int) string, n int) string {
		rows := make([]string, n)
		for i := range rows {
			rows[i] = tmpl(i)
		}
		return "[" + strings.Join(rows, ",") + "]"
	}

	t.Run("uniform_nested_flattens", func(t *testing.T) {
		in := mk(func(i int) string {
			return fmt.Sprintf(`{"id":%d,"meta":{"region":"us","tier":"gold"}}`, i)
		}, 40)
		res := crush(t, in)
		if !strings.Contains(res.Output, `"__nested__"`) || !strings.Contains(res.Output, "meta.region") {
			t.Fatalf("expected flattening into dotted columns, got: %s", truncate(res.Output))
		}
		assertNoMutation(t, in, res.Output)
	})

	t.Run("mixed_inner_keys_stay_nested", func(t *testing.T) {
		in := mk(func(i int) string {
			if i%2 == 0 {
				return fmt.Sprintf(`{"id":%d,"meta":{"region":"us"}}`, i)
			}
			return fmt.Sprintf(`{"id":%d,"meta":{"region":"eu","tier":"x"}}`, i)
		}, 40)
		res := crush(t, in)
		if strings.Contains(res.Output, `"__nested__"`) {
			t.Fatalf("must NOT flatten mixed inner key sets: %s", truncate(res.Output))
		}
		assertNoMutation(t, in, res.Output)
	})

	t.Run("real_dotted_key_not_misnested", func(t *testing.T) {
		// a REAL top-level key "meta.region" collides with the would-be flattened
		// column, so flattening must be skipped and both must survive as themselves.
		in := mk(func(i int) string {
			return fmt.Sprintf(`{"id":%d,"meta":{"region":"us","tier":"gold"},"meta.region":"REAL%d"}`, i, i)
		}, 40)
		res := crush(t, in)
		assertNoMutation(t, in, res.Output)
	})

	t.Run("absent_parent_and_present_null_inner", func(t *testing.T) {
		in := mk(func(i int) string {
			switch i % 3 {
			case 0:
				return fmt.Sprintf(`{"id":%d,"meta":{"a":"x","b":"y"}}`, i)
			case 1:
				return fmt.Sprintf(`{"id":%d}`, i) // meta absent
			default:
				return fmt.Sprintf(`{"id":%d,"meta":{"a":null,"b":"z"}}`, i) // inner present-null
			}
		}, 42)
		res := crush(t, in)
		assertNoMutation(t, in, res.Output)
	})
}

func TestJSONCrusher_EdgeArrays(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		passthrough bool // expect verbatim passthrough
	}{
		{"empty_array", `[]`, true},
		{"single_item", `[{"a":1}]`, true},
		{"four_items_below_threshold", `[1,2,3,4]`, true},
		{"array_of_numbers", `[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20]`, false},
		{"array_of_strings", `["a","b","c","d","e","f","g","h","i","j","k","l"]`, false},
		{"mixed_types", `[1,"two",{"three":3},[4],null,true,5,"six",7,8,9,10]`, false},
		{"nested_arrays", `[[1,2],[3,4],[5,6],[7,8],[9,10],[11,12],[13,14],[15,16]]`, false},
		{"all_duplicates", `[{"s":"ok"},{"s":"ok"},{"s":"ok"},{"s":"ok"},{"s":"ok"},{"s":"ok"},{"s":"ok"},{"s":"ok"}]`, false},
		{"unicode", `[{"n":"café"},{"n":"naïve"},{"n":"日本語"},{"n":"emoji😀"},{"n":"Ω"},{"n":"ß"},{"n":"x"},{"n":"y"}]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := crush(t, tc.in)
			// Output must ALWAYS be non-mutating: a subset array, or lossless columnar.
			assertNoMutation(t, tc.in, res.Output)
			if tc.passthrough && res.Output != tc.in {
				t.Errorf("expected verbatim passthrough, got %q", res.Output)
			}
			if !tc.passthrough && res.OutChars > res.InChars {
				t.Errorf("output expanded: in=%d out=%d", res.InChars, res.OutChars)
			}
		})
	}
}

// TestJSONCrusher_MalformedTruncated must fail open (passthrough), never panic.
func TestJSONCrusher_MalformedTruncated(t *testing.T) {
	bad := []string{
		`[{"a":1},{"a":`,                // truncated
		`[1,2,3,`,                       // trailing comma + truncation
		`[{"a":1} {"b":2}]`,             // missing comma
		`[`,                             // lone bracket
		`[}]`,                           // garbage
		`["unterminated string]`,        // bad string
		`[` + strings.Repeat("[", 5000), // deeply unbalanced
	}
	for _, in := range bad {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("PANIC on malformed input %q: %v", truncate(in), r)
				}
			}()
			res, err := NewJSONCrusher().Compress(context.Background(),
				compress.Input{Content: in, ContentType: compress.TypeJSON})
			if err != nil {
				t.Errorf("crusher returned error (should fail open) for %q: %v", truncate(in), err)
			}
			if res.Output != in {
				t.Errorf("malformed input must pass through verbatim; in=%q out=%q", truncate(in), truncate(res.Output))
			}
		}()
	}
}

// TestJSONCrusher_HTMLCharsRepresentation documents that kept items get re-encoded
// by json.Marshal of []json.RawMessage, which HTML-escapes < > &. The VALUE is
// preserved (subset holds via canonicalization) but the raw bytes change.
func TestJSONCrusher_HTMLCharsRepresentation(t *testing.T) {
	items := make([]string, 12)
	for i := range items {
		items[i] = fmt.Sprintf(`{"id":%d,"html":"<div class=\"x\">a & b</div>"}`, i)
	}
	in := "[" + strings.Join(items, ",") + "]"
	res := crush(t, in)
	assertNoMutation(t, in, res.Output) // lossless columnar or subset - never mutated

	if strings.Contains(res.Output, `<`) || strings.Contains(res.Output, `&`) {
		t.Logf("NOTE: JSONCrusher HTML-escapes kept items (< > & -> \\u003c \\u003e \\u0026) "+
			"via json.Marshal([]json.RawMessage). Semantically equal but byte-mutated; "+
			"a consumer doing raw string matching on the output would be surprised.\nsample: %s", truncate(res.Output))
	}
}

// TestJSONCrusher_VeryLargeArray - a large uniform array takes the lossless
// columnar path (all rows kept, keys hoisted): must shrink, stay lossless, and
// not panic. The lossy 15-item cap does NOT apply here - lossless-first keeps
// every row when key-hoisting clears the savings floor.
func TestJSONCrusher_VeryLargeArray(t *testing.T) {
	items := make([]string, 50000)
	for i := range items {
		items[i] = fmt.Sprintf(`{"i":%d,"v":"x"}`, i)
	}
	in := "[" + strings.Join(items, ",") + "]"
	res := crush(t, in)
	assertNoMutation(t, in, res.Output)
	recon, ok := reconstructIfColumnar(t, res.Output)
	if !ok {
		t.Fatalf("expected columnar output for large uniform array")
	}
	if len(recon) != 50000 {
		t.Errorf("columnar must keep every row: got %d, want 50000", len(recon))
	}
	if res.OutChars >= res.InChars {
		t.Errorf("large array did not shrink")
	}
}

// TestJSONCrusher_DistinctItemsOrderPreserved - the columnar path preserves row
// order exactly (rows[i] is items[i]).
func TestJSONCrusher_DistinctItemsOrderPreserved(t *testing.T) {
	items := make([]string, 40)
	for i := range items {
		items[i] = fmt.Sprintf(`{"seq":%d}`, i)
	}
	in := "[" + strings.Join(items, ",") + "]"
	res := crush(t, in)
	recon, ok := reconstructIfColumnar(t, res.Output)
	if !ok {
		t.Fatalf("expected columnar output, got %q", res.Output)
	}
	if len(recon) != 40 {
		t.Fatalf("columnar dropped rows: got %d, want 40", len(recon))
	}
	for i, it := range recon {
		var v struct {
			Seq int `json:"seq"`
		}
		if err := json.Unmarshal(it, &v); err != nil {
			t.Fatalf("row %d invalid: %v", i, err)
		}
		if v.Seq != i {
			t.Errorf("order not preserved: row[%d].seq=%d, want %d", i, v.Seq, i)
		}
	}
}

func truncate(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
