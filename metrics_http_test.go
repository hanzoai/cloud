package cloud

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	dto "github.com/prometheus/client_model/go"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestMain installs this process's meter BEFORE any test runs, which is the only
// moment that works: the instruments resolve once, lazily, and a resolution that
// happens first binds them to the global no-op provider — every measurement
// afterwards discarded while the code looks perfectly instrumented. That is the
// production failure metrics_http.go's own note is about, and a suite that
// reproduces it measures nothing and says so cheerfully.
func TestMain(m *testing.M) {
	installMeter(luxlog.New("cloud").Output(io.Discard), resource.NewSchemaless())
	instruments()
	os.Exit(m.Run())
}

// gathered returns the series of one metric family from this process's registry,
// each as its label set flattened to "k=v,k=v" with the sample value.
func gathered(t *testing.T, name string) map[string]float64 {
	t.Helper()
	fams, err := MetricGatherer().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]float64{}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			parts := make([]string, 0, len(m.GetLabel()))
			for _, l := range m.GetLabel() {
				parts = append(parts, l.GetName()+"="+l.GetValue())
			}
			out[strings.Join(parts, ",")] = sample(m)
		}
	}
	return out
}

func sample(m *dto.Metric) float64 {
	switch {
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	case m.GetHistogram() != nil:
		return float64(m.GetHistogram().GetSampleCount())
	default:
		return 0
	}
}

// ZERO IS A MEASUREMENT. A mounted app that has served nothing must still have a
// series, because absent is what the store shows for an app that was never
// deployed, one that failed to mount, and a typo — and a rule written against an
// absence either never fires or fires everywhere.
//
// This is the whole claim of the seed: mounted ⇒ present, with no traffic at all.
func TestAMountedAppHasASeriesBeforeItsFirstRequest(t *testing.T) {
	seedApp("kv")

	got := gathered(t, "hanzo_http_requests_total")
	for _, want := range []string{
		"app=kv,org=-,product=kv,status=2xx",
		"app=kv,org=-,product=kv,status=5xx",
	} {
		if _, present := got[want]; !present {
			t.Errorf("no series %q — an app that served nothing is indistinguishable from one that never mounted.\ngot: %v", want, got)
		} else if got[want] != 0 {
			t.Errorf("series %q = %v, want it seeded at 0", want, got[want])
		}
	}
	// 3xx and 4xx are deliberately NOT seeded: appearing for the first time,
	// either is unambiguous, and a floor under them buys nothing.
	for series := range got {
		if strings.Contains(series, "app=kv") && (strings.Contains(series, "status=3xx") || strings.Contains(series, "status=4xx")) {
			t.Errorf("series %q was seeded; only the two classes a rule cannot read from an absence are", series)
		}
	}
}

// A request names the app that served it, and it names the SAME app the span
// does. Two answers to "who served this" is how a per-app dashboard and a
// per-app trace search disagree about the same minute.
func TestARequestNamesTheAppThatServedIt(t *testing.T) {
	observeRequest("o11y", "o11y", "acme", 503, 12*time.Millisecond)

	got := gathered(t, "hanzo_http_requests_total")
	const want = "app=o11y,org=acme,product=o11y,status=5xx"
	if got[want] != 1 {
		t.Errorf("series %q = %v, want 1\ngot: %v", want, got[want], got)
	}
	dur := gathered(t, "hanzo_http_request_duration_seconds")
	const wantDur = "app=o11y,org=acme,product=o11y"
	if dur[wantDur] != 1 {
		t.Errorf("duration series %q = %v, want one observation\ngot: %v", wantDur, dur[wantDur], dur)
	}
}

// An address nothing owns still records: the request happened. It folds to a
// single bucket rather than vanishing, the same way an unauthenticated caller
// folds to one org bucket.
func TestAnUnownedAddressStillRecords(t *testing.T) {
	observeRequest("", "", "", 200, time.Millisecond)

	got := gathered(t, "hanzo_http_requests_total")
	const want = "app=-,org=-,product=unknown,status=2xx"
	if got[want] != 1 {
		t.Errorf("series %q = %v, want 1 — a request nobody claims is still a request\ngot: %v", want, got[want], got)
	}
}

// THE COUNT IS ONE PER REQUEST. A second instrument under the same metric NAME
// would be summed with this one by every query written against it, so one
// request would read as two — which is why the app dimension was added to the
// series that already existed rather than beside it.
func TestOneRequestCountsOnce(t *testing.T) {
	observeRequest("mq", "mq", "solo", 200, time.Millisecond)

	total := 0.0
	for series, v := range gathered(t, "hanzo_http_requests_total") {
		if strings.Contains(series, "org=solo") {
			total += v
		}
	}
	if total != 1 {
		t.Errorf("one request measured %v times across hanzo_http_requests_total", total)
	}
}

