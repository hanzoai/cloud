package flags

import (
	"crypto/rand"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/cek"
)

// The cek data plane fail-closes without a master key on encryption-capable
// builds; tests supply one process-wide (SetMasterKey is once-only).
var cekOnce sync.Once

func testMasterKey(t *testing.T) {
	t.Helper()
	cekOnce.Do(func() {
		k := make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			t.Fatalf("rng: %v", err)
		}
		cek.SetMasterKey(k)
	})
}

// newTestClient builds a Client over a temp-dir store tree and installs it as the
// process seam, restoring the prior seam on cleanup.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	testMasterKey(t)
	prev := mounted
	c := &Client{
		stores:     cloud.NewOrgStore[*Store](t.TempDir(), "flags", openStore),
		distinctID: "test",
		ttl:        time.Minute,
	}
	mounted = c
	t.Cleanup(func() {
		_ = c.stores.CloseAll()
		mounted = prev
	})
	return c
}

func TestEnvFallbackAndDefault(t *testing.T) {
	// No engine/store configured -> env fallback, then literal default.
	prev := mounted
	mounted = &Client{} // not configured
	t.Cleanup(func() { mounted = prev })

	// waitlist_open default is "true" (no env set).
	t.Setenv("WAITLIST_OPEN", "")
	if !Bool("waitlist_open") {
		t.Fatalf("waitlist_open default should be true")
	}
	// env override beats the literal default.
	t.Setenv("WAITLIST_OPEN", "false")
	if Bool("waitlist_open") {
		t.Fatalf("waitlist_open env=false should win")
	}
	// int default.
	if Int("waitlist_access_capacity") != 0 {
		t.Fatalf("capacity default should be 0")
	}
	t.Setenv("WAITLIST_ACCESS_CAPACITY", "250")
	if Int("waitlist_access_capacity") != 250 {
		t.Fatalf("capacity env should be 250, got %d", Int("waitlist_access_capacity"))
	}
	// network id read-only default.
	if Int("network_id_localnet") != 1337 {
		t.Fatalf("localnet id default should be 1337")
	}
	// unknown key is safe.
	if Bool("nope") || Int("nope") != 0 || String("nope") != "" {
		t.Fatalf("unknown key must be zero-valued")
	}
}

func TestStoreBackedSwitchOverridesEnv(t *testing.T) {
	if !engineAvailable {
		t.Skip("native engine not built (cgo off)")
	}
	newTestClient(t)
	t.Setenv("WAITLIST_OPEN", "true")

	// Cockpit flips the switch OFF: active=false evaluates to false and WINS.
	if err := SetPlatformSwitch("waitlist_open", json.RawMessage(`{"active": false}`), "z@hanzo.ai"); err != nil {
		t.Fatalf("SetPlatformSwitch: %v", err)
	}
	if Bool("waitlist_open") {
		t.Fatalf("stored flag false must override env true")
	}

	// Flip back ON — SetPlatformSwitch invalidates, applies immediately.
	if err := SetPlatformSwitch("waitlist_open", json.RawMessage(`{"active": true}`), "z@hanzo.ai"); err != nil {
		t.Fatalf("SetPlatformSwitch: %v", err)
	}
	if !Bool("waitlist_open") {
		t.Fatalf("stored flag true must evaluate true")
	}
}

func TestIntSwitchRidesThePayload(t *testing.T) {
	if !engineAvailable {
		t.Skip("native engine not built (cgo off)")
	}
	newTestClient(t)
	def := `{
		"active": true,
		"filters": {
			"groups": [{"properties": [], "rollout_percentage": 100}],
			"payloads": {"true": 42}
		}
	}`
	if err := SetPlatformSwitch("waitlist_access_capacity", json.RawMessage(def), "z@hanzo.ai"); err != nil {
		t.Fatalf("SetPlatformSwitch: %v", err)
	}
	if got := Int("waitlist_access_capacity"); got != 42 {
		t.Fatalf("want 42 from payload, got %d", got)
	}
}

