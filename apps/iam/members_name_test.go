// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"testing"

	iamschema "github.com/hanzoai/iam/pkg/schema"
	"github.com/hanzoai/orm"
)

// A roster names a person by the display name IAM holds, else by the handle they
// signed up as, and only when IAM holds neither by the user id: a wallet account
// has no display name, and each of its messages was signed with a uuid.
func TestAMemberIsNamedByDisplayNameThenHandleThenId(t *testing.T) {
	db := rolesStore(t)
	for _, u := range []struct{ id, name, display string }{
		{"agency/ada", "ada", "Ada Lovelace"},
		{"agency/wallet_evm_00ff", "wallet_evm_00ff", ""},
	} {
		row := orm.New[iamschema.User](db)
		row.Id, row.Owner, row.Name, row.DisplayName = u.id, "agency", u.name, u.display
		row.SetId("agency/" + u.name)
		if err := row.CreateCtx(context.Background()); err != nil {
			t.Fatalf("seed %s: %v", u.name, err)
		}
	}
	for sub, want := range map[string]string{
		"agency/ada":             "Ada Lovelace",
		"agency/wallet_evm_00ff": "wallet_evm_00ff",
		"agency/nobody":          "agency/nobody",
	} {
		if got := displayName(db, sub); got != want {
			t.Errorf("displayName(%q) = %q, want %q", sub, got, want)
		}
	}
}
