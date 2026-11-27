package form

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	basepeer "github.com/hanzoai/cloud/client/base"
	"github.com/hanzoai/cloud/internal/basetest"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	forms "github.com/hanzoai/form"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// rig mounts forms over a real base app on the plane, a settings store and a KMS,
// and records what reaches the event plane.
type rig struct {
	t      *testing.T
	app    *zip.App
	stated chan *client.EventIn
	set    *planetest.Settings
}

func mount(t *testing.T) *rig {
	t.Helper()
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	basetest.Serve(t)
	r := &rig{t: t, stated: make(chan *client.EventIn, 16), set: planetest.ServeSettings(t)}
	planetest.ServeKMS(t)
	was := sendFn
	sendFn = func(_ context.Context, in *client.EventIn) (*client.EventCaptured, error) {
		r.stated <- in
		return &client.EventCaptured{Accepted: 1}, nil
	}
	t.Cleanup(func() { sendFn = was })
	cached.Store(nil)
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	app.Use(cloud.Bridge())
	// A person signed in with their own token, as the identity boundary attests one.
	app.Use(zip.H(func(c *zip.Ctx) error {
		if sub := c.Header("X-Test-Person"); sub != "" {
			principal.Mint(c, principal.Principal{Org: c.Header("X-Org-Id"), User: sub, Subject: sub, Kind: principal.Person, Stable: true, Holder: "hanzo/" + sub})
		}
		return c.Next()
	}))
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatal(err)
	}
	r.app = app
	return r
}

func (r *rig) do(method, path string, body any, hdr map[string]string) (int, string) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := r.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var (
	admin    = map[string]string{"X-Org-Id": "acme", "X-User-Id": "u1", "X-User-IsOrgAdmin": "true", "Authorization": "Bearer t"}
	operator = map[string]string{"X-Org-Id": "admin", "X-User-Id": "z", "X-User-IsAdmin": "true", "Authorization": "Bearer t"}
)

var feedback = map[string]any{
	"slug": "video-feedback", "title": "How was the video?",
	"fields": []any{
		map[string]any{"ref": "rating", "type": "rating", "title": "Rate the video", "required": true},
		map[string]any{"ref": "comment", "type": "long_text", "title": "Anything to tell us?"},
	},
	"hidden": []any{"video"},
}

