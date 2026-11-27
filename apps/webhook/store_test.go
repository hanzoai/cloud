package webhook

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/internal/kmstest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// storeOf is the org's own store, as the dispatcher reaches it.
func storeOf(t *testing.T, org string) *store {
	t.Helper()
	st, err := mounted.storeFor(org)
	if err != nil {
		t.Fatalf("store for %s: %v", org, err)
	}
	return st
}

// rawSecret is what the column holds, with nothing opened.
func rawSecret(t *testing.T, st *store, id string) string { return rawColumn(t, st, "secret", id) }

// rawColumn is what a column of the endpoint table holds, with nothing opened.
func rawColumn(t *testing.T, st *store, column, id string) string {
	t.Helper()
	var raw string
	if err := st.db.QueryRow(`SELECT `+column+` FROM endpoint WHERE id = $1`, id).Scan(&raw); err != nil {
		t.Fatalf("read the column: %v", err)
	}
	return raw
}

// rowText is every column of an endpoint's row joined, as the table holds it: what a
// reader with SELECT sees.
func rowText(t *testing.T, st *store, id string) string {
	t.Helper()
	var all string
	if err := st.db.QueryRow(`SELECT id || '|' || org || '|' || url || '|' || events || '|' || secret || '|' || status || '|' || description
		FROM endpoint WHERE id = $1`, id).Scan(&all); err != nil {
		t.Fatalf("read the row: %v", err)
	}
	return all
}

// The signing secret and the URL are KMS values: the table holds the path of each and no
// byte of either, a read asks KMS, a rotation and an edit write the new value to the same
// path, and a delete asks KMS to forget both.
func TestASigningSecretAndAddressLiveInKMS(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		const hook = "https://hooks.acme.test/services/T0001/B0002/XXXXsecretpath"
		code, body := do(t, app, http.MethodPost, "/v1/webhook", "acme", map[string]any{"url": hook})
		if code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}
		var created Endpoint
		if err := json.Unmarshal(body, &created); err != nil {
			t.Fatal(err)
		}
		st := storeOf(t, "acme")
		secretRef := "orgs/acme/webhooks/" + created.ID + "/secret@default"
		urlRef := "orgs/acme/webhooks/" + created.ID + "/url@default"
		if got := rawSecret(t, st, created.ID); got != "kms:"+secretRef {
			t.Fatalf("the secret column holds %q, want its KMS path", got)
		}
		if got := rawColumn(t, st, "url", created.ID); got != "kms:"+urlRef {
			t.Fatalf("the url column holds %q, want its KMS path", got)
		}
		if row := rowText(t, st, created.ID); strings.Contains(row, created.Secret) || strings.Contains(row, "whsec_") || strings.Contains(row, "secretpath") || strings.Contains(row, "hooks.acme") {
			t.Fatalf("the row holds a secret byte: %q", row)
		}
		if held := testKMS.Held(); held[secretRef] != created.Secret || held[urlRef] != hook || len(held) != 2 {
			t.Fatalf("KMS holds %v, want the secret and the address at their paths", held)
		}
		ctx := context.Background()
		got, err := st.get(ctx, created.ID)
		if err != nil || got.Secret != "" || got.URL != "" {
			t.Fatalf("a registry read carries %q %q (%v), want neither: only a delivery reads them", got.Secret, got.URL, err)
		}
		if err := st.reveal(ctx, &got, true, true); err != nil || got.Secret != created.Secret || got.URL != hook {
			t.Fatalf("the dispatcher's read: %q %q %v, want what it was given", got.Secret, got.URL, err)
		}
		active, err := st.listActive(ctx)
		if err != nil || len(active) != 1 || active[0].Secret != "" {
			t.Fatalf("the active listing: %+v %v, want the endpoint named and nothing read", active, err)
		}

		code, body = do(t, app, http.MethodPost, "/v1/webhook/"+created.ID+"/secret", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("rotate want 200, got %d (%s)", code, body)
		}
		var rotated Endpoint
		if err := json.Unmarshal(body, &rotated); err != nil {
			t.Fatal(err)
		}
		if rotated.Secret == created.Secret || rawSecret(t, st, created.ID) != "kms:"+secretRef || testKMS.Held()[secretRef] != rotated.Secret {
			t.Fatalf("rotation: new secret %q, column %q, KMS %q", rotated.Secret, rawSecret(t, st, created.ID), testKMS.Held()[secretRef])
		}
		const moved = "https://hooks.acme.test/services/T0001/B0003/YYYYnewpath"
		if code, body := do(t, app, http.MethodPut, "/v1/webhook/"+created.ID, "acme", map[string]any{"url": moved}); code != http.StatusOK {
			t.Fatalf("update want 200, got %d (%s)", code, body)
		}
		if row := rowText(t, st, created.ID); strings.Contains(row, "newpath") || strings.Contains(row, rotated.Secret) {
			t.Fatalf("after an edit and a rotation the row holds %q", row)
		}
		if testKMS.Held()[urlRef] != moved {
			t.Fatalf("after an edit KMS holds %q at the address, want %q", testKMS.Held()[urlRef], moved)
		}

		if code, body := do(t, app, http.MethodDelete, "/v1/webhook/"+created.ID, "acme", nil); code != http.StatusNoContent {
			t.Fatalf("delete want 204, got %d (%s)", code, body)
		}
		if refs := testKMS.Refs(); len(refs) != 0 {
			t.Fatalf("a deleted endpoint left %v in KMS", refs)
		}
	})
}

