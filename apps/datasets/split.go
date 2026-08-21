package datasets

// split.go is every decision a materialisation makes that is a pure function of
// its inputs: which subject a row belongs to, what that row is called, which
// split it lands in, how much of an oversized window is admitted, and the digest
// that fingerprints the result.
//
// It is pure ON PURPOSE. These are the only parts of a materialisation that
// could have been pushed into the store, and pushing them there would have put
// the definition of a split in SQL where no test can reach it — which is exactly
// how a plane ends up with two definitions of one thing and no way to notice they
// disagree. The store selects and bounds; this file decides.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"sort"
	"time"
)

// The three splits. UInt8 on the wire and in the store, because a split is an
// enumeration of three and a string per row is a string per row.
const (
	train uint8 = 0
	val   uint8 = 1
	test  uint8 = 2
)

// fact is one bucket of one subject, exactly as the source surface presents it.
type fact struct {
	// Kind and Subject name WHOSE bucket this is. Together they are the subject
	// identity — the same string can be a session id for one kind and an account
	// id for another, so neither half identifies alone.
	Kind    string
	Subject string
	// At is the bucket's instant.
	At time.Time
	// Point is the coordinates, in the spec's dim order.
	Point []float64
}

// row is one materialised dataset row: a fact, plus the two things the
// materialisation decides about it.
type row struct {
	fact
	// ID names this row forever. It is derived, not allocated, so two
	// materialisations of the same fact agree on it without coordinating.
	ID string
	// Split is 0 train, 1 val, 2 test.
	Split uint8
}

// subject is the identity a split is coherent over: the kind and the subject
// together, separated by a byte neither can contain.
func (f fact) subject() string { return f.Kind + "\x00" + f.Subject }

