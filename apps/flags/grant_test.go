// Copyright © 2026 Hanzo AI. MIT License.

package flags

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// superCtx is a SuperAdmin's call, as the plane carries one.
var superCtx = zip.WithCaller(context.Background(), zip.Caller{Org: "admin", User: "u_super", Email: "z@hanzo.ai", Admin: true})

// grantFor lets user into key (or out of it) as a SuperAdmin, failing the test on an error.
func grantFor(t *testing.T, key, user string, on bool) {
	t.Helper()
	if _, err := grantOp(superCtx, &client.FlagGrantIn{Key: key, User: user, On: on, Actor: "z@hanzo.ai"}); err != nil {
		t.Fatalf("grant %s %s %v: %v", key, user, on, err)
	}
}

// onFor is On(key) for org and user, failing the test on an error.
func onFor(t *testing.T, key, org, user string) bool {
	t.Helper()
	on, err := On(principalOf(org, user), key)
	if err != nil {
		t.Fatalf("On(%s) for %s/%s: %v", key, org, user, err)
	}
	return on
}

// A GRANT LETS ONE PERSON IN AND A REVOKE TAKES THEM OUT, in any org, and leaves
// everyone else as they were: the orgs and the rollout a SuperAdmin targeted stay.
func TestAGrantLetsOnePersonIn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		target(t, []string{"acme"}, nil, 0)

		grantFor(t, Beta, "u_ann", true)
		grantFor(t, Beta, "u_ann", true) // twice is once
		grantFor(t, Beta, "u.bo", true)
		for _, c := range []struct {
			org, user string
			want      bool
		}{
			{"hanzo", "u_ann", true},
			{"globex", "u.bo", true},
			{"globex", "uxbo", false}, // the dot is a dot
			{"acme", "u_cy", true},    // the org rule is kept
			{"globex", "u_cy", false},
		} {
			if got := onFor(t, Beta, c.org, c.user); got != c.want {
				t.Errorf("beta for %s/%s = %v, want %v", c.org, c.user, got, c.want)
			}
		}
		users, orgs := named(mustStored(t, Beta))
		slices.Sort(users)
		if !slices.Equal(users, []string{"u.bo", "u_ann"}) || !slices.Equal(orgs, []string{"acme"}) {
			t.Fatalf("named = %v %v, want [u.bo u_ann] [acme]", users, orgs)
		}

		grantFor(t, Beta, "u_ann", false)
		if onFor(t, Beta, "hanzo", "u_ann") {
			t.Error("a revoked person still has beta")
		}
		if !onFor(t, Beta, "globex", "u.bo") || !onFor(t, Beta, "acme", "u_cy") {
			t.Error("a revoke took someone else out")
		}
	})
}

// THE LAST PERSON OUT TURNS THE SWITCH OFF, and nothing stored grants as a first write.
func TestTheLastPersonOutTurnsItOff(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		grantFor(t, Beta, "u_ann", true)
		if !onFor(t, Beta, "hanzo", "u_ann") {
			t.Fatal("a first grant did not take")
		}
		grantFor(t, Beta, "u_ann", false)
		if onFor(t, Beta, "hanzo", "u_ann") {
			t.Fatal("the switch stayed on with nobody in it")
		}
		if !strings.Contains(string(mustStored(t, Beta)), `"active":false`) {
			t.Fatalf("stored %s, want inactive", mustStored(t, Beta))
		}
		grantFor(t, Beta, "u_nobody", false) // a revoke of nobody is a no-op, not an error
	})
}

// ONLY A SUPERADMIN GRANTS, only a principal switch is granted, and only an IAM id.
func TestOnlyASuperAdminGrants(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		for _, who := range []zip.Caller{
			{Org: "acme", User: "u_member"},
			{Org: "acme", User: "u_orgadmin", OrgAdmin: true},
			{Org: "admin", User: "u_member"},
			{},
		} {
			if _, err := grantOp(zip.WithCaller(context.Background(), who), &client.FlagGrantIn{Key: Beta, User: who.User, On: true}); err == nil {
				t.Fatalf("SECURITY: %+v granted itself beta", who)
			}
		}
		if onFor(t, Beta, "acme", "u_member") || onFor(t, Beta, "acme", "u_orgadmin") {
			t.Fatal("SECURITY: a refused grant turned beta on")
		}
		for _, in := range []client.FlagGrantIn{
			{Key: "public_signup", User: "u1", On: true}, // a deployment switch, not a principal one
			{Key: "beta.nothing", User: "u1", On: true},  // not registered
			{Key: Beta, User: "", On: true},
			{Key: Beta, User: "a|b", On: true}, // an alternation is not a person
			{Key: Beta, User: "u 1", On: true},
			{Key: Beta, User: ".*", On: true},
		} {
			if _, err := grantOp(superCtx, &in); err == nil {
				t.Errorf("grant %+v was taken", in)
			}
		}
		if onFor(t, Beta, "acme", "a") || onFor(t, Beta, "acme", "anything") {
			t.Fatal("SECURITY: a refused grant let someone in")
		}
	})
}

