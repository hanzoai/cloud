package dataroom

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
)

// TestALegacyFileKeepsItsDataRoom proves the two kinds of file the data room kept
// before it opened through the seam are the same files now, with the rows they hold: a
// tenant's own, made when the columns said INTEGER, and the cross-tenant link index,
// whose name stays what it was because the key cek derives follows the name. The
// index is read with the link id a visitor arrives holding, which is the one thing
// that would orphan every share link if it came back empty. A file is SQLite by
// nature, so there is no PostgreSQL leg.
func TestALegacyFileKeepsItsDataRoom(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	const org = "acme"
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		t.Fatal(err)
	}

	tenant, err := cek.Open(ns, "dataroom", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		strings.NewReplacer("BIGINT", "INTEGER").Replace(schema),
		`INSERT INTO document (id, name, file_key, created_at, updated_at) VALUES ('doc_old', 'Old deck', 'dataroom/k/old', 1, 1)`,
		`INSERT INTO dataroom (id, p_id, name, created_at, updated_at) VALUES ('room_old', 'dr_old', 'Old room', 1, 1)`,
		`INSERT INTO dataroom_document (id, dataroom_id, document_id, order_index, created_at) VALUES ('dd_old', 'room_old', 'doc_old', 0, 1)`,
		`INSERT INTO link (id, dataroom_id, name, email_protected, created_at, updated_at) VALUES ('link_old', 'room_old', 'Old link', 0, 1, 1)`,
	} {
		if _, err := tenant.Exec(stmt); err != nil {
			t.Fatalf("build the old tenant file: %v\n%s", err, stmt)
		}
	}
	if err := tenant.Close(); err != nil {
		t.Fatal(err)
	}

	index, err := cek.Open(namespace.System(), "link_index", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE link_index (link_id TEXT PRIMARY KEY, org TEXT NOT NULL)`,
		`CREATE TABLE trust_index (slug TEXT PRIMARY KEY, org TEXT NOT NULL UNIQUE)`,
		`INSERT INTO link_index (link_id, org) VALUES ('link_old', 'acme')`,
		`INSERT INTO trust_index (slug, org) VALUES ('acme-trust', 'acme')`,
	} {
		if _, err := index.Exec(stmt); err != nil {
			t.Fatalf("build the old index: %v\n%s", err, stmt)
		}
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	app, _ := mountFlowAppAt(t, dir)

	// A visitor holding only the old link id reaches the old room: the index resolves the
	// org, the org's file resolves the link.
	code, body := jsonReq(t, app, http.MethodGet, "/v1/dataroom/view/link_old", "", nil)
	if code != http.StatusOK || str(body, "link", "name") != "Old link" {
		t.Fatalf("the old link no longer opens: %d %v", code, body)
	}
	code, body = jsonReq(t, app, http.MethodGet, "/v1/dataroom/datarooms/room_old", org, nil)
	if code != http.StatusOK || str(body, "dataroom", "name") != "Old room" {
		t.Fatalf("the old room is gone: %d %v", code, body)
	}
	if mounted.State.index == nil {
		t.Fatal("the index did not open")
	}
	if got, ok, err := mounted.State.index.resolve("acme-trust"); err != nil || !ok || got != org {
		t.Fatalf("the old trust address no longer resolves: %q %v %v", got, ok, err)
	}

	// And a write after the upgrade works in the new dialect on the old file.
	if code, body := jsonReq(t, app, http.MethodPost, "/v1/dataroom/datarooms", org, map[string]any{"name": "New room"}); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("a write after the upgrade: %d %v", code, body)
	}
}

// TestTextTheStoreCannotHoldIsRefused proves a NUL in a name or a note is a 400
// on both engines, where PostgreSQL alone would have failed the statement and SQLite
// would have stored it: a request that fits on one engine is the same answer on the
// other, and nothing of it is stored.
func TestTextTheStoreCannotHoldIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountTrust(t)
		const org = "acme"

		// The bundle's own routes: refused at the bridge, whatever the bundle does with it.
		code, body := jsonReq(t, app, http.MethodPost, "/v1/dataroom/datarooms", org, map[string]any{"name": "bad\x00name"})
		if code != http.StatusBadRequest {
			t.Fatalf("a NUL in a room's name: want 400, got %d %v", code, body)
		}
		if code, body := jsonReq(t, app, http.MethodGet, "/v1/dataroom/datarooms", org, nil); code != http.StatusOK || len(body["datarooms"].([]any)) != 0 {
			t.Fatalf("a refused room was stored: %d %v", code, body)
		}

		// The trust centre's own writes: refused at the op.
		if code, body := hit(t, app, admin(org), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
			"kind": "policy", "name": "Policy\x00", "attester": "self", "tier": "public",
		}); code != http.StatusBadRequest {
			t.Fatalf("a NUL in an item's name: want 400, got %d %s", code, body)
		}
	})
}
