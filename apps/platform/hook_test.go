package platform

// hook_test.go drives the forge's push door over REAL HTTP against the real
// route, because the two properties that matter are both properties of the wire:
// what a delivery has to carry to be believed, and what running it costs the
// fleet. Both clients are observed through the same registrations production uses
// — the push builder and the lifecycle subscriber list — so a test cannot pass by
// asserting a call it made itself.
//
// The door this replaces was tested by registering a builder in-process and
// checking the call that registration had just made possible, which is why its
// suite stayed green for as long as the door was dead. Every assertion here is
// about the CLIENT, and the fired/not-fired counts are the whole point.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	hookSecret = "s3cr3t-forge-hook"
	hookOwner  = "hanzoai" // the forge namespace the closed table maps
	hookOrg    = "hanzo"   // ... to this IAM org
	hookCommit = "deadbeefcafe0123456789abcdef0123456789ab"
	hookBefore = "0123456789abcdef0123456789abcdef01234567"
)

// fired records what the two clients received. Both are appended under one lock so
// a test reads a consistent pair.
type fired struct {
	mu     sync.Mutex
	pushes []cloud.GitPushEvent
	events []cloud.LifecycleEvent
}

func (f *fired) push(ev cloud.GitPushEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes = append(f.pushes, ev)
}

func (f *fired) event(ev cloud.LifecycleEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
}

func (f *fired) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pushes), len(f.events)
}

// settle waits for the lifecycle fan-out, which EmitLifecycle dispatches on its
// own goroutine so a slow reactor can never delay a push. Polling to a deadline
// rather than sleeping a fixed span: a passing run is fast and a failing one is
// still deterministic.
func (f *fired) settle(t *testing.T, pushes, events int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		p, e := f.counts()
		if p == pushes && e == events {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("clients fired %d push / %d lifecycle, want %d / %d", p, e, pushes, events)
		}
		time.Sleep(time.Millisecond)
	}
}

// hookApp mounts the forge door the way Mount does — the raw route over a Service
// whose KMS holds `key` — and registers both clients so the test observes exactly
// what production dispatches. An empty key seals nothing, which is how the
// no-secret refusal is exercised.
//
// The Service carries NO STORE, and that is an assertion rather than a shortcut:
// this door verifies, resolves and dispatches, and reads nothing of platform's
// own state. A version of it that grew a read would stop building here.
func hookApp(t *testing.T, key string) (*zip.App, *fired) {
	t.Helper()
	return hookAppOn(t, key, "api.hanzo.ai")
}

// hookAppOn is hookApp for a deployment on a given API host — the one thing the
// forge host is derived from, so an empty one is a deployment that names no forge.
func hookAppOn(t *testing.T, key, domain string) (*zip.App, *fired) {
	t.Helper()
	return hookAppWith(t, sealed(t, key), domain, nil)
}

// sealed is a KMS holding key at the ref the door reads. An empty key seals
// nothing, which is the deployment whose secret was never provisioned.
func sealed(t *testing.T, key string) *fakeKMS {
	t.Helper()
	kms := newFakeKMS()
	if key != "" {
		if err := kms.PutSecret(context.Background(), forge.WebhookRef, []byte(key)); err != nil {
			t.Fatalf("seal webhook secret: %v", err)
		}
	}
	return kms
}

// hookAppWith is hookAppOn over a caller-supplied KMS and builder, so a test can
// make either FAIL the way production can — a KMS that stops answering, a
// draining platform, no peer to ask.
func hookAppWith(t *testing.T, kms cloud.KMSClient, domain string, build func(context.Context, cloud.GitPushEvent) (int, error)) (*zip.App, *fired) {
	t.Helper()
	s := &cloud.Service[state]{
		Base: cloud.Base{KMS: kms, Log: luxlog.New("test"), Brand: "hanzo", Domain: domain},
	}

	f := &fired{}
	cloud.RegisterPushBuilder(func(ctx context.Context, ev cloud.GitPushEvent) (int, error) {
		f.push(ev)
		if build != nil {
			return build(ctx, ev)
		}
		return 0, nil
	})
	cloud.ResetLifecycleSubscribers()
	cloud.RegisterLifecycleSubscriber(func(_ context.Context, ev cloud.LifecycleEvent) { f.event(ev) })
	t.Cleanup(func() {
		cloud.RegisterPushBuilder(nil)
		cloud.ResetLifecycleSubscribers()
	})

	// The edge's own body limit, so the bound under test is the HANDLER's. Left at
	// the zip default (4 MiB) the framework refuses an oversized delivery first,
	// and the test would prove fiber's cap rather than this door's — while the
	// fleet edge admits 16 MiB (cloud.Config BodyLimit), which is the size a
	// delivery really arrives at with.
	app := zip.New(zip.Config{Logger: luxlog.New("test"), BodyLimit: 16 << 20})
	app.Post(hookPath, cloud.Terminal(cloud.Handle(s, hook)))
	return app, f
}

