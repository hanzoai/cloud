package host

// A SuperAdmin impersonation token carries the TARGET's claims and names the
// operator in `act`. The boundary is the one place that reads it, so these pin
// that X-Acted-By is written from the verified token, never from the request, and
// only for an impersonation.

import (
	"crypto/rsa"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/authz"
)

// impClaims is an IAM access token minted for a SuperAdmin acting as someone else.
type impClaims struct {
	authz.Claims
	Act map[string]string `json:"act,omitempty"`
	Imp bool              `json:"imp,omitempty"`
}

func signImp(t *testing.T, key *rsa.PrivateKey, c impClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = "test-key"
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}
