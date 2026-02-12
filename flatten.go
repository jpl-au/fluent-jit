package jit

import (
	"bytes"
	"errors"
	"io"

	"github.com/jpl-au/fluent/node"
)

// ErrDynamicContent is returned when attempting to flatten dynamic content.
var ErrDynamicContent = errors.New("NewFlattener() requires static content - use NewCompiler() for dynamic content")

// Flattener holds pre-rendered static content as bytes.
// This is the instance API for static content rendering — no map lookups,
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

// Render writes the pre-rendered bytes to the writer or returns them.
// No rendering logic is executed — this is a direct byte slice write.
func (f *Flattener) Render(w ...io.Writer) []byte {
	if len(w) > 0 && w[0] != nil {
		_, _ = w[0].Write(f.bytes)
		return nil
	}
	return f.bytes
}
