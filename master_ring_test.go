package cloud

import (
	"strings"
	"testing"
)

// When the ring is configured it is the only source. A root that can also be
// reached from the environment is only ever as strong as the environment, so the
// two together are refused rather than ordered — an operator who set both has
// made a choice they almost certainly did not mean.
func TestTheRingIsNotASecondOpinion(t *testing.T) {
	t.Setenv(RingEndpointEnv, "zap://mpc.example:9999")
	t.Setenv(RingSealedEnv, "AAAA")
	t.Setenv(MasterEnv, "bm90LWEtcmVhbC1rZXktMzItYnl0ZXMtbG9uZy0hIQ==")

	_, err := ringMaster("zap://mpc.example:9999")
	if err == nil {
		t.Fatal("both roots were accepted; the environment one is readable by whatever reads Secrets")
	}
	if !strings.Contains(err.Error(), MasterEnv) {
		t.Fatalf("the refusal does not name what to unset: %v", err)
	}
}

// The ring needs the ciphertext as well as the endpoint: the ring holds shares,
// not the sealed value. Asking it to open nothing is a misconfiguration, and one
// that would otherwise surface as a puzzling timeout.
func TestTheRingNeedsSomethingToOpen(t *testing.T) {
	t.Setenv(RingEndpointEnv, "zap://mpc.example:9999")
	t.Setenv(RingSealedEnv, "")
	t.Setenv(MasterEnv, "")

	_, err := ringMaster("zap://mpc.example:9999")
	if err == nil || !strings.Contains(err.Error(), RingSealedEnv) {
		t.Fatalf("a ring with no ciphertext was accepted: %v", err)
	}
}
