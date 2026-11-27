// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"fmt"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/money"
)

// A cap scoped to one service measures that service's spend; an org-wide cap
// measures all of it. Served through the real commerce embed, the real finance
// ledger, and the period-spend reader Use wires between them.
func TestServiceCapReadsOnlyItsService(t *testing.T) {
	fin := bootRecord(t)
	ctx := context.Background()
	const org = "scope-cap"

	if _, err := fin.Deposit(ctx, money.DepositInput{Org: org, Subject: org, Amount: money.FromCents(50_000), Ref: "grant"}); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	// $5.00 on ask, $5.00 on ai: the org spent $10.00, ask spent $5.00.
	for i, d := range []struct {
		service string
		cents   int64
	}{{"ask", 300}, {"ai", 500}, {"ask", 200}} {
		if err := fin.RecordUsage(ctx, money.UsageInput{
			Org: org, Subject: org, Amount: money.FromCents(d.cents), Service: d.service, Ref: fmt.Sprintf("use-%d", i),
		}); err != nil {
			t.Fatalf("usage %d on %s: %v", i, d.service, err)
		}
	}

	enforce := true
	ask, err := planeAlertRaise(caller(org, false), &client.AlertSpec{
		Subject: org, Title: "ask", Threshold: 600, Service: "ask", Enforce: &enforce,
	})
	if err != nil {
		t.Fatalf("raise ask cap: %v", err)
	}
	whole, err := planeAlertRaise(caller(org, false), &client.AlertSpec{
		Subject: org, Title: "org", Threshold: 1_000_000, Enforce: &enforce,
	})
	if err != nil {
		t.Fatalf("raise org cap: %v", err)
	}

	rows, err := planeAlerts(caller(org, false), &client.SubjectIn{Subject: org})
	if err != nil {
		t.Fatalf("list caps: %v", err)
	}
	spent := map[string]int64{}
	for _, r := range rows.Rows {
		if r.PeriodSpentCents == nil {
			t.Fatalf("cap %s (%q) reports no period spend", r.ID, r.Service)
		}
		spent[r.ID] = *r.PeriodSpentCents
	}
	if got := spent[ask.ID]; got != 500 {
		t.Errorf("ask cap periodSpentCents = %d, want 500 (ask's spend only, not the org's 1000)", got)
	}
	if got := spent[whole.ID]; got != 1000 {
		t.Errorf("org-wide cap periodSpentCents = %d, want 1000 (every service)", got)
	}

	// 500 + 50 fits under the $6.00 ask cap. Measured against the org's 1000 it would not.
	v, err := planeCapAuthorize(caller(org, false), &client.CapIn{Service: "ask", Amount: 50})
	if err != nil {
		t.Fatalf("authorize ask 50: %v", err)
	}
	if !v.Allow {
		t.Errorf("ask request of 50 = %+v, want allow (500+50 <= 600)", *v)
	}

	// 500 + 150 does not fit, and the denial names the ask cap and ask's spend.
	v, err = planeCapAuthorize(caller(org, false), &client.CapIn{Service: "ask", Amount: 150})
	if err != nil {
		t.Fatalf("authorize ask 150: %v", err)
	}
	if v.Allow || v.CapCents != 600 || v.SpentCents != 500 {
		t.Errorf("ask request of 150 = %+v, want deny cap=600 spent=500", *v)
	}

	// The ask cap does not cover ai; only the org-wide cap does, and it has room.
	v, err = planeCapAuthorize(caller(org, false), &client.CapIn{Service: "ai", Amount: 150})
	if err != nil {
		t.Fatalf("authorize ai 150: %v", err)
	}
	if !v.Allow {
		t.Errorf("ai request of 150 = %+v, want allow (the ask cap does not cover ai)", *v)
	}
}
