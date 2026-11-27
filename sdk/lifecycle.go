package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/account"
)

// GitPushEvent describes a push that just landed on the embedded git server: the
// org, the repo, the FULL ref that moved (refs/heads/<b> or refs/tags/<t>), and
// its new tip commit. CloneURL is the
// canonical clone URL of that repo (https://<host>/v1/git/<org>/<repo>.git) — the
// exact value an Application's RepoURL carries — so the builder can resolve which
// app (if any) tracks this ref and needs a rebuild. Tags reach the builder too:
// releases are cut by tag, so filtering them here would stop publishing silently.
type GitPushEvent struct {
	Org      string
	Project  string
	Repo     string
	Ref      string // FULL ref: refs/heads/<branch> or refs/tags/<tag>
	Commit   string
	CloneURL string
}

// IsBotActor reports whether a push actor is an automation identity rather than a
// person. Every inbound push transport asks this BEFORE firing OnGitPush: our own
// release and mirror automation push AS these identities, so without the guard a
// release's own commit triggers the next release, forever. The two wires we ingest
// spell a bot differently — GitHub suffixes App-authored logins with "[bot]", our
// forge attributes workflow-made pushes to its Actions system user, and the sync
// plane advances mirrors as its own forge account — so the ONE predicate knows all
// three.
//
// IT ANSWERS THE UNKNOWN ACTOR "BOT", and that direction is the point. This is a
// LOOP GUARD, so the question it really asks is "could this push be one of ours?"
// — and a delivery that names nobody is precisely when that cannot be ruled out.
// Answering "human" to an absent login let a push with an unparsed actor field
// through the one gate that stops a release rebuilding itself.
//
// The suffix is matched WITHOUT case for the same reason. A login is compared
// case-insensitively everywhere it is authenticated, so "renovate[BOT]" is the
// same account as "renovate[bot]" — and a guard that a different capitalization
// walks past is not one.
func IsBotActor(login string) bool {
	login = strings.TrimSpace(login)
	if login == "" {
		return true
	}
	login = strings.ToLower(login)
	return strings.HasSuffix(login, "[bot]") || forgeBots[login]
}

// forgeBots are the forge accounts that push on the estate's behalf: the forge's
// Actions system user, and hanzo-sync, the account whose token the sync plane
// holds (forge.TokenRef). A forge username cannot carry the "[bot]" suffix, so
// these are named.
var forgeBots = map[string]bool{"hanzo-actions": true, "hanzo-sync": true}

// pushBuilder is the registered git-push-to-deploy trigger. clients/platform
// installs it in Mount; clients/git calls OnGitPush after a push lands. The
// inversion keeps git⇄platform decoupled — git never imports platform — exactly
// like kmsClientFactory and the subsystem Registry. Exactly one registration.
//
// It answers HOW MANY BUILDS IT LAUNCHED. Most pushes track no application, so
// zero is ordinary rather than a failure — but zero and one are different facts,
// and a client that returns only an error collapses them into the same "accepted".
// The plane half already carried the number (client.Built.Builds) while the
// in-process half threw it away, so the answer a caller got depended on which
// process the builder happened to be in.
var pushBuilder func(ctx context.Context, ev GitPushEvent) (int, error)

// RegisterPushBuilder installs the git-push-to-deploy trigger. clients/platform
// calls this from its Mount when co-resident; it is the ONE inversion point that
// lets the embedded git server launch a platform build with no git⇄platform cycle.
func RegisterPushBuilder(f func(ctx context.Context, ev GitPushEvent) (int, error)) {
	pushBuilder = f
}

