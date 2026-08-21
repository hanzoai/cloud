package claim

import (
	"testing"
	"time"
)

// fake is a claim reduced to exactly what the order reads.
type fake struct {
	knowable time.Time
	conf     float64
	digest   string
	rank     int
}

func (f fake) Knowable() time.Time { return f.knowable }
func (f fake) Confidence() float64 { return f.conf }
func (f fake) Digest() string      { return f.digest }
func (f fake) Rank() int           { return f.rank }

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// TestKnowableTakesTheLater is the leakage guard. A filer that back-dates `seen`
// must not make a claim look knowable before this plane held it.
func TestKnowableTakesTheLater(t *testing.T) {
	wrote := epoch
	backdated := epoch.Add(-365 * 24 * time.Hour)
	if got := Knowable(backdated, wrote); !got.Equal(wrote) {
		t.Fatalf("a back-dated seen decided knowability: got %v, want the write instant %v", got, wrote)
	}
	live := epoch.Add(-time.Minute)
	if got := Knowable(live, wrote); !got.Equal(wrote) {
		t.Fatalf("live pipeline: got %v, want %v", got, wrote)
	}
	// A claim the world knew later than we wrote it is knowable then, not before.
	future := epoch.Add(time.Hour)
	if got := Knowable(future, wrote); !got.Equal(future) {
		t.Fatalf("got %v, want the later claim instant %v", got, future)
	}
}

// TestDigestIsIdempotentAndUnforgeable pins both properties the content address
// carries: redelivery is one row, and no field value can impersonate a boundary.
func TestDigestIsIdempotentAndUnforgeable(t *testing.T) {
	if Digest("a", "b") != Digest("a", "b") {
		t.Fatal("the same claim digested twice differed; redelivery would append")
	}
	// Without length-prefixing, ("ab","c") and ("a","bc") collide. They must not.
	if Digest("ab", "c") == Digest("a", "bc") {
		t.Fatal("a field value impersonated a field boundary; two distinct claims collide")
	}
	if Digest("a", "") == Digest("a") {
		t.Fatal("an empty field vanished from the address")
	}
}

func TestWindowMaturesAndObserves(t *testing.T) {
	w := Window{Now: epoch, Horizon: 48 * time.Hour}
	if w.Matured(epoch.Add(-24 * time.Hour)) {
		t.Error("an event inside the horizon reported as matured")
	}
	if !w.Matured(epoch.Add(-72 * time.Hour)) {
		t.Error("an event past the horizon reported as unmatured")
	}
	at := epoch.Add(-72 * time.Hour)
	if got := w.AsOf(at); !got.Equal(at.Add(48 * time.Hour)) {
		t.Errorf("as-of = %v, want the instant the horizon closes", got)
	}
}

// TestOrderIsTotalAndInThatOrder walks the four terms, each in isolation, in the
// order they apply. A term that stops deciding is a training set that stops
// being reproducible.
func TestOrderIsTotalAndInThatOrder(t *testing.T) {
	base := fake{knowable: epoch, conf: 0.5, digest: "m", rank: 5}

	strong := base
	strong.rank = 1
	strong.conf = 0 // a weak confidence must not lift the weak source
	if !Stronger(strong, base) {
		t.Error("rank did not decide, or confidence overrode it")
	}
	later := base
	later.knowable = epoch.Add(time.Hour)
	later.conf = 0
	if !Stronger(later, base) {
		t.Error("within one rank, the later-knowable claim did not win")
	}
	surer := base
	surer.conf = 0.9
	if !Stronger(surer, base) {
		t.Error("within one rank and instant, higher confidence did not win")
	}
	lower := base
	lower.digest = "a"
	if !Stronger(lower, base) {
		t.Error("the digest did not break the final tie deterministically")
	}
	if Stronger(base, base) {
		t.Error("the order is not irreflexive; a claim outranked itself")
	}
}

// TestResolveKeepsTheLosers is the property that separates this from last-write-
// wins: a contested set stays visible as contested.
func TestResolveKeepsTheLosers(t *testing.T) {
	agrees := func(a, b fake) bool { return a.digest[:1] == b.digest[:1] }

	win := fake{knowable: epoch, digest: "yes", rank: 1}
	lose := fake{knowable: epoch, digest: "no", rank: 9}
	got, conflicts, contested, ok := Resolve([]fake{lose, win}, epoch, agrees)
	if !ok {
		t.Fatal("nothing resolved from two knowable claims")
	}
	if got.digest != "yes" {
		t.Errorf("winner = %q, want the higher-ranked claim", got.digest)
	}
	if len(conflicts) != 1 || conflicts[0].digest != "no" {
		t.Errorf("the dissenter was discarded: %v", conflicts)
	}
	if !contested {
		t.Error("two claims that disagree did not report as contested")
	}

	// Agreement is not conflict: the loser is still returned, but the set is settled.
	same := fake{knowable: epoch, digest: "yellow", rank: 9}
	_, _, contested, _ = Resolve([]fake{same, win}, epoch, agrees)
	if contested {
		t.Error("two claims that agree reported as contested")
	}
}

// TestNothingKnowableIsNotAnError: silence is an honest answer, not a failure.
func TestNothingKnowableIsNotAnError(t *testing.T) {
	future := fake{knowable: epoch.Add(time.Hour), digest: "x"}
	if _, _, _, ok := Resolve([]fake{future}, epoch, func(a, b fake) bool { return true }); ok {
		t.Fatal("a claim not yet knowable resolved; the leakage guard is decorative")
	}
}
