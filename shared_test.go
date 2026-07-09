package jit

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// sharedTree builds a render tree whose one Dynamic region is a
// Shared keyed by "nav:vN". calls is incremented every time the
// closure actually runs, so a test can prove the render was skipped.
func sharedTree(version int, calls *int) node.Node {
	return div.New(
		div.New(
			Shared("nav:v"+strconv.Itoa(version), func() node.Node {
				*calls++
				return span.Text("nav for v" + strconv.Itoa(version))
			}),
		).Dynamic("nav"),
	)
}

// TestSharedCacheReusesAcrossSessions is the core guarantee: when two
// independent Memoisers (two sessions) diff to the same shared key, the
// second one serves the fragment from the process-global cache instead
// of running the closure - and the bytes are identical.
func TestSharedCacheReusesAcrossSessions(t *testing.T) {
	ResetSharedCache()

	var callsA, callsB int
	a := NewMemoiser()
	b := NewMemoiser()

	// Both sessions start at v1 and are seeded.
	a.RenderBytes(sharedTree(1, &callsA))
	b.RenderBytes(sharedTree(1, &callsB))

	// The shared data changes to v2. Session A diffs first and renders
	// it; session B diffs second and should reuse A's bytes.
	callsA, callsB = 0, 0
	pa, _ := a.Diff(sharedTree(2, &callsA))
	pb, _ := b.Diff(sharedTree(2, &callsB))

	if callsA != 1 {
		t.Errorf("session A should render once, ran closure %d times", callsA)
	}
	if callsB != 0 {
		t.Errorf("session B should reuse the cache, but ran the closure %d times", callsB)
	}

	hitsA, missesA := a.SharedStats()
	if hitsA != 0 || missesA != 1 {
		t.Errorf("session A shared stats = (%d hit, %d miss), want (0, 1)", hitsA, missesA)
	}
	hitsB, missesB := b.SharedStats()
	if hitsB != 1 || missesB != 0 {
		t.Errorf("session B shared stats = (%d hit, %d miss), want (1, 0)", hitsB, missesB)
	}

	if len(pa) != 1 || len(pb) != 1 {
		t.Fatalf("expected one patch each, got A=%d B=%d", len(pa), len(pb))
	}
	if string(pa[0].HTML) != string(pb[0].HTML) {
		t.Errorf("shared fragment differs between sessions:\n A: %s\n B: %s", pa[0].HTML, pb[0].HTML)
	}
}

// TestSharedCacheStoresRenderedFragment verifies a miss populates the
// global cache so a later session finds it.
func TestSharedCacheStoresRenderedFragment(t *testing.T) {
	ResetSharedCache()
	if n := SharedCacheLen(); n != 0 {
		t.Fatalf("cache should start empty, has %d", n)
	}

	var calls int
	m := NewMemoiser()
	m.RenderBytes(sharedTree(1, &calls))

	if n := SharedCacheLen(); n != 1 {
		t.Errorf("seed should have cached one fragment, cache has %d", n)
	}
}

// TestMemoiseDoesNotShare confirms a plain Memoise never touches
// the shared cache - only Shared opts in.
func TestMemoiseDoesNotShare(t *testing.T) {
	ResetSharedCache()

	m := NewMemoiser()
	m.RenderBytes(div.New(
		div.New(
			Memoise("v1", func() node.Node { return span.Text("plain") }),
		).Dynamic("nav"),
	))

	if n := SharedCacheLen(); n != 0 {
		t.Errorf("plain memoise should not populate the shared cache, has %d", n)
	}
}

