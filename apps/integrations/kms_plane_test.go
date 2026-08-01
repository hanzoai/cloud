package integrations

import (
	"os"
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

// The import runs detached, so there is no request to forward and a plane call
// would arrive anonymous — the callee reads the tenant from the caller identity
// and refuses one it cannot see. This is the second half of the cross-process
// fix: the request travels, and it travels WITH an identity.
func TestTheDetachedImportStatesItsTenant(t *testing.T) {
	src, err := os.ReadFile("github_app.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "cloud.For(ctx, org)") {
		t.Error("the background import must state the org it acts for, or the plane call is anonymous")
	}
	// It states the org it was ASKED for, never one from the payload — an inbound
	// request wins over a stated one, so a job supplies an identity and cannot
	// launder one.
	if strings.Contains(s, "cloud.For(ctx, it.") {
		t.Error("the tenant must come from the authenticated request, not the work item")
	}
}

// Each connected account is an independent installation with its own token and
// its own pagination. Read in series their latencies ADD: 816 repos across three
// accounts is ten sequential pages and about seven seconds, and under load the
// largest account exceeded the request deadline and dropped out of the union
// entirely. Degrading to the accounts that answered is right; an account failing
// because it was listed last is not.
func TestAccountsAreReadConcurrently(t *testing.T) {
	src, err := os.ReadFile("github_app.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(src)
	i := strings.Index(s, "func reachableRepos")
	if i < 0 {
		t.Fatal("reachableRepos is gone")
	}
	body := s[i:]
	if j := strings.Index(body[1:], "\nfunc "); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "sync.WaitGroup") || !strings.Contains(body, "go func(") {
		t.Error("accounts are read in series; their latencies add and the last one starves")
	}
}

// The union must be assembled in CONNECTION order, not completion order, or the
// same set of accounts yields a different list each call and a caller paging it
// sees rows reshuffle.
func TestTheUnionOrderDoesNotDependOnWhoAnsweredFirst(t *testing.T) {
	src, _ := os.ReadFile("github_app.go")
	s := string(src)
	i := strings.Index(s, "func reachableRepos")
	body := s[i:]
	if j := strings.Index(body[1:], "\nfunc "); j > 0 {
		body = body[:j]
	}
	// Results land in a slice indexed by connection, then are merged in that
	// order — never appended from inside the goroutines.
	if !strings.Contains(body, "out[i] = result{") {
		t.Error("results must be placed by connection index, not appended as they arrive")
	}
	if strings.Contains(body, "all = append(all,") && strings.Contains(body, "go func(i int") {
		before := strings.Index(body, "wg.Wait()")
		at := strings.Index(body, "all = append(all,")
		if before < 0 || at < before {
			t.Error("the union is built before every account has answered")
		}
	}
}
