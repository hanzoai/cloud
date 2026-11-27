// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/authz"
	iamschema "github.com/hanzoai/iam/pkg/schema"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"
)

// The directory is every tenant at once, so only the platform reads it; any org,
// including one whose admin asks, is refused.
func TestTheDirectoryIsThePlatformsAlone(t *testing.T) {
	rolesStore(t)
	for _, org := range []string{"hanzo", "agency", ""} {
		if _, err := directory(zip.WithCaller(context.Background(), zip.Caller{User: zSub, Org: org}), nil); err == nil {
			t.Errorf("a caller in %q read the directory", org)
		}
	}
}

// Every live person and every org comes back, projected to directory rows: a
// deleted person is absent, whether a person proved their address travels, and
// no credential column reaches the wire.
func TestTheDirectoryListsEveryLiveRowWithoutCredentials(t *testing.T) {
	db := rolesStore(t)
	for _, name := range []string{"hanzo", "agency"} {
		o := orm.New[iamschema.Organization](db)
		o.Owner, o.Name, o.DisplayName, o.CreatedTime = authz.AdminOrg, name, strings.ToUpper(name), "2026-01-02T00:00:00Z"
		o.SetId(authz.AdminOrg + "/" + name)
		if err := o.CreateCtx(context.Background()); err != nil {
			t.Fatalf("seed org %s: %v", name, err)
		}
	}
	gone := orm.New[iamschema.User](db)
	gone.Owner, gone.Name, gone.IsDeleted = "agency", "gone", true
	gone.SetId("agency/gone")
	if err := gone.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed deleted user: %v", err)
	}
	bot := orm.New[iamschema.User](db)
	bot.Owner, bot.Name, bot.Type, bot.Tag = "agency", "deploy", iamschema.ServiceAccount, "ci"
	bot.SetId("agency/deploy")
	if err := bot.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed service account: %v", err)
	}
	proved := orm.New[iamschema.User](db)
	proved.Owner, proved.Name, proved.Email, proved.EmailVerified = "agency", "ada", "ada@agency.com", true
	proved.SetId("agency/ada")
	if err := proved.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed verified person: %v", err)
	}

	got, err := directory(zip.WithCaller(context.Background(), zip.Caller{Org: authz.AdminOrg}), nil)
	if err != nil {
		t.Fatalf("platform read: %v", err)
	}
	if len(got.Orgs) != 2 {
		t.Fatalf("orgs = %d, want 2", len(got.Orgs))
	}
	seen := map[string]bool{}
	for _, u := range got.Users {
		seen[u.Owner+"/"+u.Name] = true
		// The type and tag travel, so the console can tell a person from a machine.
		if u.Name == "deploy" && (u.Type != iamschema.ServiceAccount || u.Tag != "ci") {
			t.Errorf("agency/deploy = type %q tag %q, want %q and ci", u.Type, u.Tag, iamschema.ServiceAccount)
		}
		if u.Verified != (u.Name == "ada") {
			t.Errorf("%s/%s verified = %v; only agency/ada proved an address", u.Owner, u.Name, u.Verified)
		}
	}
	// rolesStore seats five people, beside them a service account and a person
	// who proved her address; the deleted row is not a directory row.
	if len(got.Users) != 7 || seen["agency/gone"] || !seen["hanzo/z"] || !seen["agency/deploy"] || !seen["agency/ada"] {
		t.Fatalf("users = %v, want the seven live ones", seen)
	}
	wire, _ := json.Marshal(got)
	for _, col := range []string{"password", "accessKey", "accessSecret", "totpSecret", "hash"} {
		if strings.Contains(strings.ToLower(string(wire)), strings.ToLower(col)) {
			t.Errorf("the directory wire carries %q", col)
		}
	}
}
