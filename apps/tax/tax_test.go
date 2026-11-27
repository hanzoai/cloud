package tax

// tax_test.go drives /v1/tax end to end over real payments: acme (a C
// corporation) pays globex (an individual contractor) and initech on the x402
// rail, and every property the surface promises is asked of what it answers, of
// what it stored, and of what it logged.

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	taxpeer "github.com/hanzoai/cloud/client/tax"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
)

const (
	acmeEIN   = "12-3456789"
	globexSSN = "123-45-6789"
	initEIN   = "45-6789012"
)

// world is the common setup: three companies with W-9s, and real payments from
// acme to the other two in this tax year.
func world(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.profile("acme", CCorp, EIN, acmeEIN, true)
	e.profile("globex", Individual, SSN, globexSSN, true)
	e.profile("initech", LLCP, EIN, initEIN, false)
	e.payTo("acme", "globex", 150000) // $1,500
	e.payTo("acme", "globex", 100000) // $1,000 — $2,500 together, over 2026's $2,000
	e.payTo("acme", "initech", 50000) // $500, under it
	return e
}

func year() int { return 2026 }

// A 1099 prepared from payments that really moved between two orgs on the rail.
func TestPreparedFromRealTransfers(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.certify("globex")
		e.share("acme", "globex", Services)

		// The money moved: acme's balance is down the $3,000 it paid.
		bal, err := e.ledger.Balance(context.Background(), "acme", "acme", "usd", false)
		if err != nil {
			t.Fatal(err)
		}
		if want := int64(10_000_000 - 300_000); bal.Cents() != want {
			t.Fatalf("acme's balance = %d¢, want %d¢ after paying on the rail", bal.Cents(), want)
		}

		var l Ledger
		e.must(200, http.MethodGet, "/v1/tax/payments?year=2026", admin("acme"), nil, &l)
		if l.ThresholdCents != 200000 {
			t.Fatalf("ledger header = %+v", l)
		}
		byPayee := map[string]PayeeYear{}
		for _, py := range l.Payees {
			byPayee[py.Payee] = py
		}
		g := byPayee["globex"]
		if len(g.Lines) != 2 || len(g.Totals) != 1 || g.Totals[0].AmountCents != 250000 || !g.Totals[0].Reportable {
			t.Fatalf("globex = %+v", g)
		}
		for _, ln := range g.Lines {
			if ln.Rail != client.RailX402 || ln.Proof == "" || !ln.Verdict.Reportable || ln.Verdict.Box.Box != "1a" {
				t.Fatalf("globex line = %+v", ln)
			}
		}
		i := byPayee["initech"]
		if len(i.Totals) != 1 || i.Totals[0].Reportable || i.Totals[0].Rule.Code != "below_threshold" {
			t.Fatalf("initech = %+v", i)
		}

		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		if len(p.Forms) != 1 {
			t.Fatalf("prepared %d forms, want one — initech is under the threshold: %+v", len(p.Forms), p)
		}
		f := p.Forms[0]
		if f.Kind != NEC || f.Status != Draft || f.Recipient.Org != "globex" || f.Recipient.Name != "Globex Holdings" {
			t.Fatalf("form = %+v", f)
		}
		if got := box(f.Boxes, "1a"); got != 250000 {
			t.Fatalf("box 1a = %d¢, want 250000", got)
		}
		if f.Backup.Required || !f.Certified || f.W9 != shareGranted {
			t.Fatalf("a certified, granted payee carries no backup withholding: %+v", f)
		}
		if f.Recipient.TIN != "***-**-6789" || f.Payer.TIN != "**-***6789" {
			t.Fatalf("form TINs not masked: %q %q", f.Recipient.TIN, f.Payer.TIN)
		}
		if len(f.Payments) != 2 || !slices.Contains(f.Payments, g.Lines[0].ID) || f.Due.Furnish != "2027-02-01" {
			t.Fatalf("form sources/due = %v %+v", f.Payments, f.Due)
		}
	})
}

func box(bs []Amount, b string) int64 {
	for _, a := range bs {
		if a.Box == b {
			return a.Cents
		}
	}
	return -1
}

// Furnished to a consenting payee, Copy B is in its inbox, and neither copy can
// change again.
func TestFurnishedToInboxAndImmutable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.certify("globex")
		e.share("acme", "globex", Services)
		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		id := p.Forms[0].ID

		if code := e.do(http.MethodPost, "/v1/tax/forms/"+id+"/furnish", admin("acme"), nil, nil); code != http.StatusConflict {
			t.Fatalf("furnishing an unreviewed draft = %d, want 409", code)
		}
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/review", admin("acme"), nil, nil)
		var f Form
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/furnish", admin("acme"), nil, &f)
		if f.Status != Furnished || f.Delivery != "electronic" {
			t.Fatalf("furnished = %+v", f)
		}

		var inbox statementList
		e.must(200, http.MethodGet, "/v1/tax/inbox?year=2026", member("globex"), nil, &inbox)
		if len(inbox.Data) != 1 || inbox.Data[0].ID != id || box(inbox.Data[0].Boxes, "1a") != 250000 {
			t.Fatalf("globex inbox = %+v", inbox)
		}
		if inbox.Data[0].Recipient.TIN != "***-**-6789" {
			t.Fatalf("Copy B recipient TIN = %q, want truncated", inbox.Data[0].Recipient.TIN)
		}
		code, pdf, hdr := e.raw(http.MethodGet, "/v1/tax/inbox/"+id+"/pdf", member("globex"), nil, nil)
		if code != 200 || hdr.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(string(pdf), "%PDF-1.4") ||
			!strings.Contains(string(pdf), "Copy B - For Recipient") || !strings.Contains(string(pdf), acmeEIN) ||
			strings.Contains(string(pdf), globexSSN) || !strings.Contains(string(pdf), "***-**-6789") {
			t.Fatalf("Copy B pdf = %d %s", code, pdf)
		}

		// Immutable: every door refuses.
		for path, want := range map[string]int{"/review": 409, "/void": 409, "/furnish": 200} {
			if code := e.do(http.MethodPost, "/v1/tax/forms/"+id+path, admin("acme"), nil, nil); code != want {
				t.Fatalf("%s on a furnished form = %d, want %d", path, code, want)
			}
		}
		var again Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &again)
		if len(again.Forms) != 0 || len(again.Skipped) != 1 {
			t.Fatalf("re-preparing touched a furnished form: %+v", again)
		}
		st := e.storeFor("acme")
		r, err := st.form(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		r.content = strings.Replace(r.content, "250000", "1", 1)
		if err := st.updateForm(context.Background(), *r, Furnished); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("the store rewrote a furnished form: %v", err)
		}
		if _, err := st.db.Exec(`DELETE FROM forms WHERE id = $1`, id); err == nil {
			t.Fatal("the store deleted a furnished form")
		}
		gst := e.storeFor("globex")
		if _, err := gst.db.Exec(`UPDATE received SET content = '{}' WHERE id = $1`, id); err == nil {
			t.Fatal("the store rewrote a furnished statement")
		}
		if _, err := gst.db.Exec(`DELETE FROM received WHERE id = $1`, id); err == nil {
			t.Fatal("the store deleted a furnished statement")
		}
	})
}

