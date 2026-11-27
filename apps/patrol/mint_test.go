package patrol

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// mint_test.go asks what a minted address tells its holder. Stripping the
// userinfo answers "does the response CARRY the password". It does not answer
// "does the response DECIDE the password", and a MAC keyed by the very secret it
// is withholding answers exactly that: the holder cannot read the credential,
// but they can test one, offline, as fast as they like.

// TestAMintedAddressIsNoOracleOnTheCredential recovers a camera's password from
// one minted address and nothing else.
//
// mint keys an HMAC-SHA256 with the credential itself and hands the tag to the
// caller beside the org, the camera and the expiry the tag covers. Every input
// but the key is in the response, so `ticket` is a verifier for a guess: the
// holder enumerates candidate credentials and stops at the one whose tag
// matches. The bare address in the same response supplies the host and the path,
// so only the password is unknown — and a camera password is a short string from
// a small alphabet, which is the case a keyed MAC must never be built on. The
// correct key for an issued ticket is a value NOBODY can guess and NOBODY needs
// to know but the relay: a per-deployment signing secret, not the thing being
// protected.
func TestAMintedAddressIsNoOracleOnTheCredential(t *testing.T) {
	const ref = "acme/patrol/camera/cam-1"
	const host = "cam1.depot.local:554"
	const path = "/Streaming/Channels/101"
	const password = "8471"
	credential := "rtsp://admin:" + password + "@" + host + path

	v := newVault(map[string]string{ref: credential})
	app := mountVault(t, v)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTCamera, map[string]any{
		"name": "cam-1", "site": "depot", "label": "Yard", "online": true, "secret": ref,
	})

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, officer, nil)
	if code != http.StatusOK {
		t.Fatalf("mint a stream want 200, got %d (%s)", code, first(raw, 200))
	}
	var got patrolStream
	data(t, raw, &got)

	// Everything below is the holder of that one string, working offline.
	u, err := url.Parse(got.URL)
	if err != nil {
		t.Fatalf("the minted address does not parse: %v (%q)", err, got.URL)
	}
	stamp := u.Query().Get("expires")
	tag := u.Query().Get("ticket")
	if stamp == "" || tag == "" {
		t.Fatalf("the minted address carries no expiry or ticket to work from: %q", got.URL)
	}

	for guess := range 10000 {
		candidate := fmt.Sprintf("rtsp://admin:%04d@%s%s", guess, u.Host, u.Path)
		mac := hmac.New(sha256.New, []byte(candidate))
		_, _ = fmt.Fprintf(mac, "%s\n%s\n%s", org, "cam-1", stamp)
		if hex.EncodeToString(mac.Sum(nil)) == tag {
			t.Fatalf("the camera's credential was recovered from the minted address alone in %d "+
				"offline guesses: %q. The ticket is a MAC keyed by the secret it withholds, so "+
				"every caller who is shown one is shown a verifier for it", guess+1, candidate)
		}
	}
}
