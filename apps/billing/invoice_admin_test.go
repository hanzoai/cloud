package billing

import (
	"strings"
	"testing"
)

// Raising, issuing, collecting and voiding an invoice are the merchant's acts, and
// the merchant is the org's admin. A member is refused before commerce is asked.
// In the shared signup org every member is a stranger to the next, and collect
// spends the credits and prepaid balance of whichever customer the invoice names —
// so a member who could raise one naming a neighbour could spend the neighbour's
// wallet.
//
// Each row is a pair: the admin must get past the check, or the member's refusal
// would pass against an op that refuses everyone.
func TestOnlyAnOrgAdminActsOnInvoices(t *testing.T) {
	app := mountApp(t)
	for _, addr := range []string{
		"POST /v1/billing/invoices",
		"POST /v1/billing/invoices/{id}/issue",
		"POST /v1/billing/invoices/{id}/collect",
		"POST /v1/billing/invoices/{id}/void",
	} {
		method, path := split(addr)
		member := drive(t, app, method, path, map[string]string{"Authorization": "Bearer sk-test"})
		if !strings.Contains(member, "Only an admin of acme can act on its invoices. Ask an admin to do it.") {
			t.Errorf("%s as a member answered %q — a member must not act on invoices", addr, clip(member))
		}
		admin := drive(t, app, method, path, map[string]string{
			"Authorization":     "Bearer sk-test",
			"X-User-IsOrgAdmin": "true",
		})
		if strings.Contains(admin, "Only an admin of") {
			t.Errorf("%s as the org admin answered %q — the check refuses everyone", addr, clip(admin))
		}
	}
}
