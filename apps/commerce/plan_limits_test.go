// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// plan_limits_test.go — a plan bought is the plan the model gate serves.
//
// The tier and the limiter read one subscription two ways: the tier from
// commerce's tier op, the limiter (apps/ai/limits) from that op's served rung and
// the subscriptions op's settlement of the row. Each is tested alone against a
// fake of the other, so nothing proved that a real sale reads as the same plan on
// both. These drive the real money core and read the plan back through the
// limiter's own readers.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/account"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
)

// served is the limiter over this process's commerce ops: the rung and row the
// tier op serves, the rows the subscriptions op settles, and an empty wallet.
func served(t *testing.T) *limits.Limiter {
	t.Helper()
	l, err := limits.Open(t.TempDir(),
		func(ctx context.Context, subject, _ string) (string, string, error) {
			out, err := planeTier(ctx, &client.TierIn{Subject: subject})
			if err != nil {
				return "", "", err
			}
			return strings.ToLower(strings.TrimSpace(out.Plan)), strings.TrimSpace(out.Subscription), nil
		},
		func(ctx context.Context, subject, _ string) ([]client.Subscription, error) {
			out, err := planeSubscriptions(ctx, &client.SubsIn{UserID: subject})
			if err != nil {
				return nil, err
			}
			return out.Rows, nil
		},
		nil,
		func(context.Context, string, string) (int64, int64, error) { return 0, 0, nil },
		carried)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestASquareSaleOfEveryRungIsTheLimitersPlan buys every rung of the ladder —
// Go $8 to Unlimited $995 (@hanzo/plans 1.10.1) — with a card on Square's sandbox,
// as an org's own account, and Go as a person in the signup org too; reads the
// tier and the limiter, which serve the rung with its shipped usage; and reads that the
// purchase armed the default reload ($50 below $10): commerce v1.50.251 (6b090980)
// restores DefaultReload on every paid plan whose row does not switch it off.
//
// Each buyer is named for this test alone: the sandbox merchant holds the
// customers of every org it serves, and a sale must open its own.
func TestASquareSaleOfEveryRungIsTheLimitersPlan(t *testing.T) {
	type buy struct{ name, org, user, slug string }
	buys := []buy{{"go as a signup person", "hanzo", fresh("sam"), "go"}}
	for _, slug := range []string{"go", "pro-3x", "dev-5x", "max-15x", "ultra", "ultimate", "unlimited"} {
		buys = append(buys, buy{slug + " as an org account", fresh("acme-" + slug), "ada", slug})
	}
	for _, tc := range buys {
		t.Run(tc.name, func(t *testing.T) {
			pl := servePlane(t)
			// Resolved from this boot, born in test mode, rather than from an org
			// another test left in the resolver's cache.
			commerceorg.Invalidate(tc.org)
			buyer := zip.WithCaller(context.Background(), zip.Caller{Org: tc.org, User: tc.user, Name: tc.user})
			payer := principalWallet(tc.org, tc.user)
			p, ok, err := tier(context.Background(), tc.slug)
			if err != nil || !ok || p.Price <= 0 {
				t.Fatalf("the plan authority must sell %s: ok=%v err=%v", tc.slug, ok, err)
			}
			price := int64(p.Price)

			sold, err := planeSubscribe(buyer, &client.SaleIn{
				SourceID: "cnon:card-nonce-ok", PlanID: tc.slug, Subject: payer,
				Currency: "usd", IdempotencyKey: fresh(tc.slug),
			})
			if err != nil || sold.Sale == nil || sold.Sale.AmountCents != price {
				t.Fatalf("sale = %+v, %v; want %s at its list %d", sold, err, tc.slug, price)
			}
			pl.sq.soldOnce(t, tc.org, sold.Sale.InvoiceID, price, pl.began)
			if sold.Sale.Reload == nil || sold.Sale.Reload.State != "armed" {
				t.Fatalf("the %s sale answered reload %+v; want DefaultReload armed (commerce v1.50.251)", tc.slug, sold.Sale.Reload)
			}
			got, err := planeTier(buyer, &client.TierIn{Subject: payer})
			if err != nil || got.Tier.Name != "pro" || got.Plan != tc.slug {
				t.Fatalf("tier = %+v, %v; want pro serving %s", got, err, tc.slug)
			}
			l := served(t)
			st, err := l.Read(buyer, payer, tc.org)
			if err != nil {
				t.Fatal(err)
			}
			if st.Plan != tc.slug {
				t.Fatalf("the limiter serves %q for a paid %s; want %s", st.Plan, tc.slug, tc.slug)
			}
			whole(t, tc.slug, st)
			// A priced premium call with an empty wallet is the plan's to pay.
			g, hit, err := l.Admit(buyer, limits.Ask{Subject: payer, Org: tc.org, Model: "openai/gpt-5.4-mini",
				Class: limits.ClassPremium, Priced: true, Spend: true})
			if err != nil || hit != nil || g == nil || g.Pays != limits.PaysPlan || g.Plan != tc.slug {
				t.Fatalf("admit = %+v, hit %+v, err %v; want the plan to pay", g, hit, err)
			}
		})
	}
}

// whole fails unless the standing carries slug's full seeded figures: a paid
// period at list price includes all of them, and a rung that includes all of a
// class (flags.Unbounded) is read as unbounded in it.
func whole(t *testing.T, slug string, st limits.Standing) {
	t.Helper()
	seed := flags.CarriedPlans[slug]
	if st.Session.Limit != int64(seed.Session) || st.Day.Limit != int64(seed.Day) {
		t.Errorf("windows = session %d, day %d; want %d, %d", st.Session.Limit, st.Day.Limit, seed.Session, seed.Day)
	}
	for class, cents := range map[string]int{limits.ClassPremium: seed.Premium, limits.ClassOurs: seed.Ours} {
		if cents == flags.Unbounded {
			if !st.Classes[class].Unbounded {
				t.Errorf("%s on %s is bounded; the rung includes all of it", class, slug)
			}
			continue
		}
		if got, want := st.Classes[class].Period.Limit, int64(cents)*10_000_000; got != want {
			t.Errorf("%s includes %d nanos; want %d", class, got, want)
		}
	}
}

// TestARecordedSquarePaymentOfMax15xIsTheLimitersPlan records a Max (max-15x) paid
// through a Square invoice or payment link, as a SuperAdmin does at
// POST /v1/admin/customers/:org/plan, and asks the limiter which plan it serves.
func TestARecordedSquarePaymentOfMax15xIsTheLimitersPlan(t *testing.T) {
	bootRecord(t)
	const org = "acme-recorded"
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	in := &client.RecordPlanIn{
		Subject:     org,
		PlanID:      "max-15x",
		PriceCents:  9500,
		PeriodStart: start.Format(time.RFC3339),
		PeriodEnd:   start.AddDate(0, 1, 0).Format(time.RFC3339),
		Processor:   "square",
		Reference:   client.PaymentRef{Payment: "sqpay_recorded"},
	}
	if _, err := planeSubscriptionRecord(caller(org, true), in); err != nil {
		t.Fatalf("record: %v", err)
	}
	reader := caller(org, false)
	st, err := served(t).Read(reader, org, org)
	if err != nil {
		t.Fatal(err)
	}
	if st.Plan != "max-15x" {
		t.Fatalf("the limiter serves %q for a recorded Max; want max-15x", st.Plan)
	}
	whole(t, "max-15x", st)
}

// principalWallet is the wallet a signed-in member pays from.
func principalWallet(org, user string) string {
	return account.Payer(account.Credential{Owner: org, Name: user}).Subject()
}

// carried is the flag plane as the usage policy's one migration left it: every
// carried plan's figures saved, and nothing else set.
func carried(_ context.Context, _ string, in *client.FlagValueIn) (*client.FlagValue, error) {
	v, ok := flags.CarriedValues()[in.Key]
	if !ok {
		return &client.FlagValue{Value: in.Default}, nil
	}
	return &client.FlagValue{Value: v, Set: true}, nil
}
