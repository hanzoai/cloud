package platform

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// tenantEnforcingKMS models apps/kms/attest.go's admit rule: a ref under
// "orgs/<org>/..." is only accepted when cloud.Who(ctx).Org matches <org>.
// When cloud.Who(ctx).Org is empty, apps/kms falls back to the custodian brand
// ("hanzo") and rejects every other tenant's ref with 403 — the exact failure
// that caused POST /v1/platform/apps to return 502 "could not seal DUMMY_SECRET
// into KMS; nothing was written" for customer orgs.
type tenantEnforcingKMS struct {
	mu      sync.Mutex
	brand   string
	data    map[string][]byte
	putOrgs []string
	getOrgs []string
}

func (k *tenantEnforcingKMS) check(ctx context.Context, ref string) (string, error) {
	act := cloud.Who(ctx).Org
	if act == "" {
		act = k.brand
	}
	if rest, ok := strings.CutPrefix(ref, "orgs/"); ok {
		if org, _, ok := strings.Cut(rest, "/"); ok && org != act {
			return act, zip.ErrForbidden("kms: ref " + ref + " belongs to " + org + " but the call acts for " + act)
		}
	}
	return act, nil
}

func (k *tenantEnforcingKMS) PutSecret(ctx context.Context, ref string, value []byte) error {
	act, err := k.check(ctx, ref)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.putOrgs = append(k.putOrgs, act)
	if k.data == nil {
		k.data = map[string][]byte{}
	}
	k.data[ref] = append([]byte(nil), value...)
	return nil
}

func (k *tenantEnforcingKMS) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	act, err := k.check(ctx, ref)
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.getOrgs = append(k.getOrgs, act)
	v, ok := k.data[ref]
	if !ok {
		return nil, fmt.Errorf("kms get: %w", secret.ErrNone)
	}
	return append([]byte(nil), v...), nil
}

func (k *tenantEnforcingKMS) DeleteSecret(ctx context.Context, ref string) error {
	if _, err := k.check(ctx, ref); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.data, ref)
	return nil
}

func (k *tenantEnforcingKMS) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, fmt.Errorf("kms: sign not supported in tests")
}

// TestDeclareAppSealsSecretsWithTenantOrgContext reproduces the 502
// "could not seal DUMMY_SECRET into KMS; nothing was written" failure on
// POST /v1/platform/apps for a non-hanzo tenant org (and for a SuperAdmin
// acting as a tenant org). Because declareApp is a raw *zip.Ctx handler,
// c.Context() has no zip.Caller; splitSecretEnv must pass cloud.As(c, org) so
// KMSPeer carries the target tenant org to KMS.
func TestDeclareAppSealsSecretsWithTenantOrgContext(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		remote, _ := gitRemote(t, pinFixture)
		defer swapUniverseRemote(remote)()

		kms := &tenantEnforcingKMS{brand: "hanzo"}
		store, err := openStore(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })

		s := &cloud.Service[state]{
			KMS: kms, Log: luxlog.New("test"), Brand: "hanzo",
			State: state{
				store:       store,
				sitesHost:   "hanzo.app",
				kmsIdentity: newKMSOrgIdentity(kms, "", "hanzo"),
				k8s:         &k8sClient{initErr: "no cluster (test)", imagePrefix: defaultBuildImagePrefix, limits: testLimits()},
			},
		}
		fs := &cloud.Service[fleetState]{
			Log: luxlog.New("test"), Brand: "hanzo",
			State: fleetState{initErr: "no cluster (test)"},
		}
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		app.Use(cloud.Bridge())
		appsRoutes(app, s, fs)

		// 1. Customer org admin declaring an app with a secret env var on a branch.
		const customerOrg = "jamessburnss1313"
		buildFalse := false
		code, body := doAdmin(t, app, http.MethodPost, "/v1/platform/apps", customerOrg, map[string]any{
			"name":  "cd-clock-test",
			"tag":   "v1",
			"build": buildFalse,
			"mode":  "branch",
			"env":   []map[string]any{{"name": "DUMMY_SECRET", "value": "s3cr3t-value"}},
		})
		if code != http.StatusAccepted {
			t.Fatalf("POST /v1/platform/apps (%s) = %d %s, want 202", customerOrg, code, body)
		}
		ref := kmsSecretRef(customerOrg, "cd-clock-test", "DUMMY_SECRET")
		kms.mu.Lock()
		sealed := string(kms.data[ref])
		putOrgs := append([]string(nil), kms.putOrgs...)
		getOrgs := append([]string(nil), kms.getOrgs...)
		kms.mu.Unlock()
		if sealed != "s3cr3t-value" {
			t.Fatalf("secret %s = %q, want sealed value", ref, sealed)
		}
		if len(putOrgs) != 1 || putOrgs[0] != customerOrg {
			t.Fatalf("KMS PutSecret saw orgs %v, want [%s]", putOrgs, customerOrg)
		}
		if len(getOrgs) == 0 || getOrgs[0] != customerOrg {
			t.Fatalf("ensureTenantKMSAuth GetSecret saw orgs %v, want [%s]", getOrgs, customerOrg)
		}

		// 2. SuperAdmin (org "hanzo") acting as another customer org.
		const targetOrg = "acme"
		req := newJSONRequest(t, http.MethodPost, "/v1/platform/apps", map[string]any{
			"org":   targetOrg,
			"name":  "clock-two",
			"tag":   "v1",
			"build": buildFalse,
			"mode":  "branch",
			"env":   []map[string]any{{"name": "API_TOKEN", "value": "tok-123"}},
		})
		req.Header.Set("X-Org-Id", "hanzo")
		req.Header.Set("X-User-Id", "u-super")
		req.Header.Set("X-User-IsAdmin", "true")
		req.Header.Set("X-User-Owner", "hanzo")
		resp, err := app.Test(req, budget)
		if err != nil {
			t.Fatalf("Test SuperAdmin act-as: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST /v1/platform/apps (SuperAdmin acting as %s) = %d, want 202", targetOrg, resp.StatusCode)
		}
		ref2 := kmsSecretRef(targetOrg, "clock-two", "API_TOKEN")
		kms.mu.Lock()
		sealed2 := string(kms.data[ref2])
		kms.mu.Unlock()
		if sealed2 != "tok-123" {
			t.Fatalf("secret %s = %q, want tok-123", ref2, sealed2)
		}
	})
}
