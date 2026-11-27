package principals

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/tax"
	"github.com/hanzoai/cloud/client"
	principalpeer "github.com/hanzoai/cloud/client/principal"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/principal"
)

// standing reads org's standing over the plane, as the marketplace does.
func (e *env) standing(org string) client.Standing {
	e.t.Helper()
	st, err := principalpeer.PrincipalStanding(cloud.For(context.Background(), org), &client.PrincipalIn{})
	if err != nil {
		e.t.Fatalf("standing %s: %v", org, err)
	}
	return *st
}

// live is the statements in s that stand now, by agent.
func live(s client.Standing) map[string]client.Statement {
	out := map[string]client.Statement{}
	for _, st := range s.Statements {
		if st.Status == statusLive {
			out[st.Agent] = st
		}
	}
	return out
}

// A certified form signs a TaxPrincipalCredential for every agent of the org,
// verifiable offline against the published keys; a form written again withdraws
// them until it is certified again; and one that expires is withdrawn and, while
// the form stands, signed again.
func TestTaxCredentialFollowsTheForm(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		e.must(201, http.MethodPost, "/v1/principal/keys", reviewer("admin"), nil, nil)
		scout := e.agent("acme", "scout", "")
		clerk := e.agent("acme", "clerk", "")

		e.w9("acme", tax.CCorp, tax.EIN, acmeEIN)
		if got := live(e.standing("acme")); len(got) != 0 {
			t.Fatalf("an uncertified form signed %d credentials", len(got))
		}

		e.certify("acme")
		got := live(e.standing("acme"))
		if len(got) != 2 || got[scout].ID == "" || got[clerk].ID == "" {
			t.Fatalf("credentials after certification = %+v, want one per agent", got)
		}
		var keys iam.JWKS
		e.must(200, http.MethodGet, "/v1/principal/keys", who{}, nil, &keys)
		c, err := iam.Verify(got[scout].JWS, keys, time.Now())
		if err != nil {
			t.Fatalf("the credential does not verify offline: %v", err)
		}
		if c.Sub != scout || c.Org != "acme" || c.Tax == nil || c.Tax.Form != "w9" || c.Tax.Signed == 0 {
			t.Fatalf("claims = %+v, want acme's scout on a signed W-9", c)
		}
		if got[scout].Expires <= time.Now().Add(300*24*time.Hour).Unix() {
			t.Fatalf("a W-9 credential expires at %d, want about a year out", got[scout].Expires)
		}
		// Anyone holding it reads whether it stands, and nothing more.
		var status client.Statement
		e.must(200, http.MethodGet, "/v1/principal/statements/"+got[scout].ID, who{}, nil, &status)
		if status.Status != statusLive || status.JWS != "" || status.Agent != scout {
			t.Fatalf("public status = %+v, want live, no statement body", status)
		}
		// And what a shop shows beside a seller.
		pub, err := principalpeer.PrincipalPublic(cloud.For(context.Background(), ""), &client.PublicIn{Orgs: []string{"acme", "globex"}})
		if err != nil {
			t.Fatalf("public: %v", err)
		}
		if len(pub.Principals) != 2 || !pub.Principals[0].Documented || pub.Principals[1].Documented {
			t.Fatalf("public = %+v, want acme documented and globex not", pub.Principals)
		}

		// The form written again: the certification no longer covers it, so every
		// credential is withdrawn, with the reason on it.
		e.w9("acme", tax.CCorp, tax.EIN, "98-7654321")
		if n := len(live(e.standing("acme"))); n != 0 {
			t.Fatalf("%d credentials stand on a form the certification no longer covers", n)
		}
		e.must(200, http.MethodGet, "/v1/principal/statements/"+got[scout].ID, who{}, nil, &status)
		if status.Status != statusRevoked || status.Revoked == 0 {
			t.Fatalf("a withdrawn credential reads %+v", status)
		}
		e.certify("acme")
		again := live(e.standing("acme"))
		if len(again) != 2 || again[scout].ID == got[scout].ID {
			t.Fatalf("recertification signed %+v, want two new credentials", again)
		}

		// Expiry: the sweep withdraws a credential past it, and signs the agent again
		// while its principal's form stands.
		if _, err := mounted.State.store.db.Exec(`UPDATE statements SET expires = $1 WHERE id = $2`,
			time.Now().Add(-time.Minute).Unix(), again[clerk].ID); err != nil {
			t.Fatal(err)
		}
		ops{s: mounted}.sweepOnce(context.Background())
		e.must(200, http.MethodGet, "/v1/principal/statements/"+again[clerk].ID, who{}, nil, &status)
		if status.Status != statusRevoked {
			t.Fatalf("an expired credential reads %s after the sweep, want revoked", status.Status)
		}
		renewed := live(e.standing("acme"))
		if renewed[clerk].ID == "" || renewed[clerk].ID == again[clerk].ID || renewed[scout].ID != again[scout].ID {
			t.Fatalf("after expiry %+v, want clerk signed again and scout untouched", renewed)
		}
		// Every change is on the trail.
		acts := e.actions("acme")
		for _, want := range []string{"principal.statement.issued", "principal.statement.revoked"} {
			if !slices.Contains(acts, want) {
				t.Fatalf("trail %v lacks %s", acts, want)
			}
		}
	})
}

