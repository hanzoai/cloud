package train

// publish_test.go — a capability is published with the route it answers by, and
// the platform's one list of every org's published capabilities is what the
// decision service polls.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// asks is a route option: a key's exact question.
func asks(kind string, labels ...string) map[string]any {
	return map[string]any{"type": kind, "labels": labels}
}

func TestAPublishIsRoutedAndRefusesARouteItCannotCarry(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		sum := strings.Repeat("f1", 32)
		id, code, m := r.published("acme", "triage", sum, job("dgx"), nil)
		if code != 400 || !strings.Contains(str(m, "detail"), "route") {
			t.Fatalf("a capability with no route: %d %v", code, m)
		}
		for _, bad := range []map[string]any{
			{"keys": []string{}},
			{"keys": []string{"topic", "topic"}},
			{"keys": []string{"topic\x00"}},
			{"keys": []string{"topic"}, "options": map[string]any{"level": asks("score", "low", "high")}},
			{"keys": []string{"topic"}, "options": map[string]any{"topic": asks("list", "a", "b")}},
			{"keys": []string{"topic"}, "options": map[string]any{"topic": asks("choice")}},
			{"keys": []string{"topic"}, "options": map[string]any{"topic": asks("choice", "a", "a")}},
		} {
			if code, m := r.as("POST", "/v1/train/jobs/"+id+"/publish", "acme", false, map[string]any{"name": "triage", "route": bad}); code != 400 {
				t.Fatalf("route %v: %d %v", bad, code, m)
			}
		}
		rt := map[string]any{"keys": []string{"topic", "level"}, "options": map[string]any{"topic": asks("choice", "billing", "bug", "other")}}
		p := r.must(200, "POST", "/v1/train/jobs/"+id+"/publish", "acme", map[string]any{"name": "triage", "route": rt})
		if str(p, "published", "form") != "full" || str(p, "published", "route", "options", "topic", "type") != "choice" || str(p, "published", "base_sha256") != baseSHA {
			t.Fatalf("publish: %v", p)
		}
		// Another capability may take the same key only for another question.
		other := strings.Repeat("f2", 32)
		if _, code, m := r.published("acme", "levels", other, job("dgx"), map[string]any{"keys": []string{"level"}}); code != 409 || str(m, "code") != "route_conflict" {
			t.Fatalf("a second capability on a key the first answers for every question: %d %v", code, m)
		}
		third := strings.Repeat("f3", 32)
		if _, code, m := r.published("acme", "topics", third, job("dgx"), map[string]any{"keys": []string{"topic"}, "options": map[string]any{"topic": asks("choice", "billing", "bug", "other")}}); code != 409 || str(m, "code") != "route_conflict" {
			t.Fatalf("a second capability on the same question: %d %v", code, m)
		}
		fourth := strings.Repeat("f4", 32)
		if _, code, m := r.published("acme", "spam", fourth, job("dgx"), map[string]any{"keys": []string{"topic"}, "options": map[string]any{"topic": asks("noul", "false", "true")}}); code != 200 {
			t.Fatalf("a second capability on another question under the key: %d %v", code, m)
		}
		// Another org's routes are its own.
		if _, code, m := r.published("evil", "triage", sum, job("dgx"), map[string]any{"keys": []string{"topic", "level"}}); code != 200 {
			t.Fatalf("another org's publish: %d %v", code, m)
		}
		// Republishing a name replaces that capability, whatever its old route claimed.
		if _, code, m := r.published("acme", "triage", strings.Repeat("f5", 32), job("dgx"), map[string]any{"keys": []string{"level"}}); code != 200 {
			t.Fatalf("republishing a name: %d %v", code, m)
		}
	})
}

