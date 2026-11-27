package research

// These tests file the decision thread the way a DDT client and Kai do, with the
// real records: the APU auxiliary-power DDT Decision (hanzo-inc/kai
// examples/apu/ddt.json), the Evaluation Run Kai exported for it
// (army/apu/ddt.export.json) and the Decision Packages Kai recorded
// (army/apu/recorded.json), all committed verbatim under testdata/ddt.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/pubsub"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/authz"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ada is a person's IAM subject.
const ada = "4f1c0e8a-0000-4000-8000-0000000000ad"

// credentials are what the identity boundary mints for each credential a test
// sends: a person's own token, an API key, an application (a service account),
// and an agent's as() token, which carries the member's claims and no holder.
var credentials = map[string]principal.Principal{
	"person": {Org: "acme", User: ada, Subject: ada, Stable: true, Kind: principal.Person, Holder: "acme/ada", Client: "hanzo-app"},
	"key":    {Org: "acme", User: "acme/ada", Subject: "acme/ada", Stable: true, Kind: principal.Key, Key: "k1"},
	"app":    {Org: "acme", User: "app:acme/kai", Subject: "acme/kai", Stable: true, Kind: principal.App, Holder: "acme-kai", Client: "acme-kai"},
	"agent":  {Org: "acme", User: ada, Subject: ada, Stable: true, Kind: principal.Person},
}

// mountThread mounts research behind a stand-in for the identity boundary: the
// test is the boundary here, as it is the composer, and it mints the principal
// for the credential the request names.
func mountThread(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	app.Use(zip.H(func(c *zip.Ctx) error {
		if p, ok := credentials[c.Header("X-Test-As")]; ok {
			principal.Mint(c, p)
		}
		return c.Continue()
	}))
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app
}

// as sends one request with who's credential ("" is nobody) and answers the
// status and the decoded body.
func as(t *testing.T, app *zip.App, who, method, path string, body any) (int, map[string]any) {
	t.Helper()
	return asWith(t, app, who, method, path, body, nil)
}

// asWith is as, with the request edited before it is sent: headers the boundary
// mints from a token, such as its project scope or a home org it left out.
func asWith(t *testing.T, app *zip.App, who, method, path string, body any, edit func(*http.Request)) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if who != "" {
		p := credentials[who]
		req.Header.Set("X-Org-Id", p.Org)
		req.Header.Set("X-User-Id", p.User)
		req.Header.Set("X-Test-As", who)
		// A person's home org, as the boundary mints it from the token's owner, and
		// the SuperAdmin bit it mints when that home is the reserved admin org.
		if home, _, ok := strings.Cut(p.Holder, "/"); ok && p.Kind == principal.Person {
			req.Header.Set("X-User-Owner", home)
			if home == authz.AdminOrg {
				req.Header.Set("X-User-IsAdmin", "true")
			}
		}
	}
	if edit != nil {
		edit(req)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m == nil {
		m = map[string]any{"raw": string(raw)}
	}
	return resp.StatusCode, m
}

// must is as, refusing any answer but 200.
func must(t *testing.T, app *zip.App, who, method, path string, body any) map[string]any {
	t.Helper()
	code, m := as(t, app, who, method, path, body)
	if code != http.StatusOK {
		t.Fatalf("%s %s as %s: %d %v", method, path, who, code, m)
	}
	return m
}

