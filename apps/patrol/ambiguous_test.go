package patrol

import (
	"net/http"
	"testing"
)

// ambiguous_test.go is the pair of cases reach resolves from nothing: a customer
// contact on two client records, and a signed-in customer on none. Both must
// reach nothing at all — and in particular neither may be answered the sites
// whose `client` is empty, which is what a narrowed query carrying an empty
// customer would match.

// TestAContactOnTwoCustomersReachesNothing holds the ambiguous contact to the
// empty answer. Two records sharing a contact make the estate a customer reads a
// matter of which row the store returned first.
func TestAContactOnTwoCustomersReachesNothing(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTClient, map[string]any{"name": "northside", "contact": buyer})
	create(t, app, org, DTClient, map[string]any{"name": "southside", "contact": buyer})
	seedSite(t, app, org, "theirs", "1001", "northside")
	seedSite(t, app, org, "orphan", "1003", "") // no customer at all

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/site", org, buyer, nil)
	if code != http.StatusOK {
		t.Fatalf("a client listing sites want 200, got %d (%s)", code, first(raw, 200))
	}
	var sites []patrolSite
	data(t, raw, &sites)
	if len(sites) != 0 {
		t.Errorf("a contact on two customer records read %d sites: %+v", len(sites), sites)
	}
}

// TestACustomerOnNoRecordReachesNothing is the same for a signed-in client whose
// contact matches no record — a portal login made before the register caught up.
func TestACustomerOnNoRecordReachesNothing(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTClient, map[string]any{"name": "northside", "contact": rival})
	seedSite(t, app, org, "orphan", "1003", "")
	seedSite(t, app, org, "theirs", "1001", "northside")

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/site", org, buyer, nil)
	if code != http.StatusOK {
		t.Fatalf("a client listing sites want 200, got %d (%s)", code, first(raw, 200))
	}
	var sites []patrolSite
	data(t, raw, &sites)
	if len(sites) != 0 {
		t.Errorf("a client matching no customer record read %d sites: %+v", len(sites), sites)
	}
}
