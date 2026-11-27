package tax

// replica_test.go holds tax on the fleet production runs: two replicas over one
// object store, the payer's org elected to one and the payee's to the other. A
// grant is the payee's decision, written on the payee's replica; what the payer's
// replica may answer from the payee's file is the question here.

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/internal/twopod"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// A replica that is not the payee's elected writer holds, at best, the copy of
// the payee's file it hydrated when it first opened it. The payee revokes on its
// own replica; that copy still says granted. So the payer's replica answers
// nothing from the payee's file — not the TIN, not the W-9 — and furnishes
// nothing into it, and refuses BEFORE it changes anything of its own.
func TestAnotherOrgIsReadOnlyWhereItIsOwned(t *testing.T) {
	// Shipping a file between replicas is the SQLite regime; on hanzo-sql the database
	// is the one every replica shares and there is nothing to ship.
	t.Setenv(locus.Env, "")
	f := twopod.New(t, "pod-a", "pod-b")
	var payer, payee string
	for i := 0; payer == "" || payee == ""; i++ {
		name := fmt.Sprintf("co%d", i)
		switch f.Owner(name).ID {
		case "pod-a":
			payer = cmp.Or(payer, name)
		case "pod-b":
			payee = cmp.Or(payee, name)
		}
	}
	a, b := f.Pod("pod-a"), f.Pod("pod-b")
	ctx := context.Background()
	master := make([]byte, 32)
	_, _ = rand.Read(master)

	// The payee's replica: its W-9 and a live grant to the payer, shipped.
	bs := cloud.NewOrgStoreAt(cloud.Base{DataDir: b.Dir, Durable: b.Durable, Peers: true, Log: luxlog.New("test")}, subsystem, openStore(master))
	t.Cleanup(func() { _ = bs.CloseAll() })
	qns := principal.MustOrgNamespace(payee, "")
	qst, err := bs.For(qns)
	if err != nil {
		t.Fatal(err)
	}
	tin, _ := ParseTIN(SSN, globexSSN)
	w9 := record{Name: "Payee Holdings", Classification: Individual, TINType: SSN, Consent: Consent{Electronic: true},
		Address: Address{Line1: "1 Market St", City: "San Francisco", State: "CA", ZIP: "94105", Country: "US"}}
	if err := qst.putProfile(ctx, profileRow{version: 1, data: encode(w9), tin: tin, updated: now()}); err != nil {
		t.Fatal(err)
	}
	grant := shareRow{id: "w9_live", payer: payer, status: shareGranted, data: "{}", created: now(), updated: now()}
	if err := qst.putShare(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if acked, err := bs.Sync(qns); err != nil || !acked {
		t.Fatalf("the payee's owner did not ship its grant: %v %v", acked, err)
	}

	// The payer's replica, serving tax.
	t.Setenv("CLOUD_DATA_DIR", a.Dir)
	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{Audit: rec, MasterKey: master, Durable: a.Durable, Peers: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	e := &env{t: t, app: app}
	pst := e.storeFor(payer)
	pns := principal.MustOrgNamespace(payer, "")
	if err := pst.putPayee(ctx, payeeRow{payee: payee, share: grant.id, category: Services, data: "{}", created: now(), updated: now()}); err != nil {
		t.Fatal(err)
	}
	form := formRow{id: "f1099_x", payee: payee, year: 2026, kind: NEC, status: Reviewed, content: encode(content{Kind: NEC, Year: 2026}),
		created: now(), updated: now()}
	if err := pst.insertForm(ctx, form); err != nil {
		t.Fatal(err)
	}
	if acked, err := mounted.State.stores.Sync(pns); err != nil || !acked {
		t.Fatalf("the payer's owner did not ship: %v %v", acked, err)
	}

	read := func(when string) {
		t.Helper()
		for _, path := range []string{"/v1/tax/w9/" + grant.id + "/tin", "/v1/tax/w9/" + grant.id} {
			code, body, _ := e.raw(http.MethodGet, path, admin(payer), nil, nil)
			if code != http.StatusServiceUnavailable {
				t.Fatalf("%s: GET %s on the replica that does not own %s = %d, want 503: %s", when, path, payee, code, body)
			}
		}
	}
	read("while the grant is live")

	// The payee revokes, on its own replica.
	grant.status, grant.updated = shareRevoked, now()
	if err := qst.putShare(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if acked, err := bs.Sync(qns); err != nil || !acked {
		t.Fatalf("the payee's owner did not ship its revocation: %v %v", acked, err)
	}
	read("after the payee revoked")

	// Furnishing into the payee's inbox is refused before the form moves: a form
	// marked furnished whose Copy B never reached the payee would be a record of a
	// statement nobody received.
	if code, body, _ := e.raw(http.MethodPost, "/v1/tax/forms/"+form.id+"/furnish", admin(payer), nil, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("furnish across replicas = %d, want 503: %s", code, body)
	}
	got, err := pst.form(ctx, form.id)
	if err != nil {
		t.Fatal(err)
	}
	if got.status != Reviewed {
		t.Fatalf("a refused furnish left the form %s", got.status)
	}
}