func TestThePublishedListIsThePlatformsAndCheapToPoll(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		if code, _ := r.do("GET", "/v1/train/published", "acme", nil); code != 403 {
			t.Fatalf("an org read every org's capabilities: %d", code)
		}
		empty := r.must(200, "GET", "/v1/train/published", "hanzo", nil)
		if empty["changed"] != true || len(empty["data"].([]any)) != 0 || str(empty, "revision") == "" {
			t.Fatalf("empty: %v", empty)
		}
		full := strings.Repeat("a1", 32)
		if _, code, m := r.published("acme", "triage", full, job("dgx"), map[string]any{"keys": []string{"topic"}}); code != 200 {
			t.Fatalf("publish: %d %v", code, m)
		}
		head := strings.Repeat("a2", 32)
		ro := job("dgx")
		ro["adaptation"] = map[string]any{"mode": "readout"}
		if _, code, m := r.published("zeta", "intent", head, ro, map[string]any{"keys": []string{"intent"}, "options": map[string]any{"intent": asks("score", "low", "high")}}); code != 200 {
			t.Fatalf("publish a readout: %d %v", code, m)
		}
		l := r.must(200, "GET", "/v1/train/published", "hanzo", nil)
		data := l["data"].([]any)
		if len(data) != 2 || l["changed"] != true || str(l, "revision") == str(empty, "revision") {
			t.Fatalf("list: %v", l)
		}
		a, z := data[0].(map[string]any), data[1].(map[string]any)
		if str(a, "org") != "acme" || str(a, "name") != "triage" || str(a, "sha256") != full || str(a, "base_sha256") != baseSHA || str(a, "form") != "full" ||
			str(a, "route", "keys") != "[topic]" || !strings.Contains(str(a, "url"), "acme/"+full) || a["published"].(float64) == 0 || a["expires"].(float64) == 0 {
			t.Fatalf("acme: %v", a)
		}
		if str(z, "org") != "zeta" || str(z, "form") != "head" || str(z, "route", "options", "intent", "type") != "score" {
			t.Fatalf("zeta: %v", z)
		}
		rev := str(l, "revision")
		same := r.must(200, "GET", "/v1/train/published?revision="+rev, "hanzo", nil)
		if same["changed"] != false || len(same["data"].([]any)) != 0 || str(same, "revision") != rev {
			t.Fatalf("a poll at the current revision: %v", same)
		}
		again := r.must(200, "GET", "/v1/train/published", "hanzo", nil)
		if str(again, "revision") != rev || str(again["data"].([]any)[0].(map[string]any), "url") == "" {
			t.Fatalf("a read with fresh URLs moved the revision: %v", again)
		}
		if _, code, m := r.published("acme", "triage", strings.Repeat("a3", 32), job("dgx"), map[string]any{"keys": []string{"topic"}}); code != 200 {
			t.Fatalf("republish: %d %v", code, m)
		}
		moved := r.must(200, "GET", "/v1/train/published?revision="+rev, "hanzo", nil)
		if moved["changed"] != true || str(moved, "revision") == rev || len(moved["data"].([]any)) != 2 {
			t.Fatalf("a poll after a republish: %v", moved)
		}
	})
}

func TestAnOrgsCapabilitiesAreItsOwnAndAHeadReachesTheModelsBucket(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		o := ops{s: r.svc}
		set := func(org string) []map[string]any {
			t.Helper()
			c, err := o.capabilities(cloud.For(context.Background(), org), nil)
			if err != nil {
				t.Fatalf("%s: %v", org, err)
			}
			var out []map[string]any
			if err := json.Unmarshal([]byte(c.Set), &out); err != nil || out == nil {
				t.Fatalf("%s: %q %v", org, c.Set, err)
			}
			return out
		}
		if _, err := o.capabilities(context.Background(), nil); err == nil {
			t.Fatal("no org read capabilities")
		}
		if s := set("acme"); len(s) != 0 {
			t.Fatalf("nothing published: %v", s)
		}
		full := strings.Repeat("a1", 32)
		if _, code, m := r.published("acme", "triage", full, job("dgx"), map[string]any{"keys": []string{"topic"}}); code != 200 {
			t.Fatalf("publish: %d %v", code, m)
		}
		head := strings.Repeat("a2", 32)
		ro := job("dgx")
		ro["adaptation"] = map[string]any{"mode": "readout"}
		if _, code, m := r.published("zeta", "intent", head, ro, map[string]any{"keys": []string{"intent"}, "options": map[string]any{"intent": asks("score", "low", "high")}}); code != 200 {
			t.Fatalf("publish a readout: %d %v", code, m)
		}
		a := set("acme")
		if len(a) != 1 || len(a[0]) != 5 || str(a[0], "name") != "triage" || str(a[0], "sha256") != full || str(a[0], "base_sha256") != baseSHA ||
			str(a[0], "form") != "full" || str(a[0], "route", "keys") != "[topic]" {
			t.Fatalf("acme: %v", a)
		}
		z := set("zeta")
		if len(z) != 1 || str(z[0], "form") != "head" || str(z[0], "route", "options", "intent", "type") != "score" {
			t.Fatalf("zeta: %v", z)
		}
		if r.objs.served["capabilities/zeta/"+head] != head || len(r.objs.served) != 1 {
			t.Fatalf("the serving store holds %v: a head's tar and nothing else", r.objs.served)
		}
		if _, code, m := r.published("acme", "triage", strings.Repeat("a3", 32), job("dgx"), map[string]any{"keys": []string{"topic"}}); code != 200 {
			t.Fatalf("republish: %d %v", code, m)
		}
		if a := set("acme"); len(a) != 1 || str(a[0], "sha256") != strings.Repeat("a3", 32) {
			t.Fatalf("a republish: %v", a)
		}
	})
}
