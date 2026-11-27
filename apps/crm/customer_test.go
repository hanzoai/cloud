package crm

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
)

const other = "other"

// inOrg counts the records of collection in o that answer filter.
func inOrg(t *testing.T, ctx context.Context, o, collection, filter string) int {
	t.Helper()
	page, err := listFn(plane.For(ctx, o), &client.BaseListIn{Collection: collection, Filter: filter, PerPage: 1})
	if err != nil {
		t.Fatalf("count %s in %s: %v", collection, o, err)
	}
	return page.TotalItems
}

// A payer is a customer of the org they paid: their person is a customer, made when
// the CRM held none, and their commerce user is linked once however often a payment
// settles. The same address in another org is another person, and is left as it was.
func TestAPayerIsACustomerOfTheOrgTheyPaid(t *testing.T) {
	ctx := open(t)
	if err := Install(ctx, other); err != nil {
		t.Fatalf("install: %v", err)
	}
	lead := betaLead()
	for _, o := range []string{org, other} {
		if err := FileLead(ctx, o, "application", lead, filedAt); err != nil {
			t.Fatalf("file lead in %s: %v", o, err)
		}
	}
	buyer := Buyer{Email: "ada@example.com", User: "acme/ada"}
	for range 2 {
		if err := Customer(ctx, org, buyer, filedAt); err != nil {
			t.Fatalf("customer: %v", err)
		}
	}

	p := must(t, ctx, People, personID(buyer.Email))
	if p["lifecycle"] != "customer" || p["type"] != "customer" {
		t.Errorf("the payer is %v / %v, want a customer", p["lifecycle"], p["type"])
	}
	link := `source_id = "customer:acme/ada"`
	if n := count(t, ctx, Activities, link); n != 1 {
		t.Errorf("the commerce user is linked %d times, want once", n)
	}
	a, err := formOf(ctx, org, "customer:acme/ada")
	if err != nil || a == nil || a["person"] != p["id"] || a["kind"] != "system" || a["channel"] != "commerce" {
		t.Fatalf("the link = %v, %v", a, err)
	}
	if d, _ := a["data"].(map[string]any); d["user"] != "acme/ada" {
		t.Errorf("the link names %v, want acme/ada", d)
	}

	theirs, err := get(ctx, other, People, personID(buyer.Email))
	if err != nil || theirs == nil || theirs["lifecycle"] != "lead" || !empty(theirs["type"]) {
		t.Errorf("another org's person for the same address = %v, %v, want the lead it was", theirs, err)
	}
	if n := inOrg(t, ctx, other, Activities, link); n != 0 {
		t.Errorf("another org holds %d links, want none", n)
	}
}

// A payer the CRM did not hold is made a person at the address they paid from, and a
// type the team gave a person is theirs: an investor who pays is a customer by
// lifecycle and stays an investor by type.
func TestAPayerIsMadeAPersonAndKeepsTheTypeTheTeamGave(t *testing.T) {
	ctx := open(t)
	if err := Customer(ctx, org, Buyer{Email: "Grace@Example.com", User: "acme/grace"}, filedAt); err != nil {
		t.Fatalf("customer: %v", err)
	}
	grace := must(t, ctx, People, personID("grace@example.com"))
	if e, _ := grace["emails"].(map[string]any); e["primaryEmail"] != "grace@example.com" || grace["source"] != "commerce" || grace["type"] != "customer" {
		t.Errorf("a payer the CRM did not hold = %v", grace)
	}

	if err := FileLead(ctx, org, "application", betaLead(), filedAt); err != nil {
		t.Fatalf("file lead: %v", err)
	}
	id := personID("ada@example.com")
	if err := upsert(ctx, org, "application", People, "", map[string]any{"id": id, "type": "investor"}); err != nil {
		t.Fatalf("type: %v", err)
	}
	if err := Customer(ctx, org, Buyer{Email: "ada@example.com", User: "acme/ada"}, time.Now()); err != nil {
		t.Fatalf("customer: %v", err)
	}
	if ada := must(t, ctx, People, id); ada["lifecycle"] != "customer" || ada["type"] != "investor" {
		t.Errorf("an investor who pays is %v / %v, want customer / investor", ada["lifecycle"], ada["type"])
	}
}

// An org that holds no CRM is an error and files nothing: a payment never installs one.
func TestAnOrgWithNoCRMIsNeverGivenOne(t *testing.T) {
	ctx := open(t)
	if err := Customer(ctx, "bare", Buyer{Email: "ada@example.com", User: "bare/ada"}, filedAt); err == nil {
		t.Fatal("a payer was filed in an org that holds no CRM")
	}
	if _, err := listFn(plane.For(ctx, "bare"), &client.BaseListIn{Collection: People, PerPage: 1}); err == nil {
		t.Error("an org with no CRM holds a people collection after a payment")
	}
}