func (e *env) storeFor(org string) *store {
	e.t.Helper()
	st, err := ops{s: mounted}.storeOf(org)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

// A correction is a new form that supersedes the furnished one; the original is
// byte-for-byte what it was.
func TestCorrectionSupersedes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.certify("globex")
		e.share("acme", "globex", Services)
		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		id := p.Forms[0].ID
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/review", admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/furnish", admin("acme"), nil, nil)
		before, _ := e.storeFor("acme").form(context.Background(), id)

		if code := e.do(http.MethodPost, "/v1/tax/forms/"+id+"/correct", admin("acme"), map[string]any{"boxes": []Amount{{Box: "1a", Cents: 200000}}}, nil); code != 400 {
			t.Fatalf("a correction with no reason = %d, want 400", code)
		}
		var c Form
		e.must(201, http.MethodPost, "/v1/tax/forms/"+id+"/correct", admin("acme"),
			map[string]any{"reason": "a settlement was refunded", "boxes": []Amount{{Box: "1a", Cents: 200000}}}, &c)
		if !c.Corrected || c.Supersedes != id || c.Account != id || c.Status != Draft || box(c.Boxes, "1a") != 200000 {
			t.Fatalf("correction = %+v", c)
		}
		after, _ := e.storeFor("acme").form(context.Background(), id)
		if after.content != before.content || after.status != Furnished {
			t.Fatal("correcting a form changed the form it corrects")
		}
		var orig Form
		e.must(200, http.MethodGet, "/v1/tax/forms/"+id, admin("acme"), nil, &orig)
		if orig.SupersededBy != c.ID {
			t.Fatalf("original supersededBy = %q, want %q", orig.SupersededBy, c.ID)
		}
		if code := e.do(http.MethodPost, "/v1/tax/forms/"+id+"/correct", admin("acme"), map[string]any{"reason": "again"}, nil); code != 409 {
			t.Fatalf("correcting a superseded form = %d, want 409", code)
		}
		e.must(200, http.MethodPost, "/v1/tax/forms/"+c.ID+"/review", admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+c.ID+"/furnish", admin("acme"), nil, nil)
		var inbox statementList
		e.must(200, http.MethodGet, "/v1/tax/inbox", member("globex"), nil, &inbox)
		if len(inbox.Data) != 2 || inbox.Data[0].SupersededBy != c.ID || !inbox.Data[1].Corrected || inbox.Data[1].Supersedes != id {
			t.Fatalf("inbox after correction = %+v", inbox)
		}
		// The same inbox over the plane, as a seller's standing reads it: who furnished
		// each and whether a correction replaced it, and no figure.
		got, err := taxpeer.TaxInbox(cloud.For(context.Background(), "globex"), &client.InboxIn{})
		if err != nil {
			t.Fatalf("plane inbox: %v", err)
		}
		if len(got.Received) != 2 || got.Received[0].ID != id || !got.Received[0].Superseded || got.Received[0].Payer != "acme" ||
			got.Received[1].ID != c.ID || !got.Received[1].Corrected || got.Received[1].Superseded {
			t.Fatalf("plane inbox after correction = %+v", got.Received)
		}
		if other, err := taxpeer.TaxInbox(cloud.For(context.Background(), "acme"), &client.InboxIn{}); err != nil || len(other.Received) != 0 {
			t.Fatalf("the payer read the payee's inbox: %+v %v", other, err)
		}
		_, pdf, _ := e.raw(http.MethodGet, "/v1/tax/inbox/"+c.ID+"/pdf", member("globex"), nil, nil)
		if !strings.Contains(string(pdf), "(CORRECTED)") {
			t.Fatal("a corrected Copy B does not say CORRECTED")
		}
	})
}

