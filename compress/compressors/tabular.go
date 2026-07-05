package compressors

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/firstops-dev/whittle/compress"
)

var (
	// tabSepRe matches a pure ascii/markdown table separator row (---+---, |---|,
	// :---:) - it carries no data, only draws a line.
	tabSepRe = regexp.MustCompile(`^[\s|:+-]*[-+][\s|:+-]*$`)
	// tabPipeRe is the cosmetic padding around a column pipe.
	tabPipeRe = regexp.MustCompile(`\s*\|\s*`)
	// tabSpaceRe is a run of 2+ spaces - the alignment gap between fixed-width
	// columns. Collapsed to a single tab so the column boundary survives but the
	// padding does not.
	tabSpaceRe = regexp.MustCompile(` {2,}`)
)

// TabularCompressor compresses column-aligned and delimited tables (kubectl/docker/
// ls -l, psql, csv, markdown). Mirrors JSONCrusher's two layers: a LOSSLESS baseline
// that strips cosmetic alignment padding (the tabular analog of JSON minify), then
// optional row sampling for long tables that keeps the header plus a representative
// head/tail subset and marks the omitted middle. Headroom has a fixed-width parser
// but never wires a compressor to it; this is that missing piece.
type TabularCompressor struct{ maxRows int }

func NewTabularCompressor() TabularCompressor { return TabularCompressor{maxRows: 40} }

func (TabularCompressor) Name() string { return "tabular_crusher" }

func (TabularCompressor) Handles(ct compress.ContentType) bool { return ct == compress.TypeTabular }

func (t TabularCompressor) Compress(_ context.Context, in compress.Input) (compress.Result, error) {
	passthrough := compress.Result{Output: in.Content, Strategy: t.Name(), InChars: len(in.Content), OutChars: len(in.Content)}

	rows := make([]string, 0, 32)
	for _, ln := range strings.Split(in.Content, "\n") {
		if tabSepRe.MatchString(ln) && strings.ContainsAny(ln, "-+") {
			continue // drop the cosmetic ----+---- rule
		}
		rows = append(rows, normalizeRow(ln))
	}
	if len(rows) == 0 {
		return passthrough, nil
	}

	if len(rows) > t.maxRows { // long table: keep header + representative subset
		rows = sampleRows(rows, t.maxRows)
	}

	out := strings.Join(rows, "\n")
	if out == "" || len(out) >= len(in.Content) { // no win / total loss: passthrough
		return passthrough, nil
	}
	return compress.Result{Output: out, Strategy: t.Name(), InChars: len(in.Content), OutChars: len(out)}, nil
}

// normalizeRow strips cosmetic alignment padding losslessly. Pipe tables collapse
// the padding around each '|' to a bare '|'; space-aligned tables collapse each
// 2+-space column gap to a single tab so boundaries survive but padding does not.
func normalizeRow(ln string) string {
	s := strings.TrimSpace(ln)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "|") {
		s = tabPipeRe.ReplaceAllString(s, "|")
		return strings.Trim(s, "|")
	}
	return tabSpaceRe.ReplaceAllString(s, "\t")
}

// sampleRows keeps the header (row 0), a head and tail slice of the body, and a
// marker for the omitted middle. Lossy but structure-preserving - for a 200-row
// kubectl dump the agent gets the schema plus a representative window.
func sampleRows(rows []string, max int) []string {
	if len(rows) <= max {
		return rows
	}
	body := rows[1:]
	head := max * 3 / 5
	tail := max - head - 1
	if tail < 1 {
		tail = 1
	}
	if head+tail >= len(body) {
		return rows
	}
	out := make([]string, 0, max+1)
	out = append(out, rows[0])
	out = append(out, body[:head]...)
	out = append(out, "... ["+strconv.Itoa(len(body)-head-tail)+" rows omitted]")
	out = append(out, body[len(body)-tail:]...)
	return out
}