// A reference names its own endpoint's value: a column pointed at another org's path is
// refused by KMS, and one that names nothing KMS holds is a dangling reference, not an
// empty secret.
func TestAReferenceReadsOnlyItsOwnValue(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := kmstest.New()
		st, err := testOpen(k)(openRaw(t))
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		for _, e := range []Endpoint{
			{ID: "wh_a", Org: "acme", URL: "https://a.test/hook", Secret: "whsec_a", Status: "active", CreatedAt: "t", UpdatedAt: "t"},
			{ID: "wh_g", Org: "globex", URL: "https://g.test/hook", Secret: "whsec_g", Status: "active", CreatedAt: "t", UpdatedAt: "t"},
		} {
			if err := st.create(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.db.ExecContext(ctx, `UPDATE endpoint SET secret = $1 WHERE id = 'wh_a'`, rawSecret(t, st, "wh_g")); err != nil {
			t.Fatal(err)
		}
		a, err := st.get(ctx, "wh_a")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.reveal(ctx, &a, false, true); err == nil || a.Secret != "" {
			t.Fatalf("acme's endpoint read globex's secret: %q, %v", a.Secret, err)
		}
		if _, err := st.db.ExecContext(ctx, `UPDATE endpoint SET secret = 'kms:orgs/acme/webhooks/wh_a/nothing@default' WHERE id = 'wh_a'`); err != nil {
			t.Fatal(err)
		}
		a, _ = st.get(ctx, "wh_a")
		if err := st.reveal(ctx, &a, false, true); !errors.Is(err, escrow.ErrGone) {
			t.Fatalf("a dangling reference read as %q, %v, want ErrGone", a.Secret, err)
		}
	})
}

