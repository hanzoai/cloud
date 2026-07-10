package bitstring

import "math"

type Segment int64

type BitString struct {
	segments []Segment
	Length   int
	Segments uint
	mask     Segment
}

var One = Segment(1)
var Zero = Segment(0)
var Full = ^Segment(0)

func New(length int) *BitString {
	lastSegmentBitCount := uint(length % 64)
	mask := Segment(Full << lastSegmentBitCount)
	segments := uint(math.Ceil(float64(length) / 64.0))
	return &BitString{make([]Segment, segments), length, segments, mask}
}

func (b *BitString) SetBit(idx uint) *BitString {
	segI := GetAddress(idx)

	if segI >= b.Segments {
		return b
	}

	segs := make([]Segment, b.Segments)
	for i := uint(0); i < b.Segments; i++ {
		if i == segI {
			addrI := GetOffset(idx)

			seg := b.segments[segI]
			seg |= (1 << addrI)
			segs[i] = seg
		} else {
			segs[i] = b.segments[segI]
		}
	}

	return &BitString{segs, b.Length, b.Segments, b.mask}
}

func (b *BitString) ClearBit(idx uint) *BitString {
	segI := GetAddress(idx)

	if segI >= b.Segments {
		return b
	}

	segs := make([]Segment, b.Segments)
	for i := uint(0); i < b.Segments; i++ {
		if i == segI {
			addrI := GetOffset(idx)

			seg := b.segments[segI]
			seg &= ^(One << addrI)
			segs[i] = seg
		} else {
			segs[i] = b.segments[segI]
		}
	}

	return &BitString{segs, b.Length, b.Segments, b.mask}
}

func (b *BitString) GetBit(idx uint) bool {
	segI := GetAddress(idx)
	if segI > b.Segments {
		return false
	}

	addrI := GetOffset(idx)
	seg := b.segments[segI]

	return (seg & (One << addrI)) > 0
}

func (b *BitString) Equal(b2 *BitString) bool {
	if b.Length != b2.Length {
		return false
	}

	for i := uint(0); i < b.Segments; i++ {
		if b.segments[i] != b2.segments[i] {
			return false
		}
	}
	return true
}

func (b *BitString) And(b2 *BitString) *BitString {
	bShort := b
	if b2.Length < b.Length {
		bShort = b2
	}

	segs := make([]Segment, b.Segments)
	for i := uint(0); i < b.Segments; i++ {
		segs[i] = b.segments[i] & b2.segments[i]
	}

	segs[b.Segments-1] &= bShort.mask

	return &BitString{segs, bShort.Length, bShort.Segments, bShort.mask}
}

func (b *BitString) Or(b2 *BitString) *BitString {
	bShort := b
	if b2.Length < b.Length {
		bShort = b2
	}

	segs := make([]Segment, b.Segments)
	for i := uint(0); i < b.Segments; i++ {
		segs[i] = b.segments[i] | b2.segments[i]
	}

	segs[b.Segments-1] &= bShort.mask

	return &BitString{segs, bShort.Length, bShort.Segments, bShort.mask}
}

func (b *BitString) GetSegment(idx uint) Segment {
	segI := GetAddress(idx)
	if segI >= b.Segments {
		return Zero
	}
	return b.segments[segI]
}

func GetAddress(idx uint) uint {
	return idx / 64
}

func GetOffset(idx uint) uint {
	return idx % 64
}
