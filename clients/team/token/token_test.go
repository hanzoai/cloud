package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

const (
	acc    = "550e8400-e29b-41d4-a716-446655440000"
	ws     = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	secret = "test-secret"
)

// TestWireFormat locks the exact jwt-simple HS256 byte layout: header, compact
// ordered payload, base64url-no-pad, raw HMAC-SHA256 signature. These assertions
// ARE the jwt-simple contract — matching them is what makes a team token
// interchangeable with an upstream one.
func TestWireFormat(t *testing.T) {
	cases := []struct {
		name      string
		account   string
		workspace string
		extra     map[string]any
		payload   string // exact decoded payload bytes JSON.stringify would emit
	}{
		{"account only", acc, "", nil, `{"account":"` + acc + `"}`},
		{"account+workspace", acc, ws, nil, `{"account":"` + acc + `","workspace":"` + ws + `"}`},
		{"with extra", acc, ws, map[string]any{"org": "acme"},
			`{"extra":{"org":"acme"},"account":"` + acc + `","workspace":"` + ws + `"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// exp=0 → no `exp` claim, so the payload bytes stay jwt-simple-identical.
			tok, err := Generate(c.account, c.workspace, c.extra, 0, secret)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			parts := strings.Split(tok, ".")
			if len(parts) != 3 {
				t.Fatalf("want 3 segments, got %d", len(parts))
			}
			hb, err := base64.RawURLEncoding.DecodeString(parts[0])
			if err != nil {
				t.Fatalf("header not base64url-no-pad: %v", err)
			}
			if string(hb) != `{"typ":"JWT","alg":"HS256"}` {
				t.Fatalf("header = %s", hb)
			}
			pb, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				t.Fatalf("payload not base64url-no-pad: %v", err)
			}
			if string(pb) != c.payload {
				t.Fatalf("payload\n got %s\nwant %s", pb, c.payload)
			}
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(parts[0] + "." + parts[1]))
			want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
			if parts[2] != want {
				t.Fatalf("sig = %s want %s", parts[2], want)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	tok, err := Generate(acc, ws, map[string]any{"org": "acme"}, exp, secret)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(tok, secret, true)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Account != acc || got.Workspace != ws {
		t.Fatalf("got account=%s workspace=%s", got.Account, got.Workspace)
	}
	if got.Extra["org"] != "acme" {
		t.Fatalf("extra not preserved: %v", got.Extra)
	}
	if got.Exp != exp {
		t.Fatalf("exp lost: got %d want %d", got.Exp, exp)
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	tok, _ := Generate(acc, ws, nil, 0, secret)
	// flip the extra claim under a different secret → signature must fail.
	forged, _ := Generate(acc, ws, map[string]any{"org": "evil"}, 0, "other-secret")
	parts := strings.Split(tok, ".")
	bad := parts[0] + "." + strings.Split(forged, ".")[1] + "." + parts[2]
	if _, err := Decode(bad, secret, true); err != ErrSignature {
		t.Fatalf("want ErrSignature, got %v", err)
	}
	if _, err := Decode(tok, "wrong", true); err != ErrSignature {
		t.Fatalf("wrong secret: want ErrSignature, got %v", err)
	}
}

func TestRejectsNonUUID(t *testing.T) {
	if _, err := Generate("not-a-uuid", "", nil, 0, secret); err == nil {
		t.Fatal("want error for non-uuid account")
	}
	if _, err := Generate(acc, "bad-ws", nil, 0, secret); err == nil {
		t.Fatal("want error for non-uuid workspace")
	}
}

// TestExpEnforced is the temporal-claim table: Decode(verify=true) rejects an
// expired exp and a future nbf, tolerates clockSkew on both edges, and
// verify=false never enforces temporal claims.
func TestExpEnforced(t *testing.T) {
	mint := func(exp, nbf int64) string {
		payload, err := marshalCompact(Token{Account: acc, Workspace: ws, Exp: exp, Nbf: nbf})
		if err != nil {
			t.Fatal(err)
		}
		body := base64.RawURLEncoding.EncodeToString(payload)
		return header + "." + body + "." + sign(header+"."+body, secret)
	}
	n := time.Now()
	cases := []struct {
		name     string
		exp, nbf int64
		want     error
	}{
		{"valid future exp", n.Add(time.Hour).Unix(), 0, nil},
		{"expired", n.Add(-2 * time.Minute).Unix(), 0, ErrExpired},
		{"exp within skew", n.Add(-clockSkew / 2).Unix(), 0, nil},
		{"future nbf", 0, n.Add(time.Hour).Unix(), ErrNotYetValid},
		{"nbf within skew", 0, n.Add(clockSkew / 2).Unix(), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Decode(mint(c.exp, c.nbf), secret, true); err != c.want {
				t.Fatalf("Decode = %v, want %v", err, c.want)
			}
		})
	}
	// verify=false must NOT enforce expiry (it is a pure decode).
	if _, err := Decode(mint(n.Add(-2*time.Minute).Unix(), 0), secret, false); err != nil {
		t.Fatalf("verify=false must not enforce exp: %v", err)
	}
}

// TestLegacyNoExpGrace pins the pre-rollout grace: a token WITHOUT an exp claim
// verifies until the fixed legacyExp cutoff and is ErrExpired after it — the
// documented end of the immortal-token window.
func TestLegacyNoExpGrace(t *testing.T) {
	tok, err := Generate(acc, ws, nil, 0, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { now = time.Now }()
	now = func() time.Time { return legacyExp.Add(-time.Hour) }
	if _, err := Decode(tok, secret, true); err != nil {
		t.Fatalf("no-exp token before cutoff: %v, want accepted", err)
	}
	now = func() time.Time { return legacyExp.Add(time.Hour) }
	if _, err := Decode(tok, secret, true); err != ErrExpired {
		t.Fatalf("no-exp token after cutoff: %v, want ErrExpired", err)
	}
}

// TestEmptySecretRefused proves the removed fallback stays removed: neither
// minting nor verifying ever proceeds on an empty secret.
func TestEmptySecretRefused(t *testing.T) {
	if _, err := Generate(acc, ws, nil, 0, ""); err != ErrNoSecret {
		t.Fatalf("Generate(empty secret) = %v, want ErrNoSecret", err)
	}
	tok, _ := Generate(acc, ws, nil, 0, secret)
	if _, err := Decode(tok, "", true); err != ErrNoSecret {
		t.Fatalf("Decode(empty secret, verify) = %v, want ErrNoSecret", err)
	}
	// verify=false is a pure decode — no secret needed.
	if _, err := Decode(tok, "", false); err != nil {
		t.Fatalf("Decode(empty secret, no verify) = %v, want ok", err)
	}
}
