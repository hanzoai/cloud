package content

// red_rereview_test.go — RED re-review of BLUE's fixes for RED-1 (double-distribution)
// and RED-2 (source_media SSRF). This suite does NOT trust Blue's own tests; it re-derives
// the guarantees from the outside and attacks the residuals Blue's tests do not cover:
//
//   - SSRF: a full exotic-encoding battery (decimal/octal/hex IP, IPv4-mapped IPv6, zone
//     ids, trailing-dot, userinfo, scheme-relative //, UNC, punycode/unicode homoglyph,
//     backslash + fragment parser differentials).
//   - ORDER: a hostile source_media must 400 BEFORE the billing gate and BEFORE any studio
//     HTTP call (no charge, no SSRF attempt on a rejected input).
//   - RED-1 forge: the skip-set must derive ONLY from server truth. A client that forges
//     external_ids via the generic framework PUT can suppress a real fan-out — proving the
//     skip-set is NOT server-truth-only today (integrity gap).
//   - RED-1 concurrency: two simultaneous publishes of the same item both read an empty
//     external_ids and both post — the per-channel idempotency guard does not cover the
//     concurrent window (double-post gap).

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
)

// ════════════════════════════════════════════════════════════════════════════════
// RED-2 — SSRF validator: exotic-encoding battery (HARD asserts)
// ════════════════════════════════════════════════════════════════════════════════

// TestRedReview_SSRF_EncodingBattery is the adversarial deny/allow matrix. Every DENY is
// an IP/host encoding trick that must NOT reach the studio; every ALLOW is a legitimate
// studio-local ref or allowlisted https URL.
func TestRedReview_SSRF_EncodingBattery(t *testing.T) {
	t.Setenv(srcHostEnv, "assets.brand.example")

	deny := map[string]string{
		// integer/hex/octal IP encodings of 127.0.0.1 / 169.254.169.254 — net.ParseIP does
		// NOT parse these, so they must be caught by the exact-name allowlist instead.
		"https://2130706433/x":   "decimal IP (127.0.0.1)",
		"https://0x7f000001/x":   "hex IP",
		"https://017700000001/x": "octal IP",
		"https://3232235521/x":   "decimal IP (192.168.0.1)",
		// IPv4-mapped / alt IPv6 spellings of link-local metadata.
		"https://[::ffff:169.254.169.254]/x":   "IPv4-mapped IPv6 metadata",
		"https://[::ffff:a9fe:a9fe]/x":         "hex IPv4-mapped IPv6 metadata",
		"https://[0:0:0:0:0:ffff:a9fe:a9fe]/x": "expanded IPv4-mapped IPv6",
		"https://[fe80::1%25eth0]/x":           "IPv6 link-local + zone id",
		// trailing-dot forms.
		"https://169.254.169.254./x": "trailing-dot bare IPv4",
		"https://127.0.0.1./x":       "trailing-dot loopback",
		// userinfo spoof: real host is after the @.
		"https://s3.hanzo.ai@169.254.169.254/x": "userinfo hides metadata host",
		"https://s3.hanzo.ai@evil.com/x":        "userinfo hides off-list host",
		"https://s3.hanzo.ai:pass@evil.com/x":   "userinfo w/ password hides host",
		// scheme-relative // → an arbitrary host if a consumer treats it as a URL.
		"//evil.com/x.png":        "scheme-relative URL to arbitrary host",
		"//169.254.169.254/x.png": "scheme-relative URL to metadata",
		// suffix / prefix host spoofs of an allowlisted name.
		"https://s3.hanzo.ai.evil.com/x": "suffix-spoof allowlisted host",
		"https://evils3.hanzo.ai/x":      "prefix-glued lookalike (not a real subdomain match)",
		"https://s3.hanzo.ai.attacker/x": "appended label",
		// homoglyph / punycode of s3.hanzo.ai.
		"https://ѕ3.hanzo.ai/x":        "cyrillic-s homoglyph",
		"https://xn--3-7sb.hanzo.ai/x": "punycode lookalike",
		// off-allowlist + non-https + non-http schemes.
		"https://evil.com/x":                                   "off-allowlist host",
		"http://s3.hanzo.ai/x":                                 "http to allowlisted host (https-only)",
		"https://kms.internal.svc/v1/token":                    "cluster-internal name",
		"https://metadata.google.internal/computeMetadata/v1/": "GCE metadata name",
		"file:///etc/passwd":                                   "file scheme",
		"gopher://127.0.0.1:6379/_INFO":                        "gopher + loopback",
		"ftp://s3.hanzo.ai/x":                                  "ftp scheme",
		// local-ref traversal / absolute / UNC.
		"../../etc/passwd":        "literal traversal",
		"a/b/../../../etc/passwd": "mid-path literal traversal",
		"/etc/passwd":             "absolute path",
		`\\server\share\x`:        "UNC / backslash absolute",
		`\etc\passwd`:             "backslash absolute",
		"":                        "empty",
		"  ":                      "whitespace only",
	}
	for src, why := range deny {
		if err := validateSource(src); err == nil {
			t.Errorf("SSRF BYPASS: validateSource(%q) allowed — %s", src, why)
		}
	}

	allow := []string{
		"festival_fuchsia.png",
		"orgs/acme/output/hero.png",
		"https://s3.hanzo.ai/orgs/acme/output/hero.png",
		"https://s3.lux.cloud/x.png",
		"https://s3.zoo.network/x.png",
		"https://s3.hanzo.ai./x.png",         // one trailing dot on an allowed name
		"HTTPS://S3.HANZO.AI/x.png",          // case-insensitive scheme + host
		"https://assets.brand.example/x.png", // operator-added via CONTENT_SOURCE_HOSTS
	}
	for _, src := range allow {
		if err := validateSource(src); err != nil {
			t.Errorf("FALSE-DENY: validateSource(%q) should ALLOW, got %v", src, err)
		}
	}
}

