package destination

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/commerce/infra"
	psembed "github.com/hanzoai/pubsub/embed"
)

// conversion_test.go holds the paid-acquisition path end to end: a click lands,
// a person signs up with it, a card settles days later, and the sale reaches
// the platforms credited to the click, with the visitor's consent, once.

// recorder captures what the fan-out states on the event plane.
type recorder struct {
	mu   sync.Mutex
	seen []*client.EventIn
}

func (r *recorder) capture(_ context.Context, in *client.EventIn) (*client.EventCaptured, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, in)
	return &client.EventCaptured{Accepted: 1}, nil
}

func (r *recorder) statuses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, in := range r.seen {
		for _, a := range in.Attributes {
			if a.Name == "status" {
				out = append(out, a.Value)
			}
		}
	}
	return out
}

func observe(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	prev, prevPause := capture, pause
	capture, pause = r.capture, func(context.Context, time.Duration) {}
	t.Cleanup(func() { capture, pause = prev, prevPause })
	return r
}

// seal connects the fake destination for org under its own token.
func seal(t *testing.T, s *cloud.Service[state], org string) {
	t.Helper()
	if err := kmsPut(context.Background(), s, org, "fake", "access_token", []byte("tok-"+org)); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := s.State.store.Upsert(context.Background(), Row{Org: org, Platform: "fake", Enabled: true, Config: Config{"pixelId": "px"}}); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

// A gclid and an fbclid in the landing URL are this event's clicks even when the
// page never lifted them into properties, and a page that did keeps its own.
func TestClicksComeOffTheLandingURL(t *testing.T) {
	at := time.Unix(1_790_000_000, 0)
	cv := Translate(event.SinkEvent{
		Name: "signup_completed", DistinctID: "sub-1", AnonymousID: "anon-1", Time: at,
		URL: "https://hanzo.ai/pricing?gclid=G-URL&fbclid=F-URL&utm_source=google",
	})
	if cv.User.click("gclid") != "G-URL" || cv.User.click("fbclid") != "F-URL" {
		t.Fatalf("clicks = %+v", cv.User.Clicks)
	}
	if !cv.User.Clicked.Equal(at) {
		t.Errorf("a URL click is as old as its event: clicked %v", cv.User.Clicked)
	}
	if got := fbcOf(cv.User); got != "fb.1.1790000000000.F-URL" {
		t.Errorf("fbc = %q: Meta reads the click's time out of it, not the send's", got)
	}
	if cv.Person != "sub-1" {
		t.Errorf("person = %q", cv.Person)
	}
	anon := Translate(event.SinkEvent{Name: "$pageview", DistinctID: "anon-1", AnonymousID: "anon-1"})
	if anon.Person != "" {
		t.Errorf("an anonymous visitor names no person: %q", anon.Person)
	}
}

// accepted is the consent of a visitor who allowed every category.
const accepted = "analytics,marketing,ads"

// consenting is props stated under that consent.
func consenting(props map[string]any) map[string]any {
	out := map[string]any{"consent": accepted}
	for k, v := range props {
		out[k] = v
	}
	return out
}

func TestConsentIsOptIn(t *testing.T) {
	if c := consentOf(nil); c.Stated || c.Analytics || c.Marketing || c.Ads {
		t.Errorf("unstated consent must read as nothing allowed: %+v", c)
	}
	c := consentOf(map[string]any{"consent": "analytics, marketing"})
	if !c.Stated || !c.Analytics || !c.Marketing || c.Ads {
		t.Errorf("stated consent: %+v", c)
	}
	if c.String() != "analytics,marketing" {
		t.Errorf("render = %q", c.String())
	}
	if c := consentOf(map[string]any{"consent": ""}); !c.Stated || c.Analytics || c.Marketing || c.Ads {
		t.Errorf("an empty choice refuses everything: %+v", c)
	}
}

// admit keeps page views and custom events off the ad platforms, honours
// consent, and sends GA4 only what gtag did not.
func TestAdmit(t *testing.T) {
	all := Consent{Analytics: true, Marketing: true, Ads: true}
	batch := []Conversion{
		{Standard: EventPurchase, Name: "order_completed", Source: sourcePlane, Consent: all},
		{Standard: EventPageView, Name: "$pageview", Consent: all},
		{Standard: EventCustom, Name: "$click", Consent: all},
		{Standard: EventSignUp, Name: "signup_completed", Consent: Consent{Stated: true, Analytics: true}},
		{Standard: EventStartCheckout, Name: "checkout_started", Consent: all, Tags: map[string]bool{"ga": true, "meta": true}},
		{Standard: EventSelect, Name: "plan_clicked", Consent: all},
	}
	names := func(cs []Conversion) string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Name)
		}
		return strings.Join(out, " ")
	}
	if got := names(admit(meta{}, nil, batch)); got != "order_completed checkout_started plan_clicked" {
		t.Errorf("meta got %q", got)
	}
	if got := names(admit(ga4{}, nil, batch)); got != "order_completed signup_completed plan_clicked" {
		t.Errorf("ga4 got %q", got)
	}
	// The hosted lens IS this plane: a copy would count every event twice and
	// Insights' capture would bring it back round. A self-hosted one takes it all.
	if got := admit(insights{}, Config{}, batch); len(got) != 0 {
		t.Errorf("the hosted lens was sent %d events", len(got))
	}
	if got := admit(insights{}, Config{"host": "https://insights.example.org"}, batch); len(got) != len(batch) {
		t.Errorf("a self-hosted lens takes the whole stream, got %d", len(got))
	}
	// Money is stated, never typed: a purchase a browser posted is not sent, and
	// any other moment it posted goes without its figures. The org's secret key
	// and a peer on the plane state money.
	typed := []Conversion{
		{Standard: EventPurchase, Name: "order_completed", Value: 1e6, Consent: all},
		{Standard: EventStartCheckout, Name: "checkout_started", Value: 1e6, Consent: all, Items: []Item{{ID: "p", Price: 1e6}}},
	}
	for _, d := range []Destination{meta{}, ga4{}, googleads{}} {
		got := admit(d, nil, typed)
		if len(got) != 1 || got[0].Standard != EventStartCheckout || got[0].Value != 0 || got[0].Items[0].Price != 0 {
			t.Errorf("%s was sent %+v", d.ID(), got)
		}
	}
	if typed[1].Value != 1e6 || typed[1].Items[0].Price != 1e6 {
		t.Error("admit changed the caller's batch")
	}
	backend := []Conversion{{Standard: EventPurchase, Name: "order_completed", Value: 20, Vouched: true, Consent: all}}
	if got := admit(meta{}, nil, backend); len(got) != 1 || got[0].Value != 20 {
		t.Errorf("the org's secret key states money: %+v", got)
	}
}

