package crm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/basetest"
	"github.com/hanzoai/cloud/sdk/plane"
)

// The CRM's writes go to a real base app over the plane: the records meet the
// fields module crm declares, exactly as they do in production.

const org = "acme"

func open(t *testing.T) context.Context {
	t.Helper()
	basetest.Serve(t)
	ctx := plane.For(context.Background(), org)
	if err := Install(ctx, org); err != nil {
		t.Fatalf("install: %v", err)
	}
	return ctx
}

func must(t *testing.T, ctx context.Context, collection, id string) map[string]any {
	t.Helper()
	r, err := get(ctx, org, collection, id)
	if err != nil || r == nil {
		t.Fatalf("%s/%s: %v %v", collection, id, r, err)
	}
	return r
}

func count(t *testing.T, ctx context.Context, collection, filter string) int {
	t.Helper()
	page, err := listFn(plane.For(ctx, org), &client.BaseListIn{Collection: collection, Filter: filter, PerPage: 1})
	if err != nil {
		t.Fatalf("count %s: %v", collection, err)
	}
	return page.TotalItems
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var filedAt = time.Date(2026, 10, 18, 21, 30, 0, 0, time.UTC)

func betaLead() map[string]any {
	return map[string]any{
		"email":        "ada@example.com",
		"contact_name": "Ada Lovelace King",
		"company":      "Analytical",
		"size":         "1-49",
		"role":         "Founder",
		"need":         "An engine that weaves.\nAnd a second line.",
		"country":      "UK",
		"source":       "hanzo.ai/beta",
		"stage":        "new",
		"public_ref":   "lead_0123456789abcdef01234567",
		"utm_source":   "x",
		"program":      "beta",
		"offer":        "BETA-ADA",
		"answers":      []map[string]any{{"question": "What do you build?", "answer": "AI agents"}},
		"account":      "4f1c0e8a-0000-4000-8000-000000000001",
		"account_org":  "ada-lovelace",
	}
}

// A lead is a person, at lifecycle lead, and the form on their timeline holding
// every answer as it was filed.
func TestALeadIsAPersonAndItsForm(t *testing.T) {
	ctx := open(t)
	d := betaLead()
	if err := FileLead(ctx, org, "application", d, filedAt); err != nil {
		t.Fatalf("file: %v", err)
	}

	p := must(t, ctx, People, personID("ADA@example.com "))
	for field, want := range map[string]string{
		"name":      `{"firstName":"Ada","lastName":"Lovelace King"}`,
		"lifecycle": `"lead"`,
		"job_title": `"Founder"`,
		"source":    `"hanzo.ai/beta"`,
	} {
		if got := jsonOf(p[field]); got != want {
			t.Errorf("person %s = %s, want %s", field, got, want)
		}
	}
	if e, _ := p["emails"].(map[string]any); e["primaryEmail"] != "ada@example.com" {
		t.Errorf("person emails = %v", p["emails"])
	}
	if a, _ := p["address"].(map[string]any); a["addressCountry"] != "UK" {
		t.Errorf("person address = %v", p["address"])
	}

	form, err := formOf(ctx, org, "lead_0123456789abcdef01234567")
	if err != nil || form == nil {
		t.Fatalf("the form: %v %v", form, err)
	}
	if form["kind"] != "form" || form["person"] != p["id"] || form["subject"] != "Beta tester application" ||
		form["snippet"] != "An engine that weaves." || !strings.HasPrefix(form["at"].(string), "2026-10-18 21:30:00") {
		t.Fatalf("the form = %v", form)
	}
	data, _ := form["data"].(map[string]any)
	for field, want := range map[string]string{
		"kind":        `"beta"`,
		"program":     `"beta"`,
		"offer":       `"BETA-ADA"`,
		"account":     `"4f1c0e8a-0000-4000-8000-000000000001"`,
		"account_org": `"ada-lovelace"`,
		"company":     `"Analytical"`,
		"size":        `"1-49"`,
		"utm_source":  `"x"`,
		"answers":     `[{"answer":"AI agents","question":"What do you build?"}]`,
	} {
		if got := jsonOf(data[field]); got != want {
			t.Errorf("form data %s = %s, want %s", field, got, want)
		}
	}

	// The same form again is the same records.
	if err := FileLead(ctx, org, "application", d, filedAt); err != nil {
		t.Fatalf("file again: %v", err)
	}
	if n := count(t, ctx, Activities, ""); n != 1 {
		t.Fatalf("activities = %d after filing one form twice, want 1", n)
	}
	if n := count(t, ctx, People, ""); n != 1 {
		t.Fatalf("people = %d after filing one form twice, want 1", n)
	}
}

// A person's second form is a second form on their timeline; it fills what the
// person still lacks and never changes what the team has given them.
func TestASecondFormFillsAndNeverOverwrites(t *testing.T) {
	ctx := open(t)
	if err := upsert(ctx, org, "application", People, "", map[string]any{
		"id": personID("ada@example.com"), "emails": email("ada@example.com"), "job_title": "CTO", "lifecycle": "customer",
	}); err != nil {
		t.Fatal(err)
	}
	d := betaLead()
	d["program"], d["public_ref"] = "research", "lead_ffffffffffffffffffffffff"
	if err := FileLead(ctx, org, "application", d, filedAt); err != nil {
		t.Fatalf("file: %v", err)
	}
	p := must(t, ctx, People, personID("ada@example.com"))
	if p["job_title"] != "CTO" || p["lifecycle"] != "customer" {
		t.Fatalf("a form overwrote the team's values: %v", p)
	}
	if n, _ := p["name"].(map[string]any); n["firstName"] != "Ada" {
		t.Fatalf("an empty name was not filled: %v", p["name"])
	}
	if form, _ := formOf(ctx, org, "lead_ffffffffffffffffffffffff"); form == nil || form["subject"] != "Research access application" {
		t.Fatalf("the second form = %v", form)
	}
}

// A job application is a candidate, never a person sales works, and its form.
func TestAJobApplicationIsACandidateAndItsForm(t *testing.T) {
	ctx := open(t)
	d := map[string]any{
		"email":        "grace@example.com",
		"contact_name": "Grace Hopper",
		"role":         "Senior Backend Engineer",
		"job":          "senior-backend-engineer",
		"department":   "Engineering",
		"location":     "Arlington",
		"resume":       "https://example.com/grace.pdf",
		"links":        []string{"https://github.com/grace", "https://grace.dev"},
		"background":   "Compilers.\n\nCOBOL <and> more.",
		"answers":      []map[string]any{{"question": "Why us?", "answer": "Bugs."}},
		"source":       "hanzo.ai/careers",
		"stage":        "new",
		"applied":      "2026-10-17 08:00:00",
		"public_ref":   "cand_0123456789abcdef01234567",
	}
	if err := FileCandidate(ctx, org, "application", d, filedAt); err != nil {
		t.Fatalf("file: %v", err)
	}
	c := must(t, ctx, Candidates, candidateID("cand_0123456789abcdef01234567"))
	for field, want := range map[string]string{
		"name":       `{"firstName":"Grace","lastName":"Hopper"}`,
		"role":       `"Senior Backend Engineer"`,
		"job":        `"senior-backend-engineer"`,
		"stage":      `"new"`,
		"consent":    `"unasked"`,
		"public_ref": `"cand_0123456789abcdef01234567"`,
		"answers":    `[{"answer":"Bugs.","question":"Why us?"}]`,
	} {
		if got := jsonOf(c[field]); got != want {
			t.Errorf("candidate %s = %s, want %s", field, got, want)
		}
	}
	l, _ := c["links"].(map[string]any)
	if l["primaryLinkUrl"] != "https://example.com/grace.pdf" || l["primaryLinkLabel"] != "Resume" ||
		!strings.Contains(jsonOf(l["secondaryLinks"]), "https://grace.dev") {
		t.Errorf("candidate links = %v", c["links"])
	}
	if bg, _ := c["background"].(string); !strings.Contains(bg, "COBOL &lt;and&gt; more.") || strings.Contains(bg, "<and>") {
		t.Errorf("candidate background = %q: what a person wrote must stay text", bg)
	}
	if !strings.HasPrefix(c["applied"].(string), "2026-10-17 08:00:00") {
		t.Errorf("candidate applied = %v", c["applied"])
	}
	if n := count(t, ctx, People, ""); n != 0 {
		t.Fatalf("a job application made %d people", n)
	}
	form, err := formOf(ctx, org, "cand_0123456789abcdef01234567")
	if err != nil || form == nil || form["candidate"] != c["id"] || form["subject"] != "Job application: Senior Backend Engineer" {
		t.Fatalf("the form = %v %v", form, err)
	}
	if data, _ := form["data"].(map[string]any); data["kind"] != "careers" {
		t.Fatalf("the form's kind = %v", form["data"])
	}

	// The team moves the stage; the same application written again leaves it moved.
	if err := upsert(ctx, org, "application", Candidates, "", map[string]any{"id": c["id"], "stage": "interview"}); err != nil {
		t.Fatal(err)
	}
	if err := FileCandidate(ctx, org, "application", d, filedAt); err != nil {
		t.Fatalf("file again: %v", err)
	}
	if got := must(t, ctx, Candidates, c["id"].(string))["stage"]; got != "interview" {
		t.Fatalf("stage = %v after a second write, want the team's interview", got)
	}
}

// An applicant who ticks the box is a person on the careers list; one who does not
// is a candidate alone. An uploaded resume is the candidate's Resume link.
func TestOnlyAnApplicantWhoAsksJoinsTheCareersList(t *testing.T) {
	ctx := open(t)
	for _, c := range []struct{ email, consent string }{{"no@example.com", "declined"}, {"yes@example.com", "given"}} {
		d := map[string]any{"email": c.email, "contact_name": "Ada", "role": "Engineer", "consent": c.consent,
			"public_ref": "cand_" + strings.Repeat(c.email[:1], 24), "resume_file": "https://api.hanzo.ai/v1/s3/buckets/careers/objects/resumes/" + strings.Repeat("a", 24) + ".pdf"}
		if err := FileCandidate(ctx, org, "application", d, filedAt); err != nil {
			t.Fatalf("%s: %v", c.email, err)
		}
	}
	if n := count(t, ctx, People, ""); n != 1 {
		t.Fatalf("people = %d, want the one who asked", n)
	}
	if n := count(t, ctx, ListMembers, `person = "`+personID("yes@example.com")+`"`); n != 1 {
		t.Fatalf("the applicant who asked is on %d lists, want the careers list", n)
	}
	c := must(t, ctx, Candidates, candidateID("cand_"+strings.Repeat("n", 24)))
	if l, _ := c["links"].(map[string]any); l["primaryLinkUrl"] != "https://api.hanzo.ai/v1/s3/buckets/careers/objects/resumes/"+strings.Repeat("a", 24)+".pdf" || l["primaryLinkLabel"] != "Resume" || c["consent"] != "declined" {
		t.Fatalf("candidate = %v", c)
	}
}

// An investor's full address lands on a person the CRM already holds with a
// country alone, part by part, and nothing the person holds is overwritten.
func TestAFullAddressFillsAPersonPartByPart(t *testing.T) {
	ctx := open(t)
	if err := FileLead(ctx, org, "application", betaLead(), filedAt); err != nil {
		t.Fatalf("file beta lead: %v", err)
	}
	inv := map[string]any{
		"email": "ada@example.com", "contact_name": "Someone Else", "program": "investor", "status": "submitted",
		"public_ref": "lead_00000000000000000000000a", "phone": "+14155550100",
		"address": map[string]any{"street": "1 Engine Way", "city": "London", "region": "", "postcode": "N1", "country": "France"},
	}
	if err := FileLead(ctx, org, "application", inv, filedAt); err != nil {
		t.Fatalf("file investor: %v", err)
	}
	p := must(t, ctx, People, personID("ada@example.com"))
	a := p["address"].(map[string]any)
	if a["addressStreet1"] != "1 Engine Way" || a["addressCity"] != "London" || a["addressPostcode"] != "N1" || a["addressCountry"] != "UK" {
		t.Fatalf("address %v: the parts filled, the country kept", a)
	}
	if fullName(p) != "Ada Lovelace King" || p["phones"].(map[string]any)["primaryPhoneNumber"] != "+14155550100" {
		t.Fatalf("person %v", p)
	}
	page, err := listFn(plane.For(ctx, org), &client.BaseListIn{Collection: Activities, Filter: `source_id = "lead_00000000000000000000000a"`})
	if err != nil || len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), `"subject":"Investor"`) {
		t.Fatalf("the investor's form: %v %v", page, err)
	}
}

