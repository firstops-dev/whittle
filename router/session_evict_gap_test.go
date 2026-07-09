package router

import (
	"testing"
	"time"
)

// TestMemSessionStore_TouchOnAccessPreventsExpiry verifies the idle timer resets
// on every access.
//
// WHY: the TTL is an IDLE timeout ("idle sessions expire"), not an absolute one.
// A session actively in use must never be evicted mid-conversation. Current()
// touches seen on a hit; an entry accessed 20min into a 30min TTL must still be
// live 20min later (40min absolute) because the clock restarts from the touch.
func TestMemSessionStore_TouchOnAccessPreventsExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newClockedStore(30*time.Minute, 100, &now)
	s.Set("sess", "smart")

	now = now.Add(20 * time.Minute) // still within TTL → hit, and touch seen
	if _, ok := s.Current("sess"); !ok {
		t.Fatal("entry should be live 20min in (within the 30min TTL)")
	}

	now = now.Add(20 * time.Minute) // 40min absolute, but only 20min since the touch
	if tier, ok := s.Current("sess"); !ok || tier != "smart" {
		t.Fatalf("touch-on-access must reset the idle timer; entry wrongly expired (tier=%q ok=%v)", tier, ok)
	}
}

// TestMemSessionStore_EvictsLeastRecentlyUsedNotInsertionOrder verifies eviction
// is by recency (LRU), not by insertion order.
//
// WHY: the cap-overflow eviction must drop the least-recently-USED entry. If it
// dropped by insertion order instead, an actively-used oldest-inserted session
// would be evicted out from under a live conversation. Here 'a' is inserted first
// but touched last, so 'b' is the true LRU victim and 'a' must survive.
func TestMemSessionStore_EvictsLeastRecentlyUsedNotInsertionOrder(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newClockedStore(time.Hour, 3, &now) // cap = 3, TTL far away

	s.Set("a", "fast")
	now = now.Add(time.Second)
	s.Set("b", "fast")
	now = now.Add(time.Second)
	s.Set("c", "fast")
	now = now.Add(time.Second)

	// Touch 'a' → it becomes the most-recently-used; 'b' is now the oldest.
	if _, ok := s.Current("a"); !ok {
		t.Fatal("'a' should be present before the touch")
	}
	now = now.Add(time.Second)

	s.Set("d", "fast") // over cap → evict the LRU entry

	if _, ok := s.Current("b"); ok {
		t.Error("LRU eviction should have dropped 'b' (least recently used)")
	}
	if _, ok := s.Current("a"); !ok {
		t.Error("'a' was touched most recently and must survive (insertion-order eviction would wrongly drop it)")
	}
	if _, ok := s.Current("d"); !ok {
		t.Error("newly inserted 'd' should be present")
	}
}

// TestMemSessionStore_EmptyIdNeverStored verifies the "" (no-session) id is never
// persisted.
//
// WHY: a "" id means "no session" (R14 session-less request); storing it would
// collapse every session-less request onto one shared sticky slot. Set("") must
// be a no-op and Current("") must always miss.
func TestMemSessionStore_EmptyIdNeverStored(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newClockedStore(time.Hour, 100, &now)

	s.Set("", "smart")
	if tier, ok := s.Current(""); ok {
		t.Errorf(`"" id must never be retrievable, got tier=%q`, tier)
	}
	s.mu.Lock()
	n := len(s.m)
	s.mu.Unlock()
	if n != 0 {
		t.Errorf(`"" id must not be stored; map holds %d entries`, n)
	}
}

// TestMemSessionStore_NilReceiverIsInert verifies a nil *MemSessionStore is a
// valid "stickiness off" store that never panics.
//
// WHY: NewProxy accepts a nil session store to disable stickiness; the engine
// calls Current/Set on it unconditionally, so both must be safe on a nil receiver.
func TestMemSessionStore_NilReceiverIsInert(t *testing.T) {
	var s *MemSessionStore
	s.Set("x", "fast") // must not panic
	if _, ok := s.Current("x"); ok {
		t.Error("a nil store must never remember anything")
	}
}
