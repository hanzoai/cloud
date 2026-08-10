package label

// bound_test.go is the BYTE bound, and it exists because a bound on COUNT over
// caller-sized values is not a bound at all.
//
// TestEveryBoundIsEnforced (wire_test.go) walks the counts: 1000 assertions, 500
// resolved events, a 400-day window, 1000 held ids. Every one of them passed while
// a single resolve could name 500 subjects of any length whatsoever — the rows were
// bounded and the bytes were bounded by the edge's BodyLimit, which is a fact about
// the deployment and not about this plane. Each subject is then copied into a
// dedupe key, a grouping key and one bound parameter per event in a statement
// against a single-writer file, so the count bound was a bound on the wrong axis
// of the same request.
//
// TWO HALVES, AND NEITHER IS SUFFICIENT ALONE.
//
//	TestEveryCallerSizedFieldDeclaresACeiling is STRUCTURAL: it walks every door's
//	In type with reflect and fails on a string field with no declared ceiling. A
//	new field cannot arrive unbounded and be noticed later, which is what happened
//	here — the write door bounded its subject and the read doors, added after, did
//	not, and nothing compared them.
//
//	TestNoCountBoundStandsWithoutAByteBound is BEHAVIOURAL: every declared ceiling
//	is refused one byte over, over the real router. A declaration nothing enforces
//	is a comment.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/openapi"
)

// doors is every In type this plane accepts — the closed set its router registers.
//
// It is written down because reflect cannot enumerate a generic registry's type
// parameters, and a hand-written list is exactly the thing that goes stale. So it
// is PINNED against the registered operation count below: an op added without its
// In type added here fails, which is the only property that makes the list worth
// having.
var doors = []any{
	riskLabelIn{},
	riskLabelsIn{},
	riskResolveIn{},
	riskCoverageIn{},
	riskVocabularyIn{},
	riskDisposeIn{},
	riskHoldIn{},
}

// The three kinds of ceiling, and there are only three.
const (
	// vocabulary: the value must be a member of a closed finite set, so the
	// longest member IS the bound and no separate number is needed. The closed set
	// is the reason the field is bounded, not a decoration on it.
	vocabulary = "vocabulary"
	// instant: the value goes through stamp(), which is the ONE parser every time
	// field on this surface passes and which carries instantMax.
	instant = "instant"
)

// ceilings is the declared bound on every caller-sized field of every door, keyed
// by the field's path from its In type. A number is a ceiling in bytes.
var ceilings = map[string]string{
	// The write door. One assertion is bounded by admit(), which is the same
	// function the read doors ask for the same fields.
	"riskLabelIn.Labels[].Kind":        vocabulary,
	"riskLabelIn.Labels[].Subject":     fmt.Sprint(subjectMax),
	"riskLabelIn.Labels[].At":          instant,
	"riskLabelIn.Labels[].Seen":        instant,
	"riskLabelIn.Labels[].Disposition": vocabulary,
	"riskLabelIn.Labels[].Source":      vocabulary,
	"riskLabelIn.Labels[].Evidence":    fmt.Sprint(evidenceMax),

	// The read filter. Every term becomes a bound parameter against the tenant's
	// own file, so an unbounded one is a megabyte in a statement looking for a
	// value that could not be in the store.
	"riskLabelsIn.Kind":    vocabulary,
	"riskLabelsIn.Subject": fmt.Sprint(subjectMax),
	"riskLabelsIn.Source":  vocabulary,
	"riskLabelsIn.From":    instant,
	"riskLabelsIn.To":      instant,

	// The resolve door: the one where the count multiplies the value.
	"riskResolveIn.Subjects[].Kind":    vocabulary,
	"riskResolveIn.Subjects[].Subject": fmt.Sprint(subjectMax),
	"riskResolveIn.Subjects[].At":      instant,
	"riskResolveIn.Now":                instant,

	"riskCoverageIn.From": instant,
	"riskCoverageIn.To":   instant,

	"riskDisposeIn.Before": instant,

	"riskHoldIn.IDs[]": fmt.Sprint(idMax),
}

