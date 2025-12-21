package jit

import (
	"fmt"
	"io"

	"github.com/jpl-au/fluent"
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
	if dynamic(n) {
		return nil, fmt.Errorf("NewFlattener() requires static content - use NewCompiler() for dynamic content")
	}

	buf := fluent.NewBuffer()
	defer fluent.PutBuffer(buf)
	n.RenderBuilder(buf)

	return &Flattener{
		bytes: append([]byte{}, buf.Bytes()...),
	}, nil
}

// Render writes the pre-rendered bytes to the writer or returns them.
// This is extremely fast - just a byte slice copy/write operation.
func (f *Flattener) Render(w ...io.Writer) []byte {
	if len(w) > 0 && w[0] != nil {
		w[0].Write(f.bytes)
		return nil
	}
	return f.bytes
}