// pushBody is the forge's push payload for one landed ref.
func pushBody(t *testing.T, owner, repo, ref, before, after, pusher string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"ref": ref, "before": before, "after": after,
		"repository": map[string]any{
			"name":      repo,
			"full_name": owner + "/" + repo,
			"clone_url": "https://git.hanzo.ai/" + owner + "/" + repo + ".git",
			"owner":     map[string]any{"login": owner, "username": owner},
		},
		"pusher": map[string]any{"login": pusher, "username": pusher},
	})
	if err != nil {
		t.Fatalf("marshal push: %v", err)
	}
	return b
}

// sign is the forge's own signature over body: bare hex, no prefix.
func sign(key string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// deliver POSTs body with the given signature headers set (name/value pairs).
func deliver(t *testing.T, app *zip.App, body []byte, headers ...string) (int, verdict) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, hookPath, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var v verdict
	_ = json.Unmarshal(raw, &v)
	return resp.StatusCode, v
}

// signedDelivery POSTs body signed with the forge's own header.
func signedDelivery(t *testing.T, app *zip.App, body []byte) (int, verdict) {
	t.Helper()
	return deliver(t, app, body, "X-Gitea-Signature", sign(hookSecret, body))
}

// ── the signature is the whole authentication ────────────────────────────────

// A delivery signed under the wrong key is refused, and NOTHING runs. The count
// is the assertion: a 401 that had already dispatched would be a build started by
// an unauthenticated caller.
func TestHook_RefusesAWrongSignature(t *testing.T) {
	app, f := hookApp(t, hookSecret)
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")

	code, _ := deliver(t, app, body, "X-Gitea-Signature", sign("not-the-secret", body))
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong-key delivery: want 401, got %d", code)
	}
	if p, e := f.counts(); p != 0 || e != 0 {
		t.Fatalf("a refused delivery dispatched %d push / %d lifecycle", p, e)
	}
}

// The signature covers the BODY. A valid signature over different bytes than the
// ones delivered is refused, so a body cannot be swapped after signing.
func TestHook_RefusesASignatureOverOtherBytes(t *testing.T) {
	app, f := hookApp(t, hookSecret)
	signedOver := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")
	delivered := pushBody(t, hookOwner, "other", "refs/heads/main", hookBefore, hookCommit, "z")

	code, _ := deliver(t, app, delivered, "X-Gitea-Signature", sign(hookSecret, signedOver))
	if code != http.StatusUnauthorized {
		t.Fatalf("swapped-body delivery: want 401, got %d", code)
	}
	if p, e := f.counts(); p != 0 || e != 0 {
		t.Fatalf("a swapped body dispatched %d push / %d lifecycle", p, e)
	}
}

// No signature header at all is refused. An unsigned delivery is the shape an
// arbitrary internet caller sends, since this door takes no session.
func TestHook_RefusesAnUnsignedDelivery(t *testing.T) {
	app, f := hookApp(t, hookSecret)
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")

	code, _ := deliver(t, app, body)
	if code != http.StatusUnauthorized {
		t.Fatalf("unsigned delivery: want 401, got %d", code)
	}
	// Malformed hex, and an empty header value, are refused the same way rather
	// than erroring or being treated as absent-and-therefore-fine.
	for _, sig := range []string{"", "zzzz", "sha256=", "sha256=nothex"} {
		if code, _ := deliver(t, app, body, "X-Gitea-Signature", sig); code != http.StatusUnauthorized {
			t.Fatalf("signature %q: want 401, got %d", sig, code)
		}
	}
	if p, e := f.counts(); p != 0 || e != 0 {
		t.Fatalf("an unsigned delivery dispatched %d push / %d lifecycle", p, e)
	}
}

// The forge emits the GitHub spelling beside its own, and BOTH carry the same
// digest. A receiver that knew one would reject every delivery the day the forge
// renamed its header family — which it has done twice.
func TestHook_AcceptsEverySpellingTheForgeSends(t *testing.T) {
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")
	for _, h := range []struct{ name, value string }{
		{"X-Git-Signature", sign(hookSecret, body)},
		{"X-Gitea-Signature", sign(hookSecret, body)},
		{"X-Hub-Signature-256", "sha256=" + sign(hookSecret, body)},
	} {
		app, f := hookApp(t, hookSecret)
		code, v := deliver(t, app, body, h.name, h.value)
		if code != http.StatusOK || !v.Fired {
			t.Fatalf("%s: want 200 fired, got %d %+v", h.name, code, v)
		}
		f.settle(t, 1, 1)
	}
}

