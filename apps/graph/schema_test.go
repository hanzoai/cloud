package graph

// The declared schema, over HTTP: a declaration is an assertion, so every test
// here declares through the same POST /v1/graph that files anything else.

import (
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/claim"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// one files a single assertion knowable from `seen` (empty is now), as the
// authority given (nil is a member), and returns what the endpoint answered.
func one(t *testing.T, app *zip.App, authority map[string]string, entity, relation, value string, names bool, seen string) (int, string) {
	t.Helper()
	a := map[string]any{
		"entity": entity, "relation": relation, "value": value, "names": names,
		"at": "2026-01-01T00:00:00Z", "source": "test", "evidence": "test://" + entity,
	}
	if seen != "" {
		a["seen"] = seen
	}
	code, b := callAs(t, app, authority, http.MethodPost, "/v1/graph", "", map[string]any{"assertions": []any{a}})
	return code, string(b)
}

// declare files what works_at may join, as an admin, and who is what, as a
// member: a declaration is an admin's to file, and a type is anyone's.
func declare(t *testing.T, app *zip.App) {
	t.Helper()
	assertAs(t, app, orgAdmin, "", "relation:works_at", "domain", "person", false)
	assertAs(t, app, orgAdmin, "", "relation:works_at", "range", "company", false)
	assertFact(t, app, "", "acme/person/z", "type", "person", false)
	assertFact(t, app, "", "acme/co/hanzo", "type", "company", false)
	assertFact(t, app, "", "acme/co/zoo", "type", "foundation", false)
}

// TestTheDeclaredSchemaDecidesAdmission is the feature: a relation with a
// declared domain and range admits exactly the facts whose types meet them, and
// says which declaration a refused one missed.
func TestTheDeclaredSchemaDecidesAdmission(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, tc := range []struct {
			name          string
			entity, value string
			names         bool
			ok            bool
			says          string
		}{
			{"right types", "acme/person/z", "acme/co/hanzo", true, true, ""},
			{"wrong subject", "acme/co/hanzo", "acme/co/hanzo", true, false, "works_at's domain is person, and acme/co/hanzo is company"},
			{"wrong object", "acme/person/z", "acme/co/zoo", true, false, "works_at's range is company, and acme/co/zoo is foundation"},
			{"untyped object", "acme/person/z", "acme/co/nobody", true, false, "acme/co/nobody has no type"},
			{"a scalar where an entity is declared", "acme/person/z", "hanzo", false, false, "its value names an entity"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				app, _ := audited(t)
				declare(t, app)
				code, b := one(t, app, nil, tc.entity, "works_at", tc.value, tc.names, "")
				if tc.ok {
					if code >= 300 {
						t.Fatalf("a conforming fact answered %d: %s", code, b)
					}
					return
				}
				if code != http.StatusBadRequest {
					t.Fatalf("a fact the schema refuses answered %d, want 400: %s", code, b)
				}
				if !strings.Contains(b, tc.says) || !strings.Contains(b, tc.entity+" works_at") {
					t.Errorf("the refusal must name the fact and the declaration; got %s", b)
				}
			})
		}
	})
}

// TestAnUndeclaredRelationStaysOpen is today's behaviour kept: declaring one
// relation constrains that relation and no other, and an untyped entity may
// still assert anything nobody declared.
func TestAnUndeclaredRelationStaysOpen(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		declare(t, app)
		for _, f := range [][2]string{{"acme/co/hanzo", "owns"}, {"acme/thing/x", "likes"}} {
			if code, b := one(t, app, nil, f[0], f[1], "acme/co/zoo", true, ""); code >= 300 {
				t.Errorf("%s %s is undeclared and was refused: %d %s", f[0], f[1], code, b)
			}
		}
		// And the empty string reopens a declared one, from when it is knowable. It
		// is filed a minute out because the order counts in seconds: a reopening in
		// the same second as the declaration ties, and the digest breaks it.
		reopen := time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
		if code, b := one(t, app, orgAdmin, "relation:works_at", "range", "", false, reopen); code >= 300 {
			t.Fatalf("reopen: %d %s", code, b)
		}
		if code, b := one(t, app, nil, "acme/person/z", "works_at", "acme/co/zoo", true, reopen); code >= 300 {
			t.Errorf("a reopened range still refused: %d %s", code, b)
		}
	})
}