// file reads a committed DDT record.
func file(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile("testdata/ddt/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// recordedRun is one run Kai recorded: its name and its Decision Package.
type recordedRun struct {
	Name    string                     `json:"name"`
	Package map[string]json.RawMessage `json:"package"`
}

func recordedRuns(t *testing.T) []recordedRun {
	t.Helper()
	var rs []recordedRun
	if err := json.Unmarshal(file(t, "recorded.json"), &rs); err != nil {
		t.Fatalf("recorded.json: %v", err)
	}
	return rs
}

// seen is one piece of evidence as a package records it: uri, hash, content.
type seen struct {
	ID      string          `json:"id"`
	URI     string          `json:"uri"`
	Hash    string          `json:"hash"`
	Content json.RawMessage `json:"content"`
}

func seenIn(t *testing.T, pkg map[string]json.RawMessage) []seen {
	t.Helper()
	var ss []seen
	if err := json.Unmarshal(pkg["evidence"], &ss); err != nil {
		t.Fatalf("package evidence: %v", err)
	}
	return ss
}

// kindOf is the evidence kind each APU input files as.
var kindOf = map[string]string{"charter": "requirement", "thermal": "test", "techdata": "document", "bom": "document", "base": "document"}

// rows reads a listing's data.
func rows(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, r := range m["data"].([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

func list(m map[string]any, k string) []map[string]any {
	var out []map[string]any
	for _, r := range m[k].([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

// The canonical form is Kai's to the byte: every evidence hash and every
// Decision Package hash Kai recorded is the SHA-256 of what canon writes.
func TestCanonHashesAsKaiDoes(t *testing.T) {
	for _, r := range recordedRuns(t) {
		for _, e := range seenIn(t, r.Package) {
			c, err := canon(e.Content)
			if err != nil {
				t.Fatalf("%s: canon: %v", e.URI, err)
			}
			if got := sha(c); got != digest(e.Hash) {
				t.Errorf("%s %s: canon hashes to %s, Kai recorded %s", r.Name, e.URI, got, e.Hash)
			}
		}
		content := map[string]json.RawMessage{}
		for k, v := range r.Package {
			if k != "hash" && k != "trace" {
				content[k] = v
			}
		}
		b, _ := json.Marshal(content)
		c, err := canon(b)
		if err != nil {
			t.Fatalf("package: canon: %v", err)
		}
		var own string
		_ = json.Unmarshal(r.Package["hash"], &own)
		if got := sha(c); got != digest(own) {
			t.Errorf("%s: package content hashes to %s, Kai recorded %s", r.Name, got, own)
		}
	}
	// The rules where Go's encoder and serde_json disagree: <, > and & raw, U+2028
	// raw, a control character as \u00XX, every float without an exponent, -0 as 0.
	got, err := canon([]byte(`{"b":1,"a":[1.0,-0.0,1e21,0.1,1e-7,"<&>` + "\u2028" + `\u0001"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":[1,0,1000000000000000000000,0.1,0.0000001,"<&>` + "\u2028" + `\u0001"],"b":1}`; string(got) != want {
		t.Errorf("canon = %s, want %s", got, want)
	}
	for _, bad := range []string{`1e400`, `{"a":1} {}`, `{"a":`} {
		if c, err := canon([]byte(bad)); err == nil {
			t.Errorf("canon(%s) = %s, want refused", bad, c)
		}
	}
}

// The APU decision, end to end: the DDT Decision, the five pieces of evidence it
// was evaluated on, the Evaluation Run Kai exported, a run filed with its
// Decision Package, then TSB-22 revision B — a new version of one input — and
// the reconsider it publishes naming exactly what rests on the old version.
func TestAPURoundTrip(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// The bus, as the fleet runs it: the embedded node, dialled in-process.
		t.Setenv("CLOUD_PUBSUB_HOST", "127.0.0.1")
		t.Setenv("CLOUD_PUBSUB_PORT", "-1")
		t.Setenv("CLOUD_PUBSUB_STORE_DIR", t.TempDir())
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
		plane := zip.New(zip.Config{Logger: luxlog.New("test")})
		if err := pubsub.Use(plane, cloud.Deps{}); err != nil {
			t.Fatalf("pubsub.Use: %v", err)
		}
		t.Cleanup(func() { _ = pubsub.Shutdown(context.Background()) })
		heard := make(chan bus.Message, 16)
		if _, err := bus.Subscribe(topic("acme"), heard); err != nil {
			t.Fatalf("subscribe: %v", err)
		}

		app := mountThread(t)
		runs := recordedRuns(t)

		// The DDT Decision, filed verbatim by an agent's API key.
		dec := must(t, app, "key", "POST", "/v1/research/decisions", map[string]any{"decision": file(t, "ddt.json")})
		if dec["version"].(float64) != 1 || dec["supersedes"] != nil || !strings.HasPrefix(dec["id"].(string), "dec_") {
			t.Fatalf("first filing = %v, want version 1 superseding nothing", dec)
		}
		var depends []string
		for _, d := range list(dec, "depends") {
			if d["hash"] != nil {
				t.Errorf("a DDT Decision names uris without versions, and %v carries a hash", d)
			}
			depends = append(depends, d["uri"].(string))
		}
		if want := []string{"ddt:DEC-SPH-0412/need", "run://RUN_THERMAL_SWEEP_0007", "techdata://sph/tsb/22", "plm://sph/BOM/top@R2026.08"}; !slices.Equal(depends, want) {
			t.Fatalf("depends = %v, want %v (evidence, then each assumption's, once each)", depends, want)
		}
		if again := must(t, app, "key", "POST", "/v1/research/decisions", map[string]any{"decision": file(t, "ddt.json")}); again["id"] != dec["id"] || again["version"].(float64) != 1 {
			t.Fatalf("the same decision filed twice = %v, want the version already filed", again)
		}
		bad := map[string]any{"id": "DEC-1", "schemaVersion": "DDT-0.9"}
		if code, _ := as(t, app, "key", "POST", "/v1/research/decisions", map[string]any{"decision": bad}); code != http.StatusUnprocessableEntity {
			t.Errorf("a DDT-0.9 record: %d, want 422", code)
		}

		// The evidence it was evaluated on, with content: the server's hash of each is
		// the one Kai's run read.
		for _, e := range seenIn(t, runs[0].Package) {
			got := must(t, app, "key", "POST", "/v1/research/evidence", map[string]any{
				"uri": e.URI, "kind": kindOf[e.ID], "source": map[string]any{"system": strings.SplitN(e.URI, ":", 2)[0]}, "content": e.Content,
			})
			if got["hash"] != digest(e.Hash) || got["version"].(float64) != 1 || got["content"] == nil {
				t.Fatalf("%s filed as %v, want version 1 of hash %s with its content", e.URI, got, digest(e.Hash))
			}
		}

		// The exported Evaluation Run beside a package it was not exported from is
		// refused; alone, it files, reading every input at the hash filed.
		export := map[string]json.RawMessage{}
		if err := json.Unmarshal(file(t, "ddt.export.json"), &export); err != nil {
			t.Fatal(err)
		}
		pkg, _ := json.Marshal(runs[0].Package)
		if code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": export["evaluationRun"], "package": json.RawMessage(pkg)}); code != http.StatusUnprocessableEntity {
			t.Fatalf("an Evaluation Run beside another package's hash: %d %v, want 422", code, m)
		}
		exported := must(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": export["evaluationRun"]})
		if exported["decisionId"] != "DEC-SPH-0412" || exported["recommendation"] != nil || len(list(exported, "depends")) != 5 {
			t.Fatalf("exported run = %v, want DEC-SPH-0412 reading 5 inputs, no recommendation", exported)
		}

		// The run Kai's export writes for runs[0]'s package: its id and outputs.hash
		// are the package's hash, which is what binds the two.
		var own string
		_ = json.Unmarshal(runs[0].Package["hash"], &own)
		run := map[string]any{}
		_ = json.Unmarshal(export["evaluationRun"], &run)
		run["id"] = "RUN_KAI_" + strings.ToUpper(digest(own)[:16])
		run["outputs"].(map[string]any)["hash"] = own
		packaged := must(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": json.RawMessage(pkg)})
		rec := packaged["recommendation"].(map[string]any)
		ready := rec["readiness"].(map[string]any)
		if rec["selection"] != "A2" || ready["verdict"] != "allow" || ready["decisionClassPolicyId"] != "DCP_CONCEPT_STRATEGY_HIGH_STAKES" || len(rec["ranking"].([]any)) != 3 || rec["flips"] == nil {
			t.Fatalf("recommendation = %v, want A2 ranked first of 3, allow on DCP_CONCEPT_STRATEGY_HIGH_STAKES, with its flips", rec)
		}
		// Kai's own recommendation rides the body, as the client sends it; the server's
		// is the one it derives, and is the same.
		again := must(t, app, "app", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": json.RawMessage(pkg), "recommendation": rec})
		if again["id"] != packaged["id"] {
			t.Fatalf("the same filing with its recommendation = %v, want %v", again["id"], packaged["id"])
		}
		// A run whose readiness says a verdict the sealed package does not is refused.
		forged := maps.Clone(run)
		forged["outputs"] = map[string]any{"hash": own, "readiness": map[string]any{"decisionClassPolicyId": "DCP_CONCEPT_STRATEGY_HIGH_STAKES", "verdict": "deny"}}
		if code, m := as(t, app, "app", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": forged, "package": json.RawMessage(pkg)}); code != http.StatusUnprocessableEntity {
			t.Fatalf("a run whose readiness is not its package's verdict: %d %v, want 422", code, m)
		}

		// The charter files the decision's next version, superseding the first.
		v2 := must(t, app, "key", "POST", "/v1/research/decisions", map[string]any{
			"decision": file(t, "ddt.json"),
			"charter":  map[string]any{"question": "Which auxiliary power source gives 8 h silent watch at the lowest burden?", "owner": "PM HBCT"},
			"program":  map[string]any{"id": "apu.select", "version": 1, "hash": "sha256:0b0e88445da82d8e86cd5d40f243fbfa981be4f486f9769789c102d9955d8baf"},
		})
		if v2["version"].(float64) != 2 || v2["supersedes"] != dec["id"] || v2["program"].(map[string]any)["hash"] != "0b0e88445da82d8e86cd5d40f243fbfa981be4f486f9769789c102d9955d8baf" {
			t.Fatalf("second filing = %v, want version 2 superseding %v", v2, dec["id"])
		}
		versions := rows(must(t, app, "key", "GET", "/v1/research/decisions?decision=DEC-SPH-0412", nil))
		if len(versions) != 2 || versions[0]["id"] != v2["id"] || versions[1]["id"] != dec["id"] {
			t.Fatalf("versions = %v, want v2 then v1", versions)
		}

		// Nothing rests on a stale version yet.
		tsb := "techdata://sph/tsb/22"
		before := must(t, app, "key", "GET", "/v1/research/evidence/dependents?uri="+tsb, nil)
		if ds, es := list(before, "decisions"), list(before, "evaluations"); len(ds) != 1 || ds[0]["id"] != v2["id"] || len(es) != 2 || es[0]["stale"] != false || es[1]["stale"] != false {
			t.Fatalf("dependents = %v, want the latest decision and both evaluations, none stale", before)
		}

		// TSB-22 revision B: the bulletin's next version.
		var revB seen
		for _, e := range seenIn(t, runs[2].Package) {
			if e.URI == tsb {
				revB = e
			}
		}
		if revB.URI == "" || digest(revB.Hash) == before["newest"] {
			t.Fatalf("recorded run 2 does not revise %s", tsb)
		}
		got := must(t, app, "person", "POST", "/v1/research/evidence", map[string]any{"uri": tsb, "kind": "document", "content": revB.Content})
		if got["version"].(float64) != 2 || got["hash"] != digest(revB.Hash) {
			t.Fatalf("revision B = %v, want version 2", got)
		}

		// research.reconsider names the version replaced, the new one, the decision
		// and both evaluations that read revision A.
		msg, err := nextMsg(heard, 5*time.Second)
		if err != nil {
			t.Fatalf("no research.reconsider on %s: %v", topic("acme"), err)
		}
		var ev Reconsider
		if err := json.Unmarshal(msg.Data, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Org != "acme" || !slices.Equal(ev.Changes, []Change{{URI: tsb, From: before["newest"].(string), To: digest(revB.Hash)}}) ||
			!slices.Equal(ev.Decisions, []string{v2["id"].(string)}) ||
			!slices.Contains(ev.Evaluations, exported["id"].(string)) || !slices.Contains(ev.Evaluations, packaged["id"].(string)) || len(ev.Evaluations) != 2 {
			t.Fatalf("reconsider = %+v", ev)
		}
		if extra, err := nextMsg(heard, 200*time.Millisecond); err == nil {
			t.Fatalf("a second event for one new version: %s", extra.Data)
		}

		// Both evaluations now read a stale version; the decision names the uri and no
		// version, so it is never stale by hash. ?hash= narrows to those that read it.
		after := must(t, app, "key", "GET", "/v1/research/evidence/dependents?uri="+tsb, nil)
		for _, e := range list(after, "evaluations") {
			if e["stale"] != true || e["hash"] != before["newest"] {
				t.Errorf("evaluation %v, want stale at revision A", e)
			}
		}
		if ds := list(after, "decisions"); len(ds) != 1 || ds[0]["stale"] != false {
			t.Errorf("decisions %v, want the one decision, not stale by hash", ds)
		}
		old := must(t, app, "key", "GET", "/v1/research/evidence/dependents?uri="+tsb+"&hash=sha256:"+before["newest"].(string), nil)
		if len(list(old, "evaluations")) != 2 || len(list(old, "decisions")) != 0 {
			t.Errorf("?hash=<revision A> = %v, want the two evaluations alone", old)
		}
		if none := must(t, app, "key", "GET", "/v1/research/evidence/dependents?uri="+tsb+"&hash="+digest(revB.Hash), nil); len(list(none, "evaluations")) != 0 {
			t.Errorf("?hash=<revision B> = %v, want nothing read it yet", none)
		}

		// A new version that changes nothing a client holds still publishes, once, and
		// the same version filed again publishes nothing.
		must(t, app, "key", "POST", "/v1/research/evidence", map[string]any{"uri": tsb, "kind": "document", "content": revB.Content})
		if extra, err := nextMsg(heard, 200*time.Millisecond); err == nil {
			t.Fatalf("the same version filed again published %s", extra.Data)
		}

	})
}

// Versions accrue per uri; the same hash is the version already filed, and every
// earlier version is kept and read back with its content.
func TestEvidenceVersions(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		uri := "plm://sph/BOM/top@R2026.08"
		a := map[string]any{"uri": uri, "kind": "document", "source": map[string]any{"system": "plm", "revision": "R2026.08"}, "content": map[string]any{"mass_kg": 920}}
		b := map[string]any{"uri": uri, "kind": "document", "source": map[string]any{"system": "plm", "revision": "R2026.09"}, "content": map[string]any{"mass_kg": 905}}

		v1 := must(t, app, "key", "POST", "/v1/research/evidence", a)
		if again := must(t, app, "key", "POST", "/v1/research/evidence", a); again["id"] != v1["id"] || again["version"].(float64) != 1 {
			t.Fatalf("the same hash filed twice = %v, want version 1 again", again)
		}
		v2 := must(t, app, "key", "POST", "/v1/research/evidence", b)
		if v2["version"].(float64) != 2 || v2["id"] == v1["id"] {
			t.Fatalf("a new hash = %v, want version 2", v2)
		}
		vs := rows(must(t, app, "key", "GET", "/v1/research/evidence?uri="+uri, nil))
		if len(vs) != 2 || vs[0]["version"].(float64) != 2 || vs[1]["version"].(float64) != 1 || vs[0]["content"] != nil {
			t.Fatalf("versions = %v, want 2 then 1, without content", vs)
		}
		first := must(t, app, "key", "GET", "/v1/research/evidence/"+v1["id"].(string), nil)
		if first["content"].(map[string]any)["mass_kg"].(float64) != 920 || first["version"].(float64) != 1 {
			t.Fatalf("version 1 read back = %v", first)
		}
		if top := rows(must(t, app, "key", "GET", "/v1/research/evidence", nil)); len(top) != 1 || top[0]["id"] != v2["id"] {
			t.Fatalf("every uri's newest = %v, want version 2 alone", top)
		}

		// A hash beside the content is checked, prefix or not.
		raw, _ := json.Marshal(a["content"])
		c, _ := canon(raw)
		if got := must(t, app, "key", "POST", "/v1/research/evidence", map[string]any{"uri": uri, "kind": "document", "hash": "sha256:" + sha(c), "content": a["content"]}); got["version"].(float64) != 3 {
			t.Fatalf("version 1's content again, its hash beside it = %v, want version 3 (B was newest)", got)
		}
		h := strings.Repeat("ab", 32)
		for name, body := range map[string]map[string]any{
			"a hash the content does not have": {"uri": uri, "kind": "document", "hash": h, "content": map[string]any{"mass_kg": 1}},
			"no content and no hash":           {"uri": uri, "kind": "document"},
			"a hash and no content":            {"uri": uri, "kind": "document", "hash": h},
			"no uri":                           {"kind": "document", "content": 1},
			"an unknown kind":                  {"uri": uri, "kind": "rumour", "content": 1},
			"an unknown rel":                   {"uri": uri, "kind": "document", "content": 1, "links": []map[string]any{{"rel": "likes", "uri": "x"}}},
		} {
			if code, m := as(t, app, "key", "POST", "/v1/research/evidence", body); code != http.StatusUnprocessableEntity {
				t.Errorf("%s: %d %v, want 422", name, code, m)
			}
		}
		if code, _ := as(t, app, "key", "GET", "/v1/research/evidence/ev_0000", nil); code != http.StatusNotFound {
			t.Errorf("an unknown id: %d, want 404", code)
		}

	})
}

// A SysML v2 API export files one element record per element, and its satisfy
// and verify relationships as links on the source element. Names are the LTV
// powertrain trade's (hanzo-inc/kai army/jltv/ltv.sysml).
func TestSysMLImport(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		elements := []map[string]any{
			{"@id": "e-payload", "@type": "RequirementUsage", "declaredName": "payload", "declaredShortName": "R1"},
			{"@id": "e-diesel", "@type": "PartUsage", "declaredName": "diesel"},
			{"@id": "e-weighing", "@type": "VerificationCaseUsage", "declaredName": "weighing"},
			{"@id": "r-satisfy", "@type": "SatisfyRequirementUsage", "source": []map[string]any{{"@id": "e-diesel"}}, "target": []map[string]any{{"@id": "e-payload"}}},
			{"@id": "r-verify", "@type": "RequirementVerificationMembership", "relatedElement": []map[string]any{{"@id": "e-weighing"}, {"@id": "e-payload"}}},
			{"@id": "r-typing", "@type": "FeatureTyping", "source": []map[string]any{{"@id": "e-diesel"}}, "target": []map[string]any{{"@id": "e-payload"}}},
		}
		m := must(t, app, "key", "POST", "/v1/research/evidence/sysml", map[string]any{"project": "ltv", "commit": "c0ffee", "elements": elements})
		got := map[string]map[string]any{}
		for _, r := range rows(m) {
			got[r["uri"].(string)] = r
		}
		uri := func(id string) string { return "sysml://ltv@c0ffee#" + id }
		if len(got) != 3 {
			t.Fatalf("filed %d records, want the 3 elements that are not relationships: %v", len(got), m)
		}
		links := func(id string) []map[string]any { return list(got[uri(id)], "links") }
		if l := links("e-diesel"); len(l) != 1 || l[0]["rel"] != "satisfies" || l[0]["uri"] != uri("e-payload") {
			t.Errorf("diesel links = %v, want satisfies payload", l)
		}
		if l := links("e-weighing"); len(l) != 1 || l[0]["rel"] != "verifies" || l[0]["uri"] != uri("e-payload") {
			t.Errorf("weighing links = %v, want verifies payload", l)
		}
		if l := links("e-payload"); len(l) != 0 {
			t.Errorf("payload links = %v, want none", l)
		}
		raw, _ := json.Marshal(elements[0])
		c, _ := canon(raw)
		payload := got[uri("e-payload")]
		if payload["kind"] != "element" || payload["hash"] != sha(c) || payload["source"].(map[string]any)["revision"] != "c0ffee" {
			t.Errorf("payload = %v, want an element hashed over its canonical JSON at the commit", payload)
		}
		back := must(t, app, "key", "GET", "/v1/research/evidence/"+payload["id"].(string), nil)
		if back["content"].(map[string]any)["declaredShortName"] != "R1" {
			t.Errorf("payload's content = %v, want the element", back["content"])
		}
		dup := append(slices.Clone(elements), map[string]any{"@id": "e-payload", "@type": "RequirementUsage", "declaredName": "payload2"})
		if code, _ := as(t, app, "key", "POST", "/v1/research/evidence/sysml", map[string]any{"project": "ltv", "commit": "c0ffee", "elements": dup}); code != http.StatusUnprocessableEntity {
			t.Errorf("an @id twice: %d, want 422", code)
		}
		if code, _ := as(t, app, "key", "POST", "/v1/research/evidence/sysml", map[string]any{"project": "ltv", "elements": elements}); code != http.StatusUnprocessableEntity {
			t.Errorf("no commit: %d, want 422", code)
		}

	})
}

// Authorizing an action and committing to a decision are a person's: an API
// key, a service account and an agent's as() token are refused 403, and Kai's
// draft never replaces what a person authorized.
func TestHumanOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		must(t, app, "key", "POST", "/v1/research/decisions", map[string]any{"decision": file(t, "ddt.json")})
		act := map[string]any{
			"id": "RUN_KAI_08B02A2DE43C89D7/anchor", "decisionId": "DEC-SPH-0412", "status": "DRAFT",
			"verdict": "ask", "rule": "kai.anchor", "description": "Does the charter favor one option before the options were measured?",
		}
		drafted := must(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": act, "assignee": "ada"})
		id := drafted["id"].(string)
		if drafted["status"] != "DRAFT" || drafted["decisionId"] != "DEC-SPH-0412" || !strings.HasPrefix(id, "act_") {
			t.Fatalf("filed action = %v, want a DRAFT on DEC-SPH-0412", drafted)
		}
		for _, who := range []string{"key", "app", "agent"} {
			if code, m := as(t, app, who, "POST", "/v1/research/actions/"+id+"/authorize", map[string]any{"hash": drafted["hash"]}); code != http.StatusForbidden {
				t.Errorf("authorize as %s: %d %v, want 403", who, code, m)
			}
			if code, m := as(t, app, who, "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-SPH-0412", "selection": "A2"}); code != http.StatusForbidden {
				t.Errorf("commit as %s: %d %v, want 403", who, code, m)
			}
		}
		if code, _ := as(t, app, "", "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-SPH-0412", "selection": "A2"}); code < 400 || code >= 500 {
			t.Errorf("commit as nobody: %d, want a refusal", code)
		}

		ok := must(t, app, "person", "POST", "/v1/research/actions/"+id+"/authorize", map[string]any{"hash": drafted["hash"]})
		if ok["status"] != "AUTHORIZED" || ok["authorizedBy"] != ada || ok["authorizedAt"] == nil || ok["id"] != id || ok["action"].(map[string]any)["status"] != "AUTHORIZED" {
			t.Fatalf("authorized = %v, want AUTHORIZED by %s, in the DDT Action too", ok, ada)
		}
		if code, again := as(t, app, "person", "POST", "/v1/research/actions/"+id+"/authorize", map[string]any{"hash": drafted["hash"]}); code != http.StatusConflict {
			t.Errorf("authorizing twice: %d %v, want 409", code, again)
		}
		if code, _ := as(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": act, "assignee": "kai"}); code != http.StatusConflict {
			t.Errorf("a draft over an authorized action: %d, want 409", code)
		}
		if code, _ := as(t, app, "person", "POST", "/v1/research/actions/act_nope/authorize", map[string]any{"hash": drafted["hash"]}); code != http.StatusNotFound {
			t.Errorf("authorize an unknown action: %d, want 404", code)
		}
		if as := rows(must(t, app, "key", "GET", "/v1/research/actions?status=AUTHORIZED", nil)); len(as) != 1 || as[0]["id"] != id {
			t.Errorf("authorized actions = %v", as)
		}

		c := must(t, app, "person", "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-SPH-0412", "selection": "A2", "note": "lowest burden"})
		if c["by"] != ada || c["selection"] != "A2" || !strings.HasPrefix(c["id"].(string), "com_") {
			t.Fatalf("commitment = %v", c)
		}
		if code, _ := as(t, app, "person", "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-NONE", "selection": "A2"}); code != http.StatusNotFound {
			t.Errorf("a commitment to an unfiled decision: %d, want 404", code)
		}
		if code, _ := as(t, app, "person", "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-SPH-0412", "evaluationId": "eval_nope", "selection": "A2"}); code != http.StatusNotFound {
			t.Errorf("a commitment on an unfiled evaluation: %d, want 404", code)
		}
		if cs := rows(must(t, app, "key", "GET", "/v1/research/commitments?decision=DEC-SPH-0412", nil)); len(cs) != 1 {
			t.Errorf("commitments = %v, want 1", cs)
		}

	})
}

