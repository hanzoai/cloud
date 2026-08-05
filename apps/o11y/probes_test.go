package o11y

import (
	"errors"

	"context"
	"github.com/luxfi/metric"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Every entry has to be a name plus an absolute http address. prober.New rejects
// an entry missing either and mountProbes treats that as non-fatal, so a
// malformed target would silently take the WHOLE fleet gauge away — and the
// public status document then answers 503 for a platform that is fine.
func TestFleetTargetsAreWellFormed(t *testing.T) {
	for _, tg := range fleetTargets {
		if strings.TrimSpace(tg.Name) == "" {
			t.Fatalf("a target has no name: %+v", tg)
		}
		u, err := url.Parse(tg.URL)
		if err != nil {
			t.Fatalf("%s: unparseable url %q: %v", tg.Name, tg.URL, err)
		}
		if u.Scheme != "http" || u.Host == "" || u.Path == "" {
			t.Fatalf("%s: url %q must be absolute http with a path", tg.Name, tg.URL)
		}
	}
}

// Names are the `service` label on hanzo_service_up and the key the status
// document builds an incident id from; addresses are how the reporter attributes
// a failure. A duplicate of either silently merges two services into one row.
func TestFleetTargetsAreUnique(t *testing.T) {
	names := map[string]bool{}
	urls := map[string]string{}
	for _, tg := range fleetTargets {
		if names[tg.Name] {
			t.Fatalf("duplicate target name %q", tg.Name)
		}
		names[tg.Name] = true
		if prev, dup := urls[tg.URL]; dup {
			t.Fatalf("targets %q and %q share the address %q", prev, tg.Name, tg.URL)
		}
		urls[tg.URL] = tg.Name
	}
}

// address is the only way to learn where a service answers, and a miss must be a
// miss. A name the fleet does not watch — hanzo-mpc, whose Service selects no pod
// — has no address, and the caller must probe nothing rather than fall back to a
// convention.
func TestAddressFindsWatchedAndMissesUnwatched(t *testing.T) {
	got, ok := address("iam")
	if !ok {
		t.Fatal("iam is watched by the fleet and must have an address")
	}
	if !strings.HasPrefix(got, "http://iam.hanzo.svc") {
		t.Fatalf("iam address = %q, want iam's own service", got)
	}

	if got, ok := address("hanzo-mpc"); ok {
		t.Fatalf("hanzo-mpc has no Deployment; it must not be watched, got %q", got)
	}
	if _, ok := address(""); ok {
		t.Fatal("the empty name must never resolve to an address")
	}
}

// kms is served by cloud, not by the retired kms Deployment. Pinning this is
// what stops the address drifting back to a Service with no endpoints, which is
// what published a permanent false outage.
func TestKMSAddressPointsAtTheBinaryThatServesIt(t *testing.T) {
	got, ok := address("kms")
	if !ok {
		t.Fatal("kms is customer-facing and must stay watched")
	}
	if strings.Contains(got, "kms.hanzo.svc") {
		t.Fatalf("kms address = %q, but that Deployment is scaled to zero", got)
	}
	if !strings.HasPrefix(got, "http://cloud.hanzo.svc:8000/v1/kms") {
		t.Fatalf("kms address = %q, want cloud's embedded KMS probe", got)
	}
}

// The verdict, stated once so both reads share it.
//
// A 3xx is up: a redirect comes from a process that is running and routing. A
// 404 is DOWN, and that is the honest answer even though a server replied —
// it means this address is not the contract we believe it is, so nothing here
// can confirm the service is serving. Scoring it up would have hidden exactly
// the defect that put three healthy services on the public status page as
// outages; it stays down, and the reporter names it so the address gets fixed.
func TestAnsweredVerdict(t *testing.T) {
	for _, code := range []int{200, 201, 204, 301, 302, 307, 399} {
		if !answered(code) {
			t.Errorf("answered(%d) = false, want true — a server replied", code)
		}
	}
	for _, code := range []int{400, 401, 403, 404, 500, 502, 503} {
		if answered(code) {
			t.Errorf("answered(%d) = true, want false — nothing confirms the contract", code)
		}
	}
}

// A service with no measured address is not dialled at all. Before, a hostname
// was synthesized and two timeouts were burned per request against names that do
// not serve port 80 — 4 seconds of latency to learn nothing.
func TestProbeHealthDoesNotDialAnUnwatchedService(t *testing.T) {
	up, latency := probeHealth(context.Background(), "")
	if up {
		t.Fatal("an unwatched service must never read up")
	}
	if latency != 0 {
		t.Fatalf("latency = %v, want 0 — nothing was dialled", latency)
	}
}

// A refused dial is down. This is what a crashed service looks like, and it is
// also what a stale address looks like, so it MUST stay down: downgrading it
// would make a real total outage invisible.
func TestProbeHealthScoresARefusedDialDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	if up, _ := probeHealth(context.Background(), addr+"/health"); up {
		t.Fatal("a refused dial must read down")
	}
}

