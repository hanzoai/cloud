package knowledge

// The wire contract of the typed connector surface — the part a status-code test
// would not see.
//
// GET /v1/knowledge/connectors answers a row per provider, and the row's SHAPE carries
// meaning: a provider this org has never connected omits account, lastSync and
// error ENTIRELY, while a connected one carries them even when they are empty
// strings. That distinction is why connectorView holds *string and not string
// with omitempty — omitempty on a plain string drops an empty-but-present field,
// which silently turns "connected, no account name" into "never connected" for
// every reader of this document.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
)

// TestConnectorRowOmitsUnsetFieldsOnly pins the presence contract on both sides.
func TestConnectorRowOmitsUnsetFieldsOnly(t *testing.T) {
	app := mountKB(t)
	installKB(t, app, "acme")

	code, body := req(t, app, http.MethodGet, "/v1/knowledge/connectors", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list connectors: want 200, got %d (%s)", code, body)
	}
	var raw struct {
		Connectors []map[string]json.RawMessage `json:"connectors"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(raw.Connectors) != len(providers) {
		t.Fatalf("got %d connector rows, want one per provider (%d)", len(raw.Connectors), len(providers))
	}
	for _, row := range raw.Connectors {
		// Every row always carries these five.
		for _, k := range []string{"provider", "configured", "status", "docCount", "kind"} {
			if _, ok := row[k]; !ok {
				t.Fatalf("connector row is missing %q: %v", k, row)
			}
		}
		// Nothing is connected in a fresh org, so the three connection-state fields
		// must be ABSENT — not present-and-empty.
		for _, k := range []string{"account", "lastSync", "error"} {
			if _, ok := row[k]; ok {
				t.Fatalf("never-connected row carries %q; the untyped handler omitted it: %v", k, row)
			}
		}
	}

	// Now record a connection with an EMPTY account, the case a plain
	// `string,omitempty` would erase.
	svc := &cloud.Service[state]{Base: cloud.NewBase(cloud.Deps{}, "knowledge")}
	if err := upsertConnector(svc, context.Background(), "acme", "github", map[string]any{
		"provider": "github",
		"status":   "connected",
		"account":  "",
		"kms_ref":  "kb/connectors/acme/github/oauth-token",
		"error":    "",
	}); err != nil {
		t.Fatalf("upsert connector: %v", err)
	}

	code, body = req(t, app, http.MethodGet, "/v1/knowledge/connectors", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list connectors after connect: want 200, got %d (%s)", code, body)
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	var seen bool
	for _, row := range raw.Connectors {
		var provider string
		_ = json.Unmarshal(row["provider"], &provider)
		if provider != "github" {
			continue
		}
		seen = true
		for _, k := range []string{"account", "lastSync", "error"} {
			v, ok := row[k]
			if !ok {
				t.Fatalf("connected row dropped %q — an empty string is PRESENT on this wire: %v", k, row)
			}
			if string(v) != `""` {
				t.Fatalf("connected row %q = %s, want the empty string", k, v)
			}
		}
	}
	if !seen {
		t.Fatal("github row absent from the connector list")
	}
}