// Backup withholding follows IRC §3406(a)(1) for these payments: required when the
// payer holds no TIN, and NOT for a W-9 that is merely uncertified — which is said,
// with the rule, as a warning.
func TestBackupWithholdingFollowsSection3406(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		prep := func() Form {
			var p Prepared
			e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
			if len(p.Forms) != 1 {
				t.Fatalf("prepared %+v", p)
			}
			return p.Forms[0]
		}
		// No W-9 at all: no TIN, so §3406(a)(1)(A) applies.
		f := prep()
		if !f.Backup.Required || f.Backup.Rate != 24 || f.Backup.DueCents != 60000 || !strings.Contains(f.Backup.Reason, "3406(a)(1)(A)") {
			t.Fatalf("no W-9 → %+v", f.Backup)
		}
		if f.Recipient.Name != "globex" || f.Recipient.TIN != "" || len(f.Notes) == 0 {
			t.Fatalf("without a W-9 the payer knows only the org id: %+v", f.Recipient)
		}
		// Granted but not certified: the TIN was furnished, and these payments do not
		// require the certification — a warning, never a withholding.
		uncertified := func(f Form) {
			t.Helper()
			if f.Backup.Required || f.Backup.DueCents != 0 || f.Certified {
				t.Fatalf("an uncertified W-9 with a TIN required backup withholding: %+v", f.Backup)
			}
			if !strings.Contains(f.Backup.Reason, "not certified") || !strings.Contains(f.Backup.Reason, "Signature requirements") ||
				!strings.Contains(f.Backup.Reason, "3406(a)(1)(B)") {
				t.Fatalf("the uncertified warning does not state its rule: %q", f.Backup.Reason)
			}
			if !slices.ContainsFunc(f.Notes, func(n string) bool { return strings.Contains(n, "not certified") }) {
				t.Fatalf("the notes do not warn of the uncertified W-9: %v", f.Notes)
			}
		}
		e.share("acme", "globex", Services)
		uncertified(prep())
		// Certified: nothing to say.
		e.certify("globex")
		f = prep()
		if f.Backup.Required || !f.Certified || f.Backup.Reason != "" {
			t.Fatalf("certified → %+v", f.Backup)
		}
		// Changing a W-9 fact voids the certification: the warning is back, and still
		// no withholding.
		e.must(200, http.MethodPut, "/v1/tax/profile", admin("globex"), map[string]any{
			"name": "Globex Holdings LLC", "classification": Individual,
			"address": map[string]any{"line1": "1 Market St", "city": "San Francisco", "state": "CA", "zip": "94105"},
			"tinType": SSN,
		}, nil)
		uncertified(prep())
	})
}

// A payer reads a payee's W-9 only while the grant is live.
func TestW9ReadNeedsLiveGrant(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		if code := e.do(http.MethodPost, "/v1/tax/w9", admin("globex"), map[string]any{"payee": "acme"}, nil); code != http.StatusConflict {
			t.Fatalf("a W-9 request with no payment from the requester = %d, want 409", code)
		}
		var w W9
		e.must(201, http.MethodPost, "/v1/tax/w9", admin("acme"), map[string]any{"payee": "globex"}, &w)
		id := w.ID
		read := func() W9 {
			var got W9
			e.must(200, http.MethodGet, "/v1/tax/w9/"+id, admin("acme"), nil, &got)
			return got
		}
		if got := read(); got.Status != shareRequested || got.Profile != nil {
			t.Fatalf("before a grant the payer read %+v", got)
		}
		if code := e.do(http.MethodGet, "/v1/tax/w9/"+id+"/tin", admin("acme"), nil, nil); code != 403 {
			t.Fatalf("full TIN before a grant = %d", code)
		}
		if code := e.do(http.MethodPost, "/v1/tax/w9/"+id+"/grant", admin("acme"), nil, nil); code != 404 {
			t.Fatalf("the payer granting its own request = %d, want 404", code)
		}
		if code := e.do(http.MethodPost, "/v1/tax/w9/"+id+"/grant", member("globex"), nil, nil); code != 403 {
			t.Fatalf("a member granting = %d, want 403", code)
		}
		e.must(200, http.MethodPost, "/v1/tax/w9/"+id+"/grant", admin("globex"), nil, nil)
		got := read()
		if got.Status != shareGranted || got.Profile == nil || got.Profile.TIN != "***-**-6789" || got.Profile.Name != "Globex Holdings" {
			t.Fatalf("after the grant the payer read %+v", got)
		}
		var full tinOut
		e.must(200, http.MethodGet, "/v1/tax/w9/"+id+"/tin", admin("acme"), nil, &full)
		if full.TIN != globexSSN {
			t.Fatalf("full TIN = %q", full.TIN)
		}
		for _, w := range []who{member("acme"), {org: "acme", super: true}, admin("globex")} {
			if code := e.do(http.MethodGet, "/v1/tax/w9/"+id+"/tin", w, nil, nil); code != 403 {
				t.Fatalf("full TIN as %+v = %d, want 403", w, code)
			}
		}
		e.must(200, http.MethodPost, "/v1/tax/w9/"+id+"/revoke", admin("globex"), nil, nil)
		if got := read(); got.Status != shareRevoked || got.Profile != nil {
			t.Fatalf("after revocation the payer read %+v", got)
		}
		if code := e.do(http.MethodGet, "/v1/tax/w9/"+id+"/tin", admin("acme"), nil, nil); code != 403 {
			t.Fatalf("full TIN after revocation = %d", code)
		}
		// The full read was audited, once, as the payer's act.
		if n := count(e.actions("acme"), "tax.w9.tin_read"); n != 1 {
			t.Fatalf("tin reads audited = %d, want 1", n)
		}
	})
}

func count(xs []string, x string) int {
	n := 0
	for _, v := range xs {
		if v == x {
			n++
		}
	}
	return n
}

