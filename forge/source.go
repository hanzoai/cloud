package forge

// source.go is how a process GETS a client, and it is the only way.
//
// It moved here from the one app that first needed a forge (apps/todo). That
// was fine while there was one caller and wrong the moment there were several:
// the credential's KMS coordinate, its refresh window, the derivation of the
// host from the deployment's own domain, and the IAM-org → forge-org
// translation are all facts about THE FORGE, and a copy of them in each app is
// four chances for two apps to disagree about which forge they are talking to.
//
// Every process holds its own [Source]. That is not a shared cache and does not
// want to be: a client is an HTTP client and a token, both cheap, and the thing
// worth not repeating is the KMS READ, which a per-process window already
// bounds.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/brand"
)

// TokenRef is the KMS coordinate of the forge machine credential.
//
// KMS is the one home for a secret: not an env file (which reaches a git history
// and a pod spec), not a browser-held token (which puts a forge credential in
// reach of any script on the page), and not a per-user grant this process would
// have to custody and rotate N times.
const TokenRef = "orgs/hanzo/deploy/FORGE_TRACKER_TOKEN@prod"

// HostKeyRef is the KMS coordinate of the forge's SSH host key, as a known_hosts
// line.
//
// Cloud OPERATES this forge, so its host key is a value the deployment can
// simply KNOW. Learning it by connecting is trust-on-first-use however carefully
// it is done, and a process that learned it once holds that answer for its whole
// life — so a single well-timed interception poisons every run the pod serves.
// Configured, there is no first use to get wrong.
//
// Absent, [Client.Known] falls back to learning it. That is a weaker deployment
// and it says so in the log rather than failing closed, because a forge whose
// key has not been recorded yet is a deployment that has not finished being set
// up, not one under attack — and the fallback still beats the sandbox trusting
// its own network.
const HostKeyRef = "orgs/hanzo/deploy/FORGE_HOST_KEY@prod"

// WebhookRef is the KMS coordinate of the secret the forge signs its
// workflow_job deliveries with, and the one value the receiver verifies them
// against. Rotating it is writing the new value here and on the forge's system
// webhook; the receiver reads it again within its window.
const WebhookRef = "orgs/hanzo/deploy/FORGE_WEBHOOK_SECRET@prod"

// fresh bounds how long a resolved credential is reused. A rotated token is
// therefore live within this window without a restart, and a revoked one stops
// working. Short enough to make rotation real, long enough that a read is not a
// KMS round trip.
const fresh = 5 * time.Minute

// Secrets is the sliver of a KMS client this package needs.
//
// Declared here rather than imported so that forge stays a leaf of the estate's
// dependency graph: it is reachable from any app, and an app reaching it must
// not thereby link the KMS client.
type Secrets interface {
	GetSecret(ctx context.Context, ref string) ([]byte, error)
}

// Source resolves the deployment's forge client and holds it for [fresh].
//
// The zero Source is ready. It is safe for concurrent use.
type Source struct {
	// Host overrides the derived forge host. Empty — every production
	// deployment — derives it, and cannot then drift per brand.
	Host string

	mu   sync.Mutex
	c    *Client
	when time.Time
	// pinned records whether the last resolve found a usable configured host key,
	// and why not when it did not. Read by [Source.Pinned]; see [HostKeyRef].
	pinned bool
	why    string
}

// Pinned reports whether the forge's host key is CONFIGURED rather than learned
// on first use, and why not when it is not.
//
// A caller logs this once at startup. Without it the degradation is silent: a
// wrong ref, a KMS hiccup or a secret nobody created all read exactly like a
// healthy deployment right up until somebody intercepts the first handshake a
// pod makes.
func (s *Source) Pinned() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pinned, s.why
}

// badPin refuses a known_hosts line that verifies nothing.
//
// A wildcard host pattern matches every host, so ssh would accept whatever key
// it is offered — a pin in appearance and none in fact. Empty and
// single-field lines are refused for the same reason: they are not a pin.
func badPin(line string) bool {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) < 3 {
		return true
	}
	return strings.ContainsAny(f[0], "*?")
}

