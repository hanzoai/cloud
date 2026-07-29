package manifest

// The ROUTER ORACLE: does a published path reach the app that serves it?
//
// It asks the ROUTER. Not the golden, not another document — the actual zip
// router this manifest builds, loaded through the actual zip.Load the host calls,
// answered by the actual first-match the fleet performs. A request goes in and
// the name of the app that received it comes out.
//
// That is the entire point, and it is the lesson of the check this replaces.
// openapi/weave_test.go used to look for the same defect and could not find it:
// it exempted any path already present in openapi.yaml, which is the artifact it
// was protecting, and it reported what survived with t.Logf. Every one of the
// paths below is in openapi.yaml, so the check printed nothing and passed while
// the fleet routed 58 published paths somewhere else. A gate that compares two
// DERIVED things agrees with itself while both are wrong — plugin/ingress lost
// eight paths from every published SDK exactly that way. One side of a
// comparison has to be SOURCE. Here it is: manifest.Apps is hand-authored, and
// the router is built from it.
//
// The other side is each app's own subset (plugin/<app>/openapi.json), projected
// by that app's binary from that app's router. It is derived — but it is forced
// back to source on every `make test`, which regenerates it and fails on any diff
// (mk/fleet.mk openapi-check). Nothing here is forced back to anything by being
// committed.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// unreachable is the LEDGER: every published path the fleet does NOT deliver to
// the app that serves it, and where it goes instead. These are DEFECTS, recorded
// so that the 59th cannot hide among them — not exemptions, and not a shape a new
// one may take. The list may only SHRINK: an entry that stops being true fails
// this test as loudly as a new one, because a fix nobody records is a fix nobody
// can see. Fixing one is one line here and one line in Apps.
//
// Three causes, all of them at the composition root:
//
//   - AN ADDRESS ANOTHER APP OWNS (15). A prefix is a SUBTREE and it is
//     exclusive. The /v1/billing REMAINDER belongs to account-bridge — the
//     session-scoped data bridge the console calls — so the billing leaves
//     commerce publishes but the manifest does not name deeper (invoices,
//     subscriptions, spend-alerts, payouts, payment-config, topup/token) reach
//     the bridge and are served only as far as its forwardable allowlist
//     reaches. /v1/billing/payment-methods is the billing app's; /v1/s3 is
//     storage's. (The webhook + auto-recharge + catalog + plans + tenant
//     families that used to sit here are routed now: commerce's row names each
//     one deeper than the sibling that was swallowing it.)
//   - NO APP AT ALL (11). git's /:org/:repo tree, team's /collaborator and iam's
//     /.well-known/* are claimed by nobody, so they fall past every prefix to the
//     console the host serves at "/" — an SDK call gets the HTML shell.
//   - ONE NAME, TWO OWNERS (1). /v1/tracker, below.
//
// Regenerating it is mechanical: the failure below prints the current list, in
// this format, ready to paste.
var unreachable = []string{
	// The five analytics INGESTION doors that used to sit here were not a backlog
	// item: they were a live outage. Every beacon the products emit landed on
	// commerce's bare "/v1" and answered 405, so the warehouse stopped receiving
	// events at 2026-07-29 04:15:29 — eighteen seconds after the ReplicaSet running
	// the first image where manifest.Apps is the actual router. Routed now.
	//
	// /v1/tracker stays, and stays UNREACHABLE ON PURPOSE. apps/analytics wants it
	// as the @hanzo/capture unload beacon; apps/tracker owns the name for the issue
	// tracker and got there first. Routing it to analytics would break the issue
	// tracker, so the alias is retired at the caller instead — one name, one owner.
	"analytics /v1/tracker -> tracker",
	"commerce /v1/billing/invoices -> account-bridge",
	"commerce /v1/billing/invoices/{id}/pdf -> account-bridge",
	"commerce /v1/billing/payment-config -> account-bridge",
	"commerce /v1/billing/payment-methods -> billing",
	"commerce /v1/billing/payouts -> account-bridge",
	"commerce /v1/billing/plans -> account-bridge",
	"commerce /v1/billing/spend-alerts -> account-bridge",
	"commerce /v1/billing/spend-alerts/authorize -> account-bridge",
	"commerce /v1/billing/spend-alerts/{id} -> account-bridge",
	"commerce /v1/billing/subscriptions -> account-bridge",
	"commerce /v1/billing/subscriptions/{id}/cancel -> account-bridge",
	"commerce /v1/billing/subscriptions/{id}/reactivate -> account-bridge",
	"commerce /v1/billing/topup/token -> account-bridge",
	"git / -> nothing",
	"git /{org}/{repo} -> nothing",
	"git /{org}/{repo}/blob/{wildcard1} -> nothing",
	"git /{org}/{repo}/commits -> nothing",
	"git /{org}/{repo}/git-receive-pack -> nothing",
	"git /{org}/{repo}/git-upload-pack -> nothing",
	"git /{org}/{repo}/info/refs -> nothing",
	"git /{org}/{repo}/tree/{wildcard1} -> nothing",
	"iam /.well-known/{wildcard1} -> nothing",
	"provisioning /v1/s3 -> storage",
	"provisioning /v1/s3/{name} -> storage",
	"team /collaborator -> nothing",
	"team /collaborator/rpc/{documentId} -> nothing",
}

// oracle is the transport the probe mounts every app on. A mounted app is
// whatever answers at its address, so an address that answers with its own NAME
// turns "where does this request go" into a value the test can read. It replaces
// only the WIRE — Load, Mount, the route patterns and the match are the fleet's
// own, so nothing about routing is reimplemented here.
type oracle string

