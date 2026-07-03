package jit

import "sync"

// The shared-fragment cache lets one session's render of a region be
// reused by every other session that renders the same key. A plain
// [Memoiser] caches within a single session; this cache is process-
// global, so when a shared header or a broadcast leaderboard changes,
// the closure runs once for the whole process instead of once per
// connected session. Nodes opt in via [node.Shared].
//
// Memory is bounded with a two-generation scheme rather than per-entry
// LRU bookkeeping: writes fill the current map, and when it reaches the
// cap the current map is retired to prev (evicting the old prev) and a
// fresh current map starts. Lookups check both, so an entry survives at
// least one full generation. Total residency is at most 2×cap entries.
// Shared keys are meant to be versioned and long-lived ("nav:v3"), so
// this keeps hot entries resident while discarding stale versions.

// defaultSharedCacheSize is the per-generation entry cap. The cache
// holds at most twice this many rendered fragments across both
// generations. Override with [SetSharedCacheSize].
const defaultSharedCacheSize = 2048

type sharedStore struct {
	mu   sync.RWMutex
	cap  int
	cur  map[string][]byte
	prev map[string][]byte
}

func newSharedStore(capacity int) *sharedStore {
	return &sharedStore{
		cap:  capacity,
		cur:  make(map[string][]byte),
		prev: make(map[string][]byte),
	}
}

// get returns the cached bytes for key, checking the current generation
// first and then the previous one. The returned slice is never mutated
// in place - put always stores a fresh copy - so callers may read it
// without holding the lock, even if a concurrent put evicts the entry.
func (s *sharedStore) get(key string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.cur[key]; ok {
		return b, true
	}
	if b, ok := s.prev[key]; ok {
		return b, true
	}
	return nil, false
}

// put stores a copy of data under key. The copy is deliberate: the
// caller's buffer is pooled and will be reused, whereas cache entries
// must stay valid and immutable for concurrent readers.
func (s *sharedStore) put(key string, data []byte) {
	b := make([]byte, len(data))
	copy(b, data)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cur) >= s.cap {
		s.prev = s.cur
		s.cur = make(map[string][]byte, s.cap)
	}
	s.cur[key] = b
}

// reset clears both generations and applies a new per-generation cap
// when n is positive.
func (s *sharedStore) reset(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > 0 {
		s.cap = n
	}
	s.cur = make(map[string][]byte)
	s.prev = make(map[string][]byte)
}

// len reports the number of distinct keys currently resident across
// both generations. A key present in both counts once.
func (s *sharedStore) len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.cur)
	for k := range s.prev {
		if _, ok := s.cur[k]; !ok {
			n++
		}
	}
	return n
}

// sharedCache is the process-global store consulted by every Memoiser
// for [node.Shared] regions.
var sharedCache = newSharedStore(defaultSharedCacheSize)

// SetSharedCacheSize sets the per-generation entry cap of the process-
// global shared-fragment cache and clears it. The cache holds at most
// twice this many rendered fragments. Call once at startup, before
// serving traffic. A non-positive n is ignored.
func SetSharedCacheSize(n int) {
	if n <= 0 {
		return
	}
	sharedCache.reset(n)
}

// ResetSharedCache empties the process-global shared-fragment cache
// without changing its size. Intended for tests that need a clean
// starting state between cases.
func ResetSharedCache() {
	sharedCache.reset(0)
}

// SharedCacheLen returns the number of distinct fragments currently
// held in the process-global shared-fragment cache.
func SharedCacheLen() int {
	return sharedCache.len()
}
