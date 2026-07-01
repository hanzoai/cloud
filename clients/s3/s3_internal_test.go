package s3

// White-box unit tests for the pure security-critical helpers: object-key
// traversal rejection, prefix normalization, and the tenant↔physical bucket
// mapping. These are the guards RED will probe first, so they are tested
// deterministically here without any S3 backend.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/provisioning"
)

// TestCleanKeyRejectsTraversal: an object key may nest with "/", but must never
// be absolute, empty, a directory marker, or escape the bucket via "..".
func TestCleanKeyRejectsTraversal(t *testing.T) {
	ok := []struct{ in, want string }{
		{"file.txt", "file.txt"},
		{"a/b/c.png", "a/b/c.png"},
		{"deep/nested/path/data.json", "deep/nested/path/data.json"},
		{"./rel.txt", "rel.txt"}, // path.Clean strips a leading "./"
		{"a/./b.txt", "a/b.txt"}, // interior "./" collapses
		{"name with spaces.txt", "name with spaces.txt"},
	}
	for _, c := range ok {
		got, valid := cleanKey(c.in)
		if !valid {
			t.Errorf("cleanKey(%q) rejected a valid key", c.in)
			continue
		}
		if got != c.want {
			t.Errorf("cleanKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	bad := []string{
		"",                   // empty
		"   ",                // whitespace-only
		"/etc/passwd",        // absolute
		"folder/",            // directory marker
		"..",                 // parent
		"../secret",          // escape up
		"a/../../etc/passwd", // escape via interior ..
		"a/../../../root",    // multi-escape
		"../../..",           // all parents
		// RED LOW hardening: control bytes + backslash are rejected.
		"a\x00b",        // null byte (would serialize as %00, C-string truncation risk)
		"a\tb",          // tab (control)
		"a\nb",          // newline (control)
		"a\x1fb",        // unit separator (control)
		`..\..\windows`, // Windows-style backslash traversal
		`a\b`,           // bare backslash
	}
	for _, in := range bad {
		if got, valid := cleanKey(in); valid {
			t.Errorf("cleanKey(%q) = %q ACCEPTED, want rejected (traversal/control/backslash)", in, got)
		}
	}
}

// TestCleanKeyNeverEscapesBucket: property test — for any key cleanKey accepts,
// the result must never begin with "../" or "/" (i.e. can never address a
// sibling bucket or an absolute path). This is the isolation guarantee.
func TestCleanKeyNeverEscapesBucket(t *testing.T) {
	inputs := []string{
		"a/../../../../../../etc/passwd",
		"....//....//etc",
		"foo/%2e%2e/bar", // literal (already url-decoded by the router in prod)
		"a/b/../c",
		"legit/key.bin",
	}
	for _, in := range inputs {
		got, valid := cleanKey(in)
		if !valid {
			continue // rejected — safe
		}
		if got == "" || got[0] == '/' {
			t.Errorf("cleanKey(%q) = %q escapes to absolute/root", in, got)
		}
		if got == ".." || len(got) >= 3 && got[:3] == "../" {
			t.Errorf("cleanKey(%q) = %q escapes the bucket via ..", in, got)
		}
	}
}

// TestCleanPrefixCoercesUnsafeToRoot: a prefix may be empty or a folder, but an
// unsafe prefix (absolute or containing "..") is coerced to root — a listing can
// never be pointed at another location.
func TestCleanPrefixCoercesUnsafeToRoot(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"photos/":   "photos/",
		"a/b/":      "a/b/",
		"nested":    "nested",
		"/absolute": "", // absolute → root
		"../escape": "", // traversal → root
		"a/../b":    "", // interior traversal → root
		"ok/..":     "", // trailing traversal → root
	}
	for in, want := range cases {
		if got := cleanPrefix(in); got != want {
			t.Errorf("cleanPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPhysicalBucketIsOrgScoped: the physical bucket name embeds the caller's org
// hash, so the SAME friendly name in two different orgs maps to two DIFFERENT
// physical buckets — cross-tenant collision is impossible by construction.
func TestPhysicalBucketIsOrgScoped(t *testing.T) {
	acme := physicalBucket("acme", "photos")
	globex := physicalBucket("globex", "photos")
	if acme == globex {
		t.Fatalf("physicalBucket collided across orgs: %q == %q", acme, globex)
	}
	// Both must carry their own org prefix.
	if got := orgPrefix("acme"); acme[:len(got)] != got {
		t.Errorf("acme bucket %q missing prefix %q", acme, got)
	}
	if got := orgPrefix("globex"); globex[:len(got)] != got {
		t.Errorf("globex bucket %q missing prefix %q", globex, got)
	}
}

// TestFriendlyBucketRoundTrips: a physical bucket owned by org recovers its
// friendly name; a physical bucket from ANOTHER org is invisible (not owned).
func TestFriendlyBucketRoundTrips(t *testing.T) {
	physical := physicalBucket("acme", "photos")

	name, ok := friendlyBucket("acme", physical)
	if !ok {
		t.Fatal("friendlyBucket(acme, acme's bucket) = not owned, want owned")
	}
	if name != "photos" {
		t.Errorf("friendlyBucket recovered %q, want photos", name)
	}

	// Another org must NOT be able to claim acme's bucket.
	if _, ok := friendlyBucket("globex", physical); ok {
		t.Error("friendlyBucket(globex, acme's bucket) = owned — cross-tenant leak!")
	}

	// RED LOW: a bucket carrying the org prefix but a NON-conforming name (could
	// only exist via an out-of-band route, never createBucket) is treated as
	// not-owned, so listBuckets never echoes an unaddressable name to the UI.
	prefixed := orgPrefix("acme") + "Not_A_Valid_Name" // uppercase + underscore
	if _, ok := friendlyBucket("acme", prefixed); ok {
		t.Errorf("friendlyBucket recovered a non-conforming name %q — should be skipped", prefixed)
	}
}

// TestPhysicalBucketMatchesProvisioningAndIsDNSSafe: the file manager's bucket
// name MUST equal what provisioning allocates (so a POST /v1/s3 {name} bucket is
// browsable here) AND be a DNS-safe S3 name (no '_', which SeaweedFS/S3 reject).
// This locks in the fix for the underscore mismatch: physicalBucket used to
// return the raw physicalName ("o<hash>_photos") which (a) differed from
// provisioning's bucketName ("o<hash>-photos") and (b) is an invalid S3 name.
func TestPhysicalBucketMatchesProvisioningAndIsDNSSafe(t *testing.T) {
	for _, name := range []string{"photos", "my-bucket", "data123", "a"} {
		got := physicalBucket("acme", name)
		// Must equal provisioning's own derivation for kind "s3".
		if want := provisioning.BucketName("acme", name); got != want {
			t.Errorf("physicalBucket(acme,%q)=%q, want provisioning.BucketName=%q", name, got, want)
		}
		// Must be a DNS-safe S3 bucket name: no underscore, lowercase, no
		// leading/trailing hyphen.
		if strings.Contains(got, "_") {
			t.Errorf("physicalBucket(acme,%q)=%q contains '_' — invalid S3 bucket name", name, got)
		}
		if got != strings.ToLower(got) {
			t.Errorf("physicalBucket(acme,%q)=%q not lowercase", name, got)
		}
		if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Errorf("physicalBucket(acme,%q)=%q has a leading/trailing hyphen", name, got)
		}
		// The friendly name must round-trip back out.
		if rec, ok := friendlyBucket("acme", got); !ok || rec != name {
			t.Errorf("round-trip of %q: friendlyBucket=%q,%v want %q,true", name, rec, ok, name)
		}
	}
}

// TestFriendlyParamValidation: bucket path params are validated against the
// friendly-name shape before any physical name is derived.
func TestFriendlyParamValidation(t *testing.T) {
	good := []string{"photos", "my-bucket", "a", "data123", "x-y-z"}
	for _, in := range good {
		if _, ok := friendlyParam(in); !ok {
			t.Errorf("friendlyParam(%q) rejected a valid name", in)
		}
	}
	bad := []string{"", "-leading", "trailing-", "UPPER", "under_score", "has/slash", "has.dot", "has space", "..", "way-too-long-bucket-name-that-exceeds-the-forty-character-slug-limit"}
	for _, in := range bad {
		if _, ok := friendlyParam(in); ok {
			t.Errorf("friendlyParam(%q) ACCEPTED an invalid name", in)
		}
	}
}