// A caller with no org reads, for the org ?org= names, the decisions,
// evaluations and evidence it granted public, and the 404 an unserved address
// gets until it has granted one. Actions and commitments are never read so.
func TestPublicReads(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		must(t, app, "key", "POST", "/v1/research/decisions", map[string]any{"decision": file(t, "ddt.json")})
		export := map[string]json.RawMessage{}
		_ = json.Unmarshal(file(t, "ddt.export.json"), &export)
		must(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": export["evaluationRun"]})
		must(t, app, "key", "POST", "/v1/research/evidence", map[string]any{"uri": "techdata://sph/tsb/22", "kind": "document", "content": map[string]any{"rev": "A"}})
		must(t, app, "key", "POST", "/v1/research/actions", map[string]any{"action": map[string]any{"id": "a1", "decisionId": "DEC-SPH-0412"}})
		must(t, app, "person", "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-SPH-0412", "selection": "A2"})

		reads := map[string]string{
			"/v1/research/decisions?org=acme":   "DEC-SPH-0412",
			"/v1/research/evaluations?org=acme": "RUN_KAI_08B02A2DE43C89D7",
			"/v1/research/evidence?org=acme":    "techdata://sph/tsb/22",
		}
		for path := range reads {
			if code, m := as(t, app, "", "GET", path, nil); code != http.StatusNotFound {
				t.Errorf("GET %s before a grant: %d %v, want 404", path, code, m)
			}
		}
		for path, id := range reads {
			if g := must(t, app, "person", "POST", "/v1/research/grants", map[string]any{"id": id, "visibility": "public"}); g["updated"].(float64) < 1 {
				t.Fatalf("grant %s = %v", id, g)
			}
			got := rows(must(t, app, "", "GET", path, nil))
			if len(got) != 1 || got[0]["visibility"] != "public" {
				t.Errorf("GET %s after its grant = %v, want the one public record", path, got)
			}
		}
		// A version filed after the grant is private until it is granted too: the
		// public read keeps the granted version and drops the new one.
		must(t, app, "key", "POST", "/v1/research/evidence", map[string]any{"uri": "techdata://sph/tsb/22", "kind": "document", "content": map[string]any{"rev": "B"}})
		if vs := rows(must(t, app, "", "GET", "/v1/research/evidence?org=acme&uri=techdata://sph/tsb/22", nil)); len(vs) != 1 || vs[0]["version"].(float64) != 1 {
			t.Errorf("public versions after an ungranted revision = %v, want version 1 alone", vs)
		}
		if vs := rows(must(t, app, "", "GET", "/v1/research/evidence?org=acme", nil)); len(vs) != 0 {
			t.Errorf("public newest after an ungranted revision = %v, want none", vs)
		}
		for _, path := range []string{"/v1/research/actions?org=acme", "/v1/research/commitments?org=acme", "/v1/research/evidence/dependents?uri=x&org=acme"} {
			if code, _ := as(t, app, "", "GET", path, nil); code < 400 || code >= 500 {
				t.Errorf("GET %s with no org: %d, want a refusal", path, code)
			}
		}
		if code, _ := as(t, app, "", "GET", "/v1/research/decisions?org=nobody", nil); code != http.StatusNotFound {
			t.Errorf("an org with no store: %d, want 404", code)
		}
		// A caller with an org reads its own: another org's grant is not its record.
		other := credentials["key"]
		other.Org = "globex"
		credentials["globex"] = other
		t.Cleanup(func() { delete(credentials, "globex") })
		if got := rows(must(t, app, "globex", "GET", "/v1/research/decisions?org=acme", nil)); len(got) != 0 {
			t.Errorf("globex reading ?org=acme = %v, want its own (none)", got)
		}

	})
}

// sysmlParts is n plain SysML elements whose content carries tag.
func sysmlParts(n int, tag string) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = map[string]any{"@id": fmt.Sprintf("e%05d", i), "@type": "PartUsage", "declaredName": tag}
	}
	return out
}

