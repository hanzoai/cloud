package cloud

import (
	"net/http"
	"testing"

	"github.com/hanzoai/beego/v2/server/web"
)

// The EMBED session→principal bridge (validatedPrincipal → sessionAccessToken) must
// be a SAFE NO-OP whenever there is no in-process IAM session manager — i.e. on any
// binary that does not mount clients/iamsvc (and in these tests, where GlobalSessions
// is never initialised). A first-party-session cookie present WITHOUT a bearer and
// WITHOUT a session manager must resolve ANONYMOUS (never a principal, never a panic),
// so the bridge can never widen auth on a non-embed deployment.
func TestSessionBridge_NoSessionManager_IsAnonymous(t *testing.T) {
	if web.GlobalSessions != nil {
		t.Skip("a session manager is initialised in this process; the no-op path is not exercised")
	}
	name := web.BConfig.WebConfig.Session.SessionName
	if name == "" {
		name = "beegosessionID"
	}

	app, got := newIdentityApp(t, nil) // nil validator: no JWT path can validate either
	probe(t, app, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: name, Value: "deadbeefdeadbeefdeadbeefdeadbeef"})
	})

	if got.user != "" || got.org != "" || got.admin {
		t.Fatalf("session cookie without a session manager must be anonymous; got user=%q org=%q admin=%v",
			got.user, got.org, got.admin)
	}
}
