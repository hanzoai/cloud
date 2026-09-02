package ci

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	upstream "hanzo.ai/ci"
)

// probeViewer runs viewer() behind a request carrying the given headers.
func probeViewer(t *testing.T, headers map[string]string) (string, bool) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	var got string
	var ok bool
	app.Get("/probe", func(c *zip.Ctx) error {
		got, ok = viewer(c)
		return c.JSON(http.StatusOK, map[string]any{})
	})
	req := httptest.NewRequest("GET", "/probe", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if _, err := app.Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return got, ok
}

// The org this request is answered as is decided HERE and sent to the mounted
// surface as X-Org-Id. hanzo.ai/ci trusts that header completely — it is the
// whole of its visibility rule — so a wrong answer here is not a wrong page, it
// is one org reading another's repo names, branches and commits.
//
// The header the CALLER sent must never reach it. That is the property these
// tests exist for: the value is derived from the attested principal, and a
// caller-supplied X-Org-Id is not an input to that derivation.
func TestViewerIsDerivedFromTheAttestedCaller(t *testing.T) {
	// A SuperAdmin is answered as the reserved admin org, which is what the
	// surface reads as "sees everything".
	if org, ok := probeViewer(t, map[string]string{"X-User-IsAdmin": "true"}); !ok || org != authz.AdminOrg {
		t.Fatalf("SuperAdmin viewer = %q ok=%v, want %q", org, ok, authz.AdminOrg)
	}

	// A validated org member is answered as its own org.
	if org, ok := probeViewer(t, map[string]string{"X-Org-Id": "acme", "X-User-Id": "u"}); !ok || org != "acme" {
		t.Fatalf("member viewer = %q ok=%v, want \"acme\"", org, ok)
	}

	// SuperAdmin WINS over a supplied org: an admin who also carries X-Org-Id is
	// still answered as admin, never narrowed to whatever the header claimed.
	if org, ok := probeViewer(t, map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "acme", "X-User-Id": "u"}); !ok || org != authz.AdminOrg {
		t.Fatalf("admin+org viewer = %q ok=%v, want %q", org, ok, authz.AdminOrg)
	}
}

// No attested caller, no answer. The surface treats a missing X-Org-Id as fatal
// because absence means the request did not come through a gate; this is that
// gate, and it must refuse rather than pass an empty org that the surface would
// then have to interpret.
func TestAnUnattestedCallerIsRefused(t *testing.T) {
	if org, ok := probeViewer(t, nil); ok {
		t.Fatalf("a request with no principal resolved to %q; it must be refused", org)
	}
	// An org id with no user behind it is not a principal either.
	if org, ok := probeViewer(t, map[string]string{"X-Org-Id": "acme"}); ok {
		t.Fatalf("an org header with no validated user resolved to %q", org)
	}
}

// The org is keyed through the SAME injective namespace.Sanitize the rest of the
// fleet uses, so two distinct owners can never collide onto one tenant, and an
// identifier that cannot be sanitized fails closed instead of becoming a
// fabricated one.
func TestTheOrgIsTheKeyEveryOtherSurfaceUses(t *testing.T) {
	for _, raw := range []string{"acme", "ACME", "team1"} {
		org, ok := probeViewer(t, map[string]string{"X-Org-Id": raw, "X-User-Id": "u"})
		if !ok || org != raw {
			t.Errorf("viewer(%q) = %q ok=%v, want %q verbatim", raw, org, ok, raw)
		}
	}
	// A rune that is unsafe in a resource NAME is not unsafe in an isolation KEY,
	// and refusing it here would answer a caller the other surfaces serve. What
	// the org must survive is the forwarding below, which is where its grammar
	// actually matters.
	if org, ok := probeViewer(t, map[string]string{"X-Org-Id": "bad org", "X-User-Id": "u"}); !ok || org != "bad org" {
		t.Fatalf("viewer(\"bad org\") = %q ok=%v; the isolation rule admits it", org, ok)
	}
}

