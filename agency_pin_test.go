package cloud

// The pin for the trust decision. An edge.Signal carries two fingerprints of the
// same credential under different trust — one the sensor may key on, one it may
// only count — so a second place that builds one is a second answer to "did this
// credential validate", and the second answer is the one that would be wrong.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPin_AnObservationIsBuiltInOnePlace(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") || name == "agency.go" {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "edge.Signal{") {
			t.Errorf("%s builds an edge.Signal; the one constructor is observation() in agency.go", name)
		}
	}
}

// And that constructor may only put a fingerprint in the KEY field behind the
// identity boundary's own attestation.
func TestPin_OnlyAnAttestedCredentialBecomesAKey(t *testing.T) {
	b, err := os.ReadFile("agency.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func observation(")
	if i < 0 {
		t.Fatal("observation() is gone; the observation constructor moved and this pin did not")
	}
	body := src[i:]
	if j := strings.Index(body, "\n}\n"); j >= 0 {
		body = body[:j]
	}
	assign := strings.Index(body, "s.Cred = ")
	guard := strings.Index(body, "if principalValidated(c)")
	if assign < 0 || guard < 0 || guard > assign {
		t.Error("Signal.Cred is set without the identity boundary's attestation guarding it")
	}
}
