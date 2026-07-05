package compress

// EstimateTokens is a fast, dependency-free token-count estimator calibrated
// against tiktoken o200k_base (docs/compressor-opportunities.md #3). The consumer
// of compressed output pays TOKENS, not bytes, and bytes diverge from tokens by
// up to ~4x on whitespace-heavy content - so encoding choices and accept gates
// use this, not len().
//
// Single pass; integer ops only. Calibrated by grid-search on 10 content classes
// (prose, minified JSON, CSV, Go code, logs, padded tables, markdown, unicode,
// numbers, YAML) against real tiktoken: MAE 8.2%, worst class +16.3% (go code),
// vs len/4's systematic -27..-48% on structured content. The bias is a slight
// consistent OVERestimate, which is the safe direction for an accept gate, and
// cancels when comparing candidate encodings of the same content.
//
// Model: ASCII letter run of length L = 1 + (L-1)/8 tokens; digit run = ceil(L/3);
// mixed punctuation/symbol run = ceil(L/4) (BPE merges glue like "," ":" "},{");
// space/tab runs of >1 = 1 + (L-2)/6 (single spaces merge into the next word);
// newline runs = 1; every non-ASCII rune = 1.
func EstimateTokens(s string) int {
	toks := 0
	n := len(s)
	i := 0
	for i < n {
		c := s[i]
		switch {
		case c >= 0x80: // non-ASCII: count runes, 1 token each
			j := i
			runes := 0
			for j < n {
				b := s[j]
				if b < 0x80 {
					break
				}
				if b >= 0xC0 { // rune start
					runes++
				}
				j++
			}
			if runes == 0 {
				runes = 1 // defensive: stray continuation bytes
			}
			toks += runes
			i = j
		case isASCIILetter(c):
			j := i
			for j < n && isASCIILetter(s[j]) {
				j++
			}
			toks += 1 + (j-i-1)/8
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < n && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			toks += (j - i + 2) / 3
			i = j
		case c == ' ' || c == '\t':
			j := i
			for j < n && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j-i > 1 {
				toks += 1 + (j-i-2)/6
			}
			i = j
		case c == '\n':
			j := i
			for j < n && s[j] == '\n' {
				j++
			}
			toks++
			i = j
		default: // ASCII punctuation/symbol run (mixed chars merge in BPE)
			j := i
			for j < n {
				b := s[j]
				if b >= 0x80 || isASCIILetter(b) || (b >= '0' && b <= '9') || b == ' ' || b == '\t' || b == '\n' {
					break
				}
				j++
			}
			toks += (j - i + 3) / 4
			i = j
		}
	}
	return toks
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
