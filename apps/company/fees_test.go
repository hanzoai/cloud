package company

import (
	"strings"
	"testing"
)

// A QUOTE THAT CANNOT NAME THE STATE'S FEE IS NOT A QUOTE. Returning only our
// half would read as the whole bill, and the payer would meet the rest after the
// filing was already submitted.
func TestTariff_RefusesWhenTheStateFeeIsUnknown(t *testing.T) {
	t.Setenv("CLOUD_COMPANY_STATE_FEE_CENTS_DE", "")
	if _, err := TariffFor(StructureCCorp, JurisdictionDE, Options{}); err == nil {
		t.Fatal("quoted a formation whose state filing fee is not configured")
	} else if !strings.Contains(err.Error(), "CLOUD_COMPANY_STATE_FEE_CENTS_DE") {
		t.Fatalf("the refusal must name the setting that fixes it, got: %v", err)
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

// A fee that parses as zero because someone typed a currency symbol would form a
// company for free and look deliberate. Malformed is NOT CONFIGURED.
func TestTariff_MalformedFeeIsNotConfigured(t *testing.T) {
	t.Setenv("CLOUD_COMPANY_STATE_FEE_CENTS_DE", "$149")
	if _, err := TariffFor(StructureCCorp, JurisdictionDE, Options{}); err == nil {
		t.Fatal("a malformed state fee was accepted; it must read as not configured")
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
