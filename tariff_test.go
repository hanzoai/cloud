package cloud

// Tests for the rate card. They drive the real functions with no commerce beside
// them, which is the state every floor is written for: an unreachable authority
// charges exactly what was charged yesterday.
//
// Each test names the MUTATION that makes it fail, because a money test that
// passes against a wrong price is worse than no test.

import (
	"context"
	"testing"
)

// rateOf finds one line of the card by name, or fails.
func rateOf(t *testing.T, card Card, name string) Rate {
	t.Helper()
	for _, r := range card.Rates {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("the card publishes no rate named %q", name)
	return Rate{}
}

// TestAnHourOfASessionsComputerCostsSixHundredAndSixtySixHundredthsOfACent reads
// the two compute floors through the arithmetic a customer does.
//
// A session's computer is 1 vCPU and 1 GiB, so an hour of it running is the two
// rates added: $0.0504 + $0.0162 = $0.0666. That sum is the published claim, and
// it is the one number a reader can check against their own invoice.
//
// MUTATION: write VCPUHourMicros as 5_040 (a plausible slip — $0.0504 read as a
// rate in cents) and this fails naming both halves and the total.
func TestAnHourOfASessionsComputerCostsSixHundredAndSixtySixHundredthsOfACent(t *testing.T) {
	const want int64 = 66_600
	if got := VCPUHourMicros + MemoryHourMicros; got != want {
		t.Fatalf("an hour of 1 vCPU + 1 GiB = %d µ$ (%d + %d), want %d µ$ ($0.0666)",
			got, VCPUHourMicros, MemoryHourMicros, want)
	}
}

// TestASecondOfAVCPUIsAWholeNumberOfMicros holds the compute rates to the unit
// they are charged in.
//
// The hour is what is stored and the SECOND is what is billed — RuntimeCost
// prices a span as rate × secs / 3600 — so an hourly rate that is not a whole
// number of micro-USD per second rounds on every tick. A vCPU-second is exactly
// 14 µ$; memory is deliberately not held to the same bar, because $0.0000045 a
// GiB-second is the published price and half a micro-USD is what RuntimeCost's
// half-up rounding exists to carry.
//
// MUTATION: change VCPUHourMicros to 50_000 and the second becomes 13.89 µ$,
// which no tick can charge exactly.
func TestASecondOfAVCPUIsAWholeNumberOfMicros(t *testing.T) {
	if VCPUHourMicros%secsPerHour != 0 {
		t.Fatalf("a vCPU-second is %v µ$, which is not a whole number: %d µ$/hour does not divide by %d",
			float64(VCPUHourMicros)/float64(secsPerHour), VCPUHourMicros, secsPerHour)
	}
	if got := VCPUHourMicros / secsPerHour; got != 14 {
		t.Fatalf("a vCPU-second is %d µ$, want 14 µ$ ($0.000014)", got)
	}
	// RuntimeCost is what actually charges the span, so the claim is read through
	// it rather than asserted about the constant alone.
	if got := RuntimeCost(VCPUHourMicros, 1); got != 14 {
		t.Fatalf("RuntimeCost charges %d µ$ for a vCPU-second, want 14 µ$", got)
	}
	if got := RuntimeCost(MemoryHourMicros, 2); got != 9 {
		t.Fatalf("RuntimeCost charges %d µ$ for two GiB-seconds, want 9 µ$ (4.5 each)", got)
	}
}

// TestAWebCallCostsLessThanACent is why the card is in micro-USD at all.
//
// Both web rates are under a cent, so a card kept in cents could not state either
// of them — it would round web_search to zero or to a cent, which is a fifth of
// the price or five times it. That is the same argument budget.go makes for the
// unit and it is worth a test, because the tempting simplification is to reuse
// the cent-denominated fee path the tools are metered through today.
//
// MUTATION: state either rate in cents (2 and 1) and this fails.
func TestAWebCallCostsLessThanACent(t *testing.T) {
	const cent int64 = 10_000
	for _, tc := range []struct {
		name  string
		rate  int64
		want  int64
		human string
	}{
		{"web_search", SearchCallMicros, 2_158, "$0.002158"},
		{"web_fetch", FetchCallMicros, 1_079, "$0.001079"},
	} {
		if tc.rate != tc.want {
			t.Errorf("%s is %d µ$, want %d µ$ (%s)", tc.name, tc.rate, tc.want, tc.human)
		}
		if tc.rate >= cent {
			t.Errorf("%s is %d µ$, which is a cent or more — the cent-denominated fee path would hold it",
				tc.name, tc.rate)
		}
	}
}

// TestTheCardPublishesTheFloorWhenNobodyHasPricedIt reads the card the way a
// customer does, with no authority beside it.
//
// This is the ordinary state of a fresh deployment and of any process with no
// commerce in it, and the answer must be the compiled floor rather than zero — a
// price list that reads zero because a lookup failed is a price list that gives
// the product away.
//
// MUTATION: make Rates return the ask's answer without a floor and every row
// here reads 0.
func TestTheCardPublishesTheFloorWhenNobodyHasPricedIt(t *testing.T) {
	card := Current(context.Background())
	for _, tc := range []struct {
		name string
		want int64
	}{
		{"computer.vcpu", VCPUHourMicros},
		{"computer.memory", MemoryHourMicros},
		{"web_search", SearchCallMicros},
		{"web_fetch", FetchCallMicros},
	} {
		if got := rateOf(t, card, tc.name).Micros; got != tc.want {
			t.Errorf("%s publishes %d µ$ with no authority beside it, want the floor %d µ$", tc.name, got, tc.want)
		}
	}
}

// TestNothingIsChargedForAComputerThatIsNotRunning holds the four structural
// zeros at zero.
//
// A paused computer, a computer's creation, the interfaces and a seat are zero
// because that is the product — "nothing at all for a machine that is sitting
// still" — not because nobody has priced them. So none of them is resolved
// through the authority, and this reads all four together because the claim is
// about the set.
//
// MUTATION: route any of the four through RateMicros with a non-zero floor and
// this fails naming it.
func TestNothingIsChargedForAComputerThatIsNotRunning(t *testing.T) {
	card := Current(context.Background())
	for _, name := range []string{"computer.paused", "computer.created", "interfaces", "seats"} {
		if got := rateOf(t, card, name).Micros; got != 0 {
			t.Errorf("%s costs %d µ$, want nothing", name, got)
		}
	}
}

// TestEveryRateSaysWhatItIsPerAndWhichComponentBillsIt reads the card for holes.
//
// A number with no unit cannot be checked against an invoice, and a line that
// names no component cannot be found on the spend breakdown — which is the whole
// promise the card makes. Both are the kind of gap a new row acquires silently,
// so the test is over every row rather than over the ones that exist today.
//
// MUTATION: drop Per or Component from any row and this fails naming it.
func TestEveryRateSaysWhatItIsPerAndWhichComponentBillsIt(t *testing.T) {
	byName := map[string]bool{}
	for _, c := range Components() {
		byName[c.Name] = true
	}
	for _, r := range Current(context.Background()).Rates {
		switch {
		case r.Name == "":
			t.Error("a rate line carries no name")
		case r.Title == "":
			t.Errorf("%s reads with no title", r.Name)
		case r.Per == "":
			t.Errorf("%s is %d µ$ per nothing — a number with no unit", r.Name, r.Micros)
		case r.Note == "":
			t.Errorf("%s says nothing about what it covers", r.Name)
		case !byName[r.Component]:
			t.Errorf("%s bills under %q, which is not one of the four components", r.Name, r.Component)
		case r.Micros < 0:
			t.Errorf("%s is %d µ$ — a negative price", r.Name, r.Micros)
		}
	}
}

// TestABillIsMadeOfFourThings holds the components to the four an invoice names.
//
// The set is the contract: the spend breakdown sums per component, so a fifth one
// appearing here without a line on the breakdown — or one of these four going
// missing — is money attributed to a name nothing reports.
//
// MUTATION: add a component, or rename one, and this fails.
func TestABillIsMadeOfFourThings(t *testing.T) {
	want := []string{ComponentModel, ComponentComputer, ComponentWeb, ComponentMedia}
	got := Components()
	if len(got) != len(want) {
		t.Fatalf("a bill is made of %d components, want %d", len(got), len(want))
	}
	for i, c := range got {
		if c.Name != want[i] {
			t.Errorf("component %d is %q, want %q", i, c.Name, want[i])
		}
		if c.Title == "" || c.Basis == "" || c.Note == "" {
			t.Errorf("%s is published with nothing said about it", c.Name)
		}
	}
	// Two are quoted before they run and two are measured from what they used.
	// The split is the reason a budget can refuse a completion and cannot refuse
	// a render, so it is read here rather than left to prose.
	quoted := map[string]bool{}
	for _, c := range got {
		quoted[c.Name] = c.Quoted
	}
	if !quoted[ComponentModel] || !quoted[ComponentComputer] {
		t.Error("a completion and a second of compute are both boundable before they run, so both are quoted")
	}
	if quoted[ComponentWeb] || quoted[ComponentMedia] {
		t.Error("a web call and a render are priced from what they cost, so neither can be quoted")
	}
}

// TestTheFiveTokenTiersAreDisjoint reads the tier list for the property its name
// claims.
//
// Five DISJOINT tiers means a token is counted once. A repeated tier would be a
// token counted twice, which is the one arithmetic error a customer can see on
// their own invoice and cannot argue with.
//
// MUTATION: publish TierIn twice, or drop one, and this fails.
func TestTheFiveTokenTiersAreDisjoint(t *testing.T) {
	var tiers []string
	for _, c := range Components() {
		if c.Name == ComponentModel {
			tiers = c.Tiers
		}
	}
	want := []string{TierIn, TierCacheRead, TierCacheWrite, TierOut, TierReasoning}
	if len(tiers) != len(want) {
		t.Fatalf("model inference meters in %d tiers, want %d", len(tiers), len(want))
	}
	seen := map[string]bool{}
	for i, tier := range tiers {
		if seen[tier] {
			t.Errorf("%q is published twice — the tiers are not disjoint", tier)
		}
		seen[tier] = true
		if tier != want[i] {
			t.Errorf("tier %d is %q, want %q", i, tier, want[i])
		}
	}
	// Only model inference has tiers: a per-call or per-second rate metered in
	// tiers would be a rate nobody could add up.
	for _, c := range Components() {
		if c.Name != ComponentModel && len(c.Tiers) > 0 {
			t.Errorf("%s publishes tiers, and only model inference has any", c.Name)
		}
	}
}

// TestTheWindowsRunDearestFirst holds the one published fact about the windows.
//
// What each window costs is per model and comes from commerce; what this package
// states is the ORDER — immediate over priority over loose — because that is what
// makes the window a lever a caller can pull. An order that read the other way
// would tell every caller to ask for the interactive tariff.
//
// MUTATION: swap two windows, or give two the same rank, and this fails.
func TestTheWindowsRunDearestFirst(t *testing.T) {
	want := []string{SpeedImmediate, SpeedPriority, SpeedLoose}
	got := Speeds()
	if len(got) != len(want) {
		t.Fatalf("%d completion windows are published, want %d", len(got), len(want))
	}
	for i, w := range got {
		if w.Name != want[i] {
			t.Errorf("window %d is %q, want %q", i, w.Name, want[i])
		}
		if w.Rank != i+1 {
			t.Errorf("%s ranks %d, want %d — the list runs dearest first", w.Name, w.Rank, i+1)
		}
		if w.Note == "" {
			t.Errorf("%s is published with nothing said about it", w.Name)
		}
	}
}

// TestTheCardStatesItsUnitOnce reads the one thing that makes every number on it
// legible.
//
// Every amount is micro-USD and the card says so once, at the top, rather than
// suffixing eight fields. A reader that has to infer the unit from a field name
// will eventually infer cents, which is a hundredfold error in the direction
// nobody notices until the invoice.
//
// MUTATION: change the unit string and this fails.
func TestTheCardStatesItsUnitOnce(t *testing.T) {
	if got := Current(context.Background()).Unit; got != "micro_usd" {
		t.Fatalf("the card is stated in %q, want micro_usd", got)
	}
}
