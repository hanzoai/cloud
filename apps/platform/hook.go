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
// rename. apps/git's route is the 410 that names this one.
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
//	Target URL    https://api.hanzo.ai/v1/git-webhook
//	HTTP method   POST
//	Content type  application/json
//	Secret        the value at KMS forge.WebhookRef
//	Trigger       Push events only
//	Branch filter *
//
// api.hanzo.ai is the fleet's one endpoint; platform.hanzo.ai/v1/git-webhook is
// the same door through the sibling host, and is the address apps/git's 410 names.
//
// The secret is CONFIGURED, not generated here: the forge and this receiver share
// one value, and the KMS ref is where the deployment keeps it. Rotating means
// writing the new value at that ref and pasting the same value on the forge's
// hook; deliveries signed with the old one are refused within one [hookFresh]
// window.

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

// hookPath is where the forge delivers. It is the address apps/git's 410 has
// been naming since that door was retired, so making it real is what turns a
// published replacement into a working one; a receiver at a different address
// would leave the retired door pointing at nothing.
const hookPath = "/v1/git-webhook"

const (
	// maxHookBody bounds what is read and signed over. A hostile or oversized body
	// can neither exhaust memory nor slip past the HMAC, since the bytes verified
	// are exactly the bytes acted on.
	maxHookBody = 8 << 20 // 8 MiB
	// hookFresh bounds how long the verifying secret is held. A rotation is live
	// within this window with no restart, and an unauthenticated flood costs one
	// KMS read per window rather than one per request.
	hookFresh = 5 * time.Minute
	// hookWindow is how long a landed push is remembered for. It covers the
	// forge's own redelivery of a request it could not complete — which is the
	// only way one push arrives twice.
	hookWindow = 30 * time.Minute
	// zeroSHA is git's all-zero object id: the `after` of a deleted ref.
	zeroSHA = "0000000000000000000000000000000000000000"
)

// The forge has renamed its header family twice (X-Gogs-, X-Gitea-, X-Forgejo-)
// and emits the GitHub spelling beside its own for receivers written against
// that server. All of them carry the SAME hex HMAC-SHA256 of the body; the
// GitHub one prefixes it. A receiver that knew one spelling would reject every
// delivery the day the forge picked another, so the ONE verifier knows them all
// — the same shape cloud.IsBotActor takes for the two wires that spell a bot
// differently.
var sigHeaders = []string{"X-Forgejo-Signature", "X-Gitea-Signature", "X-Hub-Signature-256"}