// A deployment that cannot read its own secret refuses every delivery and
// processes nothing — 503, because the fault is ours and not the caller's, and
// the forge redelivers once KMS answers.
func TestHook_FailsClosedWithNoSecret(t *testing.T) {
	app, f := hookApp(t, "") // nothing sealed at forge.WebhookRef
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")

	// Even a delivery signed under the empty string is refused: an unset secret is
	// never a secret whose value happens to be "".
	code, _ := deliver(t, app, body, "X-Gitea-Signature", sign("", body))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no-secret delivery: want 503, got %d", code)
	}
	if p, e := f.counts(); p != 0 || e != 0 {
		t.Fatalf("a delivery we could not verify dispatched %d push / %d lifecycle", p, e)
	}
}

// An ENCODED body is refused before it is read, which is the only place the
// refusal can be: reading it is what decompresses it, and by then the allocation
// the cap would refuse has already been paid. The measurement is the assertion —
// a 64 MiB bomb that costs less than the cap itself never expanded.
func TestHook_RefusesAnEncodedBodyBeforeReadingIt(t *testing.T) {
	const plain = 64 << 20
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := io.Copy(zw, io.LimitReader(zeroes{}, plain)); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	wire := buf.Bytes()

	for _, enc := range []string{"gzip", "deflate", "br", "zstd", "gzip, gzip"} {
		app, f := hookApp(t, hookSecret)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)

		req := httptest.NewRequest(http.MethodPost, hookPath, bytes.NewReader(wire))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Content-Encoding", enc)
		req.Header.Set("X-Git-Signature", strings.Repeat("ab", 32))
		resp, err := app.Test(req, zip.TestConfig{Timeout: 60 * time.Second})
		if err != nil {
			t.Fatalf("%s: deliver: %v", enc, err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		runtime.ReadMemStats(&after)

		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("Content-Encoding %q: want 415, got %d", enc, resp.StatusCode)
		}
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc >= maxHookBody {
			t.Fatalf("Content-Encoding %q: %d bytes on the wire allocated %d — the body was expanded before it was refused",
				enc, len(wire), alloc)
		}
		if p, e := f.counts(); p != 0 || e != 0 {
			t.Fatalf("%s dispatched %d push / %d lifecycle", enc, p, e)
		}
	}
}

// zeroes is an endless run of the most compressible bytes there are.
type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// A body over the bound is refused before it is hashed or parsed.
func TestHook_RefusesAnOversizedBody(t *testing.T) {
	app, f := hookApp(t, hookSecret)
	body := []byte(`{"ref":"refs/heads/main","pad":"` + strings.Repeat("x", maxHookBody) + `"}`)

	code, _ := deliver(t, app, body, "X-Gitea-Signature", sign(hookSecret, body))
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized delivery: want 413, got %d", code)
	}
	if p, e := f.counts(); p != 0 || e != 0 {
		t.Fatalf("an oversized delivery dispatched %d push / %d lifecycle", p, e)
	}
}

// ── the payload, and both clients ──────────────────────────────────────────────

// THE PROPERTY THIS DOOR EXISTS FOR: a verified push reaches BOTH clients, exactly
// once each, carrying what each one reads.
func TestHook_FiresBothClientsOnce(t *testing.T) {
	app, f := hookApp(t, hookSecret)
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")

	code, v := signedDelivery(t, app, body)
	if code != http.StatusOK || !v.Fired {
		t.Fatalf("verified push: want 200 fired, got %d %+v", code, v)
	}
	if v.Org != hookOrg || v.Repo != "cloud" || v.Commit != hookCommit {
		t.Fatalf("verdict does not name what landed: %+v", v)
	}
	f.settle(t, 1, 1)

	// The deploy trigger reads the FULL ref (tags reach it) and a clone URL it can
	// match an application's RepoURL against.
	p := f.pushes[0]
	if p.Org != hookOrg || p.Repo != "cloud" || p.Ref != "refs/heads/main" || p.Commit != hookCommit {
		t.Fatalf("push-to-deploy got %+v", p)
	}
	if p.CloneURL != "https://git.hanzo.ai/hanzoai/cloud.git" {
		t.Fatalf("push-to-deploy clone URL = %q", p.CloneURL)
	}

	// The lifecycle subscribers read a branch NAME, the old and new tips, who
	// pushed, and the host the refs arrived from.
	e := f.events[0]
	if e.Kind != cloud.LifecyclePushLanded || e.Org != hookOrg || e.Repo != "cloud" {
		t.Fatalf("lifecycle got %+v", e)
	}
	if e.Branch != "main" || e.Before != hookBefore || e.After != hookCommit || e.Pusher != "z" {
		t.Fatalf("lifecycle fields: %+v", e)
	}
	if e.Origin != "git.hanzo.ai" {
		t.Fatalf("Origin = %q, want the forge host — without it the outbound mirror re-pushes its own echo", e.Origin)
	}
}