// No org reaches another org's forms, statements, requests or profile.
func TestCrossOrgIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.certify("globex")
		wid := e.share("acme", "globex", Services)
		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		id := p.Forms[0].ID
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/review", admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/furnish", admin("acme"), nil, nil)

		stranger := admin("initech")
		for _, path := range []string{
			"/v1/tax/forms/" + id, "/v1/tax/forms/" + id + "/pdf", "/v1/tax/inbox/" + id, "/v1/tax/inbox/" + id + "/pdf",
			"/v1/tax/w9/" + wid, "/v1/tax/w9/" + wid + "/tin",
		} {
			if code := e.do(http.MethodGet, path, stranger, nil, nil); code != 404 {
				t.Fatalf("GET %s as a stranger = %d, want 404", path, code)
			}
		}
		for _, path := range []string{"/v1/tax/w9/" + wid + "/revoke", "/v1/tax/forms/" + id + "/correct", "/v1/tax/forms/" + id + "/void"} {
			if code := e.do(http.MethodPost, path, stranger, map[string]any{"reason": "x"}, nil); code != 404 {
				t.Fatalf("POST %s as a stranger = %d, want 404", path, code)
			}
		}
		var mine formList
		e.must(200, http.MethodGet, "/v1/tax/forms", stranger, nil, &mine)
		var inbox statementList
		e.must(200, http.MethodGet, "/v1/tax/inbox", member("initech"), nil, &inbox)
		if len(mine.Data) != 0 || len(inbox.Data) != 0 {
			t.Fatalf("a stranger lists %d forms and %d statements", len(mine.Data), len(inbox.Data))
		}
		// initech's own year shows only what initech paid — nothing.
		var l Ledger
		e.must(200, http.MethodGet, "/v1/tax/payments?year=2026", stranger, nil, &l)
		if len(l.Payees) != 0 {
			t.Fatalf("initech's year shows another org's payments: %+v", l.Payees)
		}
		// And the payee's own read of its profile is its own.
		var gp Profile
		e.must(200, http.MethodGet, "/v1/tax/profile", member("initech"), nil, &gp)
		if gp.Name != "Initech Holdings" {
			t.Fatalf("initech read %q", gp.Name)
		}
	})
}

// Only admins write; members read their own profile and inbox; a SuperAdmin acts,
// and the trail says so.
func TestAuthority(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		for _, c := range []struct{ method, path string }{
			{http.MethodPut, "/v1/tax/profile"}, {http.MethodPost, "/v1/tax/profile/certify"},
			{http.MethodPost, "/v1/tax/w9"}, {http.MethodPost, "/v1/tax/forms"}, {http.MethodGet, "/v1/tax/forms"},
			{http.MethodGet, "/v1/tax/payments?year=2026"}, {http.MethodPost, "/v1/tax/filings"},
		} {
			if code := e.do(c.method, c.path, member("acme"), map[string]any{"year": 2026}, nil); code != 403 {
				t.Fatalf("%s %s as a member = %d, want 403", c.method, c.path, code)
			}
		}
		if code := e.do(http.MethodGet, "/v1/tax/profile", who{}, nil, nil); code != 401 {
			t.Fatalf("an unauthenticated read = %d, want 401", code)
		}
		e.must(200, http.MethodGet, "/v1/tax/profile", member("acme"), nil, nil)
		e.must(200, http.MethodGet, "/v1/tax/inbox", member("acme"), nil, nil)

		e.must(200, http.MethodPost, "/v1/tax/forms", who{org: "acme", super: true}, map[string]any{"year": year()}, nil)
		rows, _, err := e.rec.Query(context.Background(), audit.Filter{Org: "acme", Action: "tax.form.prepared"})
		if err != nil || len(rows) == 0 {
			t.Fatalf("SuperAdmin's prepare was not audited: %v", err)
		}
		if !rows[0].Auth.IsAdmin || rows[0].Actor.Home != "admin" {
			t.Fatalf("the trail does not say a SuperAdmin acted: %+v %+v", rows[0].Auth, rows[0].Actor)
		}
	})
}

