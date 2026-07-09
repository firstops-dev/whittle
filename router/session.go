package router

import "sync"

// SessionStore holds the current tier per Claude Code session id, for
// stickiness. The engine reads Current and writes Set. Keyed by the
// X-Claude-Code-Session-Id header (GATE-2 confirmed it is stable per session);
// a "" id means "no session" and the engine skips stickiness entirely.
//
// The interface is deliberately tiny so the engine stays testable with a fake.
// TTL/LRU eviction (a laptop daemon runs for days) is added when the proxy
// wires this in (later milestone); MemSessionStore below is the unbounded
// baseline used by the core and its tests.
type SessionStore interface {
	Current(id string) (tier string, ok bool)
	Set(id, tier string)
}

// MemSessionStore is a minimal concurrency-safe in-memory store. No eviction yet
// (see note above). A nil *MemSessionStore is a valid empty store that never
// remembers anything — handy as the "stickiness off" default.
type MemSessionStore struct {
	mu sync.Mutex
	m  map[string]string
}

func NewMemSessionStore() *MemSessionStore { return &MemSessionStore{m: map[string]string{}} }

func (s *MemSessionStore) Current(id string) (string, bool) {
	if s == nil || id == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[id]
	return t, ok
}

func (s *MemSessionStore) Set(id, tier string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[id] = tier
}
