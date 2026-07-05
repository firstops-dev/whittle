package compressors

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/firstops-dev/whittle/compress"
)

// losslessMinSavings is the floor a lossless candidate must clear (savings vs the
// ORIGINAL input) to be preferred over lossy row-dropping. Below it, the lossless
// win is too small to be worth reshaping and we fall to representative sampling.
// Mirrors headroom's lossless_min_savings_ratio (0.15).
const losslessMinSavings = 0.15

// JSONCrusher reduces JSON losslessly - it never drops rows:
//   - Minify (lossless, any JSON): json.Compact strips insignificant whitespace.
//   - Columnar reshape (lossless, uniform top-level array of objects): factor the
//     repeated keys out into {"__schema__":[keys],"__rows__":[[vals],...]}. Every
//     row survives; values are kept as raw JSON so types/nesting are exact and
//     reconstruction is obj_i = {schema[j]: rows[i][j]}.
//
// If nothing beats the input, passthrough (the pipeline's expansion guardrail then
// skips it).
//
// The LOSSY representative-sampling path (first 30% ∪ last 15% ∪ one per distinct
// value, capped at maxItems) is currently DISABLED - see the TODO in Compress. Its
// helpers (selectItems/ceilPct/canonical, savingsRatio, losslessMinSavings, the
// maxItems field) are kept intact so re-enabling is just uncommenting.
type JSONCrusher struct{ maxItems int }

func NewJSONCrusher() JSONCrusher { return JSONCrusher{maxItems: 15} }

func (JSONCrusher) Name() string { return "json_crusher" }

func (JSONCrusher) Handles(ct compress.ContentType) bool { return ct == compress.TypeJSON }

func (j JSONCrusher) Compress(_ context.Context, in compress.Input) (compress.Result, error) {
	passthrough := compress.Result{Output: in.Content, Strategy: j.Name(), InChars: len(in.Content), OutChars: len(in.Content)}

	s := strings.TrimSpace(in.Content)
	if s == "" {
		return passthrough, nil
	}

	// Baseline: lossless minification. json.Compact also validates, so an invalid
	// body falls through to passthrough untouched.
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(s)); err != nil {
		return passthrough, nil
	}
	best := compact.Bytes()
	origLen := len(in.Content)

	if s[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(best, &items); err == nil && len(items) > 0 {
			// Lossless-first: build the union-schema columnar model once, then try
			// each rendering (JSON rows, typed CSV) and adopt the smallest that still
			// beats minify. Every rendering is lossless - no row is ever dropped.
			if m, ok := buildColumnarModel(items); ok {
				// Candidates compete on ESTIMATED TOKENS (what the consumer pays),
				// with bytes as the tiebreaker - bytes alone can pick a rendering
				// that is smaller on disk but costs more model tokens.
				bestTok := compress.EstimateTokens(string(best))
				if enc, ok := renderColumnarJSON(m); ok {
					if t := compress.EstimateTokens(string(enc)); t < bestTok || (t == bestTok && len(enc) < len(best)) {
						best, bestTok = enc, t
					}
				}
				if enc, ok := renderColumnarCSV(m); ok {
					if t := compress.EstimateTokens(string(enc)); t < bestTok || (t == bestTok && len(enc) < len(best)) {
						best, bestTok = enc, t
					}
				}
			}
			// TODO(textcompress): re-enable lossy row-sampling once dropped rows are
			// recoverable (CCR-style cache + retrieval) and the response surfaces a
			// `sampled`/`dropped` signal. It is DISABLED for now: silently dropping
			// array rows can mislead an agent that needs the full list, so we never
			// drop rows.
			//
			// SmartCrusher parity (see docs/smartcrusher-gap-analysis.md). DONE:
			// union/sparse schema, typed columns + CSV encoding. LOSSLESS gaps still
			// to build: nested-object flattening, stringified-JSON recursion,
			// discriminator bucketing. LOSSY gaps - DEFERRED, need the CCR store, do
			// NOT implement yet:
			//   * opaque-blob offload (base64/html/long-string cell -> <<ccr:…>>)
			//   * statistical row-sampling, on top of CCR.
			//
			// if savingsRatio(len(best), origLen) < losslessMinSavings && len(items) >= 5 {
			// 	if keep := selectItems(items, j.maxItems); len(keep) < len(items) {
			// 		if sampled, err := json.Marshal(keep); err == nil && len(sampled) < len(best) {
			// 			best = sampled
			// 		}
			// 	}
			// }
		}
	}

	if len(best) >= origLen {
		return passthrough, nil
	}
	return compress.Result{Output: string(best), Strategy: j.Name(), InChars: origLen, OutChars: len(best)}, nil
}

