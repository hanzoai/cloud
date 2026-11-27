package train

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	contract "github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// ensoServed is the Enso catalog as the engine's admin surface writes it
// (hanzo-inc/engine admin.rs) for the catalog production serves (universe
// charts/app/files/enso/catalog.yaml, 2026-10-15): three paid tiers on OpenRouter's
// credit, a free model, and a hidden vision model.
const ensoServed = `{"margin":"3.0","visionFallback":"enso-vl","paid":true,"aliases":{},
"providers":{"openrouter":{"base":"https://openrouter.ai/api/v1","keyEnv":"OPENROUTER_API_KEY","kind":"openai","keyPresent":true,"funding":"prepaid"},
 "free":{"base":"https://openrouter.ai/api/v1","keyEnv":"OPENROUTER_API_KEY","kind":"openai","keyPresent":true,"funding":"free"}},
"models":{
 "enso-flash":{"mode":"","disabled":false,"vision":true,"margin":"","effectiveMargin":"3","funding":"prepaid","minTier":"paid","route":[
  {"provider":"openrouter","upstream":"openai/gpt-6-luna","ctx":900000,"cost":{"in":"0.11","out":"0.55","cache_read":"0.011","cache_write":"0.1375"}},
  {"provider":"openrouter","upstream":"openai/gpt-6-luna-pro","ctx":900000,"cost":{"in":"0.11","out":"0.55","cache_read":"0.011","cache_write":"0.1375"}}]},
 "enso-pro":{"mode":"","disabled":false,"vision":true,"margin":"1.5","effectiveMargin":"1.5","funding":"prepaid","minTier":"paid","route":[
  {"provider":"openrouter","upstream":"openai/gpt-6.1-sol","ctx":272000,"cost":{"in":"2.2","out":"11","cache_read":"0.11","cache_write":"2.75"},"reasons":true},
  {"provider":"openrouter","upstream":"google/gemini-4-argon","ctx":1000000,"cost":{"in":"2.2","out":"11","cache_read":"0.11","cache_write":"0.1875"},"reasons":true},
  {"provider":"openrouter","upstream":"anthropic/claude-sonnet-5.5","ctx":1000000,"cost":{"in":"2.2","out":"11","cache_read":"0.11","cache_write":"2.75"},"reasons":true}]},
 "enso-ultra":{"mode":"","disabled":false,"vision":true,"margin":"1.19","effectiveMargin":"1.19","funding":"prepaid","minTier":"paid","route":[
  {"provider":"openrouter","upstream":"anthropic/claude-sonnet-5.5","ctx":1000000,"cost":{"in":"2.2","out":"11","cache_read":"0.11","cache_write":"2.75"},"reasons":true},
  {"provider":"openrouter","upstream":"anthropic/claude-opus-5.5","ctx":1000000,"cost":{"in":"4.4","out":"22","cache_read":"0.22","cache_write":"5.5"},"reasons":true},
  {"provider":"openrouter","upstream":"google/gemini-4-argon","ctx":1000000,"cost":{"in":"2.2","out":"11","cache_read":"0.11","cache_write":"0.1875"},"reasons":true}]},
 "enso-free":{"mode":"","disabled":false,"vision":true,"margin":"","effectiveMargin":"3","funding":"grant","route":[
  {"provider":"free","upstream":"nvidia/nemotron-3-super-120b-a12b:free","ctx":256000,"cost":{"in":"0","out":"0"}}]},
 "enso-vl":{"mode":"","disabled":true,"vision":true,"margin":"","effectiveMargin":"3","funding":"grant","route":[
  {"provider":"free","upstream":"dots-studio/dots-3-note-preview:free","ctx":256000,"cost":{"in":"0","out":"0"}}]}}}`

// fakeRouter is the ai process's routing of the Enso family, in memory: the
// catalog it serves, its newest version, a price guard, and every apply.
type fakeRouter struct {
	mu      sync.Mutex
	cat     string
	version int64
	notes   []string
	// refuse is the price guard: why it would refuse a catalog, "" when it takes it.
	refuse func(cat string) string
	// down answers every read with an error, as an ai process that cannot be asked.
	down bool
	// moved has someone else apply a version between a read and the next apply.
	moved bool
	// late is a guard that refuses at the apply what the dry run let through: the
	// catalog changed in between.
	late string
	// lost is an apply the catalog took and ai did not record.
	lost bool
}

func newFakeRouter() *fakeRouter {
	return &fakeRouter{cat: ensoServed, refuse: func(string) string { return "" }}
}

func (f *fakeRouter) routing(context.Context) (*contract.Routing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, &zip.HTTPError{Status: http.StatusServiceUnavailable, Msg: "ai is not answering"}
	}
	out := &contract.Routing{Family: "enso", Catalog: []byte(f.cat), Version: f.version}
	if f.moved {
		f.version++
	}
	return out, nil
}

