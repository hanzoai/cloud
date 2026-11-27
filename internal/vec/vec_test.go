package vec_test

import (
	"errors"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/internal/vec"
)

// reference is the obvious answer Top must agree with: sort everything by score,
// descending, ties by key ascending, and cut at k.
func reference(in map[int]float64, k int) []vec.Hit[int] {
	all := make([]vec.Hit[int], 0, len(in))
	for key, s := range in {
		all = append(all, vec.Hit[int]{Key: key, Score: s})
	}
	slices.SortFunc(all, func(a, b vec.Hit[int]) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return a.Key - b.Key
	})
	if k <= 0 {
		return nil
	}
	return all[:min(k, len(all))]
}

// TestTopAgreesWithSort holds the heap against a full sort over seeded random
// inputs. Scores are drawn from a handful of values so most inputs are full of
// ties, and a map is iterated so the visiting order is itself random: the answer
// must still be exactly the sorted one, which is what makes a scan's result
// independent of the order its rows arrive in.
func TestTopAgreesWithSort(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for trial := range 500 {
		n := rng.IntN(200)
		in := map[int]float64{}
		for range n {
			in[rng.IntN(1000)] = float64(rng.IntN(6)) / 5
		}
		for _, k := range []int{-1, 0, 1, 3, len(in) / 2, len(in), len(in) + 5} {
			got := vec.Top(maps.All(in), k)
			want := reference(in, k)
			if !slices.Equal(got, want) {
				t.Fatalf("trial %d, n=%d, k=%d:\n got %v\nwant %v", trial, len(in), k, got, want)
			}
		}
	}
}

// TestTopKeepsDuplicateKeys: Top ranks what it is given. A key yielded twice is
// two hits, so a caller that wants one per key reduces before it asks.
func TestTopKeepsDuplicateKeys(t *testing.T) {
	seq := func(yield func(string, float64) bool) {
		for _, h := range []vec.Hit[string]{{"a", 0.5}, {"a", 0.9}, {"b", 0.7}} {
			if !yield(h.Key, h.Score) {
				return
			}
		}
	}
	got := vec.Top(seq, 2)
	want := []vec.Hit[string]{{"a", 0.9}, {"b", 0.7}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// TestTopNaNNeverWins: a NaN score sorts below every number, so it is the first
// thing a full heap drops and never displaces a real score.
func TestTopNaNNeverWins(t *testing.T) {
	in := map[int]float64{1: math.NaN(), 2: 0.1, 3: -0.4}
	got := vec.Top(maps.All(in), 2)
	if len(got) != 2 || got[0].Key != 2 || got[1].Key != 3 {
		t.Fatalf("got %v", got)
	}
}

// TestCosDimensionMismatch: two vectors of different dimension came from two
// models, and comparing them is an error — never a zero-padded score.
func TestCosDimensionMismatch(t *testing.T) {
	if _, err := vec.Cos([]float32{1, 0, 0}, []float32{1, 0}); err == nil {
		t.Fatal("a 3-dimensional vector scored against a 2-dimensional one")
	}
	if s, err := vec.Cos([]float32{1, 2}, []float32{2, 4}); err != nil || math.Abs(s-1) > 1e-12 {
		t.Fatalf("parallel vectors: %v %v", s, err)
	}
	if s, err := vec.Cos([]float32{1, 0}, []float32{0, 1}); err != nil || s != 0 {
		t.Fatalf("orthogonal vectors: %v %v", s, err)
	}
	if s, err := vec.Cos([]float32{1, 0}, []float32{-1, 0}); err != nil || s != -1 {
		t.Fatalf("opposite vectors: %v %v", s, err)
	}
	if s, err := vec.Cos([]float32{0, 0}, []float32{1, 0}); err != nil || s != 0 {
		t.Fatalf("a zero vector has no direction and scores 0: %v %v", s, err)
	}
}

// TestEncodeDecodeRoundTrip: every float32 comes back bit-for-bit, the edge
// values included, and a blob whose length is not a multiple of four is refused.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	v := []float32{0, float32(math.Copysign(0, -1)), 1, -1, math.MaxFloat32, math.SmallestNonzeroFloat32,
		float32(math.Inf(1)), float32(math.Inf(-1))}
	for range 1000 {
		v = append(v, rng.Float32()*2-1)
	}
	b := vec.Encode(v)
	if len(b) != 4*len(v) {
		t.Fatalf("encoded %d floats into %d bytes", len(v), len(b))
	}
	got, err := vec.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := range v {
		if math.Float32bits(got[i]) != math.Float32bits(v[i]) {
			t.Fatalf("component %d: %v came back %v", i, v[i], got[i])
		}
	}
	if nan, _ := vec.Decode(vec.Encode([]float32{float32(math.NaN())})); !math.IsNaN(float64(nan[0])) {
		t.Fatal("NaN did not survive the round trip")
	}
	for _, n := range []int{1, 2, 3, 5, 7} {
		if _, err := vec.Decode(make([]byte, n)); err == nil {
			t.Fatalf("%d bytes decoded as a vector", n)
		}
	}
	if got, err := vec.Decode(nil); err != nil || len(got) != 0 {
		t.Fatalf("an empty blob is the empty vector: %v %v", got, err)
	}
}

// TestBatch: texts go out BatchSize at a time and come back in order, an error
// stops the run, and an answer one vector short is refused rather than
// misaligned.
func TestBatch(t *testing.T) {
	texts := make([]string, 2*vec.BatchSize+3)
	for i := range texts {
		texts[i] = string(rune('a' + i%26))
	}
	var sizes []int
	got, err := vec.Batch(texts, func(b []string) ([][]float32, error) {
		sizes = append(sizes, len(b))
		out := make([][]float32, len(b))
		for i, s := range b {
			out[i] = []float32{float32(s[0])}
		}
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sizes, []int{vec.BatchSize, vec.BatchSize, 3}) {
		t.Fatalf("batches %v", sizes)
	}
	for i, v := range got {
		if v[0] != float32(texts[i][0]) {
			t.Fatalf("vector %d belongs to another text", i)
		}
	}
	down := errors.New("down")
	if _, err := vec.Batch(texts, func([]string) ([][]float32, error) { return nil, down }); !errors.Is(err, down) {
		t.Fatalf("error not surfaced: %v", err)
	}
	short := func(b []string) ([][]float32, error) { return make([][]float32, len(b)-1), nil }
	if _, err := vec.Batch(texts, short); err == nil {
		t.Fatal("a short answer was accepted")
	}
	if got, err := vec.Batch(nil, short); err != nil || len(got) != 0 {
		t.Fatalf("no texts is no call and no vectors: %v %v", got, err)
	}
}