// Amend writes a change to a form's data whole, writes nothing when change says
// so, and answers nil for a reference that names no form.
func TestAmendChangesAFormsData(t *testing.T) {
	ctx := open(t)
	d := betaLead()
	if err := FileLead(ctx, org, "application", d, filedAt); err != nil {
		t.Fatalf("file: %v", err)
	}
	ref := d["public_ref"].(string)
	got, err := Amend(ctx, org, "webhook", ref, func(data map[string]any) bool { data["status"] = "completed"; return true })
	if err != nil || got["status"] != "completed" || got["offer"] != "BETA-ADA" {
		t.Fatalf("amend: %v %v", got, err)
	}
	if _, err := Amend(ctx, org, "webhook", ref, func(data map[string]any) bool { data["status"] = "lost"; return false }); err != nil {
		t.Fatal(err)
	}
	a, _ := formOf(ctx, org, ref)
	if data := a["data"].(map[string]any); data["status"] != "completed" || data["company"] != "Analytical" {
		t.Fatalf("the form's data after amending: %v", data)
	}
	if got, err := Amend(ctx, org, "webhook", "lead_none", func(map[string]any) bool { return true }); got != nil || err != nil {
		t.Fatalf("no form: %v %v", got, err)
	}
}

func fullName(p map[string]any) string {
	n, _ := p["name"].(map[string]any)
	return strings.TrimSpace(n["firstName"].(string) + " " + n["lastName"].(string))
}