// A click recorded at signup is what a sale commerce states days later is
// credited to — with the consent the person gave, and never past the window.
func TestTouchCreditsAPeerSale(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := testStore(t)
		clicked := time.Now().Add(-48 * time.Hour)
		signup := Translate(event.SinkEvent{
			Name: "signup_completed", DistinctID: "sub-7", AnonymousID: "anon-7", Time: clicked.Add(time.Hour),
			URL: "https://hanzo.ai/auth/callback",
			Properties: map[string]any{
				"gclid": "G7", "fbp": "fb.1.1.99", "cid": "111.222", "clicked": float64(clicked.Unix()),
				"consent": "analytics,marketing", "email": "Seven@Example.com", "userAgent": "UA7",
			},
		})
		if err := st.recall(ctx, "hanzo", &signup); err != nil {
			t.Fatal(err)
		}
		sale := Translate(event.SinkEvent{
			Name: "order_completed", Source: sourcePlane, Product: "commerce", DistinctID: "sub-7",
			Time: time.Now(), Revenue: 20, Currency: "usd", Properties: map[string]any{"event_id": "sq_1", "consent": "analytics,marketing"},
		})
		if err := st.recall(ctx, "hanzo", &sale); err != nil {
			t.Fatal(err)
		}
		stale(&sale)
		u := sale.User
		if u.click("gclid") != "G7" || u.FBP != "fb.1.1.99" || u.ClientID != "111.222" || u.UserAgent != "UA7" {
			t.Errorf("the sale was not credited to the signup's touch: %+v", u)
		}
		if u.Email != "" || u.em() != sha("seven@example.com") {
			t.Errorf("the touch keeps the email's hash and never the address: email %q em %q", u.Email, u.em())
		}
		if !sale.Consent.Stated || sale.Consent.Ads || !sale.Consent.Marketing {
			t.Errorf("the sale carries its checkout's choice: %+v", sale.Consent)
		}
		if sale.URL != "https://hanzo.ai/auth/callback" {
			t.Errorf("url = %q", sale.URL)
		}

		// A new click replaces the old one whole: last click.
		again := Translate(event.SinkEvent{Name: "checkout_started", DistinctID: "sub-7", AnonymousID: "anon-7",
			Time: time.Now(), Properties: map[string]any{"wbraid": "W8", "clicked": float64(time.Now().Unix()), "consent": "analytics,marketing"}})
		if err := st.recall(ctx, "hanzo", &again); err != nil {
			t.Fatal(err)
		}
		kept, _, _ := st.touch(ctx, "hanzo", "sub-7")
		if kept.Gclid != "" || kept.Wbraid != "W8" || kept.FBP != "fb.1.1.99" {
			t.Errorf("last click: %+v", kept)
		}
		// Another org never reads this person's touch.
		if _, found, _ := st.touch(ctx, "acme", "sub-7"); found {
			t.Error("a touch leaked across orgs")
		}

		// Past the window a click is not offered at all.
		old := Conversion{Time: time.Now(), User: UserData{Clicks: map[string]string{"gclid": "OLD"}, FBC: "fb.1.x.y", Clicked: time.Now().Add(-lookback - time.Hour)}}
		stale(&old)
		if old.User.Clicks != nil || old.User.FBC != "" {
			t.Errorf("a click past the window was kept: %+v", old.User)
		}
	})
}

