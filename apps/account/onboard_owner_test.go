package account

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/iam/pkg/schema"
)

// A second org is reached through the org switcher, and the switch admits only an
// org named in the caller's signed `orgs` set: their home org plus their IAM
// membership rows. An org that writes no membership row is therefore an
// org its creator can never enter. These tests hold the row, its shape, and what
// happens to the org when the row cannot be written.

// bob is a signed-in person on the direct-Bearer path: X-User-Id is the UUID
// subject, X-User-Name the IAM username, X-User-Owner the home org from orgs[0].
func bob(actingIn string) map[string]string {
	return map[string]string{
		"X-User-Id":    "0b6e2f5c-4a8d-4f3e-9c21-7d5a1e9b3c40",
		"X-User-Name":  "bob",
		"X-User-Owner": "acme",
		"X-Org-Id":     actingIn,
	}
}

func TestOnboard_CreatorBecomesOwner(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// The body tries to name a founder, a personal flag and an owner of its own.
	// None of it may reach IAM: the member comes from the validated token.
	code, body := callH(t, app, http.MethodPost, "/v1/account/orgs", bob("acme"),
		`{"name":"Side Project","founder":"mallory","isPersonal":true,"user":"acme/mallory","owner":"evil"}`)
	if code != http.StatusOK {
		t.Fatalf("create org: want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if resp.Org != "side-project" {
		t.Fatalf("resp = %+v, want the org side-project", resp)
	}

	want := []map[string]string{{"user": "acme/bob", "org": "side-project", "role": "owner"}}
	if len(f.granted) != 1 || !maps.Equal(f.granted[0], want[0]) {
		t.Fatalf("membership writes = %v, want exactly %v", f.granted, want)
	}
	if f.roles["acme/bob|side-project"] != "owner" {
		t.Fatalf("creator's role = %q, want owner", f.roles["acme/bob|side-project"])
	}

	row := f.createdOrgs[0]
	if row["owner"] != adminOrg {
		t.Fatalf("org filed under %v, want %q", row["owner"], adminOrg)
	}
	if _, present := row["founder"]; present {
		t.Fatalf("org create carried a founder %v; the request body must never name one", row["founder"])
	}
	if personal, _ := row["isPersonal"].(bool); personal {
		t.Fatalf("an org asked for by name is not personal: %v", row)
	}
	if len(f.rows) != 0 {
		t.Fatalf("creating an org never writes its creator's row, rows=%v", f.rows)
	}
}

// A person acting in a team org they joined by membership is recorded under their
// OWN home org, not the org they happen to be acting in.
func TestOnboard_MemberIsRecordedByHome(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := callH(t, app, http.MethodPost, "/v1/account/orgs", bob("team"), `{"name":"Side Project"}`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	if len(f.granted) != 1 || f.granted[0]["user"] != "acme/bob" {
		t.Fatalf("membership writes = %v, want the creator as acme/bob", f.granted)
	}
}

// IAM refused the row, so nothing was written and the org goes at once.
func TestOnboard_RefusedOwnerLeavesNoOrg(t *testing.T) {
	f := newFakeIAM()
	f.failMembership = true
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := callH(t, app, http.MethodPost, "/v1/account/orgs", bob("acme"), `{"name":"Side Project"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("want 502 when the owner cannot be recorded, got %d (%s)", code, body)
	}
	if _, present := f.orgs["side-project"]; present {
		t.Fatalf("org left behind with nobody in it")
	}
	wantTrail := []string{"ensure acme/bob|side-project", "delete-org side-project"}
	if !slices.Equal(f.trail, wantTrail) {
		t.Fatalf("calls = %v, want %v", f.trail, wantTrail)
	}
}

// The row landed but its answer did not. The revoke is asked first, because a row
// that outlived its org would seat bob in the next org of that name; IAM refuses
// to remove an org's last owner, so the org stays, owned by the person who made
// it, which is safe.
func TestOnboard_LostAnswerKeepsTheOwnedOrg(t *testing.T) {
	f := newFakeIAM()
	f.lostMembership = true
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := callH(t, app, http.MethodPost, "/v1/account/orgs", bob("acme"), `{"name":"Side Project"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d (%s)", code, body)
	}
	wantTrail := []string{"ensure acme/bob|side-project", "revoke acme/bob|side-project"}
	if !slices.Equal(f.trail, wantTrail) {
		t.Fatalf("calls = %v, want %v", f.trail, wantTrail)
	}
	if _, present := f.orgs["side-project"]; !present || !f.members["acme/bob|side-project"] {
		t.Fatalf("the org and its owner row must both stand, orgs=%v members=%v", f.orgs, f.members)
	}
}

// When the revoke fails as well, the org stays: deleting it would strand a row
// that may have landed. It stays owned by its creator, which is safe.
func TestOnboard_UnrevokedRowKeepsTheOrg(t *testing.T) {
	f := newFakeIAM()
	f.lostMembership = true
	f.failRevoke = true
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := callH(t, app, http.MethodPost, "/v1/account/orgs", bob("acme"), `{"name":"Side Project"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d (%s)", code, body)
	}
	if len(f.deletedOrgs) != 0 {
		t.Fatalf("org deleted while its membership row may stand: %v", f.deletedOrgs)
	}
	if _, present := f.orgs["side-project"]; !present || !f.members["acme/bob|side-project"] {
		t.Fatalf("org and its owner row must both stand, orgs=%v members=%v", f.orgs, f.members)
	}
}

// The name a switcher shows is bounded and visible: no controls, bidi overrides or
// zero-width characters, which let one org's name read as another's.
func TestOnboard_DisplayNameIsBoundedAndVisible(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	for _, name := range []string{
		"Acme\u202eeVIL", "Ac\u200bme", "Acme\u2066x\u2069", "\ufeffAcme", "Ac\nme", "Ac\u2028me", "Ac\u2029me", "\u3164", "Acme\u3164", "\u2800", strings.Repeat("a", schema.MaxOrgDisplayName+1),
	} {
		body, _ := json.Marshal(map[string]string{"name": name})
		if code, got := callH(t, app, http.MethodPost, "/v1/account/orgs", bob("acme"), string(body)); code != http.StatusBadRequest {
			t.Fatalf("name %q: want 400, got %d (%s)", name, code, got)
		}
	}
	if len(f.createdOrgs) != 0 {
		t.Fatalf("IAM was written: %v", f.createdOrgs)
	}
}

// Creating an org is capped per person like the other credential writes, and the
// cap counts before IAM is asked anything. It is one budget per person: switching
// the org they act in does not buy another.
func TestOnboard_IsFrequencyCapped(t *testing.T) {
	f := newFakeIAM()
	f.orgs["taken"] = map[string]any{"owner": adminOrg, "name": "taken"}
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	for i := range keysWriteRatePerMin + 1 {
		code, _ := callH(t, app, http.MethodPost, "/v1/account/orgs", bob(fmt.Sprintf("team-%d", i)), `{"name":"Taken"}`)
		if i < keysWriteRatePerMin && code != http.StatusConflict {
			t.Fatalf("request %d: want 409, got %d", i, code)
		}
		if i == keysWriteRatePerMin && code != http.StatusTooManyRequests {
			t.Fatalf("request %d: want 429, got %d", i, code)
		}
	}
}
