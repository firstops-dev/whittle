package server

import (
	"strings"
	"testing"
	"time"
)

func TestStoreRoundtripDedupEvict(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	id := s.Put("original bytes")
	if got, ok := s.Get(id); !ok || got != "original bytes" {
		t.Fatalf("roundtrip failed: %q %v", got, ok)
	}
	if s.Put("original bytes") != id {
		t.Fatal("identical content must dedup to the same alias")
	}
	if _, ok := s.Get(99999); ok {
		t.Fatal("unknown alias must miss")
	}
	// counter survives reopen; ids never reissued
	s2, _ := OpenStore(dir, 1<<20, time.Hour)
	id2 := s2.Put("different content")
	if id2 <= id {
		t.Fatalf("alias reissued after restart: %d <= %d", id2, id)
	}
	// byte-cap eviction: oldest goes, store keeps working
	s3, _ := OpenStore(t.TempDir(), 64, time.Hour)
	a := s3.Put(strings.Repeat("a", 60))
	time.Sleep(1100 * time.Millisecond) // mtime granularity
	b := s3.Put(strings.Repeat("b", 60))
	if _, ok := s3.Get(a); ok {
		t.Fatal("oldest entry should be evicted past byte cap")
	}
	if got, ok := s3.Get(b); !ok || got[0] != 'b' {
		t.Fatal("newest entry must survive eviction")
	}
}
