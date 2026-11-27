package principals

// red_test.go holds the surface to what a payer may learn of a payee before the
// payee grants anything, to the durability of a sanctions decision across the
// names an org is known by, and to the order in which a decision reaches the
// trail and the store.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/iam"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	luxlog "github.com/luxfi/log"
)

// w9as writes org's W-9 as a corporation under name.
func (e *env) w9as(org, name, ein string) {
	e.t.Helper()
	e.must(200, http.MethodPut, "/v1/tax/profile", admin(org), map[string]any{
		"name": name, "classification": "c_corp", "tin": ein, "tinType": "ein",
		"address": map[string]any{"line1": "1 Market St", "city": "San Francisco", "state": "CA", "zip": "94105"},
	}, nil)
}

// w8as writes org's W-8BEN-E as a corporation resident in country.
func (e *env) w8as(org, name, country, city string) {
	e.t.Helper()
	e.must(200, http.MethodPut, "/v1/tax/profile", admin(org), map[string]any{
		"form": "w8bene", "name": name,
		"address": map[string]any{"line1": "1 Main Street", "city": city, "country": country},
		"w8":      map[string]any{"country": country, "chapter3": "corporation", "chapter4": "active_nffe", "capacity": "Director"},
	}, nil)
}

// status is org's sanctions verdict as its own admin reads it.
func (e *env) status(org string) iam.Screening {
	e.t.Helper()
	var v iam.View
	e.must(200, http.MethodGet, "/v1/principal", admin(org), nil, &v)
	return v.Sanctions
}

// queued is the review queue's matches naming org, every status.
func (e *env) queued(org string) []iam.Match {
	e.t.Helper()
	var q matchList
	e.must(200, http.MethodGet, "/v1/principal/matches?status=all", reviewer("admin"), nil, &q)
	var out []iam.Match
	for _, m := range q.Matches {
		if m.Org == org {
			out = append(out, m)
		}
	}
	return out
}

// Before a payee grants its form, a payer learns whether it may pay, what it
// needs from the payee, or that it may not and should ask support — never
// whether the payee is a U.S. or a foreign person, and never that the payee
// screened close to a designated person.
func TestClearanceAnswersVerdictsOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.form("sinister", "Sinister Shipping Co.", "Jane Doe")

		c := e.clear("acme", map[string]any{"payee": "wien", "amount": "1000.00", "category": "royalties", "performed": "US"})
		if c.Status != statusUnknown || c.Withholding.Rate != "" || owesOf(c, "1042-S") || !stepOf(c, "tax_form") {
			t.Fatalf("a payee whose W-8 the payer does not hold = %+v; the payer learned its status from the payee's own file", c.Decision)
		}
		c = e.clear("acme", map[string]any{"payee": "globex", "amount": "1000.00"})
		if c.Status != statusUnknown || c.Withholding.Rate != "" || !stepOf(c, "tax_form") {
			t.Fatalf("a payee whose W-9 the payer does not hold = %+v, want its status unknown to the payer", c.Decision)
		}

		c = e.clear("acme", map[string]any{"payee": "sinister", "amount": "500.00"})
		if c.Allowed || !stepOf(c, "payee_hold") {
			t.Fatalf("a payee under review = %+v, want held, with nothing said of why", c.Decision)
		}
		b, _ := json.Marshal(c)
		low := strings.ToLower(string(b))
		for _, x := range []string{"sanction", "designat", "ofac", "match"} {
			if strings.Contains(low, x) {
				t.Fatalf("the payer's clearance says %q of the payee: %s", x, b)
			}
		}
	})
}

// A payer is bounded in how many payees it may ask about, so clearance is not a
// way to read the platform's verdict on every org.
func TestClearanceIsRateLimited(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		e.form("acme", "Acme Robotics, Inc.", "Ada Lovelace")
		for i := range newPayeesPerDay {
			e.clear("acme", map[string]any{"payee": fmt.Sprintf("payee%02d", i), "amount": "1.00"})
		}
		code, b := e.raw(http.MethodPost, "/v1/principal/clearance", member("acme"), map[string]any{"payee": "onemore", "amount": "1.00"})
		if code != http.StatusTooManyRequests {
			t.Fatalf("clearance %d to a new payee in a day = %d %s, want 429", newPayeesPerDay+1, code, b)
		}
		// A payee already asked about is not a new one.
		e.clear("acme", map[string]any{"payee": "payee00", "amount": "2.00"})
	})
}