func (f *fakeRouter) apply(_ context.Context, in *contract.RoutingApplyIn) (*contract.RoutingApplied, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.Dry {
		return &contract.RoutingApplied{Base: f.version, Diff: "- a\n+ b\n", Refused: f.refuse(string(in.Catalog))}, nil
	}
	if in.Base != f.version {
		return nil, &zip.HTTPError{Status: http.StatusConflict, Msg: "enso changed since this edit was made"}
	}
	if why := f.refuse(string(in.Catalog)); why != "" {
		return nil, &zip.HTTPError{Status: http.StatusBadRequest, Msg: "enso would not be served: " + why}
	}
	if f.late != "" {
		return nil, &zip.HTTPError{Status: http.StatusBadRequest, Msg: "enso would not be served: " + f.late}
	}
	f.cat = string(in.Catalog)
	if f.lost {
		return nil, &zip.HTTPError{Status: http.StatusInternalServerError, Msg: "enso applied the catalog, and its version was not recorded: disk full"}
	}
	f.version++
	f.notes = append(f.notes, in.Note)
	return &contract.RoutingApplied{Version: f.version, Base: in.Base}, nil
}

func (f *fakeRouter) rates(context.Context) (*contract.Rates, error) {
	return &contract.Rates{Quotes: []contract.Quote{
		{Model: "enso-flash", Price: contract.Price{Input: "0.33", Output: "1.65"}},
		{Model: "enso-pro", Price: contract.Price{Input: "4.4", Output: "22"}},
		{Model: "enso-ultra", Price: contract.Price{Input: "5.5", Output: "27.5"}},
	}}, nil
}

// leads is model's route in the fake's catalog, as upstreams.
func (f *fakeRouter) leads(t *testing.T, model string) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := readEnso([]byte(f.cat))
	if err != nil {
		t.Fatal(err)
	}
	return c.upstreams(model)
}

// doc is a policy file of one benchmark whose tiers each ask one arm.
func doc(t *testing.T, tiers map[string]string, upstreams map[string]string) map[string]any {
	t.Helper()
	models := map[string]any{}
	for arm, up := range upstreams {
		models[arm] = map[string]any{"price_in": 1.0, "price_out": 5.0, "accuracy": map[string]any{"gpqa_diamond": 0.9},
			"answered": map[string]any{"gpqa_diamond": 198}, "latency_p50_s": 10.0, "latency_p95_s": 30.0, "upstream": up}
	}
	ts := map[string]any{}
	for tier, arm := range tiers {
		ts[tier] = map[string]any{"budget_usd_per_1k": 25.0,
			"cascade": map[string]any{"stages": []any{map[string]any{"arms": []string{arm}}}, "agree": 1},
			"train":   score(159, 0.91, 3.1), "heldout": score(159, 0.9, 3.2), "lockbox": score(39, 0.92, 3.0),
			"decisions": map[string]any{"gpqa-000": map[string]any{"answer": "A", "stage": 0, "cost_usd": 0.001, "latency_s": 9.0}}}
	}
	return map[string]any{
		"schema": "enso.policy/1",
		"source": map[string]any{"attempts": "data/attempts", "digest": "fnv1a64:0123456789abcdef", "folds": 5,
			"lockbox": `fnv1a64("<bench>:<question>") % 5 == 0 is the lockbox; the rest fold by (hash / 5) % 5`},
		"models": models,
		"benches": map[string]any{"gpqa_diamond": map[string]any{"questions": 198, "pool": slices.Sorted(keysOf(upstreams)),
			"lockbox": []string{"gpqa-003"}, "ceiling": score(198, 0.99, 196), "best_single": score(159, 0.918, 11.8),
			"frontier": []any{}, "tiers": ts}},
	}
}

