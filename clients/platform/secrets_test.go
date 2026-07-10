package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fakeKMS is an in-memory types.KMSClient for hermetic secret-sealing tests. It
// models the embedded KMS closely enough for the platform seal path: PutSecret
// stores by ref, GetSecret reads it back. `fail` models an unconfigured KMS
// (master key missing) so the fail-closed path is exercised.
type fakeKMS struct {
	mu   sync.Mutex
	data map[string][]byte
	fail bool
}

func newFakeKMS() *fakeKMS { return &fakeKMS{data: map[string][]byte{}} }

func (f *fakeKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[ref]
	if !ok {
		return nil, fmt.Errorf("kms: secret not found")
	}
	return append([]byte(nil), v...), nil
}

func (f *fakeKMS) PutSecret(_ context.Context, ref string, value []byte) error {
	if f.fail {
		return fmt.Errorf("kms: master key not configured")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data == nil {
		f.data = map[string][]byte{}
	}
	f.data[ref] = append([]byte(nil), value...)
	return nil
}

func (f *fakeKMS) Sign(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("kms: sign not supported in tests")
}

// TestSealSecretEnv_BlanksAndSeals proves sealSecretEnv seals secret values into
// KMS, blanks the persisted value, and passes plain vars through untouched.
func TestSealSecretEnv_BlanksAndSeals(t *testing.T) {
	s := &cloud.Service[state]{Base: cloud.Base{KMS: newFakeKMS()}}
	in := []EnvVarJSON{
		{Key: "PUBLIC", Value: "ok", Secret: false},
		{Key: "DB_PASSWORD", Value: "hunter2", Secret: true},
	}
	out, err := sealSecretEnv(s, context.Background(), "maxpower", "api", in)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 env, got %d", len(out))
	}
	for _, e := range out {
		switch e.Key {
		case "PUBLIC":
			if e.Value != "ok" || e.Secret {
				t.Fatalf("plain var mangled: %+v", e)
			}
		case "DB_PASSWORD":
			if e.Value != "" || !e.Secret {
				t.Fatalf("secret var must be blanked+secret: %+v", e)
			}
		}
	}
	got, err := s.KMS.GetSecret(context.Background(), kmsSecretRef("maxpower", "api", "DB_PASSWORD"))
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("value not sealed to KMS: %q err=%v", got, err)
	}
}

// TestSealSecretEnv_FailsClosed proves a secret with no KMS (nil client or a KMS
// error) refuses the whole set — never a plaintext fallback.
func TestSealSecretEnv_FailsClosed(t *testing.T) {
	// nil client
	s := &cloud.Service[state]{Base: cloud.Base{KMS: nil}}
	if _, err := sealSecretEnv(s, context.Background(), "o", "a", []EnvVarJSON{{Key: "X", Value: "v", Secret: true}}); err == nil {
		t.Fatal("nil KMS must fail closed for secret env")
	}
	// erroring client (master key missing)
	s = &cloud.Service[state]{Base: cloud.Base{KMS: &fakeKMS{fail: true}}}
	if _, err := sealSecretEnv(s, context.Background(), "o", "a", []EnvVarJSON{{Key: "X", Value: "v", Secret: true}}); err == nil {
		t.Fatal("KMS error must fail closed for secret env")
	}
	// but a plain-only set is fine without KMS
	s = &cloud.Service[state]{Base: cloud.Base{KMS: nil}}
	if _, err := sealSecretEnv(s, context.Background(), "o", "a", []EnvVarJSON{{Key: "X", Value: "v"}}); err != nil {
		t.Fatalf("plain env must not need KMS: %v", err)
	}
}

// TestKMSSecretRef_Injective proves distinct (org,app,key) triples never collide
// on one KMS coordinate — the cross-tenant secret-isolation boundary.
func TestKMSSecretRef_Injective(t *testing.T) {
	seen := map[string]string{}
	triples := [][3]string{
		{"acme", "api", "TOKEN"},
		{"acme", "api", "TOKEN2"},
		{"acme", "web", "TOKEN"},
		{"maxpower", "api", "TOKEN"},
		{"Acme", "api", "TOKEN"}, // fold-sibling org must not collide with "acme"
	}
	for _, tr := range triples {
		ref := kmsSecretRef(tr[0], tr[1], tr[2])
		if prev, ok := seen[ref]; ok {
			t.Fatalf("ref collision %q: %v and %v", ref, prev, tr)
		}
		seen[ref] = fmt.Sprintf("%v", tr)
	}
}

