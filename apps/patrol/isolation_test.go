package patrol

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// isolation_test.go measures the two boundaries this product turns on: one
// tenant never reads another's org, and inside an org one customer never reads
// another's estate. An incident IS a site — where a break-in happened, what the
// officer found, what it came to — so every read that carries a site is a read
// that has to narrow.

// rival is the second customer's contact — the person whose estate a hostile
// client must never reach.
const rival = "rival@acme.test"

// twoCustomers seeds one org holding two customers, a site each, and a camera on
// the rival's site. It is the shape every cross-customer test needs.
func twoCustomers(t *testing.T, app *zip.App) {
	t.Helper()
	install(t, app, org)
	create(t, app, org, DTClient, map[string]any{"name": "northside", "contact": buyer})
	create(t, app, org, DTClient, map[string]any{"name": "southside", "contact": rival})
	seedSite(t, app, org, "theirs", "1001", "northside")
	seedSite(t, app, org, "rivals", "1002", "southside")
}

// TestClientReadsNoOtherCustomersIncident holds the incident reads to the rule
// the site reads keep: a customer reads their own estate. An incident IS a site
// — where a break-in happened, what the officer found, what it came to — and the
// list and the single read admit every one of the six with no narrowing at all.
func TestClientReadsNoOtherCustomersIncident(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller, map[string]any{
		"ref": "ARC-1", "account": "1002", "zone": "Z9", "trigger": "Intruder — server room",
	})
	if code != http.StatusCreated {
		t.Fatalf("seed the rival's activation want 201, got %d (%s)", code, raw)
	}
	var made patrolActivation
	data(t, raw, &made)
	rivals := made.Incident.Name

	code, raw = call(t, app, http.MethodGet, "/v1/patrol/incident", org, buyer, nil)
	if code != http.StatusOK {
		t.Fatalf("a client listing incidents want 200, got %d (%s)", code, raw)
	}
	var incs []patrolIncident
	data(t, raw, &incs)
	for _, i := range incs {
		if i.Site == "rivals" {
			t.Errorf("a client listed another customer's incident at %s: %+v", i.Site, i)
		}
	}

	// And the single read, which carries the whole act trail and the outcome.
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/incident/"+rivals, org, buyer, nil)
	if code != http.StatusNotFound {
		t.Errorf("a client reading another customer's incident want 404, got %d (%s)", code, raw)
	}
}

// TestClientReadsNoOtherCustomersFeed holds the feed rows to the same rule. A
// matched activation raises a red row naming the site and carrying the trigger
// verbatim, so an unnarrowed /event hands one customer every other customer's
// break-ins.
func TestClientReadsNoOtherCustomersFeed(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller, map[string]any{
		"ref": "ARC-2", "account": "1002", "zone": "Z9", "trigger": "Intruder — server room",
	}); code != http.StatusCreated {
		t.Fatalf("seed the rival's activation want 201, got %d (%s)", code, raw)
	}

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/event", org, buyer, nil)
	if code != http.StatusOK {
		t.Fatalf("a client listing the feed want 200, got %d (%s)", code, raw)
	}
	var rows []patrolEvent
	data(t, raw, &rows)
	for _, r := range rows {
		if r.Site == "rivals" || strings.Contains(r.Title, "rivals") || strings.Contains(r.Detail, "server room") {
			t.Errorf("a client read another customer's feed row: %+v", r)
		}
	}
}

// TestClientReadsNoOtherCustomersCameras holds the camera register to the same
// rule, and proves ?site= is not a filter a caller may widen: a client naming
// another customer's site must learn nothing from it.
func TestClientReadsNoOtherCustomersCameras(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	create(t, app, org, DTCamera, map[string]any{
		"name": "cam-rival", "site": "rivals", "label": "Server room", "online": true,
	})

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera?site=rivals", org, buyer, nil)
	if code != http.StatusOK {
		t.Fatalf("a client listing cameras want 200, got %d (%s)", code, raw)
	}
	var cams []patrolCamera
	data(t, raw, &cams)
	if len(cams) != 0 {
		t.Errorf("a client named another customer's site and read %d of its cameras: %+v", len(cams), cams)
	}
}

// TestTenantIsolationIsMeasured replaces a check that cannot fail. The existing
// one reads a second org that was never installed, so an empty answer proves
// only that nothing was written there. This installs the other org, seeds it,
// and then asks for it — the question the design's central claim turns on.
func TestTenantIsolationIsMeasured(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	install(t, app, "victim")
	seedSite(t, app, "victim", "secret-depot", "9001", "")

	code, raw := hit(t, app, http.MethodGet, "/v1/patrol/site", nil,
		map[string]string{"X-Org-Id": "victim", "X-User-Id": boss, "X-User-Orgs": org})
	if code == http.StatusOK {
		var sites []patrolSite
		data(t, raw, &sites)
		if len(sites) > 0 {
			t.Fatalf("a caller whose orgs are %q read %d sites out of victim: %+v", org, len(sites), sites)
		}
	}
	if code != http.StatusForbidden {
		t.Fatalf("acting in an org the caller does not hold want 403, got %d (%s)", code, first(raw, 200))
	}
}

// TestFeedSubjectIsAddressable holds the feed's subject to the bus's own rule.
// principal.Org admits '.', '*' and '>' in an org, deliberately — they fold
// injectively through the slugger. A NATS subject reads '.' as a token separator
// and a bare '*' token as a one-token wildcard, so the two rules disagree, and
// bus.Org is the call that exists to refuse an org that is not addressable.
// feed.go formats with bus.Phys and never asks.
func TestFeedSubjectIsAddressable(t *testing.T) {
	for _, hostile := range []string{"a.*.b", "a.b.*.c", "a/b", "A B"} {
		if !principal.NamesOrg(hostile) {
			continue // principal itself refuses this one; nothing to answer for
		}
		subject := bus.Phys(hostile, Module)
		for tok := range strings.SplitSeq(subject, ".") {
			if tok == "*" || tok == ">" {
				t.Errorf("org %q formats the subject %q, whose token %q is a NATS wildcard: "+
					"a feed subscribed on it receives other orgs' frames", hostile, subject, tok)
			}
		}
		if strings.ContainsAny(subject, " /") {
			t.Errorf("org %q formats %q, which is not a legal NATS subject", hostile, subject)
		}
	}
}