// savingsRatio = 1 - out/in (0 when in is 0). Fraction of bytes removed.
func savingsRatio(outLen, inLen int) float64 {
	if inLen == 0 {
		return 0
	}
	return 1 - float64(outLen)/float64(inLen)
}

// columnarModel is the shared union-schema tabular form: built once from an array
// of objects (buildColumnarModel), then rendered by renderColumnarJSON or
// renderColumnarCSV. rows[i][c] holds each cell's raw JSON with a `null` placeholder
// where a key was absent; absent[str(i)] lists the columns that were absent in row
// i, so an ABSENT key and a present-but-`null` key stay distinguishable and every
// rendering is exactly reconstructible. No row is ever dropped.
type columnarModel struct {
	schema []string // union of all keys, ordered by desc frequency then alphabetically
	rows   [][]json.RawMessage
	absent map[string][]int
	// nested maps a flattened parent key -> its (sorted) inner keys. Set by
	// flattenModel; recorded verbatim in "__nested__". DECODER CONTRACT: a decoder
	// MUST rebuild each parent from EXACTLY these inner keys (`parent.<innerKey>`),
	// NOT by prefix-matching every column that starts with `parent.` - a real
	// sibling key like `meta.extra` also starts with `meta.` and must be left as a
	// top-level key. Prefix-matching would silently mis-nest it.
	nested map[string][]string
	// consts holds factored-out constant columns: a column present in EVERY row
	// with the byte-identical raw value is stored once here ("__const__") instead
	// of once per row. DECODER CONTRACT: re-add every consts entry to every row's
	// object BEFORE un-nesting (a flattened dotted column can itself be constant,
	// and the parent rebuild must see it).
	consts map[string]json.RawMessage
}

// flattenMaxInnerKeys bounds nested-object flattening: an inner schema wider than
// this stays nested rather than exploding the column count (SmartCrusher parity).
const flattenMaxInnerKeys = 6

