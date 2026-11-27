package crm

// customer.go — the person who paid is a customer.
//
// When a payment settles in an org that holds the CRM, the person the payer's
// address is becomes a customer of that org: their lifecycle is customer and, when
// the team has given them no other type, so is their type, which is what the
// Customers view lists. Their commerce user is linked once on their timeline, so a
// record can read what they pay for from commerce. Nothing here reads or moves
// money, and only the org named is written: another org's person for the same
// address is another record.

import (
	"context"
	"time"
)

// Buyer is the payer of a settled payment.
type Buyer struct {
	// Email is the payer's verified address; the person is the one it is.
	Email string
	// User is the payer's commerce user: the wallet key their payments are made from.
	User string
}

// Customer makes the person b.Email is a customer of org and links b.User on their
// timeline, writing only what is not already so: a payment that settles again changes
// nothing. An org that holds no CRM is an error; it is never installed here.
func Customer(ctx context.Context, org string, b Buyer, at time.Time) error {
	id := personID(b.Email)
	held, err := get(ctx, org, People, id)
	if err != nil {
		return err
	}
	want := map[string]any{"lifecycle": "customer"}
	if t := text(held, "type"); t == "" || t == "prospect" {
		want["type"] = "customer"
	}
	change := map[string]any{"id": id}
	for field, v := range want {
		if text(held, field) != v {
			change[field] = v
		}
	}
	if held == nil {
		change["emails"], change["source"] = email(b.Email), "commerce"
	}
	if len(change) > 1 {
		if err := upsert(ctx, org, "application", People, "", change); err != nil {
			return err
		}
	}
	return form(ctx, org, "application", map[string]any{
		"source_id": "customer:" + b.User, "kind": "system", "direction": "in", "channel": "commerce",
		"subject": "Customer", "at": stamp(at), "person": id, "data": map[string]any{"user": b.User},
	})
}
