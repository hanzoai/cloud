package content

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/clients/framework"
	"github.com/zap-proto/zip"
)

// channels_test.go proves the REAL Distributor (channels.go) end-to-end against an
// httptest stub of hanzoai/social's Public API: the publish fan-out targets the right
// channels, records external_ids for reconciliation, handles partial channel failure
// honestly, distinguishes schedule vs now, fails closed when a brand has no key/channels,
// carries the per-brand API key as the Authorization header (tenancy), and the channels
// endpoint lists the brand's connected integrations.

// ── social Public API stub ──────────────────────────────────────────────────────

// socialStub is an in-memory hanzoai/social: GET /public/v1/integrations lists the
// configured channels; POST /public/v1/posts records the body and returns a post id,
// failing (400) for any integration id in failFor. It records every Authorization
// header so a test can assert per-brand credential propagation.
type socialStub struct {
	srv      *httptest.Server
	mu       sync.Mutex
	integs   []socialIntegration
	failFor  map[string]bool    // integration id → return 400 (simulate a channel outage)
	authSeen []string           // Authorization header on every call
	posts    []socialCreatePost // captured create-post bodies
	n        int
}

func newSocialStub(t *testing.T, integs []socialIntegration) *socialStub {
	t.Helper()
	s := &socialStub{integs: integs, failFor: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/public/v1/integrations", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.authSeen = append(s.authSeen, r.Header.Get("Authorization"))
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, s.integs)
	})
	mux.HandleFunc("/public/v1/posts", func(w http.ResponseWriter, r *http.Request) {
		var body socialCreatePost
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"msg": "bad body"})
			return
		}
		s.mu.Lock()
		s.authSeen = append(s.authSeen, r.Header.Get("Authorization"))
		s.posts = append(s.posts, body)
		s.n++
		id := body.Posts[0].Integration.ID
		fail := s.failFor[id]
		postID := "sp_" + id + "_" + itoa(s.n)
		s.mu.Unlock()
		if fail {
			writeJSON(w, http.StatusBadRequest, map[string]string{"msg": "provider rejected the post"})
			return
		}
		writeJSON(w, http.StatusOK, []socialPostResult{{PostID: postID, Integration: id}})
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// useStub points the mounted content subsystem's distributor at the stub, with a
// per-org api-key resolver (so a test can both reach the stub AND assert tenancy).
func useStub(t *testing.T, stub *socialStub) {
	t.Helper()
	if mounted == nil {
		t.Fatal("content not mounted")
	}
	mounted.State.dist = socialDistributor{
		baseURL: stub.srv.URL,
		http:    stub.srv.Client(),
		apiKey:  func(_ context.Context, org string) (string, error) { return "key-for-" + org, nil },
	}
}

// createSocialPost creates a draft SocialPost through the framework surface and returns
// its name. fields overrides/adds document fields (channels, caption, media, ...).
func createSocialPost(t *testing.T, app *zip.App, org string, fields map[string]any) string {
	t.Helper()
	body := map[string]any{"title": "Post"}
	for k, v := range fields {
		body[k] = v
	}
	code, b := req(t, app, http.MethodPost, "/v1/framework/SocialPost", org, body)
	if code != http.StatusCreated {
		t.Fatalf("create SocialPost: %d %s", code, b)
	}
	var created struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(b, &created)
	if created.Name == "" {
		t.Fatalf("SocialPost has no name: %s", b)
	}
	return created.Name
}

func installMarketing(t *testing.T, app *zip.App, org string) {
	t.Helper()
	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/marketing/install", org, nil); code != http.StatusOK {
		t.Fatalf("install marketing (%s): %d %s", org, code, b)
	}
}

func docExternalIDs(t *testing.T, org, name string) map[string]any {
	t.Helper()
	doc, err := framework.Get(context.Background(), org, "SocialPost", name)
	if err != nil {
		t.Fatalf("framework.Get: %v", err)
	}
	m, _ := doc.Data["external_ids"].(map[string]any)
	return m
}

var threeChannels = []socialIntegration{
	{ID: "int_x", Name: "Brand on X", Identifier: "x", Disabled: false},
	{ID: "int_ig", Name: "Brand on IG", Identifier: "instagram", Disabled: false},
	{ID: "int_tt", Name: "Brand on TikTok", Identifier: "tiktok", Disabled: true},
}