func keysOf(m map[string]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func score(n int, acc, usd float64) map[string]any {
	return map[string]any{"n": n, "accuracy": acc, "usd_per_1k": usd, "latency_p50_s": 10.0, "latency_p95_s": 40.0}
}

// tuned is the policy every tier production serves could be promoted to: each
// tier's arm is on its route.
func tuned(t *testing.T) map[string]any {
	return doc(t, map[string]string{"flash": "gpt-6-luna-pro", "pro": "sonnet-5.5", "ultra": "opus-5.5"}, map[string]string{
		"gpt-6-luna-pro": "openai/gpt-6-luna-pro", "sonnet-5.5": "anthropic/claude-sonnet-5.5", "opus-5.5": "anthropic/claude-opus-5.5"})
}

// cascaded is the policy the engine shipped at ec4d929: its tiers are cascades of
// several arms, one of them named enso, and its arms name no upstream.
func cascaded(t *testing.T) map[string]any {
	d := doc(t, map[string]string{"flash": "deepseek-v4-pro", "enso": "deepseek-3.2"}, map[string]string{
		"deepseek-v4-pro": "", "deepseek-3.2": "", "gpt-5.6-sol": ""})
	tiers := d["benches"].(map[string]any)["gpqa_diamond"].(map[string]any)["tiers"].(map[string]any)
	tiers["enso"].(map[string]any)["cascade"] = map[string]any{"stages": []any{
		map[string]any{"arms": []string{"deepseek-3.2"}}, map[string]any{"arms": []string{"gpt-5.6-sol", "deepseek-v4-pro"}}}, "agree": 2}
	return d
}

// promote reviews version v (its read names the catalog promoting it sends) and
// promotes it over base, as the page does.
func (r *rig) promote(v, base int, note string) (int, map[string]any) {
	r.t.Helper()
	got := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso/versions/"+strconv.Itoa(v), "admin", nil)
	return r.as(http.MethodPost, "/v1/train/policies/enso/promote", "admin", true, map[string]any{"version": v, "base": base, "next": textOf(got["next"]), "note": note})
}

// rollback reviews the registry's rollback, straight to the catalog's own routes
// with origin, and makes it over base, as the page does.
func (r *rig) rollback(base int, origin bool, note string) (int, map[string]any) {
	r.t.Helper()
	b := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso", "admin", nil)
	next := ""
	for _, x := range anys(b["rollbacks"]) {
		// With one version promoted, the rollback before is the catalog's own routes,
		// and the registry lays it out once.
		if rb := x.(map[string]any); rb["origin"] == origin || (origin && rb["to"] == float64(0)) {
			next = textOf(rb["next"])
		}
	}
	return r.as(http.MethodPost, "/v1/train/policies/enso/rollback", "admin", true, map[string]any{"base": base, "origin": origin, "next": next, "note": note})
}

func anys(v any) []any {
	xs, _ := v.([]any)
	return xs
}

func policyRig(t *testing.T) (*rig, *fakeRouter) {
	t.Helper()
	r := mount(t, "")
	f := newFakeRouter()
	r.st.router = f
	return r, f
}

// The registry is the platform's: a SuperAdmin reads and changes it, and an org
// admin, a member and an anonymous caller are refused every address of it alike.
// It holds the one policy, enso.
func TestThePolicyRegistryIsASuperAdminsAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, _ := policyRig(t)
		for _, c := range []struct{ method, path string }{
			{http.MethodGet, "/v1/train/policies/enso"},
			{http.MethodPost, "/v1/train/policies/enso"},
			{http.MethodGet, "/v1/train/policies/enso/versions/1"},
			{http.MethodGet, "/v1/train/policies/enso/diff"},
			{http.MethodPost, "/v1/train/policies/enso/promote"},
			{http.MethodPost, "/v1/train/policies/enso/rollback"},
		} {
			for _, org := range []string{"acme", "admin", ""} {
				if st, out := r.as(c.method, c.path, org, false, map[string]any{"document": tuned(t)}); st != http.StatusForbidden {
					t.Errorf("%s %s as %q, not a SuperAdmin: %d %v", c.method, c.path, org, st, out)
				}
			}
		}
		if st, _ := r.as(http.MethodGet, "/v1/train/policies/zen", "admin", true, nil); st != http.StatusNotFound {
			t.Errorf("a policy the registry does not hold: %d", st)
		}
		if st, _ := r.as(http.MethodGet, "/v1/train/policies/enso", "admin", true, nil); st != http.StatusOK {
			t.Errorf("a SuperAdmin's read: %d", st)
		}
	})
}

// A draft is pinned by the sha256 of its canonical bytes: the same file written
// otherwise is the same version, and a file that is not a policy is refused with
// every reason.
func TestADraftIsPinnedByItsCanonicalBytes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, _ := policyRig(t)
		out := r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t), "note": "tuned on the October answers"})
		raw, _ := json.Marshal(tuned(t))
		canon, err := canonical(raw)
		if err != nil {
			t.Fatal(err)
		}
		if out["version"] != float64(1) || out["sha256"] != sum(canon) || out["state"] != "draft" || out["servable"] != true || out["note"] != "tuned on the October answers" {
			t.Fatalf("draft: %v", out)
		}
		pretty, _ := json.MarshalIndent(tuned(t), "", "    ")
		st, again := r.as(http.MethodPost, "/v1/train/policies/enso", "admin", true, map[string]any{"document": json.RawMessage(pretty)})
		if st != http.StatusConflict || !strings.Contains(problem(again), "version 1") {
			t.Fatalf("the same file again: %d %v", st, again)
		}
		bad := tuned(t)
		bad["schema"] = "enso.policy/0"
		delete(bad, "benches")
		st, refused := r.as(http.MethodPost, "/v1/train/policies/enso", "admin", true, map[string]any{"document": bad})
		if text := problem(refused); st != http.StatusBadRequest || !strings.Contains(text, "schema") || !strings.Contains(text, "no benchmark") {
			t.Fatalf("not a policy: %d %v", st, refused)
		}
		got, _, err := r.trail.Query(context.Background(), audit.Filter{Action: "train.policy.create"})
		if err != nil || len(got) != 1 {
			t.Fatalf("creates on the trail: %d %v", len(got), err)
		}
	})
}

