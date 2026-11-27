package patrol

import (
	"fmt"
	"net/http"
	"testing"
)

// starve_test.go asks who pays for a bound. Both bounds patrol added are keyed
// by the ORG and by nothing else, so whoever draws first spends the whole
// allowance and everyone else in the tenant is refused — including the two
// callers the product exists for: the receiving centre posting an activation,
// and the control room watching the feed. A bound whose cost falls on the victim
// converts one noisy credential into an outage of an alarm-response service.

// TestOneCredentialDoesNotStarveTheIntake drains the org's intake with one
// machine credential and then asks whether a controller can still key an
// activation in by hand. `takes` is rate{per: minute/120, burst: 60} keyed by
// org: an app registered in the tenant, a panel stuck in a loop, or a credential
// somebody found spends every token, and the activation that matters is the one
// refused. Nothing here is per credential, per source or per account, and
// nothing prioritises the hand-keyed call over the flood.
func TestOneCredentialDoesNotStarveTheIntake(t *testing.T) {
	const in = "starved"
	t.Cleanup(func() {
		takes.mu.Lock()
		delete(takes.seen, in)
		takes.mu.Unlock()
	})
	app := mount(t)
	install(t, app, in)
	seedSite(t, app, in, "depot", "1001", "")

	flood := map[string]string{"X-Org-Id": in, "X-User-Id": centre2, "X-User-IsApp": "true"}
	spent := 0
	for i := range 200 {
		code, _ := hit(t, app, http.MethodPost, "/v1/patrol/alarm",
			map[string]any{"ref": fmt.Sprintf("LOOP-%d", i), "account": "9999"}, flood)
		if code == http.StatusTooManyRequests {
			break
		}
		spent++
	}
	if spent == 0 {
		t.Skip("the machine credential was not admitted; nothing was drawn")
	}

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", in, controller,
		map[string]any{"ref": "REAL-1", "account": "1001", "zone": "Z3", "trigger": "Intruder"})
	if code == http.StatusTooManyRequests {
		t.Errorf("one machine credential spent %d tokens and the control room's own activation at a "+
			"site under contract was then refused %d (%s): the bound is keyed by the org, so the "+
			"flood denies the service to the tenant it was meant to protect",
			spent, code, first(raw, 160))
	}
}

// TestOneReaderDoesNotStarveTheFeed asks the same question of the live picture.
// hold counts open streams per org against maxStreams and takes no caller, so
// there is no term in it that could tell a customer's portal from the control
// room's wall: 32 connections from one signed-in member and the operation's own
// screens are refused 429 until they let go.
func TestOneReaderDoesNotStarveTheFeed(t *testing.T) {
	const in = "crowded"
	var lets []func()
	t.Cleanup(func() {
		for _, let := range lets {
			let()
		}
	})
	for i := range maxStreams {
		let, held := hold(in)
		if !held {
			t.Fatalf("stream %d of %d was refused while seeding", i+1, maxStreams)
		}
		lets = append(lets, let)
	}
	if _, held := hold(in); !held {
		t.Errorf("one member holding %d streams refused the next reader in the same org: the count "+
			"names the org and nothing else, so a customer's portal can close the control room's "+
			"live picture", maxStreams)
	}
}