// A tax form may be asked for ahead of the first payment only by a payer that is
// itself verified and screened clear, on a clearance it asked for recently.
func TestIntentNeedsAPayerThatCleared(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		// drifter formed nothing: its own identity is not verified.
		e.clear("drifter", map[string]any{"payee": "globex", "amount": "100.00"})
		if code, b := e.raw(http.MethodPost, "/v1/tax/w9", admin("drifter"), map[string]any{"payee": "globex"}); code != http.StatusConflict {
			t.Fatalf("an unverified payer asked for a W-9 on a clearance it could not pass = %d %s, want 409", code, b)
		}
		e.clear("acme", map[string]any{"payee": "globex", "amount": "100.00"})
		var w struct{ ID string }
		e.must(201, http.MethodPost, "/v1/tax/w9", admin("acme"), map[string]any{"payee": "globex"}, &w)

		// A clearance from long ago says nothing about a payment about to be made.
		old := time.Now().Add(-intentWindow - time.Hour).Unix()
		if _, err := mounted.State.store.db.Exec(`INSERT INTO clearances (id, payer, payee, allowed, body, created_at, payer_ok)
		VALUES ('clr_old', 'acme', 'initech', 0, '{}', $1, 1)`, old); err != nil {
			t.Fatal(err)
		}
		if code, b := e.raw(http.MethodPost, "/v1/tax/w9", admin("acme"), map[string]any{"payee": "initech"}); code != http.StatusConflict {
			t.Fatalf("a W-9 asked on a stale clearance = %d %s, want 409", code, b)
		}
	})
}

// A decision on a sanctions match is about the org and the designated person, and
// renaming the org neither escapes a confirmed match nor splits one match into
// several reviews; a namesake cleared for one name is asked again for a new one.
func TestSanctionsStickToTheOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.w9as("shady", "Sinister Shipping Company", "12-0000001")
		if c := e.clear("acme", map[string]any{"payee": "shady", "amount": "10.00"}); !stepOf(c, "payee_hold") {
			t.Fatalf("clearance = %+v, want held", c.Decision)
		}
		ms := e.queued("shady")
		if len(ms) != 1 {
			t.Fatalf("queue = %+v, want one match for shady", ms)
		}
		e.must(200, http.MethodPost, "/v1/principal/matches/"+ms[0].ID, reviewer("admin"), map[string]any{"decision": "confirmed", "note": "the designated person"}, nil)
		e.w9as("shady", "Blue Sky Holdings", "12-0000001")
		if s := e.status("shady"); s.Status != screenBlocked {
			t.Fatalf("a confirmed org renamed away from the list = %s, want still blocked", s.Status)
		}
		if c := e.clear("acme", map[string]any{"payee": "shady", "amount": "10.00"}); c.Allowed || !stepOf(c, "payee_hold") {
			t.Fatalf("a renamed confirmed payee = %+v, want held", c.Decision)
		}

		// Two names of one org close to one designation are one review.
		e.form("twice", "Sinister Shipping Co.", "Jane Doe")
		e.w9as("twice", "Sinister Shipping Company Inc", "12-0000002")
		e.clear("acme", map[string]any{"payee": "twice", "amount": "10.00"})
		ms = e.queued("twice")
		if len(ms) != 1 || len(ms[0].Hits) != 2 {
			t.Fatalf("queue = %+v, want one match for twice carrying both names", ms)
		}
		e.must(200, http.MethodPost, "/v1/principal/matches/"+ms[0].ID, reviewer("admin"), map[string]any{"decision": "cleared", "note": "a namesake"}, nil)
		if s := e.status("twice"); s.Status != screenClear {
			t.Fatalf("a cleared namesake = %s, want clear", s.Status)
		}
		// A new name close to the same designation is not what the reviewer cleared.
		e.w9as("twice", "Sinister Shipping Company Holdings", "12-0000002")
		e.clear("acme", map[string]any{"payee": "twice", "amount": "10.00"})
		if ms = e.queued("twice"); len(ms) != 1 || ms[0].Status != matchOpen || len(ms[0].Hits) != 3 {
			t.Fatalf("queue = %+v, want the match reopened for the new name", ms)
		}
	})
}

// Reading an org's principal records nothing: screening's records are written
// by a clearance, the act a payment waits on.
func TestPrincipalReadWritesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.form("sinister", "Sinister Shipping Co.", "Jane Doe")
		if s := e.status("sinister"); s.Status != screenReview {
			t.Fatalf("sinister = %s, want review", s.Status)
		}
		if n := len(e.queued("sinister")); n != 0 {
			t.Fatalf("a GET recorded %d matches", n)
		}
		if slices.Contains(e.actions("sinister"), "principal.match.found") {
			t.Fatal("a GET wrote to the audit trail")
		}
		e.clear("acme", map[string]any{"payee": "sinister", "amount": "10.00"})
		if n := len(e.queued("sinister")); n != 1 {
			t.Fatalf("a clearance recorded %d matches, want 1", n)
		}
	})
}

