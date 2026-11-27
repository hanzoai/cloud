package flags

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/authz"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// worlds is a manifest row and machines a surface; both are alpha, and a key
// that gates nothing is no stage.
func TestTheStagedKeysAreDefined(t *testing.T) {
	for key, want := range map[string]string{"worlds": "alpha", "machines": "alpha", "waitlist_open": "", "new-editor": ""} {
		if got := Stage(key); got != want {
			t.Errorf("Stage(%q) = %q, want %q", key, got, want)
		}
	}
}

// mountTrailed is mountHTTP with an audit trail to write to.
func mountTrailed(t *testing.T) (*zip.App, *audit.Recorder) {
	t.Helper()
	rec, err := audit.Open(t.TempDir(), "audit-flags", nil)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return mountOn(t, rec), rec
}

// mountOn is mountHTTP writing to rec.
func mountOn(t *testing.T, rec *audit.Recorder) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, &cloud.Service[state]{Base: cloud.NewBase(cloud.Deps{}, "flags"), State: state{client: newTestClient(t), audit: rec}})
	return app
}

// gone is the trail as this process reaches it when admin, which holds it, does
// not answer.
type gone struct{}

func (gone) Append(context.Context, audit.Record) (audit.Record, error) {
	return audit.Record{}, errors.New("admin is not running")
}
func (gone) Query(context.Context, audit.Filter) ([]audit.Record, int, error) {
	return nil, 0, errors.New("admin is not running")
}

// A STAGED WRITE THE TRAIL DID NOT TAKE IS REFUSED, AND NOTHING LANDS. flags holds
// no chain: its records go to admin's, the one /v1/admin/audit reads, and a
// SuperAdmin letting an org into an alpha with no record of it is the write this
// rule exists to stop.
func TestAStagedWriteTheTrailDidNotTakeIsRefused(t *testing.T) {
	app := mountOn(t, audit.Remote("admin", gone{}))
	if code := as(t, app, "super", http.MethodPut, "/v1/flag/defs/worlds", "hanzo", on); code != http.StatusServiceUnavailable {
		t.Fatalf("a staged write with no trail to take it answered %d, want 503", code)
	}
	if holds(t, "hanzo", "worlds") {
		t.Fatal("a write the trail did not record let hanzo into worlds")
	}
}

