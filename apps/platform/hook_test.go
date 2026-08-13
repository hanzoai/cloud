package platform

// hook_test.go drives the forge's push door over REAL HTTP against the real
// route, because the two properties that matter are both properties of the wire:
// what a delivery has to carry to be believed, and what running it costs the
// fleet. Both seams are observed through the same registrations production uses
// — the push builder and the lifecycle subscriber list — so a test cannot pass by
// asserting a call it made itself.
//
// The door this replaces was tested by registering a builder in-process and
// checking the call that registration had just made possible, which is why its
// suite stayed green for as long as the door was dead. Every assertion here is
// about the SEAM, and the fired/not-fired counts are the whole point.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// fired records what the two seams received. Both are appended under one lock so
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
			t.Fatalf("seams fired %d push / %d lifecycle, want %d / %d", p, e, pushes, events)
		}
		time.Sleep(time.Millisecond)
	}
}

// hookApp mounts the forge door the way Mount does — the raw route over a Service
// whose KMS holds `key` — and registers both seams so the test observes exactly
// what production dispatches. An empty key seals nothing, which is how the
// no-secret refusal is exercised.
//
// The Service carries NO STORE, and that is an assertion rather than a shortcut:
// this door verifies, resolves and dispatches, and reads nothing of platform's
// own state. A version of it that grew a read would stop building here.
func hookApp(t *testing.T, key string) (*zip.App, *fired) {
	t.Helper()
	kms := newFakeKMS()
	if key != "" {
		if err := kms.PutSecret(context.Background(), forge.WebhookRef, []byte(key)); err != nil {
			t.Fatalf("seal webhook secret: %v", err)
		}
	}
	s := &cloud.Service[state]{
		Base: cloud.Base{KMS: kms, Log: luxlog.New("test"), Brand: "hanzo", Domain: "api.hanzo.ai"},
	}

	f := &fired{}
	cloud.RegisterPushBuilder(func(_ context.Context, ev cloud.GitPushEvent) error { f.push(ev); return nil })
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
		{"X-Forgejo-Signature", sign(hookSecret, body)},
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

// ── the payload, and both seams ──────────────────────────────────────────────

// THE PROPERTY THIS DOOR EXISTS FOR: a verified push reaches BOTH seams, exactly
// once each, carrying what each one reads.
func TestHook_FiresBothSeamsOnce(t *testing.T) {
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

// A payload with no clone URL still resolves to this deployment's own forge,
// rather than to a repository nothing can match.
func TestHook_DerivesTheCloneURLWhenThePayloadOmitsIt(t *testing.T) {
	app, f := hookApp(t, hookSecret)
	body, err := json.Marshal(map[string]any{
		"ref": "refs/heads/main", "before": hookBefore, "after": hookCommit,
		"repository": map[string]any{"name": "cloud", "owner": map[string]any{"login": hookOwner}},
		"pusher":     map[string]any{"login": "z"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if code, v := signedDelivery(t, app, body); code != http.StatusOK || !v.Fired {
		t.Fatalf("want 200 fired, got %d %+v", code, v)
	}
	f.settle(t, 1, 1)
	if got := f.pushes[0].CloneURL; got != "https://git.hanzo.ai/hanzoai/cloud.git" {
		t.Fatalf("derived clone URL = %q", got)
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
// the seams already ran would build the same commit twice — on the tenant's
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

// seen is the redelivery memory: first inside the window, and first again once it
// has expired, so a branch pushed to the same commit weeks later still builds.
func TestSeen(t *testing.T) {
	var k seen
	now := time.Now()
	if !k.first("a", now) {
		t.Fatal("a key never seen was not first")
	}
	if k.first("a", now.Add(hookWindow-time.Second)) {
		t.Fatal("a key inside the window was first again")
	}
	if !k.first("b", now) {
		t.Fatal("a different key was not first")
	}
	if !k.first("a", now.Add(hookWindow+time.Second)) {
		t.Fatal("a key past the window was not first again")
	}
	// Swept on write: the expired entries are gone rather than held for the
	// process's lifetime.
	if _, held := k.at["b"]; held {
		t.Fatalf("expired keys are still held: %v", k.at)
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
