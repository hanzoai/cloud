package email

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
)

// The provider events: one record each however often they arrive, the message they
// name moved, what it reached counted once, the stream told, and mail sent around
// this API filed under the provider's id for it.

// at is a SendGrid timestamp a minute into the world's day, plus n minutes.
func at(n int) int64 { return signedAt.Add(time.Duration(n) * time.Minute).Unix() }

// grid posts a SendGrid delivery of events, signed, and answers the status.
func (w *world) grid(t *testing.T, events ...string) int {
	t.Helper()
	body := "[" + strings.Join(events, ",") + "]"
	return w.post(t, "/v1/email/webhooks/sendgrid?org=acme", []byte(body), w.delivery(t, body, w.ec, signedAt))
}

// ses posts one SES event as a signed SNS notification with its own id.
func (w *world) ses(t *testing.T, id, event string) int {
	t.Helper()
	return w.post(t, "/v1/email/webhooks/ses?org=acme", js(w.notification("2", func(e *snsEnvelope) {
		e.MessageID, e.Message = id, event
	})), nil)
}

// tally is the world's day counted, in all and by campaign.
func (w *world) tally(t *testing.T) *Stats {
	t.Helper()
	s, err := w.svc.Stats(context.Background(), "acme", &StatsInput{
		Since: signedAt.Add(-48 * time.Hour).Format(time.RFC3339), Until: signedAt.Add(48 * time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// statements are what the stream was told, by name, of the messages named (of any
// when none is).
func (w *world) statements(name string, of ...string) []*client.EventIn {
	w.plane.mu.Lock()
	defer w.plane.mu.Unlock()
	var out []*client.EventIn
	for _, s := range w.plane.stated {
		if s.Name == name && (len(of) == 0 || slices.Contains(of, attr(s, "message"))) {
			out = append(out, s)
		}
	}
	return out
}

func attr(in *client.EventIn, name string) string {
	for _, a := range in.Attributes {
		if a.Name == name {
			return a.Value
		}
	}
	return ""
}

// records counts the events the ledger holds under filter.
func records(t *testing.T, collection, filter string) int {
	t.Helper()
	page, err := listFn(plane.For(context.Background(), "acme"), &client.BaseListIn{Collection: collection, Filter: filter, PerPage: 1})
	if err != nil {
		t.Fatal(err)
	}
	return page.TotalItems
}

func sesOpen(message string) string {
	return `{"eventType":"Open","mail":{"messageId":"ses-1","destination":["bounced@acme.io"],"tags":{"message":["` + message + `"]}},"open":{"timestamp":"2026-10-25T12:05:00.000Z"}}`
}

func TestReplay_OneProviderEventIsOneRecordAndOneCount(t *testing.T) {
	w := newWorld(t)
	for range 3 {
		if got := w.ses(t, "n-open", sesOpen(worldMsg)); got != http.StatusOK {
			t.Fatalf("answered %d", got)
		}
	}
	if n := records(t, events, `id = "`+rid("event", "ses:n-open")+`"`); n != 1 {
		t.Errorf("the open delivered three times is %d records", n)
	}
	if n := records(t, events, ``); n != 1 {
		t.Errorf("%d event records, want 1", n)
	}
	if got := w.status(t); got != StatusOpened {
		t.Errorf("status %q, want opened", got)
	}
	if s := w.tally(t); s.Totals.Opened != 1 || s.Totals.Sent != 1 {
		t.Errorf("counted %+v, want one sent and one opened", s.Totals)
	}
	opened := w.statements("email_opened")
	if len(opened) != 1 || opened[0].ID != rid("event", "ses:n-open") {
		t.Errorf("the stream was told %d opens", len(opened))
	}
	// A second open of the same message is its own event and counts no second open.
	if w.ses(t, "n-open-2", sesOpen(worldMsg)) != http.StatusOK || w.tally(t).Totals.Opened != 1 || len(w.statements("email_opened")) != 2 {
		t.Errorf("a second open: counted %+v, stated %d", w.tally(t).Totals, len(w.statements("email_opened")))
	}
}

func TestSendGrid_ABatchMovesTheMessageAndStatesEveryEvent(t *testing.T) {
	w := newWorld(t)
	tagged := `"email":"bounced@acme.io","sg_message_id":"sg-9.filter0001","message":"` + worldMsg + `","category":"fall-launch"`
	got := w.grid(t,
		fmt.Sprintf(`{"event":"delivered","sg_event_id":"e1","timestamp":%d,%s}`, at(1), tagged),
		fmt.Sprintf(`{"event":"open","sg_event_id":"e2","timestamp":%d,%s}`, at(2), tagged),
		fmt.Sprintf(`{"event":"open","sg_event_id":"e3","timestamp":%d,%s}`, at(3), tagged),
		fmt.Sprintf(`{"event":"click","sg_event_id":"e4","timestamp":%d,"url":"https://hanzo.ai/pricing?utm_source=email&from=%s&address=bounced@acme.io&token=x",%s}`, at(4), worldMsg, tagged),
		fmt.Sprintf(`{"event":"group_resubscribe","sg_event_id":"e5","timestamp":%d,%s}`, at(5), tagged),
	)
	if got != http.StatusOK {
		t.Fatalf("answered %d", got)
	}
	if st := w.status(t); st != StatusClicked {
		t.Errorf("status %q, want clicked", st)
	}
	m, _ := w.svc.GetMessage(context.Background(), "acme", worldMsg)
	if m.DeliveredAt == nil || m.OpenedAt == nil || m.ClickedAt == nil || !m.OpenedAt.Equal(time.Unix(at(2), 0)) {
		t.Errorf("the message's times: %+v", m)
	}
	if s := w.tally(t); s.Totals.Delivered != 1 || s.Totals.Opened != 1 || s.Totals.Clicked != 1 {
		t.Errorf("counted %+v: each is a count of messages", s.Totals)
	}
	if n := records(t, events, ``); n != 4 {
		t.Errorf("%d event records, want 4: a resubscribe is not kept", n)
	}
	click := w.statements("email_clicked")
	if len(click) != 1 || len(w.statements("email_opened")) != 2 || len(w.statements("email_delivered")) != 1 {
		t.Fatalf("stated %d clicks, %d opens, %d deliveries", len(click), len(w.statements("email_opened")), len(w.statements("email_delivered")))
	}
	c := click[0]
	if link := attr(c, "link"); link != "https://hanzo.ai/pricing?from="+worldMsg+"&utm_source=email" {
		t.Errorf("the clicked link %q: the original, without who clicked", link)
	}
	if attr(c, "provider") != "sendgrid" || attr(c, "message") != worldMsg || attr(c, "provider_message_id") != "sg-9" ||
		attr(c, "campaign") != "fall-launch" || attr(c, "category") != "fall-launch" || c.Product != "email" {
		t.Errorf("the click stated %+v", c.Attributes)
	}
	if c.Subject != crm.PersonID("bounced@acme.io") {
		t.Errorf("the subject %q, want the person the CRM names", c.Subject)
	}
	w.plane.mu.Lock()
	defer w.plane.mu.Unlock()
	for _, s := range w.plane.stated {
		for _, a := range append(s.Attributes, client.Signal{Name: "subject", Value: s.Subject}) {
			if strings.Contains(a.Value, "@") {
				t.Errorf("%s carries an address in %s: %q", s.Name, a.Name, a.Value)
			}
		}
	}
	if want := []string{"sendgrid:sg-9 delivered", "sendgrid:sg-9 opened", "sendgrid:sg-9 opened", "sendgrid:sg-9 clicked"}; !slices.Equal(w.plane.events, want) {
		t.Errorf("the CRM was told %v, want %v", w.plane.events, want)
	}
}

func TestOutside_MailSentAroundTheAPIIsFiledAndCounted(t *testing.T) {
	w := newWorld(t)
	about := `"email":"grace@else.io","sg_message_id":"oob-7.filter","singlesend_name":"Fall Launch","category":["promo","fall"]`
	if got := w.grid(t,
		fmt.Sprintf(`{"event":"processed","sg_event_id":"o1","timestamp":%d,%s}`, at(1), about),
		fmt.Sprintf(`{"event":"delivered","sg_event_id":"o2","timestamp":%d,%s}`, at(2), about),
		fmt.Sprintf(`{"event":"open","sg_event_id":"o3","timestamp":%d,%s}`, at(3), about),
	); got != http.StatusOK {
		t.Fatalf("answered %d", got)
	}
	id := outsideID("sendgrid:oob-7")
	m, err := w.svc.GetMessage(context.Background(), "acme", id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Origin != originProvider || m.Provider != "sendgrid" || m.ProviderID != "oob-7" || m.Campaign != "Fall Launch" ||
		m.Status != StatusOpened || m.To[0] != "grace@else.io" || !m.CreatedAt.Equal(time.Unix(at(1), 0)) {
		t.Errorf("the message filed %+v", m)
	}
	s := w.tally(t)
	if s.Totals.Sent != 2 || s.Totals.Delivered != 1 || s.Totals.Opened != 1 {
		t.Errorf("counted %+v: ours and theirs", s.Totals)
	}
	if len(s.Campaigns) != 1 || s.Campaigns[0].Name != "Fall Launch" || s.Campaigns[0].Tally.Sent != 1 || s.Campaigns[0].Tally.Opened != 1 {
		t.Errorf("by campaign %+v", s.Campaigns)
	}
	if sent := w.statements("email_sent", id); len(sent) != 1 {
		t.Errorf("the stream was told %d sends of theirs", len(sent))
	}
	list, err := w.svc.ListMessages(context.Background(), "acme", &ListMessagesInput{Provider: "sendgrid"})
	if err != nil || len(list.Messages) != 1 || list.Messages[0].ID != id {
		t.Errorf("listed by provider: %+v %v", list, err)
	}

	// SES: an event of a message no tag of ours names, its campaign a tag of the sender's.
	ev := `{"eventType":"Delivery","mail":{"messageId":"ses-oob","timestamp":"2026-10-25T11:00:00.000Z","source":"news@acme.io",` +
		`"destination":["lin@else.io"],"commonHeaders":{"subject":"Winter"},"tags":{"campaign":["winter"],"ses:configuration-set":["hanzo-crm-events"],"ses:caller-identity":["blaster"]}},` +
		`"delivery":{"timestamp":"2026-10-25T11:00:02.000Z","recipients":["lin@else.io"]}}`
	if got := w.ses(t, "n-oob", ev); got != http.StatusOK {
		t.Fatalf("answered %d", got)
	}
	m, err = w.svc.GetMessage(context.Background(), "acme", outsideID("ses:ses-oob"))
	if err != nil || m.Subject != "Winter" || m.Campaign != "winter" || m.From != "news@acme.io" || m.Status != StatusDelivered {
		t.Fatalf("the SES message filed %+v %v", m, err)
	}
	d := w.statements("email_delivered", outsideID("ses:ses-oob"))
	if len(d) != 1 || attr(d[0], "category") != "ses:configuration-set=hanzo-crm-events" || attr(d[0], "campaign") != "winter" {
		t.Errorf("the SES delivery stated %+v", d)
	}
}

func TestBounces_AHardOneSuppressesASoftOneDoesNot(t *testing.T) {
	w := newWorld(t)
	soft := `{"eventType":"Bounce","mail":{"messageId":"ses-1","destination":["bounced@acme.io"],"tags":{"message":["` + worldMsg + `"]}},` +
		`"bounce":{"bounceType":"Transient","bounceSubType":"MailboxFull","bouncedRecipients":[{"emailAddress":"bounced@acme.io"}]}}`
	if got := w.ses(t, "n-soft", soft); got != http.StatusOK {
		t.Fatalf("answered %d", got)
	}
	m, _ := w.svc.GetMessage(context.Background(), "acme", worldMsg)
	if m.Status != StatusBounced || m.Bounce != "soft" {
		t.Errorf("a soft bounce left %+v", m)
	}
	if b := w.statements("email_bounced"); len(b) != 1 || attr(b[0], "bounce") != "soft" || attr(b[0], "reason") != "Transient MailboxFull" {
		t.Errorf("stated %+v", b)
	}
	w.plane.mu.Lock()
	if len(w.plane.suppressed) != 0 {
		t.Errorf("a soft bounce suppressed %v", w.plane.suppressed)
	}
	w.plane.mu.Unlock()

	if got := w.grid(t,
		fmt.Sprintf(`{"event":"dropped","reason":"Bounced Address","sg_event_id":"d1","timestamp":%d,"email":"gone@acme.io","sg_message_id":"sg-d.x"}`, at(1)),
		fmt.Sprintf(`{"event":"unsubscribe","sg_event_id":"u1","timestamp":%d,"email":"bye@acme.io","sg_message_id":"sg-u.x"}`, at(2)),
	); got != http.StatusOK {
		t.Fatalf("answered %d", got)
	}
	w.plane.mu.Lock()
	defer w.plane.mu.Unlock()
	got := map[string]string{}
	for _, s := range w.plane.suppressed {
		got[s.Address] = s.Reason
	}
	if got["gone@acme.io"] != "bounce: dropped: Bounced Address" || got["bye@acme.io"] != "unsubscribed at sendgrid" || len(got) != 2 {
		t.Errorf("suppressed %v", got)
	}
	if s := w.tally(t); s.Totals.Unsubscribed != 1 || s.Totals.Bounced != 2 {
		t.Errorf("counted %+v", s.Totals)
	}
}

// A delivery that fails part way is answered 503, and the provider's retry takes it
// up where it stopped: every event once, every count once.
func TestAFailedDeliveryIsTakenUpWhereItStopped(t *testing.T) {
	w := newWorld(t)
	tagged := `"email":"bounced@acme.io","sg_message_id":"sg-9.f","message":"` + worldMsg + `"`
	batch := []string{
		fmt.Sprintf(`{"event":"delivered","sg_event_id":"r1","timestamp":%d,%s}`, at(1), tagged),
		fmt.Sprintf(`{"event":"open","sg_event_id":"r2","timestamp":%d,%s}`, at(2), tagged),
	}
	w.plane.mu.Lock()
	w.plane.streamOut = true
	w.plane.mu.Unlock()
	if got := w.grid(t, batch...); got != http.StatusServiceUnavailable {
		t.Fatalf("a delivery the stream refused answered %d, want 503", got)
	}
	w.plane.mu.Lock()
	w.plane.streamOut = false
	w.plane.mu.Unlock()
	for range 2 {
		if got := w.grid(t, batch...); got != http.StatusOK {
			t.Fatalf("the retry answered %d", got)
		}
	}
	if s := w.tally(t); s.Totals.Delivered != 1 || s.Totals.Opened != 1 {
		t.Errorf("counted %+v, want each once", s.Totals)
	}
	if len(w.statements("email_delivered")) != 1 || len(w.statements("email_opened")) != 1 {
		t.Errorf("stated %d deliveries and %d opens, want one each", len(w.statements("email_delivered")), len(w.statements("email_opened")))
	}
	if st := w.status(t); st != StatusOpened {
		t.Errorf("status %q", st)
	}
}

// A hard bounce the suppression list cannot take fails the delivery, so the
// provider sends it again and the address goes on the list then; it is counted once.
func TestABounceTheListCannotTakeIsAskedAgain(t *testing.T) {
	w := newWorld(t)
	w.plane.mu.Lock()
	w.plane.down = true
	w.plane.mu.Unlock()
	if got := w.post(t, "/v1/email/webhooks/ses?org=acme", js(w.notification("2", nil)), nil); got != http.StatusServiceUnavailable {
		t.Fatalf("a bounce the list refused answered %d, want 503", got)
	}
	w.plane.mu.Lock()
	w.plane.down = false
	w.plane.mu.Unlock()
	if got := w.post(t, "/v1/email/webhooks/ses?org=acme", js(w.notification("2", nil)), nil); got != http.StatusOK {
		t.Fatalf("the retry answered %d", got)
	}
	w.reached(t, "ses:ses-1 bounced", "bounced@acme.io", "bounce: Permanent General")
	if s := w.tally(t); s.Totals.Bounced != 1 {
		t.Errorf("counted %+v, want one bounce", s.Totals)
	}
}

// SendGrid writes one category as a string and several as an array; both read.
func TestSendGrid_ACategoryIsAStringOrAnArray(t *testing.T) {
	for raw, want := range map[string][]string{
		`{"event":"open","category":"news"}`:                         {"news"},
		`{"event":"open","category":["news","fall"]}`:                {"news", "fall"},
		`{"event":"open","category":["hanzo-crm-outreach","fall"]}`:  {"fall"},
		`{"event":"open","sg_message_id":"x.y","campaign":"spring"}`: nil,
	} {
		e, err := sendGridEvent([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if !slices.Equal(e.Tags, want) {
			t.Errorf("%s: tags %v, want %v", raw, e.Tags, want)
		}
	}
	e, _ := sendGridEvent([]byte(`{"event":"open","category":["promo","fall"],"campaign":"spring"}`))
	if e.Campaign != "spring" {
		t.Errorf("a custom arg campaign beats the categories: %q", e.Campaign)
	}
	e, _ = sendGridEvent([]byte(`{"event":"open","category":["promo","fall"]}`))
	if e.Campaign != "promo" {
		t.Errorf("with no campaign the first category is it: %q", e.Campaign)
	}
	if _, err := sendGridEvent([]byte(`{"event":"group_resubscribe"}`)); err != errIgnored {
		t.Errorf("a resubscribe: %v", err)
	}
}
