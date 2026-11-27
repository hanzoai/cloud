package event

// team_peer_test.go — a stand-in for the team app's token verdict.
//
// event does not hold the team signing key and does not decode a team token: it
// asks the team app (client.TeamToken) and takes the verdict. These tests need a
// team peer to ask, and apps/team cannot be linked here (it reaches event through
// agents and projects), so the peer is a plane op answering for ONE key with the
// same rules the real one applies: HMAC, exp, nbf, no public-default key, no key
// at all. The real rules are held in apps/team/token_rpc_test.go; what is held
// here is what event does with the answer.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
)

// The space roles a team token signs.
const (
	roleOwner  = "owner"
	roleAdmin  = "admin"
	roleMember = "member"
	roleGuest  = "guest"
)

// jwtHeader is the constant jwt-simple HS256 header the team minter emits.
var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"HS256"}`))

func teamSig(signing, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// mintTeam signs a team-shaped token: an `account` claim, the extra claims, an exp.
func mintTeam(t *testing.T, account string, extra map[string]any, exp int64, secret string) string {
	t.Helper()
	p := map[string]any{"account": account}
	if len(extra) > 0 {
		p["extra"] = extra
	}
	if exp != 0 {
		p["exp"] = exp
	}
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	signing := jwtHeader + "." + base64.RawURLEncoding.EncodeToString(body)
	return signing + "." + teamSig(signing, secret)
}

// teamAsked counts the verdicts the stand-in was asked for, so a test can show a
// request that is not a team token never reached the team app.
var teamAsked atomic.Int64

// teamVerdict is the peer's rule, for one key.
func teamVerdict(key, bearer string) *client.Token {
	if key == "" || key == "secret" {
		return &client.Token{}
	}
	parts := strings.Split(bearer, ".")
	if len(parts) != 3 || teamSig(parts[0]+"."+parts[1], key) != parts[2] {
		return &client.Token{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return &client.Token{}
	}
	var p struct {
		Extra   map[string]any `json:"extra"`
		Account string         `json:"account"`
		Exp     int64          `json:"exp"`
		Nbf     int64          `json:"nbf"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return &client.Token{}
	}
	now := time.Now()
	if p.Exp == 0 || now.Add(-time.Minute).Unix() >= p.Exp || (p.Nbf != 0 && now.Add(time.Minute).Unix() < p.Nbf) {
		return &client.Token{}
	}
	claim := func(k string) string { s, _ := p.Extra[k].(string); return strings.TrimSpace(s) }
	role := claim("role")
	return &client.Token{
		Verified:   true,
		Org:        claim("org"),
		Account:    p.Account,
		Privileged: role == roleOwner || role == roleAdmin || role == roleMember,
	}
}

// serveTeam binds the team app's plane socket with the verdict for key. The caller
// has reset the plane; this registers before it listens.
func serveTeam(t *testing.T, key string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	cloud.Plane().Post("/team/token",
		func(_ context.Context, in *client.TokenIn) (*client.Token, error) {
			teamAsked.Add(1)
			return teamVerdict(key, in.Bearer), nil
		},
		zip.WithOperationID(client.TeamToken))
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
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the team plane socket never began listening")
}