// failing answers each status in turn, then 200.
func failing(statuses ...int) (*httptest.Server, *int) {
	calls := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= len(statuses) {
			w.WriteHeader(statuses[calls-1])
			return
		}
		_, _ = w.Write([]byte(`{"events_received":1}`))
	})), &calls
}

// A 503 and a 429 are tried again and the third attempt lands; a 400 is final.
// Every attempt is recorded.
func TestDeliverRetriesAndRecords(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := observe(t)
		s := testService(t, newKMS(t), nil)
		cv := Conversion{Standard: EventPurchase, Name: "order_completed", EventID: "e1", URL: "https://hanzo.ai/", Consent: Consent{Marketing: true, Ads: true}, User: UserData{ExternalID: "p", UserAgent: "UA"}}

		srv, calls := failing(http.StatusServiceUnavailable, http.StatusTooManyRequests)
		defer srv.Close()
		prev := metaGraph
		metaGraph = srv.URL
		defer func() { metaGraph = prev }()
		deliver(context.Background(), s, job{org: "hanzo", dest: meta{}, cfg: Config{"pixelId": "1"}, secret: "tok", batch: []Conversion{cv}})
		if *calls != 3 {
			t.Errorf("attempts = %d, want 3", *calls)
		}
		if got := strings.Join(r.statuses(), " "); got != "retrying retrying sent" {
			t.Errorf("recorded %q", got)
		}

		r.seen = nil
		srv2, calls2 := failing(http.StatusBadRequest)
		defer srv2.Close()
		metaGraph = srv2.URL
		deliver(context.Background(), s, job{org: "hanzo", dest: meta{}, cfg: Config{"pixelId": "1"}, secret: "tok", batch: []Conversion{cv}})
		if *calls2 != 1 {
			t.Errorf("a 400 is the platform's answer and is not retried: %d attempts", *calls2)
		}
		if got := strings.Join(r.statuses(), " "); got != "failed" {
			t.Errorf("recorded %q", got)
		}
		in := r.seen[0]
		if in.Name != "destination_sent" || in.Product != auditProduct || in.Subject != "p" {
			t.Errorf("record: %+v", in)
		}
	})
}

// Meta: a sale the server stated with no page and no browser is system_generated,
// and a visitor who turned off personalised ads is Limited Data Use.
func TestMetaSourceAndLDU(t *testing.T) {
	body := metaBuild(Config{}, "tok", []Conversion{
		{Standard: EventPurchase, Name: "order_completed", EventID: "sq_1", Consent: Consent{Stated: true, Marketing: true}},
		{Standard: EventSignUp, Name: "signup_completed", URL: "https://hanzo.ai/", User: UserData{UserAgent: "UA"}, Consent: Consent{Marketing: true, Ads: true}},
		{Name: "first_action"},
	})
	// A sale is Purchase and Subscribe under the one id; first_action has no Meta name.
	if len(body.Data) != 3 {
		t.Fatalf("a moment Meta has no event for is not sent: %d events", len(body.Data))
	}
	sale, signup := body.Data[0], body.Data[2]
	if sale.ActionSource != "system_generated" || signup.ActionSource != "website" {
		t.Errorf("action_source: sale %q signup %q", sale.ActionSource, signup.ActionSource)
	}
	if len(sale.Options) != 1 || sale.Options[0] != "LDU" || *sale.Country != 1 || *sale.State != 1000 {
		t.Errorf("LDU: %+v", sale)
	}
	if signup.Options != nil {
		t.Errorf("a visitor who allowed personalised ads is not LDU: %+v", signup.Options)
	}
	raw, _ := json.Marshal(signup)
	if strings.Contains(string(raw), "data_processing_options") {
		t.Errorf("LDU fields leaked onto an allowed event: %s", raw)
	}
	if !strings.HasSuffix(metaGraph, "/v26.0") {
		t.Errorf("graph version %q", metaGraph)
	}
}

func TestMetaTestNeedsATestCode(t *testing.T) {
	_, err := meta{}.Send(context.Background(), Config{"pixelId": "1"}, "tok", []Conversion{syntheticConversion("acme", "hanzo", metaID)})
	if err == nil || !strings.Contains(err.Error(), "Test Event Code") {
		t.Fatalf("a test without a Test Event Code would record a real Lead: %v", err)
	}
}