// OnGitPush fires the push-to-deploy trigger for a landed push: the registered
// builder when platform is co-resident, the platform app over the plane when it
// is not.
//
// IT USED TO RETURN NIL WHEN NOTHING WAS REGISTERED, and that nil was the single
// most expensive value in this package. The push lands on GIT's embedded server
// and the builder belongs to PLATFORM — two apps, therefore two processes, so the
// builder was nil on every push that has ever landed in the split fleet. Each one
// returned success to a caller that had committed the client's objects and then
// triggered nothing, and no log line anywhere recorded that a build had not
// happened. "It compiles" and "HTTP 200" both held the whole time.
//
// Best-effort stays the CALLER's contract, not this function's silence: a push
// already committed must not fail because a build could not be triggered. But the
// caller can only honor that contract if it is told. ErrNoPeer says "no
// push-to-deploy in this fleet" — the one error a caller may read as fall back —
// and anything else is an outage worth alarming on. Erasing both into nil is what
// made those two indistinguishable.
//
// It returns the number of builds launched for the same reason: a caller that
// cannot tell "built" from "did nothing" guesses, and the guess is always the
// optimistic one.
func OnGitPush(ctx context.Context, ev GitPushEvent) (int, error) {
	if pushBuilder != nil {
		return pushBuilder(ctx, ev)
	}
	if f := hooks().PlatformPush; f != nil {
		return f(ctx, ev)
	}
	return 0, ErrNoPeer
}

// ServiceReleaseEvent describes a proven, clean-semver image ready to roll live on
// an operator-managed first-party service. It is the payload of the release client
// that closes push→build→image→CR: after a build produces the image, the CR for
// this service is patched to it and the operator reconciles the Deployment.
//
//   - Service is the target CR metadata.name (the repo/service name ⇒ CR name,
//     mirroring universe's image-update.yml convention).
//   - Image is the full registry ref (repository:tag); the tag MUST be clean
//     semver (vX.Y.Z) — the releaser refuses every mutable/sha/suffixed form.
//   - SHA is the source commit for provenance (optional; logged, never gated on).
type ServiceReleaseEvent struct {
	Service string
	Image   string
	SHA     string
}

// serviceReleaser is the registered first-party CR-rollout client. clients/paas
// (the owner of the hanzo.ai/v1 Service CR control plane) installs it in Mount; a
// proven build calls OnServiceRelease, which patches spec.image on the matching
// CR. The inversion keeps package cloud from importing clients/paas (which imports
// cloud) — the same idiom as pushBuilder / kmsClientFactory. Exactly one
// registration.
var serviceReleaser func(ctx context.Context, ev ServiceReleaseEvent) error

// RegisterServiceReleaser installs the first-party CR-rollout hook. clients/paas
// calls this from its Mount when co-resident; it is the ONE inversion point that
// lets a build-completion path roll a proven image onto its operator Service CR
// with no cloud⇄paas import cycle.
func RegisterServiceReleaser(f func(ctx context.Context, ev ServiceReleaseEvent) error) {
	serviceReleaser = f
}

// OnServiceRelease rolls a proven image live by patching the matching hanzo.ai/v1
// Service CR's spec.image (the operator then reconciles the Deployment). The
// releaser enforces the clean-semver gate and CR-name resolution; this is only the dispatch.
//
// Like OnGitPush it used to return nil when unregistered, with the same result:
// a release that patched no CR, reported as a successful rollout. The proving
// build and the CR control plane are different apps, so that was every release.
//
// There is no ServiceReleaserRegistered() any more. A bool cannot carry "the app
// is elsewhere": it answered false both for a fleet with no paas control plane
// and for the ordinary split fleet where platform is simply the next process
// over, and apps/deploy turned that false into a 503 telling operators the
// release plane did not exist while it was up and reachable. Presence is
// answered by making the call — which is the only moment it is knowable anyway,
// since the router may start a lazy app on demand.
func OnServiceRelease(ctx context.Context, ev ServiceReleaseEvent) error {
	if serviceReleaser != nil {
		return serviceReleaser(ctx, ev)
	}
	if f := hooks().PlatformRelease; f != nil {
		return f(ctx, ev)
	}
	return ErrNoPeer
}

// ErrRunnerCapacity is a runner launch refused because every slot is taken. It
// is a deferral, not a failure: the job stays queued with its provider and the
// platform's queue loop starts it once a slot frees. platform's refusal wraps
// it, and [OnWorkflowJob] restores it from the plane's 429, so a receiver asks
// errors.Is rather than reading a message.
var ErrRunnerCapacity = errors.New("no runner slot is free; the job stays queued")

// JobEvent is one workflow job a provider queued, with what a runner needs to
// register for it. Provider is forge or github; Token is the forge's registration
// token or GitHub's just-in-time configuration; URL is the forge, when it is one.
// Try is 0 for the runner a delivery starts, and n for the nth the queue loop
// starts for the same job after the earlier ones were taken by other jobs.
type JobEvent struct {
	Org      string
	Provider string
	Repo     string // owner/name
	ID       int64
	Labels   []string
	URL      string
	Token    string
	Try      int
}

