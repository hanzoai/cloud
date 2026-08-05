package risk

// listing_test.go — the jurisdiction listing the geography half of the rule
// decides against, held to the one property that decides whether the rule works at
// all: it must be able to ANSWER.
//
// [reference.Jurisdictions] refuses an empty or undated listing, and [onGeography]
// swallows that refusal by design — a broken listing must not escalate every
// payment. Those two are each right on their own, and together they make a listing
// that cannot answer indistinguishable, from the outside, from a world with nothing
// risky in it. So which listing is in force, and whether it can decide, is settled
// where the listing is CHOSEN and reported where an operator can read it.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/reference"

	"github.com/hanzoai/cloud"
)

// TestJurisdictions_AnUndatedOperatorListingLosesToTheDatedDefault — a listing
// that cannot decide must not be the listing in force.
//
// THE SILENT DISARM this closes. The operator's listing used to win on MEMBERSHIP
// alone: a non-empty `action`/`monitoring` put it in force whatever else it said.
// But [reference.Jurisdictions] refuses to answer from an UNDATED listing — rightly,
// since "not listed" from a listing of unknown currency is not a fact — so a stated
// listing missing `as_of` went in force and then errored on every single country.
// [onGeography] swallows that error, so the ACTION tier became unreachable, the
// freeze the rule exists for silently vanished, and what remained — review at or
// past the freeze value — looks exactly like a rule that is working. Nothing logged
// it, because the error that mattered was consumed per-country.
//
// Mutation proof: restore `if j := ...; len(j.Action) > 0 || len(j.Monitoring) > 0
// { return j }` and the freeze assertion below fails while the Gap assertion
// reports nothing at all.
func TestJurisdictions_AnUndatedOperatorListingLosesToTheDatedDefault(t *testing.T) {
	// The misconfiguration, exactly: real membership, no date.
	undated := reference.Jurisdictions{
		Action:     []string{"XA", "XB"},
		Monitoring: []string{"XC"},
	}

	// FIRST, why it is a misconfiguration at all: taken whole, it can assess nothing
	// — not even a country it lists.
	if _, err := undated.Jurisdiction("XA"); err == nil {
		t.Fatal("an undated listing answered a country — the fixture does not reproduce the defect")
	}

	got := resolve(undated)
	if got.Operator {
		t.Error("an undated operator listing is in force — it can decide nothing, so it disarms " +
			"the geography half of the rule while looking configured")
	}
	if got.Gap == "" {
		t.Error("the fallback is SILENT — an operator whose listing was discarded has no way to " +
			"learn it, which is what made this undetectable")
	}
	if !strings.Contains(got.Gap, "as_of") {
		t.Errorf("the reason does not name the field that is missing: %q", got.Gap)
	}

	// AND THE FREEZE IS STILL REACHABLE, which is the whole point of falling back
	// rather than merely refusing the bad listing: the default is DATED, so the
	// ACTION tier answers and [onGeography]'s freeze branch can be taken.
	tier, err := got.Jurisdiction("AF")
	if err != nil {
		t.Fatalf("the listing in force cannot assess a country: %v — the freeze is still gone", err)
	}
	if tier != tierAction {
		t.Fatalf("AF is in tier %q, want %q — the tier the freeze branch turns on", tier, tierAction)
	}

	// End to end, through the rule itself: this process states no AML_JURISDICTIONS,
	// so [jurisdictions] resolved to the same dated default the fallback selects, and
	// a large payment from that tier FREEZES.
	if d := determine("AF", freezeNano); d.Action != cloud.ActionRestrict {
		t.Fatalf("determine(AF, freeze) = %q, want %q — the listing the misconfiguration falls "+
			"back to does not actually freeze anything", d.Action, cloud.ActionRestrict)
	}
}

// TestJurisdictions_TheStatedListingWinsWhenItCanDecide is the other half, so the
// test above is a rule about USABILITY and not a rule that quietly ignores
// operators.
func TestJurisdictions_TheStatedListingWinsWhenItCanDecide(t *testing.T) {
	stated := reference.Jurisdictions{
		AsOf:       time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		Action:     []string{"XA"},
		Monitoring: []string{"XC"},
	}
	got := resolve(stated)
	if !got.Operator {
		t.Fatal("a dated operator listing did not take force — the operator's listing wins whole")
	}
	if got.Gap != "" {
		t.Errorf("a usable listing reported a gap: %q", got.Gap)
	}
	// WHOLE, never merged: the default's own members are not in it. A merged listing
	// is one nobody stated and nobody can reproduce.
	if tier, err := got.Jurisdiction("AF"); err != nil || tier != "" {
		t.Errorf("AF resolved to %q (err %v) under a listing that does not name it — the "+
			"operator's listing was merged with the default", tier, err)
	}
	if tier, err := got.Jurisdiction("XA"); err != nil || tier != tierAction {
		t.Errorf("XA resolved to %q (err %v), want %q", tier, err, tierAction)
	}
}

// TestHealth_ReportsTheListingItDecidesAgainst: the listing in force is READABLE
// FROM OUTSIDE the process.
//
// A listing has a currency, and one that has gone stale — or one an operator
// stated that cannot decide at all — degrades the geography half of the rule with
// nothing anywhere saying so. That is the same argument `evicted` and `strained`
// are on this probe for: a control that switches itself off must be visible from
// outside, and "visible" means a reader can ask.
//
// It is reported, never fatal. A stale listing still decides, the default is always
// dated, and a probe that failed on a listing's age would take the whole model
// plane down over a reference table.
//
// Mutation proof: drop the listing fields from [health] and this fails while
// TestHealthCarriesItsReport still passes.
func TestHealth_ReportsTheListingItDecidesAgainst(t *testing.T) {
	probe.reset(true)
	app := mountApp(t)
	code, body := req(t, app, http.MethodGet, "/v1/risk/health", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/risk/health = %d %s", code, body)
	}
	var rep map[string]any
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	for _, k := range []string{"listed", "listed_days", "listing"} {
		if _, ok := rep[k]; !ok {
			t.Errorf("the probe's body has no %q — the listing the rule decides against is "+
				"unreadable from outside the process", k)
		}
	}
	// This process states no AML_JURISDICTIONS, so the compiled default is in force
	// and the probe says so by NAME rather than leaving a reader to infer it.
	if rep["listing"] != "default" {
		t.Errorf("listing = %v, want %q", rep["listing"], "default")
	}
	// The DATE is the fact that matters: an undated listing can assess nothing, so a
	// probe reporting a listing without one would be reporting the very state that
	// silently disarms the rule.
	listed, ok := rep["listed"].(string)
	if !ok || listed == "" {
		t.Fatalf("listed = %v — the listing in force has no date, so its currency cannot be assessed",
			rep["listed"])
	}
	if _, err := time.Parse(time.RFC3339, listed); err != nil {
		t.Errorf("listed %q is not RFC 3339: %v", listed, err)
	}
	if _, reported := rep["listing_gap"]; reported {
		t.Errorf("the probe reports a listing gap with none stated: %v", rep["listing_gap"])
	}
}