// TestKMSSecretCR_Shape proves the KMSSecret CR is the canonical kms-operator
// shape AND carries the ORG-SCOPED coordinates that make the operator read exactly
// what cloud sealed under its per-tenant auth: projectSlug=<org> (the /v1/kms/orgs/
// {org} segment the guard checks against the authenticated owner), secretsPath=
// platform/<app>, and the explicit key roster the CRD requires (no list endpoint).
// The secret's plaintext never appears in it.
func TestKMSSecretCR_Shape(t *testing.T) {
	k := &k8sClient{kmsSync: newKMSSyncConfig()}
	cr := k.kmsSecretCR("tenant-maxpower", "maxpower", "api", []string{"API_KEY", "DB_PASSWORD"})
	if got, _, _ := unstructured.NestedString(cr.Object, "apiVersion"); got != "secrets.lux.network/v1alpha1" {
		t.Fatalf("apiVersion: %s", got)
	}
	if got, _, _ := unstructured.NestedString(cr.Object, "kind"); got != "KMSSecret" {
		t.Fatalf("kind: %s", got)
	}
	if got, _, _ := unstructured.NestedString(cr.Object, "metadata", "name"); got != "api-env" {
		t.Fatalf("name: %s", got)
	}
	if got, _, _ := unstructured.NestedString(cr.Object, "spec", "managedSecretReference", "secretName"); got != "api-env" {
		t.Fatalf("managed secretName: %s", got)
	}
	scope := "spec.authentication.universalAuth.secretsScope"
	if got, _, _ := unstructured.NestedString(cr.Object, "spec", "authentication", "universalAuth", "secretsScope", "projectSlug"); got != "maxpower" {
		t.Fatalf("%s.projectSlug must be the tenant org, got %q", scope, got)
	}
	if got, _, _ := unstructured.NestedString(cr.Object, "spec", "authentication", "universalAuth", "secretsScope", "secretsPath"); got != "platform/api" {
		t.Fatalf("%s.secretsPath: %q", scope, got)
	}
	if got, _, _ := unstructured.NestedString(cr.Object, "spec", "authentication", "universalAuth", "secretsScope", "envSlug"); got != "default" {
		t.Fatalf("%s.envSlug: %q", scope, got)
	}
	// keys REQUIRED (CRD MinItems=1) and sorted, byte-stable.
	keys, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "authentication", "universalAuth", "secretsScope", "keys")
	if len(keys) != 2 || keys[0] != "API_KEY" || keys[1] != "DB_PASSWORD" {
		t.Fatalf("%s.keys must be the explicit sorted roster, got %v", scope, keys)
	}
	// credentialsRef is the PER-TENANT creds Secret in THIS namespace — never shared.
	if got, _, _ := unstructured.NestedString(cr.Object, "spec", "authentication", "universalAuth", "credentialsRef", "secretName"); got != "platform-kms-auth" {
		t.Fatalf("credentialsRef.secretName must be the per-tenant creds Secret, got %q", got)
	}
	if got, _, _ := unstructured.NestedString(cr.Object, "spec", "authentication", "universalAuth", "credentialsRef", "secretNamespace"); got != "tenant-maxpower" {
		t.Fatalf("credentialsRef.secretNamespace must be the tenant namespace, got %q", got)
	}
	// hostAPI is the ROOT (operator appends /v1/kms/…); a suffixed host would double
	// the prefix on the operator's login + read URLs.
	host, _, _ := unstructured.NestedString(cr.Object, "spec", "hostAPI")
	if host == "" || strings.HasSuffix(host, "/v1/kms") {
		t.Fatalf("hostAPI must be the KMS root without a /v1/kms suffix, got %q", host)
	}
}

// fakeIdentity is a hermetic tenantKMSIdentity that hands back an org-bound
// credential. It records the org it was asked for so a test can assert cloud never
// requests one org's identity while provisioning another.
type fakeIdentity struct {
	mu       sync.Mutex
	askedFor []string
	fail     bool
}

func (f *fakeIdentity) EnsureOrgIdentity(_ context.Context, org string) (string, string, error) {
	f.mu.Lock()
	f.askedFor = append(f.askedFor, org)
	f.mu.Unlock()
	if f.fail {
		return "", "", fmt.Errorf("iam provisioning unavailable")
	}
	// Credential is bound to THIS org (the concrete impl mints owner=<org>); the
	// fake encodes that binding in the clientId so the projection can be asserted.
	return "mi-" + org, "sec-" + org, nil
}