// Client returns a client authenticated with the deployment's machine
// credential, reading it from KMS when the held one is absent or stale.
//
// It is UNSCOPED: the returned client has no actor and every call on it refuses
// ([ErrNoActor]). A caller states who it is acting as with [Client.As], or that
// there is deliberately nobody with [Client.Machine].
//
// Fail closed at every step: no KMS, a KMS that cannot answer, or an empty
// secret each return an error and never a client. The alternative — an anonymous
// client — would quietly serve only public repositories and read as "you have no
// work" rather than "this deployment is misconfigured".
//
// The error names the REF, never the value. A ref is a path and is safe to log.
func (s *Source) Client(ctx context.Context, kms Secrets, domain string) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c != nil && time.Since(s.when) < fresh {
		return s.c, nil
	}
	if kms == nil {
		return nil, fmt.Errorf("no KMS client in use: cannot read %s", TokenRef)
	}
	b, err := kms.GetSecret(ctx, TokenRef)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", TokenRef, err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return nil, fmt.Errorf("%s is empty", TokenRef)
	}
	c, err := New(s.host(domain), token)
	if err != nil {
		return nil, err
	}
	// The configured pin, read beside the credential it travels with.
	//
	// A missing or unreadable one is not fatal — see [HostKeyRef] — but it is
	// RECORDED, because the difference between a configured pin and a learned one
	// is the difference between a fact and a guess, and a deployment that thinks
	// it configured a pin must not discover otherwise only by being attacked. The
	// signal is a field rather than a log line: this package has no logger, and
	// inventing one to say a single thing would put a dependency in every caller.
	s.pinned, s.why = false, ""
	hk, herr := kms.GetSecret(ctx, HostKeyRef)
	switch {
	case herr != nil:
		s.why = fmt.Sprintf("%s is unreadable (%v)", HostKeyRef, herr)
	case badPin(string(hk)):
		// A wildcard line pins NOTHING — ssh accepts any key for the pattern — so
		// it is worse than absent: it reads as configured and verifies nothing.
		s.why = fmt.Sprintf("%s is not a usable known_hosts line", HostKeyRef)
	default:
		c.known, s.pinned = strings.TrimSpace(string(hk)), true
	}
	// Carry the warm repository list across the rotation. Without this the
	// credential's lifetime would silently become the read cache's, and one read
	// every [fresh] would pay the full cold-path wait for no reason anyone
	// reading either constant could see.
	c.Reuse(s.c)
	s.c, s.when = c, time.Now()
	return c, nil
}

// Host is the forge a deployment with this API host talks to.
//
// It is the SIBLING of the deployment's own API host, through the one derivation
// of it. A literal "git.hanzo.ai" at a call site is what makes a white-labelled
// deployment (lux.network, zoo.ngo) read another brand's forge.
//
// CLOUD_FORGE_HOST overrides it for the deployment whose forge genuinely is not
// that sibling — a developer box, or a migration running against a staging
// forge. An override, not the source.
//
// Exported because the host is a fact about the forge that outlived the client:
// a receiver of the forge's own deliveries has no client to ask, and it needs
// the host to say where a push arrived FROM. Two derivations of one host is how
// a mirror ends up unable to recognise its own echo.
func Host(domain string) string {
	if h := strings.TrimSpace(os.Getenv("CLOUD_FORGE_HOST")); h != "" {
		return h
	}
	return brand.Sibling(domain, Name)
}

// host is [Host] with this Source's own override taking precedence.
func (s *Source) host(domain string) string {
	if h := strings.TrimSpace(s.Host); h != "" {
		return h
	}
	return Host(domain)
}

// Invalidate drops a held credential the forge has just rejected, so the next
// call re-reads KMS instead of replaying a revoked token for the rest of the
// window.
func (s *Source) Invalidate() {
	s.mu.Lock()
	s.c = nil
	s.mu.Unlock()
}

// ── the process's own client ────────────────────────────────────────────────

// process is THIS process's forge credential, and [Dial] is the one way to
// reach it.
//
// A [Source] per app was the shape while one app had a forge. It is the wrong
// shape now that four do: each one held its own credential, its own refresh
// window and — the part that actually drifted — its own derivation of the
// deployment's domain, which is what decides WHICH FORGE the process talks to.
// Four copies of that is four chances for two apps in one pod to read different
// forges, and the KMS read they each paid for is the one thing worth not
// repeating.
//
// One per process, not one per app: a client is an HTTP client and a token, and
// what a shared one buys is a shared answer to "which forge" and a single KMS
// round trip behind it.
var process Source

// Dial is the deployment's forge client for this process, UNSCOPED.
//
// Unscoped means every call on it refuses ([ErrNoActor]) until the caller states
// who it acts as — [Client.As] for a person, [Client.Machine] for an act of the
// platform that has no person behind it. That is deliberate: there is no path
// here that ends at the machine identity by default.
//
// The KMS client is a PARAMETER because this package must stay reachable from
// any app without linking one: see [Secrets].
func Dial(ctx context.Context, kms Secrets) (*Client, error) {
	return process.Client(ctx, kms, domain())
}

// Pinned reports whether the forge's host key is CONFIGURED rather than learned
// on first use, and why not when it is not — for a composition root to say once
// at startup. See [HostKeyRef].
func Pinned() (bool, string) { return process.Pinned() }

// Invalidate drops this process's held credential, so the next [Dial] re-reads
// KMS rather than replaying a token the forge has just rejected for the rest of
// the freshness window. Without it a revoked or rotated-out token keeps being
// presented for [fresh] after the forge stopped accepting it.
func Invalidate() { process.Invalidate() }