func (o oracle) Do(_ *fasthttp.Request, resp *fasthttp.Response) error {
	resp.SetStatusCode(http.StatusOK)
	resp.SetBodyString(string(o))
	return nil
}

// router builds the host's routing surface: every app, at its declared prefixes,
// through the same zip.Load cmd/cloud calls. It starts no process and opens no
// socket.
func router(t *testing.T) *zip.App {
	t.Helper()
	zip.RegisterTransport("oracle", zip.Transport{Dial: func(addr string) zip.Client { return oracle(addr) }})
	app := zip.New(zip.Config{AppName: "router-oracle", DisableStartupMessage: true})
	for _, a := range Apps {
		if err := app.Add(zip.Load(zip.Plugin{Name: a.Name, Addr: "oracle://" + a.Name}, a.Prefixes...)); err != nil {
			t.Fatalf("%s: %v", a.Name, err)
		}
	}
	return app
}

// destination is the app a request for path reaches, or "nothing" when no prefix
// matches — in the deployed host that case falls through to the console at "/",
// which answers an SDK with the HTML shell.
//
// One method answers for all of them: the host mounts a plugin with All(prefix)
// and All(prefix+"/*") (zip load.go → mountVia), so which app receives a request
// is a function of the PATH alone.
func destination(t *testing.T, app *zip.App, path string) string {
	t.Helper()
	resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, concrete(path), nil))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "nothing"
	}
	name, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(name)
}

// concrete turns a document path into a request the router can match: an OpenAPI
// template names its parameters ({org}) and a request carries values. The value
// is deliberately one no route spells literally, so a substitution can never land
// on a sibling's static segment and report the wrong app.
func concrete(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") {
			segs[i] = "zzq"
		}
	}
	return strings.Join(segs, "/")
}

// served is every path an app's own binary answers, read from the subset that
// binary projected from its own router.
func served(t *testing.T, app string) []string {
	t.Helper()
	path := filepath.Join("..", "plugin", app, "openapi.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v\n\nEvery app publishes its own subset. Run `make -f mk/fleet.mk openapi-apps`.", path, err)
	}
	var doc struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	out := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// TestEveryServedPathReachesTheAppThatServesIt is the gate. A path an app
// publishes and the fleet delivers elsewhere is published surface that answers
// someone else's 404 — and it is published: openapi.yaml is woven from these same
// subsets, so every generated SDK in every language carries a method for it.
func TestEveryServedPathReachesTheAppThatServesIt(t *testing.T) {
	fleet := router(t)
	known := map[string]bool{}
	for _, e := range unreachable {
		known[e] = true
	}

	var found, unrecorded []string
	paths := 0
	for _, a := range Apps {
		for _, p := range served(t, a.Name) {
			paths++
			to := destination(t, fleet, p)
			if to == a.Name {
				continue
			}
			entry := a.Name + " " + p + " -> " + to
			found = append(found, entry)
			if !known[entry] {
				unrecorded = append(unrecorded, entry)
			}
			delete(known, entry)
		}
	}

	// A gate that examined nothing passes. Say how much it examined, and refuse the
	// vacuous run outright — every way this could inspect zero paths (an empty
	// manifest, subsets that decoded to nothing) is a defect somewhere else that
	// would otherwise arrive here as a green tick.
	if paths == 0 {
		t.Fatal("no published paths were examined — this gate proved nothing")
	}

	// A NEW one. This is the failure the gate exists for: it fires on the routing,
	// so no committed document can talk it out of firing.
	for _, e := range unrecorded {
		t.Errorf("UNREACHABLE: %s — the fleet publishes this path and routes it elsewhere. "+
			"Name the prefix that reaches it in this app's manifest.Apps row (a prefix owns its whole "+
			"SUBTREE, so it must be deeper than the sibling that currently wins), or stop serving it.", e)
	}
	// A FIXED one, still recorded. The ledger is the count of the defect; a stale
	// entry inflates it and hides the next real one behind a number nobody trusts.
	for e := range known {
		t.Errorf("FIXED, STILL LISTED: %q — this path now reaches its app. Delete the line from `unreachable`.", e)
	}
	if len(unrecorded) > 0 || len(known) > 0 {
		sort.Strings(found)
		t.Logf("the ledger as it stands now — paste over `unreachable`:\n\t%q,", strings.Join(found, "\",\n\t\""))
	}
	t.Logf("%d published paths across %d apps; %d reach their app, %d do not (all recorded)",
		paths, len(Apps), paths-len(found), len(found))
}

// TestOpenAISurfaceLandsOnAI pins the requests every OpenAI-compatible SDK
// actually sends. The oracle above sees ai's surface as ONE published path — the
// /v1 catch-all — so a wrong owner of the /v1 remainder costs it a single entry;
// here it is spelled out as the concrete user-facing endpoints, because each of
// these misrouting is the whole product being down for every caller. No other
// app names any of these paths deeper, so whichever row holds "/v1" answers all
// of them: that row must be ai's.
func TestOpenAISurfaceLandsOnAI(t *testing.T) {
	fleet := router(t)
	for _, p := range []string{
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/messages",
		"/v1/models",
		"/v1/embeddings",
		"/v1/responses",
		"/v1/audio/speech",
		"/v1/audio/transcriptions",
		"/v1/images/generations",
	} {
		if to := destination(t, fleet, p); to != "ai" {
			t.Errorf("%s -> %s, want ai — the OpenAI-compatible surface is unreachable", p, to)
		}
	}
}
