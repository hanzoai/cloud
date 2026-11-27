package iam

import (
	"context"
	"path/filepath"
	"testing"

	iamschema "github.com/hanzoai/iam/pkg/schema"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

func seatGitHub(t *testing.T, db orm.DB, sub, owner, name, github, created, typ string, forbidden bool) {
	t.Helper()
	u := orm.New[iamschema.User](db)
	u.Id, u.Owner, u.Name, u.GitHub, u.CreatedTime, u.Type, u.IsForbidden = sub, owner, name, github, created, typ, forbidden
	u.SetId(owner + "/" + name)
	if err := u.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed %s/%s: %v", owner, name, err)
	}
}

// TestGitHubPeopleNamesTheOldestLivePersonWhoLinkedAnAccount: one GitHub account
// linked by two people (a repeat sign-in to another brand) belongs to the older; a
// barred person, a service account and an account nobody linked name no one; and only
// the platform asks.
func TestGitHubPeopleNamesTheOldestLivePersonWhoLinkedAnAccount(t *testing.T) {
	db, err := iamstore.Open("sqlite", filepath.Join(t.TempDir(), "iam.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	prev := embeddedDB
	embeddedDB = db
	t.Cleanup(func() { embeddedDB = prev; _ = db.Close() })
	seatGitHub(t, db, "sub-new", "other", "ann2", "1001", "2026-05-01T00:00:00Z", "normal-user", false)
	seatGitHub(t, db, "sub-old", "acme", "ann", "1001", "2026-01-01T00:00:00Z", "normal-user", false)
	seatGitHub(t, db, "sub-bar", "acme", "bar", "1002", "2026-01-01T00:00:00Z", "normal-user", true)
	seatGitHub(t, db, "sub-svc", "acme", "svc", "1003", "2026-01-01T00:00:00Z", "service-account", false)
	seatGitHub(t, db, "sub-none", "acme", "none", "", "2026-01-01T00:00:00Z", "normal-user", false)

	ctx := cloud.For(context.Background(), "admin")
	out, err := githubPeople(ctx, &client.GitHubPeopleIn{IDs: []string{"1001", "1002", "1003", "1004", "", "1001"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.People) != 1 || out.People[0].ID != "1001" || out.People[0].Subject != "sub-old" || out.People[0].Org != "acme" {
		t.Fatalf("people = %+v, want the oldest live person for 1001 and no one else", out.People)
	}
	if _, err := githubPeople(cloud.For(context.Background(), "acme"), &client.GitHubPeopleIn{IDs: []string{"1001"}}); err == nil {
		t.Error("a tenant resolved GitHub accounts")
	}
	if _, err := githubPeople(ctx, &client.GitHubPeopleIn{IDs: make([]string, maxGitHubPeople+1)}); err == nil {
		t.Error("an unbounded call was answered")
	}
}
