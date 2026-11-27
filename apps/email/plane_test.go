package email

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/basetest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakePlane stands in for marketing's suppression list and the CRM, and records
// what reached them.
type fakePlane struct {
	mu sync.Mutex
	// refuse is what the list says of an address, by address.
	refuse map[string]string
	// down makes the list unreachable.
	down       bool
	filed      []crm.Mail
	events     []string // "<id> <status>"
	suppressed []client.Addressee
	// stated is every statement the event stream was given, and down makes it
	// refuse them.
	stated    []*client.EventIn
	streamOut bool
}

// stubPlane stands a real Base up for the ledger (basetest) and stand-ins for the
// suppression list, the CRM and the event stream.
func stubPlane(t *testing.T) *fakePlane {
	t.Helper()
	basetest.Serve(t)
	grown.Clear()
	p := &fakePlane{refuse: map[string]string{}}
	was := [5]any{clearFn, suppressFn, fileFn, eventFn, captureFn}
	captureFn = func(_ context.Context, in *client.EventIn) (*client.EventCaptured, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.streamOut {
			return nil, errors.New("the event stream is down")
		}
		p.stated = append(p.stated, in)
		return &client.EventCaptured{Accepted: 1}, nil
	}
	clearFn = func(_ context.Context, in *client.Addressee) (*client.Clearance, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.down {
			return nil, errors.New("marketing is down")
		}
		if why := p.refuse[in.Address]; why != "" {
			return &client.Clearance{Refused: why}, nil
		}
		return &client.Clearance{Unsubscribe: "https://api.hanzo.ai/v1/marketing/unsubscribe?address=" + in.Address + "&token=t"}, nil
	}
	suppressFn = func(_ context.Context, in *client.Addressee) (*struct{}, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.down {
			return nil, errors.New("marketing is down")
		}
		p.suppressed = append(p.suppressed, *in)
		return &struct{}{}, nil
	}
	fileFn = func(_ context.Context, _ string, m crm.Mail) (bool, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.filed = append(p.filed, m)
		return true, nil
	}
	eventFn = func(_ context.Context, _, id, _, status string, _ time.Time) (bool, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.events = append(p.events, id+" "+status)
		return true, nil
	}
	t.Cleanup(func() {
		clearFn = was[0].(func(context.Context, *client.Addressee) (*client.Clearance, error))
		suppressFn = was[1].(func(context.Context, *client.Addressee) (*struct{}, error))
		fileFn = was[2].(func(context.Context, string, crm.Mail) (bool, error))
		eventFn = was[3].(func(context.Context, string, string, string, string, time.Time) (bool, error))
		captureFn = was[4].(func(context.Context, *client.EventIn) (*client.EventCaptured, error))
	})
	return p
}

// sendApp is POST /v1/email over a delivery that records what each message was
// handed, and the plane stand-in.
type sent struct {
	provider   string
	to         []string
	text, html string
	headers    map[string]string
	tags       map[string]string
}

// route is the email surface over a delivery that records what each message was
// handed: acme holds SES and SendGrid, tw only the transactional Twilio Email.
type route struct {
	plane *fakePlane
	got   *[]sent
	app   *zip.App
	creds *StaticCredentialResolver
	mu    *sync.Mutex
}

func routed(t *testing.T) *route {
	t.Helper()
	r := &route{plane: stubPlane(t), got: &[]sent{}, creds: NewStaticCredentialResolver(), mu: &sync.Mutex{}}
	for k, v := range map[string]string{"access-key-id": "AKIA", "secret-access-key": "s", "from-email": "team@acme.io"} {
		r.creds.Set("acme", "ses", k, v)
	}
	r.creds.Set("acme", "sendgrid", "api-key", "SG.k")
	r.creds.Set("acme", "sendgrid", "from-email", "team@acme.io")
	nowhere(r.creds, "acme")
	for k, v := range map[string]string{"account-sid": "AC1", "auth-token": "t", "from-email": "team@tw.io"} {
		r.creds.Set("tw", "twilio_email", k, v)
	}
	svc := NewDeliveryService(r.creds, func(_ context.Context, provider string, _ map[string]string, _ string, to []string, _, html, text string, headers, tags map[string]string) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		*r.got = append(*r.got, sent{provider: provider, to: to, text: text, html: html, headers: headers, tags: tags})
		return provider + "-" + to[0], nil
	}, luxlog.New("test"))
	h := NewHandlers(NewVerifier(nil, time.Second), svc, luxlog.New("test"))
	tr, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	trail = tr
	t.Cleanup(func() { trail = nil })
	r.app = zip.New(zip.Config{Logger: luxlog.New("test")})
	r.app.Use(cloud.Bridge())
	RegisterRoutes(r.app, h)
	return r
}

// nowhere points org's SES and SendGrid at an address that answers nothing, so a
// status read in a test never reaches a provider.
func nowhere(creds *StaticCredentialResolver, org string) {
	creds.Set(org, "ses", "endpoint", "http://127.0.0.1:1")
	creds.Set(org, "sendgrid", "endpoint", "http://127.0.0.1:1/v3/mail/send")
}

