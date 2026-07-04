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
// node.Shared keyed by "nav:vN". calls is incremented every time the
// closure actually runs, so a test can prove the render was skipped.
func sharedTree(version int, calls *int) node.Node {
	return div.New(
		div.New(
			node.Shared("nav:v"+strconv.Itoa(version), func() node.Node {
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
	a.Render(sharedTree(1, &callsA))
	b.Render(sharedTree(1, &callsB))

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
	m.Render(sharedTree(1, &calls))

	if n := SharedCacheLen(); n != 1 {
		t.Errorf("seed should have cached one fragment, cache has %d", n)
	}
}

// TestMemoiseDoesNotShare confirms a plain node.Memoise never touches
// the shared cache - only node.Shared opts in.
func TestMemoiseDoesNotShare(t *testing.T) {
	ResetSharedCache()

	m := NewMemoiser()
	m.Render(div.New(
		div.New(
			node.Memoise("v1", func() node.Node { return span.Text("plain") }),
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
				node.Shared(key, func() node.Node { return span.Text(text) }),
			).Dynamic("region"),
		)
	}

	a := NewMemoiser()
	a.Render(tree("a:v1", "alpha"))
	b := NewMemoiser()
	b.Render(tree("b:v1", "beta"))

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
	m.Render(sharedTree(0, &calls))
	for v := 1; v <= 20; v++ {
		m.Diff(sharedTree(v, &calls))
	}

	if n := SharedCacheLen(); n > 4 {
		t.Errorf("cache should stay bounded to 2×cap=4, has %d", n)
	}
}

// TestSharedCacheHitStillMarksElement verifies that a full Render served
// by a shared-cache hit still stamps data-tether-memoise on the live
// element. The page HTML comes from a later root.Render over the tree,
// so skipping the attribute on the hit path would serve a page that
// disagrees with the stored snapshot and with every other session.
func TestSharedCacheHitStillMarksElement(t *testing.T) {
	ResetSharedCache()

	var callsA, callsB int
	a := NewMemoiser()
	a.Render(sharedTree(1, &callsA)) // populates the cache

	b := NewMemoiser()
	html := string(b.Render(sharedTree(1, &callsB)))
	if !strings.Contains(html, "data-tether-memoise") || !strings.Contains(html, "nav:v1") {
		t.Errorf("cache-hit render should carry the memoise attribute in the page HTML, got:\n%s", html)
	}
}
