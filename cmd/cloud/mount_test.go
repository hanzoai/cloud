package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/credz/launch"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/webui"
	"github.com/zap-proto/zip"
)

// This file is about ONE property: a subsystem that cannot start must not take
// the others down with it.
//
// It is written against the exact failure that took api.hanzo.ai and
// cloud.hanzo.ai down for 25 minutes on 2026-07-29:
//
//	cloud: zip: Add service 0: zip: Load(pubsub): exited before listening: exit status 1
//
// /bin/false reproduces it precisely — a child that execs and exits 1 before it
// binds its socket — so these tests exercise zip's real start path, real child
// processes and the real error string, not a stub of them.

// falseBin is a binary guaranteed to exit non-zero without listening. Coreutils
// ships it at either path depending on the distro's /usr merge.
func falseBin(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/bin/false", "/usr/bin/false"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no false(1) on this host; the test needs a binary that exits non-zero")
	return ""
}

// good is an app mounted at a real HTTP address — a stand-in for every OTHER
// subsystem in the process. Mounted through the same manifest.App → zip.Plugin
// path the fleet uses (CLOUD_<NAME>_ADDR), so the request really is proxied.
func good(t *testing.T, name, prefix, body string) manifest.App {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_"+strings.ToUpper(strings.NewReplacer("-", "_").Replace(name))+"_ADDR", "http://"+strings.TrimPrefix(srv.URL, "http://"))
	return manifest.App{Name: name, Prefixes: []string{prefix}}
}

// dead is an app whose child exits 1 before listening — the production failure.
func dead(t *testing.T, name, prefix string) manifest.App {
	t.Helper()
	t.Setenv("CLOUD_"+strings.ToUpper(strings.NewReplacer("-", "_").Replace(name))+"_BIN", falseBin(t))
	return manifest.App{Name: name, Prefixes: []string{prefix}, Eager: true}
}

