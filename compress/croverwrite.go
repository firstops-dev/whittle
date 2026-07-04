package compress

import "strings"

// CRSegmentsLookLikeRewrites reports whether a CR-chain's segments are frames
// re-rendering the same content (terminal overwrite: collapse-safe) rather than
// unrelated records (`\r`-delimited data: collapse would be total loss). Frames
// share a long common prefix ("Downloading 4%" / "Downloading 5%") or suffix
// ("⠋ building" / "⠙ building"); data records share neither. Single source of
// truth for both the router's lone-CR terminal signal and the ANSIStrip
// CR-overwrite collapse (the StripANSI pattern).
//
// Requires >=2/3 of adjacent non-empty pairs to be similar; <2 non-empty
// segments trivially qualify (nothing to collapse).
func CRSegmentsLookLikeRewrites(segs []string) bool {
	var frames []string
	for _, s := range segs {
		if s != "" {
			frames = append(frames, s)
		}
	}
	if len(frames) < 2 {
		return true
	}
	similar := 0
	for i := 1; i < len(frames); i++ {
		if crFramesSimilar(frames[i-1], frames[i]) {
			similar++
		}
	}
	return similar*3 >= (len(frames)-1)*2
}

// crContentLooksLikeRewrites samples the CR-bearing lines of a whole body (cap
// bounds the scan) and reports whether their chains carry the rewrite signature.
func crContentLooksLikeRewrites(content string, capFrames int) bool {
	seen := 0
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "\r") {
			continue
		}
		segs := strings.Split(line, "\r")
		if len(segs) > capFrames-seen+1 {
			segs = segs[:capFrames-seen+1]
		}
		if !CRSegmentsLookLikeRewrites(segs) {
			return false
		}
		seen += len(segs)
		if seen >= capFrames {
			break
		}
	}
	return true
}

func crFramesSimilar(a, b string) bool {
	short := len(a)
	if len(b) < short {
		short = len(b)
	}
	if short == 0 {
		return false
	}
	p := 0
	for p < short && a[p] == b[p] {
		p++
	}
	sfx := 0
	for sfx < short && a[len(a)-1-sfx] == b[len(b)-1-sfx] {
		sfx++
	}
	affix := p
	if sfx > affix {
		affix = sfx
	}
	// Similar if the shared prefix or suffix covers >=30% of the shorter frame,
	// or is >=8 bytes (a long fixed label around a small changing region).
	return affix >= 8 || affix*10 >= short*3
}
