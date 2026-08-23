package dns

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── a second plane, added the way a real one would be ────────────────────────────
//
// Everything below the divider is what a Cloudflare or Route 53 adapter is: a type,
// its four methods, and a register() in an init(). It touches provider.go not at all,
// dns.go not at all, and the route table not at all — which is the property the tests
// after it assert. It does NOT implement Relay, so it also pins what a plane that
// speaks its own wire shape answers to an address it does not have.

type recorder struct{ seen []Call }

func (r *recorder) ID() string { return "recorder" }
func (r *recorder) op(c Call) (Answer, error) {
	r.seen = append(r.seen, c)
	return Answer{Status: http.StatusOK, ContentType: "application/json",
		Body: []byte(`{"op":"` + string(c.Op) + `","zone":"` + c.Zone + `","record":"` + c.Record + `"}`)}, nil
}
func (r *recorder) ListZones(_ context.Context, c Call) (Answer, error)    { return r.op(c) }
func (r *recorder) ListRecords(_ context.Context, c Call) (Answer, error)  { return r.op(c) }
func (r *recorder) UpsertRecord(_ context.Context, c Call) (Answer, error) { return r.op(c) }
func (r *recorder) DeleteRecord(_ context.Context, c Call) (Answer, error) { return r.op(c) }

// last is the instance the most recent Mount built, so a test can read what the head
// handed it. The constructor is called once per mount, exactly as in production.
var last *recorder

func init() {
	register("recorder", func() Provider {
		last = &recorder{}
		return last
	})
}

// ── the client ─────────────────────────────────────────────────────────────────────

// The address is read once, into the operation it names plus the zone and record it
// names it on. This table IS the contract between the head and every adapter.
func TestAddressNamesTheOperation(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		op           Op
		zone, record string
	}{
		{http.MethodGet, "/v1/dns/zones", OpListZones, "", ""},
		{http.MethodGet, "/v1/dns/zones/example.com/records", OpListRecords, "example.com", ""},
		{http.MethodPost, "/v1/dns/zones/example.com/records", OpUpsertRecord, "example.com", ""},
		{http.MethodPut, "/v1/dns/zones/example.com/records/r1", OpUpsertRecord, "example.com", "r1"},
		{http.MethodPatch, "/v1/dns/zones/example.com/records/r1", OpUpsertRecord, "example.com", "r1"},
		{http.MethodDelete, "/v1/dns/zones/example.com/records/r1", OpDeleteRecord, "example.com", "r1"},
		// Addresses the four operations do not name: a zone create, a zone read, a
		// zone delete, the plane's own sync and health.
		{http.MethodPost, "/v1/dns/zones", OpPlane, "", ""},
		{http.MethodGet, "/v1/dns/zones/example.com", OpPlane, "", ""},
		{http.MethodDelete, "/v1/dns/zones/example.com", OpPlane, "", ""},
		{http.MethodPost, "/v1/dns/sync", OpPlane, "", ""},
		{http.MethodGet, "/v1/dns/health", OpPlane, "", ""},
		{http.MethodGet, "/v1/dns", OpPlane, "", ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			got := classify(tc.method, tc.path)
			if got.Op != tc.op || got.Zone != tc.zone || got.Record != tc.record {
				t.Fatalf("classify = %v/%q/%q, want %v/%q/%q", got.Op, got.Zone, got.Record, tc.op, tc.zone, tc.record)
			}
		})
	}
}

// A deployment that names no provider gets Hanzo's own plane — the behaviour every
// existing deployment already has, unchanged.
func TestHanzoIsTheDefaultPlane(t *testing.T) {
	p, err := selected()
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "hanzo" {
		t.Fatalf("default provider = %q, want hanzo", p.ID())
	}
	if _, ok := p.(Relay); !ok {
		t.Fatal("the hanzo plane must declare Relay: its API IS this surface's contract")
	}
}

// A second plane is selected by name, receives the calls, and required no edit to
// the head, the registry or the route to get them.
func TestASecondPlaneIsOneFile(t *testing.T) {
	t.Setenv("HANZO_DNS_PROVIDER", "recorder")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	res, body := do(t, app, as(httptest.NewRequest(http.MethodDelete, "/v1/dns/zones/example.com/records/r1", nil), "orgA", "orgA/dave", "tokenA"))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the selected plane", res.StatusCode)
	}
	if !strings.Contains(body, `"op":"delete_record"`) || !strings.Contains(body, `"record":"r1"`) {
		t.Fatalf("body = %q, want the second plane's own answer", body)
	}
	if len(last.seen) != 1 {
		t.Fatalf("plane saw %d calls, want 1", len(last.seen))
	}
	if got := last.seen[0]; got.Org != "orgA" || got.Bearer != "tokenA" || got.Zone != "example.com" {
		t.Fatalf("plane saw org=%q bearer=%q zone=%q, want the validated tenant and the caller's own bearer",
			got.Org, got.Bearer, got.Zone)
	}
}

// A plane that speaks its own wire shape has no address for a call the four
// operations do not name, and 404 says exactly that rather than inventing one.
func TestAPlaneWithoutRelayHasNoOtherAddress(t *testing.T) {
	t.Setenv("HANZO_DNS_PROVIDER", "recorder")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	res, _ := do(t, app, as(httptest.NewRequest(http.MethodPost, "/v1/dns/sync", strings.NewReader(`{}`)), "orgA", "orgA/dave", "tokenA"))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an address this plane does not have", res.StatusCode)
	}
	if len(last.seen) != 0 {
		t.Fatalf("plane saw %d calls, want 0", len(last.seen))
	}
}

// A name no adapter registered is refused at MOUNT, not at the first request: a
// deployment configured for a plane that does not exist fails to start rather than
// serving 502s.
func TestAnUnknownPlaneIsRefusedAtMount(t *testing.T) {
	t.Setenv("HANZO_DNS_PROVIDER", "carrier-pigeon")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	err := Mount(app, cloud.Deps{})
	if err == nil {
		t.Fatal("Mount accepted an unregistered provider name")
	}
	if !strings.Contains(err.Error(), "carrier-pigeon") || !strings.Contains(err.Error(), "hanzo") {
		t.Fatalf("error = %q, want it to name the miss and what IS registered", err)
	}
}