// domain is this deployment's own public API host, from which the forge host is
// derived ([Source.host]). Reading it here rather than at each call site is what
// makes "which forge" one answer: a process that resolved it itself could reach
// another brand's forge while its neighbour in the same pod reached ours.
func domain() string {
	if d := strings.TrimSpace(os.Getenv("CLOUD_DOMAIN")); d != "" {
		return d
	}
	b := strings.TrimSpace(os.Getenv("CLOUD_BRAND"))
	if b == "" {
		b = brand.Default
	}
	return brand.APIHost(b)
}

// ── the org, on this forge ───────────────────────────────────────────────────

// ErrNoOwner means this IAM org has no namespace on the forge. It is a REFUSAL
// and never a fallback to the org's own name — see [Owner].
var ErrNoOwner = errors.New("forge: this org has no namespace on the forge")

// owners maps an IAM org to the org that owns its work ON THE FORGE.
//
// The two names are not the same fact, and this deployment is the proof. The IAM
// tenant is `hanzo`; its work lives under `hanzoai`, which is the name the estate
// writes wherever a namespace is written down — github.com/hanzoai,
// ghcr.io/hanzoai, git.hanzo.ai/hanzoai. Measured on git.hanzo.ai:
//
//	forge org `hanzo`     64 repos, 0 issues, and hanzo/cloud is 404
//	forge org `hanzoai`   250 repos, the actual work, hanzoai/cloud is 200
//
// A NEAR-EMPTY NAMESAKE also exists, which is why mapping by name did not fail
// loudly: the forge answered 200 with an empty list, and an empty answer reads
// as "you have nothing" rather than as "we asked the wrong org". That is the
// whole hazard — a wrong answer that looks like a healthy one.
//
// A declared table rather than a branch inside a resolver: the mapping is a
// VALUE, so it can be read, tested and added to without touching the code that
// applies it.
//
// # It is CLOSED, and that is the whole security property
//
// This table used to fall back to the org's own name, so an IAM org WAS a forge
// coordinate whenever it was not mapped. That is a cross-tenant write, and it
// needs no bug to reach — only a signup. Sign up, create the org `hanzoai`
// (which apps/account's reservedOrgs did not reserve: it reserves the BRAND
// names hanzo/lux/zoo/pars, not the forge namespaces hanzoai/luxfi/zooai), and
// every read and write this package makes for that tenant addresses the
// estate's own repositories. On the coding path that is a write deploy key on
// hanzoai/cloud handed to a pod running model output — and those repositories
// carry Actions workflows on self-hosted in-cluster runners, so it is remote
// code execution reached by filling in a signup form.
//
// So an unmapped org is a REFUSAL. The reachable set of forge namespaces is
// exactly the VALUES here, no IAM org name can ever become one by being spelled
// a certain way, and adding a tenant is a deliberate edit to this table rather
// than a side effect of naming.
var owners = map[string]string{"hanzo": "hanzoai"}

// Owner is the forge org for a VALIDATED IAM org, or [ErrNoOwner].
//
// It is applied to a principal's own org and never to anything a caller sent:
// this decides WHICH ORG is asked about, and a caller-supplied value here would
// be a tenant selecting its own tenancy.
//
// It does not decide WHO the forge answers as. That remains the Sudo actor, so
// the forge's own ACL still decides what comes back, and the two controls stay
// independent.
func Owner(org string) (string, error) {
	if o, ok := owners[strings.ToLower(strings.TrimSpace(org))]; ok {
		return o, nil
	}
	return "", fmt.Errorf("%w: %q", ErrNoOwner, org)
}

// ErrNoOrg means this forge namespace belongs to no IAM org. Like [ErrNoOwner]
// it is a REFUSAL, and never a fallback to the namespace's own name.
var ErrNoOrg = errors.New("forge: this forge namespace belongs to no org")

// orgs is [owners] read the other way — a forge namespace to the IAM org that
// owns it — DERIVED from the same table, so the two directions cannot come to
// disagree by one of them being edited.
//
// A namespace claimed by two IAM orgs has no answer in this direction, and Go
// ranges a map in a different order every run, so inverting one lazily would
// decide tenancy by process start. It is refused here instead, at init, where a
// declared table's contradiction belongs.
var orgs = func() map[string]string {
	m := make(map[string]string, len(owners))
	for org, owner := range owners {
		if prior, dup := m[owner]; dup {
			panic(fmt.Sprintf("forge: namespace %q is claimed by both %q and %q — a push delivered from it names no one org", owner, prior, org))
		}
		m[owner] = org
	}
	return m
}()

// Org is the IAM org that owns a forge namespace, or [ErrNoOrg].
//
// This is the direction a DELIVERY is read in. A signed push names the namespace
// it landed in, and that name decides whose applications rebuild and whose
// compute pays for it — so it is closed for the reason [Owner] is, from the
// other side. Anyone with a forge account can create a namespace; were an
// unmapped one to resolve to the IAM org spelled the same way, choosing which
// tenant rebuilds would take a signup and a push.
func Org(owner string) (string, error) {
	if o, ok := orgs[strings.ToLower(strings.TrimSpace(owner))]; ok {
		return o, nil
	}
	return "", fmt.Errorf("%w: %q", ErrNoOrg, owner)
}
