package main

// The fleet's spec door, pinned end to end on the REAL host surface.
//
// This file is about one production defect: GET https://api.hanzo.ai/v1/openapi.json
// answered 200 with a 3.7 KB document of EIGHT paths — /.well-known/zip/plugin.json,
// /v1/ai/health, /v1/health, /v1/iam/{wildcard1}, /v1/openapi.json, /v1/{wildcard1},
// /zap, /{wildcard1} — while the fleet serves 1039. Nothing on the host claimed the
// path, so it fell to ai's bare "/v1" and was answered by the ai child describing
// its own router. Every SDK generator, spec-derived CLI and third party reading the
// published spec read that instead, and no gate could see it: the committed artifact
// was correct the whole time, because the artifact is not what the deployment served.
//
// So the questions here are the two nobody was asking:
//
//	WHO answers /v1/openapi.json on the host?   → the host, never a plugin.
//	WHAT does it answer with?                   → openapi.yaml, exactly. The door is
//	                                              unauthenticated, so what it answers
//	                                              with is the CUSTOMER contract, the
//	                                              same bytes every SDK is generated
//	                                              from. It answered with the internal
//	                                              document until openapi.MountFleet
//	                                              projected it.
//
// Both are asked of the real thing: mount() and spec() are the host's own, in the
// host's own order, and only the WIRE is replaced — each plugin becomes an oracle
// that answers with its own name, the technique manifest/router_test.go uses to
// turn "where does this request go" into a value a test can read.

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/webui"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
	"sigs.k8s.io/yaml"
)

// golden is the artifact the SDK repos pull AND the bytes the host answers with —
// the customer contract. served is everything this fleet actually routes, which
// is a different question and has a different reader: an address is served
// whatever audience may call it. Both relative to this package.
const (
	golden = "../../openapi.yaml"
	served = "../../private.yaml"
)

// oracle answers with its own address, so a mounted plugin's NAME is what comes
// back off the wire. It replaces the transport and nothing else: the prefixes,
// the Load, and the match are the host's own.
type oracle string

func (o oracle) Do(_ *fasthttp.Request, resp *fasthttp.Response) error {
	resp.SetStatusCode(200)
	resp.SetBodyString(string(o))
	return nil
}

// host builds the host's whole routing surface — every app at its declared
// prefixes, the spec door, and the console catch-all — through the same mount(),
// spec() and webui.Mount() run() calls, in the same order. It starts no process:
// CLOUD_<NAME>_ADDR resolves every app to the oracle, which is the rung of
// manifest's ladder that mounts a client and spawns nothing.
func host(t *testing.T) *zip.App {
	t.Helper()
	zip.RegisterTransport("oracle", zip.Transport{Dial: func(addr string) zip.Client { return oracle(addr) }})

	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	absent := map[string]string{}
	// Before the mounts, exactly as run() composes it: middleware reaches only
	// what is composed after it, and the index answers addresses the mounts claim.
	index(app, manifest.Names())
	for _, a := range manifest.Apps {
		t.Setenv("CLOUD_"+strings.ToUpper(strings.NewReplacer("-", "_").Replace(a.Name))+"_ADDR", "oracle://"+a.Name)
		if err := mount(app, a, false, absent); err != nil {
			t.Fatalf("mount %s: %v", a.Name, err)
		}
	}
	health(app, absent)
	spec(app, manifest.Names()) // no --enable: the whole fleet, as production runs it
	if err := webui.Mount(app, consoleBundle()); err != nil {
		t.Fatalf("mount console: %v", err)
	}
	return app
}

// TestTheSpecDoorIsTheHostsNotACatchAlls is the defect, as a test.
func TestTheSpecDoorIsTheHostsNotACatchAlls(t *testing.T) {
	app := host(t)

	code, ctype, body := do(t, app, openapi.Path)
	if code != 200 {
		t.Fatalf("GET %s = %d, want 200 — the published spec must be readable without credentials", openapi.Path, code)
	}
	// The oracle answers with an app's NAME, so a bare name here IS the misroute:
	// a plugin took the door and this is the child that would have described its
	// own router as the whole API.
	if to := strings.TrimSpace(body); !strings.HasPrefix(to, "{") {
		t.Fatalf("GET %s was answered by the %q plugin, not by the host. A static path beats the "+
			"wildcard containing it, so this means an app row claims %s exactly (fiber merges "+
			"byte-identical patterns and the host's handler sits behind the proxy) or spec() is "+
			"no longer registered.", openapi.Path, to, openapi.Path)
	}
	if !strings.Contains(ctype, "application/json") {
		t.Errorf("GET %s Content-Type = %q, want JSON", openapi.Path, ctype)
	}

	// The other half: we claimed ONE address, not the family. The OpenAI-compatible
	// surface is ai's whole product and it must still reach ai — the same paths
	// manifest/router_test.go pins, re-asked here because the host is the router
	// that actually serves them.
	for _, p := range []string{"/v1/chat/completions", "/v1/models", "/v1/messages"} {
		if _, _, to := do(t, app, p); to != "ai" {
			t.Errorf("GET %s -> %q, want ai — claiming the spec door took the catch-all with it", p, to)
		}
	}
}