// The IRIS export is the file the portal rules accept, carries the full TINs, is
// rebuilt identically on read, and moves only by what the payer records.
func TestIRISExport(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.certify("globex")
		e.share("acme", "globex", Services)
		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		id := p.Forms[0].ID
		if code := e.do(http.MethodPost, "/v1/tax/filings", admin("acme"), map[string]any{"year": 2026, "kind": NEC}, nil); code != 409 {
			t.Fatalf("exporting before anything is furnished = %d, want 409", code)
		}
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/review", admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/furnish", admin("acme"), nil, nil)

		var f Filing
		e.must(201, http.MethodPost, "/v1/tax/filings", admin("acme"), map[string]any{"year": 2026, "kind": NEC}, &f)
		if f.Status != "exported" || len(f.Parts) != 1 || f.Parts[0].Type != "original" || f.Parts[0].Records != 1 || f.Due != "2027-02-01" {
			t.Fatalf("filing = %+v", f)
		}
		file := []byte(f.Parts[0].CSV)
		if errs := validate(NEC, file); len(errs) > 0 {
			t.Fatalf("the export breaks the portal rules: %v", errs)
		}
		for _, want := range []string{"123456789" /* globex's SSN, in full */, "2500.00", "Globex Holdings", "Acme Holdings"} {
			if !strings.Contains(string(file), want) {
				t.Fatalf("export lacks %q:\n%s", want, file)
			}
		}
		if !strings.Contains(string(file), strings.ReplaceAll(acmeEIN, "-", "")) {
			t.Fatal("the export does not carry the payer's full EIN")
		}
		if len(f.Steps) == 0 || !strings.Contains(f.Steps[0], "Transmitter Control Code") {
			t.Fatalf("the export does not name the owner steps: %v", f.Steps)
		}

		var again Filing
		e.must(200, http.MethodGet, "/v1/tax/filings/"+f.ID, admin("acme"), nil, &again)
		if again.Parts[0].CSV != f.Parts[0].CSV || again.Parts[0].Digest != f.Parts[0].Digest {
			t.Fatal("a re-read rebuilt a different file")
		}
		var list filingList
		e.must(200, http.MethodGet, "/v1/tax/filings", admin("acme"), nil, &list)
		if len(list.Data) != 1 || list.Data[0].Parts[0].CSV != "" {
			t.Fatalf("the listing carries a file: %+v", list)
		}
		if code := e.do(http.MethodPost, "/v1/tax/filings", admin("acme"), map[string]any{"year": 2026, "kind": NEC}, nil); code != 409 {
			t.Fatalf("exporting the same forms twice = %d, want 409", code)
		}

		// The payer records IRIS's answer; nothing else moves the status.
		if code := e.do(http.MethodPost, "/v1/tax/filings/"+f.ID+"/receipt", admin("acme"), map[string]any{"status": "accepted"}, nil); code != 409 {
			t.Fatalf("accepting a filing never submitted = %d, want 409", code)
		}
		if code := e.do(http.MethodPost, "/v1/tax/filings/"+f.ID+"/receipt", admin("acme"),
			map[string]any{"status": "submitted", "receiptId": "2027-1-abc", "tcc": "D12"}, nil); code != 400 {
			t.Fatalf("a malformed TCC = %d, want 400", code)
		}
		e.must(200, http.MethodPost, "/v1/tax/filings/"+f.ID+"/receipt", admin("acme"),
			map[string]any{"status": "submitted", "receiptId": "2027-12345678901-abc123def", "tcc": "d1234"}, &f)
		if f.Status != "submitted" || f.TCC != "D1234" {
			t.Fatalf("after submission = %+v", f)
		}
		e.must(200, http.MethodPost, "/v1/tax/filings/"+f.ID+"/receipt", admin("acme"), map[string]any{"status": "accepted"}, &f)

		// A correction of a filed form goes in a correction file, marked Corrected.
		var c Form
		e.must(201, http.MethodPost, "/v1/tax/forms/"+id+"/correct", admin("acme"),
			map[string]any{"reason": "refund", "boxes": []Amount{{Box: "1a", Cents: 210000}}}, &c)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+c.ID+"/review", admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+c.ID+"/furnish", admin("acme"), nil, nil)
		var fc Filing
		e.must(201, http.MethodPost, "/v1/tax/filings", admin("acme"), map[string]any{"year": 2026, "kind": NEC}, &fc)
		if len(fc.Parts) != 1 || fc.Parts[0].Type != "correction" || !strings.Contains(fc.Parts[0].CSV, "2100.00") {
			t.Fatalf("correction filing = %+v", fc)
		}
		if errs := validate(NEC, []byte(fc.Parts[0].CSV)); len(errs) > 0 {
			t.Fatalf("the correction file breaks the rules: %v", errs)
		}
	})
}

// Every state change lands on the trail, as the org it was made in.
func TestEveryStateChangeIsAudited(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.certify("globex")
		wid := e.share("acme", "globex", Services)
		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		id := p.Forms[0].ID
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/review", admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/furnish", admin("acme"), nil, nil)
		var c Form
		e.must(201, http.MethodPost, "/v1/tax/forms/"+id+"/correct", admin("acme"), map[string]any{"reason": "r", "boxes": []Amount{{Box: "1a", Cents: 1}}}, &c)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+c.ID+"/void", admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/w9/"+wid+"/match", admin("acme"), map[string]any{"result": "matched"}, nil)
		e.must(200, http.MethodGet, "/v1/tax/w9/"+wid+"/tin", admin("acme"), nil, nil)
		var f Filing
		e.must(201, http.MethodPost, "/v1/tax/filings", admin("acme"), map[string]any{"year": 2026, "kind": NEC}, &f)
		e.must(200, http.MethodGet, "/v1/tax/filings/"+f.ID, admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/filings/"+f.ID+"/receipt", admin("acme"),
			map[string]any{"status": "submitted", "receiptId": "r1", "tcc": "D1234"}, nil)
		e.must(200, http.MethodPost, "/v1/tax/w9/"+wid+"/revoke", admin("globex"), nil, nil)

		want := map[string][]string{
			"acme": {"tax.profile.updated", "tax.w9.requested", "tax.form.prepared", "tax.form.reviewed", "tax.form.furnished",
				"tax.form.corrected", "tax.form.voided", "tax.w9.matched", "tax.w9.tin_read", "tax.filing.exported",
				"tax.filing.read", "tax.filing.submitted"},
			"globex": {"tax.profile.updated", "tax.profile.certification_requested", "tax.profile.certified",
				"tax.w9.granted", "tax.w9.revoked"},
		}
		for org, actions := range want {
			if got := e.actions(org); !slices.Equal(got, actions) {
				t.Fatalf("%s trail = %v\nwant %v", org, got, actions)
			}
		}
		// The trail carries ids, never the number.
		rows, _, _ := e.rec.Query(context.Background(), audit.Filter{Limit: 1000})
		b, _ := json.Marshal(rows)
		for _, tin := range []string{"123456789", "123-45-6789", "12-3456789"} {
			if strings.Contains(string(b), tin) {
				t.Fatalf("the audit trail carries a TIN (%s)", tin)
			}
		}
	})
}