// The plane's clearance is the route's decision, recorded the same way, answered
// as the verdict a payer acts on.
func TestPlaneClearanceVerdicts(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.certify("acme")
		ask := func(payer, payee, category string) *client.Cleared {
			t.Helper()
			c, err := principalpeer.PrincipalClear(cloud.For(context.Background(), payer),
				&client.ClearIn{Payee: payee, Amount: "250.00", Category: category, Rail: client.RailX402})
			if err != nil {
				t.Fatalf("clear %s→%s: %v", payer, payee, err)
			}
			return c
		}
		// globex holds nothing of acme's yet: acme furnishes its form first, and
		// nothing else stands in the way.
		if c := ask("globex", "acme", client.CategoryService); c.Verdict != client.ClearNeedsForm || c.ID == "" {
			t.Fatalf("globex→acme before the form = %+v, want needs_form", c)
		}
		// Once acme grants its certified W-9, nothing is required first.
		e.grant("globex", "acme")
		if c := ask("globex", "acme", client.CategoryService); c.Verdict != client.ClearCleared || c.Net != "250.00" {
			t.Fatalf("globex→acme = %+v, want cleared for the whole 250.00", c)
		}
		// A refund is never a new payment to clear.
		if _, err := principalpeer.PrincipalClear(cloud.For(context.Background(), "globex"),
			&client.ClearIn{Payee: "acme", Amount: "1.00", Category: client.CategoryRefund}); err == nil {
			t.Fatal("a refund was cleared as a payment")
		}
		// Recorded, as the route records it.
		if !slices.Contains(e.actions("globex"), "principal.clearance.decided") {
			t.Fatalf("the plane's clearance is not on the trail: %v", e.actions("globex"))
		}
	})
}

// judged reads a whole decision down to the verdict.
func TestJudged(t *testing.T) {
	step := func(code string) principal.Step { return principal.Step{Code: code} }
	for _, tc := range []struct {
		name string
		cl   principal.Clearance
		want string
	}{
		{"allowed", principal.Clearance{Decision: principal.Decision{Allowed: true,
			Settlement: []principal.Method{{Rail: client.RailLedger, Net: "76.00", Withheld: "24.00"}}}}, client.ClearCleared},
		{"the form alone", principal.Clearance{Decision: principal.Decision{Required: []principal.Step{step("tax_form")},
			Facts: []principal.Fact{{Code: "us_person", Blocks: true}}}}, client.ClearNeedsForm},
		{"a form and an identity", principal.Clearance{Decision: principal.Decision{Required: []principal.Step{step("tax_form"), step("payee_identity")}}}, client.ClearBlocked},
		{"a sanctions review", principal.Clearance{Decision: principal.Decision{Required: []principal.Step{step("sanctions_review")}}}, client.ClearBlocked},
		{"an unknown source", principal.Clearance{Decision: principal.Decision{Facts: []principal.Fact{{Code: "performed", Blocks: true}}}}, client.ClearBlocked},
	} {
		got := judged(&tc.cl)
		if got.Verdict != tc.want {
			t.Errorf("%s: verdict %s, want %s (%+v)", tc.name, got.Verdict, tc.want, got)
		}
		if tc.want == client.ClearCleared && (got.Net != "76.00" || got.Withheld != "24.00") {
			t.Errorf("%s: net %s withheld %s, want the ledger method's", tc.name, got.Net, got.Withheld)
		}
	}
}

