package principals

// principals_test.go drives /v1/principal end to end over the real owners: acme and globex
// are Delaware companies with verified founders, wien is an Austrian company on a
// W-8BEN-E, and every property the surface promises is asked of what it answers
// and what it recorded.

import (
	"encoding/json"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/principal"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/tax"
)

const (
	acmeEIN   = "12-3456789"
	globexEIN = "45-6789012"
	wienFTIN  = "ATU98765432"
)

// world is two verified U.S. companies and an Austrian one.
func world(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.form("acme", "Acme Robotics, Inc.", "Ada Lovelace")
	e.form("globex", "Globex Corporation", "Hank Scorpio")
	e.w9("acme", tax.CCorp, tax.EIN, acmeEIN)
	e.must(200, http.MethodPut, "/v1/tax/profile", admin("wien"), map[string]any{
		"form": "w8bene", "name": "Wien Robotik GmbH", "foreignTin": wienFTIN,
		"address": map[string]any{"line1": "Ringstrasse 1", "city": "Wien", "zip": "1010", "country": "AT"},
		"w8": map[string]any{"country": "AT", "chapter3": "corporation", "chapter4": "active_nffe", "capacity": "Director",
			"treaty": map[string]any{"country": "AT", "article": "12(2)", "rateBps": 1000, "income": "royalties", "lob": "active_business"}},
	}, nil)
	return e
}

func stepOf(c principal.Clearance, code string) bool {
	return slices.ContainsFunc(c.Required, step(code))
}
func owesOf(c principal.Clearance, code string) bool {
	return slices.ContainsFunc(c.Reporting, owes(code))
}

// A payee whose form the payer does not hold: its form is required and nothing is
// withheld on a guess. The clearance is the evidence tax accepts to ask for the
// W-9 before any payment, and once granted, a corporation is off Form 1099
// entirely.
func TestUSPayeeW9Then1099Exemption(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		c := e.clear("acme", map[string]any{"payee": "globex", "amount": "1000.00", "category": "services"})
		if c.Allowed || c.Status != statusUnknown || !stepOf(c, "tax_form") || c.Withholding.Rate != "" {
			t.Fatalf("clearance = %+v, want the payee's form required", c.Decision)
		}

		// Before any payment, the clearance lets acme ask for globex's W-9.
		e.w9("globex", tax.CCorp, tax.EIN, globexEIN)
		e.grant("acme", "globex")
		c = e.clear("acme", map[string]any{"payee": "globex", "amount": "1000.00", "category": "services"})
		if !c.Allowed || c.Withholding.Rate != "0" || owesOf(c, "1099") {
			t.Fatalf("clearance = %+v, want a corporation cleared with no 1099 and nothing withheld", c.Decision)
		}
		if len(c.Settlement) == 0 || c.Settlement[0].Net != "1000.00" {
			t.Fatalf("settlement = %+v", c.Settlement)
		}
		var got principal.Clearance
		e.must(200, http.MethodGet, "/v1/principal/clearance/"+c.ID, member("acme"), nil, &got)
		if got.ID != c.ID || got.Allowed != c.Allowed {
			t.Fatalf("the recorded clearance = %+v", got)
		}
		if n := strings.Count(strings.Join(e.actions("acme"), ","), "principal.clearance.decided"); n != 2 {
			t.Fatalf("%d clearances on the trail, want 2", n)
		}
	})
}

