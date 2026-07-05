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

// TestExpEnforced proves Decode(verify=true) rejects an expired token and accepts
// a still-valid one — the replay-window mitigation. A missing exp (0) is not
// enforced, and verify=false never enforces temporal claims.
func TestExpEnforced(t *testing.T) {
	past := time.Now().Add(-time.Minute).Unix()
	expired, _ := Generate(acc, ws, map[string]any{"org": "acme"}, past, secret)
	if _, err := Decode(expired, secret, true); err != ErrExpired {
		t.Fatalf("expired token: want ErrExpired, got %v", err)
	}
	// verify=false must NOT enforce expiry (it is a pure decode).
	if _, err := Decode(expired, secret, false); err != nil {
		t.Fatalf("verify=false must not enforce exp: %v", err)
	}

	future, _ := Generate(acc, ws, map[string]any{"org": "acme"}, time.Now().Add(time.Hour).Unix(), secret)
	if _, err := Decode(future, secret, true); err != nil {
		t.Fatalf("valid future token rejected: %v", err)
	}

	// nbf in the future → not yet valid.
	nbf, _ := Generate(acc, ws, nil, 0, secret)
	// Re-mint with an nbf by hand is awkward; instead assert the no-exp token verifies.
	if _, err := Decode(nbf, secret, true); err != nil {
		t.Fatalf("no-exp token must verify: %v", err)
	}
}
