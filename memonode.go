package jit

import (
	"bytes"
	"io"

	"github.com/jpl-au/fluent"
	"github.com/jpl-au/fluent/node"
)

// Memoised is satisfied by anything that carries a memoisation version
// for the diff engine: a [Memoise] or [Shared] node, or a generated
// element with .Memoise(version) chained. The match is structural, so
// core fluent never imports this package. A nil MemoiseKey means "not
// memoised" - every generated element has the method, but only those
// given a version participate. (Named Memoised because [Memoiser] is
// the engine that consumes it.)
//
// When the version matches the previous render at the same tree
// position, the [Memoiser] engine skips rendering and diffing that
// region entirely. Plain rendering ignores memoisation - a memoised
// node renders like any other, which keeps trees portable between the
// Differ and the Memoiser.
type Memoised interface {
	MemoiseKey() any
}

// SharedMemoised is a [Memoised] whose rendered bytes may be reused
// across sessions, not just across renders of one session. When the
// memoisation layer meets a shared node on a cache miss, it looks the
// key up in a process-global store: the first session to render a
// given key populates it, and every other session with the same key is
// served those bytes instead of running the closure. On a busy
// broadcast - a shared header, a live leaderboard - the render runs
// once for the whole process rather than once per session.
//
// The contract is stricter than plain memoisation: the key must be
// globally unique and must fully determine the rendered bytes. A plain
// [Memoise] version is only compared within one session at one tree
// position, so a bare counter is safe; a shared key is compared across
// every session in the process, so it must be namespaced and derived
// from the content itself (a version or hash), never from per-session
// state. [Shared] enforces none of this - correctness is the caller's.
type SharedMemoised interface {
	Memoised
	MemoiseShared() bool
}

// Memoise creates a lazy node with a cache version. The closure
// produces the subtree on demand. When used with a plain [Differ], the
// closure is called unconditionally on every render (memoised nodes
// are transparent). When used with the [Memoiser] engine, the closure
// is skipped if the version matches the previous render at the same
// tree position.
//
// Versions are converted to strings using scalar formatting, with
// fmt.Sprint for other types. Prefer counters or stable string keys that
// fully identify the region's content. A parent version governs its whole
// subtree: a parent cache hit also skips checks of nested child versions.
//
// For subtrees that are cheap to build but expensive to render and
// diff, the chained element form is more ergonomic and equivalent on
// a hit: div.New(rows...).Dynamic("board").Memoise(version). The
// closure form additionally defers construction, which matters when
// building the subtree is itself expensive.
//
//	jit.Memoise(s.ItemsVersion, func() node.Node {
//	    return renderTable(s.Items.Val)
//	})
func Memoise(version any, fn func() node.Node) *MemoisedNode {
	return &MemoisedNode{key: version, fn: fn}
}

// Shared is like [Memoise] but marks the region as reusable across
// sessions: the memoisation layer caches its rendered bytes in a
// process-global store keyed by key, so the closure runs at most once
// per distinct key for the whole process rather than once per session.
// Use it for regions that render identically for every user - a shared
// header, a navigation bar, a live scoreboard broadcast to a room.
//
// The key MUST be globally unique and MUST fully determine the
// rendered bytes. Namespace it and derive it from the content
// ("nav:v3", "board:" + hash), never from per-session state - two
// sessions with the same key are served the same bytes. See
// [SharedMemoised] for the full contract.
//
//	jit.Shared("leaderboard:"+s.BoardVersion, func() node.Node {
//	    return renderBoard(s.Board)
//	})
func Shared(key any, fn func() node.Node) *MemoisedNode {
	return &MemoisedNode{key: key, fn: fn, shared: true}
}

// MemoisedNode wraps a lazy closure with a cache version. It satisfies
// [node.Node] so it slots into any position in a render tree, and
// [Memoised] so the memoisation layer can inspect the version.
type MemoisedNode struct {
	key    any
	fn     func() node.Node
	shared bool
}

// MemoiseKey returns the cache version for this node. The memoisation
// layer compares it with the previous render's version at the same
// position.
func (m *MemoisedNode) MemoiseKey() any { return m.key }

// MemoiseShared reports whether this node's rendered bytes may be
// reused across sessions via the process-global store. True only for
// nodes created with [Shared]. See [SharedMemoised].
func (m *MemoisedNode) MemoiseShared() bool { return m.shared }

// MemoiseRender calls the closure and returns the resulting subtree.
func (m *MemoisedNode) MemoiseRender() node.Node {
	if m.fn == nil {
		return nil
	}
	return m.fn()
}

// Render calls the closure unconditionally and writes the result to w.
// This is the path taken by a plain [Differ] (which does not check for
// Memoiser). Write errors are discarded - use WriteTo to observe them.
func (m *MemoisedNode) Render(w io.Writer) {
	_, _ = m.WriteTo(w)
}

// WriteTo calls the closure and writes the rendered result to w,
// returning the byte count and any write error. Satisfies
// [io.WriterTo].
func (m *MemoisedNode) WriteTo(w io.Writer) (int64, error) {
	buf := fluent.NewBuffer()
	m.RenderBuilder(buf)
	n, err := buf.WriteTo(w)
	fluent.PutBuffer(buf)
	return n, err
}

// RenderBytes calls the closure and returns the rendered result as a
// byte slice.
func (m *MemoisedNode) RenderBytes() []byte {
	var buf bytes.Buffer
	m.RenderBuilder(&buf)
	return buf.Bytes()
}

// RenderBuilder calls the closure and writes the result into the
// buffer. Nil closures and nil returns render nothing.
func (m *MemoisedNode) RenderBuilder(buf *bytes.Buffer) {
	if m.fn == nil {
		return
	}
	if n := m.fn(); n != nil {
		n.RenderBuilder(buf)
	}
}

// Nodes calls the closure and returns its output so tree walkers see
// the same children that Render produces.
func (m *MemoisedNode) Nodes() []node.Node {
	if m.fn == nil {
		return nil
	}
	if n := m.fn(); n != nil {
		return []node.Node{n}
	}
	return nil
}