// reimport times one re-import of n changed elements at one project@commit,
// after the first import filed version 1 of each.
func reimport(t *testing.T, n int) time.Duration {
	t.Helper()
	app := mountThread(t)
	commit := fmt.Sprintf("c%d", n)
	must(t, app, "key", "POST", "/v1/research/evidence/sysml", map[string]any{"project": "p", "commit": commit, "elements": sysmlParts(n, "a")})
	start := time.Now()
	got := must(t, app, "key", "POST", "/v1/research/evidence/sysml", map[string]any{"project": "p", "commit": commit, "elements": sysmlParts(n, "b")})
	if r := rows(got); len(r) != n || r[0]["version"].(float64) != 2 {
		t.Fatalf("re-import of %d filed %d records, the first at version %v; want each at version 2", n, len(r), r[0]["version"])
	}
	return time.Since(start)
}

// A SysML re-import of N changed elements reads the org's evidence, decisions and
// evaluations once and publishes one reconsider, so its cost grows with N: four
// times the elements is about four times the time, never sixteen.
func TestSysMLReimportIsLinear(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		small, large := 500, 2000
		ts, tl := reimport(t, small), reimport(t, large)
		ratio := float64(tl) / float64(ts)
		t.Logf("re-import %d: %v, %d: %v, ratio %.1f (linear ~%d)", small, ts, large, tl, ratio, large/small)
		if ratio > 2*float64(large/small) {
			t.Fatalf("re-import cost grows faster than the batch (x%.1f for x%d elements)", ratio, large/small)
		}

	})
}

// sealed is pkg with the hash Kai seals a Decision Package with (its content:
// every member but hash and trace), and that hash in envelope form.
func sealed(t *testing.T, pkg map[string]any) (map[string]any, string) {
	t.Helper()
	raw, _ := json.Marshal(pkg)
	h, err := sealOf(raw)
	if err != nil {
		t.Fatal(err)
	}
	out := maps.Clone(pkg)
	out["hash"] = "sha256:" + h
	return out, h
}

// The package an Evaluation Run binds is hashed as Kai seals it, and the hash it
// states about itself and the run's outputs.hash must both be that: a package
// whose selection was rewritten after Kai sealed it, a package with no hash, and
// a run naming no package hash are refused.
func TestPackageHashIsItsContent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		runs := recordedRuns(t)
		pkg := map[string]json.RawMessage{}
		for k, v := range runs[0].Package {
			pkg[k] = v
		}
		var own string
		_ = json.Unmarshal(pkg["hash"], &own)
		kai := "RUN_KAI_" + strings.ToUpper(digest(own)[:16])
		run := map[string]any{"id": kai, "decisionId": "DEC-SPH-0412", "outputs": map[string]any{"hash": own}}
		pkg["selection"] = json.RawMessage(`"A1"`) // Kai selected A2
		pkg["ranking"] = json.RawMessage(`["A1","A2","A3"]`)
		if code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": pkg}); code != http.StatusUnprocessableEntity {
			t.Errorf("a package rewritten after it was sealed (selection A2 -> A1, hash kept): %d %v, want 422", code, m)
		}
		delete(pkg, "hash")
		run["id"] = "RUN_KAI_UNSEALED"
		if code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": pkg}); code != http.StatusUnprocessableEntity {
			t.Errorf("a package with no hash beside outputs.hash %s: %d %v, want 422", own, code, m)
		}
		whole, _ := json.Marshal(runs[0].Package)
		run["outputs"] = map[string]any{}
		if code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": json.RawMessage(whole)}); code != http.StatusUnprocessableEntity {
			t.Errorf("a sealed package beside a run naming no outputs.hash: %d %v, want 422", code, m)
		}
		// The trace is outside the seal: Kai's wall times vary run to run.
		traced := map[string]json.RawMessage{}
		for k, v := range runs[0].Package {
			traced[k] = v
		}
		traced["trace"] = json.RawMessage(`[{"node":"best","ms":1}]`)
		run["id"], run["outputs"] = kai, map[string]any{"hash": own}
		if got := must(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": traced}); got["recommendation"].(map[string]any)["selection"] != "A2" {
			t.Errorf("the sealed package with another trace = %v, want A2", got["recommendation"])
		}

	})
}

// A SuperAdmin acting in a tenant's org (the boundary mints Org=acme,
// Holder=admin/<name>, Kind=Person, X-User-IsAdmin, X-User-Owner admin) is
// refused the tenant's authorizations and human commitments.
func TestSuperAdminInAnotherOrgNeitherAuthorizesNorCommits(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		credentials["super"] = principal.Principal{Org: "acme", User: "u-admin", Subject: "u-admin", Stable: true, Kind: principal.Person, Holder: "admin/z", Client: "hanzo-console"}
		t.Cleanup(func() { delete(credentials, "super") })
		app := mountThread(t)
		must(t, app, "key", "POST", "/v1/research/decisions", map[string]any{"decision": file(t, "ddt.json")})
		a := must(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": map[string]any{"id": "a1", "decisionId": "DEC-SPH-0412"}})
		if code, m := as(t, app, "super", "POST", "/v1/research/actions/"+a["id"].(string)+"/authorize", map[string]any{"hash": a["hash"]}); code != http.StatusForbidden {
			t.Errorf("a SuperAdmin acting in acme authorizing acme's action: %d %v, want 403", code, m)
		}
		if code, m := as(t, app, "super", "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-SPH-0412", "selection": "A2"}); code != http.StatusForbidden {
			t.Errorf("a SuperAdmin acting in acme committing acme's decision: %d %v, want 403", code, m)
		}
		// A boundary that names no home org (absent, or one it cannot represent)
		// fails closed: Owner is "", never the org acted in.
		noOwner := func(r *http.Request) { r.Header.Del("X-User-Owner") }
		if code, m := asWith(t, app, "super", "POST", "/v1/research/actions/"+a["id"].(string)+"/authorize", map[string]any{"hash": a["hash"]}, noOwner); code != http.StatusForbidden {
			t.Errorf("a SuperAdmin in acme, no X-User-Owner, authorizing: %d %v, want 403", code, m)
		}
		if code, m := asWith(t, app, "super", "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-SPH-0412", "selection": "A2"}, noOwner); code != http.StatusForbidden {
			t.Errorf("a SuperAdmin in acme, no X-User-Owner, committing: %d %v, want 403", code, m)
		}

	})
}

// A SuperAdmin in its own home org is that org's person: the refusal is the
// switch, not the operator.
func TestSuperAdminAtHomeIsItsOrgsPerson(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		credentials["home"] = principal.Principal{Org: authz.AdminOrg, User: "u-admin", Subject: "u-admin", Stable: true, Kind: principal.Person, Holder: "admin/z"}
		credentials["adminkey"] = principal.Principal{Org: authz.AdminOrg, User: "admin/z", Subject: "admin/z", Stable: true, Kind: principal.Key, Key: "k2"}
		t.Cleanup(func() { delete(credentials, "home"); delete(credentials, "adminkey") })
		app := mountThread(t)
		a := must(t, app, "adminkey", "POST", "/v1/research/actions", map[string]any{"action": map[string]any{"id": "a1"}})
		if got := must(t, app, "home", "POST", "/v1/research/actions/"+a["id"].(string)+"/authorize", map[string]any{"hash": a["hash"]}); got["status"] != "AUTHORIZED" {
			t.Errorf("a SuperAdmin authorizing in its own org = %v, want AUTHORIZED", got)
		}

	})
}

// Authorize names the hash of the action state the person read: an agent that
// refiles the draft between the person's read and the person's click gets 409,
// not its new text authorized. Authorizing the state read authorizes exactly it.
func TestAuthorizeIsBoundToWhatWasRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		act := map[string]any{"id": "RUN_KAI_1/anchor", "decisionId": "DEC-SPH-0412", "status": "DRAFT", "description": "Review the anchoring check"}
		id := must(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": act, "assignee": "ada"})["id"].(string)
		read := rows(must(t, app, "person", "GET", "/v1/research/actions?id="+id, nil))[0]

		act["description"] = "Release the A1 contract award"
		must(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": act, "assignee": "kai"})

		if code, m := as(t, app, "person", "POST", "/v1/research/actions/"+id+"/authorize", map[string]any{"hash": read["hash"]}); code != http.StatusConflict {
			t.Fatalf("the person read %q and authorizing it after a refile answered %d %v, want 409", read["action"].(map[string]any)["description"], code, m)
		}
		if code, m := as(t, app, "person", "POST", "/v1/research/actions/"+id+"/authorize", map[string]any{}); code != http.StatusUnprocessableEntity {
			t.Errorf("authorize naming no hash: %d %v, want 422", code, m)
		}
		now := rows(must(t, app, "person", "GET", "/v1/research/actions?id="+id, nil))[0]
		got := must(t, app, "person", "POST", "/v1/research/actions/"+id+"/authorize", map[string]any{"hash": now["hash"]})
		if d := got["action"].(map[string]any)["description"]; d != "Release the A1 contract award" || got["assignee"] != "kai" || got["status"] != "AUTHORIZED" {
			t.Errorf("authorizing the state read = %v (%v), want that state authorized", got, d)
		}

	})
}

// A commitment names an evaluation filing, and records that filing's hash.
// Refiling the run with other content files another evaluation: the one
// committed on stays readable by its id, as it was made.
func TestCommitmentIsBoundToTheEvaluationItRestsOn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		must(t, app, "key", "POST", "/v1/research/decisions", map[string]any{"decision": file(t, "ddt.json")})
		pkg, h := sealed(t, map[string]any{"selection": "A2"})
		run := map[string]any{"id": "RUN_KAI_" + strings.ToUpper(h[:16]), "decisionId": "DEC-SPH-0412", "inputs": map[string]string{}, "outputs": map[string]any{"hash": h}}
		ev := must(t, app, "app", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": pkg})
		c := must(t, app, "person", "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-SPH-0412", "evaluationId": ev["id"], "selection": "A2"})
		if c["evaluationHash"] != ev["hash"] || ev["hash"] == "" {
			t.Errorf("commitment = %v, want it to record evaluation hash %v", c, ev["hash"])
		}

		run["outputs"] = map[string]any{"hash": h, "biasFindings": []map[string]any{{"biasCheckId": "ACTDEF_ANCHORING_CONTROL_REVIEW", "p": 0.9}}}
		again := must(t, app, "app", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": pkg})
		if again["id"] == ev["id"] {
			t.Fatalf("a refiling with other content took the id %v of the filing committed on", ev["id"])
		}
		was := rows(must(t, app, "person", "GET", "/v1/research/evaluations?id="+ev["id"].(string), nil))
		if len(was) != 1 || was[0]["evaluationRun"].(map[string]any)["outputs"].(map[string]any)["biasFindings"] != nil || was[0]["hash"] != c["evaluationHash"] {
			t.Errorf("the filing committed on, read by its id = %v, want the run as it was, hashed %v", was, c["evaluationHash"])
		}
		if cur := rows(must(t, app, "person", "GET", "/v1/research/evaluations?decision=DEC-SPH-0412", nil)); len(cur) != 1 || cur[0]["id"] != again["id"] {
			t.Errorf("the run's evaluations = %v, want its latest filing", cur)
		}
		if code, m := as(t, app, "person", "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-SPH-0412", "evaluationId": ev["id"], "evaluationHash": again["hash"], "selection": "A2"}); code != http.StatusConflict {
			t.Errorf("a commitment naming one filing by id and another by hash: %d %v, want 409", code, m)
		}

	})
}