// ── unit: channel resolution ─────────────────────────────────────────────────────

func TestResolveTargets(t *testing.T) {
	// Empty request → every ENABLED channel (the disabled tiktok is excluded).
	tg, unk := resolveTargets(threeChannels, nil)
	if len(tg) != 2 || len(unk) != 0 {
		t.Fatalf("empty request should target both enabled channels: %+v unknown=%v", tg, unk)
	}
	// By provider identifier.
	tg, unk = resolveTargets(threeChannels, []string{"instagram"})
	if len(tg) != 1 || tg[0].ID != "int_ig" || len(unk) != 0 {
		t.Fatalf("provider match wrong: %+v unknown=%v", tg, unk)
	}
	// By exact integration id.
	tg, _ = resolveTargets(threeChannels, []string{"int_x"})
	if len(tg) != 1 || tg[0].ID != "int_x" {
		t.Fatalf("id match wrong: %+v", tg)
	}
	// A disabled channel named explicitly is NOT targeted → reported unknown.
	tg, unk = resolveTargets(threeChannels, []string{"tiktok"})
	if len(tg) != 0 || len(unk) != 1 || unk[0] != "tiktok" {
		t.Fatalf("disabled channel must be unknown, not targeted: %+v unknown=%v", tg, unk)
	}
	// Unknown token surfaces honestly; a duplicate id is de-duplicated.
	tg, unk = resolveTargets(threeChannels, []string{"int_x", "int_x", "nope"})
	if len(tg) != 1 || len(unk) != 1 || unk[0] != "nope" {
		t.Fatalf("dedupe+unknown wrong: %+v unknown=%v", tg, unk)
	}
}

// ── integration: publish fan-out over the stub ───────────────────────────────────

func TestPublishFanOutRecordsExternalIDs(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)
	stub := newSocialStub(t, threeChannels)
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{
		"caption":  "spring launch is live",
		"channels": "x,instagram",
		"media":    []any{map[string]any{"url": "https://cdn.test/orgs/acme/output/hero.png", "alt": "hero"}},
	})

	code, b := req(t, app, http.MethodPost, "/v1/content/publish", org,
		map[string]any{"doctype": "SocialPost", "name": name})
	if code != http.StatusOK {
		t.Fatalf("publish: %d %s", code, b)
	}
	var pr PublishResult
	if err := json.Unmarshal(b, &pr); err != nil {
		t.Fatalf("decode PublishResult: %v", err)
	}
	if pr.Status != "distributed" {
		t.Fatalf("expected distributed, got %q: %s", pr.Status, b)
	}
	if len(pr.Results) != 2 {
		t.Fatalf("expected 2 per-channel results, got %d: %s", len(pr.Results), b)
	}
	for _, r := range pr.Results {
		if r.Status != "distributed" || r.ExternalID == "" {
			t.Fatalf("channel %s not distributed: %+v", r.Channel, r)
		}
	}
	// external_ids: both integration ids → their social post ids, both in the RESULT
	// and PERSISTED on the document for reconciliation.
	if pr.ExternalIDs["int_x"] == "" || pr.ExternalIDs["int_ig"] == "" {
		t.Fatalf("result external ids incomplete: %+v", pr.ExternalIDs)
	}
	stored := docExternalIDs(t, org, name)
	if stored["int_x"] != pr.ExternalIDs["int_x"] || stored["int_ig"] != pr.ExternalIDs["int_ig"] {
		t.Fatalf("external_ids not persisted onto the doc: stored=%v result=%v", stored, pr.ExternalIDs)
	}
	// The disabled tiktok channel was never posted to.
	if _, ok := pr.ExternalIDs["int_tt"]; ok {
		t.Fatalf("disabled channel must not be posted: %+v", pr.ExternalIDs)
	}
	// The stub actually received two posts carrying the caption + media.
	if len(stub.posts) != 2 {
		t.Fatalf("stub should have received 2 posts, got %d", len(stub.posts))
	}
	p0 := stub.posts[0]
	if p0.Type != "now" {
		t.Fatalf("expected type now, got %q", p0.Type)
	}
	if got := p0.Posts[0].Value[0].Content; got != "spring launch is live" {
		t.Fatalf("caption not forwarded: %q", got)
	}
	if len(p0.Posts[0].Value[0].Image) != 1 || p0.Posts[0].Value[0].Image[0].Path != "https://cdn.test/orgs/acme/output/hero.png" {
		t.Fatalf("media not forwarded: %+v", p0.Posts[0].Value[0].Image)
	}
}