// TestRedReview_SSRF_BackslashFailOpen is the RED-2 residual that BLUE's own matrix missed:
// Go's url.Parse ERRORS on a backslash (or a bad %-escape) in the authority, and the old
// validateSource treated a parse error as "not a URL → local ref", which only checks
// absolute/traversal — skipping the host allowlist AND the IP-literal block. So an
// https:// URL to an ARBITRARY or INTERNAL host slipped through as a "filename". A
// downstream Python/WHATWG fetcher (the studio's URL loader — the very reason URL sources
// exist) resolves `\@evil.com` to host evil.com, making this a live SSRF-allowlist bypass.
// These MUST all deny: the SSRF gate must FAIL CLOSED on any input it cannot cleanly parse.
func TestRedReview_SSRF_BackslashFailOpen(t *testing.T) {
	mustDeny := []string{
		`https://s3.hanzo.ai\@evil.com/x`,     // userinfo-split → host evil.com downstream
		`https://169.254.169.254\@evil.com/x`, // metadata IP smuggled past the IP block
		`https://evil.com\@s3.hanzo.ai/x`,     // off-allowlist URL routed as a local ref
		`https://[::1]\@x`,                    // loopback IPv6 past the IP-literal block
		`https://evil.com%/x`,                 // bad %-escape → parse error → fail-open
		`https://s3.hanzo.ai\.evil.com/x`,     // backslash label separator
		`https:\\evil.com\x`,                  // backslash slashes
	}
	for _, s := range mustDeny {
		if err := validateSource(s); err == nil {
			t.Errorf("SSRF FAIL-OPEN: validateSource(%q) allowed — an unparseable/backslash URL "+
				"must deny (fail closed), not downgrade to a local ref", s)
		}
	}
}