// What a DDT Decision's agentPermissions keep from agents — lifecycleState, the
// agentPermissions themselves, every member mayNotChange names — only a person
// changes; anything else an application files. An Action is filed a DRAFT and
// never reads AUTHORIZED until a person authorizes it.
func TestOnlyAPersonChangesTheDDTLifecycle(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		must(t, app, "person", "POST", "/v1/research/decisions", map[string]any{"decision": file(t, "ddt.json")})
		dec := map[string]any{}
		_ = json.Unmarshal(file(t, "ddt.json"), &dec)
		edit := func(k string, v any) map[string]any {
			out := maps.Clone(dec)
			out[k] = v
			return out
		}
		for who, d := range map[string]map[string]any{
			"app": edit("lifecycleState", "DECIDED"),
			"key": edit("lifecycleState", "DECIDED"),
		} {
			if code, m := as(t, app, who, "POST", "/v1/research/decisions", map[string]any{"decision": d}); code != http.StatusForbidden {
				t.Errorf("%s moved lifecycleState IN_ANALYSIS -> DECIDED: %d %v, want 403", who, code, m)
			}
		}
		if code, m := as(t, app, "app", "POST", "/v1/research/decisions", map[string]any{"decision": edit("agentPermissions", map[string]any{"mayNotChange": []string{}})}); code != http.StatusForbidden {
			t.Errorf("an application lifting its own agentPermissions: %d %v, want 403", code, m)
		}
		if v := must(t, app, "app", "POST", "/v1/research/decisions", map[string]any{"decision": edit("constraints", []string{"silent_watch"})}); v["version"].(float64) != 2 {
			t.Errorf("an application changing what it may = %v, want version 2", v)
		}
		dec = edit("constraints", []string{"silent_watch"})
		dec["agentPermissions"] = map[string]any{"mayNotChange": []string{"lifecycleState", "options"}}
		if v := must(t, app, "person", "POST", "/v1/research/decisions", map[string]any{"decision": dec}); v["version"].(float64) != 3 {
			t.Errorf("a person changing agentPermissions = %v, want version 3", v)
		}
		if code, m := as(t, app, "app", "POST", "/v1/research/decisions", map[string]any{"decision": edit("options", []string{"A2_BATTERY_ONLY"})}); code != http.StatusForbidden {
			t.Errorf("an application changing options, which the record's mayNotChange names: %d %v, want 403", code, m)
		}
		if v := must(t, app, "person", "POST", "/v1/research/decisions", map[string]any{"decision": edit("lifecycleState", "DECIDED")}); v["version"].(float64) != 4 {
			t.Errorf("a person moving lifecycleState = %v, want version 4", v)
		}

		act := map[string]any{"id": "a9", "decisionId": "DEC-SPH-0412", "status": "AUTHORIZED", "authorizedBy": "pm@army.mil"}
		if code, m := as(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": act}); code != http.StatusUnprocessableEntity {
			t.Errorf("an application filing a DDT Action reading AUTHORIZED by pm@army.mil: %d %v, want 422", code, m)
		}
		act["status"] = "DRAFT"
		delete(act, "authorizedBy")
		if got := must(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": act}); got["status"] != "DRAFT" {
			t.Errorf("a DRAFT = %v", got)
		}

	})
}

// A grant names the kind it is for: an id that a run and an evaluation share is
// refused 409 without one, and granting the run public leaves the evaluation of
// that id private.
func TestGrantNamesTheKind(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		export := map[string]json.RawMessage{}
		_ = json.Unmarshal(file(t, "ddt.export.json"), &export)
		must(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": export["evaluationRun"]})
		must(t, app, "key", "POST", "/v1/research/runs", map[string]any{"runs": []map[string]any{{"id": "RUN_KAI_08B02A2DE43C89D7", "benchmark": "apu"}}})
		if code, m := as(t, app, "person", "POST", "/v1/research/grants", map[string]any{"id": "RUN_KAI_08B02A2DE43C89D7", "visibility": "public"}); code != http.StatusConflict {
			t.Errorf("a grant of an id a run and an evaluation share, naming no kind: %d %v, want 409", code, m)
		}
		if code, m := as(t, app, "person", "POST", "/v1/research/grants", map[string]any{"kind": "rumour", "id": "RUN_KAI_08B02A2DE43C89D7", "visibility": "public"}); code != http.StatusUnprocessableEntity {
			t.Errorf("a grant of an unknown kind: %d %v, want 422", code, m)
		}
		g := must(t, app, "person", "POST", "/v1/research/grants", map[string]any{"kind": "run", "id": "RUN_KAI_08B02A2DE43C89D7", "visibility": "public"})
		if code, m := as(t, app, "", "GET", "/v1/research/evaluations?org=acme", nil); code != http.StatusNotFound {
			t.Errorf("granting run RUN_KAI_08B02A2DE43C89D7 (updated %v) published the evaluation of that id: %d %v", g["updated"], code, m)
		}
		if got := rows(must(t, app, "", "GET", "/v1/research/runs?org=acme", nil)); len(got) != 1 || got[0]["id"] != "RUN_KAI_08B02A2DE43C89D7" {
			t.Errorf("the run granted public = %v", got)
		}

	})
}

// A uri filed A, then B, then A again files version 3: the revert is a change,
// A is the uri's newest, and what read A is current again.
func TestEvidenceRevertFilesAVersion(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		uri := "techdata://sph/tsb/22"
		a := map[string]any{"uri": uri, "kind": "document", "content": map[string]any{"rev": "A"}}
		b := map[string]any{"uri": uri, "kind": "document", "content": map[string]any{"rev": "B"}}
		va := must(t, app, "key", "POST", "/v1/research/evidence", a)
		must(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": map[string]any{"id": "R1", "decisionId": "D", "inputs": map[string]string{uri: va["hash"].(string)}}})
		must(t, app, "key", "POST", "/v1/research/evidence", b)
		back := must(t, app, "key", "POST", "/v1/research/evidence", a)
		deps := must(t, app, "key", "GET", "/v1/research/evidence/dependents?uri="+uri, nil)
		if back["version"].(float64) != 3 || back["id"] == va["id"] || deps["newest"] != va["hash"] || list(deps, "evaluations")[0]["stale"] != false {
			t.Errorf("revert to A answered version %v; newest is %v, not A %v; evaluation stale=%v", back["version"], deps["newest"], va["hash"], list(deps, "evaluations")[0]["stale"])
		}
		if again := must(t, app, "key", "POST", "/v1/research/evidence", a); again["version"].(float64) != 3 {
			t.Errorf("A again while A is newest = version %v, want 3", again["version"])
		}
		if vs := rows(must(t, app, "key", "GET", "/v1/research/evidence?uri="+uri, nil)); len(vs) != 3 {
			t.Errorf("versions = %v, want 3", vs)
		}

	})
}

// ?decision= reads a decision id exactly as the write does — the member "id",
// trimmed, its case kept — so a record filed as DEC-1 never lists as DEC-2's, and
// one filed as " DEC-3 " lists under the DEC-3 a commitment names.
func TestDecisionListingReadsTheFiledID(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		must(t, app, "key", "POST", "/v1/research/decisions", map[string]any{"decision": json.RawMessage(`{"id":"DEC-1","ID":"DEC-2","schemaVersion":"DDT-1.0"}`)})
		if got := rows(must(t, app, "key", "GET", "/v1/research/decisions?decision=DEC-2", nil)); len(got) != 0 {
			t.Errorf("?decision=DEC-2 lists a record filed as DEC-1: %v", got[0]["decision"])
		}
		if got := rows(must(t, app, "key", "GET", "/v1/research/decisions?decision=DEC-1", nil)); len(got) != 1 || got[0]["decisionId"] != "DEC-1" {
			t.Errorf("?decision=DEC-1 = %v, want the record filed as DEC-1", got)
		}
		must(t, app, "key", "POST", "/v1/research/decisions", map[string]any{"decision": map[string]any{"id": " DEC-3 ", "schemaVersion": "DDT-1.0"}})
		must(t, app, "person", "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-3", "selection": "A"})
		if got := rows(must(t, app, "key", "GET", "/v1/research/decisions?decision=DEC-3", nil)); len(got) != 1 {
			t.Errorf("DEC-3 takes a commitment but ?decision=DEC-3 lists %d records", len(got))
		}
		if got := rows(must(t, app, "key", "GET", "/v1/research/commitments?decision=%20DEC-3%20", nil)); len(got) != 1 {
			t.Errorf("?decision=%%20DEC-3%%20 lists %d commitments, want the one", len(got))
		}

	})
}

// A SysML element's uri names one element of one import: the project may not
// hold the @ and # that delimit it, and the commit is lowercase hex, so project
// "a@b" commit "c" and project "a" commit "b@c" can never both be sysml://a@b@c.
func TestSysMLURIsDoNotCollide(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		for _, in := range []map[string]any{
			{"project": "a@b", "commit": "c"},
			{"project": "a#b", "commit": "c"},
			{"project": "a", "commit": "b@c"},
			{"project": "a", "commit": "C0FFEE"},
			{"project": "a", "commit": "sha256:c0ffee"},
		} {
			in["elements"] = sysmlParts(1, "x")
			if code, m := as(t, app, "key", "POST", "/v1/research/evidence/sysml", in); code != http.StatusUnprocessableEntity {
				t.Errorf("project %v commit %v: %d %v, want 422", in["project"], in["commit"], code, m)
			}
		}

	})
}

// Evidence carries its content, so no filer claims a (uri, hash) for content it
// never sent: a hash alone is refused, and the content filed later is version 1,
// filed by whoever sent it.
func TestEvidenceCarriesItsContent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		content := map[string]any{"rev": "A"}
		raw, _ := json.Marshal(content)
		c, _ := canon(raw)
		if code, m := as(t, app, "app", "POST", "/v1/research/evidence", map[string]any{"uri": "u", "kind": "requirement", "hash": sha(c), "links": []map[string]any{{"rel": "satisfies", "uri": "x"}}}); code != http.StatusUnprocessableEntity {
			t.Errorf("a hash with no content: %d %v, want 422", code, m)
		}
		got := must(t, app, "person", "POST", "/v1/research/evidence", map[string]any{"uri": "u", "kind": "document", "content": content})
		if got["content"] == nil || got["kind"] != "document" || got["by"] != ada || got["version"].(float64) != 1 {
			t.Errorf("filing the content answered %v kind=%v by=%v content=%v", got["id"], got["kind"], got["by"], got["content"])
		}

	})
}

