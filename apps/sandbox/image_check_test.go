package sandbox

// The image field is caller-supplied and is spent against OUR pull secret, so
// these cases are a tenant boundary, not input hygiene.

import "testing"

func TestCheckImageRefusesAnotherOrgsNamespaceOnOurRegistry(t *testing.T) {
	for _, c := range []struct {
		name, org, image string
		wantErr          bool
	}{
		{"empty is not a request", "acme", "", false},
		{"a public library image needs no credential of ours", "acme", "node:22", false},
		{"a public registry is the caller's own business", "acme", "ghcr.io/someone/thing:v1", false},
		{"an org may name its own images on our registry", "acme", "oci.hanzo.ai/acme/tools:v1", false},
		{"any org may name the platform's own sandbox images", "acme", "oci.hanzo.ai/hanzoai/sandbox:dev-1.0.0", false},
		// THE HOLE. Our pull secret is fleet-wide, so without this the request
		// fetches another tenant's private image and nothing in it is forged.
		{"an org may NOT name another org's images on our registry", "acme", "oci.hanzo.ai/globex/private:v1", true},
		{"a host that is not ours spends no credential of ours", "acme", "registry.hanzo.ai/globex/private:v1", false},
		{"host match is case-insensitive", "acme", "OCI.HANZO.AI/globex/private:v1", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := checkImage(c.org, c.image)
			if c.wantErr && err == nil {
				t.Fatalf("checkImage(%q, %q) = nil, want a refusal", c.org, c.image)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("checkImage(%q, %q) = %v, want nil", c.org, c.image, err)
			}
		})
	}
}

// The closed set of runtimes moved to runtime_test.go, beside the derivation
// that reads it. Being in the set is only half the question a caller's runtime
// has to answer; the other half is whether it can hold that sandbox's volume,
// and splitting the two is what let a valid name lose a checkout.