// A tag reaches the BUILD trigger, because releases are cut by tag and filtering
// here would stop publishing with nothing failing to say so. It leaves the
// lifecycle branch empty, which is what the mirror and the indexer skip on.
func TestHook_ATagBuildsAndNamesNoBranch(t *testing.T) {
	app, f := hookApp(t, hookSecret)
	body := pushBody(t, hookOwner, "cloud", "refs/tags/v1.2.3", hookBefore, hookCommit, "z")

	if code, v := signedDelivery(t, app, body); code != http.StatusOK || !v.Fired {
		t.Fatalf("tag push: want 200 fired, got %d %+v", code, v)
	}
	f.settle(t, 1, 1)
	if got := f.pushes[0].Ref; got != "refs/tags/v1.2.3" {
		t.Fatalf("deploy trigger ref = %q, want the full tag ref", got)
	}
	if got := f.events[0].Branch; got != "" {
		t.Fatalf("lifecycle branch = %q for a tag; the mirror would push a ref that is not one", got)
	}
}

// THE CLONE URL IS DERIVED, NEVER READ. It is what buildFromPush matches an
// application's RepoURL against and what isReleasePush compares to cloud's own
// upstream, so a delivery that could name it could aim a build at a repository
// this forge does not serve. A payload naming one is ignored, whatever it says.
func TestHook_DerivesTheCloneURLAndIgnoresThePayloadsOwn(t *testing.T) {
	const want = "https://git.hanzo.ai/hanzoai/cloud.git"
	for _, claimed := range []string{
		"",                                 // the shape a forge with no root URL sends
		"https://github.com/hanzoai/cloud", // cloud's own upstream: the release trigger
		"https://evil.example/hanzoai/cloud.git",
	} {
		app, f := hookApp(t, hookSecret)
		body, err := json.Marshal(map[string]any{
			"ref": "refs/heads/main", "before": hookBefore, "after": hookCommit,
			"repository": map[string]any{
				"name": "cloud", "clone_url": claimed,
				"owner": map[string]any{"login": hookOwner},
			},
			"pusher": map[string]any{"login": "z"},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if code, v := signedDelivery(t, app, body); code != http.StatusOK || !v.Fired {
			t.Fatalf("clone_url %q: want 200 fired, got %d %+v", claimed, code, v)
		}
		f.settle(t, 1, 1)
		if got := f.pushes[0].CloneURL; got != want {
			t.Fatalf("clone_url %q reached the build path as %q, want the derived %q", claimed, got, want)
		}
	}
}

// A deployment that cannot name its own forge refuses. The value it would carry on
// is the empty origin, which every mirror reads as "a native push, send it on" —
// the one loop this door must not start.
func TestHook_RefusesWhenTheDeploymentNamesNoForge(t *testing.T) {
	app, f := hookAppOn(t, hookSecret, "")
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")

	if code, _ := signedDelivery(t, app, body); code != http.StatusServiceUnavailable {
		t.Fatalf("no-forge deployment: want 503, got %d", code)
	}
	if p, e := f.counts(); p != 0 || e != 0 {
		t.Fatalf("a deployment with no forge dispatched %d push / %d lifecycle", p, e)
	}
}

// ── whose push it is ─────────────────────────────────────────────────────────

// The org comes from the CLOSED forge-namespace table. A namespace nobody has
// mapped is declined and dispatches nothing — a forge account is enough to create
// one, so a fallback to the name would let a signup choose which tenant rebuilds
// and whose compute pays.
func TestHook_RefusesAnUnmappedNamespace(t *testing.T) {
	for _, owner := range []string{"acme", "hanzo", "luxfi", "admin"} {
		app, f := hookApp(t, hookSecret)
		body := pushBody(t, owner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")

		code, v := signedDelivery(t, app, body)
		if code != http.StatusOK {
			t.Fatalf("%s: want a benign 200 the forge does not retry, got %d", owner, code)
		}
		if v.Fired || v.Reason == "" {
			t.Fatalf("%s: a push from an unmapped namespace fired: %+v", owner, v)
		}
		if p, e := f.counts(); p != 0 || e != 0 {
			t.Fatalf("%s dispatched %d push / %d lifecycle", owner, p, e)
		}
	}
}

// ── the deliveries deliberately declined ─────────────────────────────────────

// Each of these is a VERIFIED delivery this door correctly does nothing with. All
// answer a benign 200 naming the reason, so the forge does not retry-storm and an
// operator reads why on the forge's own delivery page.
func TestHook_DeclinesWithAReason(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		body       func(*testing.T) []byte
	}{
		{"ref delete", "ref deleted", func(t *testing.T) []byte {
			return pushBody(t, hookOwner, "cloud", "refs/heads/gone", hookBefore, zeroSHA, "z")
		}},
		{"bot push", "bot push", func(t *testing.T) []byte {
			return pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "hanzo-actions")
		}},
		{"not a ref", "not a push", func(t *testing.T) []byte {
			return pushBody(t, hookOwner, "cloud", "main", hookBefore, hookCommit, "z")
		}},
		{"no repository", "not a push", func(t *testing.T) []byte {
			return []byte(`{"ref":"refs/heads/main","after":"` + hookCommit + `"}`)
		}},
		{"another event", "not a push", func(t *testing.T) []byte {
			return []byte(`{"action":"opened","issue":{"number":7}}`)
		}},
		// The coordinate leaves this door as a clone URL, a directory key and a git
		// argument. A separator, a leading dash or a dot-dot in any part of it is
		// refused at the boundary rather than in each place it would arrive.
		{"traversal in the name", "malformed coordinate", func(t *testing.T) []byte {
			return pushBody(t, hookOwner, "../../etc", "refs/heads/main", hookBefore, hookCommit, "z")
		}},
		{"separator in the namespace", "malformed coordinate", func(t *testing.T) []byte {
			return pushBody(t, hookOwner+"/x", "cloud", "refs/heads/main", hookBefore, hookCommit, "z")
		}},
		{"flag-shaped branch", "malformed coordinate", func(t *testing.T) []byte {
			return pushBody(t, hookOwner, "cloud", "refs/heads/--upload-pack=sh", hookBefore, hookCommit, "z")
		}},
		{"dot-dot in the ref", "malformed coordinate", func(t *testing.T) []byte {
			return pushBody(t, hookOwner, "cloud", "refs/heads/a../b", hookBefore, hookCommit, "z")
		}},
		{"a ref that is neither", "malformed coordinate", func(t *testing.T) []byte {
			return pushBody(t, hookOwner, "cloud", "refs/pull/7/head", hookBefore, hookCommit, "z")
		}},
		{"a commit that is not one", "malformed coordinate", func(t *testing.T) []byte {
			return pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, "HEAD;curl evil", "z")
		}},
		// Git names a ref's new tip in FULL. A prefix is a name that resolves to
		// different objects in different clones of one repository, and it is not
		// something the forge ever sends.
		{"a commit prefix", "malformed coordinate", func(t *testing.T) []byte {
			return pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit[:7], "z")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, f := hookApp(t, hookSecret)
			code, v := signedDelivery(t, app, tc.body(t))
			if code != http.StatusOK {
				t.Fatalf("want a benign 200, got %d", code)
			}
			if v.Fired || v.Reason != tc.want {
				t.Fatalf("verdict = %+v, want reason %q", v, tc.want)
			}
			if p, e := f.counts(); p != 0 || e != 0 {
				t.Fatalf("dispatched %d push / %d lifecycle", p, e)
			}
		})
	}
}