// GA4 keys the event on the _ga client id and the person, carries ad consent,
// and a test goes to the validation server, whose findings are the answer.
func TestGA4ClientConsentAndValidation(t *testing.T) {
	groups := ga4Group([]Conversion{{Standard: EventSignUp, Name: "signup_completed", Person: "sub-1",
		Consent: Consent{Stated: true, Analytics: true, Marketing: true},
		User:    UserData{ExternalID: "sub-1", ClientID: "123.456"}}})
	g := groups[0]
	// The person rides hashed: a sender's own id may be an address.
	if g.ClientID != "123.456" || g.UserID != sha("sub-1") {
		t.Errorf("ids: %+v", g)
	}
	if g.Consent == nil || g.Consent.AdUserData != "GRANTED" || g.Consent.AdPersonalization != "DENIED" {
		t.Errorf("consent: %+v", g.Consent)
	}
	if g.Events[0].Name != "sign_up" || g.Events[0].Params["engagement_time_msec"] != 1 {
		t.Errorf("event: %+v", g.Events[0])
	}

	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"validationMessages":[{"fieldPath":"events[0].name","description":"bad","validationCode":"NAME_INVALID"}]}`))
	}))
	defer srv.Close()
	prev := ga4Debug
	ga4Debug = srv.URL + "/debug/mp/collect"
	defer func() { ga4Debug = prev }()
	_, err := ga4{}.Send(context.Background(), Config{"measurementId": "G-X"}, "s", []Conversion{syntheticConversion("acme", "hanzo", ga4ID)})
	if path != "/debug/mp/collect" || err == nil || !strings.Contains(err.Error(), "NAME_INVALID") {
		t.Fatalf("validation: path %q err %v", path, err)
	}
}

// Google answers 200 and refuses rows in partialFailureError; that is a failure,
// and a test upload is validateOnly.
func TestGoogleAdsPartialFailureAndValidate(t *testing.T) {
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"t"}`))
	}))
	defer token.Close()
	var body googleUploadBody
	answer := `{"results":[{}],"partialFailureError":{"code":3,"message":"The click is too old."}}`
	upload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(answer))
	}))
	defer upload.Close()
	ot, ou := googleOAuthURL, googleAdsAPI
	googleOAuthURL, googleAdsAPI = token.URL, upload.URL
	defer func() { googleOAuthURL, googleAdsAPI = ot, ou }()

	creds := `{"developer_token":"D","client_id":"C","client_secret":"S","refresh_token":"R"}`
	cfg := Config{"customerId": "1", "purchaseAction": "2", "leadAction": "3"}
	_, err := googleads{}.Send(context.Background(), cfg, creds, []Conversion{{Standard: EventPurchase, User: UserData{Clicks: map[string]string{"gclid": "G"}}}})
	if err == nil || !strings.Contains(err.Error(), "too old") || retryable(err) {
		t.Fatalf("partial failure must be a final failure: %v", err)
	}

	answer = `{"results":[]}`
	res, err := googleads{}.Send(context.Background(), cfg, creds, []Conversion{syntheticConversion("acme", "hanzo", googleadsID)})
	if err != nil || !body.ValidateOnly || res.Sent != 1 {
		t.Fatalf("test upload: err %v validateOnly %v sent %d", err, body.ValidateOnly, res.Sent)
	}
	if !strings.HasSuffix(ou, "/v25") {
		t.Errorf("api version %q", ou)
	}
}

// The listener reads the event plane off a real bus: a fact and the listener's
// own records are left alone, and a sale commerce stated for a paying customer
// reaches the platform org's destination and not the customer's.
func TestListenerForwardsOffTheBus(t *testing.T) {
	observe(t)
	srv, err := psembed.Open(psembed.Options{Port: -1, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("embed pubsub: %v", err)
	}
	defer srv.Shutdown()

	fd := &fakeDest{}
	s := testService(t, newKMS(t), map[string]Destination{"fake": fd})
	seal(t, s, "acme")
	seal(t, s, "hanzo")

	ctx := context.Background()
	cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{URL: srv.ClientURL(), Name: "test", EnableJetStream: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Close() }()
	if err := event.EnsureEventStream(ctx, cl); err != nil {
		t.Fatal(err)
	}
	l := newListener(s)
	l.wg.Add(1)
	go l.run(l.ctx, srv.ClientURL())
	defer l.stop()

	// The durable delivers what arrives after it exists, so wait for it.
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := cl.JetStream().Consumer(ctx, event.EventStream, durable); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the listener never created its consumer")
		}
	}
	publish := func(subject string, v any) {
		b, _ := json.Marshal(v)
		if _, err := cl.PublishToStream(ctx, subject, b); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	publish("event.act", map[string]any{"signal": "act", "org": "acme", "name": "order_completed", "id": "f1"})
	publish("event.destination_sent", event.EventEnvelope{Org: "acme", ID: "a1", Name: "order_completed", Source: sourcePlane, Product: auditProduct})
	publish("event.order_completed", event.EventEnvelope{
		Org: "acme", ID: "m1", Name: "order_completed", Source: sourcePlane, Product: "commerce",
		DistinctID: "sub-9", Time: time.Now(), Revenue: 20, Currency: "USD",
		Properties: map[string]any{"event_id": "sq_9", "consent": accepted},
	})

	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		fd.mu.Lock()
		n := len(fd.secrets)
		fd.mu.Unlock()
		if n >= 1 {
			break
		}
	}
	time.Sleep(200 * time.Millisecond) // anything extra would have arrived by now
	fd.mu.Lock()
	defer fd.mu.Unlock()
	got := append([]string(nil), fd.secrets...)
	sort.Strings(got)
	if strings.Join(got, " ") != "tok-hanzo" {
		t.Fatalf("sent with %v, want the sale once, to the platform org: the payer's own ad accounts are not told it bought from us", fd.secrets)
	}
	if len(fd.batch) != 1 || fd.batch[0].EventID != "sq_9" || fd.batch[0].Value != 20 {
		t.Fatalf("sent %+v", fd.batch)
	}
}

