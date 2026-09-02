package main

// The hypermedia layer, asked of the REAL host surface.
//
// The index answers two addresses that live inside subtrees the mounts already
// claim — /v1 is ai's remainder, /v1/<capability> is each capability's own root —
// so the only thing that can go wrong with it is the thing that would go wrong
// silently: it answers where a capability was answering, and a published
// operation starts returning an index of itself. That is what this file refuses,
// and it refuses it against the fleet's own published surface rather than against
// a list kept here.
//
// Same technique as openapi_test.go beside it: every plugin is an oracle that
// answers with its own NAME, so "who answered" is a value the test can read.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
	"sigs.k8s.io/yaml"
)

// reply drives one request through the host and hands back everything a caller can
// see. do() beside it answers three fields; the links are headers, so this one
// keeps the response.
func reply(t *testing.T, app *zip.App, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://cloud"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

// TestTheRootIndexIsTheHostsNotTheRemainders is the whole reason the index is
// middleware rather than a route.
//
// ai's manifest row is the bare "/v1", and zip mounts a prefix as All(prefix) as
// well as All(prefix+"/*") — so the host cannot REGISTER a route at /v1 at all;
// two definitions claiming one address is a composition zip refuses outright.
// Composed ahead of the mounts, the host answers it and ai's remainder is
// untouched one segment down.
func TestTheRootIndexIsTheHostsNotTheRemainders(t *testing.T) {
	app := host(t)

	resp, body := reply(t, app, openapi.RootPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 — the API root must be readable without credentials",
			openapi.RootPath, resp.StatusCode)
	}
	// The oracle answers with an app's NAME, so a bare name here IS the misroute.
	if to := strings.TrimSpace(body); !strings.HasPrefix(to, "{") {
		t.Fatalf("GET %s was answered by the %q plugin, not by the host — the index is composed "+
			"after the mounts, or not at all", openapi.RootPath, to)
	}
	var root openapi.Root
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		t.Fatalf("GET %s is not the capability index (%v): %.160q", openapi.RootPath, err, body)
	}
	if len(root.Capabilities) < 50 {
		t.Fatalf("the root lists %d capabilities; the fleet is %d apps — a near-empty index answering "+
			"200 is the failure this gate exists for", len(root.Capabilities), len(manifest.Apps))
	}
	for _, c := range root.Capabilities {
		if c.Stage != "ga" {
			t.Errorf("%s is listed at stage %q — only a generally available capability is listed at all, "+
				"because a caller without the flag must not learn the others exist", c.Name, c.Stage)
		}
		if c.Name == openapi.Operator {
			t.Errorf("the operator's product is listed in the API root")
		}
	}
	if !sort.SliceIsSorted(root.Capabilities, func(i, j int) bool {
		return root.Capabilities[i].Name < root.Capabilities[j].Name
	}) {
		t.Error("the capabilities are not in name order — the answer must be a function of the document, not of a map walk")
	}

	// BY NAME, on the fleet's own non-ga capability, because a rule that excludes
	// nothing is a rule nobody is testing. Whichever row carries a stage today is
	// the one this must not list; the manifest is asked for it rather than a name
	// being written here, so the check survives the row being promoted.
	for _, a := range manifest.Apps {
		if a.Stage == "" {
			continue
		}
		for _, c := range root.Capabilities {
			if c.Name == a.Name {
				t.Errorf("%s is %s and the API root lists it — a caller without the flag "+
					"must not learn it exists", a.Name, a.Stage)
			}
		}
		if resp, body := reply(t, app, openapi.RootPath+"/"+a.Name); strings.HasPrefix(strings.TrimSpace(body), `{"name":"`+a.Name) {
			t.Errorf("GET /v1/%s answered %d with an index of a %s capability", a.Name, resp.StatusCode, a.Stage)
		}
	}
	t.Logf("%d capabilities at %s", len(root.Capabilities), openapi.RootPath)
}

// oneSegment is every published address exactly one segment under /v1 — the
// addresses the capability index sits among.
var oneSegment = regexp.MustCompile(`^/v1/[^/{]+$`)