// A malformed body that passed the signature is a 400. It cannot reach here from
// the outside — the bytes are ours by then — so it reports a broken forge rather
// than being smoothed into an ignore.
func TestHook_RefusesAMalformedPayload(t *testing.T) {
	app, f := hookApp(t, hookSecret)
	body := []byte(`{"ref":`)
	if code, _ := signedDelivery(t, app, body); code != http.StatusBadRequest {
		t.Fatalf("malformed payload: want 400, got %d", code)
	}
	if p, e := f.counts(); p != 0 || e != 0 {
		t.Fatalf("a malformed payload dispatched %d push / %d lifecycle", p, e)
	}
}

// ── one push, one build ──────────────────────────────────────────────────────

// The forge redelivers a request it could not complete, and a retry landing after
// the clients already ran would build the same commit twice — on the tenant's
// compute. The FACT is the key, so the second delivery is declined and the counts
// stay at one.
func TestHook_ARedeliveryFiresOnce(t *testing.T) {
	app, f := hookApp(t, hookSecret)
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")

	if code, v := signedDelivery(t, app, body); code != http.StatusOK || !v.Fired {
		t.Fatalf("first delivery: want 200 fired, got %d %+v", code, v)
	}
	f.settle(t, 1, 1)

	code, v := signedDelivery(t, app, body)
	if code != http.StatusOK {
		t.Fatalf("redelivery: want a benign 200, got %d", code)
	}
	if v.Fired || v.Reason != "already landed" {
		t.Fatalf("redelivery verdict = %+v", v)
	}
	f.settle(t, 1, 1)

	// The NEXT commit on the same branch is a different fact and builds.
	next := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookCommit,
		"feedfacedeadbeef0123456789abcdef01234567", "z")
	if code, v := signedDelivery(t, app, next); code != http.StatusOK || !v.Fired {
		t.Fatalf("next commit: want 200 fired, got %d %+v", code, v)
	}
	f.settle(t, 2, 2)
}