// TestARetractionIsNeverRefused: a declared range asks for an entity, and a
// retraction names none — it withdraws. Refusing it would make every claim under
// a declared relation permanent, so it is admitted, and from when it is knowable
// the edge it won is no longer walked.
func TestARetractionIsNeverRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		declare(t, app)
		if code, b := one(t, app, nil, "acme/person/z", "works_at", "acme/co/hanzo", true, ""); code >= 300 {
			t.Fatalf("assert: %d %s", code, b)
		}
		// A minute out, for the reason the reopening above is: the order counts in
		// seconds.
		withdrawn := time.Now().UTC().Add(time.Minute)
		if code, b := one(t, app, nil, "acme/person/z", "works_at", "", false, withdrawn.Format(time.RFC3339)); code >= 300 {
			t.Fatalf("a retraction under a declared range was refused: %d %s", code, b)
		}
		// The retraction is of the same moment as the edge, heard later: the path
		// is there as known now and gone as known once the retraction is.
		for known, want := range map[time.Time]bool{time.Now().UTC(): true, withdrawn.Add(time.Minute): false} {
			var out graphPathOut
			_, b := call(t, app, http.MethodPost, "/v1/graph/path", "", map[string]any{
				"from": "acme/person/z", "to": "acme/co/hanzo", "as_known": known.Format(time.RFC3339),
			})
			if err := json.Unmarshal(b, &out); err != nil || out.Found != want {
				t.Errorf("path as known %s: found=%v, want %v: %s", known.Format(time.RFC3339), out.Found, want, b)
			}
		}
	})
}

// TestAReDeclarationBindsFromWhenItIsKnowable is the schema's history. A range
// re-declared to become knowable in two minutes binds facts knowable after it
// and not facts knowable now, and the vocabulary answers what is in force now.
func TestAReDeclarationBindsFromWhenItIsKnowable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		declare(t, app)
		later := time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339)
		after := time.Now().UTC().Add(3 * time.Minute).Format(time.RFC3339)
		if code, b := one(t, app, orgAdmin, "relation:works_at", "range", "foundation", false, later); code >= 300 {
			t.Fatalf("re-declare: %d %s", code, b)
		}

		for _, tc := range []struct {
			name, value, seen string
			ok                bool
		}{
			{"now, under the first range", "acme/co/hanzo", "", true},
			{"now, under the second range", "acme/co/zoo", "", false},
			{"after, under the second range", "acme/co/zoo", after, true},
			{"after, under the first range", "acme/co/hanzo", after, false},
		} {
			code, b := one(t, app, nil, "acme/person/z", "works_at", tc.value, true, tc.seen)
			if got := code < 300; got != tc.ok {
				t.Errorf("%s: answered %d, want admitted=%v: %s", tc.name, code, tc.ok, b)
			}
		}

		env := ask(t, app, `{ vocabulary { schema { relation domain range } } }`)
		schema := env["data"].(map[string]any)["vocabulary"].(map[string]any)["schema"].([]any)
		if len(schema) != 1 {
			t.Fatalf("vocabulary declares %v, want works_at alone", schema)
		}
		d := schema[0].(map[string]any)
		if d["relation"] != "works_at" || d["domain"] != "person" || d["range"] != "company" {
			t.Errorf("vocabulary answered %v; now is still under the first range", d)
		}
	})
}