// id names a row from what the row IS. A materialisation that reproduces the
// same facts reproduces the same ids, so the digest below compares like with
// like across two runs, two processes and two years.
func (f fact) id() string {
	h := sha256.New()
	h.Write([]byte(f.Kind))
	h.Write([]byte{0})
	h.Write([]byte(f.Subject))
	h.Write([]byte{0})
	_ = binary.Write(h, binary.BigEndian, f.At.UTC().Unix())
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// assign is where one instant falls against the cuts.
func assign(c [2]time.Time, at time.Time) uint8 {
	switch {
	case at.Before(c[0]):
		return train
	case at.Before(c[1]):
		return val
	default:
		return test
	}
}

// cut turns facts into rows: it assigns every row of one subject to ONE split,
// decided by that subject's EARLIEST admitted instant.
//
// THE SPLIT IS TEMPORAL AND THEN GROUPED BY SUBJECT, and both halves are
// load-bearing.
//
// Temporal, because a model is asked to predict the future from the past, and a
// split that interleaves time asks it to predict the past from the future — an
// offline score that production will not reproduce.
//
// Grouped by subject, because the same device, card, address or account
// appearing on both sides of the line lets the model memorise the entity instead
// of the behaviour. It scores well on the test split for the one reason that
// makes the number worthless.
//
// A subject whose activity straddles a cut goes to the split of its FIRST
// instant, never its last. First-seen is the direction that cannot leak: a
// subject introduced during training may keep appearing, whereas moving it
// forward would put its training-window behaviour inside the test set.
//
// The output is ordered by (id) so that everything downstream — the digest, the
// insert, the export — sees one order, and no caller can make two runs differ by
// asking in a different sequence.
func cut(facts []fact, c [2]time.Time) []row {
	first := make(map[string]time.Time, len(facts))
	for _, f := range facts {
		s := f.subject()
		if t, seen := first[s]; !seen || f.At.Before(t) {
			first[s] = f.At
		}
	}
	out := make([]row, 0, len(facts))
	for _, f := range facts {
		out = append(out, row{fact: f, ID: f.id(), Split: assign(c, first[f.subject()])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// coherent reports the number of subjects that landed in more than one split. It
// is zero by construction of [cut]; it exists so a test asserts the property
// rather than the implementation, and so the property survives a rewrite.
func coherent(rows []row) int {
	where := make(map[string]uint8, len(rows))
	broken := map[string]bool{}
	for _, r := range rows {
		s := r.subject()
		if w, seen := where[s]; seen && w != r.Split {
			broken[s] = true
			continue
		}
		where[s] = r.Split
	}
	return len(broken)
}

// shareDenominator is the resolution of the membership sample. A thousand buckets
// makes the smallest expressible share a tenth of a percent, which is finer than
// any window this plane will carry needs.
const shareDenominator = 1000

// share is how much of a window's subjects are admitted, in thousandths.
//
// It is derived from what the window actually holds — total rows and total
// subjects — so a tenant with a small window gets all of it and a tenant with a
// huge one gets a UNIFORM sample of it rather than whichever rows the store
// returned first. Alphabetical truncation would be a sample too, and a biased
// one: it would silently omit every subject whose id sorts late.
//
// It rounds UP, and the row cap is still applied as a hard backstop, because a
// share that rounds down is a dataset quietly smaller than the cap it was allowed.
func share(rows, budget int) int {
	if rows <= 0 || budget <= 0 || rows <= budget {
		return shareDenominator
	}
	s := (budget*shareDenominator + rows - 1) / rows
	if s < 1 {
		return 1
	}
	return s
}

// trim drops the trailing partial subject from a read that hit its LIMIT.
//
// A truncated read cuts mid-subject, and half a subject on one side of a split is
// the entity leak [cut] exists to prevent. Dropping the whole trailing subject
// costs at most one subject and keeps the invariant exact. Facts must arrive in
// the read's own (kind, subject, bucket) order for this to be the trailing one,
// which is why the statement pins that order.
func trim(facts []fact, hitLimit bool) []fact {
	if !hitLimit || len(facts) == 0 {
		return facts
	}
	last := facts[len(facts)-1].subject()
	i := len(facts)
	for i > 0 && facts[i-1].subject() == last {
		i--
	}
	return facts[:i]
}

// digest fingerprints a materialised version: the question and the answer
// together.
//
// It covers the canonical spec, the version number and every row's id, split,
// subject and coordinates — so two materialisations of one spec either agree on
// it or the plane says they do not. A digest over the rows alone would let the
// same bytes be claimed for a different question; a digest over the spec alone
// would let the same question be claimed for different bytes.
//
// Coordinates are hashed as their IEEE-754 bits rather than as text: a decimal
// rendering is a lossy function of a float64, so two runs holding the same number
// could print it differently and disagree about bytes they in fact share.
func digest(s spec, version int, rows []row) string {
	h := sha256.New()
	h.Write([]byte("hanzo.dataset\x00"))
	h.Write([]byte(s.canon()))
	h.Write([]byte{0})
	_ = binary.Write(h, binary.BigEndian, uint32(version))
	_ = binary.Write(h, binary.BigEndian, uint64(len(rows)))
	for _, d := range s.Dims {
		h.Write([]byte(d))
		h.Write([]byte{0})
	}
	for _, r := range rows {
		h.Write([]byte(r.ID))
		h.Write([]byte{r.Split, 0})
		h.Write([]byte(r.Kind))
		h.Write([]byte{0})
		h.Write([]byte(r.Subject))
		h.Write([]byte{0})
		_ = binary.Write(h, binary.BigEndian, r.At.UTC().Unix())
		_ = binary.Write(h, binary.BigEndian, uint32(len(r.Point)))
		for _, v := range r.Point {
			_ = binary.Write(h, binary.BigEndian, math.Float64bits(v))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// counts is how a version's rows fall across the splits, and how much of it is
// judged. Reported as its own value because it is what decides whether the
// dataset can train anything at all.
type counts struct {
	Rows, Train, Val, Test           int
	Subjects                         int
	Judged, Productive, Unproductive int
}

// count reduces rows to their counts. Judged is zero for every row this plane
// writes: a disposition arrives from the label plane, and reporting an unjudged
// dataset as judged-with-no-positives would read as a tenant with no fraud.
func count(rows []row) counts {
	var c counts
	c.Rows = len(rows)
	subjects := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		subjects[r.subject()] = struct{}{}
		switch r.Split {
		case train:
			c.Train++
		case val:
			c.Val++
		default:
			c.Test++
		}
	}
	c.Subjects = len(subjects)
	return c
}
