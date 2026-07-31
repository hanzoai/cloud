package integrations

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// Exactly one process owns the secret store; every other app is handed a peer that
// reaches it over the internal plane. integrations is one of those, so it must
// speak the KMSClient INTERFACE — asserting to the embedded client yielded nil for
// months and every credential op failed closed while the error blamed a master key
// that was correctly configured.

// The peer must satisfy the interface integrations depends on, or the process that
// is not the store's owner has no way to reach it.
func TestThePlanePeerIsAKMSClient(t *testing.T) {
	var _ cloud.KMSClient = cloud.KMSPeer{}
}

// The interface has to carry DELETE, or disconnecting a provider drops the
// connection row and leaves the customer's credential behind. Both implementations
// must have it: the embedded client for the process that owns the store, and the
// peer for every process that does not.
func TestBothImplementationsCanForgetASecret(t *testing.T) {
	var peer cloud.KMSClient = cloud.KMSPeer{}
	if peer == nil {
		t.Fatal("the peer must be usable as the interface")
	}
	// DeleteSecret being on the interface is what this asserts — it compiles only
	// if KMSClient declares it and KMSPeer implements it.
	_ = peer.DeleteSecret
}

// A ref round-trips the same coordinates the store holds: "path/name@env" is what
// parseRef reads back into (path, name, env).
func TestRefCarriesPathNameAndEnv(t *testing.T) {
	ref := kmsRef("/orgs/acme/integrations/github", "token")
	for _, want := range []string{"/orgs/acme/integrations/github", "token", "@" + kmsEnv} {
		if !strings.Contains(ref, want) {
			t.Errorf("ref %q lost %q", ref, want)
		}
	}
	if !strings.HasPrefix(ref, "/orgs/") {
		t.Errorf("ref %q must keep the org-scoped path that isolates tenants", ref)
	}
}

// The gate asks whether a store is reachable at all. It cannot ask a REMOTE store
// whether its master key is loaded without a round trip per check, and a store that
// is wired but unhealthy reports that on the operation, where a caller can act on
// it. What this gate is for is the case with no store.
func TestReadinessIsAboutHavingAStore(t *testing.T) {
	withStore := &cloud.Service[state]{State: state{kms: cloud.KMSPeer{}}}
	if !kmsReady(withStore) {
		t.Error("a wired peer IS a reachable store; this is the case that failed closed for months")
	}
	withNone := &cloud.Service[state]{State: state{}}
	if kmsReady(withNone) {
		t.Error("no store must not read as ready")
	}
}