// A policy production cannot serve is kept as a draft and says why, and it is
// never promoted: a tier asking several arms, a tier named for no Enso model, an
// arm naming no upstream.
func TestACascadeIsNotServable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		out := r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": cascaded(t)})
		why := strings.Join(strs(out["reasons"]), "\n")
		for _, want := range []string{"tier enso on gpqa_diamond asks 3 models in 2 stages", "arm deepseek-v4-pro names no upstream"} {
			if !strings.Contains(why, want) {
				t.Errorf("reasons lack %q:\n%s", want, why)
			}
		}
		if out["servable"] != false {
			t.Fatalf("servable: %v", out)
		}
		st, refused := r.as(http.MethodPost, "/v1/train/policies/enso/promote", "admin", true, map[string]any{"version": 1, "base": 0})
		if st != http.StatusConflict || !strings.Contains(problem(refused), "one model a request") {
			t.Fatalf("promoting a cascade: %d %v", st, refused)
		}
		if f.cat != ensoServed || f.version != 0 {
			t.Fatal("a refused promotion changed the catalog")
		}
		// A tier named for no Enso model, and one whose arm is not on its route.
		other := doc(t, map[string]string{"turbo": "sonnet-5.5", "flash": "haiku-5.5"}, map[string]string{
			"sonnet-5.5": "anthropic/claude-sonnet-5.5", "haiku-5.5": "anthropic/claude-haiku-5.5"})
		out = r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": other})
		why = strings.Join(strs(out["reasons"]), "\n")
		if !strings.Contains(why, "serves no enso-turbo") || !strings.Contains(why, "anthropic/claude-haiku-5.5 (arm haiku-5.5) is not on enso-flash's route") {
			t.Fatalf("reasons:\n%s", why)
		}
	})
}

// Promoting leads each tier's route with the version's arm and keeps the rest
// behind it in the order they had before any policy; a later version serves its
// own tiers and puts back the ones only an earlier version set; rollback serves the
// version before, and past the first, the routes before any policy. Each is a
// catalog version in the ai process and an event here, on the trail first.
func TestPromoteLeadsEachTierAndRollbackPutsItBack(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})

		v := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso/versions/1", "admin", nil)
		if v["servable"] != true || v["diff"] == "" || v["document"] == nil {
			t.Fatalf("version 1: servable %v diff %q", v["servable"], v["diff"])
		}
		if f.version != 0 {
			t.Fatal("reading a version applied something")
		}

		if st, out := r.promote(1, 3, ""); st != http.StatusConflict {
			t.Fatalf("a promotion made from a stale base: %d %v", st, out)
		}
		if st, out := r.as(http.MethodPost, "/v1/train/policies/enso/promote", "admin", true, map[string]any{"version": 1, "base": 0, "next": "stale"}); st != http.StatusConflict || !strings.Contains(problem(out), "review it again") {
			t.Fatalf("a promotion of a catalog nobody reviewed: %d %v", st, out)
		}
		st, board := r.promote(1, 0, "held out 90%")
		if st != http.StatusOK {
			t.Fatalf("promote: %d %v", st, board)
		}
		if board["promoted"] != float64(1) {
			t.Fatalf("promoted: %v", board["promoted"])
		}
		want := map[string][]string{
			"enso-flash": {"openai/gpt-6-luna-pro", "openai/gpt-6-luna"},
			"enso-pro":   {"anthropic/claude-sonnet-5.5", "openai/gpt-6.1-sol", "google/gemini-4-argon"},
			"enso-ultra": {"anthropic/claude-opus-5.5", "anthropic/claude-sonnet-5.5", "google/gemini-4-argon"},
		}
		for m, w := range want {
			if got := f.leads(t, m); !slices.Equal(got, w) {
				t.Errorf("%s after promoting version 1: %v, want %v", m, got, w)
			}
		}
		if got := f.leads(t, "enso-free"); !slices.Equal(got, []string{"nvidia/nemotron-3-super-120b-a12b:free"}) {
			t.Errorf("a model no tier names moved: %v", got)
		}
		for _, tier := range board["serving"].(map[string]any)["tiers"].([]any) {
			if tier.(map[string]any)["lead"] != float64(1) {
				t.Errorf("serving: %v", tier)
			}
		}
		if len(f.notes) != 1 || !strings.Contains(f.notes[0], "Enso policy version 1 (sha256:") || !strings.Contains(f.notes[0], "held out 90%") {
			t.Fatalf("the catalog version's note: %v", f.notes)
		}

		// Version 2 sets flash alone: flash leads with Luna again behind its own arm,
		// and pro and ultra go back to the routes they had before any policy.
		two := doc(t, map[string]string{"flash": "gpt-6-luna"}, map[string]string{"gpt-6-luna": "openai/gpt-6-luna"})
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": two})
		if st, out := r.promote(2, 0, ""); st != http.StatusConflict || !strings.Contains(problem(out), "version 1 serves now") {
			t.Fatalf("a promotion made while another serves: %d %v", st, out)
		}
		if st, out := r.promote(2, 1, ""); st != http.StatusOK {
			t.Fatalf("promote 2: %d %v", st, out)
		}
		if got := f.leads(t, "enso-flash"); !slices.Equal(got, []string{"openai/gpt-6-luna", "openai/gpt-6-luna-pro"}) {
			t.Errorf("flash under version 2: %v", got)
		}
		if got := f.leads(t, "enso-pro"); !slices.Equal(got, []string{"openai/gpt-6.1-sol", "google/gemini-4-argon", "anthropic/claude-sonnet-5.5"}) {
			t.Errorf("pro under version 2, which sets no pro: %v", got)
		}

		st, board = r.rollback(2, false, "back")
		if st != http.StatusOK || board["promoted"] != float64(1) {
			t.Fatalf("rolled back to %v", board["promoted"])
		}
		for m, w := range want {
			if got := f.leads(t, m); !slices.Equal(got, w) {
				t.Errorf("%s rolled back to version 1: %v, want %v", m, got, w)
			}
		}
		st, board = r.rollback(1, false, "")
		if st != http.StatusOK || board["promoted"] != float64(0) {
			t.Fatalf("rolled back past the first to %v", board["promoted"])
		}
		served, _ := readEnso([]byte(ensoServed))
		for _, m := range []string{"enso-flash", "enso-pro", "enso-ultra"} {
			if got := f.leads(t, m); !slices.Equal(got, served.upstreams(m)) {
				t.Errorf("%s past the first promotion: %v, want the catalog's own %v", m, got, served.upstreams(m))
			}
		}
		if st, out := r.rollback(0, false, ""); st != http.StatusConflict || !strings.Contains(problem(out), "no version serves") {
			t.Fatalf("a rollback with nothing promoted: %d %v", st, out)
		}

		hist := board["history"].([]any)
		if len(hist) != 4 {
			t.Fatalf("history: %d events", len(hist))
		}
		last := hist[0].(map[string]any)
		if last["action"] != "rollback" || last["version"] != float64(0) || last["prior"] != float64(1) || last["routing"] != float64(4) {
			t.Fatalf("the newest event: %v", last)
		}
		for _, a := range []string{"train.policy.create", "train.policy.promote", "train.policy.rollback"} {
			got, _, _ := r.trail.Query(context.Background(), audit.Filter{Action: a})
			if len(got) != 2 {
				t.Errorf("%s on the trail: %d", a, len(got))
			}
		}
		states := map[float64]string{}
		for _, v := range board["versions"].([]any) {
			states[v.(map[string]any)["version"].(float64)] = v.(map[string]any)["state"].(string)
		}
		if states[1] != "draft" || states[2] != "draft" {
			t.Fatalf("states after rolling back past both: %v", states)
		}
	})
}