// TestRedReview_SSRF_ParserDifferentialProbes logs (does not hard-assert) the tricky
// parser-differential shapes so the re-review records EXACTLY what Go's url.Parse computes
// and whether Blue's validator is stricter-or-equal to any downstream fetcher. A case that
// Go REJECTS is safe regardless of the studio's parser; a case Go ALLOWS but that resolves
// internally on a WHATWG/python parser is the residual to note.
func TestRedReview_SSRF_ParserDifferentialProbes(t *testing.T) {
	probes := []string{
		`https://s3.hanzo.ai\@evil.com/x`,   // backslash-as-slash differential (browsers/WHATWG)
		`https://s3.hanzo.ai\.evil.com/x`,   // backslash label separator
		"https://s3.hanzo.ai#@evil.com/x",   // fragment after allowlisted host (safe: real host is s3)
		"https://s3.hanzo.ai?@evil.com/x",   // query after allowlisted host
		"%2e%2e/%2e%2e/etc/passwd",          // percent-encoded traversal (inert unless downstream decodes)
		"..%2fetc/passwd",                   // mixed encoded traversal
		"https://s3.hanzo.ai%2f@evil.com/x", // encoded slash in userinfo
	}
	for _, s := range probes {
		err := validateSource(s)
		verdict := "ALLOW"
		if err != nil {
			verdict = "DENY"
		}
		t.Logf("PROBE %-38q -> %s", s, verdict)
	}
}

// ════════════════════════════════════════════════════════════════════════════════
// RED-2 — ORDER: reject BEFORE bill, BEFORE studio
// ════════════════════════════════════════════════════════════════════════════════

// TestRedReview_HostileSource_400_NoBill_NoStudio proves a hostile source_media is a 400
// that never spawns a studio call and never bills. It drives the real generate handler with
// a counting studio + counting meter and asserts zero submits + zero balance gates.
func TestRedReview_HostileSource_400_NoBill_NoStudio(t *testing.T) {
	const org = "acme"
	studio := newCountingStudio(t, http.StatusOK) // would succeed IF ever contacted
	t.Setenv("CONTENT_STUDIO_URL", studio.srv.URL)
	meter := newRedMeter(t, 100000) // funded — a bill WOULD go through if the gate ran

	app := mountWith(t, cloud.Deps{Metering: meter.client})
	install(t, app, org)

	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset, "kind": "hero",
		"source_media": "https://169.254.169.254/latest/meta-data/iam/security-credentials/",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("hostile source_media must be 400, got %d %s", code, b)
	}
	if g := meter.gateCount(); g != 0 {
		t.Fatalf("MONEY: SSRF-rejected input must NOT hit the billing gate, got %d gates", g)
	}
	if s := studio.submitCount(); s != 0 {
		t.Fatalf("SSRF: rejected input must NOT reach the studio, got %d submits", s)
	}
	meter.expectNoDebit(t)
}

// ════════════════════════════════════════════════════════════════════════════════
// RED-1 — forge external_ids: skip-set is NOT server-truth-only (integrity gap)
// ════════════════════════════════════════════════════════════════════════════════

