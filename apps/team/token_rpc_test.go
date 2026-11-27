package team

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/client"
	teampeer "github.com/hanzoai/cloud/client/team"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
)

const verdictAccount = "550e8400-e29b-41d4-a716-446655440000"

func mintFor(t *testing.T, org, secret string, extra map[string]any, exp int64) string {
	t.Helper()
	e := map[string]any{}
	if org != "" {
		e["org"] = org
	}
	for k, v := range extra {
		e[k] = v
	}
	tok, err := token.Generate(verdictAccount, "", e, exp, secret)
	if err != nil {
		t.Fatalf("token.Generate: %v", err)
	}
	return tok
}

// TestVerdict is the fail-closed table that used to live in event: every token the
// deployment will not stand behind is the zero verdict, and the ones it does carry
// the signed org, account and capability.
func TestVerdict(t *testing.T) {
	const key = "a-real-team-secret"
	hour := time.Now().Add(time.Hour).Unix()
	cases := []struct {
		name   string
		secret string // the deployment's key
		bearer string
		want   client.Token
	}{
		{"member", key, mintFor(t, "acme", key, map[string]any{"role": token.RoleMember}, hour),
			client.Token{Verified: true, Org: "acme", Account: verdictAccount, Privileged: true}},
		{"owner", key, mintFor(t, "acme", key, map[string]any{"role": token.RoleOwner}, hour),
			client.Token{Verified: true, Org: "acme", Account: verdictAccount, Privileged: true}},
		{"guest is not privileged", key, mintFor(t, "acme", key, map[string]any{"role": token.RoleGuest}, hour),
			client.Token{Verified: true, Org: "acme", Account: verdictAccount}},
		{"no role is not privileged", key, mintFor(t, "acme", key, nil, hour),
			client.Token{Verified: true, Org: "acme", Account: verdictAccount}},
		{"forged: another key", key, mintFor(t, "victim", "attacker-secret", nil, hour), client.Token{}},
		{"expired", key, mintFor(t, "acme", key, nil, time.Now().Add(-time.Hour).Unix()), client.Token{}},
		{"malformed", key, "not-a-token", client.Token{}},
		{"empty bearer", key, "", client.Token{}},
		// The upstream public default: a published key verifies nothing.
		{"public default key", "secret", mintFor(t, "victim", "secret", nil, hour), client.Token{}},
		// A genuine token on a deployment with no key is not trusted either.
		{"key unset", "", mintFor(t, "acme", key, nil, hour), client.Token{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := verdict(c.secret, &client.TokenIn{Bearer: c.bearer})
			if *got != c.want {
				t.Fatalf("verdict = %+v, want %+v", *got, c.want)
			}
		})
	}
}

// TestTokenVerdictCrossesThePlane serves the real op on the real plane and asks it
// through the generated client, so the verdict a peer reads is the one team wrote.
func TestTokenVerdictCrossesThePlane(t *testing.T) {
	const key = "a-real-team-secret"
	dir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	cloud.ResetPlane()
	exposeToken(key)
	app := cloud.Plane()
	sock := zip.SocketPath("team")
	go func() { _ = app.Listen(sock) }()
	t.Cleanup(func() {
		_ = app.Shutdown()
		cloud.ResetPlane()
		plane.Unbind()
		_ = os.RemoveAll(dir)
	})
	for range 300 {
		if c, derr := net.Dial("unix", sock); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	hour := time.Now().Add(time.Hour).Unix()
	got, err := teampeer.TeamToken(context.Background(),
		&client.TokenIn{Bearer: mintFor(t, "acme", key, map[string]any{"role": token.RoleAdmin}, hour)})
	if err != nil {
		t.Fatalf("TeamToken: %v", err)
	}
	if want := (client.Token{Verified: true, Org: "acme", Account: verdictAccount, Privileged: true}); *got != want {
		t.Fatalf("verdict = %+v, want %+v", *got, want)
	}
	got, err = teampeer.TeamToken(context.Background(),
		&client.TokenIn{Bearer: mintFor(t, "victim", "attacker-secret", nil, hour)})
	if err != nil || got.Verified {
		t.Fatalf("a forged token: (%+v, %v), want an unverified verdict", got, err)
	}
}