// push is the subset of the forge's push payload this door acts on. Owner and
// pusher each carry both spellings the payload has used across forge versions
// (login vs username); first non-empty wins.
//
// The payload's own clone_url is deliberately NOT among these. It would be a
// third-party string reaching the build path — the value buildFromPush matches an
// application's RepoURL against, and the one isReleasePush compares to cloud's own
// upstream — and it carries nothing this door does not already know: the host is
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
type verdict struct {
	Org    string `json:"org,omitempty"`
	Repo   string `json:"repo,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	Fired  bool   `json:"fired"`
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
			"trusting a delivery it could not check. A bad signature is 401, a payload over 8 MiB "+
			"is 413, and a malformed one 400.\n\n"+
			"Every other outcome is 200 carrying what the receiver DID: fired true for a push that "+
			"reached both seams, or a reason it did not. The deliveries deliberately ignored are a "+
			"payload that is not a push, a ref DELETE (a zero `after` has no commit to build), a "+
			"BOT-authored push (release automation pushes as the forge's own Actions user, and a "+
			"release must never rebuild itself), a push from a forge namespace that maps to no "+
			"org, and a redelivery of a push already fired. Branches and tags both reach the "+
			"build trigger, because releases are cut by tag and filtering here would silently stop "+
			"publishing. A trigger that fails is logged rather than returned, so a push that has "+
			"already landed on the forge is not redelivered against us.")
}

// secret is the verifying key, held for [hookFresh].
//
// The read is under the lock rather than around it, so a flood arriving on a cold
// or expired key becomes ONE in-flight KMS read and not one per request — this
// door is unauthenticated until the signature is checked, and the check is what
// needs the key.
type secret struct {
	mu    sync.Mutex
	value string
	when  time.Time
}

// read returns the forge's webhook secret. Fail-closed at every step: no KMS, a
// KMS that cannot answer, or an empty secret each return an error and never a
// value, because a door that starts builds must refuse rather than trust. The
// error names the REF and never the value — a ref is a path and is safe to log.
func (k *secret) read(s *cloud.Service[state], ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.value != "" && time.Since(k.when) < hookFresh {
		return k.value, nil
	}
	if s.KMS == nil {
		return "", fmt.Errorf("no KMS client mounted: cannot read %s", forge.WebhookRef)
	}
	b, err := s.KMS.GetSecret(ctx, forge.WebhookRef)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", forge.WebhookRef, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", forge.WebhookRef)
	}
	k.value, k.when = v, time.Now()
	return v, nil
}

// seen is the set of pushes this receiver has already fired, within [hookWindow].
type seen struct {
	mu sync.Mutex
	at map[string]time.Time
}

// first reports whether key is new inside the window, and records it.
//
// The key is the FACT — namespace, repo, ref, and the commit it moved to — not
// the forge's delivery id, so two deliveries describing one landed ref fire once
// however the forge chooses to identify them.
//
// It is this process's memory, which is the whole of what it claims to be: the
// duplicate it exists to stop is the forge redelivering a request that timed out
// AFTER the seams already ran, and that retry reaches the replica the load
// balancer sends it to. A cross-replica answer is the build store's to give, and
// giving it here would put the same question in two places.
func (k *seen) first(key string, now time.Time) bool {
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
var (
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	refRE    = regexp.MustCompile(`^refs/(heads|tags)/[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$`)
	commitRE = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
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
// 200 and not an error: the delivery is well-formed and correctly declined, and a
// non-2xx would put it in the forge's retry queue to be declined again forever.
// The reason travels in the BODY, where the forge's delivery page shows it, so
// "why did my push not build" is answered at the forge instead of only in a log.
func ignored(c *zip.Ctx, why string) error {
	return c.JSON(http.StatusOK, verdict{Reason: why})
}

// hook verifies and processes one forge delivery.
func hook(s *cloud.Service[state], c *zip.Ctx) error {
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
		// forge's hook configuration for a fault that is in ours. It is still
		// fail-closed — nothing below this line runs — and the forge redelivers, so
		// the push builds once KMS answers again.
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
	if !s.State.landed.first(owner+"/"+ev.Repository.Name+" "+ev.Ref+" "+ev.After, time.Now()) {
		return ignored(c, "already landed")
	}

	// SEAM ONE: the deploy trigger. Single-registrant and synchronous, dispatching
	// in THIS process to buildFromPush. Best-effort by the seam's contract — the
	// push already landed on the forge, so a trigger failure is logged rather than
	// returned as an error the forge would redeliver against us.
	//
	// The clone URL is derived from the forge that delivered and the repository it
	// named — the spelling the forge itself publishes, and the one an application's
	// RepoURL normalises to (sameRepo drops the ".git" and the case).
	if err := cloud.OnGitPush(c.Context(), cloud.GitPushEvent{
		Org: org, Repo: ev.Repository.Name, Ref: ev.Ref, Commit: ev.After,
		CloneURL: "https://" + host + "/" + owner + "/" + ev.Repository.Name + ".git",
	}); err != nil {
		s.Log.Warn("forge hook: build trigger failed",
			"org", org, "repo", ev.Repository.Name, "ref", ev.Ref, "err", err)
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
		"ref", ev.Ref, "commit", shortTag(ev.After), "pusher", pusher)
	return c.JSON(http.StatusOK, verdict{
		Org: org, Repo: ev.Repository.Name, Ref: ev.Ref, Commit: ev.After, Fired: true,
	})
}