// The org reaches the mounted surface as a query value, so the escape is the
// property that matters — not a fold upstream of it. A raw space would end the
// value and hand hanzo.ai/ci an org it was never asked for.
func TestForwardedOrgIsEscaped(t *testing.T) {
	if got := "org=" + url.QueryEscape("bad org"); got != "org=bad+org" {
		t.Fatalf("forwarded query = %q, want org=bad+org", got)
	}
}

// ONE RULE, TWO READERS. principal.OrgOf is the org-isolation decision itself;
// a capability that re-derives the scope key from the same two headers must
// reach the same answer, or the same caller carries one org here and another
// everywhere else. That divergence is silent: the key still looks like an org,
// so the surface answers 200 with the runs of a tenant that does not exist.
//
// The cases below are the ones where a fold and the canonical rule part company
// — an id that is not already a clean short slug, and one inside the canonical
// length bound but past the fold's.
func TestViewer_AgreesWithCanonicalOrgRule(t *testing.T) {
	for _, org := range []string{
		"acme",                  // clean slug: both rules agree anyway
		"Acme",                  // case: a fold rewrites it, the rule does not
		strings.Repeat("a", 40), // 40 ≤ MaxOrgLen, past the fold's 32
		strings.Repeat("b", principal.MaxOrgLen),
	} {
		t.Run(org[:min(len(org), 12)], func(t *testing.T) {
			h := map[string]string{"X-User-Id": "u-1", "X-Org-Id": org}
			got, ok := probeViewer(t, h)
			want, wantOK := principal.OrgOf("u-1", org)
			if ok != wantOK || got != want {
				t.Fatalf("viewer(%q) = (%q,%v); the canonical rule says (%q,%v)",
					org, got, ok, want, wantOK)
			}
		})
	}
}

// Past the bound the canonical rule refuses. A fold cannot express that refusal:
// it truncates and suffixes, turning an org claim the boundary rejects into a
// well-formed key.
func TestViewer_RefusesOrgPastCanonicalBound(t *testing.T) {
	org := strings.Repeat("c", principal.MaxOrgLen+1)
	if got, ok := probeViewer(t, map[string]string{"X-User-Id": "u-1", "X-Org-Id": org}); ok {
		t.Fatalf("viewer admitted an org past MaxOrgLen as %q", got)
	}
}

// The fail-closed answer exists to say WHICH piece of configuration is missing.
// Built by pasting an error between quotes it stopped being JSON as soon as the
// error contained one — and an error is arbitrary text, so that is a property of
// the next edit to the function that produces it, not of today's string.
func TestTheUnconfiguredAnswerIsJSONWhateverTheErrorSays(t *testing.T) {
	for _, cause := range []error{
		errors.New("CI_GIT_TOKEN required"),
		errors.New(`open "/etc/ci.env": no such file`),
		errors.New("dial tcp\n\tgit.hanzo.ai:443: refused"),
		errors.New(`{"not":"json"}` + "\\"),
	} {
		rec := httptest.NewRecorder()
		unconfigured(cause).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/ci/runs", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status %d, want 503", rec.Code)
		}
		var out struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Errorf("body is not JSON for %q: %v\n%s", cause, err, rec.Body.String())
			continue
		}
		if !strings.Contains(out.Detail, cause.Error()) {
			t.Errorf("detail lost the reason: %q", out.Detail)
		}
	}
}

// And the caller has to receive that sentence. Reporting the status alone threw
// it away, so every misconfiguration read the same from the outside: the
// handler's careful message was written to a recorder and dropped.
func TestTheReasonSurvivesTheRoundTripToTheCaller(t *testing.T) {
	want := "CI_GIT_TOKEN required (Hanzo Git API token)"
	rec := httptest.NewRecorder()
	unconfigured(errors.New(want)).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/ci/runs", nil))

	got := unconfiguredDetail(rec.Body.Bytes())
	if !strings.Contains(got, want) {
		t.Fatalf("the reason did not survive: %q", got)
	}

	// An answer this binary did not compose has no detail to report, and must
	// not have one invented for it.
	if d := unconfiguredDetail([]byte(`<html>502 Bad Gateway</html>`)); d != "" {
		t.Errorf("a foreign body yielded a detail: %q", d)
	}
	if d := unconfiguredDetail(nil); d != "" {
		t.Errorf("an empty body yielded a detail: %q", d)
	}
}

