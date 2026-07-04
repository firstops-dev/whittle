package server

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Review B1: the emitted replacement (hint included) must NEVER be larger than
// the original on either axis; marginal lossy wins keep the win hint-less.
func TestFinalizeReplacement_PostHintInvariant(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir, 1<<20, time.Hour)
	orig := strings.Repeat("2026 INFO tick handler ok\n", 12) + "ERROR boot failed\n"

	// marginal win: compressed output only slightly smaller than original
	marginal := orig[:len(orig)-40]
	final, id := finalizeReplacement(orig, marginal, "ansi_strip+log_compressor", store)
	if len(final) >= len(orig) {
		t.Fatalf("emitted larger than original: %d >= %d", len(final), len(orig))
	}
	if final != "" && strings.Contains(final, "whittle_get") && id == 0 {
		t.Fatal("hint emitted without a stored alias")
	}
	// big win: hint should emit, alias spent, still strictly smaller
	big := "ERROR boot failed\n"
	final2, id2 := finalizeReplacement(orig, big, "ansi_strip+log_compressor", store)
	if !strings.Contains(final2, "whittle_get(") || id2 == 0 {
		t.Fatalf("clear win must carry a retrieval hint: %q", final2)
	}
	if len(final2) >= len(orig) {
		t.Fatal("hinted output larger than original")
	}
	// lossless strategy: never a hint, never an alias
	final3, id3 := finalizeReplacement(orig, big, "ansi_strip+json_crusher", store)
	if strings.Contains(final3, "whittle_get") || id3 != 0 {
		t.Fatal("lossless strategy must not hint or store")
	}
}

// Review B3: corrupt index must never reissue aliases.
func TestStore_CorruptIndexNeverReissues(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir, 1<<20, time.Hour)
	id1 := s.Put("alpha")
	// tear the index
	if err := writeFileHalf(dir + "/index.json"); err != nil {
		t.Fatal(err)
	}
	s2, _ := OpenStore(dir, 1<<20, time.Hour)
	id2 := s2.Put("gamma")
	if id2 <= id1 {
		t.Fatalf("alias reissued after corruption: %d <= %d", id2, id1)
	}
	if got, ok := s2.Get(id1); ok && got != "alpha" {
		t.Fatalf("stale alias returned WRONG content: %q", got)
	}
}

func writeFileHalf(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b[:len(b)/2], 0o644)
}
