// hook.go is the forge's push door: git.hanzo.ai POSTs every push here, this
// verifies the signature, and the landed ref becomes a build (buildFromPush) and
// a lifecycle fact (notify, code-index, mirror).
//
// IT IS IN PLATFORM BECAUSE THE BUILD IS. cloud runs each app as its own OS
// process, and the deploy trigger has exactly one registrant — platform's, next
// to this file. The door that used to take these deliveries lived in apps/git, a
// different process, where that registrant is nil forever: every delivery was
// signed, accepted, answered 204, and built nothing. A receiver has to sit in the
// process that can act, which is why moving the address was the fix and not a
// rename. apps/git's route is a 410 naming platform.hanzo.ai — a different
// deployment, not this one (see [hookPath]).
//
// The forge and native pushes now travel the SAME two seams. apps/git's
// fireBranchBuild fires OnGitPush + EmitLifecycle for a push its own receive-pack
// took; this fires the same pair for a push the forge took. One trigger, two
// transports — the build decision stays downstream in buildFromPush, which is the
// one place that knows what a push MEANS.
//
// PUBLIC at the JWT layer, because the forge holds no Hanzo session:
// AUTHENTICATION IS THE SIGNATURE. It covers the raw bytes and is checked before
// the payload is parsed, so an unauthenticated body is never decoded — which is
// also why this cannot be a typed op, since a typed op decodes first.
//
// # What the forge has to be configured with
//
// A receiver nobody delivers to is the same silence as a receiver that builds
// nothing, so the forge half is stated here, beside the half it has to match. It
// is a forge-admin act (Site Administration → Webhooks — the SYSTEM webhook,
// which covers every repository and every namespace at once, so a repo opts in by
// having an application that tracks it rather than by owning a hook):
//
//	Target URL    https://api.hanzo.ai/v1/platform/hook
//	HTTP method   POST
//	Content type  application/json
//	Secret        the value at KMS forge.WebhookRef
//	Trigger       Push events only
//	Branch filter *
//
// api.hanzo.ai is the fleet's one endpoint and the only host that reaches this
// receiver. platform.hanzo.ai is a separate deployment; the address apps/git's
// 410 names resolves there, not here.
//
// The secret is CONFIGURED, not generated here: the forge and this receiver share
// one value, and the KMS ref is where the deployment keeps it. Rotating means
// writing the new value at that ref and pasting the same value on the forge's
// hook; deliveries signed with the old one are refused within one [hookFresh]
// window — of a KMS that ANSWERS. A refresh that fails keeps serving the last
// value that read cleanly ([secret.read]), so a rotation done to burn a leaked
// secret is only as fast as the read that carries it: if KMS is unreachable,
// restart the pods rather than waiting for a window that cannot turn.

package platform

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// hookPath is where the forge delivers, and it is under this app because the
// build trigger is: one capability, one prefix (HIP-0139 §3.1). The leaf is
// `hook` — the noun for what arrives here — and not `git-webhook`, which is a
// compound §2.3 refuses and a second spelling of the transport the first
// segment already gives.
//
// NOTHING OUTSIDE THIS REPO FOLLOWS THIS ADDRESS. api.hanzo.ai is the only host
// that reaches this receiver, and the forge's system webhook targets
// platform.hanzo.ai — a separate deployment, the one apps/git's 410 names.
// Moving this leaf changes what a forge would have to be pointed at to reach
// THIS receiver, and nothing that is pointed anywhere today.
const hookPath = "/v1/platform/hook"