// Without an audit trail nothing in the registry changes: no draft, no promotion.
func TestNoTrailNoPolicyChange(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})
		r.st.audit = nil
		if st, _ := r.promote(1, 0, ""); st != http.StatusServiceUnavailable {
			t.Fatalf("an unaudited promotion: %d", st)
		}
		if st, _ := r.as(http.MethodPost, "/v1/train/policies/enso", "admin", true, map[string]any{"document": cascaded(t)}); st != http.StatusServiceUnavailable {
			t.Fatalf("an unaudited draft: %d", st)
		}
		if f.version != 0 || f.cat != ensoServed {
			t.Fatal("an unaudited act changed the catalog")
		}
	})
}

// The price guard's verdict is the version's: one whose routes would leave a tier
// over its price reads unservable with the guard's reason, and promoting it is
// refused with nothing changed, the refusal on the trail beside the attempt.
func TestTheGuardsRefusalIsTheVersions(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		f.refuse = func(cat string) string {
			c, _ := readEnso([]byte(cat))
			if c.upstreams("enso-ultra")[0] == "anthropic/claude-opus-5.5" {
				return "anthropic/claude-opus-5.5 through openrouter costs more than enso-ultra bills in input, output"
			}
			return ""
		}
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})
		v := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso/versions/1", "admin", nil)
		if v["servable"] != false || !strings.Contains(strings.Join(strs(v["reasons"]), " "), "the price guard would refuse it") {
			t.Fatalf("version 1: %v %v", v["servable"], v["reasons"])
		}
		st, out := r.promote(1, 0, "")
		if st != http.StatusConflict || !strings.Contains(problem(out), "costs more than enso-ultra bills") {
			t.Fatalf("promoting over the price: %d %v", st, out)
		}
		if f.version != 0 {
			t.Fatal("the guard's refusal changed the catalog")
		}
		board := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso", "admin", nil)
		if board["promoted"] != float64(0) || len(board["history"].([]any)) != 0 {
			t.Fatalf("after the refusal: %v %v", board["promoted"], board["history"])
		}
		if got, _, _ := r.trail.Query(context.Background(), audit.Filter{Action: "train.policy.promote"}); len(got) != 0 {
			t.Fatalf("a promotion refused before it was sent is on the trail: %d", len(got))
		}
		// One the dry run let through and the apply refused is on the trail twice: the
		// attempt, then its refusal.
		f.refuse = func(string) string { return "" }
		f.late = "anthropic/claude-opus-5.5 through openrouter costs more than enso-ultra bills in output"
		st, out = r.promote(1, 0, "")
		if st != http.StatusConflict || !strings.Contains(problem(out), "costs more than enso-ultra bills") || f.version != 0 {
			t.Fatalf("refused at the apply: %d %v", st, out)
		}
		got, _, _ := r.trail.Query(context.Background(), audit.Filter{Action: "train.policy.promote"})
		results := []string{}
		for _, g := range got {
			results = append(results, g.Outcome.Result)
		}
		slices.Sort(results)
		if !slices.Equal(results, []string{"error", "success"}) {
			t.Fatalf("the trail: %v", results)
		}
	})
}

