package audit

import (
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud/internal/sqltest"
	"testing"
)

// The operator columns could be added to a SEALED chain: a zero Act marshals to
// nothing, so every record without one hashes exactly as before, and a chain
// sealed on the schema without the columns still verifies once they are added.
// A record that names an operator then round-trips through the store, verifies,
// and is what the impersonated filter finds.
func TestVerify_ActIsHashCompatible(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		now, was := Actor{Org: "acme", Sub: "u-ada", Email: "ada@acme.test"}, actorBeforeHome{Org: "acme", Sub: "u-ada", Email: "ada@acme.test"}
		nowJSON, _ := json.Marshal(now)
		wasJSON, _ := json.Marshal(was)
		if string(nowJSON) != string(wasJSON) {
			t.Fatalf("adding Act changed what a record without one hashes over:\n now %s\n was %s", nowJSON, wasJSON)
		}

		dir := t.TempDir()
		ctx := context.Background()
		rec, err := Open(dir, "audit", nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		for i := range 3 {
			if _, err := rec.Append(ctx, sampleRecord("POST /v1/projects")); err != nil {
				t.Fatalf("append %d: %v", i, err)
			}
		}
		if err := rec.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		tamperOutOfBand(t, dir, `ALTER TABLE audit_log DROP COLUMN act_sub`)
		tamperOutOfBand(t, dir, `ALTER TABLE audit_log DROP COLUMN act_owner`)
		tamperOutOfBand(t, dir, `ALTER TABLE audit_log DROP COLUMN act_name`)

		rec2, err := Open(dir, "audit", nil)
		if err != nil {
			t.Fatalf("re-Open on the schema without the operator: %v", err)
		}
		t.Cleanup(func() { _ = rec2.Close() })
		op := Act{Sub: "u-op", Owner: "admin", Name: "z"}
		r := sampleRecord("GET /v1/projects")
		r.Actor = Actor{Org: "acme", Sub: "u-ada", Act: op}
		if _, err := rec2.Append(ctx, r); err != nil {
			t.Fatalf("append with an operator: %v", err)
		}
		if iv, err := rec2.Verify(ctx); err != nil || iv.Verdict != Intact || iv.Count != 4 {
			t.Fatalf("verify %+v %v, want 4 records intact", iv, err)
		}
		rows, total, err := rec2.Query(ctx, Filter{Impersonated: true})
		if err != nil || total != 1 || len(rows) != 1 || rows[0].Actor.Act != op {
			t.Fatalf("impersonated rows %+v total %d %v, want the one naming %+v", rows, total, err, op)
		}
		if w := rows[0].ToWire(); w.Act != op {
			t.Fatalf("wire %+v lost the operator", w)
		}
	})
}

// Acting names the operator on a record that did not, and never overrides one
// that did.
func TestActingStampsOnlyAnUnnamedRecord(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		rec, _ := openTemp(t)
		rec.Acting(func(context.Context) Act { return Act{Sub: "u-op"} })
		got, err := rec.Append(context.Background(), sampleRecord("POST /v1/projects"))
		if err != nil || got.Actor.Act.Sub != "u-op" {
			t.Fatalf("unnamed record %+v %v, want the operator stamped", got.Actor, err)
		}
		r := sampleRecord("POST /v1/projects")
		r.Actor.Act = Act{Sub: "u-other"}
		if got, err = rec.Append(context.Background(), r); err != nil || got.Actor.Act.Sub != "u-other" {
			t.Fatalf("named record %+v %v, want its own operator kept", got.Actor, err)
		}
	})
}
