package patrol

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// notify_test.go reads what patrol SENDS. Mail is best effort in production —
// an activation on record and an incident open are the facts the operation runs
// on — so what is measured here is who gets told, off which document, and which
// addresses are refused before anything is sent at all.

// posted is what a notify peer was handed.
type posted struct {
	mu   sync.Mutex
	sent []client.Send
}

func (p *posted) all() []client.Send {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]client.Send(nil), p.sent...)
}

// to reports whether a message went to one address.
func (p *posted) to(addr string) bool {
	for _, s := range p.all() {
		if s.To == addr {
			return true
		}
	}
	return false
}

// mailbox serves the notify peer on the plane this test already runs, so what a
// route sent and to whom is readable. Call it after mount, which is what claims
// the runtime directory the peers answer in.
func mailbox(t *testing.T) *posted {
	t.Helper()
	box := &posted{}
	app := zip.New(zip.Config{AppName: "notify"})
	app.Post("/notify/send",
		func(_ context.Context, in *client.Send) (*client.Sent, error) {
			box.mu.Lock()
			defer box.mu.Unlock()
			box.sent = append(box.sent, *in)
			return &client.Sent{Provider: "test"}, nil
		}, zip.WithOperationID(client.NotifySend))

	path := zip.SocketPath("notify")
	go func() { _ = app.Listen(path) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	for range 400 {
		if c, err := net.Dial("unix", path); err == nil {
			_ = c.Close()
			return box
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the notify peer never began listening on %s", path)
	return nil
}

// contacted writes the tenant's controller list.
func contacted(t *testing.T, app *zip.App, lists map[string]any) {
	t.Helper()
	if code, raw := call(t, app, http.MethodPut, "/v1/framework/"+DTTenant.String()+"/"+DTTenant.Name, org, boss,
		map[string]any{"contacts": lists}); code != http.StatusOK {
		t.Fatalf("write the tenant contacts: %d (%s)", code, raw)
	}
}

// TestAnActivationTellsTheControllers proves the list an activation reaches: the
// controllers named on the tenant document, with the incident and the site in
// the message, so the control room learns of it wherever they are.
func TestAnActivationTellsTheControllers(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	contacted(t, app, map[string]any{"controllers": []string{controller, supervisor}})
	sent := mailbox(t)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller, map[string]any{
		"ref": "ARC-1", "account": "1001", "zone": "Z3", "trigger": "Intruder — rear door",
	}); code != http.StatusCreated {
		t.Fatalf("a matched activation want 201, got %d (%s)", code, raw)
	}
	if !sent.to(controller) || !sent.to(supervisor) {
		t.Fatalf("both controllers must be told: %+v", sent.all())
	}
	var body string
	for _, m := range sent.all() {
		if m.To == controller {
			body = m.Body
			if m.Channel != "email" || m.Org != org || !strings.Contains(m.Subject, "depot") {
				t.Errorf("the message is addressed wrong: %+v", m)
			}
		}
	}
	for _, want := range []string{"depot", "Z3", "Intruder — rear door"} {
		if !strings.Contains(body, want) {
			t.Errorf("the message must carry %q: %q", want, body)
		}
	}
}

// TestAContactListThatIsNotOneReachesNobody proves the tenant's contacts are
// read as the named lists they are: a value shaped otherwise, and a list nobody
// is on, both send nothing rather than failing the activation.
func TestAContactListThatIsNotOneReachesNobody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lists any
	}{
		{"a contacts field that is not a set of lists", []string{controller}},
		{"a list nobody is named on", map[string]any{"controllers": []string{}}},
		{"some other list", map[string]any{"keyholders": []string{controller}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := mount(t)
			install(t, app, org)
			seedSite(t, app, org, "depot", "1001", "")
			if code, raw := call(t, app, http.MethodPut, "/v1/framework/"+DTTenant.String()+"/"+DTTenant.Name,
				org, boss, map[string]any{"contacts": tc.lists}); code != http.StatusOK {
				t.Fatalf("write the tenant contacts: %d (%s)", code, raw)
			}
			sent := mailbox(t)
			if code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller,
				map[string]any{"ref": "ARC-1", "account": "1001", "trigger": "Intruder"}); code != http.StatusCreated {
				t.Fatalf("a matched activation want 201, got %d (%s)", code, raw)
			}
			if got := sent.all(); len(got) != 0 {
				t.Fatalf("%d messages went out: %+v", len(got), got)
			}
		})
	}
}

