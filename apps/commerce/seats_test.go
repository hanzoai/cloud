// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"testing"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercesubscription "github.com/hanzoai/commerce/models/subscription"

	"github.com/hanzoai/cloud/client"
)

// A team's seat types cross the plane both ways: what a buyer asks for reaches the
// sale as commerce reads it, and what a subscription holds — each seat type, the
// seats its period paid for, and who holds which — reaches the wire.
func TestATeamsSeatTypesCrossThePlane(t *testing.T) {
	in := saleLines([]client.SaleLine{{PlanID: "team-premium", Quantity: 3}})
	if len(in) != 1 || in[0] != (commercebilling.LineIn{PlanID: "team-premium", Quantity: 3}) {
		t.Fatalf("the sale reads %+v, want three team-premium seats", in)
	}
	if saleLines(nil) != nil {
		t.Fatal("a sale with no lines reads some")
	}

	row := subscriptionRow(&commercebilling.Subscription{
		ID: "sub_1", PlanID: "team", Quantity: 1, Seats: 4,
		Lines: []commercebilling.SubscriptionLine{{Plan: "team-premium", Name: "Team Premium", Price: 10000,
			Quantity: 3, PendingQuantity: 2, Seats: 3}},
		Assigned: []commercesubscription.Seat{{Member: "acme/dana", Plan: "team-premium"}},
	})
	want := client.SubscriptionLine{Plan: "team-premium", Name: "Team Premium", Price: 10000, Quantity: 3, PendingQuantity: 2, Seats: 3}
	if row.Seats != 4 || len(row.Lines) != 1 || row.Lines[0] != want {
		t.Fatalf("the wire row holds %d seats and lines %+v, want 4 and %+v", row.Seats, row.Lines, want)
	}
	if len(row.Assigned) != 1 || row.Assigned[0] != (client.SubscriptionSeat{Member: "acme/dana", Plan: "team-premium"}) {
		t.Fatalf("the wire row assigns %+v, want dana on a premium seat", row.Assigned)
	}
}
