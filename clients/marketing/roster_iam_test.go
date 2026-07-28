package marketing

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	iamclient "github.com/hanzoai/cloud/clients/iam"
	model "github.com/hanzoai/iam/pkg/model"
	"github.com/hanzoai/orm"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestRosterReadsTheRealEmbeddedIAM exercises the PRODUCTION reader against a
// really-mounted IAM, not the fake: it mounts the co-resident IAM subsystem the
// way cloud does at boot, writes users into IAM's own store, and reads them back
// through iamRoster. Every other test stubs rosterFn, so without this one the
// cross-repo seam — the whole point of the change — would never actually run.
//
// The fail-closed assertion lives HERE, before the mount, rather than in its own
// test: clients/iam.DB() is a process-global set by Mount with no un-mount, so
// "IAM is not available" is only observable before this test mounts it. Ordering
// it inside one test makes that explicit instead of leaving a sibling test
// silently dependent on running first.
func TestRosterReadsTheRealEmbeddedIAM(t *testing.T) {
	// Before any mount: no store published → the roster REFUSES.
	if _, err := iamRoster("hanzo"); err != errIAMUnavailable {
		t.Fatalf("unmounted iam must fail closed, got %v", err)
	}

	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	if err := iamclient.Mount(app, cloud.Deps{
		Logger:  luxlog.NewNoOpLogger(),
		DataDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("mount iam: %v", err)
	}
	db := iamclient.DB()
	if db == nil {
		t.Fatal("iam mounted but published no store — the in-process seam is dead")
	}

	// Write through IAM's own model, the way the migrator does.
	for _, u := range []*model.User{
		{Id: "id-ada", Owner: "hanzo", Name: "ada", Email: "ada@hanzo.ai", PasswordHash: "$argon2id$secret"},
		{Id: "id-gone", Owner: "hanzo", Name: "gone", Email: "gone@hanzo.ai", IsDeleted: true},
		{Id: "id-eve", Owner: "acme", Name: "eve", Email: "eve@acme.com"},
	} {
		row := orm.New[model.User](db)
		m := row.Model
		*row = *u
		row.Model = m
		row.SetId(u.Owner + "/" + u.Name)
		if err := row.CreateCtx(context.Background()); err != nil {
			t.Fatalf("seed %s/%s: %v", u.Owner, u.Name, err)
		}
	}

	got, err := iamRoster("hanzo")
	if err != nil {
		t.Fatalf("iamRoster: %v", err)
	}
	if len(got) != 1 || got[0].Email != "ada@hanzo.ai" {
		t.Fatalf("want [ada@hanzo.ai] (deleted excluded, acme excluded), got %+v", got)
	}
	if got[0].PasswordHash != "" {
		t.Fatalf("credential material crossed the embed seam: %q", got[0].PasswordHash)
	}

	// And it resolves as an audience — the real read, end to end.
	r, err := resolveAudience(context.Background(), "hanzo", Audience{Name: "All customers"})
	if err != nil {
		t.Fatalf("resolveAudience over the real store: %v", err)
	}
	if len(r.addresses) != 1 || r.addresses[0] != "ada@hanzo.ai" {
		t.Fatalf("want [ada@hanzo.ai], got %v", r.addresses)
	}
}