// A renewal names only the wallet. The first sale named the buyer and the wallet
// both, so the buyer's touch is found again under the wallet, and the renewal is
// Meta's system_generated and Google's renewal action.
func TestRenewalFindsTheBuyersTouch(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := testStore(t)
		signup := Translate(event.SinkEvent{Name: "signup_completed", DistinctID: "sub-3", AnonymousID: "anon-3", Time: time.Now(),
			URL: "https://hanzo.ai/auth/callback", Properties: map[string]any{"gclid": "G3", "userAgent": "UA", "email": "b@x.io", "consent": "analytics,marketing"}})
		if err := st.recall(ctx, "hanzo", &signup); err != nil {
			t.Fatal(err)
		}
		sale := Translate(event.SinkEvent{Name: "order_completed", Source: sourcePlane, Product: "commerce", DistinctID: "sub-3",
			Time: time.Now(), Properties: map[string]any{"event_id": "sub_1", "payer": "acme/bo"}})
		// A browser naming the wallet as its person writes nothing under it.
		forged := Translate(event.SinkEvent{Name: "signup_completed", DistinctID: "payer:acme/bo", AnonymousID: "anon-x", Time: time.Now(),
			Properties: map[string]any{"gclid": "EVIL", "consent": "analytics,marketing,ads"}})
		if err := st.recall(ctx, "hanzo", &forged); err != nil {
			t.Fatal(err)
		}
		if err := st.recall(ctx, "hanzo", &sale); err != nil {
			t.Fatal(err)
		}
		renewal := Translate(event.SinkEvent{Name: "order_completed", Source: sourcePlane, Product: "commerce", DistinctID: "acme/bo",
			Time: time.Now(), Revenue: 20, Properties: map[string]any{"event_id": "inv_2", "recurring": "true", "payer": "acme/bo"}})
		if renewal.Person != "" {
			t.Errorf("a renewal names a wallet, not a person: %q", renewal.Person)
		}
		if err := st.recall(ctx, "hanzo", &renewal); err != nil {
			t.Fatal(err)
		}
		if renewal.User.click("gclid") != "G3" || renewal.User.em() != sha("b@x.io") {
			t.Fatalf("the renewal did not find the buyer's touch: %+v", renewal.User)
		}
		if metaSource(renewal) != "system_generated" {
			t.Errorf("a renewal happened in no browser: %q", metaSource(renewal))
		}
		body := googleadsBuild(Config{"customerId": "1", "purchaseAction": "10", "renewalAction": "11"}, []Conversion{renewal, sale})
		if len(body.Conversions) != 2 || body.Conversions[0].ConversionAction != "customers/1/conversionActions/11" ||
			body.Conversions[1].ConversionAction != "customers/1/conversionActions/10" {
			t.Errorf("actions: %+v", body.Conversions)
		}
	})
}

// A lead the sales form files is stated by a peer with its page's click and
// consent as text, and the address's hash rather than the address: it still
// reaches Meta as a website Lead matched on both, under the visitor's choice.
func TestAPeerLeadCarriesItsPage(t *testing.T) {
	clicked := time.Now().Add(-time.Hour).Unix()
	cv := Translate(event.SinkEvent{Name: "generate_lead", Source: sourcePlane, Product: "marketing", DistinctID: "lead_1",
		Time: time.Now(), Properties: map[string]any{
			"event_id": "lead_1", "em": sha("ada@analytical.co"), "gclid": "G-1", "clicked": fmt.Sprint(clicked),
			"consent": "analytics,marketing", "userAgent": "UA", "url": "https://hanzo.ai/contact/sales",
		}})
	if cv.Standard != EventLead || cv.User.em() != sha("ada@analytical.co") || cv.User.Clicked.Unix() != clicked {
		t.Fatalf("lead: %+v", cv)
	}
	body := metaBuild(Config{}, "tok", admit(meta{}, nil, []Conversion{cv}))
	if len(body.Data) != 1 || body.Data[0].EventName != "Lead" || body.Data[0].ActionSource != "website" ||
		body.Data[0].EventSourceURL != "https://hanzo.ai/contact/sales" || len(body.Data[0].Options) != 1 {
		t.Fatalf("meta: %+v", body.Data)
	}
	if hashed("not-a-hash") != "" || hashed(strings.ToUpper(sha("x"))) != sha("x") {
		t.Error("only a 64-hex hash is taken as one")
	}
}

