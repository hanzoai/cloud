package cloud_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/host"
	cloud "github.com/hanzoai/cloud/sdk"
)

// TestAPaidReadAsksForStanding holds price.go's exception to its promise. An entry
// there says "this READ spends", and the whole point of saying so is that the money
// path then asks the caller for standing before the meter runs.
//
// The third assertion is the one that keeps the two halves from drifting: the edge
// must still charge NOTHING. The downstream meter owns the charge — that is what
// Metered means — so an edge price on top of it would bill the same work twice, and
// this change was about authorization, never about a second charge.
func TestAPaidReadAsksForStanding(t *testing.T) {
	if len(cloud.PaidReads()) == 0 {
		t.Fatal("no paid reads declared — asserting nothing")
	}
	for _, p := range cloud.PaidReads() {
		probe := p
		if probe != "/" {
			probe = strings.TrimSuffix(probe, "/")
		}
		if !cloud.Consumes(http.MethodGet, probe) {
			t.Errorf("Consumes(GET %s) = false — it is declared as a read that spends", probe)
		}
		if !host.Billable(http.MethodGet, probe) {
			t.Errorf("Billable(GET %s) = false. The entry is inert: the surface it names does not "+
				"declare Metered, so nothing asks the caller for standing and the meter runs on "+
				"whatever they have. Declare the surface Metered and name it in meteredApps.", probe)
		}
		if cents := cloud.DefaultPrice(http.MethodGet, probe); cents != 0 {
			t.Errorf("DefaultPrice(GET %s) = %d, want 0 — the downstream meter owns the charge, "+
				"and an edge price on top of it bills the same work twice", probe, cents)
		}
	}
}