// TestRedReview_ForgeExternalIDs_Rejected is the RED-1 forge gap, now CLOSED (was
// TestRedReview_ForgeExternalIDs_SuppressesDistribution_DEMONSTRATES_GAP). external_ids is
// SERVER-MANAGED: the enforceServerOwned before_save gate rejects any client-attempted
// change to it (only the trusted Publish path writes it), so a raw framework PUT can no
// longer pre-seed the skip-set. The forge is refused with a 4xx, never persists, and a
// later publish therefore posts the REAL fan-out from SERVER truth.
func TestRedReview_ForgeExternalIDs_Rejected(t *testing.T) {
	const org = "acme"
	app := mountContent(t)
	installMarketing(t, app, org)
	stub := newSocialStub(t, threeChannels)
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "real launch", "channels": "x,instagram"})

	// FORGE: client raw-PUTs external_ids for BOTH channels while status stays draft
	// (a no-op status edge is legal). The status edge passes; the server-owned-field gate
	// does NOT — external_ids cannot be set by a client.
	forge := map[string]any{
		"title":        "Post",
		"caption":      "real launch",
		"channels":     "x,instagram",
		"status":       StatusDraft,
		"external_ids": map[string]any{"int_x": "FORGED", "int_ig": "FORGED"},
	}
	code, b := req(t, app, http.MethodPut, "/v1/framework/SocialPost/"+name, org, forge)
	if code < 400 || code >= 500 {
		t.Fatalf("forge PUT external_ids must be rejected with a 4xx, got %d %s", code, b)
	}
	// The forged ids never persisted — external_ids is still empty server-side.
	if ext := docExternalIDs(t, org, name); len(ext) != 0 {
		t.Fatalf("forged external_ids must NOT persist, got %v", ext)
	}

	// Walk to published — the ONE distribution edge. The skip-set now derives from SERVER
	// truth (empty), so the real fan-out runs.
	for _, to := range []string{StatusInReview, StatusApproved, StatusPublished} {
		if code, bb := req(t, app, http.MethodPost, "/v1/content/SocialPost/"+name+"/transition", org,
			map[string]any{"to": to}); code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, bb)
		}
	}

	stub.mu.Lock()
	posts := len(stub.posts)
	stub.mu.Unlock()

	// CLOSED: the forge could not suppress anything — the fan-out posted to both enabled
	// channels (x + instagram; tiktok is disabled in threeChannels).
	if posts != 2 {
		t.Fatalf("real fan-out must post to both channels (x, instagram), got %d", posts)
	}
	// And Publish recorded the REAL, server-managed post ids — never the forged sentinels.
	ext := docExternalIDs(t, org, name)
	if len(ext) != 2 || ext["int_x"] == "FORGED" || ext["int_ig"] == "FORGED" {
		t.Fatalf("external_ids must be the server-recorded post ids, not forged values, got %v", ext)
	}
	t.Logf("RED-1 FORGE CLOSED: client PUT of external_ids rejected (%d), real fan-out posted %d channels; "+
		"skip-set is server-truth-only.", code, posts)
}

// ════════════════════════════════════════════════════════════════════════════════
// RED-1 — concurrent double-publish: idempotency does not cover the concurrent window
// ════════════════════════════════════════════════════════════════════════════════

// TestRedReview_ConcurrentPublish_PostsExactlyOnce is the RED-1 concurrency gap, now
// CLOSED (was TestRedReview_ConcurrentPublish_DoublePosts_DEMONSTRATES_GAP). It fires N
// simultaneous publishes of the SAME item, released together to maximize the read-read
// overlap. A store-backed per-item publish LEASE now serializes the read-skipset →
// fan-out → record section: the winner posts and records external_ids; every contender
// re-reads that recorded set after the winner releases and skips the already-posted
// channel. Net: channel 'int_x' is posted EXACTLY once no matter how many publishers race.
func TestRedReview_ConcurrentPublish_PostsExactlyOnce(t *testing.T) {
	const org = "acme"
	app := mountContent(t)
	installMarketing(t, app, org)
	stub := newSocialStub(t, []socialIntegration{{ID: "int_x", Identifier: "x"}})
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "concurrent", "channels": "x"})
	// Advance to approved so the item is a legal publish target, WITHOUT distributing.
	for _, to := range []string{StatusInReview, StatusApproved} {
		if code, b := req(t, app, http.MethodPost, "/v1/content/SocialPost/"+name+"/transition", org,
			map[string]any{"to": to}); code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, b)
		}
	}

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines at once to maximize the read-read overlap
			_, errs[i] = Publish(context.Background(), org, PublishInput{DocType: DocTypeSocialPost, Name: name})
		}(i)
	}
	close(start)
	wg.Wait()

	// Honest never-5xx: a lease-contended publish returns a clean result, never an error.
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent publish %d must return a clean result, got error: %v", i, err)
		}
	}

	stub.mu.Lock()
	posts := len(stub.posts)
	stub.mu.Unlock()

	// CLOSED: exactly one post to 'int_x' across N concurrent publishers.
	if posts != 1 {
		t.Fatalf("RED-1 concurrency: %d concurrent publishers must post to 'int_x' exactly once, got %d", n, posts)
	}
	// external_ids records exactly the one channel (merge is order-safe, no clobber).
	if ext := docExternalIDs(t, org, name); len(ext) != 1 || ext["int_x"] == "" {
		t.Fatalf("external_ids must hold exactly the one posted channel, got %v", ext)
	}
	t.Logf("RED-1 CONCURRENCY CLOSED: %d concurrent publishers posted to 'int_x' exactly once.", n)
}
