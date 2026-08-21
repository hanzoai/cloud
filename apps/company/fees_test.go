package company

import (
	"strings"
	"testing"
	"time"
)

// A TARIFF THAT CANNOT NAME THE STATE'S FEE IS NOT A TARIFF. Pricing only our
// half would read as the whole bill, and the payer would meet the rest after the
// filing was already submitted. The states we form in ship with a figure, so the
// refusal is for a jurisdiction nobody has priced.
func TestTariff_RefusesAJurisdictionItCannotPrice(t *testing.T) {
	if _, err := TariffFor(StructureCCorp, Jurisdiction("ZZ"), Options{}); err == nil {
		t.Fatal("priced a formation in a jurisdiction with no known filing fee")
	} else if !strings.Contains(err.Error(), "CLOUD_COMPANY_STATE_FEE_CENTS_ZZ") {
		t.Fatalf("the refusal must name the setting that fixes it, got: %v", err)
	}
}

// Delaware charges differently per entity, so the tariff has to pick by
// structure rather than per state — an LLC quoted at the corporation's fee is
// wrong in the direction that only shows up on the invoice.
func TestTariff_DelawareIsPricedPerStructure(t *testing.T) {
	llc, err := TariffFor(StructureLLC, JurisdictionDE, Options{})
	if err != nil {
		t.Fatal(err)
	}
	corp, err := TariffFor(StructureCCorp, JurisdictionDE, Options{})
	if err != nil {
		t.Fatal(err)
	}
	find := func(q *Tariff) Charge {
		for _, l := range q.Lines {
			if l.Code == "state_filing" {
				return l
			}
		}
		t.Fatal("no state_filing line")
		return Charge{}
	}
	if find(llc).AmountCents == find(corp).AmountCents {
		t.Fatal("DE LLC and C-Corp priced identically — the per-structure fee is not being read")
	}
}

// A SHIPPED FIGURE CARRIES ITS RECEIPT. Without a source and a date nobody can
// tell a checked number from a guessed one.
func TestTariff_PassThroughLinesCarryProvenance(t *testing.T) {
	q, err := TariffFor(StructureLLC, JurisdictionWY, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range q.Lines {
		if l.Code != "state_filing" {
			continue
		}
		if l.Source == "" || l.AsOf == "" {
			t.Fatalf("state fee has no provenance: source=%q asOf=%q", l.Source, l.AsOf)
		}
	}
}

// An override is the deployment's figure, not one we checked. Reporting a source
// for it would launder a local value as a verified one.
func TestTariff_OverrideIsNotPresentedAsVerified(t *testing.T) {
	t.Setenv("CLOUD_COMPANY_STATE_FEE_CENTS_WY", "12345")
	sf, ok := stateFee(StructureLLC, JurisdictionWY)
	if !ok || sf.AmountCents != 12345 {
		t.Fatalf("override not applied: %+v", sf)
	}
	if sf.AsOf != "" || sf.Source != "deployment override" {
		t.Fatalf("an override must not claim a checked source: %+v", sf)
	}
	if sf.Stale(time.Now()) {
		t.Error("an override has no review date and must never be called stale")
	}
}

// Staleness is the whole point of AsOf: a figure nobody has checked inside the
// window says so, rather than looking as fresh as one checked yesterday.
func TestStateFee_StaleWhenOlderThanTheReviewWindow(t *testing.T) {
	now := time.Now()
	fresh := StateFee{AmountCents: 1, Source: "x", AsOf: now.Format("2006-01-02")}
	old := StateFee{AmountCents: 1, Source: "x", AsOf: now.Add(-2 * feeReviewWindow).Format("2006-01-02")}
	if fresh.Stale(now) {
		t.Error("a figure checked today is not stale")
	}
	if !old.Stale(now) {
		t.Error("a figure older than the review window must report stale")
	}
	if !(StateFee{AsOf: "not-a-date"}).Stale(now) {
		t.Error("an unparseable date is not a date anyone checked")
	}
}

// The state's fee is money we collect and remit. A quote that does not mark it
// reads as revenue it is not.
func TestTariff_StateFeeIsMarkedPassThrough(t *testing.T) {
	t.Setenv("CLOUD_COMPANY_STATE_FEE_CENTS_WY", "10000")
	q, err := TariffFor(StructureLLC, JurisdictionWY, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, l := range q.Lines {
		if l.Code == "state_filing" {
			found = true
			if !l.PassThrough {
				t.Error("the state filing fee is not marked pass-through")
			}
			if l.AmountCents != 10000 {
				t.Errorf("state fee = %d, want the configured 10000", l.AmountCents)
			}
		}
	}
	if !found {
		t.Fatal("no state_filing line in the quote")
	}
	if q.DueNowCents != feeCents()+10000 {
		t.Errorf("dueNow = %d, want service+state = %d", q.DueNowCents, feeCents()+10000)
	}
}

// AN AGENT OF RECORD IS A YEARLY OBLIGATION. Folding it into the one-time total
// would have a payer agree to a number that is not what they will pay again next
// year, which is the whole reason the two are separated.
func TestTariff_RecurringIsNotFoldedIntoDueNow(t *testing.T) {
	t.Setenv("CLOUD_COMPANY_STATE_FEE_CENTS_DE", "8900")
	q, err := TariffFor(StructureCCorp, JurisdictionDE, Options{AgentOfRecord: true})
	if err != nil {
		t.Fatal(err)
	}
	if q.RecurringCents != agentFeeCents() || q.Recurring != "yearly" {
		t.Fatalf("recurring = %d/%q, want %d/yearly", q.RecurringCents, q.Recurring, agentFeeCents())
	}
	if q.DueNowCents != feeCents()+8900 {
		t.Errorf("dueNow = %d — the yearly agent fee was folded into the one-time total", q.DueNowCents)
	}
}

// A fee that parses as zero because someone typed a currency symbol would file a
// company for free and look deliberate. Malformed is NOT CONFIGURED — and since
// a checked figure ships for this state, the fallback is that figure rather than
// a refusal: bad config should not take pricing down when a verified number is
// right there.
func TestTariff_MalformedOverrideFallsBackToTheCheckedFigure(t *testing.T) {
	t.Setenv("CLOUD_COMPANY_STATE_FEE_CENTS_DE", "$149")
	sf, ok := stateFee(StructureCCorp, JurisdictionDE)
	if !ok {
		t.Fatal("no fee resolved")
	}
	if sf.AmountCents == 149 || sf.AmountCents == 0 {
		t.Fatalf("a malformed override was parsed: %+v", sf)
	}
	if sf.Source == "deployment override" {
		t.Fatal("a malformed override must not be treated as configured")
	}
}

func TestTariff_ExpeditedEINIsAnOptionalOneTimeLine(t *testing.T) {
	t.Setenv("CLOUD_COMPANY_STATE_FEE_CENTS_WY", "10000")
	base, err := TariffFor(StructureLLC, JurisdictionWY, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fast, err := TariffFor(StructureLLC, JurisdictionWY, Options{ExpeditedEIN: true})
	if err != nil {
		t.Fatal(err)
	}
	if fast.DueNowCents-base.DueNowCents != expeditedEINFeeCents() {
		t.Fatalf("expedited EIN changed dueNow by %d, want %d", fast.DueNowCents-base.DueNowCents, expeditedEINFeeCents())
	}
	if fast.RecurringCents != 0 {
		t.Error("expedited EIN is one-time, not recurring")
	}
}
