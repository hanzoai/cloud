package meet

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
)

// hs256Space is a correctly signed HS256 space session in the layout apps/team
// minted before sessions moved to IAM: an `account` and `workspace` claim, the role
// in `extra`, an exp. meet does not read this credential any more, and the tests
// that use it assert exactly that — so it has to be VALID, or a regression that
// started verifying it would still refuse it for a bad signature and the test would
// pass for the wrong reason.
func hs256Space(t *testing.T, account, space, role string, exp int64, secret string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"extra":     map[string]any{"role": role},
		"account":   account,
		"workspace": space,
		"exp":       exp,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	signing := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"HS256"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