// TestASourceFilesOnlyWhatConforms carries the schema through extract and
// ingest. The document types its own entities, which counts, and states one
// relation backwards, which is reported and not filed.
func TestASourceFilesOnlyWhatConforms(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const text = `# acme/person/y
type:: person
works_at:: [[acme/co/lux]]

# acme/co/lux
type:: company
works_at:: [[acme/person/y]]
`
		app, _ := audited(t)
		declare(t, app)

		got := extracted(t, app, text)
		if len(got) != 3 {
			t.Errorf("extract admitted %d relations, want the 3 that conform: %+v", len(got), got)
		}
		code, b := source(t, app, "/v1/graph/extract", "", text)
		var ex graphExtractOut
		if err := json.Unmarshal(b, &ex); err != nil || code >= 300 {
			t.Fatalf("extract: %d %v %s", code, err, b)
		}
		if len(ex.Refused) != 1 || ex.Refused[0].Subject != "acme/co/lux" || !strings.Contains(ex.Refused[0].Reason, "domain is person") {
			t.Errorf("extract refused %+v, want the backwards works_at with its reason", ex.Refused)
		}

		code, b = source(t, app, "/v1/graph/ingest", "", text)
		var out graphAssertOut
		if err := json.Unmarshal(b, &out); err != nil || code >= 300 {
			t.Fatalf("ingest: %d %v %s", code, err, b)
		}
		if out.Recorded != 3 || out.Refused != 1 || len(out.Reasons) != 1 {
			t.Errorf("ingest recorded=%d refused=%d reasons=%v, want 3, 1 and its reason", out.Recorded, out.Refused, out.Reasons)
		}
		for _, f := range answered(t, app, "", "/v1/graph?entity=acme/co/lux") {
			if f.Relation == "works_at" {
				t.Errorf("a refused relation was filed: %+v", f)
			}
		}

		// A source every relation of which is refused is a 400, like any batch that
		// filed nothing.
		if code, b := source(t, app, "/v1/graph/ingest", "", "# acme/co/hanzo\nworks_at:: [[acme/co/lux]]\n"); code != http.StatusBadRequest {
			t.Errorf("a wholly refused source answered %d: %s", code, b)
		}
	})
}

// superElsewhere is a SuperAdmin acting in a tenant that has not made them one
// of its own admins.
var superElsewhere = map[string]string{"X-User-IsAdmin": "true", "X-User-Owner": "admin"}

// filing posts one batch as the authority given and reads what it answered.
func filing(t *testing.T, app *zip.App, authority map[string]string, assertions ...map[string]any) (int, graphAssertOut, string) {
	t.Helper()
	return filingIn(t, app, "", authority, assertions...)
}

// filingIn is filing in a project: a test that mounts twice and wants the second
// to start empty files in another, because on hanzo-sql a store outlives the
// mount that opened it.
func filingIn(t *testing.T, app *zip.App, project string, authority map[string]string, assertions ...map[string]any) (int, graphAssertOut, string) {
	t.Helper()
	for _, a := range assertions {
		a["at"], a["source"], a["evidence"] = "2026-01-01T00:00:00Z", "test", "test://batch"
	}
	code, b := callAs(t, app, authority, http.MethodPost, "/v1/graph", project, map[string]any{"assertions": assertions})
	var out graphAssertOut
	if code == http.StatusOK {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("the answer is not the assert shape: %v: %s", err, b)
		}
	}
	return code, out, string(b)
}

