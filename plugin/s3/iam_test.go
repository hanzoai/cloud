package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"strings"

	"github.com/hanzoai/cloud/apps/provisioning"
)

// TestConfigureNamesThisDeploymentsIAM: the file the store reads describes the
// issuer this deployment already validates against, not a default.
func TestConfigureNamesThisDeploymentsIAM(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLOUD_DATA_DIR", dir)
	t.Setenv("CLOUD_IAM_ISSUER", "https://hanzo.id")
	t.Setenv("IAM_AUDIENCE", "hanzo-cloud")
	t.Setenv("CLOUD_JWKS_URL", "https://hanzo.id/v1/iam/.well-known/jwks")

	path, err := write(dir, document("https://hanzo.id", []byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if path != filepath.Join(dir, "iam.json") {
		t.Fatalf("path = %q", path)
	}

	// The key signs credentials, so the file is not world-readable.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600: the file carries the session signing key", perm)
	}

	var doc struct {
		STS struct {
			SigningKey []byte `json:"signingKey"`
			Providers  []struct {
				Name   string `json:"name"`
				Type   string `json:"type"`
				Config struct {
					Issuer    string   `json:"issuer"`
					ClientIDs []string `json:"clientIds"`
					JWKSUri   string   `json:"jwksUri"`
				} `json:"config"`
			} `json:"providers"`
		} `json:"sts"`
		Policy struct {
			DefaultEffect string `json:"defaultEffect"`
		} `json:"policy"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the store has to be able to read it: %v", err)
	}
	if len(doc.STS.Providers) != 1 {
		t.Fatalf("providers = %d, want the one this deployment has", len(doc.STS.Providers))
	}
	p := doc.STS.Providers[0]
	if p.Name != federation || p.Type != "oidc" {
		t.Errorf("provider = %s/%s", p.Name, p.Type)
	}
	if p.Config.Issuer != "https://hanzo.id" {
		t.Errorf("issuer = %q", p.Config.Issuer)
	}
	if len(p.Config.ClientIDs) != 1 || p.Config.ClientIDs[0] != "hanzo-cloud" {
		t.Errorf("audience = %v; a provider that accepts any audience accepts a token minted for someone else", p.Config.ClientIDs)
	}
	if p.Config.JWKSUri != "https://hanzo.id/v1/iam/.well-known/jwks" {
		t.Errorf("jwks = %q", p.Config.JWKSUri)
	}
	if doc.Policy.DefaultEffect != "Deny" {
		t.Errorf("defaultEffect = %q, want Deny: an org with no role yet must reach nothing", doc.Policy.DefaultEffect)
	}
	if len(doc.STS.SigningKey) == 0 {
		t.Error("no session signing key; STS would mint credentials nothing could verify")
	}
}

// TestConfigureIsSilentWithoutAnIssuer: a deployment that names no IAM runs on
// the admin credential rather than failing to boot.
func TestConfigureIsSilentWithoutAnIssuer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLOUD_IAM_ISSUER", "")
	path, err := configure(dir)
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want none", path)
	}
	if _, err := os.Stat(filepath.Join(dir, "iam.json")); !os.IsNotExist(err) {
		t.Error("wrote a config for an issuer that was never named")
	}
}

// TestRoleAdmitsOnlyItsOwnOrg: the trust condition IS the tenancy boundary, so
// its absence would let one tenant assume another's role by naming it.
func TestRoleAdmitsOnlyItsOwnOrg(t *testing.T) {
	r := role("acme")
	if r.RoleName != "org-acme" || r.RoleArn != "arn:aws:iam::role/org-acme" {
		t.Fatalf("role = %s / %s", r.RoleName, r.RoleArn)
	}
	if len(r.AttachedPolicies) != 1 || r.AttachedPolicies[0] != "org-acme" {
		t.Errorf("attached = %v, want the org's own scope", r.AttachedPolicies)
	}
	if r.TrustPolicy == nil || len(r.TrustPolicy.Statement) != 1 {
		t.Fatal("a role with no trust policy is assumable by every user this provider authenticates")
	}
	st := r.TrustPolicy.Statement[0]
	if st.Effect != "Allow" || len(st.Action) != 1 || st.Action[0] != "sts:AssumeRoleWithWebIdentity" {
		t.Errorf("statement = %s %v", st.Effect, st.Action)
	}
	if got := st.Principal.(map[string]interface{})["Federated"]; got != federation {
		t.Errorf("federated = %v, want this deployment's own IAM", got)
	}
	if got := st.Condition["StringEquals"]["oidc:owner"]; got != "acme" {
		t.Errorf("condition = %v, want the org's own claim", got)
	}
}

// TestScopeReachesOneOrgsBuckets: the resources are that org's prefix, which is
// a hash of its name — the whole reason a role exists per org rather than one
// role carrying a claim variable.
func TestScopeReachesOneOrgsBuckets(t *testing.T) {
	prefix := provisioning.BucketPrefix("acme")
	if prefix == "" || prefix == "-" {
		t.Fatalf("prefix = %q", prefix)
	}
	doc := scope("acme")
	var listable, objects bool
	for _, st := range doc.Statement {
		for _, r := range st.Resource {
			switch r {
			case "arn:aws:s3:::" + prefix + "*":
				listable = true
			case "arn:aws:s3:::" + prefix + "*/*":
				objects = true
			case "*", "arn:aws:s3:::*", "arn:aws:s3:::*/*":
				t.Errorf("resource %q reaches every tenant", r)
			}
		}
	}
	if !listable {
		t.Error("no statement names the buckets themselves; the role cannot list them")
	}
	if !objects {
		t.Error("no statement names the objects; the role can list but not read")
	}
	// Another org's prefix is a different string, which is what makes the scope
	// a boundary rather than a label.
	if other := provisioning.BucketPrefix("other"); strings.Contains(doc.Statement[0].Resource[0], other) {
		t.Error("acme's scope contains another org's prefix")
	}
}

// TestEnrollIsQuietWithoutAManager: a deployment whose store is elsewhere still
// creates buckets.
func TestEnrollIsQuietWithoutAManager(t *testing.T) {
	held.Store(nil)
	if err := enroll(context.Background(), "acme"); err != nil {
		t.Errorf("enroll without IAM = %v, want nil", err)
	}
}