func TestProjectEvaluationRolloutAndVariants(t *testing.T) {
	if !engineAvailable {
		t.Skip("native engine not built (cgo off)")
	}
	c := newTestClient(t)
	st, err := c.stores.For("acme", "web")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	def := `{
		"active": true,
		"filters": {
			"groups": [
				{"properties": [{"key": "plan", "value": "pro", "operator": "exact", "type": "person"}],
				 "rollout_percentage": 100}
			],
			"multivariate": {"variants": [
				{"key": "control", "rollout_percentage": 50},
				{"key": "treatment", "rollout_percentage": 50}
			]},
			"payloads": {"treatment": {"cta": "buy"}}
		}
	}`
	if err := st.Upsert("checkout-exp", json.RawMessage(def), "t"); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	eval := func(distinct string, props string) map[string]json.RawMessage {
		t.Helper()
		ctx := []byte(`{"distinct_id":"` + distinct + `","person_properties":` + props + `}`)
		res, err := c.evaluateProject("acme", "web", ctx)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		var out struct {
			FeatureFlags map[string]json.RawMessage `json:"featureFlags"`
		}
		if err := json.Unmarshal(res, &out); err != nil {
			t.Fatalf("parse: %v", err)
		}
		return out.FeatureFlags
	}

	// pro users get a stable variant; the same user always gets the same one
	a := eval("user-1", `{"plan":"pro"}`)
	b := eval("user-1", `{"plan":"pro"}`)
	if string(a["checkout-exp"]) != string(b["checkout-exp"]) {
		t.Fatalf("variant not sticky: %s vs %s", a["checkout-exp"], b["checkout-exp"])
	}
	v := string(a["checkout-exp"])
	if v != `"control"` && v != `"treatment"` {
		t.Fatalf("unexpected variant %s", v)
	}
	// non-pro users are off
	if got := string(eval("user-1", `{"plan":"free"}`)["checkout-exp"]); got != "false" {
		t.Fatalf("free plan must be off, got %s", got)
	}
}

func TestStoreCRUDAndActivity(t *testing.T) {
	c := newTestClient(t)
	st, err := c.stores.For("acme", "")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.Upsert("k1", json.RawMessage(`{"active": true}`), "alice"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.Upsert("k1", json.RawMessage(`{"active": false}`), "bob"); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	row, found, err := st.Get("k1")
	if err != nil || !found {
		t.Fatalf("get: %v found=%v", err, found)
	}
	if row.Version != 2 || row.UpdatedBy != "bob" {
		t.Fatalf("version/actor wrong: %+v", row)
	}
	rows, err := st.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %v n=%d", err, len(rows))
	}
	deleted, err := st.Delete("k1", "carol")
	if err != nil || !deleted {
		t.Fatalf("delete: %v deleted=%v", err, deleted)
	}
	acts, err := st.Activity(10)
	if err != nil || len(acts) != 3 {
		t.Fatalf("activity: %v n=%d", err, len(acts))
	}
	if acts[0].Action != "deleted" || acts[0].Actor != "carol" {
		t.Fatalf("latest activity wrong: %+v", acts[0])
	}
}

func TestBoard(t *testing.T) {
	newTestClient(t)
	b := Board()
	if b.Engine != "hanzo-flags" {
		t.Fatalf("engine name: %s", b.Engine)
	}
	if len(b.Switches) == 0 {
		t.Fatalf("board must surface the registered switches")
	}
	seen := map[string]bool{}
	for _, s := range b.Switches {
		seen[s.Key] = true
	}
	for _, want := range []string{"waitlist_open", "public_signup", "gateway_rate_limit_rpm"} {
		if !seen[want] {
			t.Fatalf("board missing %s", want)
		}
	}
}

func TestParsers(t *testing.T) {
	if b, ok := asBool(json.RawMessage(`true`)); !ok || !b {
		t.Fatal("asBool true")
	}
	if b, ok := asBool(json.RawMessage(`"variant-a"`)); !ok || !b {
		t.Fatal("variant string should read as enabled")
	}
	if _, ok := asBool(json.RawMessage(`null`)); ok {
		t.Fatal("null is absent")
	}
	if n, ok := asInt(json.RawMessage(`42`)); !ok || n != 42 {
		t.Fatal("asInt 42")
	}
	if n, ok := asInt(json.RawMessage(`"17"`)); !ok || n != 17 {
		t.Fatal("asInt string")
	}
	if s, ok := asString(json.RawMessage(`"hi"`)); !ok || s != "hi" {
		t.Fatal("asString")
	}
}