// A residence or organization in a comprehensively embargoed jurisdiction is put
// before a platform reviewer, whatever the org is called.
func TestEmbargoedJurisdiction(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.w8as("pars", "Pars Trading Co", "IR", "Tehran")
		if s := e.status("pars"); s.Status != screenReview || !slices.ContainsFunc(s.Matches, func(m iam.Match) bool { return m.List == "jurisdiction" && m.Ref == "IR" }) {
			t.Fatalf("an Iranian-resident org = %+v, want a jurisdiction match awaiting review", s)
		}
		if c := e.clear("acme", map[string]any{"payee": "pars", "amount": "10.00"}); c.Allowed || !stepOf(c, "payee_hold") {
			t.Fatalf("clearance = %+v, want held", c.Decision)
		}
		e.w8as("kyiv", "Kyiv Robotics LLC", "UA", "Kyiv")
		if s := e.status("kyiv"); s.Status != screenReview {
			t.Fatalf("a Ukrainian-resident org = %s, want review: a reviewer places the address against the embargoed regions", s.Status)
		}
		if s := e.status("wien"); s.Status != screenClear {
			t.Fatalf("an Austrian org = %s, want clear", s.Status)
		}
	})
}

// readOnly makes the store refuse writes, or take them again: the file is told to answer
// no query that changes it, and a server is told its sessions are read only, on the one
// connection the handle is held to meanwhile.
func readOnly(t *testing.T, db *sql.DB, on bool) {
	t.Helper()
	stmt, pooled := `PRAGMA query_only = `, db.Stats().MaxOpenConnections
	if dialect.Of(db).Postgres() {
		stmt = `SET default_transaction_read_only = `
		db.SetMaxOpenConns(1)
		defer db.SetMaxOpenConns(pooled)
	}
	word := map[bool]string{true: "ON", false: "OFF"}[on]
	if _, err := db.Exec(stmt + word); err != nil {
		t.Fatalf("%s%s: %v", stmt, word, err)
	}
}

// A clearance and a reviewer's decision are on the trail exactly when they are
// in the store: a write the store refuses leaves no record that it was made.
func TestDecisionsReachTheTrailWithTheStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.form("sinister", "Sinister Shipping Co.", "Jane Doe")
		e.clear("acme", map[string]any{"payee": "sinister", "amount": "10.00"})
		ms := e.queued("sinister")
		if len(ms) != 1 {
			t.Fatalf("queue = %+v", ms)
		}
		before := len(e.actions("acme"))

		db := mounted.State.store.db
		readOnly(t, db, true)
		t.Cleanup(func() { readOnly(t, db, false) })

		if code, _ := e.raw(http.MethodPost, "/v1/principal/clearance", member("acme"), map[string]any{"payee": "globex", "amount": "10.00"}); code < 500 {
			t.Fatalf("a clearance the store refused = %d, want a 5xx", code)
		}
		if got := e.actions("acme"); len(got) != before {
			t.Fatalf("a clearance the store refused is on the trail: %v", got[before:])
		}
		if code, _ := e.raw(http.MethodPost, "/v1/principal/matches/"+ms[0].ID, reviewer("admin"), map[string]any{"decision": "confirmed", "note": "x"}); code < 500 {
			t.Fatalf("a decision the store refused = %d, want a 5xx", code)
		}
		if slices.Contains(e.actions("admin"), "principal.match.decided") {
			t.Fatal("a decision the store refused is on the trail as decided")
		}
		readOnly(t, db, false)
		var m iam.Match
		e.must(200, http.MethodPost, "/v1/principal/matches/"+ms[0].ID, reviewer("admin"), map[string]any{"decision": "confirmed", "note": "x"}, &m)
		if m.Status != matchConfirmed || !slices.Contains(e.actions("admin"), "principal.match.decided") {
			t.Fatalf("decision = %+v", m)
		}
	})
}

// A publisher this process cannot reach — egress refused, a host down — leaves
// the whole set unfit: every clearance says screening is unavailable, and none is
// read as clear, while the principal read says why.
func TestAnUnreachableListClearsNobody(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		mounted.State.lists.close()
		l := newLists(func(ctx context.Context) []ScreenResult {
			r := designations(ctx)
			r[0] = ScreenResult{Source: r[0].Source, Err: errors.New("dial tcp: egress refused")}
			return r
		}, luxlog.New("test"))
		mounted.State.lists = l
		l.fit() // starts the load, as the first screening does
		deadline := time.Now().Add(10 * time.Second)
		for !slices.ContainsFunc(l.ready.Lists(), func(f ScreenFitness) bool { return !f.AttemptedAt.IsZero() }) {
			if time.Now().After(deadline) {
				t.Fatal("the lists were never attempted")
			}
			time.Sleep(10 * time.Millisecond)
		}
		c := e.clear("acme", map[string]any{"payee": "globex", "amount": "10.00"})
		if c.Allowed || !stepOf(c, "sanctions_unavailable") {
			t.Fatalf("a clearance with the OFAC list unreachable = %+v, want held on sanctions_unavailable", c.Decision)
		}
		if s := e.status("acme"); s.Status != screenUnavailable || !strings.Contains(s.Reason, "ofac") {
			t.Fatalf("acme = %s: %s, want unavailable naming the list", s.Status, s.Reason)
		}
	})
}
