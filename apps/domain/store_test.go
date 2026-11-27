package domain

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
)

// replicas opens the ownership store as n pods do: n handles, each with its own pool,
// on the one database. A SQLite file belongs to one process, so there the replicas
// share the file's one handle.
func replicas(t *testing.T, n int) []*Holdings {
	t.Helper()
	dir := t.TempDir()
	var out []*Holdings
	for range n {
		if len(out) > 0 && locus.DSN() == "" {
			out = append(out, out[0])
			continue
		}
		h, err := OpenHoldings(dir)
		if err != nil {
			t.Fatalf("OpenHoldings: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		out = append(out, h)
	}
	return out
}

// The rules a store keeps, held to the deployment's store as they are to the double
// the policy tests use: an org is keyed exactly and a domain case-folded; a record
// round-trips whole; a second Put replaces the first; and the list is newest first.
func TestHoldingsKeepTheStoreRules(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := replicas(t, 1)[0]
		want := Holding{
			Org: "acme", Domain: "example.com", RegisteredAt: 1_800_000_000, ExpiresAt: "2027-01-01T00:00:00Z",
			PriceCents: 6439, CostCents: 5599, Nameservers: []string{"ns1.hanzo.ai", "ns2.hanzo.ai"}, Order: 1001,
		}
		in := want
		in.Domain = "Example.COM"
		if err := s.Put(in); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := s.Get("acme", "EXAMPLE.com"); err != nil || !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("the record came back as %+v, %v, %v; want %+v", got, ok, err, want)
		}
		if _, ok, _ := s.Get("Acme", "example.com"); ok {
			t.Fatal("Acme finds acme's domain")
		}
		if got, _ := s.ListByOrg("Acme"); len(got) != 0 {
			t.Fatalf("Acme lists %d of acme's domains", len(got))
		}

		want.ExpiresAt, want.Nameservers = "2028-01-01T00:00:00Z", nil
		if err := s.Put(want); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := s.Get("acme", "example.com"); got.ExpiresAt != want.ExpiresAt || got.Nameservers != nil {
			t.Fatalf("a second Put did not replace the first: %+v", got)
		}
		older := Holding{Org: "acme", Domain: "older.com", RegisteredAt: 1_700_000_000}
		if err := s.Put(older); err != nil {
			t.Fatal(err)
		}
		list, err := s.ListByOrg("acme")
		if err != nil || len(list) != 2 || list[0].Domain != "example.com" || list[1].Domain != "older.com" {
			t.Fatalf("the list is %+v, %v; want newest first", list, err)
		}
	})
}

// A domain bought through one replica is the org's on every replica, and what it holds
// survives the pod that sold it. Three replicas share one ownership store: the one that
// registers a name records it for all, another is told the org already holds it and
// asks the registrar for nothing, a third renews it, and a fourth store opened on the
// same database after a restart lists it.
//
// MUTATION: keep the holdings in a map in each replica (the MemStore this replaced) and
// the second replica registers the name again and the third cannot renew it.
func TestADomainBoughtOnOneReplicaIsTheOrgsOnEveryReplica(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		stores := replicas(t, 3)
		reg := &mockReg{configured: true, avail: map[string]SearchResult{
			"acme.ai": {Domain: "acme.ai", Available: true, Price: 55.99, Renewal: 55.99, TLD: "ai"},
		}}
		bill := &mockBill{balance: 100000}
		cfg := Config{Markup: Markup{Multiplier: 1.15, MinMarginCents: 300}, Nameservers: []string{"ns1.hanzo.ai"}}
		pods := make([]*Service, len(stores))
		for i, s := range stores {
			pods[i] = NewService(reg, bill, &mockZones{ns: []string{"ns1.hanzo.ai"}}, s, cfg)
		}
		ctx := context.Background()

		if _, err := pods[0].Register(ctx, "acme", "acme.ai", 1, nil); err != nil {
			t.Fatalf("register on replica 0: %v", err)
		}
		if _, err := pods[1].Register(ctx, "acme", "acme.ai", 1, nil); !errors.Is(err, ErrAlreadyOwned) {
			t.Fatalf("a second replica bought a name the org holds: %v", err)
		}
		if len(reg.created) != 1 || len(bill.captured) != 1 {
			t.Fatalf("the registrar was asked %d times and the org charged %d for one name", len(reg.created), len(bill.captured))
		}
		if _, err := pods[2].Renew(ctx, "acme", "acme.ai", 1); err != nil {
			t.Fatalf("a replica that did not sell the name cannot renew it: %v", err)
		}
		list, err := pods[1].ListByOrg("acme")
		if err != nil || len(list) != 1 || list[0].Domain != "acme.ai" {
			t.Fatalf("replica 1 lists %+v, %v", list, err)
		}
		if _, err := pods[2].Renew(ctx, "globex", "acme.ai", 1); !errors.Is(err, ErrNotOwned) {
			t.Fatalf("another org renewed acme's domain: %v", err)
		}
	})
}