// do is one request as user of org, an org admin when admin is set.
func (r *route) do(t *testing.T, method, path, org, user string, admin bool, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	resp, err := r.app.Test(req, deadline)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// deadline bounds every request a test in this package sends through app.Test. Bare,
// Test gives the handler one second and fails the test past it, and on a CI runner
// testing 32 packages at once a send here took two (runs 17680 and 17683). It is the
// bound the rest of this repository passes.
var deadline = zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}

// sendApp is POST /v1/email as a member of acme.
func sendApp(t *testing.T) (*fakePlane, *[]sent, func(body string) (int, []byte)) {
	t.Helper()
	r := routed(t)
	return r.plane, r.got, func(body string) (int, []byte) {
		return r.do(t, http.MethodPost, "/v1/email", "acme", "u_1", false, body)
	}
}

func TestSend_OneMessagePerRecipientWithItsOwnOptOut(t *testing.T) {
	_, got, post := sendApp(t)
	code, body := post(`{"to":["ada@analytical.example","grace@gmail.com"],"subject":"News","text":"Hello."}`)
	if code != http.StatusOK {
		t.Fatalf("answered %d: %s", code, body)
	}
	if len(*got) != 2 {
		t.Fatalf("%d messages left, want one per recipient", len(*got))
	}
	for _, m := range *got {
		link := "https://api.hanzo.ai/v1/marketing/unsubscribe?address=" + m.to[0] + "&token=t"
		if m.headers["List-Unsubscribe"] != "<"+link+">" || m.headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
			t.Errorf("%s: headers %v, want its own List-Unsubscribe and the one-click Post", m.to[0], m.headers)
		}
		if !strings.Contains(m.text, "Unsubscribe: "+link) {
			t.Errorf("%s: no footer link in %q", m.to[0], m.text)
		}
		company := strings.Contains(m.text, "Unsubscribe everyone at ")
		if m.to[0] == "ada@analytical.example" && !strings.Contains(m.text, "Unsubscribe everyone at analytical.example: "+link+"&company=true") {
			t.Errorf("a company address is not offered its company's opt-out: %q", m.text)
		}
		if m.to[0] == "grace@gmail.com" && company {
			t.Errorf("a free mail address was offered a company opt-out: %q", m.text)
		}
	}
}

func TestSend_RefusedRecipientsAre422AndFiledSuppressed(t *testing.T) {
	p, got, post := sendApp(t)
	p.refuse["out@acme.example"] = "unsubscribed"
	p.refuse["all@corp.example"] = "company unsubscribed"
	p.refuse["spam@block.example"] = "blocklisted"
	code, body := post(`{"to":["in@acme.example","out@acme.example","all@corp.example","spam@block.example"],"subject":"News","text":"Hello."}`)
	if code != http.StatusOK {
		t.Fatalf("answered %d: %s", code, body)
	}
	var d Dispatch
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0].to[0] != "in@acme.example" || len(d.Messages) != 1 {
		t.Fatalf("sent %v, want only in@acme.example", *got)
	}
	want := map[string]string{"out@acme.example": "unsubscribed", "all@corp.example": "company unsubscribed", "spam@block.example": "blocklisted"}
	if len(d.Refused) != 3 {
		t.Fatalf("refused %+v", d.Refused)
	}
	for _, r := range d.Refused {
		if r.Status != http.StatusUnprocessableEntity || r.Reason != want[r.Address] {
			t.Errorf("refused %+v, want 422 %q", r, want[r.Address])
		}
	}
	statuses := map[string]string{}
	for _, m := range p.filed {
		statuses[m.To] = m.Status + "|" + m.Reason
	}
	if statuses["in@acme.example"] != "sent|" {
		t.Errorf("the sent message was filed %q", statuses["in@acme.example"])
	}
	for addr, why := range want {
		if statuses[addr] != "suppressed|"+why {
			t.Errorf("%s was filed %q, want suppressed|%s", addr, statuses[addr], why)
		}
	}
	for _, m := range p.filed {
		if m.To == "in@acme.example" && (m.ID != "ses:ses-in@acme.example" || m.Rail != "ses") {
			t.Errorf("the sent message is filed under %q by %q, want the provider's id", m.ID, m.Rail)
		}
	}
}

func TestSend_EveryRecipientRefusedSendsNothing(t *testing.T) {
	p, got, post := sendApp(t)
	p.refuse["out@acme.example"] = "unsubscribed"
	if code, body := post(`{"to":["out@acme.example"],"subject":"News","text":"Hello."}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("answered %d: %s, want 422", code, body)
	}
	if len(*got) != 0 {
		t.Fatalf("%d messages left to a refused recipient", len(*got))
	}
}

func TestSend_AListThatCannotAnswerSendsNothing(t *testing.T) {
	p, got, post := sendApp(t)
	p.down = true
	if code, body := post(`{"to":["in@acme.example"],"subject":"News","text":"Hello."}`); code != http.StatusServiceUnavailable {
		t.Fatalf("answered %d: %s, want 503", code, body)
	}
	if len(*got) != 0 {
		t.Fatalf("%d messages left uncleared", len(*got))
	}
}