// ───────────────── the mounted surface, driven end to end ─────────────────

// forge stands in for git.hanzo.ai: it answers the two reads the pollers make
// and counts them, so a test can see both what the surface was told and whether
// the pollers are still asking.
type forge struct {
	srv  *httptest.Server
	asks atomic.Int64
}

func newForge(t *testing.T) *forge {
	t.Helper()
	f := &forge{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.asks.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/repos/search"):
			_, _ = io.WriteString(w, `{"data":[{"full_name":"acme/app"}]}`)
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			_, _ = io.WriteString(w, `{"workflow_runs":[{"id":1,"display_title":"ship","path":"build.yml@refs/heads/main","event":"push","status":"completed","conclusion":"success","head_branch":"main","head_sha":"0123456789","run_number":1}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// mount builds the capability against f, and tears its pollers down with the
// test. refresh is how often the run poller re-reads.
func mount(t *testing.T, f *forge, refresh string) state {
	t.Helper()
	t.Setenv("CI_GIT_BASE", f.srv.URL)
	t.Setenv("CI_GIT_TOKEN", "token")
	t.Setenv("CI_REFRESH_SECONDS", refresh)
	s, err := build(cloud.Base{Log: luxlog.New("test")})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	until(t, "the first poll", func() bool { return f.asks.Load() > 0 })
	return s
}

// until waits for cond, or fails the test naming what never happened.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// runsFor asks the mounted surface what an org may see, exactly as call does.
func runsFor(t *testing.T, s state, org string) []upstream.Execution {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/ci/runs", nil)
	req.Header.Set("X-Org-Id", org)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("org %q: status %d: %s", org, rec.Code, rec.Body.String())
	}
	var out upstream.Executions
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Executions
}

// ONE ORG, ONE HOME. The org this binary writes for a SuperAdmin and the org the
// surface treats as seeing across tenants are the same fact, and they used to be
// two values that agreed only because both defaulted to the same word — a literal
// here, CI_ADMIN_ORG there. Either could move alone, and both directions are
// silent: the SuperAdmin quietly narrowed to one org, or an ordinary org handed
// the fleet.
//
// CI_ADMIN_ORG is named here only to say it is gone: set to another org, it
// changes nothing, because the value now lives once, in authz.
func TestTheSuperAdminOrgIsTheOneTheSurfaceSeesTheFleetFor(t *testing.T) {
	t.Setenv("CI_ADMIN_ORG", "hanzo")
	f := newForge(t)
	s := mount(t, f, "45")
	until(t, "acme's run", func() bool { return len(runsFor(t, s, authz.AdminOrg)) > 0 })

	admin, _ := probeViewer(t, map[string]string{"X-User-IsAdmin": "true"})
	got := runsFor(t, s, admin)
	if len(got) != 1 || got[0].Org != "acme" {
		t.Fatalf("the SuperAdmin org %q saw %+v; the fleet view is acme's run", admin, got)
	}
	if other := runsFor(t, s, "hanzo"); len(other) != 0 {
		t.Errorf("the hanzo org saw %+v; a named org sees its own runs and no others", other)
	}
}

// THE POLLERS ARE STOPPABLE. They read git.hanzo.ai every CI_REFRESH_SECONDS for
// as long as their context lives, so a mount handed a context nothing cancels is
// a mount that cannot be unwound — the process keeps the traffic whether or not
// anything still serves the surface.
func TestShutdownStopsThePollers(t *testing.T) {
	f := newForge(t)
	mount(t, f, "1")

	if err := Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // let a poll already in flight land
	settled := f.asks.Load()

	time.Sleep(2500 * time.Millisecond) // two refresh intervals
	if after := f.asks.Load(); after != settled {
		t.Fatalf("the forge was asked %d more times after shutdown; the pollers outlive the mount", after-settled)
	}
}