// TestEnsureTenantKMSAuth_FailClosedWhenUnprovisioned proves that with NO identity
// provisioner wired (the default), cloud creates NO creds Secret — the operator
// cannot authenticate, so the tenant's sync stays fail-closed pending. It NEVER
// falls back to a shared reader.
func TestEnsureTenantKMSAuth_FailClosedWhenUnprovisioned(t *testing.T) {
	k := fakeK8s()
	s := &cloud.Service[state]{Base: cloud.Base{KMS: newFakeKMS(), Log: luxlog.New("test")}, State: state{k8s: k, kmsIdentity: nil}}
	ensureTenantKMSAuth(s, context.Background(), "maxpower")
	if _, err := k.dyn.Resource(secretsGVR).Namespace("tenant-maxpower").Get(context.Background(), "platform-kms-auth", metav1.GetOptions{}); err == nil {
		t.Fatal("no provisioner ⇒ no creds Secret must be created (fail-closed, never a shared reader)")
	}
}

// TestEnsureTenantKMSAuth_ProvisionsPerTenant proves that WITH a provisioner, cloud
// projects the tenant's OWN org-bound credential into tenant-<org> under the exact
// data keys the operator reads (clientId/clientSecret), and asks the provisioner
// only for that org — never a cross-tenant identity.
func TestEnsureTenantKMSAuth_ProvisionsPerTenant(t *testing.T) {
	k := fakeK8s()
	id := &fakeIdentity{}
	s := &cloud.Service[state]{Base: cloud.Base{KMS: newFakeKMS(), Log: luxlog.New("test")}, State: state{k8s: k, kmsIdentity: id}}
	ctx := context.Background()
	ensureTenantKMSAuth(s, ctx, "maxpower")

	sec, err := k.dyn.Resource(secretsGVR).Namespace("tenant-maxpower").Get(ctx, "platform-kms-auth", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("per-tenant creds Secret not projected: %v", err)
	}
	sd, _, _ := unstructured.NestedStringMap(sec.Object, "stringData")
	if sd["clientId"] != "mi-maxpower" || sd["clientSecret"] != "sec-maxpower" {
		t.Fatalf("creds Secret must carry the org-bound identity under clientId/clientSecret, got %v", sd)
	}
	// The provisioner was asked ONLY for maxpower — never a different tenant.
	if len(id.askedFor) != 1 || id.askedFor[0] != "maxpower" {
		t.Fatalf("provisioner must be asked only for the deploying org, got %v", id.askedFor)
	}
	// Idempotent: a second pass reuses the existing Secret (no error, no dup ask).
	ensureTenantKMSAuth(s, ctx, "maxpower")
	if len(id.askedFor) != 1 {
		t.Fatalf("existing creds Secret must short-circuit provisioning, asked=%v", id.askedFor)
	}
}

// TestEnsureTenantKMSAuth_ProvisionFailureStaysPending proves a provisioner error
// leaves NO creds Secret (fail-closed) — never a partial or wrong-org credential.
func TestEnsureTenantKMSAuth_ProvisionFailureStaysPending(t *testing.T) {
	k := fakeK8s()
	s := &cloud.Service[state]{Base: cloud.Base{KMS: newFakeKMS(), Log: luxlog.New("test")}, State: state{k8s: k, kmsIdentity: &fakeIdentity{fail: true}}}
	ensureTenantKMSAuth(s, context.Background(), "maxpower")
	if _, err := k.dyn.Resource(secretsGVR).Namespace("tenant-maxpower").Get(context.Background(), "platform-kms-auth", metav1.GetOptions{}); err == nil {
		t.Fatal("provisioner failure must leave no creds Secret (fail-closed)")
	}
}