// buildColumnarModel constructs the union-schema model (SmartCrusher parity, gap 1
// in docs/smartcrusher-gap-analysis.md). The schema is the UNION of all keys across
// the array (not a single identical key set), ordered by descending frequency then
// alphabetically - deterministic, common columns first. Values are kept as raw JSON
// so types/nesting/precision survive exactly.
//
// Returns (nil, false) if any element is not an object, an object has a duplicate
// key (json.Unmarshal would silently collapse it - not lossless), the array is all
// empty objects, or the projected N×len(schema) matrix would exceed the input size
// (columnar cannot beat minify then - this also caps memory at O(input) on
// disjoint-key arrays, where the matrix would otherwise be O(N²)).
func buildColumnarModel(items []json.RawMessage) (*columnarModel, bool) {
	objs := make([]map[string]json.RawMessage, len(items))
	freq := make(map[string]int)
	inputBytes := 0
	for i, it := range items {
		inputBytes += len(it)
		keys, ok := objectKeys(it)
		if !ok {
			return nil, false // not an object
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(it, &obj); err != nil {
			return nil, false
		}
		if len(obj) != len(keys) {
			return nil, false // duplicate key collapsed by the map -> not lossless
		}
		objs[i] = obj
		for _, k := range keys {
			freq[k]++
		}
	}
	if len(freq) == 0 {
		return nil, false // every object empty: nothing to hoist
	}
	// Early-abort BEFORE allocating the N×len(schema) row matrix (see doc comment).
	if len(items)*len(freq) > inputBytes {
		return nil, false
	}

	schema := make([]string, 0, len(freq))
	for k := range freq {
		schema = append(schema, k)
	}
	sort.Slice(schema, func(a, b int) bool {
		if fa, fb := freq[schema[a]], freq[schema[b]]; fa != fb {
			return fa > fb
		}
		return schema[a] < schema[b]
	})

	null := json.RawMessage("null")
	rows := make([][]json.RawMessage, len(items))
	absent := make(map[string][]int)
	for i, obj := range objs {
		row := make([]json.RawMessage, len(schema))
		var miss []int
		for c, k := range schema {
			if v, ok := obj[k]; ok {
				row[c] = v
			} else {
				row[c] = null // placeholder; the absent list makes it non-authoritative
				miss = append(miss, c)
			}
		}
		rows[i] = row
		if len(miss) > 0 {
			absent[strconv.Itoa(i)] = miss
		}
	}
	m := &columnarModel{schema: schema, rows: rows, absent: absent}
	flattenModel(m)
	factorConstants(m)
	return m, true
}

// factorConstants moves columns whose value is byte-identical in EVERY row (and
// absent in none) out of the row matrix into m.consts, stored once (SmartCrusher
// gap: they built factor_out_constants and default-disabled it citing "schema
// preservation" - moot for an explicit envelope with a decoder contract; measured
// +55% on a real pod table, docs/compressor-opportunities.md #2). Runs AFTER
// flattenModel so flattened dotted columns (meta.region all "us") factor too.
// Strictly lossless: the decoder re-adds each constant to every row. Requires
// >=2 rows (a single row would trivially make every column "constant").
func factorConstants(m *columnarModel) {
	if len(m.rows) < 2 {
		return
	}
	absentCols := make(map[int]bool)
	for _, cols := range m.absent {
		for _, c := range cols {
			absentCols[c] = true
		}
	}
	keep := make([]bool, len(m.schema))
	var consts map[string]json.RawMessage
	changed := false
	for c := range m.schema {
		keep[c] = true
		if absentCols[c] {
			continue // absent somewhere -> not constant
		}
		first := m.rows[0][c]
		constant := true
		for _, row := range m.rows[1:] {
			if !bytes.Equal(row[c], first) {
				constant = false
				break
			}
		}
		if constant {
			if consts == nil {
				consts = make(map[string]json.RawMessage)
			}
			consts[m.schema[c]] = first
			keep[c] = false
			changed = true
		}
	}
	if !changed {
		return
	}
	// Rebuild schema/rows and remap absent indices to the post-removal layout.
	newIdx := make([]int, len(m.schema)) // old col -> new col (kept cols only)
	newSchema := make([]string, 0, len(m.schema))
	for c, name := range m.schema {
		if keep[c] {
			newIdx[c] = len(newSchema)
			newSchema = append(newSchema, name)
		}
	}
	for r, row := range m.rows {
		newRow := make([]json.RawMessage, 0, len(newSchema))
		for c := range m.schema {
			if keep[c] {
				newRow = append(newRow, row[c])
			}
		}
		m.rows[r] = newRow
	}
	for k, cols := range m.absent {
		remapped := make([]int, len(cols))
		for i, c := range cols {
			remapped[i] = newIdx[c] // constant cols have no absents, so c is kept
		}
		m.absent[k] = remapped
	}
	m.schema = newSchema
	m.consts = consts
}

// flattenModel promotes columns whose every PRESENT cell is a JSON object with the
// same key set into dotted columns (`meta` -> `meta.region`, `meta.tier`), bounded
// by flattenMaxInnerKeys (SmartCrusher parity, gap 4). One level only. A parent is
// flattened only if none of its `parent.innerKey` names collides with an existing
// column, and the mapping is recorded in m.nested so reconstruction is exact - a
// real key that literally contains a dot is never mis-nested. Absent parents expand
// to absent inner cells; present cells' inner values move to the new columns.
//
// Runs AFTER buildColumnarModel's O(input) matrix guard, so it can widen a column
// 1->N (N<=flattenMaxInnerKeys). This stays input-bounded - the inner keys it hoists
// were already object-cell content counted in inputBytes - so it is not a DoS path,
// but the realized matrix can slightly exceed the pre-flatten guard's projection.
func flattenModel(m *columnarModel) {
	absentSet := make([]map[int]bool, len(m.rows))
	for k, cols := range m.absent {
		i, err := strconv.Atoi(k)
		if err != nil || i < 0 || i >= len(m.rows) {
			return // malformed absent map: skip flattening entirely (still lossless)
		}
		set := make(map[int]bool, len(cols))
		for _, c := range cols {
			set[c] = true
		}
		absentSet[i] = set
	}

	existing := make(map[string]bool, len(m.schema))
	for _, name := range m.schema {
		existing[name] = true
	}

	inners := make([][]string, len(m.schema)) // nil = keep column as-is
	nested := make(map[string][]string)
	changed := false
	for c := range m.schema {
		keys, ok := uniformInnerKeys(m, c, absentSet)
		if !ok || len(keys) == 0 || len(keys) > flattenMaxInnerKeys {
			continue
		}
		parent := m.schema[c]
		collision := false
		for _, k := range keys {
			if existing[parent+"."+k] {
				collision = true
				break
			}
		}
		if collision {
			continue
		}
		inners[c] = keys
		nested[parent] = keys
		changed = true
	}
	if !changed {
		return
	}

	newSchema := make([]string, 0, len(m.schema))
	for c, name := range m.schema {
		if inners[c] == nil {
			newSchema = append(newSchema, name)
		} else {
			for _, k := range inners[c] {
				newSchema = append(newSchema, name+"."+k)
			}
		}
	}

	null := json.RawMessage("null")
	newRows := make([][]json.RawMessage, len(m.rows))
	newAbsent := make(map[string][]int)
	for r := range m.rows {
		row := make([]json.RawMessage, 0, len(newSchema))
		var miss []int
		for c := range m.schema {
			if inners[c] == nil {
				if absentSet[r][c] {
					miss = append(miss, len(row))
				}
				row = append(row, m.rows[r][c])
				continue
			}
			if absentSet[r][c] {
				for range inners[c] {
					miss = append(miss, len(row))
					row = append(row, null)
				}
				continue
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(m.rows[r][c], &obj); err != nil {
				return // uniformInnerKeys guaranteed an object; be safe and abort
			}
			for _, k := range inners[c] {
				if v, ok := obj[k]; ok {
					row = append(row, v)
				} else {
					miss = append(miss, len(row))
					row = append(row, null)
				}
			}
		}
		newRows[r] = row
		if len(miss) > 0 {
			newAbsent[strconv.Itoa(r)] = miss
		}
	}
	m.schema = newSchema
	m.rows = newRows
	m.absent = newAbsent
	m.nested = nested
}

// uniformInnerKeys returns the sorted inner key set if every PRESENT cell at column
// c is a JSON object with the identical key set (and at least one present object
// exists); nil,false otherwise. Already-dotted columns are skipped (one-level only).
func uniformInnerKeys(m *columnarModel, c int, absentSet []map[int]bool) ([]string, bool) {
	if strings.Contains(m.schema[c], ".") {
		return nil, false
	}
	var canonical []string
	saw := false
	for r := range m.rows {
		if absentSet[r] != nil && absentSet[r][c] {
			continue
		}
		keys, ok := objectKeys(m.rows[r][c])
		if !ok {
			return nil, false // a present cell is not an object -> don't flatten
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(m.rows[r][c], &obj); err != nil || len(obj) != len(keys) {
			return nil, false // duplicate inner key -> not lossless to flatten
		}
		sort.Strings(keys)
		if !saw {
			canonical, saw = keys, true
		} else if !equalStrings(canonical, keys) {
			return nil, false
		}
	}
	if !saw {
		return nil, false
	}
	return canonical, true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// renderColumnarJSON emits {"__schema__":[keys],"__rows__":[[vals],...],"__absent__":…}.
// Reconstruction: obj_i = { schema[c]: rows[i][c] for every c NOT in absent[str(i)] }.
// "__absent__" is omitted when no row is sparse.
func renderColumnarJSON(m *columnarModel) ([]byte, bool) {
	out, err := json.Marshal(struct {
		Schema []string                   `json:"__schema__"`
		Rows   [][]json.RawMessage        `json:"__rows__"`
		Absent map[string][]int           `json:"__absent__,omitempty"`
		Nested map[string][]string        `json:"__nested__,omitempty"`
		Consts map[string]json.RawMessage `json:"__const__,omitempty"`
	}{m.schema, m.rows, m.absent, m.nested, m.consts})
	if err != nil {
		return nil, false
	}
	return out, true
}

// renderColumnarCSV emits {"__schema__":[keys],"__types__":[types],"__csv__":"…",
// "__absent__":…} (SmartCrusher parity, gap 2-3). Each column gets an inferred type;
// a column is a primitive type (int/float/bool/string) ONLY when every present cell
// is that same non-null primitive - any null or type mix makes it "json", whose
// cells render as raw JSON. That rule sidesteps the empty-cell-vs-null ambiguity: a
// "string" column never contains null, so an empty CSV field is unambiguously "".
// Rows are written with encoding/csv (RFC-4180 escaping of commas/quotes/newlines),
// so string cells shed their JSON quotes losslessly. Reconstruction re-quotes
// "string" cells and takes every other type verbatim. Absent cells render empty and
// are skipped on reconstruction via "__absent__".
//
// Requires >= 2 columns: a single-column row could render as a blank CSV line, which
// encoding/csv's reader skips - and single-column tables gain little from CSV anyway.
func renderColumnarCSV(m *columnarModel) ([]byte, bool) {
	nCols := len(m.schema)
	if nCols < 2 {
		return nil, false
	}
	absentByRow := make([]map[int]bool, len(m.rows))
	for k, cols := range m.absent {
		i, err := strconv.Atoi(k)
		if err != nil || i < 0 || i >= len(m.rows) {
			return nil, false
		}
		set := make(map[int]bool, len(cols))
		for _, c := range cols {
			set[c] = true
		}
		absentByRow[i] = set
	}

	// Infer per-column type, skipping absent + null cells.
	types := make([]string, nCols)
	for c := 0; c < nCols; c++ {
		t := ""
		forceJSON := false
		for i, row := range m.rows {
			if absentByRow[i][c] {
				continue
			}
			jt := jsonScalarType(row[c])
			if jt == "null" || jt == "json" {
				forceJSON = true // null or object/array -> render this column raw
				break
			}
			if jt == "string" {
				// encoding/csv's reader normalizes a CRLF byte-pair to LF, so a real
				// carriage return written into a CSV field would be lost. Render any
				// column holding such a string as raw JSON instead - a JSON token
				// carries \r as the escape "\r", which csv never touches.
				var sv string
				if err := json.Unmarshal(row[c], &sv); err != nil {
					return nil, false
				}
				if strings.IndexByte(sv, '\r') >= 0 {
					forceJSON = true
					break
				}
			}
			if t == "" {
				t = jt
			} else if t != jt {
				forceJSON = true
				break
			}
		}
		if forceJSON || t == "" {
			types[c] = "json"
		} else {
			types[c] = t
		}
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	field := make([]string, nCols)
	for i, row := range m.rows {
		for c := 0; c < nCols; c++ {
			switch {
			case absentByRow[i][c]:
				field[c] = "" // absent -> empty; skipped on reconstruct via __absent__
			case types[c] == "string":
				var str string
				if err := json.Unmarshal(row[c], &str); err != nil {
					return nil, false
				}
				field[c] = str // shed the JSON quotes; csv re-escapes if needed
			default:
				field[c] = string(row[c]) // int/float/bool/json: raw JSON token
			}
		}
		if err := w.Write(field); err != nil {
			return nil, false
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, false
	}

	out, err := json.Marshal(struct {
		Schema []string                   `json:"__schema__"`
		Types  []string                   `json:"__types__"`
		CSV    string                     `json:"__csv__"`
		Absent map[string][]int           `json:"__absent__,omitempty"`
		Nested map[string][]string        `json:"__nested__,omitempty"`
		Consts map[string]json.RawMessage `json:"__const__,omitempty"`
	}{m.schema, types, buf.String(), m.absent, m.nested, m.consts})
	if err != nil {
		return nil, false
	}
	return out, true
}

// jsonScalarType returns the coarse JSON type of a raw value: "string", "bool",
// "null", "int"/"float" for numbers, or "json" for objects/arrays (which a CSV
// column renders verbatim). int/float are distinguished only for the schema tag;
// both render as their raw token.
func jsonScalarType(raw json.RawMessage) string {
	s := bytes.TrimSpace(raw)
	if len(s) == 0 {
		return "json"
	}
	switch s[0] {
	case '"':
		return "string"
	case '{', '[':
		return "json"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	default:
		if bytes.ContainsAny(s, ".eE") {
			return "float"
		}
		return "int"
	}
}

// objectKeys returns the keys of a JSON object in insertion order, or ok=false if
// raw is not an object. It scans the token stream so key order is preserved (a
// map would lose it), skipping each value (scalar or nested) via balanced delims.
func objectKeys(raw json.RawMessage) ([]string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	t, err := dec.Token()
	if err != nil {
		return nil, false
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, false
	}
	var keys []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := kt.(string)
		if !ok {
			return nil, false
		}
		keys = append(keys, key)
		if err := skipValue(dec); err != nil {
			return nil, false
		}
	}
	return keys, true
}

// skipValue consumes exactly one JSON value from dec: a scalar is one token, an
// object/array is consumed to its matching close delim.
func skipValue(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := t.(json.Delim)
	if !ok || (d != '{' && d != '[') {
		return nil // scalar
	}
	for depth := 1; depth > 0; {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

func selectItems(items []json.RawMessage, maxItems int) []json.RawMessage {
	n := len(items)
	head := ceilPct(n, 30)
	tail := ceilPct(n, 15)

	sel := make([]bool, n)
	for i := 0; i < head && i < n; i++ {
		sel[i] = true
	}
	for i := n - tail; i < n; i++ {
		if i >= 0 {
			sel[i] = true
		}
	}
	// One representative per distinct value not already covered (dedup-identical).
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		if sel[i] {
			seen[canonical(items[i])] = true
		}
	}
	for i := 0; i < n; i++ {
		if sel[i] {
			continue
		}
		k := canonical(items[i])
		if !seen[k] {
			seen[k] = true
			sel[i] = true
		}
	}

	out := make([]json.RawMessage, 0, n)
	for i := 0; i < n; i++ {
		if sel[i] {
			out = append(out, items[i])
		}
	}
	// Cap: keep a head bias and a tail tail, drop the middle.
	if len(out) > maxItems {
		headKeep := maxItems * 2 / 3
		tailKeep := maxItems - headKeep
		trimmed := make([]json.RawMessage, 0, maxItems)
		trimmed = append(trimmed, out[:headKeep]...)
		trimmed = append(trimmed, out[len(out)-tailKeep:]...)
		out = trimmed
	}
	return out
}

// ceilPct = ceil(n * pct / 100).
func ceilPct(n, pct int) int { return (n*pct + 99) / 100 }

// canonical compacts JSON whitespace so dedup compares structure, not formatting.
func canonical(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}