// RunnerName is the name one job's runner runs under: the Job platform creates
// for it and, on GitHub, the registration it joins as. ONE function, because the
// queue reconciles a registration with its Job by this name, so both sides must
// derive it alike.
//
// repo is the repository's own name, never owner/name, mapped onto the alphabet
// Kubernetes names objects in: each run of other characters inside it becomes a
// dot when the run holds one — hanzo.ai keeps its dot, foo._bar is foo.bar, since
// a dot beside a hyphen is no object name — and as many hyphens as it is long
// otherwise, and a run at either end goes. The job id and try follow, and the
// name is held to 63 characters by shortening the repository, never the id: two
// jobs of one long repository cut at the id were one name, and the second never
// got a runner. A try past 0 ends in -r<n>, which no try-0 name can, since those
// end in a digit after a hyphen.
func RunnerName(provider, repo string, id int64, try int) string {
	alnum := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
	m := make([]byte, 0, len(repo))
	for _, r := range strings.ToLower(repo) {
		switch {
		case r < 0x80 && alnum(byte(r)), r == '.':
			m = append(m, byte(r))
		default:
			m = append(m, '-')
		}
	}
	seg := make([]byte, 0, len(m))
	for i := 0; i < len(m); {
		if alnum(m[i]) {
			seg = append(seg, m[i])
			i++
			continue
		}
		j := i
		for j < len(m) && !alnum(m[j]) {
			j++
		}
		switch {
		case i == 0 || j == len(m):
		case strings.IndexByte(string(m[i:j]), '.') >= 0:
			seg = append(seg, '.')
		default:
			seg = append(seg, m[i:j]...)
		}
		i = j
	}
	head, tail := "runner-"+provider+"-", "-"+strconv.FormatInt(id, 10)
	if try > 0 {
		tail += "-r" + strconv.Itoa(try)
	}
	if room := 63 - len(head) - len(tail); len(seg) > room {
		seg = []byte(strings.TrimRight(string(seg[:max(room, 0)]), "-."))
	}
	return head + string(seg) + tail
}

// Podless reports whether a runner label names a platform no pod can run —
// macOS or Windows, which are hosts — so that no Linux pod is started for a
// job asking for one. It reads the label's parts between hyphens and
// underscores, in any case, and never a substring: macOS, macos-14, osx,
// darwin-arm64, m1 to m4, self_hosted_mac, Windows, windows-2022, win-amd64,
// win32 and win64 are hosts; windmill, twin, machine-learning and gpu-macro are
// not. A label naming linux is Linux's whatever else it names, as a
// cross-compiler's linux-win-cross is.
func Podless(label string) bool {
	parts := strings.FieldsFunc(strings.ToLower(label), func(r rune) bool { return r == '-' || r == '_' })
	for _, p := range parts {
		if p == "linux" {
			return false
		}
	}
	for _, p := range parts {
		if strings.HasPrefix(p, "macos") || strings.HasPrefix(p, "windows") {
			return true
		}
		switch p {
		case "osx", "darwin", "mac", "m1", "m2", "m3", "m4", "win", "win32", "win64":
			return true
		}
	}
	return false
}

// runner is the registered launcher: platform installs it in Use when
// co-resident. Same inversion as pushBuilder, and exactly one registration.
var runner func(ctx context.Context, ev JobEvent) (string, error)

// RegisterRunner installs the launcher that turns a queued job into a runner.
func RegisterRunner(f func(ctx context.Context, ev JobEvent) (string, error)) {
	runner = f
}

// OnWorkflowJob launches one runner for a queued job: the registered launcher
// when platform is co-resident, the platform app over the plane when it is not.
// It answers the Job's name, so a caller can tell "launched" from "did nothing".
func OnWorkflowJob(ctx context.Context, ev JobEvent) (string, error) {
	if runner != nil {
		return runner(ctx, ev)
	}
	f := hooks().PlatformJob
	if f == nil {
		return "", ErrNoPeer
	}
	job, err := f(ctx, ev)
	if err != nil {
		// The platform op answers 429 for exactly one refusal, a full cluster.
		var he *zip.HTTPError
		if errors.As(err, &he) && he.Status == http.StatusTooManyRequests {
			return "", fmt.Errorf("%w: %v", ErrRunnerCapacity, err)
		}
		return "", err
	}
	return job, nil
}