// EVERY MOUNTED APP, FROM ONE PLACE. The seed sits in the mount loop, so the
// claim is not "144 apps were each remembered to call it" but "an app that
// composed has a series". This runs the real MountAll over specs that register
// nothing at all — no routes, no traffic, nothing to serve — and every one of
// them must still be in the registry when it returns.
func TestEveryMountedAppReachesTheRegistry(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("cloud").Output(io.Discard), DisableStartupMessage: true})
	names := []string{"seeded-a", "seeded-b", "seeded-c"}
	specs := make([]Plugin, 0, len(names))
	for _, n := range names {
		specs = append(specs, Plugin{Name: n, Mount: func(Router, Deps) error { return nil }})
	}
	if err := MountAll(app, specs, &Config{Enable: names}, Deps{}); err != nil {
		t.Fatalf("MountAll: %v", err)
	}

	got := gathered(t, "hanzo_http_requests_total")
	for _, n := range names {
		want := "app=" + n + ",org=-,product=" + n + ",status=2xx"
		if _, present := got[want]; !present {
			t.Errorf("%s mounted and has no series — nothing can report that it stopped, because it never started",
				n)
		}
	}
}

// A subsystem whose Mount FAILS must not be seeded: a series says the app is
// there to be measured, and one that refused to compose is not. This is why the
// seed is in the loop after the mount rather than beside the declaration.
func TestAnAppThatFailsToMountIsNotSeeded(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("cloud").Output(io.Discard), DisableStartupMessage: true})
	specs := []Plugin{{Name: "refuses", Mount: func(Router, Deps) error { return errRefused }}}
	if err := MountAll(app, specs, &Config{Enable: []string{"refuses"}}, Deps{}); err == nil {
		t.Fatal("MountAll accepted a subsystem that refused to compose")
	}
	for series := range gathered(t, "hanzo_http_requests_total") {
		if strings.Contains(series, "app=refuses") {
			t.Errorf("series %q exists for an app that never composed", series)
		}
	}
}

var errRefused = errors.New("this subsystem declines to compose")

// THE REAL MIDDLEWARE, not the function under it. observeRequest can be correct
// while nothing hands it the app — a per-app dashboard that is empty for every
// app looks the same as a fleet that serves nothing, and both halves pass their
// own tests. So this drives a request through the chain all 144 apps inherit and
// reads the series it produced.
//
// The span and the metric are asserted TOGETHER, because the point is that they
// name the same subsystem: two answers to "who served this" is how a per-app
// board and a per-app trace search disagree about the same minute.
func TestTheRequestChainNamesTheAppOnBothSignals(t *testing.T) {
	sr := newRecordingTracer(t)
	Declare([]Plugin{{Name: "kafka", Prefixes: []string{"/v1/kafka"}}}, &Config{Enable: []string{"kafka"}})

	app := zip.New(zip.Config{Logger: luxlog.New("cloud").Output(io.Discard)})
	app.Use(TracingMiddleware())
	app.Get("/v1/kafka/topics", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "yes"}) })

	if _, err := app.Test(httptest.NewRequest("GET", "/v1/kafka/topics", nil)); err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	sub, stamped := attrOf(spans[0], "hanzo.subsystem")
	if !stamped || sub.AsString() != "kafka" {
		t.Fatalf("span subsystem = %v (stamped=%v), want kafka", sub, stamped)
	}

	got := gathered(t, "hanzo_http_requests_total")
	const want = "app=kafka,org=-,product=kafka,status=2xx"
	if got[want] != 1 {
		t.Errorf("series %q = %v, want 1 — the metric must name the subsystem the span names\ngot: %v",
			want, got[want], got)
	}
}