// THE GATE. Every published one-segment READ must still reach the app that serves
// it. This is the failure mode of a remainder route: a static claim inside
// somebody else's subtree wins by specificity, and 60 collection endpoints —
// /v1/models and the whole OpenAI-compatible wire among them — sit at exactly that
// depth.
//
// Per (method, path), which is the unit the index yields on and the unit an
// operation is: an address that only ACTS at its root publishes no GET there, so
// the GET is the index's and the POST is still the capability's. Both directions
// are asserted, because a rule tested one way is a rule that can be inverted
// without anything going red.
//
// Asked of the golden rather than of a list here, so a route added at that depth
// tomorrow is covered by this gate the day it is published.
func TestTheIndexShadowsNoPublishedRead(t *testing.T) {
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v — run `make -f mk/fleet.mk describe`", golden, err)
	}
	var d struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := yaml.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}

	app := host(t)
	reads, acts := 0, 0
	for path, methods := range d.Paths {
		if !oneSegment.MatchString(path) || openapi.Host(path) {
			continue // these are the host's own; everything else belongs to an app
		}
		resp, body := reply(t, app, path)
		to := strings.TrimSpace(body)
		answered := resp.StatusCode == http.StatusOK && strings.HasPrefix(to, "{")
		if _, published := methods["get"]; published {
			reads++
			if answered {
				t.Errorf("GET %s was answered by the host (%d %.80q), not by the app that serves it — "+
					"the index took a published read, and every caller of it now gets an index of "+
					"the capability instead of the answer", path, resp.StatusCode, body)
				continue
			}
			if owner := manifest.OwnerOf(path); owner != "" && to != owner {
				t.Errorf("GET %s reached %q; the manifest routes it to %q", path, to, owner)
			}
			continue
		}
		// No published read at this address. If it is a capability's own name the
		// index answers, and the link in the root resolves instead of 405ing.
		acts++
		if !answered && manifest.OwnerOf(path) != "" {
			t.Logf("GET %s is unpublished and unanswered by the index (%d) — it reaches %q",
				path, resp.StatusCode, to)
		}
	}
	if reads == 0 {
		t.Fatal("no published one-segment read was examined — this gate proved nothing")
	}
	t.Logf("%d published reads one segment under /v1 still reach their app; %d addresses at that "+
		"depth publish no read", reads, acts)
}

// The other half of the same property: where a capability answers NOTHING at its
// own root, following the root index's href reaches the index rather than the
// child's 404. kms is one — its whole surface is /v1/kms/<noun>.
func TestFollowingACapabilityHrefReachesItsIndex(t *testing.T) {
	app := host(t)

	resp, body := reply(t, app, "/v1/kms")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/kms = %d — the root index links here and a link that 404s is not a link",
			resp.StatusCode)
	}
	var ix openapi.Index
	if err := json.Unmarshal([]byte(body), &ix); err != nil {
		t.Fatalf("GET /v1/kms is not a capability index (%v): %.160q", err, body)
	}
	if ix.Name != "kms" || len(ix.Operations) == 0 {
		t.Fatalf("GET /v1/kms answered %+v — the index names the capability and its operations", ix)
	}
	for _, o := range ix.Operations {
		if !strings.HasPrefix(o.Href, "/v1/kms") {
			t.Errorf("kms's index carries %s %s, which is not kms's address", o.Method, o.Href)
		}
	}
	if ix.Links["up"].Href != openapi.RootPath {
		t.Errorf("kms's index does not link back to %s", openapi.RootPath)
	}
}

// A name nothing publishes is answered by whatever answers every other unclaimed
// address under /v1 — the remainder — so the surface cannot be asked which names
// exist. On the real host both fall to ai; here ai is an oracle, so both come
// back as the same bytes from the same app.
func TestAnUnpublishedNameIsAnsweredLikeAnyUnclaimedAddress(t *testing.T) {
	app := host(t)

	nameShaped, a := reply(t, app, "/v1/quasar")
	invented, b := reply(t, app, "/v1/nothing-is-here")
	if nameShaped.StatusCode != invented.StatusCode || a != b {
		t.Errorf("a capability-shaped name answers %d %.60q and an invented one answers %d %.60q — "+
			"the difference is an oracle for which capabilities exist",
			nameShaped.StatusCode, a, invented.StatusCode, b)
	}
}

// Every /v1 answer says where it came from, where the whole API is described and
// where the index of it starts — including an answer a PLUGIN produced, which is
// the only place these can be added: a proxied response is written by the far end
// into this very response object, so anything stamped before the hop is gone.
func TestEveryAnswerUnderTheContractCarriesItsLinks(t *testing.T) {
	app := host(t)

	for _, path := range []string{"/v1/kms/secrets", "/v1/chat/completions", openapi.RootPath, openapi.Path} {
		resp, _ := reply(t, app, path)
		got := strings.Join(resp.Header.Values("Link"), " ")
		for _, want := range []string{
			`<` + path + `>; rel="self"`,
			`<` + openapi.Path + `>; rel="describedby"`,
			`<` + openapi.RootPath + `>; rel="index"`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("GET %s carries Link %q, missing %q", path, got, want)
			}
		}
	}

	// Outside the contract's namespace the CONTRACT has nothing to point at. The
	// SERVICE still does: `service-desc` and `service-doc` (RFC 8631) name where
	// this service is described and documented, and they are properties of the
	// service rather than of /v1, so the framework stamps them on every answer.
	// What must not appear off /v1 is the contract's own pair — `describedby` and
	// `index` — because those say "this answer is part of the described API".
	resp, _ := reply(t, app, "/healthz")
	got := strings.Join(resp.Header.Values("Link"), " ")
	for _, unwanted := range []string{`rel="describedby"`, `rel="index"`} {
		if strings.Contains(got, unwanted) {
			t.Errorf("GET /healthz carries %q — %s belongs to /v1", got, unwanted)
		}
	}
}
