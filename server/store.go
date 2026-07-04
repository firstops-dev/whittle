package server

// Store — the disk-backed, content-addressed original-content cache behind
// whittle_get. Originals of REDUCED outputs (lossy/marked strategies only) are
// kept so the model can retrieve the exact bytes when strictly required.
// Content-addressed (SHA-256) for dedup; exposed by small integer alias (2
// tokens in a hint vs 8 for hex). Bounded: TTL + byte cap, oldest evicted.
// Misses are honest: "expired" — the agent can re-run the tool.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Store struct {
	mu       sync.Mutex
	dir      string
	maxBytes int64
	ttl      time.Duration
	next     int64            // persistent alias counter (never reissued)
	ids      map[int64]string // alias -> content hash
}

func OpenStore(dir string, maxBytes int64, ttl time.Duration) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, maxBytes: maxBytes, ttl: ttl, ids: map[int64]string{}, next: 1}
	if b, err := os.ReadFile(filepath.Join(dir, "index.json")); err == nil {
		var idx struct {
			Next int64            `json:"next"`
			IDs  map[int64]string `json:"ids"`
		}
		if json.Unmarshal(b, &idx) == nil && idx.Next > 0 {
			s.next, s.ids = idx.Next, idx.IDs
			if s.ids == nil {
				s.ids = map[int64]string{}
			}
		} else {
			// Index unreadable but present: aliases may be outstanding in old
			// transcripts. NEVER reissue — jump the counter far past anything a
			// small sequence could have reached (review B3).
			s.next = time.Now().Unix()
		}
	}
	return s, nil
}

func (s *Store) persist() {
	b, _ := json.Marshal(struct {
		Next int64            `json:"next"`
		IDs  map[int64]string `json:"ids"`
	}{s.next, s.ids})
	// Atomic: temp + rename (review B3 — a torn index.json reset the alias
	// counter, and a stale hint then retrieved the WRONG content).
	tmp := filepath.Join(s.dir, "index.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, filepath.Join(s.dir, "index.json"))
	}
}

// Put stores content and returns its alias id (deduped by content hash).
func (s *Store) Put(content string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(h[:8])
	for id, existing := range s.ids {
		if existing == hash {
			return id // dedup: same content keeps its alias
		}
	}
	_ = os.WriteFile(filepath.Join(s.dir, hash), []byte(content), 0o644)
	id := s.next
	s.next++
	s.ids[id] = hash
	s.evict()
	s.persist()
	return id
}

// Get returns the original content for an alias, or ok=false (expired/unknown).
func (s *Store) Get(id int64) (string, bool) {
	s.mu.Lock()
	hash, ok := s.ids[id]
	s.mu.Unlock()
	if !ok {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(s.dir, hash))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// evict drops TTL-expired entries, then oldest-first until under the byte cap.
// Caller holds the lock.
func (s *Store) evict() {
	type f struct {
		hash string
		mod  time.Time
		size int64
	}
	var files []f
	var total int64
	for _, hash := range s.ids {
		if st, err := os.Stat(filepath.Join(s.dir, hash)); err == nil {
			files = append(files, f{hash, st.ModTime(), st.Size()})
			total += st.Size()
		}
	}
	cutoff := time.Now().Add(-s.ttl)
	sort.Slice(files, func(a, b int) bool { return files[a].mod.Before(files[b].mod) })
	drop := map[string]bool{}
	for _, fl := range files {
		if fl.mod.Before(cutoff) || total > s.maxBytes {
			drop[fl.hash] = true
			total -= fl.size
		}
	}
	for id, hash := range s.ids {
		if drop[hash] {
			delete(s.ids, id)
			_ = os.Remove(filepath.Join(s.dir, hash))
		}
	}
}