// activity reads the one timeline entry a reference names from org's CRM.
func activity(t *testing.T, org, ref string) map[string]any {
	t.Helper()
	for i := 0; i < 200; i++ {
		page, err := basepeer.BaseList(plane.For(context.Background(), org), &client.BaseListIn{Collection: crm.Activities, Filter: `source_id = "` + ref + `"`})
		if err == nil && len(page.Items) == 1 {
			var a map[string]any
			_ = json.Unmarshal(page.Items[0], &a)
			return a
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no timeline entry %s in %s", ref, org)
	return nil
}

func TestAnAnonymousResponseIsTheOrgsOwnTimelineEntry(t *testing.T) {
	r := mount(t)
	if code, body := r.do("POST", "/v1/form/forms", feedback, admin); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	if code, body := r.do("GET", "/v1/form/public/acme.video-feedback", nil, nil); code != 200 || strings.Contains(body, "sinks") {
		t.Fatalf("public: %d %s", code, body)
	}
	sub := map[string]any{"token": "r_9f1c2a7b", "answers": []any{map[string]any{"field": "rating", "number": 5}, map[string]any{"field": "comment", "text": "Loved it"}},
		"hidden": map[string]any{"video": "7mVTEAHHjPc"}, "visitor": "v_k3j2h1"}
	code, body := r.do("POST", "/v1/form/public/acme.video-feedback/responses", sub, nil)
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %s", code, body)
	}
	var got forms.Receipt
	_ = json.Unmarshal([]byte(body), &got)
	if code, _ := r.do("POST", "/v1/form/public/acme.video-feedback/responses", sub, nil); code != http.StatusCreated {
		t.Fatalf("again: %d", code)
	}
	a := activity(t, "acme", "form:"+got.ID)
	if a["kind"] != "form" || a["subject"] != "How was the video?" || a["person"] != "" {
		t.Fatalf("entry %v", a)
	}
	data, _ := a["data"].(map[string]any)
	if data["visitor"] != "v_k3j2h1" || data["public_id"] != "acme.video-feedback" {
		t.Fatalf("entry data %v", data)
	}
	page, err := basepeer.BaseList(plane.For(context.Background(), "acme"), &client.BaseListIn{Collection: forms.Responses})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("two clicks are one response: %v %v", page, err)
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case in := <-r.stated:
			seen[in.Name] = true
			if in.Product != "form" {
				t.Fatalf("stated as %q", in.Product)
			}
			for _, s := range in.Attributes {
				if strings.Contains(s.Value, "Loved") {
					t.Fatalf("an event carries what was said: %+v", in)
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("stated %v", seen)
		}
	}
}

func TestASignedInResponseIsOnItsPerson(t *testing.T) {
	r := mount(t)
	r.do("POST", "/v1/form/forms", feedback, admin)
	sub := map[string]any{"token": "r_signed01", "answers": []any{map[string]any{"field": "rating", "number": 4}}}
	code, body := r.do("POST", "/v1/form/public/acme.video-feedback/responses", sub,
		map[string]string{"X-Test-Person": "4f1c0e8a", "X-Org-Id": "ada-org", "X-User-Id": "4f1c0e8a", "X-User-Email": "ada@example.com", "Authorization": "Bearer t"})
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %s", code, body)
	}
	var got forms.Receipt
	_ = json.Unmarshal([]byte(body), &got)
	a := activity(t, "acme", "form:"+got.ID)
	if a["person"] != crm.PersonID("ada@example.com") {
		t.Fatalf("entry is on %v", a["person"])
	}
	for {
		select {
		case in := <-r.stated:
			for _, s := range in.Attributes {
				if strings.Contains(s.Value, "@") {
					t.Fatalf("an event carries an address: %+v", in)
				}
			}
			if in.Name == forms.Submitted {
				for _, s := range in.Attributes {
					if s.Name == "person_id" && s.Value == crm.PersonID("ada@example.com") {
						return
					}
				}
				t.Fatalf("form_submitted carries no person_id: %+v", in)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("form_submitted was not stated")
		}
	}
}

// A session cookie names no respondent: only a credential the caller presented
// in a header does.
func TestACookieIsNobody(t *testing.T) {
	r := mount(t)
	r.do("POST", "/v1/form/forms", feedback, admin)
	sub := map[string]any{"token": "r_cookie01", "answers": []any{map[string]any{"field": "rating", "number": 4}}}
	code, body := r.do("POST", "/v1/form/public/acme.video-feedback/responses", sub,
		map[string]string{"X-Test-Person": "4f1c0e8a", "X-Org-Id": "ada-org", "X-User-Id": "4f1c0e8a", "X-User-Email": "ada@example.com", "Cookie": "hanzo_iam_token=t"})
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %s", code, body)
	}
	var got forms.Receipt
	_ = json.Unmarshal([]byte(body), &got)
	if a := activity(t, "acme", "form:"+got.ID); a["person"] != "" {
		t.Fatalf("a cookie named %v", a["person"])
	}
}

