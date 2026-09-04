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

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/zap-proto/zip"
)

// iamAddr is what the identity store answers about each subject, keyed by the
// caller's subject exactly as the real op keys it. A test states the rows it
// needs before standing the peers up.
var iamAddr map[string]client.Email

// forgeFor stands up a forge that answers per SUDO ACTOR, and points this
// process at it. writers is the set of logins allowed to push.
func forgeFor(t *testing.T, writers ...string) *[]string {
	t.Helper()
	can := map[string]bool{}
	for _, w := range writers {
		can[w] = true
	}
	// Every writer owns the address their login derives from. A test that needs
	// the two to DISAGREE (the escalation) states its own table.
	return forgeAs(t, can, func(login string) string { return login + "@hanzo.ai" })
}

// forgeAs is forgeFor with the login→address table stated, so a test can make a
// derived login belong to somebody else.
func forgeAs(t *testing.T, can map[string]bool, addr func(login string) string) *[]string {
	t.Helper()
	var minted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := r.Header.Get("Sudo")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/users/"):
			login := strings.TrimPrefix(r.URL.Path, "/v1/users/")
			a := addr(login)
			if a == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"login": login, "email": a})
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
	// CLOUD_FORGE_HOST is the documented override for a deployment whose forge is
	// not the sibling of its own domain — a developer box, a staging forge, and
	// this. The held credential is dropped either side so no test is answered by
	// the forge another one stood up.
	t.Setenv("CLOUD_FORGE_HOST", srv.URL)
	forge.Invalidate()
	t.Cleanup(forge.Invalidate)

	t.Setenv("ZIP_RUNTIME_DIR", socketDir(t))
	// The identity store, answering about the CALLER. Every test address here is
	// confirmed unless the test states otherwise — an unverified one is its own
	// case (TestResolveActor_RefusesAnUnverifiedAddress).
	iamApp := zip.New(zip.Config{AppName: "iam", DisableStartupMessage: true})
	zip.Post[struct{}, client.Email](iamApp, "/iam/email",
		func(ctx context.Context, _ *struct{}) (*client.Email, error) {
			a, ok := iamAddr[zip.CallerOf(ctx).User]
			if !ok {
				return nil, zip.ErrUnauthorized("no such subject")
			}
			return &a, nil
		}, zip.WithOperationID(client.IAMEmail))
	kms := zip.New(zip.Config{AppName: "kms", DisableStartupMessage: true})
	zip.Post[client.SecretIn, client.Secret](kms, "/kms/get",
		func(_ context.Context, in *client.SecretIn) (*client.Secret, error) {
			switch in.Ref {
			case forge.TokenRef:
				return &client.Secret{Value: []byte("machine")}, nil
			case forge.HostKeyRef:
				// The CONFIGURED pin. With it there is no handshake and no first use
				// to intercept — which is also why these tests need no SSH server.
				return &client.Secret{Value: []byte("git.test ssh-ed25519 AAAAPINNED")}, nil
			}
			return nil, zip.ErrNotFound("no such secret")
		}, zip.WithOperationID(client.KMSGet))
	client.Bind()
	for name, app := range map[string]*zip.App{"kms": kms, "iam": iamApp} {
		app := app
		go func(path string) { _ = app.Listen(path) }(zip.SocketPath(name))
		t.Cleanup(func() { _ = app.Shutdown() })
		waitListening(t, name)
	}
	return &minted
}

// A MEMBER WHO CANNOT PUSH IS NOT HANDED A KEY, and nothing is registered on the
// repository on the way to refusing them.
func TestDelegate_RefusesAMemberWhoCannotPush(t *testing.T) {
	minted := forgeFor(t, "zoe") // zoe may push; nobody else may
	iamAddr = map[string]client.Email{"sub": verified("zoe@hanzo.ai")}
	ctx := asCaller("sub")

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
	iamAddr = map[string]client.Email{"sub": verified("zoe@hanzo.ai")}
	ctx := asCaller("sub")

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
	iamAddr = map[string]client.Email{"sub": verified("zoe@hanzo.ai")}
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzoai", User: "sub"})

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
