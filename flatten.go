package jit

import (
	"bytes"
	"io"

	"github.com/jpl-au/fluent/node"
)

// Flattener holds pre-rendered static content as bytes.
// This is the instance API for static content rendering - no map lookups,
// just direct byte access. Ideal for maximum performance with static templates.
type Flattener struct {
	bytes []byte // pre-rendered static content
}

// NewFlattener creates a flattener by rendering static content once.
// Returns an error if the node contains dynamic content.
func NewFlattener(n node.Node) (*Flattener, error) {
	if isDynamic(n) {
		return nil, ErrDynamicContent
	}

	var buf bytes.Buffer
	n.RenderBuilder(&buf)

	return &Flattener{
		bytes: buf.Bytes(),
	}, nil
}

// Render writes the pre-rendered bytes to w. No rendering logic is
// executed - this is a direct byte slice write. Write errors are
// discarded - use WriteTo to observe them.
func (f *Flattener) Render(w io.Writer) {
	_, _ = f.WriteTo(w)
}

// WriteTo writes the pre-rendered bytes to w, returning the byte count
// and any write error.
func (f *Flattener) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(f.bytes)
	return int64(n), err
}

// RenderBytes returns the pre-rendered bytes directly.
func (f *Flattener) RenderBytes() []byte {
	return f.bytes
}