// TestSharedCacheKeysDoNotCollide verifies distinct keys keep distinct
// bytes - no cross-key contamination.
func TestSharedCacheKeysDoNotCollide(t *testing.T) {
	ResetSharedCache()

	tree := func(key, text string) node.Node {
		return div.New(
			div.New(
				Shared(key, func() node.Node { return span.Text(text) }),
			).Dynamic("region"),
		)
	}

	a := NewMemoiser()
	a.RenderBytes(tree("a:v1", "alpha"))
	b := NewMemoiser()
	b.RenderBytes(tree("b:v1", "beta"))

	// Diff each to the other's key would be a structural no-op (same
	// Dynamic key "region"), so instead diff each forward and confirm
	// the cache served the right bytes by their content.
	pa, _ := a.Diff(tree("a:v2", "alpha-2"))
	pb, _ := b.Diff(tree("b:v2", "beta-2"))
	if len(pa) != 1 || len(pb) != 1 {
		t.Fatalf("expected one patch each")
	}
	if string(pa[0].HTML) == string(pb[0].HTML) {
		t.Errorf("distinct keys produced identical bytes: %s", pa[0].HTML)
	}
}

// TestSharedCacheBounded verifies the two-generation scheme keeps the
// cache size bounded to at most twice the configured cap.
func TestSharedCacheBounded(t *testing.T) {
	SetSharedCacheSize(2)
	t.Cleanup(func() { SetSharedCacheSize(defaultSharedCacheSize) })

	var calls int
	m := NewMemoiser()
	m.RenderBytes(sharedTree(0, &calls))
	for v := 1; v <= 20; v++ {
		m.Diff(sharedTree(v, &calls))
	}

	if n := SharedCacheLen(); n > 4 {
		t.Errorf("cache should stay bounded to 2×cap=4, has %d", n)
	}
}

// TestSharedCacheHitStillMarksElement verifies that a full Render served
// by a shared-cache hit still stamps data-fluent-memoise on the live
// element. The page HTML comes from a later root.Render over the tree,
// so skipping the attribute on the hit path would serve a page that
// disagrees with the stored snapshot and with every other session.
func TestSharedCacheHitStillMarksElement(t *testing.T) {
	ResetSharedCache()

	var callsA, callsB int
	a := NewMemoiser()
	a.RenderBytes(sharedTree(1, &callsA)) // populates the cache

	b := NewMemoiser()
	html := string(b.RenderBytes(sharedTree(1, &callsB)))
	if !strings.Contains(html, "data-fluent-memoise") || !strings.Contains(html, "nav:v1") {
		t.Errorf("cache-hit render should carry the memoise attribute in the page HTML, got:\n%s", html)
	}
}

// TestSharedCacheOverwriteKeepsGeneration verifies that overwriting a
// key already present in a full current generation does not rotate the
// generations - the map has not grown, so retiring it would evict live
// entries early.
func TestSharedCacheOverwriteKeepsGeneration(t *testing.T) {
	s := newSharedStore(2, defaultSharedCacheBudget)
	s.put("a", []byte("a1"))
	s.put("b", []byte("b1"))

	// cur is now full. Overwriting a key must update in place.
	s.put("a", []byte("a2"))
	if len(s.prev) != 0 {
		t.Errorf("overwrite should not retire the generation, prev has %d entries", len(s.prev))
	}
	if got, _ := s.get("a"); string(got) != "a2" {
		t.Errorf("overwritten key should return new bytes, got %q", got)
	}

	// A genuinely new key does rotate.
	s.put("c", []byte("c1"))
	if len(s.prev) != 2 {
		t.Errorf("new key on a full generation should rotate, prev has %d entries", len(s.prev))
	}
}

// TestSharedCacheHitSkipsClosureOnRender verifies that a full Render
// served by a shared-cache hit does not run the closure at all - the
// cached bytes serve both the page and the snapshot. The old two-pass
// design re-ran the closure for the page HTML, negating the shared
// cache on initial loads.
func TestSharedCacheHitSkipsClosureOnRender(t *testing.T) {
	ResetSharedCache()

	var callsA, callsB int
	a := NewMemoiser()
	pageA := string(a.RenderBytes(sharedTree(1, &callsA)))
	if callsA != 1 {
		t.Fatalf("populating session should run the closure once, ran %d times", callsA)
	}

	b := NewMemoiser()
	pageB := string(b.RenderBytes(sharedTree(1, &callsB)))
	if callsB != 0 {
		t.Errorf("cache-hit render should not run the closure, ran %d times", callsB)
	}
	if pageB != pageA {
		t.Errorf("cache-hit page should match the populating session's page:\n A: %s\n B: %s", pageA, pageB)
	}
	if hits, misses := b.SharedStats(); hits != 1 || misses != 0 {
		t.Errorf("seeding shared stats = (%d hit, %d miss), want (1, 0)", hits, misses)
	}
}