// A foreign payee whose W-8 the payer does not hold owes it first; once its
// certified W-8BEN-E is granted, the treaty rate it claims applies; a service
// performed abroad is withheld on by nothing; and as the payee's platform, the
// payer owes the DAC7 report — as filer, itself.
func TestForeignPayee(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		us := map[string]any{"payee": "wien", "amount": "1000.00", "category": "royalties", "performed": "US"}
		c := e.clear("acme", us)
		if c.Status != statusUnknown || !stepOf(c, "tax_form") || c.Withholding.Rate != "" || owesOf(c, "1042-S") {
			t.Fatalf("clearance = %+v, want the payee's form required and nothing guessed", c.Decision)
		}

		e.certify("wien")
		e.grant("acme", "wien")
		c = e.clear("acme", us)
		if c.Withholding.Rate != "10" || c.Withholding.Rule.Code != "treaty_rate" || stepOf(c, "w8") {
			t.Fatalf("clearance = %+v, want the 10%% treaty rate on the valid W-8BEN-E", c.Decision)
		}

		yes := true
		c = e.clear("acme", map[string]any{"payee": "wien", "amount": "1000.00", "category": "services", "performed": "AT", "platform": yes})
		if c.Withholding.Rate != "0" || c.Withholding.Rule.Code != "foreign_source" || owesOf(c, "1042-S") {
			t.Fatalf("clearance = %+v, want nothing withheld on a service performed abroad", c.Decision)
		}
		i := slices.IndexFunc(c.Reporting, owes("dac7"))
		if i < 0 || c.Reporting[i].Filer != "acme" || c.Reporting[i].Jurisdiction != "EU" {
			t.Fatalf("reporting = %+v, want DAC7 filed by acme", c.Reporting)
		}
		// wien's own founders are unverified — it formed nothing here — so it still
		// does not clear, whatever its tax status.
		if c.Allowed || !stepOf(c, "payee_identity") {
			t.Fatalf("an unverified payee cleared: %+v", c.Decision)
		}
	})
}

// A payee whose name screens close to a designation waits on a platform
// reviewer; the org it names cannot decide it; a confirmed match blocks; and a
// designated address is a hit with no review at all.
func TestSanctions(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.form("sinister", "Sinister Shipping Co.", "Jane Doe")
		body := map[string]any{"payee": "sinister", "amount": "500.00"}
		c := e.clear("acme", body)
		if c.Allowed || !stepOf(c, "payee_hold") {
			t.Fatalf("clearance = %+v, want the payment held", c.Decision)
		}
		b, _ := json.Marshal(c)
		if strings.Contains(string(b), "9001") || strings.Contains(string(b), "Sinister Shipping Company") {
			t.Fatal("the payer was shown the payee's match")
		}

		var v iam.View
		e.must(200, http.MethodGet, "/v1/principal", admin("sinister"), nil, &v)
		if v.Sanctions.Status != screenReview || len(v.Sanctions.Matches) != 1 || v.Sanctions.Matches[0].List != "ofac" {
			t.Fatalf("sinister's own view = %+v, want the OFAC match", v.Sanctions)
		}
		id := v.Sanctions.Matches[0].ID
		var mv iam.View
		e.must(200, http.MethodGet, "/v1/principal", member("sinister"), nil, &mv)
		if mv.Sanctions.Status != screenReview || len(mv.Sanctions.Matches) != 0 {
			t.Fatalf("a member's view = %+v, want the status and not the match", mv.Sanctions)
		}

		decision := map[string]any{"decision": "cleared", "note": "a namesake"}
		for _, w := range []who{admin("sinister"), member("sinister"), admin("acme")} {
			if code, _ := e.raw(http.MethodPost, "/v1/principal/matches/"+id, w, decision); code != http.StatusForbidden {
				t.Fatalf("%+v decided a sanctions match: %d", w, code)
			}
			if code, _ := e.raw(http.MethodGet, "/v1/principal/matches", w, nil); code != http.StatusForbidden {
				t.Fatalf("%+v read the review queue: %d", w, code)
			}
		}
		var q matchList
		e.must(200, http.MethodGet, "/v1/principal/matches", reviewer("admin"), nil, &q)
		if len(q.Matches) != 1 || q.Matches[0].ID != id {
			t.Fatalf("queue = %+v", q.Matches)
		}
		e.must(200, http.MethodPost, "/v1/principal/matches/"+id, reviewer("admin"), map[string]any{"decision": "confirmed", "note": "the designated person"}, nil)
		c = e.clear("acme", body)
		if c.Allowed || !stepOf(c, "payee_hold") || len(c.Settlement) != 0 {
			t.Fatalf("clearance = %+v, want held", c.Decision)
		}
		if !slices.Contains(e.actions("admin"), "principal.match.decided") {
			t.Fatalf("the decision is not on the trail: %v", e.actions("admin"))
		}

		// A designated address blocks with no review.
		var acct, w struct {
			ID      string `json:"id"`
			Address string `json:"address"`
		}
		e.must(200, http.MethodPost, "/v1/wallet/accounts", member("globex"), map[string]any{"name": "ops"}, &acct)
		e.must(200, http.MethodPost, "/v1/wallet", member("globex"), map[string]any{
			"accountId": acct.ID, "name": "treasury", "custody": "kms", "tier": "hot", "chain": "eip155:36963"}, &w)
		e.designate(w.Address)
		c = e.clear("acme", map[string]any{"payee": "globex", "amount": "10.00"})
		if !stepOf(c, "payee_hold") {
			t.Fatalf("a payee holding a designated address = %+v, want held", c.Decision)
		}
		if s := e.status("globex"); s.Status != screenBlocked {
			t.Fatalf("globex = %s, want blocked on its designated address", s.Status)
		}
	})
}

