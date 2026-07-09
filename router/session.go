package router

import (
	"sync"
	"time"
)

// SessionStore holds the current tier per Claude Code session id, for
// stickiness. The engine reads Current and writes Set. Keyed by the
// X-Claude-Code-Session-Id header (GATE-2 confirmed it is stable per session);
// a "" id means "no session" and the engine skips stickiness entirely.
//
// The interface is deliberately tiny so the engine stays testable with a fake.
type SessionStore interface {
	Current(id string) (tier string, ok bool)
	Set(id, tier string)
}

const (
	defaultSessionTTL = 30 * time.Minute // idle sessions expire (a daemon runs for days)
	defaultSessionMax = 10_000           // hard cap; oldest evicted past this
)

// MemSessionStore is a concurrency-safe in-memory store with idle-TTL expiry and
// a max-entries LRU cap, so the map cannot grow unbounded on a long-lived
// daemon. A nil *MemSessionStore is a valid store that never remembers anything
// (the "stickiness off" default).
type MemSessionStore struct {
	mu  sync.Mutex
	m   map[string]sessEntry
	ttl time.Duration
	max int
	now func() time.Time // injectable for tests
}

type sessEntry struct {
	tier string
	seen time.Time
}

func NewMemSessionStore() *MemSessionStore {
	return &MemSessionStore{
		m:   map[string]sessEntry{},
		ttl: defaultSessionTTL,
		max: defaultSessionMax,
		now: time.Now,
	}
}

func (s *MemSessionStore) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *MemSessionStore) Current(id string) (string, bool) {
	if s == nil || id == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[id]
	if !ok {
		return "", false
	}
	if s.clock().Sub(e.seen) > s.ttl {
		delete(s.m, id) // expired
		return "", false
	}
	e.seen = s.clock() // touch (LRU recency)
	s.m[id] = e
	return e.tier, true
}

func (s *MemSessionStore) Set(id, tier string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]sessEntry{}
	}
	now := s.clock()
	if _, exists := s.m[id]; !exists && len(s.m) >= s.max {
		s.evictLocked(now)
	}
	s.m[id] = sessEntry{tier: tier, seen: now}
}

// evictLocked drops expired entries first; if still at capacity, drops the
// single oldest (LRU). Called under lock, only on an at-capacity insert, so the
// O(n) scan is rare on a single-user daemon.
func (s *MemSessionStore) evictLocked(now time.Time) {
	var oldestID string
	var oldest time.Time
	for id, e := range s.m {
		if now.Sub(e.seen) > s.ttl {
			delete(s.m, id)
			continue
		}
		if oldestID == "" || e.seen.Before(oldest) {
			oldestID, oldest = id, e.seen
		}
	}
	if len(s.m) >= s.max && oldestID != "" {
		delete(s.m, oldestID)
	}
}