// Two people authorizing one draft at once: the read and the write are one
// transaction, so one is answered AUTHORIZED, the other 409, and the record names
// the one answered.
func TestConcurrentAuthorizeHasOneWinner(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bob := credentials["person"]
		bob.User, bob.Subject, bob.Holder = "4f1c0e8a-0000-4000-8000-0000000000b0", "4f1c0e8a-0000-4000-8000-0000000000b0", "acme/bob"
		credentials["bob"] = bob
		t.Cleanup(func() { delete(credentials, "bob") })
		app := mountThread(t)
		for i := range 20 {
			filed := must(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": map[string]any{"id": fmt.Sprintf("a%d", i)}})
			id := filed["id"].(string)
			var wg sync.WaitGroup
			var mu sync.Mutex
			codes, said := map[string]int{}, map[string]any{}
			for _, who := range []string{"person", "bob"} {
				wg.Go(func() {
					code, m := as(t, app, who, "POST", "/v1/research/actions/"+id+"/authorize", map[string]any{"hash": filed["hash"]})
					mu.Lock()
					codes[who], said[who] = code, m["authorizedBy"]
					mu.Unlock()
				})
			}
			wg.Wait()
			final := rows(must(t, app, "key", "GET", "/v1/research/actions?id="+id, nil))[0]["authorizedBy"]
			var won []string
			for who, code := range codes {
				switch code {
				case http.StatusOK:
					won = append(won, who)
				case http.StatusConflict:
				default:
					t.Fatalf("round %d: %s answered %d", i, who, code)
				}
			}
			if len(won) != 1 || said[won[0]] != final {
				t.Fatalf("round %d: answered %v (authorizedBy %v); the record says %v", i, codes, said, final)
			}
		}

	})
}

// A decision filed X, then Y, then X again files version 3, decided in one
// transaction against the newest as evidence versions are: a person re-locking
// options by refiling the version that locked them locks them, and an
// application editing the newest version it reads is refused.
func TestDecisionRevertFilesAVersion(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		dec := map[string]any{}
		_ = json.Unmarshal(file(t, "ddt.json"), &dec)
		locked, loose := maps.Clone(dec), maps.Clone(dec)
		locked["agentPermissions"] = map[string]any{"mayNotChange": []string{"lifecycleState", "options"}}
		loose["agentPermissions"] = map[string]any{"mayNotChange": []string{"lifecycleState"}}
		first := must(t, app, "person", "POST", "/v1/research/decisions", map[string]any{"decision": locked})
		must(t, app, "person", "POST", "/v1/research/decisions", map[string]any{"decision": loose})
		back := must(t, app, "person", "POST", "/v1/research/decisions", map[string]any{"decision": locked})
		if back["version"].(float64) != 3 || back["id"] == first["id"] {
			t.Fatalf("X, Y, X filed %v version %v, want version 3, not version 1's id %v", back["id"], back["version"], first["id"])
		}
		if again := must(t, app, "person", "POST", "/v1/research/decisions", map[string]any{"decision": locked}); again["id"] != back["id"] || again["version"].(float64) != 3 {
			t.Errorf("X again while X is newest = %v version %v, want version 3", again["id"], again["version"])
		}

		newest := rows(must(t, app, "app", "GET", "/v1/research/decisions?decision=DEC-SPH-0412", nil))
		if len(newest) != 3 || newest[0]["id"] != back["id"] {
			t.Fatalf("versions = %v, want 3, version 3 newest", newest)
		}
		edit := map[string]any{}
		_ = json.Unmarshal(mustJSON(t, newest[0]["decision"]), &edit)
		edit["options"] = []string{"A1_INTEGRATED_APU"}
		if code, m := as(t, app, "app", "POST", "/v1/research/decisions", map[string]any{"decision": edit}); code != http.StatusForbidden {
			t.Errorf("an application changing options the newest version locks: %d version %v, want 403", code, m["version"])
		}

	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A run holds its id in every project, as the thread kinds do: a grant naming no
// kind, made in the default scope, of an id a run filed in project bench and an
// evaluation share is 409, and publishes nothing.
func TestAKindlessGrantSeesARunInAnyProject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		export := map[string]json.RawMessage{}
		_ = json.Unmarshal(file(t, "ddt.export.json"), &export)
		must(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": export["evaluationRun"]})
		bench := func(r *http.Request) { r.Header.Set("X-Project-Id", "bench") }
		if code, m := asWith(t, app, "key", "POST", "/v1/research/runs", map[string]any{"runs": []map[string]any{{"id": "RUN_KAI_08B02A2DE43C89D7", "benchmark": "apu"}}}, bench); code != http.StatusOK {
			t.Fatalf("run ingest in project bench: %d %v", code, m)
		}
		if code, m := as(t, app, "person", "POST", "/v1/research/grants", map[string]any{"id": "RUN_KAI_08B02A2DE43C89D7", "visibility": "public"}); code != http.StatusConflict {
			t.Errorf("a kind-less grant of an id a run (project bench) and an evaluation share: %d %v, want 409", code, m)
		}
		if code, m := as(t, app, "", "GET", "/v1/research/evaluations?org=acme", nil); code != http.StatusNotFound {
			t.Errorf("the public reads the evaluation: %d %v", code, m["total"])
		}

	})
}

// One evidence change reads what rests on the uri from each filing's depends,
// never a package: packages sit in the hash-addressed blob store, so a write of a
// few bytes allocates nothing like the packages the org holds.
func TestAnEvidenceChangeDecodesNoPackage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		uri := "techdata://sph/tsb/22"
		v1 := must(t, app, "key", "POST", "/v1/research/evidence", map[string]any{"uri": uri, "kind": "document", "content": map[string]any{"rev": "A"}})
		const n = 32
		pad := strings.Repeat("x", 1<<20)
		read := []map[string]any{{"uri": uri, "hash": v1["hash"]}}
		for i := range n {
			pkg, h := sealed(t, map[string]any{"selection": "A2", "evidence": read, "pad": fmt.Sprint(i) + pad})
			run := map[string]any{"id": fmt.Sprintf("R%d", i), "decisionId": "D", "inputs": map[string]any{uri: v1["hash"]}, "outputs": map[string]any{"hash": h}}
			must(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": pkg})
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		must(t, app, "key", "POST", "/v1/research/evidence", map[string]any{"uri": uri, "kind": "document", "content": map[string]any{"rev": "B"}})
		runtime.ReadMemStats(&after)
		alloc, held := after.TotalAlloc-before.TotalAlloc, uint64(n<<20)
		t.Logf("one evidence change allocated %d KiB; the org holds %d MiB of packages", alloc>>10, held>>20)
		if alloc > held/4 {
			t.Errorf("an evidence change allocated %d MiB to find %d dependents: it decodes the packages the org holds (%d MiB)", alloc>>20, n, held>>20)
		}
		if deps := list(must(t, app, "key", "GET", "/v1/research/evidence/dependents?uri="+uri, nil), "evaluations"); len(deps) != n || deps[0]["stale"] != true {
			t.Errorf("dependents = %d evaluations, want %d, each stale", len(deps), n)
		}
		got := rows(must(t, app, "key", "GET", "/v1/research/evaluations?decision=D", nil))
		if len(got) != n || len(got[0]["package"].(map[string]any)["pad"].(string)) <= 1<<20 {
			t.Errorf("the listing answers %d evaluations, want %d each with its whole package", len(got), n)
		}

	})
}

// fanout times one SysML import of one element and one Satisfy relationship
// naming n targets.
func fanout(t *testing.T, n int) time.Duration {
	t.Helper()
	app := mountThread(t)
	start := time.Now()
	body := satisfying(1, n)
	// One store holds both of a test's mounts on hanzo-sql, where a file per mount did
	// not: the import is idempotent by its commit, so each size is a commit of its own.
	body["commit"] = fmt.Sprintf("c%d", n)
	got := must(t, app, "key", "POST", "/v1/research/evidence/sysml", body)
	if l := rows(got)[0]["links"].([]any); len(l) != n {
		t.Fatalf("links = %d, want %d", len(l), n)
	}
	return time.Since(start)
}

// satisfying is a SysML import of one PartUsage s and one Satisfy relationship
// from s and sources-1 more ids to targets ids.
func satisfying(sources, targets int) map[string]any {
	ids := func(tag string, n int) []map[string]any {
		out := make([]map[string]any, n)
		for i := range out {
			out[i] = map[string]any{"@id": fmt.Sprintf("%s%07d", tag, i)}
		}
		return out
	}
	from := append([]map[string]any{{"@id": "s"}}, ids("f", sources-1)...)
	return map[string]any{"project": "p", "commit": "c0", "elements": []map[string]any{
		{"@id": "s", "@type": "PartUsage"},
		{"@id": "r", "@type": "SatisfyRequirementUsage", "source": from, "target": ids("t", targets)},
	}}
}

// A relationship's links are deduplicated by a set, so one relationship naming n
// targets costs about n, and every link it names counts against the 20000 bound
// with the elements: one target past it, or sources times targets past it, is
// 413 before a link is made.
func TestSysMLLinksAreLinearAndBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		small, large := 2000, 16000
		ts, tl := fanout(t, small), fanout(t, large)
		ratio := float64(tl) / float64(ts)
		t.Logf("fan-out %d: %v, %d: %v, ratio %.1f (linear ~%d)", small, ts, large, tl, ratio, large/small)
		if ratio > 2*float64(large/small) {
			t.Errorf("link fan-out grows faster than its targets (x%.1f for x%d)", ratio, large/small)
		}
		app := mountThread(t)
		for _, c := range []struct{ sources, targets int }{{1, maxBatchItems - 1}, {150, 150}} {
			if code, m := as(t, app, "key", "POST", "/v1/research/evidence/sysml", satisfying(c.sources, c.targets)); code != http.StatusRequestEntityTooLarge {
				t.Errorf("a relationship from %d sources to %d targets: %d %v, want 413", c.sources, c.targets, code, m)
			}
		}

	})
}

// outputs.readiness is DDT's readiness result or nothing: any other shape is 422,
// so no verdict reaches the record in a form the check against the package
// skips. Kai's null verdict (no gate rules) is the package's no verdict.
func TestReadinessIsDDTsShape(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		pkg, h := sealed(t, map[string]any{"selection": "A2", "verdict": "deny"})
		for i, readiness := range []any{
			"allow",
			map[string]any{"decisionClassPolicyId": "DCP_CONCEPT_STRATEGY_HIGH_STAKES", "verdict": []string{"allow"}},
			map[string]any{"decisionClassPolicyId": "DCP_CONCEPT_STRATEGY_HIGH_STAKES", "verdict": nil},
			map[string]any{"verdict": "deny"},
			map[string]any{"decisionClassPolicyId": nil, "verdict": "deny"},
		} {
			run := map[string]any{"id": fmt.Sprintf("R%d", i), "decisionId": "D", "outputs": map[string]any{"hash": h, "readiness": readiness}}
			if code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": pkg}); code != http.StatusUnprocessableEntity {
				t.Errorf("outputs.readiness %v beside a package whose verdict is deny: %d, run filed %v", readiness, code, m["evaluationRun"])
			}
		}
		none, h := sealed(t, map[string]any{"selection": "A2"})
		run := map[string]any{"id": "R", "decisionId": "D", "outputs": map[string]any{"hash": h, "readiness": map[string]any{"decisionClassPolicyId": "DCP_CONCEPT_STRATEGY_HIGH_STAKES", "verdict": nil}}}
		if got := must(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": none}); got["recommendation"].(map[string]any)["readiness"].(map[string]any)["verdict"] != nil {
			t.Errorf("Kai's null verdict beside a package with none = %v", got["recommendation"])
		}

	})
}

