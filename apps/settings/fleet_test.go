package settings

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fleetService is the settings service with a store and an audit trail, as Use
// builds it.
func fleetService(t *testing.T) (settingsOps, *audit.Recorder) {
	t.Helper()
	store, err := openSettingsStore(t.TempDir())
	if err != nil {
		t.Fatalf("openSettingsStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	return settingsOps{s: &service{store: store, log: luxlog.New("test"), trail: trail}}, trail
}

func statusOf(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// THE WRITE HALF OF THE PLATFORM'S KNOBS. A SuperAdmin caller replaces the
// platform org's document for a product; the read answers it back; the change is
// on the trail with what it replaced.
func TestFleetSetWritesThePlatformRowAndAuditsIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		o, trail := fleetService(t)
		super := zip.WithCaller(context.Background(), zip.Caller{Org: "admin", Owner: "admin", User: "z", Email: "z@hanzo.ai", Admin: true})

		first := `{"enabled":["bing","ddg"]}`
		if _, err := o.fleetSet(super, &client.FleetConfig{Product: "websearch", Config: first}); err != nil {
			t.Fatalf("first write: %v", err)
		}
		second := `{"enabled":["mojeek"]}`
		out, err := o.fleetSet(super, &client.FleetConfig{Product: "websearch", Config: second})
		if err != nil {
			t.Fatalf("second write: %v", err)
		}
		if out.Config != second {
			t.Fatalf("answered %s, want the stored document %s", out.Config, second)
		}
		got, err := o.fleetConfig(context.Background(), &client.Product{Product: "websearch"})
		if err != nil || got.Config != second {
			t.Fatalf("read back %v %v, want %s", got, err, second)
		}

		rows, _, err := trail.Query(context.Background(), audit.Filter{Action: "settings.fleet.set"})
		if err != nil {
			t.Fatalf("query trail: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("trail has %d settings.fleet.set records, want one per write", len(rows))
		}
		var last audit.Record
		for _, r := range rows {
			if string(r.After) == second {
				last = r
			}
		}
		if string(last.Before) != first || last.Actor.Email != "z@hanzo.ai" || last.Resource.ID != "websearch" {
			t.Fatalf("record = before %s after %s actor %+v resource %+v — want the replaced document, the operator and the product",
				last.Before, last.After, last.Actor, last.Resource)
		}
	})
}

// Only a SuperAdmin caller writes the platform's configuration — an org admin
// stating itself is refused like anyone else — and a document that is not a JSON
// object, or a write the trail cannot take, changes nothing.
func TestFleetSetRefuses(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		o, _ := fleetService(t)
		super := zip.WithCaller(context.Background(), zip.Caller{Org: "admin", User: "z", Admin: true})
		orgAdmin := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u", OrgAdmin: true})

		for _, tc := range []struct {
			name string
			ctx  context.Context
			in   client.FleetConfig
			want int
		}{
			{"org admin", orgAdmin, client.FleetConfig{Product: "websearch", Config: `{}`}, http.StatusForbidden},
			{"nobody", context.Background(), client.FleetConfig{Product: "websearch", Config: `{}`}, http.StatusForbidden},
			{"not json", super, client.FleetConfig{Product: "websearch", Config: `nope`}, http.StatusBadRequest},
			{"not an object", super, client.FleetConfig{Product: "websearch", Config: `["a"]`}, http.StatusBadRequest},
			{"null", super, client.FleetConfig{Product: "websearch", Config: `null`}, http.StatusBadRequest},
			{"bad product", super, client.FleetConfig{Product: "../x", Config: `{}`}, http.StatusBadRequest},
		} {
			if _, err := o.fleetSet(tc.ctx, &tc.in); statusOf(err) != tc.want {
				t.Errorf("%s: err = %v, want %d", tc.name, err, tc.want)
			}
		}
		if got, _ := o.fleetConfig(context.Background(), &client.Product{Product: "websearch"}); got.Config != "" {
			t.Fatalf("a refused write was stored: %s", got.Config)
		}

		o.s.trail = nil
		if _, err := o.fleetSet(super, &client.FleetConfig{Product: "websearch", Config: `{"a":1}`}); statusOf(err) != http.StatusServiceUnavailable {
			t.Fatalf("no trail: err = %v, want 503 — an unrecorded change is not made", err)
		}
		if got, _ := o.fleetConfig(context.Background(), &client.Product{Product: "websearch"}); got.Config != "" {
			t.Fatalf("an unaudited write was stored: %s", got.Config)
		}
	})
}

func TestOrgSetUpdatesCallerTenantConfigAndMergesSecretKeyIndex(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		o, _ := fleetService(t)
		acme := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "alice"})
		globex := zip.WithCaller(context.Background(), zip.Caller{Org: "globex", User: "bob"})

		if _, err := o.orgSet(context.Background(), &client.OrgSettingsSet{Product: "vault", Config: `{}`}); statusOf(err) != http.StatusForbidden {
			t.Fatalf("missing tenant: err = %v, want 403", err)
		}
		if _, err := o.orgSet(acme, &client.OrgSettingsSet{
			Product:    "vault",
			Config:     `{"items":[{"id":"v_1","name":"Portal"}]}`,
			SecretKeys: []string{"item_v_1_s"},
		}); err != nil {
			t.Fatalf("acme first orgSet: %v", err)
		}
		if _, err := o.orgSet(acme, &client.OrgSettingsSet{
			Product:    "vault",
			Config:     `{"items":[{"id":"v_1","name":"Portal"},{"id":"v_2","name":"GitHub"}]}`,
			SecretKeys: []string{"item_v_2_s"},
		}); err != nil {
			t.Fatalf("acme second orgSet: %v", err)
		}

		st, err := o.s.store.Get(context.Background(), "acme", "vault")
		if err != nil {
			t.Fatalf("store.Get(acme, vault): %v", err)
		}
		if len(st.SecretKeys) != 2 || st.SecretKeys[0] != "item_v_1_s" || st.SecretKeys[1] != "item_v_2_s" {
			t.Fatalf("merged SecretKeys = %v, want [item_v_1_s item_v_2_s]", st.SecretKeys)
		}
		other, err := o.orgConfig(globex, &client.Product{Product: "vault"})
		if err != nil || other.Config != "" {
			t.Fatalf("globex saw acme config: %+v %v", other, err)
		}
	})
}