// TestADeclarationIsAnAdminsToFile is who may write the schema. A declaration
// binds what every member of the organization may assert, so a member is
// refused one, and so is a SuperAdmin acting in a tenant that has not made them
// its own; an org admin's is filed and is on the audit trail, naming what was
// declared, under the digest of what was filed.
func TestADeclarationIsAnAdminsToFile(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, tc := range []struct {
			name      string
			authority map[string]string
			code      int
		}{
			{"member", nil, http.StatusForbidden},
			{"superadmin elsewhere", superElsewhere, http.StatusForbidden},
			{"org admin", orgAdmin, http.StatusOK},
		} {
			t.Run(tc.name, func(t *testing.T) {
				app, rec := audited(t)
				code, b := one(t, app, tc.authority, "relation:works_at", "domain", "person", false, "")
				if code != tc.code {
					t.Fatalf("status %d, want %d: %s", code, tc.code, b)
				}
				filed := answered(t, app, "", "/v1/graph?entity=relation:works_at")
				declared := trailOf(t, rec, "graph.declare")
				if code != http.StatusOK {
					if !strings.Contains(b, "relation:works_at is a declaration") || !strings.Contains(b, "requires an admin of the organization") {
						t.Errorf("the refusal must name the declaration and why: %s", b)
					}
					if len(filed) != 0 || len(declared) != 0 {
						t.Fatalf("a refused declaration left %d rows and %d trail records", len(filed), len(declared))
					}
					return
				}
				if len(filed) != 1 || len(declared) != 1 {
					t.Fatalf("an admin's declaration: %d rows and %d trail records, want 1 and 1", len(filed), len(declared))
				}
				r := declared[0]
				if r.Resource.ID != claim.Digest(filed[0].ID) || r.Actor.Org != "acme" || r.Actor.Sub == "" || r.Outcome.Result != "success" {
					t.Fatalf("trail record: %+v", r)
				}
				var after struct {
					Declared []map[string]string `json:"declared"`
				}
				if err := json.Unmarshal(r.After, &after); err != nil {
					t.Fatalf("after: %v", err)
				}
				want := map[string]string{"id": filed[0].ID, "entity": "relation:works_at", "relation": "domain", "value": "person"}
				if len(after.Declared) != 1 || !maps.Equal(after.Declared[0], want) {
					t.Fatalf("the trail says %v was declared, want %v", after.Declared, want)
				}

				// A redelivery changes nothing, so it records nothing.
				if code, b := one(t, app, tc.authority, "relation:works_at", "domain", "person", false, ""); code != http.StatusOK {
					t.Fatalf("redelivery: %d %s", code, b)
				}
				if n := len(trailOf(t, rec, "graph.declare")); n != 1 {
					t.Fatalf("a redelivered declaration was recorded again: %d records", n)
				}
			})
		}
	})
}

// TestAMembersOwnFactsStillLand: a member's declaration is refused and nothing
// else in the batch is. A type is an ordinary fact about an ordinary entity,
// and so is every relation between two of them.
func TestAMembersOwnFactsStillLand(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, rec := audited(t)
		code, out, b := filing(t, app, nil,
			map[string]any{"entity": "relation:works_at", "relation": "range", "value": "company"},
			map[string]any{"entity": "acme/person/z", "relation": "type", "value": "person"},
			map[string]any{"entity": "acme/person/z", "relation": "works_at", "value": "acme/co/hanzo", "names": true},
		)
		if code != http.StatusOK || out.Recorded != 2 || out.Refused != 1 {
			t.Fatalf("status %d, recorded %d, refused %d; want 200, 2 and 1: %s", code, out.Recorded, out.Refused, b)
		}
		if len(out.Reasons) != 1 || !strings.Contains(out.Reasons[0], "requires an admin") {
			t.Errorf("reasons = %v, want the declaration's", out.Reasons)
		}
		if got := answered(t, app, "", "/v1/graph?entity=acme/person/z"); len(got) != 2 {
			t.Errorf("the member's own facts: %d filed, want 2", len(got))
		}
		if n := len(trailOf(t, rec, "graph.declare")); n != 0 {
			t.Errorf("a batch that declared nothing is on the trail %d times", n)
		}
	})
}