// evidenceAt is the piece of evidence a recorded package read at uri.
func evidenceAt(t *testing.T, r recordedRun, uri string) seen {
	t.Helper()
	for _, e := range seenIn(t, r.Package) {
		if e.URI == uri {
			return e
		}
	}
	t.Fatalf("%s read no %s", r.Name, uri)
	return seen{}
}

// An evaluation rests on what its sealed package read: a run whose inputs say
// another version than the package's evidence is 422, and a run naming no inputs
// rests on the package's evidence — stale once a newer version is filed.
func TestAnEvaluationRestsOnWhatItsPackageRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		runs := recordedRuns(t)
		uri := "techdata://sph/tsb/22"
		a, b := evidenceAt(t, runs[0], uri), evidenceAt(t, runs[2], uri)
		must(t, app, "key", "POST", "/v1/research/evidence", map[string]any{"uri": uri, "kind": "document", "content": a.Content})
		must(t, app, "key", "POST", "/v1/research/evidence", map[string]any{"uri": uri, "kind": "document", "content": b.Content})
		var own string
		_ = json.Unmarshal(runs[0].Package["hash"], &own)
		run := map[string]any{"id": "RUN_KAI_" + strings.ToUpper(digest(own)[:16]), "decisionId": "DEC-SPH-0412", "outputs": map[string]any{"hash": own}}
		run["inputs"] = map[string]string{uri: b.Hash}
		if code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": runs[0].Package}); code != http.StatusUnprocessableEntity {
			t.Errorf("a run saying it read %s beside a package sealed over %s: %d %v, want 422", digest(b.Hash)[:12], digest(a.Hash)[:12], code, m)
		}
		delete(run, "inputs")
		filed := must(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": runs[0].Package})
		if d, ok := rests(depends(t, filed), uri, digest(a.Hash)); !ok || len(filed["depends"].([]any)) != len(seenIn(t, runs[0].Package)) {
			t.Errorf("depends = %v (%v), want the package's evidence", filed["depends"], d)
		}
		ev := list(must(t, app, "key", "GET", "/v1/research/evidence/dependents?uri="+uri, nil), "evaluations")
		if len(ev) != 1 || ev[0]["stale"] != true {
			t.Errorf("dependents = %v, want the evaluation stale at revision A", ev)
		}

	})
}

// depends reads a filing's depends.
func depends(t *testing.T, m map[string]any) []Dependency {
	t.Helper()
	var ds []Dependency
	if err := json.Unmarshal(mustJSON(t, m["depends"]), &ds); err != nil {
		t.Fatal(err)
	}
	return ds
}

// A run is bound to one package: Kai's run id names its package's hash, so a
// package sealed by anyone else is refused under it, and any run id refuses a
// second package hash (409) — a new package is a new run.
func TestARunIsBoundToOnePackage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		runs := recordedRuns(t)
		var own string
		_ = json.Unmarshal(runs[0].Package["hash"], &own)
		kai := "RUN_KAI_" + strings.ToUpper(digest(own)[:16])
		run := map[string]any{"id": kai, "decisionId": "DEC-SPH-0412", "outputs": map[string]any{"hash": own}}
		must(t, app, "app", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": runs[0].Package})

		forged := map[string]any{}
		for k, v := range runs[0].Package {
			forged[k] = v
		}
		delete(forged, "hash")
		forged["selection"], forged["ranking"] = "A1", []string{"A1", "A2", "A3"}
		forged, h := sealed(t, forged)
		run["outputs"] = map[string]any{"hash": h}
		if code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": forged}); code != http.StatusUnprocessableEntity {
			t.Errorf("a package sealed by an API key under Kai's run %s: %d %v, want 422", kai, code, m)
		}
		if got := rows(must(t, app, "person", "GET", "/v1/research/evaluations?decision=DEC-SPH-0412", nil)); len(got) != 1 || got[0]["recommendation"].(map[string]any)["selection"] != "A2" {
			t.Errorf("Kai's run lists %v, want its own A2", got)
		}
		if code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": map[string]any{"id": "RUN_KAI_NOTAHASH", "decisionId": "D"}}); code != http.StatusUnprocessableEntity {
			t.Errorf("a RUN_KAI_ id naming no package hash: %d %v, want 422", code, m)
		}

		pkg, h := sealed(t, map[string]any{"selection": "A2"})
		other := map[string]any{"id": "R", "decisionId": "D", "outputs": map[string]any{"hash": h}}
		must(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": other, "package": pkg})
		pkg, h = sealed(t, map[string]any{"selection": "A1"})
		other["outputs"] = map[string]any{"hash": h}
		if code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": other, "package": pkg}); code != http.StatusConflict {
			t.Errorf("run R with a second package: %d %v, want 409", code, m)
		}
		delete(other, "outputs")
		if code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": other}); code != http.StatusConflict {
			t.Errorf("run R naming no package after one: %d %v, want 409", code, m)
		}

	})
}

// Who authorized an action, and when, is the server's: a draft carrying its own
// authorizedBy or authorizedAt, in any spelling of a value, is refused 422.
func TestADraftCarriesNoAuthorization(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		for _, extra := range []map[string]any{
			{"authorizedBy": "pm@army.mil", "authorizedAt": "2026-10-01T00:00:00Z"},
			{"authorizedBy": "pm@army.mil"},
			{"authorizedAt": "2026-10-01T00:00:00Z"},
			{"authorizedBy": nil},
		} {
			act := map[string]any{"id": "a9", "decisionId": "DEC-SPH-0412", "status": "DRAFT"}
			maps.Copy(act, extra)
			if code, m := as(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": act}); code != http.StatusUnprocessableEntity {
				t.Errorf("a draft carrying %v: %d %v, want 422", extra, code, m)
			}
		}

	})
}

// Publishing the decision thread is a person's act: an API key, an application
// and an agent acting for a member are refused 403 granting a decision, an
// evaluation or evidence a visibility, named or found by id, and the public reads
// nothing. A run's grant is as it was.
func TestOnlyAPersonPublishesTheThread(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		runs := recordedRuns(t)
		var own string
		_ = json.Unmarshal(runs[0].Package["hash"], &own)
		id := "RUN_KAI_" + strings.ToUpper(digest(own)[:16])
		must(t, app, "app", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": map[string]any{"id": id, "decisionId": "DEC-SPH-0412", "outputs": map[string]any{"hash": own}}, "package": runs[0].Package})
		must(t, app, "app", "POST", "/v1/research/decisions", map[string]any{"decision": file(t, "ddt.json")})
		must(t, app, "app", "POST", "/v1/research/evidence", map[string]any{"uri": "techdata://sph/tsb/22", "kind": "document", "content": map[string]any{"rev": "A"}})
		for _, who := range []string{"key", "app", "agent"} {
			for _, g := range []map[string]any{
				{"kind": "evaluation", "id": id},
				{"id": id},
				{"kind": "decision", "id": "DEC-SPH-0412"},
				{"kind": "evidence", "id": "techdata://sph/tsb/22"},
			} {
				g["visibility"] = "public"
				if code, m := as(t, app, who, "POST", "/v1/research/grants", g); code != http.StatusForbidden {
					t.Errorf("%s granting %v: %d %v, want 403", who, g, code, m)
				}
			}
		}
		for _, path := range []string{"/v1/research/evaluations?org=acme", "/v1/research/decisions?org=acme", "/v1/research/evidence?org=acme"} {
			if code, _ := as(t, app, "", "GET", path, nil); code != http.StatusNotFound {
				t.Errorf("GET %s after refused grants: %d, want 404", path, code)
			}
		}
		must(t, app, "key", "POST", "/v1/research/runs", map[string]any{"runs": []map[string]any{{"id": "run-1", "benchmark": "apu"}}})
		if g := must(t, app, "key", "POST", "/v1/research/grants", map[string]any{"kind": "run", "id": "run-1", "visibility": "public"}); g["updated"].(float64) < 1 {
			t.Errorf("an API key granting its run = %v", g)
		}
		if g := must(t, app, "person", "POST", "/v1/research/grants", map[string]any{"kind": "evaluation", "id": id, "visibility": "public"}); g["updated"].(float64) != 1 {
			t.Errorf("a person granting the evaluation = %v", g)
		}

	})
}

// The authorize hash binds one state of one action: another action's hash, a
// state since replaced (also by a refile of the content first read), and an
// authorized state's hash are 409; the current hash in any spelling is the state;
// and a draft under a padded id is the authorized action's, refused 409.
func TestAuthorizeHashNamesOneState(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		a := must(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": map[string]any{"id": "a", "description": "one"}})
		b := must(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": map[string]any{"id": "b", "description": "one"}})
		if code, _ := as(t, app, "person", "POST", "/v1/research/actions/"+a["id"].(string)+"/authorize", map[string]any{"hash": b["hash"]}); code != http.StatusConflict {
			t.Errorf("authorizing a with b's hash: %d, want 409", code)
		}
		must(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": map[string]any{"id": "a", "description": "two"}})
		must(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": map[string]any{"id": "a", "description": "one"}})
		if code, _ := as(t, app, "person", "POST", "/v1/research/actions/"+a["id"].(string)+"/authorize", map[string]any{"hash": a["hash"]}); code != http.StatusConflict {
			t.Errorf("authorizing a's first state after one, two, one: %d, want 409 (two is latest)", code)
		}
		cur := rows(must(t, app, "person", "GET", "/v1/research/actions?id="+a["id"].(string), nil))[0]
		got := must(t, app, "person", "POST", "/v1/research/actions/"+a["id"].(string)+"/authorize", map[string]any{"hash": "SHA256:" + strings.ToUpper(cur["hash"].(string))})
		if code, _ := as(t, app, "person", "POST", "/v1/research/actions/"+a["id"].(string)+"/authorize", map[string]any{"hash": got["hash"]}); code != http.StatusConflict {
			t.Errorf("authorizing an AUTHORIZED state again: %d, want 409", code)
		}
		if code, _ := as(t, app, "app", "POST", "/v1/research/actions", map[string]any{"action": map[string]any{"id": " a ", "description": "three"}}); code != http.StatusConflict {
			t.Errorf("a draft of the authorized action under a padded id: %d, want 409", code)
		}

	})
}

// What only a person changes stays a person's in other spellings: a duplicate
// lifecycleState member (the last wins for every JSON reader), a null, and
// dropping agentPermissions.
func TestLifecycleIsAPersonsInEverySpelling(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		must(t, app, "person", "POST", "/v1/research/decisions", map[string]any{"decision": file(t, "ddt.json")})
		raw := string(file(t, "ddt.json"))
		dup := strings.Replace(raw, `"lifecycleState"`, `"lifecycleState": "DECIDED", "lifecycleState"`, 1)
		dup = strings.Replace(dup, `"IN_ANALYSIS"`, `"IN_ANALYSIS", "lifecycleState": "DECIDED"`, 1)
		null, gone := map[string]any{}, map[string]any{}
		_ = json.Unmarshal(file(t, "ddt.json"), &null)
		_ = json.Unmarshal(file(t, "ddt.json"), &gone)
		null["lifecycleState"] = nil
		delete(gone, "agentPermissions")
		for name, d := range map[string]any{"duplicate": json.RawMessage(dup), "null": null, "dropped": gone} {
			if code, m := as(t, app, "app", "POST", "/v1/research/decisions", map[string]any{"decision": d}); code != http.StatusForbidden {
				t.Errorf("%s: %d %v, want 403", name, code, m["version"])
			}
		}

	})
}

