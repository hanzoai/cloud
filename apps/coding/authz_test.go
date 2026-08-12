package coding

// authz_test.go pins WHO may be handed a run's push credential.
//
// The control this file exists for was once a tautology. delegate authorized
// with the MACHINE client, whose forge identity is a site administrator, so
// forge.Writable asked "may the site admin push here" — true of every
// repository — and any validated member of the org, including a read-only one,
// could name any repository in the namespace and be handed a write key to it.
// The check is now a SUDOED read, so the forge's own ACL applied to the human is
// the answer.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// forgeFor stands up a forge that answers per SUDO ACTOR, and points this
// process at it. writers is the set of logins allowed to push.
func forgeFor(t *testing.T, writers ...string) *[]string {
	t.Helper()
	can := map[string]bool{}
	for _, w := range writers {
		can[w] = true
	}
	var minted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := r.Header.Get("Sudo")
		switch {
		case strings.HasSuffix(r.URL.Path, "/keys") && r.Method == http.MethodPost:
			minted = append(minted, r.URL.Path)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7})
		case strings.HasSuffix(r.URL.Path, "/keys"):
			_ = json.NewEncoder(w).Encode([]any{})
		case strings.HasSuffix(r.URL.Path, "/branch_protections"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"rule_name": "main", "enable_push": false, "enable_force_push": false,
			}})
		default:
			// THE FORGE DECIDES. A machine call (no Sudo) is the site admin and may
			// do anything; a sudoed one gets that person's own access.
			perm := map[string]any{"pull": true, "push": actor == "" || can[actor]}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "cloud", "full_name": "hanzoai/cloud", "default_branch": "main",
				"ssh_url": "git@git.test:hanzoai/cloud.git", "permissions": perm,
			})
		}
	}))
	t.Cleanup(srv.Close)
	credential = forge.Source{Host: srv.URL}
	t.Cleanup(func() { credential = forge.Source{} })

	t.Setenv("ZIP_RUNTIME_DIR", socketDir(t))
	kms := zip.New(zip.Config{AppName: "kms", DisableStartupMessage: true})
	zip.Post[plane.SecretIn, plane.Secret](kms, "/kms/get",
		func(_ context.Context, in *plane.SecretIn) (*plane.Secret, error) {
			switch in.Ref {
			case forge.TokenRef:
				return &plane.Secret{Value: []byte("machine")}, nil
			case forge.HostKeyRef:
				// The CONFIGURED pin. With it there is no handshake and no first use
				// to intercept — which is also why these tests need no SSH server.
				return &plane.Secret{Value: []byte("git.test ssh-ed25519 AAAAPINNED")}, nil
			}
			return nil, zip.ErrNotFound("no such secret")
		}, zip.WithOperationID(plane.KMSGet))
	plane.Bind()
	go func() { _ = kms.Listen(zip.SocketPath("kms")) }()
	t.Cleanup(func() { _ = kms.Shutdown() })
	waitListening(t, "kms")
	return &minted
}

// A MEMBER WHO CANNOT PUSH IS NOT HANDED A KEY, and nothing is registered on the
// repository on the way to refusing them.
func TestDelegate_RefusesAMemberWhoCannotPush(t *testing.T) {
	minted := forgeFor(t, "zoe") // zoe may push; nobody else may
	ctx := cloud.For(context.Background(), "hanzo")

	if _, err := delegate(ctx, "hanzo", "reader", "cloud", "sess_1"); err == nil {
		t.Fatal("a read-only member was handed a write key to hanzoai/cloud")
	}
	if len(*minted) != 0 {
		t.Fatalf("a refused delegation still registered a key: %v", *minted)
	}

	// And the person who CAN push still gets one — a control that refuses
	// everybody is not a control, it is an outage.
	if _, err := delegate(ctx, "hanzo", "zoe", "cloud", "sess_1"); err != nil {
		t.Fatalf("a writer was refused: %v", err)
	}
	if len(*minted) != 1 {
		t.Fatalf("the writer's key was not registered: %v", *minted)
	}
}

// NO ACTOR IS A REFUSAL, never a fall back to the machine — the machine is a
// site administrator, so falling back IS the escalation.
func TestDelegate_RefusesWithNoActor(t *testing.T) {
	minted := forgeFor(t, "zoe")
	ctx := cloud.For(context.Background(), "hanzo")

	if _, err := delegate(ctx, "hanzo", "", "cloud", "sess_1"); err == nil {
		t.Fatal("a run with no forge identity was handed a key on the machine's authority")
	}
	if len(*minted) != 0 {
		t.Fatalf("a keyless delegation still registered a key: %v", *minted)
	}
}

// AN ORG WITH NO FORGE NAMESPACE IS REFUSED BEFORE ANYTHING IS ASKED. This is
// the cross-tenant hole: an unmapped IAM org used to become a forge namespace by
// being spelled one, so signing up as `hanzoai` reached the estate's own repos.
func TestDelegate_RefusesAnOrgWithNoForgeNamespace(t *testing.T) {
	minted := forgeFor(t, "zoe")
	ctx := cloud.For(context.Background(), "hanzoai")

	// `zoe` is a writer, and the repo and protection are fine. The ONLY thing
	// wrong is the org, and it must be enough.
	_, err := delegate(ctx, "hanzoai", "zoe", "cloud", "sess_1")
	if err == nil {
		t.Fatal("an org named after a forge namespace reached that namespace")
	}
	if !strings.Contains(err.Error(), "no namespace on the forge") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
	if len(*minted) != 0 {
		t.Fatalf("a cross-tenant delegation registered a key: %v", *minted)
	}
}