// TWO GRANTS MADE TOGETHER BOTH LAND.
func TestGrantsMadeTogetherBothLand(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		var wg sync.WaitGroup
		people := []string{"u0", "u1", "u2", "u3", "u4", "u5", "u6", "u7"}
		for _, u := range people {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := grantOp(superCtx, &client.FlagGrantIn{Key: Beta, User: u, On: true}); err != nil {
					t.Errorf("grant %s: %v", u, err)
				}
			}()
		}
		wg.Wait()
		for _, u := range people {
			if !onFor(t, Beta, "hanzo", u) {
				t.Errorf("%s's grant was lost", u)
			}
		}
	})
}

// A GRANT IS AUDITED with what it replaced, and a grant the trail cannot take is not made.
func TestAGrantIsAudited(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := newTestClient(t)
		rec, err := audit.Open(t.TempDir(), "audit-flags", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rec.Close() })
		c.trail = rec
		grantFor(t, Beta, "u_ann", true)
		grantFor(t, Beta, "u_bo", true)
		rows, _, err := rec.Query(context.Background(), audit.Filter{Action: "flag.platform.put"})
		if err != nil || len(rows) != 2 {
			t.Fatalf("trail holds %d grants (%v), want 2", len(rows), err)
		}
		for _, r := range rows {
			if r.Actor.Email != "z@hanzo.ai" || !strings.Contains(string(r.After), "u_ann") {
				t.Fatalf("a grant's record does not say who and what: %+v", r)
			}
		}

		c.trail = audit.Remote("admin", gone{})
		if _, err := grantOp(superCtx, &client.FlagGrantIn{Key: Beta, User: "u_cy", On: true}); err == nil {
			t.Fatal("a grant the trail did not take was made")
		}
		if onFor(t, Beta, "hanzo", "u_cy") {
			t.Fatal("an unaudited grant let someone in")
		}
	})
}

// EVERY APP IN BETA IS A PRINCIPAL SWITCH OF ITS OWN, off with nothing stored, and
// a grant of one is that one alone.
func TestEveryAppInBetaIsASwitchOfItsOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		if len(Betas) == 0 {
			t.Fatal("no app is in beta")
		}
		seen := map[string]bool{}
		for _, b := range Betas {
			key := BetaKey(b.ID)
			if seen[key] {
				t.Fatalf("%s is listed twice", key)
			}
			seen[key] = true
			if d, ok := lookupDef(key); !ok || !d.Principal || d.Label != b.Name {
				t.Fatalf("%s is not registered as a principal switch named %q: %+v", key, b.Name, d)
			}
			if onFor(t, key, "acme", "u1") {
				t.Fatalf("%s is on with nothing stored", key)
			}
		}
		first, second := BetaKey(Betas[0].ID), BetaKey(Betas[1].ID)
		grantFor(t, first, "u1", true)
		if !onFor(t, first, "acme", "u1") || onFor(t, second, "acme", "u1") || onFor(t, Beta, "acme", "u1") {
			t.Fatal("a grant of one app reached another")
		}
	})
}

// mustStored is the definition stored for key.
func mustStored(t *testing.T, key string) []byte {
	t.Helper()
	st, err := mounted.platform()
	if err != nil {
		t.Fatal(err)
	}
	row, found, err := st.Get(key)
	if err != nil || !found {
		t.Fatalf("nothing stored for %s (%v)", key, err)
	}
	return row.Definition
}

// THE CREATIVE MODELS AND ZEN 7 ARE IN BETA, under the ids @hanzo/app's Betas list
// names, so a request for one can be granted from the CRM.
func TestTheModelsInBetaAreListed(t *testing.T) {
	have := map[string]bool{}
	for _, b := range Betas {
		have[b.ID] = true
	}
	for _, id := range []string{"koma", "iro", "mori", "oto", "koe", "uta", "zen7"} {
		if !have[id] {
			t.Fatalf("%s is not in beta: a request for it could not be granted", id)
		}
	}
}

// GOOGLE WORKSPACE IS A PRIVATE BETA until Google verifies the app's Drive and
// Gmail scopes: @hanzo/app draws its connect only for a reader beta.workspace
// opens for, so the switch must exist to be granted.
func TestGoogleWorkspaceIsInBeta(t *testing.T) {
	for _, b := range Betas {
		if b.ID == "workspace" {
			return
		}
	}
	t.Fatal("workspace is not in beta: no reader could be granted Google Workspace")
}