func do(t *testing.T, app *zip.App, path string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest("GET", "http://cloud"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

// TestADeadSubsystemDoesNotTakeTheHostDown is the outage, as a test.
//
// pubsub is Apps[0] and Eager, so it is the FIRST thing the host mounts and the
// first thing that can fail. Before the fix its failure returned from run() and
// the process exited 1 — every other subsystem in the binary died with it,
// including the API, IAM validation, billing and the team backend. Nothing about
// pubsub's inability to open a SQLite file is a reason for /v1/flags to stop
// answering.
func TestADeadSubsystemDoesNotTakeTheHostDown(t *testing.T) {
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	absent := map[string]string{}

	// The dead one goes FIRST, exactly as it does in manifest order.
	if err := mount(app, dead(t, "pubsub", "/v1/pubsub"), true, "s", "", absent); err != nil {
		t.Fatalf("a dead OPTIONAL subsystem aborted the host: %v\n"+
			"this is the 25-minute outage: one child that cannot boot must degrade to absent", err)
	}
	if err := mount(app, good(t, "flags", "/v1/flags", `{"flag":"on"}`), false, "s", "", absent); err != nil {
		t.Fatalf("mounting a healthy subsystem after a dead one failed: %v", err)
	}
	health(app, absent)

	// 1. EVERY OTHER SUBSYSTEM SERVES. This is the whole point.
	code, ctype, body := do(t, app, "/v1/flags")
	if code != 200 {
		t.Errorf("GET /v1/flags = %d, want 200 — a healthy subsystem stopped serving because a different one died", code)
	}
	// A 200 proves nothing on its own here: the console catch-all answers unrouted
	// /v1/* with the SPA shell, so assert what came back.
	if !strings.Contains(ctype, "application/json") {
		t.Errorf("GET /v1/flags Content-Type = %q, want JSON — this is the SPA shell, not the subsystem", ctype)
	}
	if !strings.Contains(body, `"flag":"on"`) {
		t.Errorf("GET /v1/flags body = %q, want the subsystem's own JSON", body)
	}

	// 2. THE DEAD ONE IS HONEST: 503 "deployed but down", not a 404 a client is
	// entitled to cache, and above all not a 200 of HTML.
	code, ctype, body = do(t, app, "/v1/pubsub")
	if code != 503 {
		t.Errorf("GET /v1/pubsub = %d, want 503 — an absent subsystem must say so", code)
	}
	if code == 200 || strings.Contains(ctype, "text/html") {
		t.Errorf("GET /v1/pubsub = %d %s %q — an absent subsystem answered as if it were fine", code, ctype, body)
	}
}

// TestAnAbsentPrefixBeatsTheConsoleCatchAll is why "mount it with no process" is
// not optional — the alternative is not a 404, it is a 200 of HTML.
//
// The host owns "/" (the white-labelled console) and registers it LAST, so a
// prefix that was never registered reaches the SPA. webui only refuses the
// namespaces in its apiPrefixes list — /v1/, /api/, /zap, /healthz, /readyz —
// which is the fix that stopped /v1/meet/health answering 200 text/html. SEVEN
// prefixes across FIVE apps escape that list and are still answered by the shell:
//
//	iam          /login/oauth      ← an OAuth client gets HTML, not a redirect
//	agentskills  /.well-known/agent-skills/{index.json,:skill/SKILL.md}
//	commerce     /_/commerce
//	git          /git, /explore
//	tasks        /tasks
//
// So this asserts BOTH: the /v1 prefix that webui would 404 (honest but
// indistinguishable from "this deployment does not run that subsystem"), and the
// /login/oauth prefix it would answer 200 text/html for. Measured, not assumed —
// an unregistered /login/oauth/authorize returns 3576 bytes of console shell.
// Registering the mount is what makes both of them 503 instead.
func TestAnAbsentPrefixBeatsTheConsoleCatchAll(t *testing.T) {
	// iam's real prefix, because that is where the lie is still live.
	for _, tc := range []struct{ app, prefix, probe string }{
		{"iam", "/login/oauth", "/login/oauth/authorize"},
		{"pubsub", "/v1/pubsub", "/v1/pubsub/health"},
	} {
		t.Run(tc.app, func(t *testing.T) {
			app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
			absent := map[string]string{}
			if err := mount(app, dead(t, tc.app, tc.prefix), true, "s", "", absent); err != nil {
				t.Fatal(err)
			}
			health(app, absent)
			if err := webui.Mount(app); err != nil {
				t.Skipf("console embed unavailable in this build: %v", err)
			}

			code, ctype, body := do(t, app, tc.probe)
			if code == 200 {
				t.Fatalf("GET %s = 200 %s (%d bytes) — the console catch-all answered for an absent subsystem",
					tc.probe, ctype, len(body))
			}
			if strings.Contains(ctype, "text/html") {
				t.Fatalf("GET %s Content-Type = %q — the caller got the SPA shell", tc.probe, ctype)
			}
			if code != 503 {
				t.Errorf("GET %s = %d, want 503 (deployed but down) rather than the console's answer", tc.probe, code)
			}
		})
	}
}

// TestAbsenceIsObservable is the second half of the fix and the one this fleet
// keeps paying for: a subsystem that is silently absent is worse than one that is
// loudly broken. Earlier the same day a fail-soft durable-queue downgrade left
// N-1 plugins with no queue and nothing to alert on, and a campaign resolved its
// audience and then mailed nobody.
//
// So a probe must be able to READ the absence, with the reason, off the host.
func TestAbsenceIsObservable(t *testing.T) {
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	absent := map[string]string{}
	if err := mount(app, dead(t, "pubsub", "/v1/pubsub"), true, "s", "", absent); err != nil {
		t.Fatal(err)
	}
	health(app, absent)

	code, ctype, body := do(t, app, "/healthz")

	// Liveness NEVER moves for an optional plugin. Failing it would recreate the
	// outage one layer up: K8s would restart a pod that is serving every other
	// subsystem correctly.
	if code != 200 {
		t.Fatalf("GET /healthz = %d with one absent subsystem, want 200 — an optional plugin must not fail liveness", code)
	}
	if !strings.Contains(ctype, "application/json") {
		t.Fatalf("GET /healthz Content-Type = %q, want JSON", ctype)
	}

	var got struct {
		Status string            `json:"status"`
		Absent map[string]string `json:"absent"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("GET /healthz body %q is not JSON: %v", body, err)
	}
	if got.Status != "ok" {
		t.Errorf(`/healthz status = %q, want "ok" — the field is about THIS PROCESS, which is up and routing`, got.Status)
	}
	why, named := got.Absent["pubsub"]
	if !named {
		t.Fatalf("/healthz does not name the absent subsystem: %q\n"+
			"a probe cannot tell this pod apart from a healthy one, which is how a dead plane ships behind a green deploy", body)
	}
	// The REASON, not just the name: "staged" and "failed" both answer 503, and
	// without the reason an operator cannot tell a subsystem this deployment never
	// ran from one that died.
	if !strings.Contains(why, "exited before listening") {
		t.Errorf("/healthz absent[pubsub] = %q, want the start failure — the name alone does not say what to fix", why)
	}
}

// TestAHealthyHostReportsNoAbsence pins the other side, so "absent" cannot become
// a field that is always populated and therefore always ignored.
func TestAHealthyHostReportsNoAbsence(t *testing.T) {
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	absent := map[string]string{}
	if err := mount(app, good(t, "flags", "/v1/flags", `{}`), false, "s", "", absent); err != nil {
		t.Fatal(err)
	}
	health(app, absent)

	_, _, body := do(t, app, "/healthz")
	if strings.Contains(body, "absent") {
		t.Errorf("/healthz on a healthy host = %q, want no absent field", body)
	}
}

// TestRequiredAbortsByName is the escape hatch, and it must actually abort —
// otherwise "required" is decoration. It is the ONLY way to express "do not serve
// without this", which is what keeps the choice out of start order.
func TestRequiredAbortsByName(t *testing.T) {
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	a := dead(t, "pubsub", "/v1/pubsub")
	a.Required = true

	err := mount(app, a, true, "s", "", map[string]string{})
	if err == nil {
		t.Fatal("a REQUIRED subsystem that would not start was tolerated — required must abort the host")
	}
	if !strings.Contains(err.Error(), "pubsub") || !strings.Contains(err.Error(), "required") {
		t.Errorf("abort error = %q, want it to name the app and say it was required", err)
	}
}

// TestAnAllowlistWithoutTheBrokerIsRefused pins the OTHER half of the outage —
// why the child had no key at all.
//
// A child launched by this host always carries a CREDZ_TOKEN, and credz refuses
// to fall back to a dev key once a token is present, so a deployment with no
// broker is one where EVERY child resolves Unkeyed and dies at its first store
// open. Reproduced exactly, with the real binaries:
//
//	[pubsub] data-plane encryption posture: no usable key → store opens fail closed
//	[pubsub] audit: open audit store: … cek: CLOUD_KMS_MASTER_KEY_REF is required
//	cloud: zip: Add service 0: zip: Load(pubsub): exited before listening: exit status 1
//
// The host used to mount zero brokers and say nothing, so the only evidence was a
// child complaining about an environment variable the host deliberately scrubs.
// An allowlist that omits the broker is a misconfiguration, refused for the same
// reason a name the manifest does not list is refused.
func TestAnAllowlistWithoutTheBrokerIsRefused(t *testing.T) {
	t.Setenv(launch.RootEnv, devKey)

	// run returns from the guard BEFORE it loads anything or listens, which is the
	// only reason it is callable from a test at all.
	err := run(":0", ":0", "pubsub") // no "kms"
	if err == nil {
		t.Fatal("an --enable list without the credential broker was accepted: every child would fail closed at its first store open")
	}
	if !strings.Contains(err.Error(), launch.Broker) {
		t.Errorf("refusal = %q, want it to name %q so the fix is obvious", err, launch.Broker)
	}

	// The negative controls are the two lists that must NOT trip it: the broker
	// named explicitly, and the empty list production runs (nil = every app, which
	// includes the broker). Asserted on the guard's own predicate rather than by
	// calling run, because a run that gets past the guard mounts 112 children and
	// blocks in Listen.
	for _, list := range []string{"kms,pubsub", ""} {
		if on := enabled(list); on != nil && !on[launch.Broker] {
			t.Errorf("--enable %q would be refused as missing %q", list, launch.Broker)
		}
	}
}

// TestNothingIsRequiredByDefault is the argued default, pinned. Optional-by-
// default is the whole fix: the host is a router, it holds no state whose absence
// corrupts anything, and every child enforces its own auth in its own process —
// so one child's absence cannot silently weaken another's plane.
//
// Adding a name here must be a deliberate diff with a reason, never a side effect
// of a manifest edit. A candidate would have to show that serving WITHOUT it is
// unsafe rather than merely degraded — note that the credz broker does NOT
// qualify: children that cannot reach it fail closed, so its absence is useless,
// not unsafe.
func TestNothingIsRequiredByDefault(t *testing.T) {
	for _, a := range manifest.Apps {
		if a.Required {
			t.Errorf("%s is Required: serving without it must be UNSAFE, not merely degraded. "+
				"If that is the claim, say why here; aborting the host takes the console, "+
				"the health surface and every healthy sibling with it", a.Name)
		}
	}
}
