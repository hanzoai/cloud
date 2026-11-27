package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/zap-proto/zip"
)

// day is the first instant of the ledger tests' calendar.
var day = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

// sendAt sends one message as though at t.
func (l *ledger) sendAt(t *testing.T, at time.Time, to, provider, campaign string) *MessageRecord {
	t.Helper()
	l.svc.now = func() time.Time { return at }
	return l.send(t, MessageID("acme", ""), to, provider, campaign, "")
}

func TestList_NarrowsAndPagesTheLedger(t *testing.T) {
	l := newLedger(t)
	var sent []*MessageRecord
	for i := range 7 {
		provider := []string{"ses", "sendgrid"}[i%2]
		sent = append(sent, l.sendAt(t, day.Add(time.Duration(i)*time.Hour), fmt.Sprintf("p%d@acme.io", i%3), provider, ""))
	}
	ctx := context.Background()
	list := func(in *ListMessagesInput) []string {
		t.Helper()
		out, err := l.svc.ListMessages(ctx, "acme", in)
		if err != nil {
			t.Fatalf("%+v: %v", in, err)
		}
		ids := []string{}
		for _, m := range out.Messages {
			ids = append(ids, m.ID)
		}
		return ids
	}
	ids := func(is ...int) []string {
		out := []string{}
		for _, i := range is {
			out = append(out, sent[i].ID)
		}
		return out
	}
	eq := func(name string, got, want []string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	eq("newest first", list(&ListMessagesInput{}), ids(6, 5, 4, 3, 2, 1, 0))
	eq("by provider", list(&ListMessagesInput{Provider: "sendgrid"}), ids(5, 3, 1))
	eq("by recipient", list(&ListMessagesInput{Recipient: " P1@acme.io "}), ids(4, 1))
	eq("by status", list(&ListMessagesInput{Status: "sent"}), ids(6, 5, 4, 3, 2, 1, 0))
	eq("a range", list(&ListMessagesInput{Since: day.Add(2 * time.Hour).Format(time.RFC3339), Until: day.Add(5 * time.Hour).Format(time.RFC3339)}), ids(4, 3, 2))

	// Pages of three, each from where the last stopped, cover the ledger once.
	var all []string
	cursor := ""
	for range 4 {
		out, err := l.svc.ListMessages(ctx, "acme", &ListMessagesInput{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Messages {
			all = append(all, m.ID)
		}
		if cursor = out.Next; cursor == "" {
			break
		}
	}
	eq("paged", all, ids(6, 5, 4, 3, 2, 1, 0))

	for _, bad := range []*ListMessagesInput{
		{Status: "nope"}, {Provider: "twilio_email"}, {Since: "yesterday"}, {Cursor: "zz.abc"},
		{Cursor: "61.abcdefabcdefabc"}, {Cursor: `x" || id != "`},
	} {
		var he *zip.HTTPError
		if _, err := l.svc.ListMessages(ctx, "acme", bad); !errors.As(err, &he) || he.Status != http.StatusBadRequest {
			t.Errorf("%+v: %v, want 400", bad, err)
		}
	}
}

func TestStats_CountsByProviderCampaignAndDay(t *testing.T) {
	l := newLedger(t)
	l.sendAt(t, day.Add(1*time.Hour), "a@acme.io", "ses", "fall")
	l.sendAt(t, day.Add(2*time.Hour), "b@acme.io", "ses", "fall")
	l.sendAt(t, day.Add(26*time.Hour), "c@acme.io", "sendgrid", "winter")
	l.sendAt(t, day.Add(27*time.Hour), "d@acme.io", "sendgrid", "")
	ctx := context.Background()

	s, err := l.svc.Stats(ctx, "acme", &StatsInput{Since: day.Format(time.RFC3339), Until: day.Add(72 * time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if s.Interval != "day" || !s.Since.Equal(day) || !s.Until.Equal(day.Add(72*time.Hour)) || len(s.Series) != 3 {
		t.Fatalf("range %v..%v by %s, %d points", s.Since, s.Until, s.Interval, len(s.Series))
	}
	if s.Totals.Sent != 4 || s.Series[0].Tally.Sent != 2 || s.Series[1].Tally.Sent != 2 || s.Series[2].Tally.Sent != 0 || !s.Series[2].At.Equal(day.Add(48*time.Hour)) {
		t.Errorf("totals %+v series %+v", s.Totals, s.Series)
	}
	if len(s.Providers) != 2 || s.Providers[0].Name != "sendgrid" || s.Providers[0].Tally.Sent != 2 || s.Providers[1].Tally.Sent != 2 {
		t.Errorf("by provider %+v", s.Providers)
	}
	if len(s.Campaigns) != 2 || s.Campaigns[0].Name != "fall" || s.Campaigns[0].Tally.Sent != 2 || s.Campaigns[1].Name != "winter" {
		t.Errorf("by campaign %+v", s.Campaigns)
	}

	s, err = l.svc.Stats(ctx, "acme", &StatsInput{Interval: "hour", Campaign: "fall", Since: day.Format(time.RFC3339), Until: day.Add(3 * time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Series) != 3 || s.Series[1].Tally.Sent != 1 || s.Series[2].Tally.Sent != 1 || s.Totals.Sent != 2 || len(s.Campaigns) != 0 {
		t.Errorf("one campaign by hour: %+v %+v", s.Totals, s.Series)
	}
	s, _ = l.svc.Stats(ctx, "acme", &StatsInput{Provider: "ses", Since: day.Format(time.RFC3339), Until: day.Add(72 * time.Hour).Format(time.RFC3339)})
	if s.Totals.Sent != 2 || len(s.Providers) != 1 {
		t.Errorf("one provider: %+v", s)
	}

	for _, bad := range []*StatsInput{
		{Interval: "week"}, {Provider: "mail"}, {Since: "now"}, {Campaign: `x" || campaign != "`},
		{Interval: "hour", Since: day.Format(time.RFC3339), Until: day.Add(40 * 24 * time.Hour).Format(time.RFC3339)},
		{Since: day.Format(time.RFC3339), Until: day.Add(-time.Hour).Format(time.RFC3339)},
	} {
		var he *zip.HTTPError
		if _, err := l.svc.Stats(ctx, "acme", bad); !errors.As(err, &he) || he.Status != http.StatusBadRequest {
			t.Errorf("%+v: %v, want 400", bad, err)
		}
	}
}

// The status answers say whether the org holds credentials, what the providers
// say of the accounts, and what the ledger counted in the last day.
func TestStatus_IsTheCredentialsTheAccountAndTheLedger(t *testing.T) {
	l := newLedger(t)
	aws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/email/account" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIA/") {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"SendQuota":{"Max24HourSend":1000000,"MaxSendRate":200,"SentLast24Hours":812345},"ProductionAccessEnabled":true,"SendingEnabled":true}`))
	}))
	defer aws.Close()
	sg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/user/credits" || r.Header.Get("Authorization") != "Bearer SG.k" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"remain":150000,"total":2500000,"overage":0,"used":2350000}`))
	}))
	defer sg.Close()
	l.creds.Set("acme", "ses", "endpoint", aws.URL)
	l.creds.Set("acme", "sendgrid", "endpoint", sg.URL+"/v3/mail/send")

	now := time.Now().UTC()
	l.sendAt(t, now.Add(-2*time.Hour), "a@acme.io", "ses", "")
	l.sendAt(t, now.Add(-30*time.Minute), "b@acme.io", "ses", "")
	l.sendAt(t, now.Add(-30*time.Hour), "c@acme.io", "ses", "")
	l.sendAt(t, now.Add(-time.Hour), "d@acme.io", "sendgrid", "")
	l.svc.now = func() time.Time { return now }
	ctx := context.Background()

	ses, err := l.svc.GetSESStatus(ctx, "acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ses.Configured || ses.QuotaError != "" || ses.Sandbox || !ses.SendingEnabled || ses.Max24HourSend != 1000000 || ses.MaxSendRate != 200 ||
		ses.ProviderSentLast24Hours != 812345 || ses.SentLast24Hours != 2 || ses.ActiveProvider != "ses" || ses.ConfigurationSet != "" || ses.Domain != "acme.io" {
		t.Errorf("SES status %+v", ses)
	}
	grid, err := l.svc.GetSendGridStatus(ctx, "acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if !grid.Configured || grid.QuotaError != "" || grid.CreditsTotal != 2500000 || grid.CreditsRemaining != 150000 || grid.CreditsUsed != 2350000 || grid.SentLast24Hours != 1 {
		t.Errorf("SendGrid status %+v", grid)
	}

	// A provider that refuses the read is said so, with the limits at 0.
	l.creds.Set("acme", "ses", "access-key-id", "WRONG")
	if ses, _ = l.svc.GetSESStatus(ctx, "acme", ""); ses.QuotaError == "" || ses.Max24HourSend != 0 || !ses.Configured {
		t.Errorf("a refused read: %+v", ses)
	}
	// An org that holds nothing is not configured, asks no provider, and counts nothing.
	none, err := l.svc.GetSESStatus(ctx, "nobody", "")
	if err != nil || none.Configured || none.SentLast24Hours != 0 || none.QuotaError != "" || none.Max24HourSend != 0 || none.ActiveProvider != "" {
		t.Errorf("an org with nothing: %+v %v", none, err)
	}
}

// ---- routing, over HTTP ----

func TestRoute_EmailSendsOnlyBySESOrSendGrid(t *testing.T) {
	r := routed(t)
	body := func(provider string) string {
		return `{"to":["ada@acme.io"],"subject":"Fall","text":"Hello","provider":"` + provider + `"}`
	}
	for _, rail := range []string{"twilio_email", "mail", "twilio"} {
		if code, raw := r.do(t, http.MethodPost, "/v1/email", "acme", "u_1", false, body(rail)); code != http.StatusBadRequest {
			t.Errorf("pinned %s: %d %s, want 400", rail, code, raw)
		}
	}
	// An org whose only email credential is the transactional rail has none here.
	if code, raw := r.do(t, http.MethodPost, "/v1/email", "tw", "u_1", false, body("")); code != http.StatusBadRequest {
		t.Errorf("an org with Twilio Email only: %d %s, want 400", code, raw)
	}
	if len(*r.got) != 0 {
		t.Fatalf("%d messages left by a transactional rail", len(*r.got))
	}
	for _, rail := range []string{"ses", "sendgrid"} {
		if code, raw := r.do(t, http.MethodPost, "/v1/email", "acme", "u_1", false, body(rail)); code != http.StatusOK || (*r.got)[len(*r.got)-1].provider != rail {
			t.Errorf("pinned %s: %d %s", rail, code, raw)
		}
	}
}

func TestRoute_AnOrgAdminSetsTheDefaultProvider(t *testing.T) {
	r := routed(t)
	send := func() string {
		t.Helper()
		if code, raw := r.do(t, http.MethodPost, "/v1/email", "acme", "u_1", false, `{"to":["ada@acme.io"],"text":"Hi"}`); code != http.StatusOK {
			t.Fatalf("send: %d %s", code, raw)
		}
		return (*r.got)[len(*r.got)-1].provider
	}
	if got := send(); got != "ses" {
		t.Errorf("no default: sent by %s, want ses", got)
	}
	if code, _ := r.do(t, http.MethodPut, "/v1/email/provider", "acme", "u_1", false, `{"provider":"sendgrid"}`); code != http.StatusForbidden {
		t.Errorf("a member set the default: %d, want 403", code)
	}
	if code, _ := r.do(t, http.MethodPut, "/v1/email/provider", "acme", "u_admin", true, `{"provider":"twilio_email"}`); code != http.StatusBadRequest {
		t.Errorf("a transactional default: %d, want 400", code)
	}
	if code, raw := r.do(t, http.MethodPut, "/v1/email/provider", "acme", "u_admin", true, `{"provider":"sendgrid"}`); code != http.StatusOK || !strings.Contains(string(raw), `"sendgrid"`) {
		t.Fatalf("an admin set the default: %d %s", code, raw)
	}
	if got := send(); got != "sendgrid" {
		t.Errorf("default sendgrid: sent by %s", got)
	}
	var st SESStatusResult
	if code, raw := r.do(t, http.MethodGet, "/v1/email/ses/status", "acme", "u_1", false, ""); code != http.StatusOK || json.Unmarshal(raw, &st) != nil || st.ActiveProvider != "sendgrid" {
		t.Errorf("the status shows the default: %d %s", code, raw)
	}
}

// A message's links say where the visit came from, the provider is handed our id
// and the campaign to carry back, the ledger keeps it, and the stream is told.
func TestRoute_ASendIsTaggedFiledAndStated(t *testing.T) {
	r := routed(t)
	code, raw := r.do(t, http.MethodPost, "/v1/email", "acme", "u_1", false,
		`{"to":["Ada@Acme.io"],"subject":"Fall","campaign":"fall-2026","text":"Read https://hanzo.ai/fall.","html":"<p><a href=\"https://hanzo.ai/fall?utm_source=newsletter\">Read</a></p>"}`)
	if code != http.StatusOK {
		t.Fatalf("send: %d %s", code, raw)
	}
	var d Dispatch
	if err := json.Unmarshal(raw, &d); err != nil || len(d.Messages) != 1 {
		t.Fatalf("%s %v", raw, err)
	}
	m := d.Messages[0]
	got := (*r.got)[0]
	if !strings.Contains(got.text, "https://hanzo.ai/fall?utm_source=email&utm_medium=ses&utm_campaign=fall-2026&utm_content=link1&from="+m.ID+".") {
		t.Errorf("the text's link: %s", got.text)
	}
	if !strings.Contains(got.html, `href="https://hanzo.ai/fall?utm_source=newsletter&amp;utm_medium=ses&amp;utm_campaign=fall-2026&amp;utm_content=link1&amp;from=`+m.ID+`"`) {
		t.Errorf("the html's link, the sender's utm_source kept: %s", got.html)
	}
	if strings.Contains(got.text, "unsubscribe?utm") || !strings.Contains(got.text, "Unsubscribe: https://api.hanzo.ai/v1/marketing/unsubscribe?address=ada@acme.io&token=t") {
		t.Errorf("the opt-out link is the footer's, untouched: %s", got.text)
	}
	if got.tags["message"] != m.ID || got.tags["campaign"] != "fall-2026" {
		t.Errorf("tags %v", got.tags)
	}
	if code, raw := r.do(t, http.MethodPost, "/v1/email", "acme", "u_1", false, `{"to":["ada@acme.io"],"text":"x","campaign":"Fall Launch"}`); code != http.StatusBadRequest {
		t.Errorf("a campaign an SES tag cannot hold: %d %s", code, raw)
	}

	var held MessageRecord
	if code, raw := r.do(t, http.MethodGet, "/v1/email/"+m.ID, "acme", "u_1", false, ""); code != http.StatusOK || json.Unmarshal(raw, &held) != nil {
		t.Fatalf("GET: %d %s", code, raw)
	}
	if held.Campaign != "fall-2026" || held.Origin != originAPI || held.ProviderID != "ses-ada@acme.io" || held.Status != StatusSent {
		t.Errorf("held %+v", held)
	}
	r.plane.mu.Lock()
	defer r.plane.mu.Unlock()
	if len(r.plane.stated) != 1 {
		t.Fatalf("stated %d", len(r.plane.stated))
	}
	s := r.plane.stated[0]
	if s.Name != "email_sent" || s.ID != rid("event", "sent:"+m.ID) || s.Subject != crm.PersonID("ada@acme.io") ||
		attr(s, "provider") != "ses" || attr(s, "campaign") != "fall-2026" || attr(s, "message") != m.ID || attr(s, "provider_message_id") != "<address>" {
		t.Errorf("stated %+v %+v", s, s.Attributes)
	}
}