// openRaw is a database for a store to be made over.
func openRaw(t *testing.T) *sql.DB {
	t.Helper()
	ns, err := principal.OrgNamespace("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := cloud.OrgDB(t.TempDir(), ns, "webhooks")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A process with no KMS neither keeps a secret anywhere else nor reads one: creating an
// endpoint answers 503 and leaves no row.
func TestNoKMSKeepsNoSecretAnywhere(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		compose(app)
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
		if err := Use(app, cloud.Deps{MasterKey: testMaster}); err != nil {
			t.Fatalf("Use: %v", err)
		}
		t.Cleanup(func() { _ = Shutdown(nil) })
		code, body := do(t, app, http.MethodPost, "/v1/webhook", "acme", map[string]any{"url": "https://hooks.acme.test/in"})
		if code != http.StatusServiceUnavailable {
			t.Fatalf("create with no KMS want 503, got %d (%s)", code, body)
		}
		if strings.Contains(string(body), "whsec_") {
			t.Fatalf("the refusal carries a secret: %s", body)
		}
		st, err := mounted.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM endpoint`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("a refused create left %d rows (%v)", n, err)
		}
	})
}

// A file made before KMS keeps working: its plain address and secret, a secret concealed
// under org.Cipher.Conceal, and an address and a secret sealed as fields are each written to
// KMS when the store opens, and the column is replaced by the path. The second open reads
// and writes nothing. Built by hand on both engines: the rows are the old shapes in the
// current table.
func TestALegacyRowMovesToKMSOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		db := openRaw(t)
		k := kmstest.New()
		st, err := testOpen(k)(db)
		if err != nil {
			t.Fatal(err)
		}
		ns, err := principal.OrgNamespace("acme", "")
		if err != nil {
			t.Fatal(err)
		}
		c := cipherOf(testMaster)
		blob, err := c.Conceal(ns, legacySecretSubsystem, []byte("whsec_concealed99"))
		if err != nil {
			t.Fatal(err)
		}
		concealed := legacySecretPrefix + base64.RawURLEncoding.EncodeToString(blob)
		f, err := c.Fields(ns, subsystem)
		if err != nil {
			t.Fatal(err)
		}
		r := f.Row("endpoint", "acme", "wh_field")
		sealedURL, sealedSecret := r.Seal("url", "https://hooks.acme.test/field/FIELDPATH"), r.Seal("secret", "whsec_fieldfield")
		if err := r.Err(); err != nil {
			t.Fatal(err)
		}
		const cols = `INSERT INTO endpoint (id, org, url, secret, created_at, updated_at) VALUES ($1, 'acme', $2, $3, 't', 't')`
		for _, w := range []struct{ id, url, secret string }{
			{"wh_plain", "https://hooks.acme.test/plain/PLAINPATH", "whsec_0123abcd"},
			{"wh_conc", "https://hooks.acme.test/conc/CONCPATH", concealed},
			{"wh_field", sealedURL, sealedSecret},
		} {
			if _, err := st.db.Exec(cols, w.id, w.url, w.secret); err != nil {
				t.Fatalf("seed %s: %v", w.id, err)
			}
		}
		if err := st.moveToKMS(context.Background()); err != nil {
			t.Fatalf("move: %v", err)
		}
		ctx := context.Background()
		for _, w := range []struct{ id, url, secret, path string }{
			{"wh_plain", "https://hooks.acme.test/plain/PLAINPATH", "whsec_0123abcd", "PLAINPATH"},
			{"wh_conc", "https://hooks.acme.test/conc/CONCPATH", "whsec_concealed99", "CONCPATH"},
			{"wh_field", "https://hooks.acme.test/field/FIELDPATH", "whsec_fieldfield", "FIELDPATH"},
		} {
			if rawSecret(t, st, w.id) != "kms:orgs/acme/webhooks/"+w.id+"/secret@default" || rawColumn(t, st, "url", w.id) != "kms:orgs/acme/webhooks/"+w.id+"/url@default" {
				t.Fatalf("%s: the columns hold %q and %q, want their KMS paths", w.id, rawSecret(t, st, w.id), rawColumn(t, st, "url", w.id))
			}
			if row := rowText(t, st, w.id); strings.Contains(row, w.path) || strings.Contains(row, "0123abcd") || strings.Contains(row, "concealed99") || strings.Contains(row, "fieldfield") || strings.Contains(row, "seal:") {
				t.Fatalf("%s: the row still holds the old value: %q", w.id, row)
			}
			e, err := st.get(ctx, w.id)
			if err != nil || st.reveal(ctx, &e, true, true) != nil || e.Secret != w.secret || e.URL != w.url {
				t.Fatalf("%s after the move: %q %q %v, want the same values", w.id, e.Secret, e.URL, err)
			}
		}
		// A second open reads and writes nothing.
		before, rows := k.Held(), []string{rowText(t, st, "wh_plain"), rowText(t, st, "wh_conc"), rowText(t, st, "wh_field")}
		reads := k.Reads()
		if err := st.migrate(); err != nil {
			t.Fatal(err)
		}
		after := k.Held()
		if len(after) != len(before) || k.Reads() != reads {
			t.Fatalf("a second open touched KMS: %d values before, %d after, reads %d to %d", len(before), len(after), reads, k.Reads())
		}
		for ref, v := range before {
			if after[ref] != v {
				t.Fatalf("a second open rewrote %s", ref)
			}
		}
		for i, id := range []string{"wh_plain", "wh_conc", "wh_field"} {
			if rowText(t, st, id) != rows[i] {
				t.Fatalf("a second open rewrote the row of %s", id)
			}
		}
	})
}

// A log line says which host a delivery went to and nothing of the rest of the address.
func TestALogLineNamesTheHostAndNotTheAddress(t *testing.T) {
	if got := hostOf("https://hooks.acme.test/services/T1/B2/SECRETPATH?token=abc"); got != "hooks.acme.test" {
		t.Fatalf("hostOf = %q", got)
	}
	if got := hostOf("::not a url"); got != "" {
		t.Fatalf("hostOf of a malformed address = %q", got)
	}
}

// What the delivery log keeps of a failed attempt is the cause and not the address: for a
// hosted receiver the address is a credential, and the standard library's text names it.
func TestTheDeliveryLogDoesNotKeepTheAddress(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://hooks.acme.test/services/T1/B2/SECRETPATH", Err: errors.New("dial tcp 192.0.2.1:443: connect: connection refused")}
	if got := faultText(err); strings.Contains(got, "SECRETPATH") || strings.Contains(got, "hooks.acme.test") || !strings.Contains(got, "connection refused") {
		t.Fatalf("faultText = %q", got)
	}
	if got := faultText(errors.New("plain")); got != "plain" {
		t.Fatalf("faultText of a non-URL error = %q", got)
	}
}

// Text PostgreSQL would refuse is refused at the store on both engines, and the error
// never carries it.
func TestWebhookStoreRefusesTextPostgresRefuses(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		st := storeOf(t, "acme")
		ctx := context.Background()
		for name, bad := range map[string]string{"NUL": "a\x00b", "not UTF-8": "a\xffb"} {
			e := Endpoint{ID: "wh_x", Org: "acme", URL: "https://x.test/" + bad, Secret: "whsec_1", Status: "active", CreatedAt: "t", UpdatedAt: "t"}
			errs := map[string]error{
				"create": st.create(ctx, e),
				"record": st.recordDelivery(ctx, DeliveryRow{EndpointID: "wh_x", DeliveryID: "d", Subject: bad, Status: "ok", Created: "t"}),
			}
			_, errs["update"] = st.update(ctx, "acme", "wh_x", "https://x.test", nil, "active", bad, "t")
			for what, err := range errs {
				if !errors.Is(err, dialect.ErrText) {
					t.Errorf("%s with %s text: %v, want ErrText", what, name, err)
				}
				if err != nil && strings.Contains(err.Error(), bad) {
					t.Errorf("%s with %s text: the error carries the value", what, name)
				}
			}
		}
	})
}
