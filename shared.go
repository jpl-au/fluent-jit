package jit

import "sync"

// The shared-fragment cache lets one session's render of a region be
// reused by every other session that renders the same key. A plain
// [Memoiser] caches within a single session; this cache is process-
// global, so when a shared header or a broadcast leaderboard changes,
// the closure runs once for the whole process instead of once per
// connected session. Nodes opt in via [Shared]. Entries contain encoded
// HTML and nested region metadata, so cache hits retain targeted child
// updates. The byte budget includes both the HTML and this metadata.
//
// Memory is bounded with a two-generation scheme rather than per-entry
// LRU bookkeeping: writes fill the current map, and when it reaches
// the entry cap or the byte budget the current map is retired to prev
// (evicting the old prev) and a fresh current map starts. Lookups
// check both, so an entry survives at least one full generation.
// Total residency is at most 2×cap entries and around 2×budget bytes.
// Shared keys are meant to be versioned and long-lived ("nav:v3"), so
// this keeps hot entries resident while discarding stale versions.

// defaultSharedCacheSize is the per-generation entry cap. The cache
// holds at most twice this many rendered fragments across both
// generations. Override with [SetSharedCacheSize].
const defaultSharedCacheSize = 2048

// defaultSharedCacheBudget is the per-generation byte budget. The
// entry cap alone would let 4,096 half-megabyte fragments legally
// hold ~2GB; the budget bounds the bytes as well, so worst-case
// residency is about twice this figure. 32MB per generation holds
// thousands of typical shared fragments (headers, leaderboards - a
// few KB each), making it a safety net rather than a working limit.
// Override with [SetSharedCacheBudget].
const defaultSharedCacheBudget = 32 << 20

type sharedStore struct {
	mu       sync.RWMutex
	cap      int
	budget   int
	cur      map[string][]byte
	prev     map[string][]byte
	curBytes int // sum of value lengths in cur
}

func newSharedStore(capacity, budget int) *sharedStore {
	return &sharedStore{
		cap:    capacity,
		budget: budget,
		cur:    make(map[string][]byte),
		prev:   make(map[string][]byte),
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

	old, exists := s.cur[key]
	grown := s.curBytes + len(b)
	if exists {
		grown -= len(old)
	}

	// Rotate when this put would grow a full generation - full by
	// entry count (new keys only: overwriting does not grow the map,
	// so retiring the generation for it would evict live entries
	// early) or by byte budget (overwrites count here, because a
	// larger value does grow residency). The budget is a rotation
	// threshold, not a hard cap: a single fragment larger than the
	// whole budget still caches - sharing must keep working - but it
	// sits alone in its generation and rotates out on the next put.
	if (!exists && len(s.cur) >= s.cap) || (grown > s.budget && len(s.cur) > 0) {
		s.prev = s.cur
		s.cur = make(map[string][]byte, s.cap)
		s.curBytes = 0
		old = nil
	}

	s.cur[key] = b
	s.curBytes += len(b) - len(old)
}

// reset clears both generations and applies a new per-generation
// entry cap and byte budget where positive.
func (s *sharedStore) reset(entries, budget int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entries > 0 {
		s.cap = entries
	}
	if budget > 0 {
		s.budget = budget
	}
	s.cur = make(map[string][]byte)
	s.prev = make(map[string][]byte)
	s.curBytes = 0
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
// for [Shared] regions.
var sharedCache = newSharedStore(defaultSharedCacheSize, defaultSharedCacheBudget)

// SetSharedCacheSize sets the per-generation entry cap of the process-
// global shared-fragment cache and clears it. The cache holds at most
// twice this many rendered fragments. Call once at startup, before
// serving traffic. A non-positive n is ignored.
func SetSharedCacheSize(n int) {
	if n <= 0 {
		return
	}
	sharedCache.reset(n, 0)
}

// SetSharedCacheBudget sets the per-generation byte budget of the
// process-global shared-fragment cache and clears it. Generations
// rotate when either the entry cap or the byte budget would be
// exceeded, so total residency stays around twice this figure. Call
// once at startup, before serving traffic. A non-positive n is
// ignored.
func SetSharedCacheBudget(n int) {
	if n <= 0 {
		return
	}
	sharedCache.reset(0, n)
}

// ResetSharedCache empties the process-global shared-fragment cache
// without changing its size or budget. Intended for tests that need a
// clean starting state between cases.
func ResetSharedCache() {
	sharedCache.reset(0, 0)
}

// SharedCacheLen returns the number of distinct fragments currently
// held in the process-global shared-fragment cache.
func SharedCacheLen() int {
	return sharedCache.len()
}