// TestEveryCallerSizedFieldDeclaresACeiling walks every door with reflect and
// refuses a string field that declares no bound.
//
// This is the STRUCTURAL half. A field is caller-sized exactly when it is a string
// (numbers and bools are fixed width and a bool cannot be made large), so the walk
// needs no judgment: every string leaf, at any depth, through any slice, must
// appear in `ceilings`. A new one fails here rather than in production, and the
// failure names the field and the file to edit.
func TestEveryCallerSizedFieldDeclaresACeiling(t *testing.T) {
	seen := map[string]bool{}
	var walk func(t reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		switch rt.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			suffix := ""
			if rt.Kind() != reflect.Pointer {
				suffix = "[]"
			}
			walk(rt.Elem(), path+suffix)
		case reflect.Struct:
			for f := range rt.Fields() {
				if !f.IsExported() {
					continue
				}
				walk(f.Type, path+"."+f.Name)
			}
		case reflect.String:
			seen[path] = true
		}
	}
	for _, d := range doors {
		rt := reflect.TypeOf(d)
		walk(rt, rt.Name())
	}
	if len(seen) == 0 {
		t.Fatal("the walk found no caller-sized field at all — it proved nothing")
	}

	var missing, stale []string
	for f := range seen {
		if ceilings[f] == "" {
			missing = append(missing, f)
		}
	}
	for f := range ceilings {
		if !seen[f] {
			stale = append(stale, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	for _, f := range missing {
		t.Errorf("UNBOUNDED: %s is caller-sized and declares no ceiling. A count bound over it "+
			"bounds rows and not bytes. Give it a ceiling in `ceilings` and enforce it at the door "+
			"(admitSubject / admitKind / admitSource / admitEvidence / stamp).", f)
	}
	for _, f := range stale {
		t.Errorf("STALE: %q is declared in `ceilings` and is not a field of any door. A ceiling for "+
			"a field that no longer exists inflates the count and hides the next real gap.", f)
	}
	t.Logf("%d caller-sized fields across %d doors, every one bounded", len(seen), len(doors))
}

// TestEveryDoorIsWalked pins the door list against the registry, because a list
// nothing checks is a list that goes stale. An op added without its In type added
// to `doors` publishes a door the walk above never sees, which is precisely how an
// unbounded field arrives unnoticed.
func TestEveryDoorIsWalked(t *testing.T) {
	app, _ := wireApp(t, "")
	doc, err := openapi.FleetSpec(app)
	if err != nil {
		t.Fatalf("FleetSpec: %v", err)
	}
	ops := 0
	for _, item := range doc.Paths {
		ops += len(item)
	}
	if ops == 0 {
		t.Fatal("the projection found no operation — this gate proved nothing")
	}
	if ops != len(doors) {
		t.Fatalf("the router registers %d operations and `doors` names %d In types; "+
			"every op has exactly one In, so add the new one to `doors` (and its fields to `ceilings`)", ops, len(doors))
	}
}

// TestNoCountBoundStandsWithoutAByteBound is the BEHAVIOURAL half: every declared
// ceiling, refused one byte over, over the real router.
//
// The resolve case is the one that mattered. maxResolve bounded the events at 500
// and nothing bounded a subject, so the request a shared single-writer pod had to
// hold was 500 × whatever the edge let through, amplified three times below the
// door. With the ceiling asked at the door, 500 × subjectMax IS that bound.
func TestNoCountBoundStandsWithoutAByteBound(t *testing.T) {
	app, _ := wireApp(t, "")
	at := time.Now().UTC().Add(-300 * 24 * time.Hour).Format(time.RFC3339)
	over := strings.Repeat("s", subjectMax+1)
	longID := strings.Repeat("d", idMax+1)
	// A BIG instant, not merely one byte over. An instant is not a value the parser
	// can be talked into accepting, so 400-vs-200 cannot observe its ceiling: what
	// the ceiling BUYS is that the value is measured before time.Parse walks it and
	// before %q renders it into a refusal and a log line. So the assertion below is
	// that the refusal stays SMALL — a bound whose only symptom is the caller's own
	// kilobytes echoed back to it is still a bound worth having, and this is how it
	// is observed.
	//
	// Two sizes, because the two lanes have different envelopes and neither number
	// is arbitrary. A GET carries the instant in the query string, which the edge
	// reads into a fixed request-header buffer (fiber ReadBufferSize, 4 KiB), so an
	// instant that could not fit there never reaches this plane at all and would
	// prove nothing about it. A POST body is bounded only by BodyLimit, which is
	// 16 MiB, so it is the lane where the amplification actually lives.
	queryInstant := strings.Repeat("2", 2<<10)
	bodyInstant := strings.Repeat("2", 64<<10)

	for _, tc := range []struct {
		what, method, path, body string
	}{
		{"resolve: a subject over the ceiling", http.MethodPost, "/v1/risk/labels/resolve",
			fmt.Sprintf(`{"subjects":[{"kind":"transaction","subject":%q,"at":%q}]}`, over, at)},
		{"resolve: an unknown kind", http.MethodPost, "/v1/risk/labels/resolve",
			fmt.Sprintf(`{"subjects":[{"kind":"teapot","subject":"tx-1","at":%q}]}`, at)},
		{"resolve: an oversized instant", http.MethodPost, "/v1/risk/labels/resolve",
			fmt.Sprintf(`{"subjects":[{"kind":"transaction","subject":"tx-1","at":%q}]}`, bodyInstant)},
		{"resolve: an oversized backtest instant", http.MethodPost, "/v1/risk/labels/resolve",
			fmt.Sprintf(`{"subjects":[{"kind":"transaction","subject":"tx-1","at":%q}],"now":%q}`, at, bodyInstant)},
		{"read: a subject filter over the ceiling", http.MethodGet, "/v1/risk/labels?subject=" + over, ""},
		{"read: an unknown kind filter", http.MethodGet, "/v1/risk/labels?kind=teapot", ""},
		{"read: an unknown source filter", http.MethodGet, "/v1/risk/labels?source=rumour", ""},
		{"read: an oversized from", http.MethodGet, "/v1/risk/labels?from=" + queryInstant, ""},
		{"coverage: an oversized to", http.MethodGet, "/v1/risk/labels/coverage?to=" + queryInstant, ""},
		{"dispose: an oversized boundary", http.MethodPost, "/v1/risk/labels/dispose",
			fmt.Sprintf(`{"before":%q}`, bodyInstant)},
		{"hold: an id over the ceiling", http.MethodPost, "/v1/risk/labels/hold",
			fmt.Sprintf(`{"ids":[%q],"hold":true}`, longID)},
	} {
		code, raw := req(t, app, tc.method, tc.path, "acme", "u_acme", tc.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s = %d %.200s, want 400 — the ceiling is declared and not enforced", tc.what, code, raw)
			continue
		}
		// The refusal names the bound and does NOT carry the value back. A 400 whose
		// body is the caller's own 64 KiB is the amplification the ceiling exists to
		// prevent, arriving one layer later: in the response, and in the log line
		// beside it.
		if len(raw) > 1024 {
			t.Errorf("%s refused with a %d-byte body — the oversized value was measured after it was "+
				"rendered into the refusal, so the ceiling is not at the door", tc.what, len(raw))
		}
	}

	// The write door refuses PER FACT rather than per request, deliberately: a
	// webhook redelivering five disputes must not lose four to one malformed
	// fifth. So the property is that the over-long value is refused and NOT
	// recorded, not that the request fails.
	body := batch(
		fmt.Sprintf(`{"kind":"transaction","subject":%q,"at":%q,"seen":%q,"disposition":"productive","source":"dispute","evidence":"d1","confidence":1}`, over, at, at),
		fmt.Sprintf(`{"kind":"transaction","subject":"tx-ok","at":%q,"seen":%q,"disposition":"productive","source":"dispute","evidence":%q,"confidence":1}`, at, at, strings.Repeat("e", evidenceMax+1)),
		fmt.Sprintf(`{"kind":"transaction","subject":"tx-ok","at":%q,"seen":%q,"disposition":"productive","source":"dispute","evidence":"d2","confidence":1}`, at, at),
	)
	code, raw := req(t, app, http.MethodPost, "/v1/risk/labels", "acme", "u_acme", body)
	if code != http.StatusOK {
		t.Fatalf("write = %d %s", code, raw)
	}
	var out riskLabelOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Refused != 2 || out.Recorded != 1 {
		t.Fatalf("refused=%d recorded=%d, want 2 refused (an over-long subject and an over-long evidence) and 1 recorded: %+v",
			out.Refused, out.Recorded, out.Results)
	}
	// And the refusal SAYS which bound it was, so a caller can fix it rather than
	// guess. A refusal nobody can act on is a 400 with a shrug in it.
	for i, r := range out.Results[:2] {
		if !strings.Contains(r.Refusal, "the bound is") {
			t.Errorf("results[%d].refusal = %q, want the bound named", i, r.Refusal)
		}
	}

	// A subject exactly AT the ceiling is admitted, at the write and at the read.
	// A bound that also refuses the legal maximum is a smaller bound wearing the
	// number of a larger one, and nothing here would say so.
	edge := strings.Repeat("s", subjectMax)
	code, raw = req(t, app, http.MethodPost, "/v1/risk/labels", "acme", "u_acme",
		batch(fmt.Sprintf(`{"kind":"transaction","subject":%q,"at":%q,"seen":%q,"disposition":"productive","source":"dispute","evidence":"d3","confidence":1}`, edge, at, at)))
	if code != http.StatusOK {
		t.Fatalf("a subject exactly at the ceiling = %d %s", code, raw)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Recorded != 1 {
		t.Fatalf("a subject of exactly %d bytes was not recorded: %+v", subjectMax, out.Results)
	}
	if code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/resolve", "acme", "u_acme",
		fmt.Sprintf(`{"subjects":[{"kind":"transaction","subject":%q,"at":%q}]}`, edge, at)); code != http.StatusOK {
		t.Fatalf("resolving a subject exactly at the ceiling = %d %s", code, raw)
	}
}
