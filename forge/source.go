package forge

// source.go is how a process GETS a client, and it is the only way.
//
// It moved here from the one app that first needed a forge (apps/tracker). That
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
		return nil, fmt.Errorf("no KMS client mounted: cannot read %s", TokenRef)
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
	// Carry the warm repository list across the rotation. Without this the
	// credential's lifetime would silently become the read cache's, and one read
	// every [fresh] would pay the full cold-path wait for no reason anyone
	// reading either constant could see.
	c.Reuse(s.c)
	s.c, s.when = c, time.Now()
	return c, nil
}

// host is the forge this deployment talks to.
//
// It is the SIBLING of the deployment's own API host, through the one derivation
// of it. A literal "git.hanzo.ai" here is what makes a white-labelled deployment
// (lux.network, zoo.ngo) read another brand's forge.
//
// CLOUD_FORGE_HOST overrides it for the deployment whose forge genuinely is not
// that sibling — a developer box, or a migration running against a staging
// forge. An override, not the source.
func (s *Source) host(domain string) string {
	if h := strings.TrimSpace(s.Host); h != "" {
		return h
	}
	if h := strings.TrimSpace(os.Getenv("CLOUD_FORGE_HOST")); h != "" {
		return h
	}
	return brand.Sibling(domain, Name)
}

// Invalidate drops a held credential the forge has just rejected, so the next
// call re-reads KMS instead of replaying a revoked token for the rest of the
// window.
func (s *Source) Invalidate() {
	s.mu.Lock()
	s.c = nil
	s.mu.Unlock()
}

// ── the org, on this forge ───────────────────────────────────────────────────

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
// applies it. Identity by default, so a tenant whose two names already agree
// needs no entry.
var owners = map[string]string{"hanzo": "hanzoai"}

// Owner is the forge org for a VALIDATED IAM org.
//
// It is applied to a principal's own org and never to anything a caller sent:
// this decides WHICH ORG is asked about, and a caller-supplied value here would
// be a tenant selecting its own tenancy.
//
// It does not touch WHO the forge answers as. That remains the Sudo actor, so
// the forge's own ACL still decides what comes back — which means a wrong entry
// in this table can show a user an empty answer, but cannot show them anything
// they are not entitled to see. The two controls stay independent.
func Owner(org string) string {
	if o, ok := owners[strings.ToLower(strings.TrimSpace(org))]; ok {
		return o
	}
	return org
}
