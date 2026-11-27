package agents

// person_test.go pins that in a held org a machine is its owner's as one person,
// whichever credential they call with: one linked with an API key is theirs
// signed in, one an API key of theirs linked before machines were keyed on the
// person is theirs from both and moves to their id at its next touch, and an
// API key whose person IAM cannot say is told so rather than answered as nobody.

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/zap-proto/zip"
)

// people serves IAM's person read: each subject in rows is answered its row, and
// any other is no such subject.
func people(t *testing.T, rows map[string]iam.Self) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	a := zip.New(zip.Config{AppName: "iam", DisableStartupMessage: true})
	a.Post("/iam/self", func(ctx context.Context, _ *struct{}) (*iam.Self, error) {
		p, ok := rows[zip.CallerOf(ctx).User]
		if !ok {
			return nil, zip.ErrUnauthorized("no such subject")
		}
		return &p, nil
	}, zip.WithOperationID(iam.IAMSelf))
	at := zip.SocketPath("iam")
	go func() { _ = a.Listen(at) }()
	t.Cleanup(func() { _ = a.Shutdown() })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if c, err := net.Dial("unix", at); err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never accepted", at)
		}
	}
}

// ONE PERSON, ONE OWNER. Kim signs in as her IAM row's id and calls with an API
// key whose subject is her row. A machine she links with the key is keyed on her
// id and is hers signed in; a machine her key linked before, keyed on her row, is
// hers from both, is written under her id at its next touch, and is found again
// by a re-link of its host rather than linked twice; a stranger reaches none.
func TestAHeldOrgsMachineIsItsOwnersWhicheverCredential(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		kim := iam.Self{ID: "uuid-kim", Row: "hanzo/kim"}
		people(t, map[string]iam.Self{
			"uuid-kim": kim, "hanzo/kim": kim,
			"uuid-lee": {ID: "uuid-lee", Row: "hanzo/lee"},
		})
		app := mountApp(t, &fakeAI{content: "x"})
		now := time.Now().Unix()
		owned := func(id string) string {
			t.Helper()
			got, err := storeOf(t, &mounted.State, "hanzo").GetTarget(context.Background(), "hanzo", id)
			if err != nil {
				t.Fatal(err)
			}
			return got.Owner
		}
		listed := func(user string) []string {
			t.Helper()
			code, body := reqAs(t, app, "GET", "/v1/agent/targets", "hanzo", user, false, "", nil)
			if code != 200 {
				t.Fatalf("list as %s: %d %s", user, code, body)
			}
			var out struct {
				Targets []struct {
					ID string `json:"id"`
				} `json:"targets"`
			}
			_ = json.Unmarshal(body, &out)
			ids := []string{}
			for _, tv := range out.Targets {
				ids = append(ids, tv.ID)
			}
			slices.Sort(ids)
			return ids
		}

		linked := registerAs(t, app, "hanzo", "hanzo/kim", "kim-mbp")
		if got := owned(linked); got != "uuid-kim" {
			t.Fatalf("a machine her API key linked is owned by %q, want her id", got)
		}
		seed(t, "hanzo", "tgt_old", "hanzo/kim", "old", "old-box", now)
		seed(t, "hanzo", "tgt_spark", "hanzo/kim", "spark", "spark", now)
		want := []string{linked, "tgt_old", "tgt_spark"}
		slices.Sort(want)
		for _, user := range []string{"uuid-kim", "hanzo/kim"} {
			if got := listed(user); !slices.Equal(got, want) {
				t.Fatalf("%s lists %v, want her three machines %v", user, got, want)
			}
		}
		if got := listed("uuid-lee"); len(got) != 0 {
			t.Fatalf("a stranger lists %v", got)
		}
		if code, _ := reqAs(t, app, "GET", "/v1/agent/targets/tgt_old", "hanzo", "uuid-lee", false, "", nil); code != 404 {
			t.Fatalf("a stranger reads her machine: %d", code)
		}
		// In process, as a coding run asks: one person's machines whichever subject.
		for _, user := range []string{"uuid-kim", "hanzo/kim"} {
			got, err := TargetsForOrg(as("hanzo", user, false, false), "hanzo")
			if err != nil || len(got) != 3 {
				t.Fatalf("TargetsForOrg as %s = %v %v, want her three", user, ids(got), err)
			}
			if _, err := ResolveTarget(as("hanzo", user, false, false), "hanzo", "spark"); err != nil {
				t.Fatalf("`on spark` as %s: %v", user, err)
			}
		}

		// Her next touch — signed in, minting the daemon's key — writes it under her id.
		if code, body := reqAs(t, app, "POST", "/v1/agent/targets/tgt_old/key", "hanzo", "uuid-kim", false, "", nil); code != 200 {
			t.Fatalf("mint signed in on the machine her key linked: %d %s", code, body)
		}
		if got := owned("tgt_old"); got != "uuid-kim" {
			t.Fatalf("after her touch the machine is owned by %q, want her id", got)
		}
		// A re-link of the host finds the machine her key linked, and writes her id.
		if id := registerAs(t, app, "hanzo", "uuid-kim", "spark"); id != "tgt_spark" {
			t.Fatalf("re-linking spark signed in made %s, want the machine her key linked", id)
		}
		if got := owned("tgt_spark"); got != "uuid-kim" {
			t.Fatalf("after the re-link spark is owned by %q, want her id", got)
		}

		// An API key whose person IAM cannot say is told so, and owns nothing by name.
		if code, body := reqAs(t, app, "GET", "/v1/agent/targets", "hanzo", "hanzo/ghost", false, "", nil); code != 503 {
			t.Fatalf("a key whose person cannot be read = %d %s, want 503", code, body)
		}
		if _, err := ResolveTarget(as("hanzo", "hanzo/ghost", false, false), "hanzo", "spark"); err == nil {
			t.Fatal("a key whose person cannot be read resolved a machine")
		}

	})
}
