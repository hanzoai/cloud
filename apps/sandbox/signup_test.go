package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// IN THE SIGNUP ORG A PROJECT IS THE PERSON'S. Everybody who signs up acts in
// account.SignupOrg, and a workspace names everybody's first machine the same
// way, so a project keyed on the org alone was one disk for strangers: the
// second person was refused while the first held it, and given the first's files
// once they let it go.
func TestTwoPeopleInTheSignupOrgEachGetTheirOwnProject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := owning(t)
		const body = `{"class":"exec","project":"cloud-1"}`
		got := map[string]Sandbox{}
		for _, who := range []member{alice, bob} {
			code, b := asIn(t, app, account.SignupOrg, who, http.MethodPost, "/v1/sandbox", body)
			if code != http.StatusCreated {
				t.Fatalf("%s leasing project cloud-1 in the signup org = %d %s, want 201", who.user, code, b)
			}
			var m Sandbox
			if err := json.Unmarshal(b, &m); err != nil || m.ID == "" {
				t.Fatalf("lease reply %s: %v", b, err)
			}
			got[who.user] = m
		}
		a, b := got["alice"], got["bob"]
		if a.Volume == "" || a.Volume == b.Volume {
			t.Fatalf("alice and bob share a disk: %q and %q", a.Volume, b.Volume)
		}
		if shared := volumeName(account.SignupOrg, "", "cloud-1"); a.Volume == shared || b.Volume == shared {
			t.Fatalf("a person's project disk is the org's %q", shared)
		}

		// Single-attach still holds, per person: asking again names their own.
		code, again := asIn(t, app, account.SignupOrg, alice, http.MethodPost, "/v1/sandbox", body)
		if code != http.StatusConflict || !strings.Contains(string(again), a.ID) || strings.Contains(string(again), b.ID) {
			t.Fatalf("alice asking again = %d %s, want 409 naming %s and not %s", code, again, a.ID, b.ID)
		}
	})
}

// AN ORG'S DISKS KEEP THEIR NAMES. A project of an org of colleagues hashes the
// org and the project exactly as it always did, so no customer's disk moves.
func TestAnOrgsProjectDiskKeepsItsName(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sum := sha256.Sum256([]byte("acme\x00atlas"))
		if got, want := volumeName("acme", "", "atlas"), "m-acme-atlas-"+hex.EncodeToString(sum[:5]); got != want {
			t.Fatalf("volumeName = %q, want %q", got, want)
		}
		if projectOwner("acme", "alice") != "" || projectOwner(account.SignupOrg, "alice") != "alice" {
			t.Fatal("a project is the org's in acme and the person's in the signup org")
		}
	})
}

// A PERSON'S DISK NAME IS 128 BITS WIDE. Every person's disk on one project
// shares its readable head, so the hash alone keeps strangers apart; 40 bits of
// it collide near a million sign-ups.
func TestAPersonsDiskNameIsWideEnoughForEverySignup(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sum := sha256.Sum256([]byte(account.SignupOrg + "\x00alice\x00cloud-1"))
		got := volumeName(account.SignupOrg, "alice", "cloud-1")
		if want := "-" + hex.EncodeToString(sum[:16]); !strings.HasSuffix(got, want) || len(got) > 63 {
			t.Fatalf("volumeName = %q, want a %d-character name ending %q", got, len(got), want)
		}
		long := volumeName(account.SignupOrg, "alice", strings.Repeat("p", 80))
		if len(long) > 63 {
			t.Fatalf("a long project's disk name is %d characters: %q", len(long), long)
		}
	})
}