// TestASourceIsJudgedAsItIsFiled: a source that declares is judged by who sent
// it, in extract as in ingest. A member's declaration is refused for want of
// authority, so it is not schema and the edge it would have constrained lands;
// an admin's is filed, and binds that edge in the same breath.
func TestASourceIsJudgedAsItIsFiled(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const text = "# relation:works_at\ndomain:: person\n# acme/bob\nworks_at:: [[acme/co]]\nteam:: core\n"
		for _, tc := range []struct {
			name            string
			authority       map[string]string
			refused, says   string
			declared, about int
		}{
			{"member", nil, "relation:works_at", "requires an admin", 0, 2},
			{"org admin", orgAdmin, "acme/bob", "works_at's domain is person, and acme/bob has no type", 1, 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				app, _ := audited(t)
				// Each case files in a project of its own: on hanzo-sql a store outlives
				// the mount that opened it, and the second case starts empty.
				project := strings.ReplaceAll(tc.name, " ", "-")
				read := func(path string, out any) {
					t.Helper()
					code, b := callAs(t, app, tc.authority, http.MethodPost, path, project, map[string]any{
						"source": "wiki/api", "at": readAt, "text": text,
					})
					if err := json.Unmarshal(b, out); err != nil || code != http.StatusOK {
						t.Fatalf("%s: %d %v %s", path, code, err, b)
					}
				}

				var ex graphExtractOut
				read("/v1/graph/extract", &ex)
				if len(ex.Triples) != 2 || len(ex.Refused) != 1 || ex.Refused[0].Subject != tc.refused || !strings.Contains(ex.Refused[0].Reason, tc.says) {
					t.Errorf("extract admitted %+v and refused %+v, want 2 and the one about %s", ex.Triples, ex.Refused, tc.refused)
				}
				var out graphAssertOut
				read("/v1/graph/ingest", &out)
				if out.Recorded != 2 || out.Refused != 1 || len(out.Reasons) != 1 || !strings.Contains(out.Reasons[0], tc.says) {
					t.Errorf("ingest recorded=%d refused=%d reasons=%v, want 2, 1 and %q", out.Recorded, out.Refused, out.Reasons, tc.says)
				}
				if n := len(answered(t, app, project, "/v1/graph?entity=relation:works_at")); n != tc.declared {
					t.Errorf("%d declarations filed, want %d", n, tc.declared)
				}
				if n := len(answered(t, app, project, "/v1/graph?entity=acme/bob")); n != tc.about {
					t.Errorf("%d assertions about acme/bob filed, want %d", n, tc.about)
				}
			})
		}

		// A member's source that only declares files nothing it may, so it is
		// refused whole for want of authority.
		app, _ := audited(t)
		if code, b := source(t, app, "/v1/graph/ingest", "", "# relation:works_at\ndomain:: person\n"); code != http.StatusForbidden {
			t.Errorf("a member's source of declarations answered %d, want 403: %s", code, b)
		}
	})
}

// TestADeclarationUnrecordedIsNotFiled: a deployment with no trail files no
// declaration, and neither does one whose trail fails at the write — and in
// that batch nothing else is filed either, because the batch is one write.
// Without a declaration in it, a batch never asks the trail.
func TestADeclarationUnrecordedIsNotFiled(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		declaration := func() map[string]any {
			return map[string]any{"entity": "relation:works_at", "relation": "domain", "value": "person"}
		}
		ordinary := func() map[string]any {
			return map[string]any{"entity": "acme/person/z", "relation": "type", "value": "person"}
		}

		bare := mountGraph(t)
		if code, _, b := filing(t, bare, orgAdmin, declaration(), ordinary()); code != http.StatusServiceUnavailable {
			t.Fatalf("no trail: status %d, want 503: %s", code, b)
		}
		if got := answered(t, bare, "", "/v1/graph"); len(got) != 0 {
			t.Fatalf("no trail, and %d rows filed", len(got))
		}
		if code, out, b := filing(t, bare, nil, ordinary()); code != http.StatusOK || out.Recorded != 1 {
			t.Fatalf("an ordinary fact on a deployment with no trail: %d %s", code, b)
		}
		_ = Shutdown()

		app, rec := audited(t)
		_ = rec.Close()
		if code, _, b := filingIn(t, app, "trail", orgAdmin, declaration(), ordinary()); code != http.StatusServiceUnavailable {
			t.Fatalf("a trail that fails: status %d, want 503: %s", code, b)
		}
		if got := answered(t, app, "trail", "/v1/graph"); len(got) != 0 {
			t.Fatalf("a failed record, and %d rows filed", len(got))
		}
	})
}
