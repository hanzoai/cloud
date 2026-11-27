package vec

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"iter"
	"math"
	"slices"
)

// Encode is the stored form of a vector: each component as its IEEE 754 bits,
// little-endian, four bytes apiece. Fixed-width and headerless, so a row's
// dimension is len(blob)/4 and nothing else has to be kept to read it back.
func Encode(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// Decode reads a blob Encode wrote. A length that is not a multiple of four is
// not a vector — it is a truncated or foreign value — and is refused rather than
// read short, which would hand a scorer a vector one component shy of its
// neighbours.
func Decode(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("vec: %d bytes is not a float32 vector", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v, nil
}

// Cos is the cosine similarity of a and b, from -1 to 1.
//
// Two vectors of different dimension are an error, never a score. They come from
// different models (or one model re-sized under a live index), and there is no
// honest number to give: padding the shorter with zeros would rank vectors that
// were never comparable. A zero vector has no direction and scores 0.
func Cos(a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vec: dimension %d against %d", len(a), len(b))
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0, nil
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), nil
}

// Hit is one key and its score.
type Hit[K cmp.Ordered] struct {
	Key   K
	Score float64
}

// better orders hits best first: the higher score, and between equal scores the
// lower key, so a result never depends on the order a scan happened to visit rows
// in. cmp.Compare puts NaN below every number, so a NaN never displaces a score.
func better[K cmp.Ordered](a, b Hit[K]) bool {
	if c := cmp.Compare(a.Score, b.Score); c != 0 {
		return c > 0
	}
	return cmp.Less(a.Key, b.Key)
}

// Top keeps the k best of seq, best first, in O(n log k) time and O(k) memory: a
// min-heap of the k best so far, whose root is the worst of them and the only one
// a newcomer has to beat. A scan therefore never holds more than k hits whatever
// it reads. Ties break on the key (see better). k <= 0 keeps nothing; fewer than k
// in seq keeps them all.
func Top[K cmp.Ordered](seq iter.Seq2[K, float64], k int) []Hit[K] {
	if k <= 0 {
		return nil
	}
	var h []Hit[K]
	for key, score := range seq {
		x := Hit[K]{Key: key, Score: score}
		switch {
		case len(h) < k:
			h = append(h, x)
			for i := len(h) - 1; i > 0; {
				p := (i - 1) / 2
				if !better(h[p], h[i]) {
					break
				}
				h[p], h[i] = h[i], h[p]
				i = p
			}
		case better(x, h[0]):
			h[0] = x
			for i := 0; ; {
				worst, l, r := i, 2*i+1, 2*i+2
				if l < len(h) && better(h[worst], h[l]) {
					worst = l
				}
				if r < len(h) && better(h[worst], h[r]) {
					worst = r
				}
				if worst == i {
					break
				}
				h[i], h[worst] = h[worst], h[i]
				i = worst
			}
		}
	}
	slices.SortFunc(h, func(a, b Hit[K]) int {
		switch {
		case better(a, b):
			return -1
		case better(b, a):
			return 1
		}
		return 0
	})
	return h
}

// BatchSize bounds one embedding request, so indexing a large document or a
// large repository fans out over several round trips instead of one body the
// gateway may refuse.
const BatchSize = 64

// Batch calls embed over texts BatchSize at a time and joins the answers in
// order. It stops at the first error, and refuses an answer that does not carry
// exactly one vector per text: a short answer would pair every vector after the
// gap with the wrong text, silently.
func Batch(texts []string, embed func([]string) ([][]float32, error)) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for lo := 0; lo < len(texts); lo += BatchSize {
		hi := min(lo+BatchSize, len(texts))
		vs, err := embed(texts[lo:hi])
		if err != nil {
			return nil, err
		}
		if len(vs) != hi-lo {
			return nil, fmt.Errorf("vec: %d vectors for %d texts", len(vs), hi-lo)
		}
		out = append(out, vs...)
	}
	return out, nil
}
