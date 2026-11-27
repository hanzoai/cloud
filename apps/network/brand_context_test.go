package network

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/basetest"
	"github.com/hanzoai/cloud/internal/kmstest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/zap-proto/zip"
)

// tenantKMS holds the invite key in ONE org's tenancy: a read whose context does
// not act for that org finds no key, the way a tenant-scoped store answers a
// caller with no org. It records the org each read acted for, so a test can see
// whether a request steered the read.
type tenantKMS struct {
	*kmstest.Mem
	org  string
	mu   sync.Mutex
	seen []string
}

func (k *tenantKMS) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	acting := zip.CallerOf(ctx).Org
	k.mu.Lock()
	k.seen = append(k.seen, acting)
	k.mu.Unlock()
	if acting != k.org {
		return nil, secret.ErrNone
	}
	return k.Mem.GetSecret(ctx, ref)
}

func (k *tenantKMS) orgs() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.seen...)
}

func (k *tenantKMS) everyReadIsBrand(t *testing.T, brand string) {
	t.Helper()
	seen := k.orgs()
	if len(seen) < 2 {
		t.Fatalf("only %d KMS reads were observed, want at least two", len(seen))
	}
	for _, org := range seen {
		if org != brand {
			t.Errorf("a KMS read acted for %q, want only the brand %q; the request named another org", org, brand)
		}
	}
}

// forgetKey makes the next op read the key from KMS again, the way a restarted pod
// does, so every request in these tests exercises the tenant-scoped read.
func (in *intake) forgetKey() { in.svc.State.seal = sealOf(in.svc.Coord) }

// mountTenanted mounts the intake over a KMS that keeps the key in the brand's
// tenancy, with the key already sealed and no key cached in the process.
func mountTenanted(t *testing.T) (*intake, *tenantKMS) {
	t.Helper()
	in := mountOn(t, func(app *zip.App, s *cloud.Service[state]) { routes(app, s) }, basetest.Serve)
	in.key(t) // seal the key in KMS before any request arrives
	k := &tenantKMS{Mem: in.kms, org: cloud.Brand()}
	in.svc.KMS = k
	in.svc.State.seal = sealOf(in.svc.Coord) // the next read must go through the tenanted KMS
	return in, k
}

// TestAnAnonymousIntakeReadsTheBrandTenantedKey: a stranger with no principal can
// verify an invite and join the waitlist when the key is held in the brand org's
// tenancy. Before the intake stated that org on the KMS context, both answered
// 503 "network access is not configured" because the read acted for nobody.
func TestAnAnonymousIntakeReadsTheBrandTenantedKey(t *testing.T) {
	in, kms := mountTenanted(t)
	in.seed(t, live, nil)
	in.forgetKey()
	if a := in.verify(t, live, from(1)); a.body != yes {
		t.Errorf("an anonymous verify of a live invite: %d %s", a.code, a.body)
	}
	in.forgetKey()
	if a := in.join(t, `{"email":"ada@example.com","role":"builder","source":"hanzo.network/join"}`, from(2)); a.code != http.StatusAccepted {
		t.Errorf("an anonymous waitlist ask: %d %s", a.code, a.body)
	}
	// The typed op invoked off the call plane has no request and no principal at all.
	in.forgetKey()
	v, err := (ops{s: in.svc}).verifyInvite(context.Background(), &InviteCheckIn{Code: live})
	if err != nil || v == nil || !v.Valid {
		t.Errorf("a verify with no request and no principal: %+v, %v", v, err)
	}
	kms.everyReadIsBrand(t, "hanzo")
}

// TestNoRequestCanRedirectTheBrandKeyRead: neither a forged org header, a signed-in
// member of another org, nor a SuperAdmin's own org moves the KMS read off the
// brand. The issue path still works; only the brand's key is used.
func TestNoRequestCanRedirectTheBrandKeyRead(t *testing.T) {
	in, kms := mountTenanted(t)
	in.seed(t, live, nil)

	// send forges X-Org-Id: mallory for a visitor with no credential.
	in.forgetKey()
	if a := in.verify(t, live, from(3)); a.body != yes {
		t.Errorf("a forged org header: %d %s", a.code, a.body)
	}
	other := caller{org: "mallory", user: "mallory/zed"}
	b, _ := json.Marshal(map[string]string{"code": live})
	in.forgetKey()
	if a := in.send(t, http.MethodPost, "/v1/network/invite/verify", string(b), other, from(4)); a.body != yes {
		t.Errorf("a member of another org: %d %s", a.code, a.body)
	}
	// The SuperAdmin's home org is admin; the key still comes from the brand.
	in.forgetKey()
	out := in.issue(t, `{"count":1}`)
	if len(out.Invites) != 1 {
		t.Fatalf("issue returned %d codes, want 1", len(out.Invites))
	}
	in.forgetKey()
	if a := in.verify(t, out.Invites[0].Code, from(5)); a.body != yes {
		t.Errorf("the code just issued: %d %s", a.code, a.body)
	}

	kms.everyReadIsBrand(t, "hanzo")
	for _, org := range []string{"mallory", "admin"} {
		if n := len(records(t, org, invitesName)); n != 0 {
			t.Errorf("a request routed %d invite records to %s", n, org)
		}
	}
}

// TestAnonymous503SaysNothingAboutTheKeyOrOrg: a KMS fault is the generic 503,
// not the configuration answer; neither the secret ref nor an org is in it. A key
// that was never sealed keeps its own, equally quiet configuration answer.
func TestAnonymous503SaysNothingAboutTheKeyOrOrg(t *testing.T) {
	t.Run("a KMS fault", func(t *testing.T) {
		in := mountOn(t, func(app *zip.App, s *cloud.Service[state]) { routes(app, s) }, basetest.Serve)
		in.key(t)
		in.svc.State.seal = sealOf(in.svc.Coord)
		in.svc.KMS = brokenKMS{}
		a := in.verify(t, miss(0), from(6))
		if !problem(a, http.StatusServiceUnavailable) {
			t.Fatalf("want a 503 problem, got %d %s", a.code, a.body)
		}
		var p struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal([]byte(a.body), &p); err != nil || p.Detail != "network access is unavailable" {
			t.Fatalf("a KMS fault must be the generic 503, got %q (%v)", a.body, err)
		}
		assertNoAnonymousLeak(t, a.body)
	})
	t.Run("no key sealed", func(t *testing.T) {
		in := mountOn(t, func(app *zip.App, s *cloud.Service[state]) { routes(app, s) }, basetest.Serve)
		a := in.verify(t, miss(0), from(7))
		if !problem(a, http.StatusServiceUnavailable) {
			t.Fatalf("want a 503 problem, got %d %s", a.code, a.body)
		}
		var p struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal([]byte(a.body), &p); err != nil || p.Detail != "network access is not configured" {
			t.Fatalf("an unsealed key must be the configuration 503, got %q (%v)", a.body, err)
		}
		assertNoAnonymousLeak(t, a.body)
	})
}

func assertNoAnonymousLeak(t *testing.T, body string) {
	t.Helper()
	for _, leak := range []string{inviteRef, "/network", "invite-hmac", "hanzo", "mallory", "acme", "admin"} {
		if strings.Contains(body, leak) {
			t.Errorf("the anonymous 503 names %q: %s", leak, body)
		}
	}
}