// A catalog changed between the read and the apply refuses the promotion, and the
// registry keeps serving what it served.
func TestACatalogThatMovedRefusesThePromotion(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})
		v := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso/versions/1", "admin", nil)
		f.moved = true
		st, out := r.as(http.MethodPost, "/v1/train/policies/enso/promote", "admin", true, map[string]any{"version": 1, "base": 0, "next": textOf(v["next"])})
		if st != http.StatusConflict || !strings.Contains(problem(out), "changed since this was read") {
			t.Fatalf("a catalog that moved: %d %v", st, out)
		}
		f.moved = false
		if board := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso", "admin", nil); board["promoted"] != float64(0) {
			t.Fatalf("promoted after a refused apply: %v", board["promoted"])
		}
	})
}

// A catalog the ai process cannot read is said, never served as an empty one:
// every version reads unservable for it, and nothing is promoted.
func TestAnUnreadableCatalogIsSaid(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})
		f.down = true
		board := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso", "admin", nil)
		serving := board["serving"].(map[string]any)
		if !strings.Contains(textOf(serving["error"]), "could not be read") || len(serving["tiers"].([]any)) != 0 {
			t.Fatalf("serving: %v", serving)
		}
		v := board["versions"].([]any)[0].(map[string]any)
		if v["servable"] != false || !strings.Contains(strings.Join(strs(v["reasons"]), " "), "could not be read") {
			t.Fatalf("a version over an unreadable catalog: %v", v)
		}
		if st, _ := r.as(http.MethodPost, "/v1/train/policies/enso/promote", "admin", true, map[string]any{"version": 1, "base": 0, "next": "x"}); st != http.StatusServiceUnavailable {
			t.Fatalf("promoting over an unreadable catalog: %d", st)
		}
	})
}

// A diff says what changed between two versions: each tier's arms, budget and
// scores on new questions and the sealed set, the pool and the arms known.
func TestADiffSaysWhatChanged(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, _ := policyRig(t)
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})
		two := doc(t, map[string]string{"flash": "gpt-6-luna"}, map[string]string{"gpt-6-luna": "openai/gpt-6-luna"})
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": two})
		d := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso/diff?from=1&to=2", "admin", nil)
		if d["from"] != float64(1) || d["to"] != float64(2) {
			t.Fatalf("diff of %v to %v", d["from"], d["to"])
		}
		models := d["models"].(map[string]any)
		if !slices.Equal(strs(models["added"]), []string{"gpt-6-luna"}) || len(strs(models["removed"])) != 3 {
			t.Fatalf("models: %v", models)
		}
		bench := d["benches"].([]any)[0].(map[string]any)
		tiers := map[string]map[string]any{}
		for _, x := range bench["tiers"].([]any) {
			tiers[x.(map[string]any)["tier"].(string)] = x.(map[string]any)
		}
		flash := tiers["flash"]["arms"].(map[string]any)
		if flash["from"].([]any)[0].([]any)[0] != "gpt-6-luna-pro" || flash["to"].([]any)[0].([]any)[0] != "gpt-6-luna" {
			t.Fatalf("flash arms: %v", flash)
		}
		if tiers["pro"]["heldout"].(map[string]any)["to"] != nil {
			t.Fatalf("a tier version 2 does not have scores nothing: %v", tiers["pro"]["heldout"])
		}
		if st, _ := r.as(http.MethodGet, "/v1/train/policies/enso/diff?from=1&to=9", "admin", true, nil); st != http.StatusNotFound {
			t.Fatalf("a diff to a version that is not there: %d", st)
		}
	})
}

// A tier is served by one model a request: planOf reads one arm a tier, the same
// on every benchmark, naming its upstream, and says why of every other shape.
func TestAPlanIsOneArmATier(t *testing.T) {
	d := tuned(t)
	benches := d["benches"].(map[string]any)
	lcb := map[string]any{}
	raw, _ := json.Marshal(benches["gpqa_diamond"])
	_ = json.Unmarshal(raw, &lcb)
	lcb["tiers"].(map[string]any)["pro"].(map[string]any)["cascade"] = map[string]any{"stages": []any{map[string]any{"arms": []string{"opus-5.5"}}}, "agree": 1}
	benches["livecodebench"] = lcb
	raw, _ = json.Marshal(d)
	_, _, pd, err := readPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	p := planOf(pd)
	if _, ok := p.Tiers["pro"]; ok || !strings.Contains(strings.Join(p.Reasons, " "), "tier pro: gpqa_diamond chooses sonnet-5.5, livecodebench chooses opus-5.5") {
		t.Fatalf("benchmarks that disagree: %+v", p)
	}
	if p.Tiers["flash"].Upstream != "openai/gpt-6-luna-pro" || p.Tiers["ultra"].Arm != "opus-5.5" {
		t.Fatalf("tiers: %+v", p.Tiers)
	}
}