// A touch keeps no choice: a sale whose checkout stated none reaches no platform,
// however much the person allowed on an earlier page; and a touch keeps an ad id
// only from a visitor who allowed Marketing.
func TestATouchKeepsNoChoice(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := testStore(t)
		say := func(consent string) {
			cv := Translate(event.SinkEvent{Name: "checkout_started", DistinctID: "sub-r", AnonymousID: "a", Time: time.Now(),
				Properties: map[string]any{"consent": consent, "gclid": "G-" + consent, "fbp": "fb.1.1.7", "cid": "1.2"}})
			if err := st.recall(ctx, "hanzo", &cv); err != nil {
				t.Fatal(err)
			}
		}
		say("analytics")
		kept, _, _ := st.touch(ctx, "hanzo", "sub-r")
		if kept.Gclid != "" || kept.FBP != "" || kept.CID != "1.2" {
			t.Errorf("an Analytics-only visit kept %+v", kept)
		}
		say("analytics,marketing,ads")
		kept, _, _ = st.touch(ctx, "hanzo", "sub-r")
		if kept.Gclid != "G-analytics,marketing,ads" || kept.FBP != "fb.1.1.7" {
			t.Errorf("a Marketing visit kept %+v", kept)
		}
		sale := Translate(event.SinkEvent{MessageID: "m1", Name: "order_completed", Source: sourcePlane, DistinctID: "sub-r",
			Time: time.Now(), Revenue: 49, Currency: "USD", Properties: map[string]any{"event_id": "ord_1", "payer": "wallet-1"}})
		if err := st.recall(ctx, "hanzo", &sale); err != nil {
			t.Fatal(err)
		}
		if sale.Consent.Stated {
			t.Fatalf("a sale took a choice from a touch: %+v", sale.Consent)
		}
		if got := admit(meta{}, nil, []Conversion{sale}); len(got) != 0 {
			t.Fatal("a sale whose checkout stated no choice reached Meta")
		}
		refused := Translate(event.SinkEvent{MessageID: "m2", Name: "order_completed", Source: sourcePlane, DistinctID: "sub-r",
			Time: time.Now(), Revenue: 49, Currency: "USD", Properties: map[string]any{"event_id": "ord_2", "consent": ""}})
		if err := st.recall(ctx, "hanzo", &refused); err != nil {
			t.Fatal(err)
		}
		if got := admit(meta{}, nil, []Conversion{refused}); len(got) != 0 {
			t.Fatal("a sale whose checkout allowed nothing reached Meta")
		}
	})
}

// A click the page did not date is as old as the event that carried it.
func TestAnUndatedClickAges(t *testing.T) {
	old := time.Now().Add(-400 * 24 * time.Hour)
	cv := Translate(event.SinkEvent{Name: "signup_completed", Time: old, Properties: map[string]any{"gclid": "G"}})
	cv.Time = time.Now()
	stale(&cv)
	if cv.User.Clicks != nil {
		t.Errorf("a 400-day-old click was offered: %+v", cv.User.Clicks)
	}
}

// A copy a lens sent that comes back round the plane is never forwarded again,
// and one (org, platform) whose platform never answers fills its own lane and
// nobody else's: the consumer never waits on it, and what its lane cannot hold
// is recorded as not sent.
func TestALaneThatNeverAnswersHoldsNoOneElse(t *testing.T) {
	r := observe(t)
	release := make(chan struct{})
	stuck := &gate{release: release}
	fd := &fakeDest{}
	s := testService(t, newKMS(t), map[string]Destination{"stuck": stuck, "fake": fd})
	if err := kmsPut(context.Background(), s, "acme", "stuck", "access_token", []byte("t")); err != nil {
		t.Fatal(err)
	}
	if err := s.State.store.Upsert(context.Background(), Row{Org: "acme", Platform: "stuck", Enabled: true, Config: Config{"pixelId": "p"}}); err != nil {
		t.Fatal(err)
	}
	seal(t, s, "beta")
	l := newListener(s)
	ctx := context.Background()
	send := func(org string, props map[string]any) time.Duration {
		b, _ := json.Marshal(event.EventEnvelope{Org: org, ID: "x", Name: "checkout_started", Source: sourcePlane,
			Product: "shop", Time: time.Now(), Properties: consenting(props)})
		at := time.Now()
		l.handle(ctx, b)
		return time.Since(at)
	}
	send("beta", map[string]any{forwarded: "insights"})
	for range perLane + laneDepth + 10 {
		if d := send("acme", nil); d > time.Second {
			t.Fatalf("the consumer waited %v on a platform that never answers", d)
		}
	}
	send("beta", nil)
	deadline := time.Now().Add(3 * time.Second)
	for {
		fd.mu.Lock()
		n := len(fd.secrets)
		fd.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("beta's send waited behind acme's stuck platform (sends %d)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	failed := func() int {
		n := 0
		for _, st := range r.statuses() {
			if st == "failed" {
				n++
			}
		}
		return n
	}
	for end := time.Now().Add(3 * time.Second); failed() < 10 && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
	}
	if n := failed(); n != 10 {
		t.Errorf("%d sends recorded as not sent, want the 10 acme's lane could not hold", n)
	}
	close(release)
	l.work.Wait()
	if n := stuck.count(); n != perLane+laneDepth {
		t.Errorf("acme's platform was sent %d, want every send its lane held", n)
	}
	if len(fd.secrets) != 1 {
		t.Errorf("a forwarded copy was forwarded again: %v", fd.secrets)
	}
}

// gate is a platform that does not answer until released.
type gate struct {
	release chan struct{}
	mu      sync.Mutex
	n       int
}