// The command door opens on the host's own mount, and answers with the
// projection of the document served beside it.
//
// Asked through spec() — the host's real registration — because the door is not
// written down anywhere else: openapi.serve registers both addresses, so a change
// that kept the document and dropped the palette's list would be invisible to
// every gate in the openapi package, which tests serve directly.
//
// WHAT THIS DOES NOT ASK, and where it is asked instead: whether the door beats
// ai's bare "/v1" on the full host. It is the same trap the spec door fell into
// — a static path wins RIGHT UP UNTIL an app row claims it exactly, and then
// fiber merges the patterns and the host's handler sits silently behind a proxy —
// but the mechanism is a manifest row, and manifest.TestNoAppClaimsAHostDoor asks
// it there, of openapi.Door, for both doors at once. The full-fleet version
// belongs here beside TestTheSpecDoorIsTheHostsNotACatchAlls and cannot be
// written yet: that test is red on this tree because openapi.Fleet refuses the
// whole compose over a duplicate operationId (get_billing_portal_methods, two
// billing routes), so the host answers 500 on BOTH doors. Add it in the commit
// that fixes the compose.
func TestTheCommandDoorOpensOnTheHostsOwnMount(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	spec(app, []string{"kms", "flags"})

	code, ctype, body := do(t, app, openapi.CommandPath)
	if code != 200 {
		t.Fatalf("GET %s = %d, want 200 — the command list must be readable without credentials",
			openapi.CommandPath, code)
	}
	if !strings.Contains(ctype, "application/json") {
		t.Errorf("GET %s Content-Type = %q, want JSON", openapi.CommandPath, ctype)
	}

	var cmds []zip.Command
	if err := json.Unmarshal([]byte(body), &cmds); err != nil {
		t.Fatalf("GET %s did not answer with a command list (%v): %.120q", openapi.CommandPath, err, body)
	}
	if len(cmds) == 0 {
		t.Fatal("the host serves NO commands — this gate proved nothing")
	}

	// Every command names an operation the document served beside it carries.
	// Two addresses, one artifact — that is the whole claim.
	_, _, doc := do(t, app, openapi.Path)
	published := paths(t, "the host", []byte(doc))
	for _, c := range cmds {
		if _, ok := published[template(c.Path)]; !ok {
			t.Errorf("command %s %s (%s %s) names a path the served document does not carry",
				c.Service, c.Name, c.Method, c.Path)
		}
	}
	t.Logf("%d commands over %d served paths", len(cmds), len(published))
}

// template converts the router's ":name" form back to the document's "{name}".
func template(path string) string {
	if !strings.Contains(path, ":") {
		return path
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			parts[i] = "{" + p[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}

// TestTheServedDocumentIsTheArtifact is the second question, and the one that
// keeps the fix from becoming a second source of truth.
//
// The host does not serve A document about the fleet — it serves THE document,
// the same bytes mk/fleet.mk check regenerates from source and refuses to
// let drift. Both sides are produced by openapi.Fleet over the same committed
// subsets, so this is byte equality, not a resemblance check: render what the
// host served through the same JSONToYAML that writes the golden and the two
// files are identical. A route that moved without the artifact being regenerated
// fails the gate; it cannot ship and be discovered on the wire.
func TestTheServedDocumentIsTheArtifact(t *testing.T) {
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v — run `make describe`", golden, err)
	}
	_, _, body := do(t, host(t), openapi.Path)

	// Path COUNT first: it is the number that says how much API is published, and
	// the failure it catches (a near-empty spec answering 200) is the whole defect.
	served := paths(t, "the host", []byte(body))
	committed := paths(t, golden, want)
	if len(served) != len(committed) {
		t.Fatalf("the host serves %d paths; %s carries %d. Every SDK generator reads what the "+
			"deployment answers, so a smaller number here is published surface that no generated "+
			"client can reach.", len(served), golden, len(committed))
	}
	if len(served) == 0 {
		t.Fatal("the host serves a document with NO paths — this gate proved nothing")
	}

	got, err := yaml.JSONToYAML([]byte(body))
	if err != nil {
		t.Fatalf("render the served document: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the host serves %d paths and %s carries %d, but the documents are not the same "+
			"bytes. They are composed by one function (openapi.Fleet) over one set of files, so this "+
			"means the host read something else — check plugin.Spec's embed against plugin/*/openapi.json.",
			len(served), golden, len(committed))
	}
	t.Logf("%d paths served, byte-identical to %s", len(served), golden)
}

// TestTheDocumentIsScopedToWhatTheDeploymentRuns keeps the property the fused
// binary had for free and the host could silently lose: the spec describes THIS
// deployment.
//
// A white-label that runs a subset (--enable / CLOUD_ENABLE) must not publish the
// subsystems it does not run — a route in the document that 404s on the wire is
// the same lie as a route on the wire that is not in the document, and cheaper to
// ship. Production names no allowlist, which is why the artifact comparison above
// is the whole fleet.
func TestTheDocumentIsScopedToWhatTheDeploymentRuns(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	spec(app, []string{"kms", "flags"})

	_, _, body := do(t, app, openapi.Path)
	served := paths(t, "a two-app deployment", []byte(body))
	if len(served) == 0 {
		t.Fatal("a scoped deployment published NOTHING")
	}
	for p := range served {
		if openapi.Door(p) {
			continue // the doors themselves, which every deployment serves
		}
		if !strings.HasPrefix(p, "/v1/kms") && !strings.HasPrefix(p, "/v1/flags") {
			t.Errorf("a deployment running only kms and flags publishes %q — an SDK generated "+
				"off this document offers a call that 404s here", p)
		}
	}
	t.Logf("%d paths for a two-app deployment (the whole fleet is %d)", len(served), len(manifest.Apps))
}

// paths is a document's path set, whatever encoding it arrived in — sigs.k8s.io/yaml
// reads JSON as the YAML subset it is, so one decoder answers for both sides.
func paths(t *testing.T, what string, doc []byte) map[string]json.RawMessage {
	t.Helper()
	var d struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := yaml.Unmarshal(doc, &d); err != nil {
		t.Fatalf("%s did not answer with an OpenAPI document (%v): %.120q", what, err, doc)
	}
	return d.Paths
}