// Canonical bytes are the file's meaning, not its spelling: key order, space and
// how a number is written (1.10 or 1.1, 1e-5 or 0.00001, 1e6 or 1000000) do not
// change them, so a file a browser read and wrote again is the same version.
func TestCanonicalBytesAreTheMeaning(t *testing.T) {
	a, err := canonical([]byte(`{"b": 1.10, "c": 1e-5, "d": 1000000, "a": {"y": [1, 2.0], "x": "é<"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonical([]byte("{\n  \"a\": {\"x\": \"é<\", \"y\": [1,2]},\n  \"b\": 1.1, \"c\": 0.00001, \"d\": 1e6\n}"))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || string(a) != `{"a":{"x":"é<","y":[1,2]},"b":1.1,"c":1e-05,"d":1000000}` {
		t.Fatalf("%s\n%s", a, b)
	}
	if _, err := canonical([]byte(`{} {}`)); err == nil {
		t.Fatal("two documents read as one")
	}
}

func strs(v any) []string {
	var out []string
	xs, _ := v.([]any)
	for _, x := range xs {
		out = append(out, textOf(x))
	}
	return out
}

func textOf(v any) string {
	s, _ := v.(string)
	return s
}

// problem is a refusal's sentence, whichever field the wire carried it in.
func problem(m map[string]any) string {
	return strings.Join([]string{textOf(m["detail"]), textOf(m["error"]), textOf(m["message"]), textOf(m["msg"])}, " ")
}

// edit changes the fake's catalog around the registry, as a Model routing edit or
// a new release of the catalog file does.
func (f *fakeRouter) edit(t *testing.T, change func(models map[string]any)) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var c map[string]any
	if err := json.Unmarshal([]byte(f.cat), &c); err != nil {
		t.Fatal(err)
	}
	change(c["models"].(map[string]any))
	b, _ := json.Marshal(c)
	f.cat = string(b)
}

func routeOf(models map[string]any, sku string) []any {
	return models[sku].(map[string]any)["route"].([]any)
}

// A rollback moves order and nothing else: a rung removed or repriced since the
// first promotion stays as the catalog has it, and the route goes back to the
// order it had before any policy.
func TestARollbackMovesOrderAndNothingElse(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})
		if st, out := r.promote(1, 0, ""); st != http.StatusOK {
			t.Fatalf("promote: %d %v", st, out)
		}
		// Argon leaves enso-pro and Sol is repriced, in Model routing.
		f.edit(t, func(m map[string]any) {
			var kept []any
			for _, x := range routeOf(m, "enso-pro") {
				rung := x.(map[string]any)
				if rung["upstream"] == "google/gemini-4-argon" {
					continue
				}
				if rung["upstream"] == "openai/gpt-6.1-sol" {
					rung["cost"].(map[string]any)["in"] = "3.00"
				}
				kept = append(kept, rung)
			}
			m["enso-pro"].(map[string]any)["route"] = kept
		})
		if st, out := r.rollback(1, false, ""); st != http.StatusOK {
			t.Fatalf("rollback: %d %v", st, out)
		}
		if got := f.leads(t, "enso-pro"); !slices.Equal(got, []string{"openai/gpt-6.1-sol", "anthropic/claude-sonnet-5.5"}) {
			t.Fatalf("enso-pro after the rollback: %v", got)
		}
		f.edit(t, func(m map[string]any) {
			sol := routeOf(m, "enso-pro")[0].(map[string]any)
			if in := sol["cost"].(map[string]any)["in"]; in != "3.00" {
				t.Fatalf("the rollback put back Sol's old price: %v", in)
			}
		})
	})
}

// A promoted version a new release of the catalog stands over no longer leads, and
// can be promoted again; one that leads cannot.
func TestAPromotedVersionThatNoLongerLeadsIsPromotedAgain(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})
		if st, out := r.promote(1, 0, ""); st != http.StatusOK {
			t.Fatalf("promote: %d %v", st, out)
		}
		if st, out := r.promote(1, 1, ""); st != http.StatusConflict || !strings.Contains(problem(out), "already serves") {
			t.Fatalf("promoting what leads: %d %v", st, out)
		}
		f.mu.Lock()
		f.cat = ensoServed // the engine restarted onto a new release of its file
		f.mu.Unlock()
		v := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso/versions/1", "admin", nil)
		if v["state"] != "promoted" || v["leads"] != false || textOf(v["next"]) == "" {
			t.Fatalf("version 1 over a new release: %v %v %v", v["state"], v["leads"], v["next"])
		}
		if st, out := r.promote(1, 1, "again"); st != http.StatusOK || out["promoted"] != float64(1) {
			t.Fatalf("promoting again: %d %v", st, out)
		}
		if got := f.leads(t, "enso-ultra"); got[0] != "anthropic/claude-opus-5.5" {
			t.Fatalf("enso-ultra: %v", got)
		}
		board := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso", "admin", nil)
		if n := len(anys(board["history"])); n != 2 {
			t.Fatalf("history: %d", n)
		}
		if st, out := r.rollback(1, false, ""); st != http.StatusOK || out["promoted"] != float64(0) {
			t.Fatalf("one rollback past a version promoted twice: %d %v", st, out["promoted"])
		}
	})
}

// When the version beneath can no longer serve, a rollback to it is refused and
// says so, and a rollback straight to the catalog's own routes is the way out.
func TestARollbackPastAVersionThatCannotServe(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})
		two := doc(t, map[string]string{"flash": "gpt-6-luna"}, map[string]string{"gpt-6-luna": "openai/gpt-6-luna"})
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": two})
		r.promote(1, 0, "")
		if st, out := r.promote(2, 1, ""); st != http.StatusOK {
			t.Fatalf("promote 2: %d %v", st, out)
		}
		// Opus leaves enso-ultra: version 1 cannot serve any more.
		f.edit(t, func(m map[string]any) {
			var kept []any
			for _, x := range routeOf(m, "enso-ultra") {
				if x.(map[string]any)["upstream"] != "anthropic/claude-opus-5.5" {
					kept = append(kept, x)
				}
			}
			m["enso-ultra"].(map[string]any)["route"] = kept
		})
		board := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso", "admin", nil)
		rbs := anys(board["rollbacks"])
		if len(rbs) != 2 || len(strs(rbs[0].(map[string]any)["reasons"])) == 0 || rbs[1].(map[string]any)["origin"] != true {
			t.Fatalf("rollbacks: %v", rbs)
		}
		if st, out := r.rollback(2, false, ""); st != http.StatusConflict || !strings.Contains(problem(out), "roll back to the catalog's own routes instead") {
			t.Fatalf("a rollback to a version that cannot serve: %d %v", st, out)
		}
		if st, out := r.rollback(2, true, "back to the catalog"); st != http.StatusOK || out["promoted"] != float64(0) {
			t.Fatalf("a rollback to the catalog's own routes: %d %v", st, out["promoted"])
		}
		if got := f.leads(t, "enso-flash"); got[0] != "openai/gpt-6-luna" {
			t.Fatalf("enso-flash: %v", got)
		}
	})
}

// A catalog the engine took and ai did not record is said for what it is, not as
// a refusal, and the registry records nothing it cannot show.
func TestAnApplyAiDidNotRecordIsSaid(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})
		f.lost = true
		st, out := r.promote(1, 0, "")
		if st != http.StatusInternalServerError || !strings.Contains(problem(out), "took it and ai did not record") {
			t.Fatalf("an apply ai did not record: %d %v", st, out)
		}
		board := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso", "admin", nil)
		if board["promoted"] != float64(0) {
			t.Fatalf("promoted: %v", board["promoted"])
		}
		for _, x := range anys(board["serving"].(map[string]any)["tiers"]) {
			if tier := x.(map[string]any); tier["model"] == "enso-ultra" && strs(tier["route"])[0] != "anthropic/claude-opus-5.5" {
				t.Fatalf("serving shows what the engine serves: %v", tier["route"])
			}
		}
	})
}

// A policy routes the paid tiers alone: a tier named for the free model is not one.
func TestAFreeModelIsNoTier(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, _ := policyRig(t)
		free := doc(t, map[string]string{"free": "nemotron"}, map[string]string{"nemotron": "nvidia/nemotron-3-super-120b-a12b:free"})
		out := r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": free})
		if why := strings.Join(strs(out["reasons"]), " "); !strings.Contains(why, "enso-free is not a paid tier") {
			t.Fatalf("reasons: %s", why)
		}
		d := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso/diff", "admin", nil)
		if d["from"] != float64(0) || d["to"] != float64(1) {
			t.Fatalf("the diff of the only version: %v", d)
		}
	})
}

// A release of the catalog that drops a model a policy set leaves the registry
// working: the board reads, and a rollback serves what remains.
func TestADroppedModelLeavesTheRegistryWorking(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})
		if st, out := r.promote(1, 0, ""); st != http.StatusOK {
			t.Fatalf("promote: %d %v", st, out)
		}
		f.edit(t, func(m map[string]any) { delete(m, "enso-pro") })
		board := r.must(http.StatusOK, http.MethodGet, "/v1/train/policies/enso", "admin", nil)
		if v := anys(board["versions"])[0].(map[string]any); v["servable"] != false {
			t.Fatalf("a version setting a model the catalog dropped: %v", v)
		}
		if st, out := r.rollback(1, true, "the catalog changed"); st != http.StatusOK || out["promoted"] != float64(0) {
			t.Fatalf("rollback: %d %v", st, out)
		}
		if got := f.leads(t, "enso-ultra"); got[0] != "anthropic/claude-sonnet-5.5" {
			t.Fatalf("enso-ultra: %v", got)
		}
	})
}

// What an attempt that did not land wrote down is not the order before any policy:
// with nothing promoted, a promotion takes that order from what serves.
func TestAnAttemptThatDidNotLandLeavesNoOrder(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f := policyRig(t)
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": tuned(t)})
		f.late = "refused"
		if st, _ := r.promote(1, 0, ""); st != http.StatusConflict {
			t.Fatalf("a refused promotion: %d", st)
		}
		f.late = ""
		// Model routing reorders enso-pro meanwhile.
		f.edit(t, func(m map[string]any) {
			rs := routeOf(m, "enso-pro")
			rs[0], rs[2] = rs[2], rs[0]
		})
		two := doc(t, map[string]string{"flash": "gpt-6-luna-pro"}, map[string]string{"gpt-6-luna-pro": "openai/gpt-6-luna-pro"})
		r.must(http.StatusCreated, http.MethodPost, "/v1/train/policies/enso", "admin", map[string]any{"document": two})
		if st, out := r.promote(2, 0, ""); st != http.StatusOK {
			t.Fatalf("promote 2: %d %v", st, out)
		}
		if got := f.leads(t, "enso-pro"); got[0] != "anthropic/claude-sonnet-5.5" {
			t.Fatalf("the reorder made in Model routing was undone: %v", got)
		}
	})
}
