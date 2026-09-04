package client_test

// billing_wire_test.go — the shapes on this plane are the shapes that can
// actually cross it.
//
// The wire is derived from the TYPE: fields take slots in declaration order and
// no name travels. Three Go shapes have no layout under that rule, and each one
// fails in the worst possible way — QUIETLY:
//
//	A MAP has no fixed field set, so it cannot be laid out at all.
//
//	AN INTERFACE has no layout until it holds something, and what it holds is
//	not known to the type.
//
//	A STRUCT WITH NO EXPORTED FIELDS crosses as an EMPTY struct, because
//	reflection cannot read an unexported field and the encoder skips it rather
//	than refusing. time.Time is exactly that shape — wall, ext and loc are all
//	unexported — so a date sent as a time.Time arrives as the zero instant with
//	nothing anywhere reporting a loss. That is why every timestamp on the
//	billing contract is RFC3339 text, which is also what a time.Time marshals
//	to, so the JSON is unchanged.
//
// The encoder refuses the first two at run time, and it does so on the FIRST
// CALL — which for a money read means in production, on a customer's request,
// rather than here. It does not refuse the third at all. So this walks the
// declared types instead and refuses all three before anything is served.

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
)

// billingWire is every type the billing family sends or receives. A type added
// to the contract and not to this list is not checked, which is why the list is
// also where the next one gets caught.
var billingWire = []any{
	// invoices
	client.InvoicesIn{}, client.Invoices{}, client.BillingInvoice{}, client.InvoiceLineItem{},
	client.RaiseIn{}, client.InvoiceRef{}, client.Invoice{}, client.InvoiceLine{},
	client.Collected{}, client.Document{},
	// statement
	client.CallerIn{}, client.Accounts{}, client.BillingAccount{},
	client.HoldersIn{}, client.Holders{}, client.Holder{},
	client.Payouts{}, client.Payout{},
	client.TransactionsIn{}, client.Transactions{}, client.Transaction{},
	// credits
	client.SubjectIn{}, client.CreditGrants{}, client.CreditGrant{},
	client.CreditBalance{}, client.CreditEntry{},
	client.CreditBreakdown{}, client.CreditTag{}, client.CreditTotal{},
	// caps
	client.Alerts{}, client.Alert{}, client.AlertSpec{}, client.AlertPatch{},
	client.AlertRef{}, client.Dropped{}, client.CapIn{}, client.CapVerdict{},
	// rails
	client.CryptoOptions{}, client.CryptoMintIn{}, client.CryptoDepositIn{},
	client.CryptoDeposit{}, client.WireIn{}, client.WireInstructions{},
	// tier and rollup
	client.Tier{}, client.TierLimits{}, client.TierBalance{}, client.Window{},
	client.RollupIn{}, client.Rollup{}, client.RollupAllotment{}, client.RollupBalance{},
	// posture, methods, plans, subscriptions
	client.PaymentConfig{}, client.ModeIn{}, client.Mode{},
	client.Rendered{}, client.MethodsIn{}, client.MethodSaveIn{},
	client.MethodRef{}, client.Detachment{}, client.PlansIn{},
	client.SubscriptionRef{},
}

func TestBillingShapesCanCrossThePlane(t *testing.T) {
	for _, v := range billingWire {
		typ := reflect.TypeOf(v)
		if bad := uncrossable(typ, typ.Name()); bad != "" {
			t.Errorf("%s", bad)
		}
	}
}

// uncrossable walks a type and names the first field that has no wire form, or
// returns "" when every field has one. It recurses through structs and slice
// elements because the defect is usually one level down — a date inside a row
// inside a listing.
func uncrossable(t reflect.Type, path string) string {
	switch t.Kind() {
	case reflect.Pointer:
		return uncrossable(t.Elem(), path)
	case reflect.Map:
		return path + " is a map, which has no layout on this plane — send the entries as a slice and render the object at the endpoint, as CreditBreakdown does"
	case reflect.Interface:
		return path + " is an interface, which has no layout until it holds something — declare the shape"
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return "" // bytes cross whole
		}
		return uncrossable(t.Elem(), path+"[]")
	case reflect.Struct:
		if t == reflect.TypeOf(time.Time{}) {
			return path + " is a time.Time, which crosses as an EMPTY struct because its fields are unexported — carry the timestamp as RFC3339 text, which is what it marshals to anyway"
		}
		exported := 0
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			exported++
			// A field held out of the JSON entirely is still sent, so it is still
			// checked: the wire and the document are different projections.
			if bad := uncrossable(f.Type, path+"."+f.Name); bad != "" {
				return bad
			}
		}
		if exported == 0 && t.NumField() > 0 {
			return path + " has no exported fields, so it crosses as an empty struct"
		}
		return ""
	default:
		return ""
	}
}

// TestBillingTimestampsAreText is the same rule stated the other way round, and
// it is here because the failure it guards is invisible rather than loud: a
// field NAMED like a timestamp that is not text has almost certainly been
// declared as a time value by someone who did not know it cannot cross.
func TestBillingTimestampsAreText(t *testing.T) {
	stamps := []string{"At", "Date", "Resets", "Created", "Until", "Period"}
	for _, v := range billingWire {
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			named := false
			for _, s := range stamps {
				if strings.HasSuffix(f.Name, s) {
					named = true
				}
			}
			if !named {
				continue
			}
			k := f.Type.Kind()
			if k == reflect.Pointer {
				k = f.Type.Elem().Kind()
			}
			// int64 is a legitimate spelling — the ledger ops carry unix seconds —
			// and so is a nested struct that holds its own fields. What is refused
			// is the one that looks right and arrives empty.
			if k == reflect.Struct || k == reflect.Interface || k == reflect.Map {
				t.Errorf("%s.%s reads as a timestamp but is a %s — carry it as RFC3339 text or unix seconds",
					typ.Name(), f.Name, f.Type)
			}
		}
	}
}
