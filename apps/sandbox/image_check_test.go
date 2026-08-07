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
		{"the deprecated alias is still our registry", "acme", "registry.hanzo.ai/globex/private:v1", true},
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

// A runtime is passed to the apiserver as runtimeClassName, so an unknown value
// is a pod that never schedules. Refusing it here turns a silent Pending into a
// 400 that says which runtimes exist.
func TestCheckRuntimeIsAClosedSet(t *testing.T) {
	for _, c := range []struct {
		rc      string
		wantErr bool
	}{
		{"", false}, {"gvisor", false}, {"kata-fc", false}, {"kata-clh", false},
		{"runsc", true}, {"gVisor", true}, {"anything", true},
	} {
		if err := checkRuntime(c.rc); (err != nil) != c.wantErr {
			t.Fatalf("checkRuntime(%q) err=%v, wantErr=%v", c.rc, err, c.wantErr)
		}
	}
}