func TestPublishScheduleVsNow(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)
	stub := newSocialStub(t, threeChannels)
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "later", "channels": "x"})

	const when = "2026-08-01T12:00:00Z"
	code, b := req(t, app, http.MethodPost, "/v1/content/publish", org,
		map[string]any{"doctype": "SocialPost", "name": name, "scheduleAt": when})
	if code != http.StatusOK {
		t.Fatalf("schedule publish: %d %s", code, b)
	}
	var pr PublishResult
	_ = json.Unmarshal(b, &pr)
	if pr.Status != "scheduled" {
		t.Fatalf("expected scheduled, got %q: %s", pr.Status, b)
	}
	if len(stub.posts) != 1 || stub.posts[0].Type != "schedule" || stub.posts[0].Date != when {
		t.Fatalf("schedule not forwarded: %+v", stub.posts)
	}
}

func TestPublishPartialFailure(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)
	stub := newSocialStub(t, threeChannels)
	stub.failFor["int_ig"] = true // instagram is down
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "mixed", "channels": "x,instagram"})

	code, b := req(t, app, http.MethodPost, "/v1/content/publish", org,
		map[string]any{"doctype": "SocialPost", "name": name})
	// Partial failure is NEVER a 5xx — it is a 200 with the honest per-channel truth.
	if code != http.StatusOK {
		t.Fatalf("partial failure must be 200, got %d %s", code, b)
	}
	var pr PublishResult
	_ = json.Unmarshal(b, &pr)
	if pr.Status != "distributed" {
		t.Fatalf("some channels succeeded → distributed, got %q: %s", pr.Status, b)
	}
	byChan := map[string]ChannelResult{}
	for _, r := range pr.Results {
		byChan[r.Channel] = r
	}
	if byChan["int_x"].Status != "distributed" || byChan["int_x"].ExternalID == "" {
		t.Fatalf("x should have succeeded: %+v", byChan["int_x"])
	}
	if byChan["int_ig"].Status != "failed" || byChan["int_ig"].Error == "" {
		t.Fatalf("instagram should have failed with a reason: %+v", byChan["int_ig"])
	}
	// Only the successful channel is recorded for reconciliation.
	if _, ok := pr.ExternalIDs["int_ig"]; ok {
		t.Fatalf("failed channel must not record an external id: %+v", pr.ExternalIDs)
	}
	if got := docExternalIDs(t, org, name); got["int_x"] == "" || got["int_ig"] != nil {
		t.Fatalf("persisted external_ids should hold only the success: %v", got)
	}
}

func TestPublishAllChannelsFail(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)
	stub := newSocialStub(t, threeChannels)
	stub.failFor["int_x"] = true
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "doomed", "channels": "x"})
	code, b := req(t, app, http.MethodPost, "/v1/content/publish", org,
		map[string]any{"doctype": "SocialPost", "name": name})
	if code != http.StatusOK {
		t.Fatalf("total channel failure must still be 200 (honest), got %d %s", code, b)
	}
	var pr PublishResult
	_ = json.Unmarshal(b, &pr)
	if pr.Status != "failed" || len(pr.ExternalIDs) != 0 {
		t.Fatalf("nothing went out → failed with no external ids, got %q %+v", pr.Status, pr.ExternalIDs)
	}
}

func TestPublishUnknownChannelIsHonest(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)
	stub := newSocialStub(t, threeChannels)
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "typo", "channels": "x,mastodon"})
	code, b := req(t, app, http.MethodPost, "/v1/content/publish", org,
		map[string]any{"doctype": "SocialPost", "name": name})
	if code != http.StatusOK {
		t.Fatalf("publish: %d %s", code, b)
	}
	var pr PublishResult
	_ = json.Unmarshal(b, &pr)
	var mastodon *ChannelResult
	for i := range pr.Results {
		if pr.Results[i].Channel == "mastodon" {
			mastodon = &pr.Results[i]
		}
	}
	if mastodon == nil || mastodon.Status != "failed" || !strings.Contains(mastodon.Error, "not connected") {
		t.Fatalf("unknown channel must be reported failed/not-connected: %+v", pr.Results)
	}
}

// ── fail-closed (the scaffold contract) ──────────────────────────────────────────