// An org certified before it had an agent is documented from its form at once,
// and the agent it makes afterwards is signed for by the sweep — nothing about
// the form changed to ask for it. A form that stops standing takes both back.
func TestAnAgentMadeAfterCertificationIsSignedFor(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		e.must(201, http.MethodPost, "/v1/principal/keys", reviewer("admin"), nil, nil)
		documented := func() bool {
			t.Helper()
			pub, err := principalpeer.PrincipalPublic(cloud.For(context.Background(), ""), &client.PublicIn{Orgs: []string{"acme"}})
			if err != nil {
				t.Fatal(err)
			}
			return pub.Principals[0].Documented
		}
		e.w9("acme", tax.CCorp, tax.EIN, acmeEIN)
		if documented() {
			t.Fatal("an uncertified form is documented")
		}
		e.certify("acme")
		if !documented() {
			t.Fatal("a certified form with no agents yet is not documented")
		}
		scout := e.agent("acme", "scout", "")
		ops{s: mounted}.sweepOnce(context.Background())
		if got := live(e.standing("acme")); got[scout].ID == "" {
			t.Fatalf("an agent made after certification holds no credential after a sweep: %+v", got)
		}
		e.w9("acme", tax.CCorp, tax.EIN, "98-7654321") // written again, not yet certified
		if documented() {
			t.Fatal("a form written again and not certified is still documented")
		}
		if got := live(e.standing("acme")); len(got) != 0 {
			t.Fatalf("credentials stand on an uncertified form: %+v", got)
		}
	})
}

// A payment already agreed is cleared again at each step its money takes, and
// that counts against the payment, not the payer: a payer that spent its hour
// asking still has a job's release cleared, but only for its own earlier
// clearance to the same payee, and one payment asked again at most twelve times
// an hour.
func TestAPaymentClearedAgainIsNotThePayersHour(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		e.form("acme", "Acme Robotics, Inc.", "Ada Lovelace")
		ask := func(payer, payee, again string) (*client.Cleared, error) {
			return principalpeer.PrincipalClear(cloud.For(context.Background(), payer), &client.ClearIn{Payee: payee, Amount: "250.00",
				Category: client.CategoryService, Rail: client.RailX402, Again: again})
		}
		quote, err := ask("acme", "seller", "")
		if err != nil {
			t.Fatal(err)
		}
		other, err := ask("acme", "other", "")
		if err != nil {
			t.Fatal(err)
		}
		for range clearsPerHour - 2 {
			if code, b := e.raw(http.MethodPost, "/v1/principal/clearance", member("acme"), map[string]any{"payee": "seller", "amount": "1.00"}); code >= 300 {
				t.Fatalf("clearance = %d %s", code, b)
			}
		}
		if _, err := ask("acme", "seller", ""); err == nil {
			t.Fatal("a new payment past the payer's hour was cleared")
		}
		// Asked again of another payee's clearance, or of none that exists, it is a new
		// ask, and the hour is spent.
		for _, again := range []string{other.ID, "clr_nothing"} {
			if _, err := ask("acme", "seller", again); err == nil {
				t.Fatalf("an ask again of %s past the payer's hour was cleared", again)
			}
		}
		e.form("globex", "Globex Corporation", "Hank Scorpio")
		if _, err := ask("globex", "seller", quote.ID); err != nil {
			t.Fatalf("globex's own first ask = %v", err)
		}
		// The payment agreed on the quote is cleared again, and again of that, up to
		// its own bound.
		last := quote.ID
		for i := range againPerHour {
			c, err := ask("acme", "seller", last)
			if err != nil {
				t.Fatalf("ask again %d past the payer's hour = %v", i+1, err)
			}
			last = c.ID
		}
		if _, err := ask("acme", "seller", last); err == nil {
			t.Fatalf("one payment was cleared again %d times in an hour", againPerHour+1)
		}
	})
}