// A statement owed on paper is not in the inbox; the payer records mailing it.
func TestPaperWhenNotConsenting(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.profile("globex", Individual, SSN, globexSSN, false)
		e.certify("globex")
		e.share("acme", "globex", Services)
		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		id := p.Forms[0].ID
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/review", admin("acme"), nil, nil)
		var f Form
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/furnish", admin("acme"), nil, &f)
		if f.Status != Owed || f.Delivery != "paper" || f.Recipient.Address.Line1 == "" {
			t.Fatalf("owed = %+v", f)
		}
		var inbox statementList
		e.must(200, http.MethodGet, "/v1/tax/inbox", member("globex"), nil, &inbox)
		if len(inbox.Data) != 0 {
			t.Fatal("a statement owed on paper was delivered electronically without consent")
		}
		code, pdf, _ := e.raw(http.MethodGet, "/v1/tax/forms/"+id+"/pdf?copy=B", admin("acme"), nil, nil)
		if code != 200 || !strings.Contains(string(pdf), "Copy B - For Recipient") {
			t.Fatalf("the payer's printable Copy B = %d", code)
		}
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/mailed", admin("acme"), nil, &f)
		if f.Status != Furnished || f.MailedAt == 0 {
			t.Fatalf("mailed = %+v", f)
		}
	})
}

// THE TIN IS NEVER AT REST IN THE CLEAR: not in any row of any org's store, not
// in any file on disk, not in the log, not on the audit trail, and not in any
// answer but the audited full read and the filing export.
func TestTINNeverAtRestOrInLogs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.certify("globex")
		wid := e.share("acme", "globex", Services)
		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		id := p.Forms[0].ID
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/review", admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/furnish", admin("acme"), nil, nil)
		e.must(200, http.MethodGet, "/v1/tax/w9/"+wid+"/tin", admin("acme"), nil, nil)
		e.must(201, http.MethodPost, "/v1/tax/filings", admin("acme"), map[string]any{"year": 2026, "kind": NEC}, nil)

		secrets := []string{"123456789", "123-45-6789", "12-3456789", "456789012", "45-6789012"}
		leaks := func(where, s string) {
			t.Helper()
			for _, x := range secrets {
				if strings.Contains(s, x) {
					t.Fatalf("a TIN (%s) is in %s", x, where)
				}
			}
		}

		// Every answer a caller sees, except the two that exist to carry it.
		for _, c := range []struct {
			w    who
			path string
		}{
			{member("acme"), "/v1/tax/profile"}, {member("globex"), "/v1/tax/profile"}, {admin("acme"), "/v1/tax/w9"},
			{admin("acme"), "/v1/tax/w9/" + wid}, {admin("globex"), "/v1/tax/w9/" + wid}, {admin("acme"), "/v1/tax/forms"},
			{admin("acme"), "/v1/tax/forms/" + id}, {member("globex"), "/v1/tax/inbox"}, {member("globex"), "/v1/tax/inbox/" + id},
			{admin("acme"), "/v1/tax/payments?year=2026"}, {admin("acme"), "/v1/tax/filings"},
			{member("globex"), "/v1/legal/documents"},
		} {
			_, b, _ := e.raw(http.MethodGet, c.path, c.w, nil, nil)
			leaks("GET "+c.path, string(b))
		}
		// The payee's Copy B masks the payee; the payer's EIN prints, as the law requires.
		_, pdf, _ := e.raw(http.MethodGet, "/v1/tax/inbox/"+id+"/pdf", member("globex"), nil, nil)
		leaks("Copy B", strings.ReplaceAll(string(pdf), acmeEIN, ""))

		// Every value of every row of every org's tax store.
		for _, org := range []string{"acme", "globex", "initech"} {
			st := e.storeFor(org)
			for _, table := range []string{"profile", "payees", "shares", "forms", "received", "filings"} {
				rows, err := st.db.Query(`SELECT * FROM ` + table)
				if err != nil {
					t.Fatal(err)
				}
				cols, _ := rows.Columns()
				for rows.Next() {
					vals := make([]any, len(cols))
					ptrs := make([]any, len(cols))
					for i := range vals {
						ptrs[i] = &vals[i]
					}
					if err := rows.Scan(ptrs...); err != nil {
						t.Fatal(err)
					}
					for i, v := range vals {
						var s string
						switch x := v.(type) {
						case []byte:
							s = string(x)
						case string:
							s = x
						}
						leaks(org+"."+table+"."+cols[i], s)
					}
				}
				_ = rows.Close()
			}
		}

		// The log, and the trail.
		leaks("the log", e.logs.String())
		rows, _, _ := e.rec.Query(context.Background(), audit.Filter{Limit: 1000})
		b, _ := json.Marshal(rows)
		leaks("the audit trail", string(b))

		// Every file on disk, once every store has closed.
		if err := Shutdown(); err != nil {
			t.Fatal(err)
		}
		_ = filepath.WalkDir(e.dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr == nil {
				leaks(path, string(raw))
			}
			return nil
		})
	})
}

// The profile refuses a malformed W-9 and names the rule, never the number.
func TestProfileValidation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		base := map[string]any{
			"name": "Acme", "classification": CCorp, "tinType": EIN, "tin": "07-1234567",
			"address": map[string]any{"line1": "1 Market St", "city": "SF", "state": "CA", "zip": "94105"},
		}
		code, b, _ := e.raw(http.MethodPut, "/v1/tax/profile", admin("acme"), base, nil)
		if code != 400 || !strings.Contains(string(b), "EIN prefix") || strings.Contains(string(b), "071234567") {
			t.Fatalf("a bad EIN prefix = %d %s", code, b)
		}
		base["tin"], base["tinType"] = "123-45-6789", SSN
		if code := e.do(http.MethodPut, "/v1/tax/profile", admin("acme"), base, nil); code != 400 {
			t.Fatalf("a corporation stating an SSN = %d, want 400", code)
		}
		base["classification"], base["exemptPayee"] = Individual, "5"
		if code := e.do(http.MethodPut, "/v1/tax/profile", admin("acme"), base, nil); code != 400 {
			t.Fatalf("an individual with an exempt payee code = %d, want 400", code)
		}
		delete(base, "exemptPayee")
		var p Profile
		e.must(200, http.MethodPut, "/v1/tax/profile", admin("acme"), base, &p)
		if p.TIN != "***-**-6789" || p.Version != 1 || p.Certification.Status != certNone || p.Disclosure == "" {
			t.Fatalf("profile = %+v", p)
		}
		// An address change keeps the TIN without re-sending it, and makes a new version.
		delete(base, "tin")
		base["address"] = map[string]any{"line1": "2 Pine St", "city": "Oakland", "state": "CA", "zip": "94612"}
		e.must(200, http.MethodPut, "/v1/tax/profile", admin("acme"), base, &p)
		if p.TIN != "***-**-6789" || p.Version != 2 {
			t.Fatalf("after an address change = %+v", p)
		}
	})
}

