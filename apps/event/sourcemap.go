// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package event

// sourcemap.go — a Source Map v3 decoder, just big enough to map one generated
// position back to its original source, line and name.
//
// IN TREE, and small, on purpose. The plane reads maps an org's own build wrote, so
// the format it has to understand is the one every bundler emits (Vite, Rollup,
// esbuild, webpack, Next): a flat map with `sources`, `names` and a VLQ `mappings`
// string. An index map (`sections`) is refused at upload rather than half-read, and
// `sourcesContent` is never decoded — it is the org's source code, the plane has no
// use for it, and leaving it out of the struct is what keeps json from allocating it.
//
// Positions follow the spec: lines and columns in a map are 0-based, a stack frame's
// are 1-based. The conversion happens at lookup and nowhere else.

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// sourceMap is one parsed map: for every generated line, its segments sorted by
// generated column. int32 throughout, because a map of a large bundle carries
// millions of segments and the parsed form is what the LRU holds.
type sourceMap struct {
	sources []string
	names   []string
	lines   [][]segment
	// size approximates the parsed map's memory, which is what the cache bounds.
	size int
}

// segment is one decoded mapping. src < 0 marks a generated-only segment (one
// field in the wire), which maps to nothing.
type segment struct {
	genCol  int32
	src     int32
	srcLine int32
	srcCol  int32
	name    int32
}

// mapped is the original position a generated one maps to.
type mapped struct {
	source string
	line   uint32 // 1-based
	column uint32 // 1-based
	name   string
}

// rawMap is the wire. sourcesContent and x_* extensions are deliberately absent.
type rawMap struct {
	Version    int               `json:"version"`
	File       string            `json:"file"`
	SourceRoot string            `json:"sourceRoot"`
	Sources    []*string         `json:"sources"`
	Names      []string          `json:"names"`
	Mappings   string            `json:"mappings"`
	Sections   []json.RawMessage `json:"sections"`
}

// maxSegments bounds one map's decoded size whatever its byte length says, so a
// mappings string built to expand (a long run of one-character segments) cannot
// turn a capped upload into an uncapped allocation.
const maxSegments = 4 << 20

var (
	errNotSourceMap = errors.New("not a source map")
	errIndexMap     = errors.New("index maps (sections) are not supported; upload one map per generated file")
)

// parseSourceMap decodes one v3 map.
func parseSourceMap(data []byte) (*sourceMap, error) {
	var raw rawMap
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", errNotSourceMap, err)
	}
	if len(raw.Sections) > 0 {
		return nil, errIndexMap
	}
	if raw.Version != 3 {
		return nil, fmt.Errorf("%w: version %d, want 3", errNotSourceMap, raw.Version)
	}
	sm := &sourceMap{names: raw.Names, sources: make([]string, len(raw.Sources))}
	for i, s := range raw.Sources {
		if s != nil {
			sm.sources[i] = sourcePath(raw.SourceRoot, *s)
		}
	}
	if err := sm.decode(raw.Mappings); err != nil {
		return nil, err
	}
	return sm, nil
}

// decode reads the VLQ mappings string. Every field but the generated column is a
// delta against the previous segment in the whole map; the generated column resets
// at each line.
func (sm *sourceMap) decode(m string) error {
	var (
		line                         []segment
		genCol, src, srcLine, srcCol int32
		name                         int32
		total                        int
		fields                       [5]int32
	)
	for i := 0; i <= len(m); {
		if i == len(m) || m[i] == ';' {
			sm.lines = append(sm.lines, line)
			line, genCol = nil, 0
			i++
			continue
		}
		if m[i] == ',' {
			i++
			continue
		}
		n := 0
		for i < len(m) && m[i] != ',' && m[i] != ';' {
			if n == len(fields) {
				return fmt.Errorf("%w: a segment has more than five fields", errNotSourceMap)
			}
			v, next, err := vlq(m, i)
			if err != nil {
				return err
			}
			fields[n], i = v, next
			n++
		}
		if n != 1 && n != 4 && n != 5 {
			return fmt.Errorf("%w: a segment has %d fields", errNotSourceMap, n)
		}
		genCol += fields[0]
		seg := segment{genCol: genCol, src: -1, name: -1}
		if n >= 4 {
			src += fields[1]
			srcLine += fields[2]
			srcCol += fields[3]
			if src < 0 || int(src) >= len(sm.sources) || srcLine < 0 || srcCol < 0 {
				return fmt.Errorf("%w: a segment points outside its sources", errNotSourceMap)
			}
			seg.src, seg.srcLine, seg.srcCol = src, srcLine, srcCol
		}
		if n == 5 {
			name += fields[4]
			if name < 0 || int(name) >= len(sm.names) {
				return fmt.Errorf("%w: a segment names outside its names", errNotSourceMap)
			}
			seg.name = name
		}
		if total++; total > maxSegments {
			return fmt.Errorf("%w: more than %d segments", errNotSourceMap, maxSegments)
		}
		line = append(line, seg)
	}
	for _, l := range sm.lines {
		// Bundlers emit segments in column order; a map that does not is sorted
		// rather than refused, so a lookup's binary search stays correct.
		if !sort.SliceIsSorted(l, func(a, b int) bool { return l[a].genCol < l[b].genCol }) {
			sort.SliceStable(l, func(a, b int) bool { return l[a].genCol < l[b].genCol })
		}
	}
	sm.size = total*20 + len(sm.lines)*24
	for _, s := range sm.sources {
		sm.size += len(s) + 16
	}
	for _, s := range sm.names {
		sm.size += len(s) + 16
	}
	return nil
}