// AN ADMIN OF THE SIGNUP ORG HOLDS NO STRANGER'S SANDBOX. Its members are
// customers of their own, so an org admin there reaches another's sandbox no
// more than any member does: not its row, its shell, its files or its screen.
func TestASignupOrgAdminHoldsNoStrangersSandbox(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := owning(t)
		code, b := asIn(t, app, account.SignupOrg, alice, http.MethodPost, "/v1/sandbox", `{"class":"exec","project":"cloud-1"}`)
		if code != http.StatusCreated {
			t.Fatalf("alice's lease = %d %s", code, b)
		}
		var m Sandbox
		if err := json.Unmarshal(b, &m); err != nil || m.ID == "" {
			t.Fatalf("lease reply %s: %v", b, err)
		}
		staff := member{user: "staffer", orgAdmin: true}
		for _, c := range []struct{ method, path, body string }{
			{http.MethodGet, "/v1/sandbox/" + m.ID, ""},
			{http.MethodPost, "/v1/sandbox/" + m.ID + "/exec", `{"argv":["cat","/work/secret"]}`},
			{http.MethodPost, "/v1/sandbox/" + m.ID + "/terminal/ticket", ""},
		} {
			if code, body := asIn(t, app, account.SignupOrg, staff, c.method, c.path, c.body); code != http.StatusNotFound {
				t.Fatalf("signup-org admin %s %s = %d %.160s, want 404", c.method, c.path, code, body)
			}
		}
		if code, body := asIn(t, app, account.SignupOrg, staff, http.MethodGet, "/v1/sandbox", ""); code != http.StatusOK || strings.Contains(string(body), m.ID) {
			t.Fatalf("signup-org admin's list = %d %.200s, want 200 without %s", code, body, m.ID)
		}
		if !(holder{orgAdmin: true}).adminOf("acme") || (holder{orgAdmin: true}).adminOf(account.SignupOrg) || !(holder{super: true}).adminOf(account.SignupOrg) {
			t.Fatal("an org admin administers acme, not the signup org; a SuperAdmin administers both")
		}
	})
}

// A LEASE THAT NAMES NO PERSON TAKES NO PROJECT IN THE SIGNUP ORG: with no
// owner it would land on the org-wide disk strangers' projects once shared.
func TestTheSignupOrgRefusesAProjectToNobody(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		owning(t)
		_, err := planeLease(cloud.For(context.Background(), account.SignupOrg), &client.LeaseIn{Class: "exec", Project: "cloud-1"})
		if err == nil || !strings.Contains(err.Error(), "belongs to a person") {
			t.Fatalf("a project lease with no person in the signup org = %v, want refused as nobody's", err)
		}
	})
}

// ATTACH AND RESUME FIND THE PERSON'S OWN. A stranger's sandbox on the same
// project name is neither the one attach stamps nor one that blocks a resume.
func TestAttachAndResumeInTheSignupOrgFindTheirOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := owning(t)
		org := account.SignupOrg
		st, _ := storeFor(s, org)
		a := leasedBy(t, s, org, "sbx_a", "alice")
		b := leasedBy(t, s, org, "sbx_b", "bob")
		for _, id := range []string{a.ID, b.ID} {
			if _, err := st.db.Exec(`UPDATE sandbox SET project='cloud-1' WHERE org=$1 AND id=$2`, org, id); err != nil {
				t.Fatal(err)
			}
		}
		got, err := planeAttach(zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "bob"}), &client.AttachIn{Project: "cloud-1"})
		if err != nil || got.ID != b.ID {
			t.Fatalf("bob's attach = %+v %v, want his own %s", got, err, b.ID)
		}

		now := time.Now().Unix()
		for _, who := range []string{"carol", "dave"} {
			m := Sandbox{ID: "m_" + who, Org: org, Kind: KindSandbox, Actor: who, Class: "dev",
				Project: "cloud-2", Volume: volumeName(org, who, "cloud-2"), Status: statusParked, Pod: podName("m_" + who),
				CreatedAt: now, LastUsedAt: now, ExpiresAt: now + 3600}
			if err := st.Put(context.Background(), m); err != nil {
				t.Fatal(err)
			}
		}
		dave := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "dave"})
		if got, err := Lease(s, dave, org, account.Org(org), false, "", Spec{ID: "m_dave"}); err != nil || got.Status != statusRunning {
			t.Fatalf("dave's resume beside carol's parked sandbox = %+v %v", got, err)
		}
	})
}