// TestSharedCacheByteBudgetRotates verifies that a generation rotates
// when the byte budget would be exceeded, even with the entry cap far
// from full.
func TestSharedCacheByteBudgetRotates(t *testing.T) {
	s := newSharedStore(1000, 10)
	s.put("a", []byte("12345"))
	s.put("b", []byte("12345"))

	// cur holds exactly 10 bytes. One more byte must rotate.
	s.put("c", []byte("x"))
	if len(s.prev) != 2 {
		t.Errorf("byte budget should rotate the generation, prev has %d entries", len(s.prev))
	}
	if s.curBytes != 1 {
		t.Errorf("fresh generation should hold 1 byte, has %d", s.curBytes)
	}

	// Entries from the retired generation stay readable.
	if _, ok := s.get("a"); !ok {
		t.Error("retired generation should still serve lookups")
	}
}

// TestSharedCacheOverwriteAccounting verifies that overwriting a key
// adjusts the byte total by the size difference, and that an overwrite
// which grows past the budget rotates - unlike the entry cap, a larger
// value genuinely grows residency.
func TestSharedCacheOverwriteAccounting(t *testing.T) {
	s := newSharedStore(1000, 10)
	s.put("a", []byte("1234"))
	s.put("a", []byte("12"))
	if s.curBytes != 2 {
		t.Errorf("shrinking overwrite should leave 2 bytes, has %d", s.curBytes)
	}
	if len(s.prev) != 0 {
		t.Errorf("in-budget overwrite should not rotate, prev has %d entries", len(s.prev))
	}

	s.put("b", []byte("1234567"))   // 9 bytes total
	s.put("b", []byte("123456789")) // would be 11 - rotates
	if len(s.prev) != 2 {
		t.Errorf("overwrite growing past the budget should rotate, prev has %d entries", len(s.prev))
	}
	if got, _ := s.get("b"); string(got) != "123456789" {
		t.Errorf("overwritten key should return new bytes, got %q", got)
	}
}

// TestSharedCacheOversizedFragmentStillCaches verifies the documented
// policy for a fragment larger than the whole budget: it caches (the
// sharing contract must keep working) but sits alone in its generation
// and rotates out on the next put.
func TestSharedCacheOversizedFragmentStillCaches(t *testing.T) {
	s := newSharedStore(1000, 10)
	s.put("huge", []byte("this is far larger than ten bytes"))
	if _, ok := s.get("huge"); !ok {
		t.Error("oversized fragment should still cache")
	}

	s.put("small", []byte("x"))
	if len(s.prev) != 1 {
		t.Errorf("next put should rotate the oversized generation, prev has %d entries", len(s.prev))
	}
}

// TestSetSharedCacheBudget verifies the process-global setter applies
// the budget and clears the cache, and ignores non-positive values.
func TestSetSharedCacheBudget(t *testing.T) {
	t.Cleanup(func() {
		sharedCache.reset(defaultSharedCacheSize, defaultSharedCacheBudget)
	})

	SetSharedCacheBudget(10)
	sharedCache.put("a", []byte("123456"))
	sharedCache.put("b", []byte("123456"))
	if len(sharedCache.prev) != 1 {
		t.Errorf("configured budget should rotate at 10 bytes, prev has %d entries", len(sharedCache.prev))
	}

	SetSharedCacheBudget(-1)
	if sharedCache.budget != 10 {
		t.Errorf("non-positive budget should be ignored, budget is %d", sharedCache.budget)
	}
}