// A typed address is a claim: on a person the CRM holds it is marked unverified
// and changes nothing about them; on anyone else it is the org's own entry.
func TestATypedAddressIsAnUnverifiedClaim(t *testing.T) {
	r := mount(t)
	form := map[string]any{"slug": "contact", "title": "Contact", "fields": []any{
		map[string]any{"ref": "email", "type": "email", "title": "Email", "required": true},
		map[string]any{"ref": "name", "type": "short_text", "title": "Name"},
	}, "sinks": map[string]any{"crm": map[string]any{"name": "name", "list": "Customers"}}}
	if code, body := r.do("POST", "/v1/form/forms", form, admin); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	ctx := plane.For(context.Background(), "acme")
	if err := crm.Install(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	held := crm.PersonID("ceo@acme.example")
	raw, _ := json.Marshal(map[string]any{"id": held, "emails": map[string]any{"primaryEmail": "ceo@acme.example"}})
	if _, err := basepeer.BaseUpsert(ctx, &client.BaseUpsertIn{Collection: crm.People, Records: []json.RawMessage{raw}}); err != nil {
		t.Fatal(err)
	}
	for i, address := range []string{"ceo@acme.example", "nobody@acme.example"} {
		sub := map[string]any{"token": "r_claim0" + strconv.Itoa(i), "answers": []any{
			map[string]any{"field": "email", "text": address}, map[string]any{"field": "name", "text": "Mallory"},
		}}
		code, body := r.do("POST", "/v1/form/public/acme.contact/responses", sub, nil)
		if code != http.StatusCreated {
			t.Fatalf("submit: %d %s", code, body)
		}
		var got forms.Receipt
		_ = json.Unmarshal([]byte(body), &got)
		a := activity(t, "acme", "form:"+got.ID)
		want := ""
		if address == "ceo@acme.example" {
			want = held
		}
		if a["person"] != want || a["subject"] != "Unverified: Contact" {
			t.Fatalf("%s: entry %v", address, a)
		}
	}
	got, err := basepeer.BaseGet(ctx, &client.BaseGetIn{Collection: crm.People, ID: held})
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	_ = json.Unmarshal(got.Record, &p)
	if name, _ := p["name"].(map[string]any); name != nil && name["firstName"] == "Mallory" {
		t.Fatalf("a claim filled the person: %v", p)
	}
	if page, _ := basepeer.BaseList(ctx, &client.BaseListIn{Collection: crm.ListMembers, PerPage: 1}); page != nil && page.TotalItems > 0 {
		t.Fatalf("a claim joined a list: %d members", page.TotalItems)
	}
	if page, _ := basepeer.BaseList(ctx, &client.BaseListIn{Collection: crm.People, PerPage: 10}); page == nil || page.TotalItems != 1 {
		t.Fatalf("a claim made a person: %v", page)
	}
}

// A claimed address on the brand's newsletter form is asked the double opt-in.
func TestAClaimIsAskedToConfirm(t *testing.T) {
	r := mount(t)
	t.Setenv("CLOUD_BRAND", "acme")
	asked := make(chan *client.SubscribeAsk, 4)
	was := subscribeFn
	subscribeFn = func(_ context.Context, in *client.SubscribeAsk) (*struct{}, error) {
		asked <- in
		return &struct{}{}, nil
	}
	t.Cleanup(func() { subscribeFn = was })
	form := map[string]any{"slug": "news", "kind": "subscribe", "title": "News", "fields": []any{
		map[string]any{"ref": "email", "type": "email", "title": "Email", "required": true},
	}, "sinks": map[string]any{"crm": map[string]any{"list": "Newsletter"}}}
	if code, body := r.do("POST", "/v1/form/forms", form, admin); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	r.do("POST", "/v1/form/public/acme.news/responses", map[string]any{"answers": []any{map[string]any{"field": "email", "text": "ada@example.com"}}}, nil)
	select {
	case in := <-asked:
		if in.Address != "ada@example.com" || in.Source != "form:acme.news" {
			t.Fatalf("asked %+v", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no double opt-in was asked")
	}
}

// A welcome goes to no address on a suppression list.
func TestAWelcomeIsSuppressed(t *testing.T) {
	sent := make(chan *client.Send, 4)
	was, cl := tellFn, clearFn
	tellFn = func(_ context.Context, in *client.Send) (*client.Sent, error) { sent <- in; return &client.Sent{}, nil }
	clearFn = func(context.Context, *client.Addressee) (*client.Clearance, error) {
		return &client.Clearance{Refused: "unsubscribed"}, nil
	}
	t.Cleanup(func() { tellFn, clearFn = was, cl })
	if err := mail(context.Background(), "acme", forms.Mail{To: "ada@example.com", Kind: "welcome", Form: "f", Subject: "Hi", Text: "Hi"}); err == nil || !strings.Contains(err.Error(), "suppression") {
		t.Fatalf("a suppressed welcome: %v", err)
	}
	if err := mail(context.Background(), "acme", forms.Mail{To: "sales@acme.example", Kind: "forward", Form: "f", Subject: "New", Text: "x"}); err != nil {
		t.Fatalf("a forward: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d", len(sent))
	}
}

// One IPv6 host cannot walk around the rate by changing its address.
func TestAnIPv6HostIsOneBudget(t *testing.T) {
	name := "test:" + t.Name()
	for i := 0; i < 3; i++ {
		ip := "2001:db8:1:2::" + strconv.Itoa(i+1)
		if !allow(context.Background(), name, ip, 3, time.Hour) {
			t.Fatalf("refused at %d", i)
		}
	}
	if allow(context.Background(), name, "2001:db8:1:2::99", 3, time.Hour) {
		t.Fatal("a fresh address in the same /64 was a fresh budget")
	}
	for i := 0; i < 3; i++ {
		_ = allow(context.Background(), name+"-", "", 3, time.Hour)
	}
	if allow(context.Background(), name+"-", "", 3, time.Hour) {
		t.Fatal("no address was no limit")
	}
}

func TestADeletedFormTakesItsResponses(t *testing.T) {
	r := mount(t)
	r.do("POST", "/v1/form/forms", feedback, admin)
	r.do("POST", "/v1/form/public/acme.video-feedback/responses", map[string]any{"answers": []any{map[string]any{"field": "rating", "number": 3}}}, nil)
	if code, body := r.do("DELETE", "/v1/form/forms/video-feedback", nil, admin); code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", code, body)
	}
	page, err := basepeer.BaseList(plane.For(context.Background(), "acme"), &client.BaseListIn{Collection: forms.Responses})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("responses left: %v %v", page, err)
	}
	if code, _ := r.do("GET", "/v1/form/public/acme.video-feedback", nil, nil); code != 404 {
		t.Fatalf("a deleted form is served: %d", code)
	}
}

// TestTheSeedStatesValidForms PUTs the two forms scripts/form-seed.sh states, as
// it states them, so a seed that drifts from what the service accepts fails here
// and not on the CTO's terminal.
func TestTheSeedStatesValidForms(t *testing.T) {
	r := mount(t)
	raw, err := os.ReadFile("../../scripts/form-seed.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"VIDEO", "ENTERPRISE", "PRODUCT_FEEDBACK", "ARTICLE_FEEDBACK", "HELP_CHAT", "SALES_CHAT", "STARTUPS_APPLY"} {
		m := regexp.MustCompile(`(?s)` + name + `='(\{.*?\})'`).FindSubmatch(raw)
		if m == nil {
			t.Fatalf("the seed states no %s", name)
		}
		var body map[string]any
		if err := json.Unmarshal(m[1], &body); err != nil {
			t.Fatalf("%s is not JSON: %v", name, err)
		}
		slug := body["slug"].(string)
		for range 2 {
			if code, out := r.do("PUT", "/v1/form/forms/"+slug, body, admin); code != 200 {
				t.Fatalf("%s: %d %s", slug, code, out)
			}
		}
		if code, out := r.do("GET", "/v1/form/public/acme."+slug, nil, nil); code != 200 {
			t.Fatalf("%s public: %d %s", slug, code, out)
		}
	}
	sub := map[string]any{"token": "r_seeded01", "answers": []any{map[string]any{"field": "note", "text": "We need SSO"}, map[string]any{"field": "email", "text": "cto@acme.example"}},
		"hidden": map[string]any{"step": "review", "seats": "40"}}
	// Signed in, the note is on the reader; the address typed beside it is theirs to give.
	code, body := r.do("POST", "/v1/form/public/acme.enterprise-feedback/responses", sub,
		map[string]string{"X-Test-Person": "9a1b", "X-Org-Id": "acme-buyer", "X-User-Id": "9a1b", "X-User-Email": "cto@acme.example", "Authorization": "Bearer t"})
	if code != http.StatusCreated {
		t.Fatalf("enterprise feedback: %d %s", code, body)
	}
	var got forms.Receipt
	_ = json.Unmarshal([]byte(body), &got)
	if a := activity(t, "acme", "form:"+got.ID); a["person"] != crm.PersonID("cto@acme.example") {
		t.Fatalf("the Enterprise note is on %v", a["person"])
	}
}

func TestOnlyASuperAdminSetsTheSettings(t *testing.T) {
	r := mount(t)
	code, body := r.do("GET", "/v1/admin/form/settings", nil, operator)
	if code != 200 || !strings.Contains(body, `"saved":false`) || !strings.Contains(body, `"long_text"`) {
		t.Fatalf("defaults: %d %s", code, body)
	}
	if code, _ := r.do("GET", "/v1/admin/form/settings", nil, admin); code != http.StatusForbidden {
		t.Fatalf("an org admin reads platform settings: %d", code)
	}
	st := forms.Defaults()
	st.Limits.Fields = 0
	if code, body := r.do("PUT", "/v1/admin/form/settings", st, operator); code != 400 || !strings.Contains(body, "limits.fields") {
		t.Fatalf("a bad document: %d %s", code, body)
	}
	st = forms.Defaults()
	st.Retention = 30
	if code, body := r.do("PUT", "/v1/admin/form/settings", st, operator); code != 200 || !strings.Contains(body, `"saved":true`) {
		t.Fatalf("save: %d %s", code, body)
	}
	if !strings.Contains(r.set.Doc("form"), `"retention":30`) {
		t.Fatalf("stored %s", r.set.Doc("form"))
	}
}

// A document saved before the mail budgets existed reads them as the defaults.
func TestASavedDocumentKeepsTheDefaults(t *testing.T) {
	r := mount(t)
	r.set.Set("form", `{"limits":{"fields":100,"hidden":10,"body":65536,"rate":30,"window":3600,"webhooks":10},"sinks":{"events":true}}`)
	st, saved, err := fetch(context.Background())
	if err != nil || !saved {
		t.Fatalf("fetch: %v saved=%v", err, saved)
	}
	if st.Limits.Forward != forms.Defaults().Limits.Forward || st.Limits.Welcome != forms.Defaults().Limits.Welcome || len(st.Types) != len(forms.Types) {
		t.Fatalf("read %+v", st.Limits)
	}
}