func (g *gate) ID() string       { return "stuck" }
func (g *gate) Name() string     { return "Stuck" }
func (g *gate) Category() string { return categoryAdvertising }
func (g *gate) Spec() Spec {
	return Spec{Fields: []DestinationField{{Key: "pixelId", Required: true}}, Secrets: []string{"access_token"}}
}
func (g *gate) Send(context.Context, Config, string, []Conversion) (Result, error) {
	<-g.release
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return Result{Sent: 1}, nil
}
func (g *gate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}

// A late or redelivered event never puts an older click back, and what a
// browser writes into a touch is bounded.
func TestATouchKeepsTheLaterClickAndBoundsItself(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := testStore(t)
		now := time.Now()
		say := func(gclid string, clicked time.Time, ua string) {
			cv := Translate(event.SinkEvent{Name: "checkout_started", DistinctID: "sub-o", AnonymousID: "a", Time: now,
				Properties: map[string]any{"gclid": gclid, "clicked": float64(clicked.Unix()), "userAgent": ua, "consent": "analytics,marketing"}})
			if err := st.recall(ctx, "hanzo", &cv); err != nil {
				t.Fatal(err)
			}
		}
		say("NEW", now.Add(-time.Hour), "UA")
		say("OLD", now.Add(-48*time.Hour), strings.Repeat("x", 1<<20))
		kept, _, _ := st.touch(ctx, "hanzo", "sub-o")
		if kept.Gclid != "NEW" {
			t.Errorf("gclid = %q, want the later click kept", kept.Gclid)
		}
		if len(kept.UA) > maxLong {
			t.Errorf("a %d-byte agent was stored", len(kept.UA))
		}
	})
}

// Through the listener, as the bus hands it over: a purchase a browser posted
// reaches no ad platform, whatever key or session carried it, and the same
// purchase from the org's secret key does. The step between the envelope and
// the conversion is the one that has to carry the difference.
func TestAPurchaseIsStatedThroughTheBus(t *testing.T) {
	observe(t)
	fd := &fakeDest{}
	s := testService(t, newKMS(t), map[string]Destination{"fake": fd})
	seal(t, s, "acme")
	l := newListener(s)
	send := func(vouched bool) {
		b, _ := json.Marshal(event.EventEnvelope{Org: "acme", ID: "p", Name: "order_completed", Source: "event",
			Vouched: vouched, Time: time.Now(), Revenue: 1e6, Properties: map[string]any{"gclid": "G", "consent": accepted}})
		l.handle(context.Background(), b)
		l.work.Wait()
	}
	send(false)
	if len(fd.secrets) != 0 {
		t.Fatalf("a browser's purchase reached an ad platform: %+v", fd.batch)
	}
	send(true)
	if len(fd.secrets) != 1 || fd.batch[0].Value != 1e6 {
		t.Fatalf("the org's secret key states a sale: %+v", fd.batch)
	}
}

// A durable deleted from under the listener ends its subscription, and the
// listener makes it again rather than going quiet for good.
func TestTheListenerRemakesItsDurable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		observe(t)
		srv, err := psembed.Open(psembed.Options{Port: -1, StoreDir: t.TempDir()})
		if err != nil {
			t.Fatalf("embed pubsub: %v", err)
		}
		defer srv.Shutdown()
		s := testService(t, newKMS(t), map[string]Destination{})
		ctx := context.Background()
		cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{URL: srv.ClientURL(), Name: "test", EnableJetStream: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cl.Close() }()
		l := newListener(s)
		l.wg.Add(1)
		go l.run(l.ctx, srv.ClientURL())
		defer l.stop()
		exists := func() bool {
			_, err := cl.JetStream().Consumer(ctx, event.EventStream, durable)
			return err == nil
		}
		until := func(want bool) {
			for end := time.Now().Add(15 * time.Second); exists() != want; time.Sleep(50 * time.Millisecond) {
				if time.Now().After(end) {
					t.Fatalf("durable exists=%v, want %v", !want, want)
				}
			}
		}
		until(true)
		if err := cl.JetStream().DeleteConsumer(ctx, event.EventStream, durable); err != nil {
			t.Fatal(err)
		}
		until(false)
		until(true)
	})
}

