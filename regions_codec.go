package jit

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
)

// Snapshot blobs begin with this internal encoding marker. Both engines use
// the same opaque representation for HTML and region metadata.
const exportVersion = 1

func (t *regionTree) export() []byte {
	if !t.seeded {
		return nil
	}
	return t.encode(t.roots, true)
}

// encode can also capture a single Shared subtree, without unrelated regions.
func (t *regionTree) encode(roots []string, detached bool) []byte {
	keys := []string{}
	seen := make(map[string]bool)
	var visit func(string)
	visit = func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		keys = append(keys, key)
		for _, child := range t.regions[key].children {
			visit(child.key)
		}
	}
	for _, key := range roots {
		visit(key)
	}
	if detached {
		var rest []string
		for key := range t.regions {
			if !seen[key] {
				rest = append(rest, key)
			}
		}
		slices.Sort(rest)
		for _, key := range rest {
			visit(key)
		}
	}
	data := []byte{exportVersion}
	u32 := func(n int) { data = binary.LittleEndian.AppendUint32(data, uint32(n)) }
	str := func(s string) { u32(len(s)); data = append(data, s...) }
	u32(len(keys))
	for _, key := range keys {
		r := t.regions[key]
		str(key)
		u32(r.html.Len())
		data = append(data, r.html.Bytes()...)
		str(r.version)
		u32(len(r.children))
		for _, child := range r.children {
			str(child.key)
			u32(child.start)
			u32(child.end)
		}
	}
	u32(len(roots))
	for _, key := range roots {
		str(key)
	}
	return data
}

type regionDecoder struct {
	data []byte
	err  error
}

func (d *regionDecoder) fail() {
	if d.err == nil {
		d.err = fmt.Errorf("jit: import: invalid region snapshot")
	}
}

func (d *regionDecoder) take(n uint32) []byte {
	if d.err != nil || uint64(n) > uint64(len(d.data)) {
		d.fail()
		return nil
	}
	b := d.data[:n]
	d.data = d.data[n:]
	return b
}

func (d *regionDecoder) u32() uint32 {
	b := d.take(4)
	if len(b) != 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (d *regionDecoder) blob() []byte { return d.take(d.u32()) }

// Every counted item requires at least one four-byte field. Bound iteration
// and allocation before trusting any count from external snapshot storage.
func (d *regionDecoder) count() int {
	n := d.u32()
	if uint64(n) > uint64(len(d.data)/4) {
		d.fail()
		return 0
	}
	return int(n)
}

func decodeRegions(data []byte) (tree regionTree, err error) {
	tree = regionTree{}
	defer func() {
		if err != nil {
			tree.releaseExcept(nil)
		}
	}()
	if len(data) == 0 || data[0] != exportVersion {
		return tree, fmt.Errorf("jit: import: unsupported or missing export version (want %d)", exportVersion)
	}
	d := regionDecoder{data: data[1:]}
	count := d.count()
	tree.regions = make(map[string]*region, count)
	for i := 0; i < count && d.err == nil; i++ {
		key := string(d.blob())
		html := d.blob()
		version := string(d.blob())
		children := make([]regionChild, d.count())
		for i := range children {
			children[i] = regionChild{string(d.blob()), int(d.u32()), int(d.u32())}
		}
		if d.err != nil {
			break
		}
		if tree.regions[key] != nil {
			d.fail()
			break
		}
		r := takeRegion(len(html), version)
		r.html.Write(html)
		r.children = append(r.children, children...)
		tree.regions[key] = r
	}
	for n := d.count(); n > 0 && d.err == nil; n-- {
		tree.roots = append(tree.roots, string(d.blob()))
	}
	if d.err != nil || len(d.data) != 0 {
		d.fail()
		return tree, d.err
	}
	for key, r := range tree.regions {
		end := 0
		for _, child := range r.children {
			c := tree.regions[child.key]
			_, hasParent := tree.parents[child.key]
			if c == nil || hasParent || child.start < end || child.end < child.start || child.end > r.html.Len() {
				d.fail()
				return tree, d.err
			}
			if !bytes.Equal(r.html.Bytes()[child.start:child.end], c.html.Bytes()) {
				d.fail()
				return tree, d.err
			}
			tree.setParent(child.key, key)
			end = child.end
		}
	}
	// Iterative traversal rejects cycles, including detached components, without
	// recursing through an untrusted depth supplied by a corrupt snapshot.
	queue := make([]string, 0, len(tree.regions))
	for key := range tree.regions {
		if _, hasParent := tree.parents[key]; !hasParent {
			queue = append(queue, key)
		}
	}
	for i := 0; i < len(queue); i++ {
		for _, child := range tree.regions[queue[i]].children {
			queue = append(queue, child.key)
		}
	}
	if len(queue) != len(tree.regions) {
		d.fail()
		return tree, d.err
	}
	seen := make(map[string]bool)
	for _, key := range tree.roots {
		_, hasParent := tree.parents[key]
		if tree.regions[key] == nil || hasParent || seen[key] {
			d.fail()
			return tree, d.err
		}
		seen[key] = true
	}
	tree.seeded = true
	return tree, nil
}
