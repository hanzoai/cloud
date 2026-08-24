package account

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// A valid shared key, as an operator would provision it: 32 bytes, hex.
const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestAVerifierWithoutTheSharedKeyRefusesToBoot pins the whole point of Shared: an
// app that VERIFIES a token this process did not mint must not come up holding a key
// it invented, because that key matches no token it will ever be sent.
//
// The suite cannot see this from inside one process — a test that mounts the issuer
// and the verifier together shares the memoized key and passes either way — so the
// question is asked of the ENVIRONMENT, which is the thing that actually differs
// between a laptop and a pod.
func TestAVerifierWithoutTheSharedKeyRefusesToBoot(t *testing.T) {
	t.Setenv(KeyEnv, "")
	err := Shared()
	if err == nil {
		t.Fatal("a verifier came up with no shared key; every token it is sent was minted under another")
	}
	if !strings.Contains(err.Error(), KeyEnv) {
		t.Errorf("the refusal does not name the value to set: %v", err)
	}

	t.Setenv(KeyEnv, "not-a-key")
	if err := Shared(); err == nil {
		t.Fatal("a value that is not 32 bytes was accepted as the shared key")
	}

	t.Setenv(KeyEnv, testKey)
	if err := Shared(); err != nil {
		t.Fatalf("a provisioned key was refused: %v", err)
	}
}

// TestTheMinterKeepsItsOwnKeyOnALaptopAndRefusesItOnADeployment: the issuer accepts
// only the tokens it wrote, so one key over one lifetime is self-consistent and a
// machine with no KMS still works. A deployment is neither — it holds a master key,
// so it has a secret store, and a key per replica refuses every session that moves.
func TestTheMinterKeepsItsOwnKeyOnALaptopAndRefusesItOnADeployment(t *testing.T) {
	t.Setenv(KeyEnv, "")
	if err := own(false); err != nil {
		t.Fatalf("a machine with no secret store was refused its own key: %v", err)
	}
	err := own(true)
	if err == nil {
		t.Fatal("a deployment came up on a key of its own; it would hold one value per replica")
	}
	if !strings.Contains(err.Error(), KeyEnv) {
		t.Errorf("the refusal does not name the value to set: %v", err)
	}

	t.Setenv(KeyEnv, testKey)
	if err := own(true); err != nil {
		t.Fatalf("a provisioned key was refused on a deployment: %v", err)
	}
}

// TestATokenMintedUnderOneKeyDoesNotVerifyUnderAnother is the fact the two boot
// verdicts exist for, measured rather than assumed: two processes that resolved
// different keys cannot accept each other's tokens, so an unset CONSOLE_CSRF_KEY
// across the mint/verify split is a permanent refusal and not a degraded one.
func TestATokenMintedUnderOneKeyDoesNotVerifyUnderAnother(t *testing.T) {
	mint := &cloud.Service[state]{State: state{csrfKey: decode(t, testKey)}}
	verify := &cloud.Service[state]{State: state{csrfKey: decode(t,
		"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")}}

	tok, _ := issueCSRF(mint, "alice", "acme")
	if !verifyCSRF(mint, tok, "alice", "acme") {
		t.Fatal("a token does not verify under the key that minted it")
	}
	if verifyCSRF(verify, tok, "alice", "acme") {
		t.Fatal("a token verified under a key that did not mint it")
	}
}

// TestDeployedDecidesWhichKeyTheMinterMayHold pins the fact `own` reads. cloud.Deployed
// is false in this binary — nothing handed it a master key — which is exactly the
// laptop answer, so the minter's fallback stays available to the suite.
func TestDeployedDecidesWhichKeyTheMinterMayHold(t *testing.T) {
	if cloud.Deployed() {
		t.Fatal("a test binary reports itself a deployment; the minter's fallback is then untestable")
	}
}

func decode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