const (
	// maxHookBody bounds what is HASHED and acted on: the bytes verified are
	// exactly the bytes acted on, and a body past this is refused before the MAC
	// runs. What it does not bound is the allocation — by the time it can be
	// checked the request is already in memory — so the two bounds that do are the
	// edge's own BodyLimit and the refusal of an encoded body below, which is the
	// one that keeps a few bytes on the wire from buying unbounded work.
	maxHookBody = 8 << 20 // 8 MiB
	// hookFresh bounds how long the verifying secret is held. A rotation is live
	// within this window with no restart, and an unauthenticated flood costs one
	// KMS read per window rather than one per request.
	hookFresh = 5 * time.Minute
	// hookWindow is how long a fired push is remembered for. It covers a replay of
	// a delivery whose seams already ran and whose answer never arrived — which is
	// the only way one push arrives twice, since a delivery that FAILED is not
	// remembered at all (seen.drop).
	hookWindow = 30 * time.Minute
	// hookRead bounds one KMS read of the verifying secret. The read runs on a
	// context detached from the request (fetch), so this is the only thing that
	// stops a hung read from pinning the refresh open and answering errUnread for
	// every delivery behind it.
	//
	// UNDER the forge's own 5s delivery timeout, deliberately. The forge hangs up
	// at 5s and this fork does not retry — the delivery is marked delivered before
	// the attempt, and the only redelivery is a person clicking Replay — so a read
	// that outlives the delivery has already lost the push and is only choosing
	// whether to also hold the refresh open behind it. Failing inside the window
	// the forge still cares about is what lets the answer reach the delivery page.
	hookRead = 4 * time.Second
	// zeroSHA is git's all-zero object id: the `after` of a deleted ref.
	zeroSHA = "0000000000000000000000000000000000000000"
)

// Every header this forge signs with, in the order it owns them. All carry the
// SAME hex HMAC-SHA256 of the body; the GitHub one prefixes it with "sha256=".
//
// X-Git-Signature FIRST, because it is the one the forge itself minted: Hanzo Git
// is a Gitea fork that rebranded the family, and its own delivery code emits this
// spelling under its own test. X-Gitea-Signature is the inheritance it still
// sends, and X-Hub-Signature-256 the GitHub-compatible alias it sends for
// receivers written against that server.
//
// The list is the forge's ACTUAL wire and not a guess at it. An X-Forgejo-
// spelling stood here and was removed: upstream Forgejo is not what this
// deployment runs, and a header nobody sends is a line that reads like coverage
// while providing none. If the fork completes its stated rename of the X-Gitea-
// family to X-Webhook-, that spelling is added HERE, with a delivery to show for
// it — the cost of a wrong guess in this list is that every push 401s and the
// answer blames the secret.
var sigHeaders = []string{"X-Git-Signature", "X-Gitea-Signature", "X-Hub-Signature-256"}

// push is the subset of the forge's push payload this door acts on. Owner and
// pusher each carry both spellings the payload has used across forge versions
// (login vs username); first non-empty wins.
//
// The payload's own clone_url is deliberately NOT among these. It would be a
// third-party string reaching the build path — the value buildFromPush matches an
// application's RepoURL against — and it carries nothing this door does not
// already know: the host is
// ours, and the path is the owner and name it has read and vetted anyway. So the
// clone URL is DERIVED, and a delivery cannot aim a build at a repository the
// forge does not serve.
//
// The decoded strings are safe to hand to the detached reactors without cloning,
// unlike the path parameters apps/git clones: encoding/json allocates a fresh
// string per field rather than sub-slicing the request buffer fiber reuses.
type push struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login    string `json:"login"`
			Username string `json:"username"`
		} `json:"owner"`
	} `json:"repository"`
	Pusher struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	} `json:"pusher"`
}