// as drives one request from a caller of org holding the named admin bit:
// "member", "orgadmin" or "owner" (the one org-admin bit, which the identity
// boundary mints for either role), or "super" (a SuperAdmin acting in org).
func as(t *testing.T, app *zip.App, role, method, path, org string, body string) int {
	t.Helper()
	rq := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		rq.Header.Set("Content-Type", "application/json")
	}
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", "u_"+role)
	switch role {
	case "orgadmin", "owner":
		rq.Header.Set("X-User-IsOrgAdmin", "true")
	case "super":
		rq.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Test(rq, httpCfg)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// holds is what cloud.Stage is told when it asks whether org holds key.
func holds(t *testing.T, org, key string) bool {
	t.Helper()
	got, err := hold(zip.WithCaller(context.Background(), zip.Caller{Org: org}), &client.FlagIn{Key: key})
	if err != nil {
		t.Fatalf("hold %s/%s: %v", org, key, err)
	}
	return got.On
}

const on = `{"key":"worlds","active":true,"filters":{"groups":[{"rollout_percentage":100}]}}`

// AN ORG IS LET INTO AN ALPHA BY A SUPERADMIN ALONE. A member and an admin of the
// org are refused 403 and nothing lands — neither the definition nor a trail row
// — while a SuperAdmin acting in the org sets it, the stage then answers the org
// in, and the write is on the trail. Removing it is the SuperAdmin's too. Every
// other key is the org's to set, as it always was.
func TestAStagedKeyIsASuperAdminsToSet(t *testing.T) {
	app, rec := mountTrailed(t)
	for _, key := range []string{"worlds", "machines"} {
		for _, role := range []string{"member", "orgadmin"} {
			if code := as(t, app, role, http.MethodPut, "/v1/flag/defs/"+key, "hanzo", on); code != http.StatusForbidden {
				t.Fatalf("%s set %s: %d, want 403", role, key, code)
			}
		}
		if holds(t, "hanzo", key) {
			t.Fatalf("a refused write let hanzo into %s", key)
		}
	}
	if rows, _, _ := rec.Query(context.Background(), audit.Filter{Org: "hanzo"}); len(rows) != 0 {
		t.Fatalf("a refused write reached the trail: %+v", rows)
	}

	for _, key := range []string{"worlds", "machines"} {
		if code := as(t, app, "super", http.MethodPut, "/v1/flag/defs/"+key, "hanzo", on); code != http.StatusOK {
			t.Fatalf("SuperAdmin set %s: %d", key, code)
		}
		if !holds(t, "hanzo", key) {
			t.Fatalf("the SuperAdmin's write did not let hanzo into %s", key)
		}
	}
	if holds(t, "acme", "worlds") {
		t.Fatal("hanzo's grant reached another org")
	}
	if code := as(t, app, "orgadmin", http.MethodDelete, "/v1/flag/defs/worlds", "hanzo", ""); code != http.StatusForbidden {
		t.Fatalf("an org admin removed a staged key: %d", code)
	}
	if code := as(t, app, "super", http.MethodDelete, "/v1/flag/defs/machines", "hanzo", ""); code != http.StatusOK {
		t.Fatalf("SuperAdmin removed machines: %d", code)
	}
	rows, _, err := rec.Query(context.Background(), audit.Filter{Org: "hanzo"})
	if err != nil {
		t.Fatal(err)
	}
	trail := map[string]int{}
	for _, r := range rows {
		trail[r.Action+" "+r.Resource.ID]++
		if r.Actor.Sub != "u_super" {
			t.Fatalf("the trail names %q, not the SuperAdmin", r.Actor.Sub)
		}
	}
	want := map[string]int{"flag.stage.put worlds": 1, "flag.stage.put machines": 1, "flag.stage.delete machines": 1}
	if len(trail) != len(want) {
		t.Fatalf("trail %v, want %v", trail, want)
	}
	for k, n := range want {
		if trail[k] != n {
			t.Fatalf("trail %v, want %v", trail, want)
		}
	}

	if code := as(t, app, "orgadmin", http.MethodPut, "/v1/flag/defs/new-editor", "hanzo", `{"active":true}`); code != http.StatusOK {
		t.Fatalf("an org admin's own flag: %d", code)
	}
}

// A FLAG THAT IS NOT STAGED IS STILL THE WHOLE ORG'S BEHAVIOR, so writing or
// removing one is an owner's or admin's of that org, or a SuperAdmin's. A member
// is refused 403 with the reason, and nothing lands; reading stays every
// member's.
func TestOnlyAnOrgAdminWritesAFlag(t *testing.T) {
	app, _ := mountTrailed(t)
	const doc = `{"active":true}`
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		body := doc
		if method == http.MethodDelete {
			body = ""
		}
		rq := httptest.NewRequest(method, "/v1/flag/defs/new-editor", strings.NewReader(body))
		rq.Header.Set("Content-Type", "application/json")
		rq.Header.Set("X-Org-Id", "hanzo")
		rq.Header.Set("X-User-Id", "u_member")
		resp, err := app.Test(rq, httpCfg)
		if err != nil {
			t.Fatal(err)
		}
		said, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("a member's %s: %d, want 403", method, resp.StatusCode)
		}
		if !strings.Contains(string(said), "owner's or admin's of the org") {
			t.Fatalf("a member's %s is refused without the reason: %s", method, said)
		}
	}
	if holds(t, "hanzo", "new-editor") {
		t.Fatal("a member's refused write landed")
	}

	for _, role := range []string{"orgadmin", "owner", "super"} {
		if code := as(t, app, role, http.MethodPut, "/v1/flag/defs/new-editor", "hanzo", doc); code != http.StatusOK {
			t.Fatalf("%s set new-editor: %d, want 200", role, code)
		}
		if code := as(t, app, role, http.MethodDelete, "/v1/flag/defs/new-editor", "hanzo", ""); code != http.StatusOK {
			t.Fatalf("%s removed new-editor: %d, want 200", role, code)
		}
	}
	if code := as(t, app, "orgadmin", http.MethodPut, "/v1/flag/defs/new-editor", "hanzo", doc); code != http.StatusOK {
		t.Fatalf("seed: %d", code)
	}
	for _, path := range []string{"/v1/flag/defs", "/v1/flag/defs/new-editor"} {
		if code := as(t, app, "member", http.MethodGet, path, "hanzo", ""); code != http.StatusOK {
			t.Fatalf("a member's read of %s: %d, want 200", path, code)
		}
	}

	// A staged key stays a SuperAdmin's: an org admin, owner included, is refused.
	for _, role := range []string{"orgadmin", "owner"} {
		if code := as(t, app, role, http.MethodPut, "/v1/flag/defs/worlds", "hanzo", on); code != http.StatusForbidden {
			t.Fatalf("%s set worlds: %d, want 403", role, code)
		}
	}
	if code := as(t, app, "super", http.MethodPut, "/v1/flag/defs/worlds", "hanzo", on); code != http.StatusOK {
		t.Fatalf("SuperAdmin set worlds: %d, want 200", code)
	}
}

// The org-admin bit the boundary mints (authz.Claims.OrgAdmin) is an owner's and
// an admin's of THAT org, never a member's, and never an admin's of another org.
func TestTheOrgAdminBitIsOwnerOrAdminOfThatOrg(t *testing.T) {
	for _, tc := range []struct {
		role authz.Role
		org  string
		want bool
	}{
		{authz.Owner, "hanzo", true},
		{authz.Admin, "hanzo", true},
		{authz.Member, "hanzo", false},
		{authz.Admin, "acme", false},
	} {
		c := &authz.Claims{Orgs: []authz.Membership{{Org: "hanzo", Role: tc.role}}}
		if got := c.OrgAdmin(tc.org); got != tc.want {
			t.Errorf("%s of hanzo administers %s: %v, want %v", tc.role, tc.org, got, tc.want)
		}
	}
}

// The composing write path (the experiments primitive) never writes a staged key.
func TestPutDefRefusesAStagedKey(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		def := json.RawMessage(on)
		if err := PutDef("hanzo", "", "worlds", def, "u"); err == nil {
			t.Fatal("PutDef wrote a staged key")
		}
		if holds(t, "hanzo", "worlds") {
			t.Fatal("PutDef let hanzo into worlds")
		}
		if err := PutDef("hanzo", "", "exp_1", def, "u"); err != nil {
			t.Fatalf("PutDef of an ordinary key: %v", err)
		}
	})
}