// The principal read composes every owner, and answers the detail behind a
// verdict to admins alone.
func TestPrincipalView(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.certify("acme")
		var v iam.View
		e.must(200, http.MethodGet, "/v1/principal", admin("acme"), nil, &v)
		if v.Org != "acme" || v.Identity.Status != idVerified || v.Entity == nil || v.Entity.Name != "Acme Robotics, Inc." ||
			v.Entity.EIN != "on_file" || v.Tax == nil || !v.Tax.USPerson || v.Tax.TIN != "**-***6789" || len(v.Entity.Founders) != 1 {
			t.Fatalf("admin view = %+v", v)
		}
		if v.Sanctions.Status != screenClear || !v.Compliance.Ready {
			t.Fatalf("acme = %+v / %+v, want clear and ready", v.Sanctions, v.Compliance)
		}
		for _, s := range v.Sources {
			if s.Status != "read" {
				t.Fatalf("source %+v, want every owner read", s)
			}
		}
		var mv iam.View
		e.must(200, http.MethodGet, "/v1/principal", member("acme"), nil, &mv)
		if mv.Tax.TIN != "" || mv.Entity.Founders != nil {
			t.Fatalf("a member saw the detail: %+v", mv)
		}
		var w iam.View
		e.must(200, http.MethodGet, "/v1/principal", admin("wien"), nil, &w)
		if w.Tax == nil || w.Tax.Form != "w8bene" || w.Tax.ForeignTIN != "****5432" || w.Identity.Status != idNone || w.Compliance.Ready {
			t.Fatalf("wien = %+v", w)
		}
	})
}

// No org reads another's clearance, and no clearance names another org's facts.
func TestCrossOrgIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		c := e.clear("acme", map[string]any{"payee": "globex", "amount": "1000.00"})
		if code, _ := e.raw(http.MethodGet, "/v1/principal/clearance/"+c.ID, member("globex"), nil); code != http.StatusNotFound {
			t.Fatalf("globex read acme's clearance: %d", code)
		}
		var l clearanceList
		e.must(200, http.MethodGet, "/v1/principal/clearance", member("globex"), nil, &l)
		if len(l.Clearances) != 0 {
			t.Fatalf("globex lists %d clearances it did not ask for", len(l.Clearances))
		}
		b, _ := json.Marshal(c)
		for _, x := range []string{"Hank Scorpio", "Globex Corporation", globexEIN} {
			if strings.Contains(string(b), x) {
				t.Fatalf("acme's clearance names globex's %q", x)
			}
		}
		if code, _ := e.raw(http.MethodPost, "/v1/principal/clearance", member("acme"), map[string]any{"payee": "acme", "amount": "1.00"}); code != http.StatusBadRequest {
			t.Fatalf("a clearance to itself = %d, want 400", code)
		}
		if code, _ := e.raw(http.MethodGet, "/v1/principal", who{}, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("an anonymous read = %d, want refused", code)
		}
	})
}