// The scoped read and the fleet prober must agree on one address. The prober
// counts 3xx as up, so this one does too.
func TestProbeHealthAgreesWithTheProberOnRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	up, latency := probeHealth(context.Background(), srv.URL+"/health")
	if !up {
		t.Fatal("a 302 is a running server; the fleet prober scores it up and so must this")
	}
	if latency <= 0 {
		t.Fatal("a completed probe must carry its measured round trip")
	}
}

// A 404 is a reply, not a confirmation.
func TestProbeHealthScoresA404Down(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if up, _ := probeHealth(context.Background(), srv.URL+"/healthz"); up {
		t.Fatal("a 404 must read down — this address is not the service's contract")
	}
}

// ---- the derivation ----

// The scoped read takes its address from the fleet registry rather than building
// one. Two tables for one fact is how the second goes stale, which is what left
// nineteen of twenty-seven products dialling a name that serves nothing.
func TestResolveServiceTakesItsAddressFromTheRegistry(t *testing.T) {
	for _, name := range []string{"iam", "kms", "cloud", "datastore", "s3"} {
		want, ok := address(name)
		if !ok {
			t.Fatalf("%s is watched but has no address", name)
		}
		svc, resolved := resolveService(name)
		if !resolved {
			t.Fatalf("%s must resolve", name)
		}
		if svc.URL != want {
			t.Fatalf("%s: resolved url %q, want the registry's %q", name, svc.URL, want)
		}
	}
}

// An alias resolves to the workload's address, not to a name built from the slug
// the caller typed — cloud-api has no Service of its own.
func TestResolveServiceFollowsAliasesToTheRealAddress(t *testing.T) {
	want, _ := address("cloud")
	svc, ok := resolveService("cloud-api")
	if !ok {
		t.Fatal("cloud-api must resolve through the alias table")
	}
	if svc.URL != want {
		t.Fatalf("cloud-api url = %q, want cloud's %q", svc.URL, want)
	}
}

// A known workload the fleet does not watch resolves with no address, so it is
// queried for logs and metrics and probed for nothing. That is honest: we have
// measured nothing about whether it answers.
func TestResolveServiceLeavesUnwatchedWorkloadsWithoutAnAddress(t *testing.T) {
	svc, ok := resolveService("gateway")
	if !ok {
		t.Fatal("gateway is a known workload and must resolve")
	}
	if svc.URL != "" {
		t.Fatalf("gateway url = %q, want empty — the fleet does not watch it", svc.URL)
	}
	if svc.App != "gateway" || svc.PromService != "gateway" {
		t.Fatalf("gateway lost its telemetry identity: %+v", svc)
	}
}

// hanzo-mpc names no workload, so it is not a product either.
func TestRetiredWorkloadDoesNotResolve(t *testing.T) {
	if _, ok := resolveService("hanzo-mpc"); ok {
		t.Fatal("hanzo-mpc has no Deployment; resolving it invites a probe of a dead address")
	}
}

// ---- the reporter ----

type line struct {
	level string
	msg   string
	ctx   []interface{}
}

type recorder struct {
	mu    sync.Mutex
	lines []line
}

func (r *recorder) Info(msg string, ctx ...interface{}) { r.add("info", msg, ctx) }
func (r *recorder) Warn(msg string, ctx ...interface{}) { r.add("warn", msg, ctx) }

