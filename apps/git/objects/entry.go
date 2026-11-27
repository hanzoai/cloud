package objects

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
)

// A pack entry is a header, an optional delta reference, and a zlib stream:
//
//	header   type in three bits, then the length of what the stream inflates to, four
//	         bits and then seven at a time (entryHeader)
//	ofs      for an OFS_DELTA, the distance back to its base's entry
//	ref      for a REF_DELTA, the 20 byte name of its base
//	stream   the zlib stream
//
// The store keeps entries exactly as a pack has them and never re-encodes one except to
// turn an OFS_DELTA into a REF_DELTA when it moves to another pack.

// head is a parsed entry header.
type head struct {
	typ  plumbing.ObjectType // the entry's own type: a base type, OFSDelta or REFDelta
	size int64               // inflated length of the stream
	ofs  int64               // OFS_DELTA: distance back to the base
	ref  plumbing.Hash       // REF_DELTA: the base
	n    int                 // bytes the header and any reference take
}

var errShortEntry = errors.New("objects: truncated pack entry")

// parseHead reads the header of the entry at the start of b.
func parseHead(b []byte) (head, error) {
	var h head
	if len(b) == 0 {
		return h, errShortEntry
	}
	c := b[0]
	h.typ = plumbing.ObjectType(c >> 4 & 7)
	h.size = int64(c & 15)
	i, shift := 1, uint(4)
	for c&0x80 != 0 {
		if i >= len(b) || shift > 56 {
			return h, errShortEntry
		}
		c = b[i]
		i++
		h.size |= int64(c&0x7f) << shift
		shift += 7
	}
	switch h.typ {
	case plumbing.OFSDeltaObject:
		if i >= len(b) {
			return h, errShortEntry
		}
		c = b[i]
		i++
		h.ofs = int64(c & 0x7f)
		for c&0x80 != 0 {
			if i >= len(b) {
				return h, errShortEntry
			}
			c = b[i]
			i++
			h.ofs = (h.ofs+1)<<7 | int64(c&0x7f)
		}
	case plumbing.REFDeltaObject:
		if len(b) < i+hashSize {
			return h, errShortEntry
		}
		h.ref = hashOf(b[i : i+hashSize])
		i += hashSize
	case plumbing.CommitObject, plumbing.TreeObject, plumbing.BlobObject, plumbing.TagObject:
	default:
		return h, fmt.Errorf("objects: pack entry of type %d", h.typ)
	}
	h.n = i
	return h, nil
}

// entryHeader is a pack entry's header: the type in three bits and the content length
// in a variable-length number, four bits then seven at a time.
func entryHeader(t plumbing.ObjectType, size int64) []byte {
	c := byte(t)<<4 | byte(size&0x0f)
	size >>= 4
	var b []byte
	for size > 0 {
		b = append(b, c|0x80)
		c = byte(size & 0x7f)
		size >>= 7
	}
	return append(b, c)
}

// refDeltaHead is the header of a REF_DELTA whose delta inflates to size, naming base.
func refDeltaHead(size int64, base plumbing.Hash) []byte {
	return append(entryHeader(plumbing.REFDeltaObject, size), base[:]...)
}