// The W-9 certification is the named signer's own act: a completion another member
// of the org records in legal does not certify it, and opens a fresh request.
func TestCertifiedOnlyByItsSigner(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		// Platform authority does not sign a company's certification.
		if code := e.do(http.MethodPost, "/v1/tax/profile/certify", who{org: "globex", super: true}, nil, nil); code != 403 {
			t.Fatalf("a SuperAdmin certifying globex's W-9 = %d, want 403", code)
		}
		var p Profile
		e.must(200, http.MethodPost, "/v1/tax/profile/certify", admin("globex"), nil, &p)
		first := p.Certification.Document
		if p.Certification.Status != certPending || first == "" || p.Certification.Signer != "u_globex" {
			t.Fatalf("certify opened %+v", p.Certification)
		}
		// A clerk — a member, not the admin who is the signer — marks it signed.
		clerk := map[string]string{"X-User-Id": "u_clerk", "X-User-Email": "clerk@globex.test"}
		code, b, _ := e.raw(http.MethodPost, "/v1/legal/documents/"+first+"/sign/complete", member("globex"),
			map[string]any{"signed": true}, clerk)
		if code != 200 {
			t.Fatalf("the clerk's completion = %d: %s", code, b)
		}
		e.must(200, http.MethodPost, "/v1/tax/profile/certify", admin("globex"), nil, &p)
		if p.Certification.Status == certCertified {
			t.Fatal("a W-9 certified by a signature its signer never gave")
		}
		if p.Certification.Status != certPending || p.Certification.Document == first || p.Certification.Document == "" {
			t.Fatalf("a completion by someone else did not open a fresh request: %+v", p.Certification)
		}
		// The signer's own completion of the fresh request certifies it, as the signer.
		e.must(200, http.MethodPost, "/v1/legal/documents/"+p.Certification.Document+"/sign/complete", admin("globex"),
			map[string]any{"signed": true}, nil)
		e.must(200, http.MethodPost, "/v1/tax/profile/certify", admin("globex"), nil, &p)
		if p.Certification.Status != certCertified || p.Certification.By != "u_globex" {
			t.Fatalf("the signer's own completion = %+v", p.Certification)
		}
	})
}

// Every answer that carries a payee's TIN in full is the payer's own admin's: a
// SuperAdmin acting inside the org cannot take the export, or re-read it, as the
// way around the TIN read it is refused.
func TestSuperAdminNeverHoldsATIN(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.certify("globex")
		e.share("acme", "globex", Services)
		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		id := p.Forms[0].ID
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/review", admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/furnish", admin("acme"), nil, nil)

		super := who{org: "acme", super: true}
		code, b, _ := e.raw(http.MethodPost, "/v1/tax/filings", super, map[string]any{"year": 2026, "kind": NEC}, nil)
		if code != 403 || strings.Contains(string(b), "123456789") {
			t.Fatalf("a SuperAdmin's export = %d: %s", code, b)
		}
		var f Filing
		e.must(201, http.MethodPost, "/v1/tax/filings", admin("acme"), map[string]any{"year": 2026, "kind": NEC}, &f)
		code, b, _ = e.raw(http.MethodGet, "/v1/tax/filings/"+f.ID, super, nil, nil)
		if code != 403 || strings.Contains(string(b), "123456789") {
			t.Fatalf("a SuperAdmin's re-read of the export = %d: %s", code, b)
		}
		// The listing, which carries no file, still answers a SuperAdmin.
		e.must(200, http.MethodGet, "/v1/tax/filings", super, nil, nil)
	})
}

// A filing IRIS rejected filed nothing, so its forms go out again as originals.
func TestRejectedFilingIsExportedAgain(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		e.certify("globex")
		e.share("acme", "globex", Services)
		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		id := p.Forms[0].ID
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/review", admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+id+"/furnish", admin("acme"), nil, nil)
		var f Filing
		e.must(201, http.MethodPost, "/v1/tax/filings", admin("acme"), map[string]any{"year": 2026, "kind": NEC}, &f)
		e.must(200, http.MethodPost, "/v1/tax/filings/"+f.ID+"/receipt", admin("acme"),
			map[string]any{"status": "submitted", "receiptId": "2027-1-abc", "tcc": "D1234"}, nil)
		e.must(200, http.MethodPost, "/v1/tax/filings/"+f.ID+"/receipt", admin("acme"), map[string]any{"status": "rejected"}, nil)

		var again Filing
		e.must(201, http.MethodPost, "/v1/tax/filings", admin("acme"), map[string]any{"year": 2026, "kind": NEC}, &again)
		if !slices.Equal(again.Forms, []string{id}) || len(again.Parts) != 1 || again.Parts[0].Type != "original" {
			t.Fatalf("the rejected return's forms were not exported again as originals: %+v", again)
		}
		// An accepted one stays filed.
		e.must(200, http.MethodPost, "/v1/tax/filings/"+again.ID+"/receipt", admin("acme"),
			map[string]any{"status": "submitted", "receiptId": "2027-2-abc", "tcc": "D1234"}, nil)
		e.must(200, http.MethodPost, "/v1/tax/filings/"+again.ID+"/receipt", admin("acme"), map[string]any{"status": "accepted"}, nil)
		if code := e.do(http.MethodPost, "/v1/tax/filings", admin("acme"), map[string]any{"year": 2026, "kind": NEC}, nil); code != 409 {
			t.Fatalf("exporting an accepted return again = %d, want 409", code)
		}
	})
}

