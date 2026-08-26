package account

import (
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

// TestDeployedDecidesWhichKeyTheMinterMayHold pins the fact `own` reads. cloud.Deployed
// is false in this binary — nothing handed it a master key — which is exactly the
// laptop answer, so the minter's fallback stays available to the suite.
func TestDeployedDecidesWhichKeyTheMinterMayHold(t *testing.T) {
	if cloud.Deployed() {
		t.Fatal("a test binary reports itself a deployment; the minter's fallback is then untestable")
	}
}

// A user's keys are addressed as a COLLECTION UNDER THE USER, which means the id
// this client holds is not a field any more — it is the PATH. So a composite that
// does not name a pair cannot be sent: `admin` alone would address
// /v1/iam/users/admin/keys, and an id carrying a second slash would address a
// deeper route, both of them somebody else's if they resolved at all.
//
// The two halves are escaped rather than trusted, so a name holding a slash names
// one segment instead of two.
func TestUserKeysPathNamesAPair(t *testing.T) {
	for _, id := range []string{"", "admin", "/alice", "acme/", "/"} {
		if got, err := userKeysPath(id); err == nil {
			t.Errorf("userKeysPath(%q) = %q, want a refusal — it does not name <owner>/<user>", id, got)
		}
	}
	got, err := userKeysPath("acme/alice")
	if err != nil {
		t.Fatalf("userKeysPath(acme/alice): %v", err)
	}
	if want := "/v1/iam/users/acme/alice/keys"; got != want {
		t.Errorf("userKeysPath = %q, want %q — the address IAM serves (internal/oidc)", got, want)
	}
	if got, _ := userKeysPath("acme/a b/c"); !strings.Contains(got, "a%20b%2Fc") {
		t.Errorf("userKeysPath escaped = %q, want the user rendered as ONE segment", got)
	}
}