// THE ORG ON BOTH SIGNALS IS THE ONE THE IDENTITY WAS VALIDATED FOR.
//
// A metric label and a span attribute are two different costs and this one value
// pays both: the label is a standing time series, and the attribute IS the tenant
// column apps/o11y files the row under (planeOrg). So the value has to come from
// the identity boundary's own attestation and from nothing a caller can send —
// an org taken off the wire would let any reachable caller open a series per
// string it types AND file its request under a tenant that is not its own.
//
// This drives the REAL chain — the tracing middleware in front of the real
// SanitizeIdentity — twice: once with no credential and a chosen X-Org-Id, once
// with a signed token for a real org.
func TestTelemetryNamesTheOrgTheIdentityWasValidatedFor(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	Declare([]Plugin{{Name: "ledger", Prefixes: []string{"/v1/ledger"}}}, &Config{Enable: []string{"ledger"}})

	newApp := func() *zip.App {
		app := zip.New(zip.Config{Logger: luxlog.New("cloud").Output(io.Discard)})
		app.Use(TracingMiddleware())
		app.Use(SanitizeIdentity(v))
		app.Get("/v1/ledger/entries", func(c *zip.Ctx) error {
			return c.JSON(200, map[string]string{"ok": "yes"})
		})
		return app
	}
	call := func(t *testing.T, mutate func(*http.Request)) sdktrace.ReadOnlySpan {
		t.Helper()
		sr := newRecordingTracer(t)
		req := httptest.NewRequest("GET", "/v1/ledger/entries", nil)
		if mutate != nil {
			mutate(req)
		}
		if _, err := newApp().Test(req); err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		spans := sr.Ended()
		if len(spans) != 1 {
			t.Fatalf("recorded %d spans, want 1", len(spans))
		}
		return spans[0]
	}

	t.Run("no credential names no org", func(t *testing.T) {
		span := call(t, setHdr(map[string]string{"X-Org-Id": "chosen-corp"}))

		if org, stamped := attrOf(span, "hanzo.org"); stamped {
			t.Errorf("span carries hanzo.org=%v — the row would be filed under a tenant nobody proved", org)
		}
		got := gathered(t, "hanzo_http_requests_total")
		if got["app=ledger,org=-,product=ledger,status=2xx"] != 1 {
			t.Errorf("want one request in the org=- bucket, got: %v", got)
		}
		for series := range got {
			if strings.Contains(series, "org=chosen-corp") {
				t.Errorf("series %q exists — a caller opened a time series by sending a header", series)
			}
		}
	})

	t.Run("a validated token names its own org", func(t *testing.T) {
		tok := signWith(t, key, tokenClaims("hanzo-console", "ledger-co", "joe@ledger.co", false, time.Now().Add(time.Hour)))
		span := call(t, both(bearer(tok), setHdr(map[string]string{"X-Org-Id": "chosen-corp"})))

		org, stamped := attrOf(span, "hanzo.org")
		if !stamped || org.AsString() != "ledger-co" {
			t.Errorf("span org = %v (stamped=%v), want the org the token was signed for", org, stamped)
		}
		got := gathered(t, "hanzo_http_requests_total")
		if got["app=ledger,org=ledger-co,product=ledger,status=2xx"] != 1 {
			t.Errorf("want one request under the validated org, got: %v", got)
		}
	})
}

// A PROGRAM BUILT THE ONE WAY NAMES ITS APP, WITH NOTHING TOLD TWICE.
//
// The sibling above drives the middleware after an explicit Declare, which is
// how the label resolved in tests and only in tests: production reached Declare
// through MountAll, and the o11y binary composed its subsystem by hand and never
// reached it. Every series it wrote carried app="-" while its own test suite
// stayed green.
//
// So this builds the app through the ONE constructor, mounts through the ONE
// mount, and calls Declare nowhere. The series has to name the app anyway.
func TestAProgramBuiltTheOneWayNamesItsApp(t *testing.T) {
	prev := subsystems.Load()
	t.Cleanup(func() { subsystems.Store(prev) })
	subsystems.Store(nil)

	cfg := &Config{Brand: "hanzo", Enable: []string{"kite"}}
	specs := []Plugin{{
		Name:     "kite",
		Prefixes: []string{"/v1/kite"},
		Price:    Free,
		Mount: func(r Router, _ Deps) error {
			r.Get("/v1/kite/fly", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "yes"}) })
			return nil
		},
	}}

	app := App(cfg, Deps{}, specs, nil)
	if err := MountAll(app, specs, cfg, Deps{}); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	if _, err := app.Test(httptest.NewRequest("GET", "/v1/kite/fly", nil)); err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	got := gathered(t, "hanzo_http_requests_total")
	const want = "app=kite,org=-,product=kite,status=2xx"
	if got[want] < 1 {
		t.Errorf("series %q = %v, want at least 1 — a request served by a mounted app must name it\ngot: %v",
			want, got[want], got)
	}
	for series := range got {
		if strings.HasPrefix(series, "app=-,") && strings.Contains(series, "product=kite") {
			t.Errorf("series %q — the app that served /v1/kite is nameless, which is the empty index", series)
		}
	}
}