// verdict is what the receiver did with one delivery, and it is the reason this
// answers a body rather than a bare 204.
//
// The forge shows the response on its own delivery page. The door this replaces
// answered an empty 204 whether or not it dispatched, and the cost was eight
// commits of drift behind a green hook page — the truth lived only in the
// forge's hook_task rows, which nobody reads until something is already wrong.
// Fired says whether the two seams ran; Reason says why not when they did not.
//
// Builds is how many builds the push actually LAUNCHED, which is a different
// fact from having fired: most pushes track no application, so zero is ordinary
// — and it is exactly the answer "fired" cannot give. The builder has always
// known the number and the forge leg used to drop it, which left the delivery
// page showing the same green for a push that built eleven services and one that
// built nothing at all.
type verdict struct {
	Org    string `json:"org,omitempty"`
	Repo   string `json:"repo,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	Fired  bool   `json:"fired"`
	Builds int    `json:"builds"`
	Reason string `json:"reason,omitempty"`
}

// init declares the operation. The handler is raw — the HMAC covers the bytes and
// runs before the decode, and a typed op decodes first — so its request, response
// and prose are stated here, next to the route, rather than being left as an
// operationId every generated SDK offers with nowhere to put the delivery.
func init() {
	openapi.Register(hookPath, "POST", push{}, verdict{})
	openapi.Describe(hookPath, "POST",
		"Receive a push from the forge and trigger its build",
		"The forge's push-to-deploy door. git.hanzo.ai runs as a separate server, so its pushes "+
			"never reach this fleet's own receive-pack; without this a push to the host we call "+
			"canonical builds nothing. A verified push is handed to the SAME two seams a native "+
			"push travels — the single-registrant deploy trigger, and the many-subscriber "+
			"lifecycle stream that notifies and indexes — and the build decision itself stays "+
			"downstream in the one place that knows what a push means.\n\n"+
			"PUBLIC at the JWT layer, because the forge carries no Hanzo session: AUTHENTICATION "+
			"IS THE SIGNATURE. The HMAC covers the raw bytes and is verified BEFORE the payload "+
			"is parsed, so an unauthenticated body is never decoded. The secret is read from KMS; "+
			"a deployment that cannot read it answers 503 and processes nothing, rather than "+
			"trusting a delivery it could not check. The body is read UNCOMPRESSED — a request "+
			"declaring a Content-Encoding is refused 415 before it is touched, because decoding "+
			"one is unbounded work bought with a few bytes and no credential. A bad signature is "+
			"401, a payload over 8 MiB is 413, and a malformed one 400.\n\n"+
			"A verified push that reaches both seams answers 200 with fired true and the NUMBER "+
			"OF BUILDS it launched — zero is ordinary, since most pushes track no application, "+
			"and it is the answer 'fired' cannot give. A push that could not be dispatched "+
			"answers 500: the delivery page shows it red, and the Replay that prompts reaches a "+
			"fresh attempt rather than being declined as already landed.\n\n"+
			"The deliveries deliberately ignored answer 200 with a reason and nothing else: a "+
			"payload that is not a push, a ref DELETE (a zero `after` has no commit to build), a "+
			"BOT-authored push (release automation pushes as the forge's own Actions user, and a "+
			"release must never rebuild itself), a push from a forge namespace that maps to no "+
			"org, and a redelivery of a push already fired. Branches and tags both reach the "+
			"build trigger, because releases are cut by tag and filtering here would silently stop "+
			"publishing.")
}

// errUnread is the answer while the very first read is still in flight: there is
// no held outcome to serve and inventing one would be the bypass.
var errUnread = fmt.Errorf("%s has not been read yet", forge.WebhookRef)

// errNoAnswer is recorded when the read did not come back at all — a panic
// inside the KMS client. It exists so the deferred settle always has an outcome
// to record: settling a panic as success would hold the empty value and 401
// every delivery while blaming the forge's configuration.
var errNoAnswer = fmt.Errorf("read %s: the KMS client did not return", forge.WebhookRef)

// secret is the verifying key. It holds the last value that READ CLEANLY, and
// the outcome of the last read whether or not that was one, for [hookFresh].
//
// HOLDING THE FAILURE IS THE POINT, and it is what makes this door survivable
// while it is unauthenticated. Verifying needs the key, so the key is read before
// any caller has proved anything; and the state every deployment STARTS in is the
// ref unprovisioned. Caching only success meant that state turned a flood of
// anonymous POSTs into a flood of KMS reads, from the process that also owns
// builds, deploys and the reconciler. The window now bounds the read whichever
// way it goes: one per window, not one per request.
//
// THE KMS CALL IS OUTSIDE THE LOCK, and a caller arriving during a refresh is
// answered from what is held rather than queued behind it. Holding the lock
// across the call is a correct single-flight and the wrong one to ship here: it
// costs one round trip per window instead of one per request, but every arrival
// inside that trip is a goroutine parked in the build process — which is the
// pile-up, not a fix for it.
type secret struct {
	mu   sync.Mutex
	v    string    // the last value that read cleanly; a failure never replaces it
	err  error     // the last read's outcome, kept for observability
	when time.Time // zero until the first read has completed
	busy bool      // a refresh is in flight; others read what is held
}

// read returns the forge's webhook secret, refreshing it at most once per
// [hookFresh] however the last read turned out.
//
// Fail-closed at every step: no KMS, a KMS that cannot answer, an empty secret,
// or a refresh still in flight with nothing held yet each return an error and
// never a value, because a door that starts builds must refuse rather than trust.
// The error names the REF and never the value — a ref is a path and is safe to log.
func (k *secret) read(s *cloud.Service[state], ctx context.Context) (string, error) {
	if v, err, mine := k.claim(); !mine {
		return v, err
	}
	// Seeded, and DEFERRED BEFORE THE CALL, so the refresh is released whatever
	// the read does — including panicking, which used to leave busy set and every
	// later delivery answering 503 for the life of the process with KMS healthy.
	v, err := "", errNoAnswer
	defer func() { k.settle(v, err) }()

	v, err = fetch(s, ctx)
	if err != nil {
		// A FAILED REFRESH DOES NOT DISCARD THE KEY THAT WORKS. The value read
		// last time is still the value the forge signs with — the read failed, the
		// secret did not change — so replacing it with nothing turned one KMS blip
		// into five minutes of deliveries this door could not verify, and this fork
		// does not redeliver them. The failure is still recorded (settle, above),
		// so it is visible; it just does not take the key with it.
		if good := k.good(); good != "" {
			s.Log.Warn("forge hook: KMS refresh failed; still verifying with the key that read cleanly", "err", err)
			return good, nil
		}
		return "", err
	}
	return v, nil
}

// claim answers from what is held, and reports whether THIS caller is the one that
// must go and refresh it. Exactly one caller is, per window.
func (k *secret) claim() (string, error, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	// Someone is already fetching, or what is held is still fresh: answer now,
	// without a call and without waiting for one.
	if k.busy || time.Since(k.when) < hookFresh {
		if k.when.IsZero() {
			return "", errUnread, false
		}
		// A held key answers whatever the last refresh did, which is the SAME rule
		// the refreshing caller applies to its own failure — stated once, so the
		// answer does not depend on which caller you were.
		if k.v != "" {
			return k.v, nil, false
		}
		return "", k.err, false
	}
	k.busy = true
	return "", nil, true
}

// good is the last value that read cleanly, or empty when there has never been one.
func (k *secret) good() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.v
}

// settle records an outcome and releases the refresh. A failed read records the
// failure and KEEPS the last value that read cleanly — see [secret.read].
func (k *secret) settle(v string, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.err, k.when, k.busy = err, time.Now(), false
	if err == nil {
		k.v = v
	}
}

// fetch is the KMS read itself.
//
// It runs on a context DETACHED from the request's, with its own bound. The
// answer is shared process state, so a client that hangs up mid-read must not
// cancel it — cancelled, that error is what gets held for the window, and one
// disconnecting caller would 503 every legitimate delivery behind it. The bound
// is there because a refresh nobody finishes leaves the door answering errUnread
// forever.
func fetch(s *cloud.Service[state], ctx context.Context) (string, error) {
	if s.KMS == nil {
		return "", fmt.Errorf("no KMS client mounted: cannot read %s", forge.WebhookRef)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hookRead)
	defer cancel()
	b, err := s.KMS.GetSecret(ctx, forge.WebhookRef)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", forge.WebhookRef, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", forge.WebhookRef)
	}
	return v, nil
}

// seen is the set of pushes this receiver is firing, or has fired, within
// [hookWindow].
type seen struct {
	mu sync.Mutex
	at map[string]time.Time
}

// hold takes key for this caller, reporting whether the caller now has it. A key
// already held inside the window is somebody else's, and the delivery naming it
// is a duplicate.
//
// The key is the FACT — namespace, repo, ref, and the commit it moved to — not
// the forge's delivery id, so two deliveries describing one landed ref fire once
// however the forge chooses to identify them.
//
// TAKING IT IS HALF THE ACT: what a hold becomes is decided by whether the
// dispatch succeeded, and a failed one gives it back ([seen.drop]). Recording the
// fact up front and never rolling it back is what turned a transient trigger
// failure into a push lost for the whole window — and into a Replay, the ONE
// recovery this fork has, refused "already landed".
//
// It is this process's memory, which is the whole of what it claims to be: the
// duplicate it exists to stop is a redelivery of a request that timed out AFTER
// the seams already ran, and that retry reaches the replica the load balancer
// sends it to. A cross-replica answer is the build store's to give, and giving it
// here would put the same question in two places.
func (k *seen) hold(key string, now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.at == nil {
		k.at = map[string]time.Time{}
	}
	if was, ok := k.at[key]; ok && now.Sub(was) < hookWindow {
		return false
	}
	// Swept on write, so the map is bounded by one window's pushes rather than by
	// uptime, and there is no goroutine to stop at shutdown.
	for k2, was := range k.at {
		if now.Sub(was) >= hookWindow {
			delete(k.at, k2)
		}
	}
	k.at[key] = now
	return true
}

// drop gives a held key back, so the next delivery naming that push is a fresh
// attempt rather than a duplicate. It is what a dispatch failure does with its
// hold: nothing fired, so there is nothing to remember.
func (k *seen) drop(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.at, key)
}

// What a delivery may name, as the forge itself spells it.
//
// These three leave this door and are used as more than text: the namespace and
// the name build the clone URL a build Job is handed, the name is the key a
// lifecycle reactor resolves a directory by, and the commit is a git argument. A
// separator, a leading dash or a dot-dot in any of them is a traversal or a flag
// in a position that expects a value — so the shape is checked once, here, at the
// boundary, rather than in each of the places it arrives.
//
// It is the FORGE's naming rule and deliberately not platform's slugRE: a forge
// repository may be Mixed.Case where a platform app slug may not, so borrowing
// that value would refuse legitimate repositories in order to reuse a regexp.
// The commit is a WHOLE object id and never a prefix. Git names a ref's new tip
// in full — 40 hex under SHA-1, 64 under SHA-256 — so the two widths are the
// whole set a delivery can carry, and admitting a 7-character prefix admitted a
// value that resolves to different objects in different clones of one repository.
var (
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	refRE    = regexp.MustCompile(`^refs/(heads|tags)/[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$`)
	commitRE = regexp.MustCompile(`^([0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
)

// coordinate reports whether a delivery names a repository, a ref and a commit
// this door can act on. The dot-dot is refused separately because the character
// classes above admit each dot on its own, and git's own ref rules refuse the
// pair for the same reason a path does.
func coordinate(owner, repo, ref, commit string) bool {
	return nameRE.MatchString(owner) && nameRE.MatchString(repo) &&
		refRE.MatchString(ref) && !strings.Contains(ref, "..") &&
		commitRE.MatchString(commit)
}

// signed reports whether any of sigs carries the hex HMAC-SHA256 of body under
// secret, compared in constant time.
//
// The MAC is computed ONCE and each candidate compared against it, so reading
// several header spellings costs several comparisons rather than several hashes
// of the body. An empty secret, an absent header or malformed hex is false —
// fail-closed, never a bypass — and no candidate can admit a delivery without
// the secret, so accepting whichever spelling the forge sent widens nothing.
func signed(secret string, body []byte, sigs ...string) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, sig := range sigs {
		// "sha256=<hex>" is the GitHub spelling the forge emits beside its own bare
		// hex; the digest is identical.
		got, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(sig), "sha256="))
		if err != nil || len(got) == 0 {
			continue
		}
		if hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

// ignored answers 200 naming why nothing fired.
//
// 200 and not an error: the delivery is well-formed and CORRECTLY declined, so
// there is nothing to recover. Red on the forge's delivery page means "act", and
// the only act it offers is Replay — which would decline this delivery again,
// identically, forever. A non-2xx is spent on the one case where replaying does
// help: a push this door meant to dispatch and could not.
//
// The reason travels in the BODY, where the forge's delivery page shows it, so
// "why did my push not build" is answered at the forge instead of only in a log.
func ignored(c *zip.Ctx, why string) error {
	return c.JSON(http.StatusOK, verdict{Reason: why})
}

// hook verifies and processes one forge delivery.
func hook(s *cloud.Service[state], c *zip.Ctx) error {
	// AN ENCODED BODY IS REFUSED BEFORE IT IS TOUCHED, and the order is the whole
	// control. Reading the body DECOMPRESSES it when the request declares a
	// Content-Encoding, so maxHookBody — checked on what comes back — bounds the
	// INFLATED size and can only ever be told about an allocation that has already
	// happened. 8 KB of gzip on the wire bought 8 MiB of it, in the process that
	// owns builds, deploys and the reconciler, from a caller holding no credential
	// at all. The forge sends its deliveries uncompressed, so nothing this door
	// serves needs the feature it was paying for.
	if enc := strings.TrimSpace(c.Header("Content-Encoding")); enc != "" {
		return zip.Errorf(http.StatusUnsupportedMediaType, "this door reads an uncompressed body")
	}
	body := c.Body()
	if len(body) > maxHookBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "payload too large")
	}
	// The forge this deployment owns, resolved once: the delivery's clone URL is
	// built from it, and the lifecycle origin IS it. A deployment that cannot name
	// its own forge refuses rather than continues, because the value it would carry
	// on is the empty origin — which every mirror reads as "a native push, send it
	// on" and is the one loop this door has to not start.
	host := forge.Host(s.Domain)
	if host == "" {
		s.Log.Error("forge hook: this deployment names no forge", "domain", s.Domain)
		return zip.Errorf(http.StatusServiceUnavailable, "no forge host for this deployment")
	}
	key, err := s.State.hook.read(s, c.Context())
	if err != nil {
		// 503 and not 401. A deployment that cannot read its own secret has not been
		// handed a bad signature, and saying so would send an operator to look at the
		// forge's hook configuration for a fault that is in ours. It is fail-closed —
		// nothing below this line runs — and it is red on the delivery page, which is
		// the whole recovery: the forge does not retry, so the push builds when
		// somebody replays it after KMS answers again. That is also why a good key
		// survives a failed refresh (secret.read): the fewer deliveries land here,
		// the fewer need a person.
		s.Log.Error("forge hook: no secret to verify against", "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "forge webhook secret unavailable")
	}
	sigs := make([]string, len(sigHeaders))
	for i, h := range sigHeaders {
		sigs[i] = c.Header(h)
	}
	if !signed(key, body, sigs...) {
		return zip.Errorf(http.StatusUnauthorized, "invalid signature")
	}

	var ev push
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid payload")
	}
	// THE PAYLOAD SAYS WHAT THIS IS, not a header. The forge names the event in a
	// header from the same renamed family as the signature, and a receiver gating
	// on one spelling of it answers every push a benign 200 the day the forge picks
	// another — the silent-nothing this door exists to have stopped. A push is the
	// only delivery carrying a ref, a repository and a commit the ref moved to;
	// every other event is missing one of them.
	owner := cmp.Or(ev.Repository.Owner.Login, ev.Repository.Owner.Username)
	if !strings.HasPrefix(ev.Ref, "refs/") || owner == "" || ev.Repository.Name == "" {
		return ignored(c, "not a push")
	}
	if ev.After == "" || ev.After == zeroSHA {
		return ignored(c, "ref deleted")
	}
	if !coordinate(owner, ev.Repository.Name, ev.Ref, ev.After) {
		s.Log.Warn("forge hook: a delivery named a coordinate the forge cannot hold",
			"owner", owner, "repo", ev.Repository.Name, "ref", ev.Ref)
		return ignored(c, "malformed coordinate")
	}
	// Our own release and mirror automation pushes as the forge's Actions user, so
	// without this a release's own commit triggers the next release, forever. The
	// ONE bot predicate every push transport shares.
	pusher := cmp.Or(ev.Pusher.Login, ev.Pusher.Username)
	if cloud.IsBotActor(pusher) {
		return ignored(c, "bot push")
	}
	// WHOSE push this is comes from the closed forge-namespace table and from
	// nothing else in the delivery. A namespace is free to create with a forge
	// account, so an unmapped one resolving to the org spelled the same way would
	// make "which tenant rebuilds, and whose compute pays" a thing a signup picks.
	org, oerr := forge.Org(owner)
	if oerr != nil {
		s.Log.Warn("forge hook: push from a namespace that maps to no org",
			"owner", owner, "repo", ev.Repository.Name, "ref", ev.Ref)
		return ignored(c, "forge namespace maps to no org")
	}
	// The key is the LANDED FACT, spelled the way tenancy is decided: the namespace
	// lowercased, as forge.Org reads it. Keyed on the raw string, `HanzoAI` and
	// `hanzoai` are two keys for one landed commit and the second one builds it
	// again — on the tenant's compute — while every other decision on this path has
	// already agreed they are one namespace.
	fact := strings.ToLower(owner) + "/" + ev.Repository.Name + " " + ev.Ref + " " + ev.After
	if !s.State.landed.hold(fact, time.Now()) {
		return ignored(c, "already landed")
	}

	// SEAM ONE: the deploy trigger. Single-registrant and synchronous, dispatching
	// in THIS process to buildFromPush.
	//
	// The clone URL is derived from the forge that delivered and the repository it
	// named — the spelling the forge itself publishes, and the one an application's
	// RepoURL is matched against (normRepo).
	//
	// A FAILURE IS THE DELIVERY'S FAILURE, and is answered as one. This receiver
	// is not the push — the push landed on the forge minutes ago and nothing here
	// can undo it — it is the only thing that turns that push into a build, and
	// this fork does not retry: a delivery is marked delivered before the attempt,
	// and the one recovery is a person clicking Replay on the forge's delivery
	// page. So the hold is given back and the answer is non-2xx: the page shows
	// red where it would have shown a green "fired", and the Replay it prompts
	// reaches a fresh attempt instead of "already landed".
	builds, err := cloud.OnGitPush(c.Context(), cloud.GitPushEvent{
		Org: org, Repo: ev.Repository.Name, Ref: ev.Ref, Commit: ev.After,
		CloneURL: "https://" + host + "/" + owner + "/" + ev.Repository.Name + ".git",
	})
	if err != nil {
		s.State.landed.drop(fact)
		s.Log.Error("forge hook: build trigger failed",
			"org", org, "repo", ev.Repository.Name, "ref", ev.Ref, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "the push was verified but no build could be started; replay this delivery")
	}

	// SEAM TWO: the lifecycle stream. Many-subscriber and detached (notify, the
	// code index, the outbound mirror), and never able to perturb the deploy above.
	//
	// Branch, not the ref: every subscriber reads it as a branch NAME — the mirror
	// pushes it, the indexer compares it to the default branch. A tag leaves it
	// empty, which those subscribers already skip, and still reaches the build
	// trigger above because releases are cut by tag.
	branch, isBranch := strings.CutPrefix(ev.Ref, "refs/heads/")
	if !isBranch {
		branch = ""
	}
	// Origin is the host these refs arrived FROM, and it is the outbound mirror's
	// echo-suppression key. Cloud's own mirror pushes TO this forge; the forge signs
	// a delivery straight back; without an origin the mirror would push what it has
	// just sent, and keep doing it. Named from the one derivation of the forge host
	// so it is the same string the mirror's target carries.
	cloud.EmitLifecycle(c.Context(), cloud.LifecycleEvent{
		Kind: cloud.LifecyclePushLanded, Org: org, Repo: ev.Repository.Name,
		Branch: branch, Before: ev.Before, After: ev.After, Pusher: pusher,
		Origin: host,
	})

	s.Log.Info("forge push landed", "org", org, "repo", ev.Repository.Name,
		"ref", ev.Ref, "commit", shortTag(ev.After), "pusher", pusher, "builds", builds)
	return c.JSON(http.StatusOK, verdict{
		Org: org, Repo: ev.Repository.Name, Ref: ev.Ref, Commit: ev.After,
		Fired: true, Builds: builds,
	})
}