func (r *recorder) add(level, msg string, ctx []interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line{level: level, msg: msg, ctx: ctx})
}

func (r *recorder) get() []line {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]line(nil), r.lines...)
}

func field(l line, key string) (string, bool) {
	for i := 0; i+1 < len(l.ctx); i += 2 {
		if k, ok := l.ctx[i].(string); ok && k == key {
			v, _ := l.ctx[i+1].(string)
			return v, true
		}
	}
	return "", false
}

func probeOnce(t *testing.T, c *http.Client, u string) {
	t.Helper()
	resp, err := c.Get(u)
	if err == nil {
		resp.Body.Close()
	}
}

// A failure names its target, its address and its reason. Without those three a
// down gauge is a number, and telling "the Service has no endpoints" apart from
// "it answers 404 on a path it never served" costs a live investigation.
func TestReporterNamesTargetAddressAndReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	rec := &recorder{}
	c := &http.Client{Transport: &reporter{
		next: http.DefaultTransport,
		name: map[string]string{srv.URL + "/healthz": "iam"},
		log:  rec,
		last: map[string]string{},
	}}
	probeOnce(t, c, srv.URL+"/healthz")

	got := rec.get()
	if len(got) != 1 || got[0].level != "warn" {
		t.Fatalf("want exactly one warning, got %+v", got)
	}
	if v, _ := field(got[0], "target"); v != "iam" {
		t.Errorf("target = %q, want iam", v)
	}
	if v, _ := field(got[0], "url"); v != srv.URL+"/healthz" {
		t.Errorf("url = %q, want the exact address dialled", v)
	}
	if v, _ := field(got[0], "reason"); v != "HTTP 404" {
		t.Errorf("reason = %q, want the status that was returned", v)
	}
}

// A dial that never connects reports the transport's own words, which is where
// "connection refused" and "no such host" — the two that mean the address is
// wrong — become readable.
func TestReporterReportsADialFailure(t *testing.T) {
	rec := &recorder{}
	dead := "http://127.0.0.1:1/health"
	c := &http.Client{Transport: &reporter{
		next: http.DefaultTransport,
		name: map[string]string{dead: "kms"},
		log:  rec,
		last: map[string]string{},
	}}
	probeOnce(t, c, dead)

	got := rec.get()
	if len(got) != 1 {
		t.Fatalf("want one warning, got %+v", got)
	}
	reason, ok := field(got[0], "reason")
	if !ok || reason == "" {
		t.Fatalf("a dial failure must carry a reason, got %+v", got[0])
	}
	if !strings.Contains(reason, "connect") && !strings.Contains(reason, "refused") {
		t.Errorf("reason = %q, want the transport's own error", reason)
	}
}

// The same failure every 30 seconds would bury the one line that mattered, so a
// verdict is written only when it changes. A DIFFERENT reason is a different
// fact and is written again — a target that moved from refusing connections to
// answering 404 is a different chase.
func TestReporterWritesOnlyWhenTheVerdictChanges(t *testing.T) {
	code := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}))
	defer srv.Close()

	rec := &recorder{}
	c := &http.Client{Transport: &reporter{
		next: http.DefaultTransport,
		name: map[string]string{srv.URL + "/health": "chat"},
		log:  rec,
		last: map[string]string{},
	}}

	probeOnce(t, c, srv.URL+"/health")
	probeOnce(t, c, srv.URL+"/health")
	probeOnce(t, c, srv.URL+"/health")
	if n := len(rec.get()); n != 1 {
		t.Fatalf("three identical failures wrote %d lines, want 1", n)
	}

	code = http.StatusServiceUnavailable
	probeOnce(t, c, srv.URL+"/health")
	got := rec.get()
	if len(got) != 2 {
		t.Fatalf("a changed reason must be written; got %d lines", len(got))
	}
	if v, _ := field(got[1], "reason"); v != "HTTP 503" {
		t.Errorf("second reason = %q, want HTTP 503", v)
	}

	code = http.StatusOK
	probeOnce(t, c, srv.URL+"/health")
	got = rec.get()
	if len(got) != 3 || got[2].level != "info" {
		t.Fatalf("a recovery must be written as info; got %+v", got)
	}
}