// TestAnAddressThatIsNotOneIsNeverSentTo proves the addresses are parsed rather
// than trusted. They are free text off a register row, and a carriage return
// inside one is how a recipient list becomes a header list — a second Bcc, a
// second Subject — so anything that is not an address is refused before the
// message is handed to the plane.
func TestAnAddressThatIsNotOneIsNeverSentTo(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	contacted(t, app, map[string]any{"controllers": []string{
		"not an address",
		"ok@acme.test\nBcc: everyone@rival.test",
		"ok@acme.test\r\nSubject: Cleared",
		"",
		"Fine Person <fine@acme.test>",
	}})
	sent := mailbox(t)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller,
		map[string]any{"ref": "ARC-1", "account": "1001", "trigger": "Intruder"}); code != http.StatusCreated {
		t.Fatalf("a matched activation want 201, got %d (%s)", code, raw)
	}
	got := sent.all()
	if len(got) != 1 || got[0].To != "fine@acme.test" {
		t.Fatalf("only the one real address may be sent to, got %+v", got)
	}
	for _, m := range got {
		if strings.ContainsAny(m.To, "\r\n") {
			t.Errorf("an address carrying a line break was handed to the plane: %q", m.To)
		}
	}
}

// TestADispatchTellsTheOfficerAndTheKeyholder proves who a dispatch reaches: the
// officer crewing the unit sent, and the site's keyholder contact when the
// operation holds keys for it — because whoever attends may need them.
func TestADispatchTellsTheOfficerAndTheKeyholder(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTSite, map[string]any{
		"name": "depot", "account": "1001", "tier": "gold", "sla": 20,
		"lat": 51.90, "lon": -8.47, "status": SiteOK,
		"keys": true, "contact": "keyholder@acme.test",
	})
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
		map[string]any{"site": "depot", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("open an incident: %d (%s)", code, raw)
	}
	var inc patrolIncident
	data(t, raw, &inc)
	sent := mailbox(t)

	if code, _ := stepped(t, app, inc.Name, controller, map[string]any{"act": Verify}); code != http.StatusOK {
		t.Fatalf("verify want 200, got %d", code)
	}
	if code, _ := stepped(t, app, inc.Name, controller,
		map[string]any{"act": Dispatch, "unit": "gf-01"}); code != http.StatusOK {
		t.Fatalf("dispatch want 200, got %d", code)
	}
	if !sent.to(officer) {
		t.Errorf("the officer crewing the unit was not told: %+v", sent.all())
	}
	if !sent.to("keyholder@acme.test") {
		t.Errorf("a site whose keys the operation holds must reach its keyholder: %+v", sent.all())
	}
	for _, m := range sent.all() {
		if !strings.Contains(m.Subject, inc.Name) || !strings.Contains(m.Body, "depot") {
			t.Errorf("a dispatch names the incident and the site: %+v", m)
		}
	}
}

// TestAnEscalationTellsTheControllers proves the act that exists to reach
// somebody does: an escalation is a call for help, and it goes to the list.
func TestAnEscalationTellsTheControllers(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	contacted(t, app, map[string]any{"controllers": []string{controller}})
	sent := mailbox(t)
	for _, act := range []map[string]any{
		{"act": Verify}, {"act": Dispatch, "unit": "gf-01"}, {"act": Escalate, "detail": "no answer"},
	} {
		if code, _ := stepped(t, app, name, controller, act); code != http.StatusOK {
			t.Fatalf("%v want 200, got %d", act, code)
		}
	}
	told := false
	for _, m := range sent.all() {
		if m.To == controller && strings.Contains(m.Subject, "escalated") {
			told = true
		}
	}
	if !told {
		t.Fatalf("an escalation must reach the controllers: %+v", sent.all())
	}
}

// TestAnEventThatCannotBeWrittenDoesNotUnwindTheAct proves the feed row is best
// effort: raising one against a site the register does not hold writes nothing
// and returns, because the act that asked for it has already happened.
func TestAnEventThatCannotBeWrittenDoesNotUnwindTheAct(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	o := ops{s: &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}}
	o.raise(context.Background(), org, LevelRed, "Intruder", "no-such-site", "", "")

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/event", org, controller, nil)
	var rows []patrolEvent
	data(t, raw, &rows)
	if code != http.StatusOK || len(rows) != 0 {
		t.Fatalf("the feed carries %d rows, want none: %+v", len(rows), rows)
	}
}

// TestACallerThatStopsIsForgotten proves the allowance map does not grow by one
// row per caller forever: a bucket that has refilled to the brim says nothing
// that a fresh key would not, so it is dropped.
func TestACallerThatStopsIsForgotten(t *testing.T) {
	r := &rate{per: time.Nanosecond, burst: 1}
	if !r.take("a-caller") {
		t.Fatal("the first draw on a fresh key was refused")
	}
	time.Sleep(time.Millisecond)
	if !r.take("another-caller") {
		t.Fatal("a second key was refused its own allowance")
	}
	r.mu.Lock()
	_, remembered := r.seen["a-caller"]
	r.mu.Unlock()
	if remembered {
		t.Error("a caller whose allowance had refilled is still a row in the map")
	}
}