// A commitment rests only on a filing of its own decision.
func TestACommitmentRestsOnItsOwnDecisionsFiling(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		must(t, app, "key", "POST", "/v1/research/decisions", map[string]any{"decision": file(t, "ddt.json")})
		must(t, app, "key", "POST", "/v1/research/decisions", map[string]any{"decision": map[string]any{"id": "DEC-OTHER", "schemaVersion": "DDT-1.0"}})
		pkg, h := sealed(t, map[string]any{"selection": "A1"})
		other := must(t, app, "app", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": map[string]any{"id": "R", "decisionId": "DEC-OTHER", "outputs": map[string]any{"hash": h}}, "package": pkg})
		if code, _ := as(t, app, "person", "POST", "/v1/research/commitments", map[string]any{"decisionId": "DEC-SPH-0412", "evaluationId": other["id"], "selection": "A1"}); code != http.StatusNotFound {
			t.Errorf("a commitment on DEC-SPH-0412 resting on DEC-OTHER's filing: %d, want 404", code)
		}

	})
}

// A replaced filing granted public stays readable by ?id=; the filing that
// replaced it is private until granted, and the public listing reads only latest
// filings that are public.
func TestAReplacedPublicFilingStaysPublic(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		pkg, h := sealed(t, map[string]any{"selection": "A2"})
		run := map[string]any{"id": "R", "decisionId": "D", "outputs": map[string]any{"hash": h}}
		f1 := must(t, app, "app", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": pkg})
		must(t, app, "person", "POST", "/v1/research/grants", map[string]any{"kind": "evaluation", "id": "R", "visibility": "public"})
		run["evaluator"] = map[string]any{"id": "kai-program"}
		f2 := must(t, app, "app", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": pkg})
		if code, _ := as(t, app, "", "GET", "/v1/research/evaluations?org=acme&id="+f2["id"].(string), nil); code != http.StatusNotFound {
			t.Errorf("the private filing that replaced a public one, by ?id=: %d, want 404", code)
		}
		if got := rows(must(t, app, "", "GET", "/v1/research/evaluations?org=acme&id="+f1["id"].(string), nil)); len(got) != 1 {
			t.Errorf("the granted filing by ?id= = %v", got)
		}
		if code, _ := as(t, app, "", "GET", "/v1/research/evaluations?org=acme", nil); code != http.StatusNotFound {
			t.Errorf("the public listing with a private latest filing: %d, want 404", code)
		}

	})
}

// A run belongs to the decision it was first filed under: re-filing Kai's sealed
// run under another decision id is 409, and the decision keeps its evaluation.
func TestAKaiRunStaysWithItsDecision(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		runs := recordedRuns(t)
		var own string
		_ = json.Unmarshal(runs[0].Package["hash"], &own)
		kai := "RUN_KAI_" + strings.ToUpper(digest(own)[:16])
		run := map[string]any{"id": kai, "decisionId": "DEC-SPH-0412", "outputs": map[string]any{"hash": own}}
		must(t, app, "app", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": runs[0].Package})

		moved := map[string]any{"id": kai, "decisionId": "DEC-ELSEWHERE", "outputs": map[string]any{"hash": own}}
		code, _ := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": moved, "package": runs[0].Package})
		got := rows(must(t, app, "person", "GET", "/v1/research/evaluations?decision=DEC-SPH-0412", nil))
		if code != http.StatusConflict || len(got) != 1 {
			t.Errorf("re-filing Kai's run %s under DEC-ELSEWHERE: %d, DEC-SPH-0412 lists %d evaluations; want 409 and 1", kai, code, len(got))
		}

	})
}

// A RUN_KAI_ id filed with no package and a made-up hash does not hold the id:
// Kai's filing with its verified package binds the run.
func TestAKaiRunIDIsHeldByItsVerifiedPackage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		runs := recordedRuns(t)
		var own string
		_ = json.Unmarshal(runs[0].Package["hash"], &own)
		kai := "RUN_KAI_" + strings.ToUpper(digest(own)[:16])
		squat := digest(own)[:16] + strings.Repeat("0", 48)
		code, _ := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": map[string]any{"id": kai, "decisionId": "DEC-SPH-0412", "outputs": map[string]any{"hash": "sha256:" + squat}}})
		run := map[string]any{"id": kai, "decisionId": "DEC-SPH-0412", "outputs": map[string]any{"hash": own}}
		if c, m := as(t, app, "app", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": runs[0].Package}); c != http.StatusOK {
			t.Errorf("squat filed %d; Kai's own sealed run %s then: %d %v, want 200 (refuse a RUN_KAI_ id without its package)", code, kai, c, m)
		}

	})
}

// depends reads package.evidence by exact member names, as the seal and Kai do: a
// "HASH" beside "hash" does not move the index to a version the package does not
// name, and dependents call the evaluation stale.
func TestDependsReadsThePackagesExactMemberNames(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		uri := "techdata://sph/tsb/22"
		old := must(t, app, "key", "POST", "/v1/research/evidence", map[string]any{"uri": uri, "kind": "document", "content": map[string]any{"rev": "A"}})
		cur := must(t, app, "key", "POST", "/v1/research/evidence", map[string]any{"uri": uri, "kind": "document", "content": map[string]any{"rev": "B"}})
		a, b := old["hash"].(string), cur["hash"].(string)
		// Raw, so "HASH" follows "hash" as a client may write it (a map would sort it first).
		seen := json.RawMessage(`[{"id":"tsb","uri":"` + uri + `","hash":"sha256:` + a + `","HASH":"sha256:` + b + `"}]`)
		pkg, h := sealed(t, map[string]any{"selection": "A2", "evidence": seen})
		run := map[string]any{"id": "R", "decisionId": "D", "outputs": map[string]any{"hash": h}}
		code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": pkg})
		if code != http.StatusOK {
			t.Fatalf("filing: %d %v", code, m)
		}
		if d, ok := rests(depends(t, m), uri, a); !ok {
			t.Errorf("package evidence hash %s (exact member) filed as depends %v: the server's index is not what the package names", a[:12], d)
		}
		ev := list(must(t, app, "key", "GET", "/v1/research/evidence/dependents?uri="+uri, nil), "evaluations")
		if len(ev) != 1 || ev[0]["stale"] != true {
			t.Errorf("dependents = %v, want the evaluation stale: its package read revision A", ev)
		}

	})
}

// A package's verdict is a string or null, as Kai's Option<Verdict> is; any other
// shape is 422 rather than read as no verdict.
func TestAPackageVerdictIsAVerdictOrNull(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		for i, v := range []any{[]string{"deny"}, map[string]any{"deny": true}, 2} {
			pkg, h := sealed(t, map[string]any{"selection": "A2", "verdict": v})
			run := map[string]any{"id": fmt.Sprintf("V%d", i), "decisionId": "D", "outputs": map[string]any{"hash": h, "readiness": map[string]any{"decisionClassPolicyId": "DCP_CONCEPT_STRATEGY_HIGH_STAKES", "verdict": nil}}}
			if code, m := as(t, app, "key", "POST", "/v1/research/evaluations", map[string]any{"evaluationRun": run, "package": pkg}); code != http.StatusUnprocessableEntity {
				t.Errorf("package verdict %v beside readiness verdict null: %d, recommendation %v, want 422", v, code, m["recommendation"])
			}
		}

	})
}

// A NUL in any document a person files is refused with a reason on both engines:
// SQLite's TEXT keeps it and PostgreSQL's jsonb does not, so without the check the
// same request is recorded on one and a 500 on the other. The refusal comes before
// the document is read, so a document that is otherwise unfit is refused for the
// NUL all the same.
func TestADocumentHoldingANULIsRefusedWhereverItIsFiled(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountThread(t)
		nul := "a\x00b"
		for _, c := range []struct {
			path string
			body map[string]any
		}{
			{"/v1/research/evidence", map[string]any{"uri": "techdata://x", "kind": "document", "content": map[string]any{"bulletin": nul}}},
			{"/v1/research/decisions", map[string]any{"decision": map[string]any{"id": "DEC-1", "note": nul}}},
			{"/v1/research/evaluations", map[string]any{"evaluationRun": map[string]any{"id": "RUN_1", "note": nul}}},
			{"/v1/research/actions", map[string]any{"action": map[string]any{"id": "act", "description": nul}}},
			{"/v1/research/commitments", map[string]any{"decisionId": "DEC-1", "selection": "A", "note": nul}},
			{"/v1/research/grants", map[string]any{"id": nul, "visibility": "public"}},
			{"/v1/research/artifacts", map[string]any{"kind": "snapshot", "content": "aGk=", "run_id": nul}},
		} {
			who := "key"
			if c.path == "/v1/research/commitments" || c.path == "/v1/research/grants" {
				who = "person"
			}
			if code, m := as(t, app, who, "POST", c.path, c.body); code != http.StatusUnprocessableEntity {
				t.Errorf("POST %s with a NUL: %d %v, want 422", c.path, code, m)
			}
		}
		code, m := as(t, app, "person", "POST", "/v1/research/actions/act_x/authorize", map[string]any{"hash": nul})
		if code != http.StatusUnprocessableEntity {
			t.Errorf("authorize with a NUL: %d %v, want 422", code, m)
		}
	})
}

// nextMsg is the next message heard on ch, or an error when none comes in d.
func nextMsg(ch <-chan bus.Message, d time.Duration) (bus.Message, error) {
	select {
	case m := <-ch:
		return m, nil
	case <-time.After(d):
		return bus.Message{}, errors.New("no message")
	}
}