// A target that was healthy from the first cycle is not news, so nothing is
// written until something breaks.
func TestReporterIsSilentWhileATargetIsHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := &recorder{}
	c := &http.Client{Transport: &reporter{
		next: http.DefaultTransport,
		name: map[string]string{srv.URL + "/health": "s3"},
		log:  rec,
		last: map[string]string{},
	}}
	probeOnce(t, c, srv.URL+"/health")
	probeOnce(t, c, srv.URL+"/health")

	if n := len(rec.get()); n != 0 {
		t.Fatalf("a healthy target wrote %d lines, want silence", n)
	}
}

// A redirect hop is not a verdict: the client follows it to an address that is
// not a target, and reporting that hop would attribute another host's answer to
// our service.
func TestReporterIgnoresAddressesItDoesNotWatch(t *testing.T) {
	rec := &recorder{}
	dead := "http://127.0.0.1:1/elsewhere"
	c := &http.Client{Transport: &reporter{
		next: http.DefaultTransport,
		name: map[string]string{"http://watched.example/health": "watched"},
		log:  rec,
		last: map[string]string{},
	}}
	probeOnce(t, c, dead)

	if n := len(rec.get()); n != 0 {
		t.Fatalf("an unwatched address wrote %d lines, want silence", n)
	}
}

// probeClient carries the reporter and keys it by every target's exact address,
// so a failure of any watched service is attributable without a second table.
func TestProbeClientWatchesEveryTarget(t *testing.T) {
	rec := &recorder{}
	c := probeClient(rec, nil)
	rt, ok := c.Transport.(*reporter)
	if !ok {
		t.Fatalf("probe client transport is %T, want the reporter", c.Transport)
	}
	if len(rt.name) != len(fleetTargets) {
		t.Fatalf("reporter watches %d addresses, want all %d targets", len(rt.name), len(fleetTargets))
	}
	for _, tg := range fleetTargets {
		if rt.name[tg.URL] != tg.Name {
			t.Errorf("address %q is attributed to %q, want %q", tg.URL, rt.name[tg.URL], tg.Name)
		}
	}
	// The prober bounds each probe with a context deadline; a second bound here
	// would be a second number to keep in step with the first.
	if c.Timeout != 0 {
		t.Errorf("probe client timeout = %v, want the prober's context deadline to be the one bound", c.Timeout)
	}
}

// The verdict the reporter logs is also the verdict it records: hanzo_service_up
// reads 1 for an answered probe and 0 for a refusal, under the exact series
// name and label the meter pipeline has always published — one uninterrupted
// series, whichever producer wrote the sample.
func TestReporterRecordsTheVerdictItReports(t *testing.T) {
	reg := metric.NewRegistry()
	up := upgauge(reg)
	r := &reporter{
		next: verdictTransport{},
		name: map[string]string{
			"http://up.hanzo.svc:80/healthz":   "up",
			"http://down.hanzo.svc:80/healthz": "down",
		},
		log:  &recorder{},
		up:   up,
		last: map[string]string{},
	}
	for _, u := range []string{"http://up.hanzo.svc:80/healthz", "http://down.hanzo.svc:80/healthz"} {
		req := httptest.NewRequest(http.MethodGet, u, nil)
		req.RequestURI = ""
		resp, _ := r.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
	}
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	got := map[string]float64{}
	for _, f := range fams {
		if f.Name != "hanzo_service_up" {
			continue
		}
		for _, m := range f.Metrics {
			for _, l := range m.Labels {
				if l.Name == "service" {
					got[l.Value] = m.Value.Value
				}
			}
		}
	}
	if got["up"] != 1 {
		t.Errorf("up = %v, want 1 — an answered probe must record 1", got["up"])
	}
	if v, ok := got["down"]; !ok || v != 0 {
		t.Errorf("down = %v (present=%v), want 0 — a refused dial must record 0, not be absent", v, ok)
	}
}

// verdictTransport answers 200 for the up host and refuses the down host, so a
// test drives both verdicts without a network.
type verdictTransport struct{}

func (verdictTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() == "down.hanzo.svc" {
		return nil, errors.New("connect: connection refused")
	}
	return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
}