// What is held is bounded: an envelope too large to be a conversion is not
// planned, and a flood of what anyone can send never queues ahead of a sale a
// peer stated, which has a lane of its own.
func TestASaleHasItsOwnLaneAndAFloodIsBounded(t *testing.T) {
	r := observe(t)
	release := make(chan struct{})
	stuck := &gate{release: release}
	s := testService(t, newKMS(t), map[string]Destination{"stuck": stuck})
	if err := kmsPut(context.Background(), s, "hanzo", "stuck", "access_token", []byte("t")); err != nil {
		t.Fatal(err)
	}
	if err := s.State.store.Upsert(context.Background(), Row{Org: "hanzo", Platform: "stuck", Enabled: true, Config: Config{"pixelId": "p"}}); err != nil {
		t.Fatal(err)
	}
	l := newListener(s)
	send := func(source string, props map[string]any) {
		b, _ := json.Marshal(event.EventEnvelope{Org: "hanzo", ID: "x", Name: "generate_lead", Source: source,
			Time: time.Now(), Properties: consenting(props)})
		l.handle(context.Background(), b)
	}
	send("event", map[string]any{"pad": strings.Repeat("x", maxEnvelope)})
	if l.queued != 0 {
		t.Fatalf("an oversized envelope was planned: %d queued", l.queued)
	}
	for range perLane + laneDepth + 5 {
		send("event", nil)
	}
	send(sourcePlane, nil) // the anyone lane is full; the stated lead has its own
	close(release)
	l.work.Wait()
	if n := stuck.count(); n != perLane+laneDepth+1 {
		t.Errorf("sent %d, want the anyone lane's %d and the stated lead", n, perLane+laneDepth)
	}
	failed := 0
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		failed = 0
		for _, st := range r.statuses() {
			if st == "failed" {
				failed++
			}
		}
		if failed == 5 {
			break
		}
	}
	if failed != 5 {
		t.Errorf("%d recorded as not sent, want the 5 the full lane refused", failed)
	}
}

// An org at its cap of people forgets the one seen longest ago, so a real
// visitor arriving after a flood of invented ones is still kept.
func TestTouchesEvictTheStalest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := testStore(t)
		tx, err := st.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := range maxTouches {
			if _, err := tx.ExecContext(ctx, `INSERT INTO touches (org,person,seen) VALUES ('hanzo',$1,$2)`, fmt.Sprint("junk-", i), 1000+i); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		cv := Translate(event.SinkEvent{Name: "checkout_started", DistinctID: "real", AnonymousID: "a", Time: time.Now(),
			Properties: map[string]any{"gclid": "REAL", "consent": "analytics,marketing"}})
		if err := st.recall(ctx, "hanzo", &cv); err != nil {
			t.Fatal(err)
		}
		if got, found, _ := st.touch(ctx, "hanzo", "real"); !found || got.Gclid != "REAL" {
			t.Fatal("a real visitor was refused at the cap")
		}
		if _, found, _ := st.touch(ctx, "hanzo", "junk-0"); found {
			t.Error("the stalest person was kept")
		}
		long := Translate(event.SinkEvent{Name: "checkout_started", DistinctID: strings.Repeat("p", maxID+1), AnonymousID: "a",
			Time: time.Now(), Properties: map[string]any{"gclid": "G"}})
		if err := st.recall(ctx, "hanzo", &long); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM touches WHERE length(person) > $1`, maxID).Scan(&n)
		if n != 0 {
			t.Error("an oversized person id was stored as a key")
		}
	})
}

// TestAnUnconsentedSaleReachesNoPlatform: a sale the server states names no
// consent of its own, and a buyer with no kept touch never chose. Nothing stated
// is nothing allowed, so no ad platform is told; one stating a refusal is not
// either; the same sale under an accepted choice is.
func TestAnUnconsentedSaleReachesNoPlatform(t *testing.T) {
	observe(t)
	fd := &fakeDest{}
	s := testService(t, newKMS(t), map[string]Destination{"fake": fd})
	seal(t, s, "acme")
	l := newListener(s)
	send := func(props map[string]any) {
		b, _ := json.Marshal(event.EventEnvelope{Org: "acme", ID: "p", Name: "order_completed", Source: "event",
			Vouched: true, DistinctID: "sub-1", Time: time.Now(), Revenue: 49, Currency: "USD", Properties: props})
		l.handle(context.Background(), b)
		l.work.Wait()
	}
	send(map[string]any{"event_id": "sub_1"})
	send(map[string]any{"event_id": "sub_2", "consent": ""})
	if len(fd.secrets) != 0 {
		t.Fatalf("a sale with no consent reached an ad platform: %+v", fd.batch)
	}
	send(map[string]any{"event_id": "sub_3", "consent": accepted})
	if len(fd.secrets) != 1 {
		t.Fatalf("a consented sale was not sent: %d sends", len(fd.secrets))
	}
}

// TestNoGrantForwardsNothing: the stream keeps every visitor's events (event/consent.go)
// and stamps each with the grants it may be forwarded under; one stamped with none —
// no choice, a refusal, or GPC/DNT on the request — reaches neither Google nor Meta,
// and the same moment under the grant does.
func TestNoGrantForwardsNothing(t *testing.T) {
	ev := func(consent string) []Conversion {
		var out []Conversion
		for _, name := range []string{"signup_completed", "checkout_started", "plan_clicked"} {
			out = append(out, Translate(event.SinkEvent{Name: name, MessageID: name, URL: "https://hanzo.ai/", Properties: map[string]any{"consent": consent}}))
		}
		return out
	}
	for _, d := range []Destination{ga4{}, meta{}} {
		if got := admit(d, nil, ev("")); len(got) != 0 {
			t.Errorf("%s was forwarded %d events with no grant", d.ID(), len(got))
		}
		if got := admit(d, nil, ev("analytics,marketing,ads")); len(got) == 0 {
			t.Errorf("%s was forwarded nothing under the grant", d.ID())
		}
	}
}