// TestApplyKMSSecret_CreateUpdateDelete proves applyKMSSecret creates the CR when
// there are secret keys, patches it on change, and deletes it when there are none.
func TestApplyKMSSecret_CreateUpdateDelete(t *testing.T) {
	k := fakeK8s()
	ctx := context.Background()
	ns := "tenant-maxpower"

	if err := k.applyKMSSecret(ctx, "maxpower", "api", []string{"DB_PASSWORD"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := k.dyn.Resource(kmsSecretsGVR).Namespace(ns).Get(ctx, "api-env", metav1.GetOptions{}); err != nil {
		t.Fatalf("KMSSecret not created: %v", err)
	}
	// Idempotent update (still has keys).
	if err := k.applyKMSSecret(ctx, "maxpower", "api", []string{"DB_PASSWORD", "API_KEY"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// No keys → delete.
	if err := k.applyKMSSecret(ctx, "maxpower", "api", nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := k.dyn.Resource(kmsSecretsGVR).Namespace(ns).Get(ctx, "api-env", metav1.GetOptions{}); err == nil {
		t.Fatal("KMSSecret should be deleted when no secret keys remain")
	}
}

// TestObserveSecretSync proves the sync status is honest: "" with no secrets,
// pending when the CR is absent, ready when the operator reports Ready=True.
func TestObserveSecretSync(t *testing.T) {
	k := fakeK8s()
	ctx := context.Background()
	if st, _ := k.observeSecretSync(ctx, "maxpower", "api", false); st != "" {
		t.Fatalf("no secrets → empty status, got %q", st)
	}
	if st, _ := k.observeSecretSync(ctx, "maxpower", "api", true); st != "pending" {
		t.Fatalf("absent CR → pending, got %q", st)
	}
	// Seed a KMSSecret with a Ready=True condition and assert "ready".
	cr := k.kmsSecretCR("tenant-maxpower", "maxpower", "api", []string{"DB_PASSWORD"})
	_ = unstructured.SetNestedSlice(cr.Object, []any{
		map[string]any{"type": "Ready", "status": "True", "message": ""},
	}, "status", "conditions")
	if _, err := k.dyn.Resource(kmsSecretsGVR).Namespace("tenant-maxpower").Create(ctx, cr, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed CR: %v", err)
	}
	if st, _ := k.observeSecretSync(ctx, "maxpower", "api", true); st != "ready" {
		t.Fatalf("Ready=True → ready, got %q", st)
	}
}

// TestSecretEnvEndToEndDeploy is the money test: creating an app with a secret env
// and deploying it renders a valueFrom.secretKeyRef into the Service CR (never the
// plaintext) AND writes a KMSSecret CR that materializes the managed Secret.
func TestSecretEnvEndToEndDeploy(t *testing.T) {
	app, s := mountSvcK8s(t, fakeK8s())
	ctx := context.Background()
	do(t, app, "POST", "/v1/platform/projects", "maxpower", map[string]any{"name": "web"})
	if code, body := do(t, app, "POST", "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		"env": []map[string]any{{"key": "DB_PASSWORD", "value": "hunter2", "secret": true}, {"key": "PUBLIC", "value": "ok"}},
	}); code != 201 {
		t.Fatalf("create app: %d (%s)", code, body)
	}
	if code, body := do(t, app, "POST", "/v1/platform/projects/web/apps/api/deploy", "maxpower", nil); code != 202 {
		t.Fatalf("deploy: %d (%s)", code, body)
	}

	// Service CR: secret env rendered as secretKeyRef, plaintext nowhere.
	svcCR, err := s.State.k8s.dyn.Resource(servicesGVR).Namespace("tenant-maxpower").Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("service CR: %v", err)
	}
	svcJSON, _ := json.Marshal(svcCR.Object)
	if strings.Contains(string(svcJSON), "hunter2") {
		t.Fatalf("plaintext leaked into Service CR: %s", svcJSON)
	}
	env, _, _ := unstructured.NestedSlice(svcCR.Object, "spec", "env")
	var sawSecretRef, sawPlain bool
	for _, e := range env {
		m := e.(map[string]any)
		if m["name"] == "DB_PASSWORD" {
			skr, _, _ := unstructured.NestedString(m, "valueFrom", "secretKeyRef", "name")
			if skr == "api-env" {
				sawSecretRef = true
			}
		}
		if m["name"] == "PUBLIC" && m["value"] == "ok" {
			sawPlain = true
		}
	}
	if !sawSecretRef {
		t.Fatalf("DB_PASSWORD must render as secretKeyRef → api-env; env=%v", env)
	}
	if !sawPlain {
		t.Fatalf("PUBLIC must render as a plain value; env=%v", env)
	}

	// KMSSecret CR authored to materialize the managed Secret.
	kmsCR, err := s.State.k8s.dyn.Resource(kmsSecretsGVR).Namespace("tenant-maxpower").Get(ctx, "api-env", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("KMSSecret CR not authored on deploy: %v", err)
	}
	if got, _, _ := unstructured.NestedString(kmsCR.Object, "spec", "managedSecretReference", "secretName"); got != "api-env" {
		t.Fatalf("KMSSecret managed secretName: %s", got)
	}
}