// A payee cannot plant a formula in the file its payer opens in a spreadsheet: the
// profile refuses one, and the export never writes one whatever a form holds.
func TestNoFormulaReachesTheExport(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		for _, name := range []string{"=HYPERLINK(1)", "+1 Holdings", "-2 Holdings", "@SUM(A1)"} {
			code, b, _ := e.raw(http.MethodPut, "/v1/tax/profile", admin("acme"), map[string]any{
				"name": name, "classification": CCorp, "tinType": EIN, "tin": acmeEIN,
				"address": map[string]any{"line1": "1 Market St", "city": "SF", "state": "CA", "zip": "94105"},
			}, nil)
			if code != 400 {
				t.Fatalf("a profile named %q = %d: %s", name, code, b)
			}
		}
		for _, addr := range []map[string]any{
			{"line1": "=1+1", "city": "SF", "state": "CA", "zip": "94105"},
			{"line1": "1 Market St", "city": "@x", "state": "CA", "zip": "94105"},
			{"line1": "1 Market St", "city": "SF", "state": "=1", "zip": "94105"},
			{"line1": "1 Market St", "city": "SF", "state": "CA", "zip": "94105", "country": "+X"},
		} {
			if code := e.do(http.MethodPut, "/v1/tax/profile", admin("acme"), map[string]any{
				"name": "Acme", "classification": CCorp, "tinType": EIN, "tin": acmeEIN, "address": addr,
			}, nil); code != 400 {
				t.Fatalf("an address %v = %d, want 400", addr, code)
			}
		}

		// A form that holds one anyway exports without it.
		r := record1(NEC, "123-45-6789")
		r.c.Recipient.Name = "=cmd|' /C calc'!A0"
		r.c.Recipient.BusinessName = "@SUM(1+1)"
		r.c.Recipient.Address.City = "+Oakland"
		r.c.Recipient.Address.State = "-CA"
		file, err := render(NEC, []irisRecord{r})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		rows, err := csv.NewReader(bytes.NewReader(file)).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows[1:] {
			for i, cell := range row {
				if cell != "" && strings.ContainsRune("=+-@", rune(cell[0])) {
					t.Fatalf("%s is a formula in the export: %q", layouts[NEC][i].name, cell)
				}
			}
		}
	})
}

// A correction states new figures, and what should have been withheld follows
// them: a return corrected to zero owes no backup withholding.
func TestCorrectionRecomputesBackup(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := world(t)
		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		f := p.Forms[0]
		if !f.Backup.Required || f.Backup.DueCents != 60000 {
			t.Fatalf("no W-9 → %+v", f.Backup)
		}
		e.must(200, http.MethodPost, "/v1/tax/forms/"+f.ID+"/review", admin("acme"), nil, nil)
		e.must(200, http.MethodPost, "/v1/tax/forms/"+f.ID+"/furnish", admin("acme"), nil, nil)
		var c Form
		e.must(201, http.MethodPost, "/v1/tax/forms/"+f.ID+"/correct", admin("acme"),
			map[string]any{"reason": "refunded in full", "boxes": []Amount{{Box: "1a", Cents: 0}}}, &c)
		if !c.Backup.Required || c.Backup.DueCents != 0 {
			t.Fatalf("a return corrected to zero still owes %d¢ of backup withholding", c.Backup.DueCents)
		}
		// One live correction per form: a second is refused until the first is voided.
		if code := e.do(http.MethodPost, "/v1/tax/forms/"+f.ID+"/correct", admin("acme"),
			map[string]any{"reason": "again", "boxes": []Amount{{Box: "1a", Cents: 1}}}, nil); code != 409 {
			t.Fatalf("a second correction of one form = %d, want 409", code)
		}
		e.must(200, http.MethodPost, "/v1/tax/forms/"+c.ID+"/void", admin("acme"), nil, nil)
		e.must(201, http.MethodPost, "/v1/tax/forms/"+f.ID+"/correct", admin("acme"),
			map[string]any{"reason": "partly refunded", "boxes": []Amount{{Box: "1a", Cents: 100000}}}, &c)
		if c.Backup.DueCents != 24000 {
			t.Fatalf("24%% of $1,000 = %d¢", c.Backup.DueCents)
		}
	})
}

// One live return per payee, kind and year, whatever order two preparations write
// in: the second insert is refused, and a voided form gives its place back.
func TestOneLiveFormPerPayeeYear(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		st := e.storeFor("acme")
		ctx := context.Background()
		row := func(id string) formRow {
			return formRow{id: id, payee: "globex", year: 2026, kind: NEC, status: Draft, content: "{}", created: 1, updated: 1}
		}
		if err := st.insertForm(ctx, row("f1099_a")); err != nil {
			t.Fatal(err)
		}
		if err := st.insertForm(ctx, row("f1099_b")); err != errMoved {
			t.Fatalf("a second live 1099-NEC for one payee's year = %v, want refused", err)
		}
		other := row("f1099_c")
		other.kind = MISC
		if err := st.insertForm(ctx, other); err != nil {
			t.Fatalf("a 1099-MISC beside the 1099-NEC = %v", err)
		}
		void := row("f1099_a")
		void.status = Void
		if err := st.updateForm(ctx, void, Draft); err != nil {
			t.Fatal(err)
		}
		if err := st.insertForm(ctx, row("f1099_d")); err != nil {
			t.Fatalf("a form after the first was voided = %v", err)
		}
	})
}