func TestPublishFailClosedNoKey(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)
	stub := newSocialStub(t, threeChannels)
	// Distributor is real, but the brand has NO social key (never connected).
	mounted.State.dist = socialDistributor{
		baseURL: stub.srv.URL,
		http:    stub.srv.Client(),
		apiKey:  func(context.Context, string) (string, error) { return "", errNotConfigured },
	}

	name := createSocialPost(t, app, org, map[string]any{"caption": "no creds", "channels": "x"})
	// Direct publish → honest 503, never a crash.
	if code, b := req(t, app, http.MethodPost, "/v1/content/publish", org,
		map[string]any{"doctype": "SocialPost", "name": name}); code != http.StatusServiceUnavailable {
		t.Fatalf("no key must be 503 not_configured, got %d %s", code, b)
	}
	// The stub was never touched (fail-closed BEFORE any post).
	if len(stub.posts) != 0 {
		t.Fatalf("nothing must be posted without a key: %d", len(stub.posts))
	}
}

func TestPublishFailClosedNoChannels(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)
	stub := newSocialStub(t, []socialIntegration{}) // connected, but zero channels
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "nowhere", "channels": "x"})
	if code, b := req(t, app, http.MethodPost, "/v1/content/publish", org,
		map[string]any{"doctype": "SocialPost", "name": name}); code != http.StatusServiceUnavailable {
		t.Fatalf("zero channels must be 503 not_configured, got %d %s", code, b)
	}
}

func TestTransitionFailClosedRecordsNotConfigured(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)
	stub := newSocialStub(t, []socialIntegration{})
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "walk", "channels": "x"})
	tpath := "/v1/content/SocialPost/" + name + "/transition"
	for _, to := range []string{StatusInReview, StatusApproved, StatusPublished} {
		code, b := req(t, app, http.MethodPost, tpath, org, map[string]any{"to": to})
		if code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, b)
		}
		if to == StatusPublished {
			var tr TransitionResult
			_ = json.Unmarshal(b, &tr)
			// The status change SUCCEEDS; distribution honestly reports not_configured.
			if tr.To != StatusPublished || tr.Distribution == nil || tr.Distribution.Status != "not_configured" {
				t.Fatalf("published must record not_configured distribution, got: %s", b)
			}
		}
	}
}

// ── channels endpoint + tenancy ──────────────────────────────────────────────────

func TestChannelsEndpointListsIntegrations(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)
	stub := newSocialStub(t, threeChannels)
	useStub(t, stub)

	code, b := req(t, app, http.MethodGet, "/v1/content/channels", org, nil)
	if code != http.StatusOK {
		t.Fatalf("channels: %d %s", code, b)
	}
	var out struct {
		Data []Channel `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode channels: %v", err)
	}
	if len(out.Data) != 3 {
		t.Fatalf("expected 3 channels, got %d: %s", len(out.Data), b)
	}
	byID := map[string]Channel{}
	for _, c := range out.Data {
		byID[c.ID] = c
	}
	if byID["int_ig"].Provider != "instagram" || !byID["int_tt"].Disabled {
		t.Fatalf("channel mapping wrong: %+v", out.Data)
	}
}

// TestPublishCarriesPerBrandKey proves the brand's own API key is what reaches social
// (the Authorization header), and org A's publish never rides org B's key — tenancy at
// the credential boundary.
func TestPublishCarriesPerBrandKey(t *testing.T) {
	app := mountContent(t)
	stub := newSocialStub(t, threeChannels)
	useStub(t, stub) // apiKey resolves "key-for-<org>"

	for _, org := range []string{"acme", "globex"} {
		installMarketing(t, app, org)
		name := createSocialPost(t, app, org, map[string]any{"caption": "hi", "channels": "x"})
		if code, b := req(t, app, http.MethodPost, "/v1/content/publish", org,
			map[string]any{"doctype": "SocialPost", "name": name}); code != http.StatusOK {
			t.Fatalf("publish %s: %d %s", org, code, b)
		}
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	sawAcme, sawGlobex, leak := false, false, false
	for _, a := range stub.authSeen {
		switch a {
		case "key-for-acme":
			sawAcme = true
		case "key-for-globex":
			sawGlobex = true
		default:
			leak = true
		}
	}
	if !sawAcme || !sawGlobex || leak {
		t.Fatalf("per-brand key propagation wrong: %v", stub.authSeen)
	}
}