// ONE LANDED COMMIT IS ONE FACT, whatever case the namespace arrives in. Tenancy
// is decided on the lowercased namespace (forge.Org), so a key on the raw one is
// a second key for the same push — and the second delivery builds it again, on
// the tenant's compute. The forge will not vary the case; the dedup is the only
// thing standing between a redelivery and a second build, and it must not depend
// on that.
func TestHook_ARedeliveryUnderAnotherCaseFiresOnce(t *testing.T) {
	app, f := hookApp(t, hookSecret)
	for _, owner := range []string{"hanzoai", "HanzoAI", "HANZOAI", "hanzoAI"} {
		body := pushBody(t, owner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")
		code, v := signedDelivery(t, app, body)
		if code != http.StatusOK {
			t.Fatalf("owner %q: want 200, got %d", owner, code)
		}
		if owner == "hanzoai" && !v.Fired {
			t.Fatalf("the first delivery did not fire: %+v", v)
		}
		if owner != "hanzoai" && (v.Fired || v.Reason != "already landed") {
			t.Fatalf("owner %q is the same landed commit and got %+v", owner, v)
		}
	}
	f.settle(t, 1, 1)
}

// A PUSH THAT COULD NOT BE DISPATCHED IS ANSWERED AS ONE, and leaves nothing
// behind. This fork has no auto-retry — a delivery is marked delivered before
// the attempt, and the only recovery is a person clicking Replay — so answering
// a failed dispatch 200 fired:true loses the push twice over: the delivery page
// says it worked, and the Replay that would have worked is declined as a
// duplicate of the attempt that did not.
func TestHook_ADispatchFailureIsRefusedAndLeavesNoDedup(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	app, f := hookAppWith(t, sealed(t, hookSecret), "api.hanzo.ai",
		func(context.Context, cloud.GitPushEvent) (int, error) {
			if fail.Load() {
				return 0, fmt.Errorf("store read failed: platform draining")
			}
			return 2, nil
		})
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")

	code, v := signedDelivery(t, app, body)
	if code != http.StatusInternalServerError {
		t.Fatalf("a dispatch that failed answered %d, want 500 so the delivery page shows it", code)
	}
	if v.Fired {
		t.Fatalf("a dispatch that failed claimed to have fired: %+v", v)
	}
	// The lifecycle client does not run either: the delivery is unprocessed as a
	// whole, and the Replay redoes both halves.
	if p, e := f.counts(); p != 1 || e != 0 {
		t.Fatalf("dispatched %d push / %d lifecycle; want the one attempt and no lifecycle", p, e)
	}

	// The operator fixes the fault and replays the delivery, which is the ONE
	// recovery this fork has. It must reach a fresh attempt.
	fail.Store(false)
	code, v = signedDelivery(t, app, body)
	if code != http.StatusOK || !v.Fired {
		t.Fatalf("the replay of a lost push got %d %+v — it was refused as a duplicate of an attempt that built nothing", code, v)
	}
	if v.Builds != 2 {
		t.Fatalf("verdict Builds = %d, want the 2 the builder launched", v.Builds)
	}
	f.settle(t, 2, 1)
}

// FIRED IS NOT BUILT. Most pushes track no application, so a fired delivery that
// built nothing is ordinary — and it is exactly what "fired" cannot say. The
// builder has always known the number; the forge leg used to drop it, leaving
// one green for a push that built eleven services and one that built none.
func TestHook_TheVerdictCarriesWhatWasBuilt(t *testing.T) {
	for _, want := range []int{0, 1, 7} {
		app, f := hookAppWith(t, sealed(t, hookSecret), "api.hanzo.ai",
			func(context.Context, cloud.GitPushEvent) (int, error) { return want, nil })
		body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")
		code, v := signedDelivery(t, app, body)
		if code != http.StatusOK || !v.Fired {
			t.Fatalf("want 200 fired, got %d %+v", code, v)
		}
		if v.Builds != want {
			t.Fatalf("verdict Builds = %d, want %d", v.Builds, want)
		}
		f.settle(t, 1, 1)
	}
	// And the count is in the wire shape, not only in the struct: it is what the
	// forge's delivery page shows.
	b, err := json.Marshal(verdict{Org: "hanzo", Repo: "cloud", Fired: true, Builds: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"builds":3`) {
		t.Fatalf("the answer the forge shows is %s", b)
	}
}

// ── the parts, directly ──────────────────────────────────────────────────────

// signed knows every spelling and is fail-closed on each way a signature can be
// absent or malformed. Asserted here as well as over HTTP because it is the one
// function standing between the internet and a build.
func TestSigned(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	good := sign(hookSecret, body)

	if !signed(hookSecret, body, good) {
		t.Fatal("the forge's own bare-hex signature was refused")
	}
	if !signed(hookSecret, body, "sha256="+good) {
		t.Fatal("the GitHub spelling was refused")
	}
	if !signed(hookSecret, body, "", "zzz", good) {
		t.Fatal("a good signature beside two bad ones was refused")
	}
	if !signed(hookSecret, body, "  "+good+"  ") {
		t.Fatal("a signature with surrounding whitespace was refused")
	}
	// Fail-closed: no secret, no signature, the wrong key, and a signature over
	// other bytes.
	if signed("", body, good) {
		t.Fatal("an unset secret admitted a delivery")
	}
	if signed(hookSecret, body) {
		t.Fatal("a delivery with no signature header at all was admitted")
	}
	if signed(hookSecret, body, sign("other", body)) {
		t.Fatal("the wrong key was admitted")
	}
	if signed(hookSecret, []byte(`{"ref":"refs/heads/other"}`), good) {
		t.Fatal("a signature over other bytes was admitted")
	}
	// A truncated-but-prefix-matching digest must not pass: hmac.Equal compares
	// length too, and a prefix compare would let a search find the rest one byte
	// at a time.
	if signed(hookSecret, body, good[:len(good)-2]) {
		t.Fatal("a truncated digest was admitted")
	}
}

// seen is the redelivery memory: held inside the window, and free again once it
// has expired, so a branch pushed to the same commit weeks later still builds.
func TestSeen(t *testing.T) {
	var k seen
	now := time.Now()
	if !k.hold("a", now) {
		t.Fatal("a key never seen was not held")
	}
	if k.hold("a", now.Add(hookWindow-time.Second)) {
		t.Fatal("a key inside the window was held twice")
	}
	if !k.hold("b", now) {
		t.Fatal("a different key was not held")
	}
	if !k.hold("a", now.Add(hookWindow+time.Second)) {
		t.Fatal("a key past the window was not held again")
	}
	// Swept on write: the expired entries are gone rather than held for the
	// process's lifetime.
	if _, held := k.at["b"]; held {
		t.Fatalf("expired keys are still held: %v", k.at)
	}
	// A hold given back is free at once: nothing fired, so there is nothing to
	// remember, and the next delivery naming that push is a fresh attempt.
	k.drop("a")
	if !k.hold("a", now.Add(hookWindow+time.Second)) {
		t.Fatal("a dropped key was still held")
	}
}

// The secret is held for a window, so an unauthenticated flood costs one KMS read
// rather than one per delivery — and a rotation is live inside that window with no
// restart.
func TestSecretIsHeldAndRefused(t *testing.T) {
	kms := newFakeKMS()
	if err := kms.PutSecret(context.Background(), forge.WebhookRef, []byte("  key-one  ")); err != nil {
		t.Fatalf("seal: %v", err)
	}
	s := &cloud.Service[state]{Base: cloud.Base{KMS: kms, Log: luxlog.New("test")}}

	got, err := s.State.hook.read(s, context.Background())
	if err != nil || got != "key-one" {
		t.Fatalf("read = %q, %v; want the trimmed value", got, err)
	}
	// A rotation the window has not expired is not yet visible — which is the
	// bargain the window buys, stated rather than assumed.
	if err := kms.PutSecret(context.Background(), forge.WebhookRef, []byte("key-two")); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if got, _ := s.State.hook.read(s, context.Background()); got != "key-one" {
		t.Fatalf("held value = %q, want the one inside the window", got)
	}
	// Past the window, the rotation is live with no restart.
	s.State.hook.when = time.Now().Add(-hookFresh - time.Second)
	if got, err := s.State.hook.read(s, context.Background()); err != nil || got != "key-two" {
		t.Fatalf("after the window: %q, %v; want the rotated value", got, err)
	}

	// Fail-closed: no KMS at all, and an empty secret, are errors and never a value.
	none := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}
	if got, err := none.State.hook.read(none, context.Background()); err == nil {
		t.Fatalf("an unmounted KMS produced a key: %q", got)
	}
	blank := newFakeKMS()
	if err := blank.PutSecret(context.Background(), forge.WebhookRef, []byte("   ")); err != nil {
		t.Fatalf("seal blank: %v", err)
	}
	empty := &cloud.Service[state]{Base: cloud.Base{KMS: blank, Log: luxlog.New("test")}}
	if got, err := empty.State.hook.read(empty, context.Background()); err == nil {
		t.Fatalf("an empty secret produced a key: %q", got)
	}
}

// A FAILED REFRESH DOES NOT TAKE THE KEY WITH IT. The read failed; the secret
// did not change — it is still the value the forge is signing with. Discarding
// it turned one KMS blip into a window of deliveries this door could not verify,
// and this fork does not redeliver them.
func TestSecretSurvivesAFailedRefresh(t *testing.T) {
	kms := newFakeKMS()
	if err := kms.PutSecret(context.Background(), forge.WebhookRef, []byte(hookSecret)); err != nil {
		t.Fatalf("seal: %v", err)
	}
	s := &cloud.Service[state]{Base: cloud.Base{KMS: kms, Log: luxlog.New("test")}}
	if v, err := s.State.hook.read(s, context.Background()); err != nil || v != hookSecret {
		t.Fatalf("first read: %q %v", v, err)
	}

	// KMS goes down exactly at the window boundary.
	kms.down = fmt.Errorf("dial kms: connection refused")
	s.State.hook.when = time.Now().Add(-hookFresh - time.Second)
	if v, err := s.State.hook.read(s, context.Background()); err != nil || v != hookSecret {
		t.Fatalf("a failed refresh answered %q, %v — the key that works was discarded", v, err)
	}
	// And every caller behind it inside the window gets the same answer, not the
	// error: one rule, wherever you arrived.
	if v, err := s.State.hook.read(s, context.Background()); err != nil || v != hookSecret {
		t.Fatalf("a caller inside the window got %q, %v", v, err)
	}
	// The failure is still RECORDED — kept, not swallowed.
	s.State.hook.mu.Lock()
	held := s.State.hook.err
	s.State.hook.mu.Unlock()
	if held == nil {
		t.Fatal("the failed refresh left no error behind; the degradation is invisible")
	}
	// KMS recovers, the window turns, and the rotation is live.
	kms.down = nil
	if err := kms.PutSecret(context.Background(), forge.WebhookRef, []byte("key-two")); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	s.State.hook.when = time.Now().Add(-hookFresh - time.Second)
	if v, err := s.State.hook.read(s, context.Background()); err != nil || v != "key-two" {
		t.Fatalf("after recovery: %q, %v; want the rotated value", v, err)
	}
}

// A PANIC INSIDE THE READ RELEASES THE REFRESH. One panic used to strand the
// in-flight flag: every later delivery answered 503 for the life of the process,
// with KMS healthy, and no second read was ever attempted.
func TestSecretRefreshSurvivesAPanickingKMS(t *testing.T) {
	kms := newFakeKMS()
	kms.panics = true
	s := &cloud.Service[state]{Base: cloud.Base{KMS: kms, Log: luxlog.New("test")}}

	func() {
		defer func() { _ = recover() }() // the edge recovers; the door must settle
		_, _ = s.State.hook.read(s, context.Background())
	}()

	s.State.hook.mu.Lock()
	busy := s.State.hook.busy
	s.State.hook.mu.Unlock()
	if busy {
		t.Fatal("the refresh is still marked in flight; every later delivery is 503 forever")
	}
	// The recorded outcome is a FAILURE, never the empty value settled as a
	// success — which would 401 every delivery and blame the forge's config.
	if v, err := s.State.hook.read(s, context.Background()); err == nil || v != "" {
		t.Fatalf("a panicked read settled as %q, %v", v, err)
	}
	// KMS is healthy again, the window turns, and the door recovers by itself.
	kms.panics = false
	if err := kms.PutSecret(context.Background(), forge.WebhookRef, []byte(hookSecret)); err != nil {
		t.Fatalf("seal: %v", err)
	}
	s.State.hook.when = time.Now().Add(-hookFresh - time.Second)
	if v, err := s.State.hook.read(s, context.Background()); err != nil || v != hookSecret {
		t.Fatalf("after the panic cleared: %q, %v", v, err)
	}
}

// The KMS read is bounded UNDER the forge's own 5s delivery timeout. The forge
// hangs up at 5s and this fork does not retry, so a read that outlives the
// delivery has already lost the push and is only choosing whether to hold the
// refresh open behind it as well.
func TestHookReadFailsInsideTheDeliveryWindow(t *testing.T) {
	const forgeDelivers = 5 * time.Second // services/webhook: DeliverTimeout
	if hookRead >= forgeDelivers {
		t.Fatalf("hookRead = %v, which is not inside the %v the forge waits: a slow read answers nobody", hookRead, forgeDelivers)
	}
}

// The declaration and the route are one operation. openapi.Register is keyed by
// the router's own pattern, so a route moved without its declaration publishes an
// operation with no body and an SDK with nowhere to put a delivery.
func TestHookIsDeclaredAtTheAddressItServes(t *testing.T) {
	app, _ := hookApp(t, hookSecret)
	doc, err := openapi.Spec(app, openapi.Info{Title: "platform", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	op := doc.Paths[hookPath]["post"]
	if op == nil {
		t.Fatalf("%s is served and undeclared; paths: %v", hookPath, doc.Paths)
	}
	if strings.TrimSpace(op.Summary) == "" || strings.TrimSpace(op.Description) == "" {
		t.Fatalf("%s publishes an operationId and nothing a consumer can read", hookPath)
	}
	if op.RequestBody == nil {
		t.Fatalf("%s declares no request body; every generated SDK offers a webhook with nowhere to put the delivery", hookPath)
	}
}
