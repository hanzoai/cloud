package content

// blue_hardening_test.go — BLUE proofs for the two fixes Red's review demanded:
//   RED-1 belt-and-suspenders: Publish is idempotent per-channel (a retry / re-publish
//         never double-posts a channel and never clobbers recorded external_ids); and
//   RED-2: validateSource allows ONLY a safe studio-local/S3-key reference or an https
//          URL on the asset-host allowlist, and denies every SSRF/traversal shape.
// The single-edge fix (entersDistribution == published only) is proven by the updated
// TestRed_QueuedThenPublishedDoubleDistributes_BUG and TestLifecycleInvariants.

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestBlue_PublishIsIdempotentPerChannel proves the belt-and-suspenders guard: publishing
// the SAME item twice posts each channel exactly once and preserves (merges, never
// clobbers) the recorded external_ids — while still honestly reporting "distributed".
func TestBlue_PublishIsIdempotentPerChannel(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)
	stub := newSocialStub(t, threeChannels)
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "once", "channels": "x,instagram"})

	// Publish #1: both enabled channels go out; ids recorded.
	code, b := req(t, app, http.MethodPost, "/v1/content/publish", org,
		map[string]any{"doctype": "SocialPost", "name": name})
	if code != http.StatusOK {
		t.Fatalf("publish#1: %d %s", code, b)
	}
	var pr1 PublishResult
	_ = json.Unmarshal(b, &pr1)
	if pr1.Status != "distributed" || len(pr1.ExternalIDs) != 2 {
		t.Fatalf("publish#1 must distribute both channels: %+v", pr1)
	}
	stub.mu.Lock()
	after1 := len(stub.posts)
	stub.mu.Unlock()
	if after1 != 2 {
		t.Fatalf("publish#1 should post to 2 channels, got %d", after1)
	}
	first := docExternalIDs(t, org, name)

	// Publish #2 (retry / reconcile): NOTHING re-posts; external_ids preserved.
	code, b = req(t, app, http.MethodPost, "/v1/content/publish", org,
		map[string]any{"doctype": "SocialPost", "name": name})
	if code != http.StatusOK {
		t.Fatalf("publish#2: %d %s", code, b)
	}
	var pr2 PublishResult
	_ = json.Unmarshal(b, &pr2)
	// An already-distributed item stays "distributed" (idempotent success, never "failed").
	if pr2.Status != "distributed" {
		t.Fatalf("re-publish of an already-distributed item must stay distributed, got %q", pr2.Status)
	}
	if len(pr2.ExternalIDs) != 2 {
		t.Fatalf("re-publish must still report the full reconciliation set: %+v", pr2.ExternalIDs)
	}
	stub.mu.Lock()
	after2 := len(stub.posts)
	stub.mu.Unlock()
	if after2 != after1 {
		t.Fatalf("MONEY/DUP: re-publish must NOT post again, posts went %d → %d", after1, after2)
	}
	second := docExternalIDs(t, org, name)
	if len(second) != len(first) || second["int_x"] != first["int_x"] || second["int_ig"] != first["int_ig"] {
		t.Fatalf("external_ids must be preserved (merge, not clobber): first=%v second=%v", first, second)
	}
}

// TestBlue_ValidateSource is the allow/deny matrix for the SSRF/traversal gate.
func TestBlue_ValidateSource(t *testing.T) {
	t.Setenv("CONTENT_SOURCE_HOSTS", "assets.mybrand.example")

	allow := []string{
		"festival_fuchsia.png",                          // design-slug convention (studio-local)
		"orgs/acme/output/hero.png",                     // S3-style key: slashes ok, no traversal
		"https://s3.hanzo.ai/orgs/acme/output/hero.png", // allowlisted platform host
		"https://s3.lux.cloud/x.png",                    // allowlisted (branded)
		"https://assets.mybrand.example/x.png",          // operator-added host (env)
		"HTTPS://S3.HANZO.AI/x.png",                     // scheme + host case-insensitive
		"https://s3.hanzo.ai./x.png",                    // FQDN trailing dot == same host
	}
	for _, s := range allow {
		if err := validateSource(s); err != nil {
			t.Errorf("validateSource(%q) should ALLOW, got %v", s, err)
		}
	}

	deny := []string{
		"",                                  // empty
		"http://s3.hanzo.ai/x.png",          // http (not https), even to an allowed host
		"https://169.254.169.254/x",         // link-local metadata, bare IPv4
		"https://10.0.0.5/x",                // private IPv4
		"https://[::1]/x",                   // IPv6 loopback literal
		"https://[fd00::1]/x",               // IPv6 ULA literal
		"https://s3.hanzo.ai.evil.com/x",    // suffix-spoof host
		"https://evil.com/x",                // off-allowlist host
		"https://kms.internal.svc/v1/token", // cluster-internal name
		"file:///etc/passwd",                // non-http scheme
		"gopher://127.0.0.1:6379/_INFO",     // non-http scheme + loopback
		"../../etc/passwd",                  // traversal
		"a/b/../../../etc/passwd",           // traversal mid-path
		"/etc/passwd",                       // absolute path (LoadImage escape)
		"\\\\server\\share\\x",              // UNC / backslash absolute
	}
	for _, s := range deny {
		if err := validateSource(s); err == nil {
			t.Errorf("validateSource(%q) should DENY, got nil", s)
		}
	}
}