// lookup maps a 1-based generated (line, column) to its original position: the
// last segment on that line at or before the column. ok is false when the line has
// no segment there, or the segment maps to no source.
func (sm *sourceMap) lookup(line, column uint32) (mapped, bool) {
	if line == 0 || int(line) > len(sm.lines) {
		return mapped{}, false
	}
	segs := sm.lines[line-1]
	col := int32(0)
	if column > 0 {
		col = int32(column - 1)
	}
	i := sort.Search(len(segs), func(i int) bool { return segs[i].genCol > col }) - 1
	if i < 0 || segs[i].src < 0 {
		return mapped{}, false
	}
	s := segs[i]
	out := mapped{source: sm.sources[s.src], line: uint32(s.srcLine) + 1, column: uint32(s.srcCol) + 1}
	if s.name >= 0 {
		out.name = sm.names[s.name]
	}
	return out, out.source != ""
}

// vlq decodes one base64 VLQ value starting at m[i].
func vlq(m string, i int) (int32, int, error) {
	var v, shift uint32
	for {
		if i >= len(m) {
			return 0, i, fmt.Errorf("%w: truncated mapping", errNotSourceMap)
		}
		d := b64(m[i])
		if d < 0 {
			return 0, i, fmt.Errorf("%w: invalid mapping character %q", errNotSourceMap, m[i])
		}
		i++
		v |= uint32(d&31) << shift
		if d&32 == 0 {
			break
		}
		if shift += 5; shift > 30 {
			return 0, i, fmt.Errorf("%w: mapping value overflows", errNotSourceMap)
		}
	}
	n := int32(v >> 1)
	if v&1 == 1 {
		n = -n
	}
	return n, i, nil
}

func b64(c byte) int {
	switch {
	case c >= 'A' && c <= 'Z':
		return int(c - 'A')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 26
	case c >= '0' && c <= '9':
		return int(c-'0') + 52
	case c == '+':
		return 62
	case c == '/':
		return 63
	}
	return -1
}

// sourcePath is the original file as a reader wants it and as grouping needs it:
// the same file in two builds is one string. sourceRoot is joined, a bundler's
// scheme prefix (webpack://<pkg>/) is dropped, and the leading ./ and ../ a build
// directory adds are cleaned away, so `../../src/app.tsx` and `src/app.tsx` read
// alike.
func sourcePath(root, src string) string {
	src = strings.TrimSpace(src)
	if root = strings.TrimSpace(root); root != "" && !strings.Contains(src, "://") && !strings.HasPrefix(src, "/") {
		src = strings.TrimSuffix(root, "/") + "/" + src
	}
	if i := strings.Index(src, "://"); i >= 0 {
		rest := src[i+3:]
		if strings.HasPrefix(src, "webpack://") {
			// webpack://<namespace>/./src/x.ts — the namespace is the package name.
			if j := strings.IndexByte(rest, '/'); j >= 0 {
				rest = rest[j+1:]
			}
		} else if j := strings.IndexByte(rest, '/'); j >= 0 {
			rest = rest[j:] // a URL keeps its path
		}
		src = rest
	}
	if src == "" {
		return ""
	}
	clean := path.Clean("/" + src)
	return strings.TrimPrefix(clean, "/")
}