// LifecycleKind classifies a git lifecycle event. The value IS the wire name a
// subscription filters on.
type LifecycleKind string

const (
	LifecyclePushLanded   LifecycleKind = "push.landed"
	LifecycleBuildStarted LifecycleKind = "build.started"
	LifecycleDeployLive   LifecycleKind = "deploy.live"
	LifecycleDeployFailed LifecycleKind = "deploy.failed"
)

// LifecycleEvent is one git lifecycle fact fanned out to every registered
// subscriber. A plain data value — values, not places:
//   - Org/Project/Repo    the tenant + repo the fact happened in (the routing key).
//   - Branch/Before/After  the ref that moved and its old→new tip (a push).
//   - Pusher              who pushed (best-effort; "" for a client-less push).
//   - DeployID/Detail     the deployment id + a human one-liner (a deploy transition).
//   - Origin              "" for a native push; the source host when the refs
//     arrived via an inbound mirror sync — the loop-prevention client that lets the
//     outbound mirror subscriber suppress a re-mirror of refs it just pulled in.
type LifecycleEvent struct {
	Kind    LifecycleKind
	Org     string
	Project string
	Repo    string
	Branch  string
	Before  string
	After   string
	Pusher  string
	// Payer is who pays for the work this event causes — the pusher's account on a
	// push, resolved where the push was authenticated. Zero when no person caused
	// it (a sync from a linked repository), and a reactor that spends money then
	// names the org that owns the repository on purpose.
	Payer    account.Account
	DeployID string
	Detail   string
	Origin   string
}

// RepoFromCloneURL extracts the repo name from a git clone URL (last path segment,
// ".git" stripped) — the repo component of the (org,project,repo) routing key that
// a deploy emitter derives from an app/project's linked RepoURL. The ONE place this
// derivation lives, shared by the platform + projects deploy paths (no per-package
// copy).
func RepoFromCloneURL(u string) string {
	u = strings.TrimSuffix(strings.TrimSpace(u), "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		return u[i+1:]
	}
	return u
}

// lifecycleSubscribers is the fan-out list. Registration happens at Mount
// (single-threaded, before any request is served), so a plain slice is correct:
// EmitLifecycle only ever ranges it after every subsystem's Mount has run.
var lifecycleSubscribers []func(ctx context.Context, ev LifecycleEvent)

// RegisterLifecycleSubscriber adds a git-lifecycle reactor. Every subsystem that
// reacts to a push/deploy (mirror-out, Slack-notify) registers ONE here at Mount;
// git/platform EMIT via EmitLifecycle. The inversion keeps the emitters from
// importing the subscribers — the same pattern as RegisterPushBuilder, but
// many-registrant.
func RegisterLifecycleSubscriber(fn func(ctx context.Context, ev LifecycleEvent)) {
	if fn == nil {
		return
	}
	lifecycleSubscribers = append(lifecycleSubscribers, fn)
}

// ResetLifecycleSubscribers clears the registry. TEST-ONLY client (a test mounts and
// unmounts repeatedly); production registers once at Mount and never resets.
func ResetLifecycleSubscribers() { lifecycleSubscribers = nil }

// EmitLifecycle fans one event out to every registered subscriber, best-effort and
// NON-BLOCKING: each subscriber runs in its own goroutine on a cancel-immune
// context (the fact already happened — a request cancel must not abort the
// notify/mirror), so a slow reactor (a mirror push) can never delay the git/deploy
// path or another reactor. A panicking subscriber is contained so one bad reactor
// can neither crash the shared multi-tenant process nor starve the others; each
// subscriber logs its own errors.
func EmitLifecycle(ctx context.Context, ev LifecycleEvent) {
	subs := lifecycleSubscribers
	if len(subs) == 0 {
		return
	}
	bg := context.WithoutCancel(ctx)
	for _, fn := range subs {
		go func(fn func(context.Context, LifecycleEvent)) {
			defer func() { _ = recover() }()
			fn(bg, ev)
		}(fn)
	}
}
