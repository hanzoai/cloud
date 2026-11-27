package seo

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/internal/cluster"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
)

// The KMS coordinates of the DataForSEO account, in the flat-ref grammar
// apps/kms parseRef reads: "orgs/<org>/<path>/<name>@<env>".
//
// THE ORG PREFIX IS LOAD-BEARING and is not decoration. It picks the store FILE:
// a bare "seo/NAME" resolves to path "/seo", which the store treats as the
// deployment facade and lands in the _platform partition, while the REST endpoint
// that provisioned these folded the caller's org and landed them under
// /orgs/hanzo. Those are two different databases, and reading through the wrong
// one is not an error — it is a secret that is simply not there.
//
// The env is pinned to prod EXPLICITLY. An omitted env silently becomes
// "default", and a store full of prod records answering a default read with
// nothing is an outage that looks like an empty account.
//
// One account serves every tenant — this is a resale, not per-tenant custody —
// so the ref carries no caller in it and cannot be influenced by one.
const (
	loginRef    = "orgs/" + cluster.Platform + "/seo/DATAFORSEO_LOGIN@prod"
	passwordRef = "orgs/" + cluster.Platform + "/seo/DATAFORSEO_PASSWORD@prod"
)

// How long a resolved credential and a fetched price list stay usable, and how
// soon each is asked for again after it could not be got.
//
// A credential is re-read every five minutes so a rotation lands without a
// restart; the read is a local store open, so the window could be shorter at no
// cost and five minutes is simply short enough. Its failure is remembered for
// only a second, because a store that just came back should serve the very next
// request rather than 503 for five minutes.
//
// A price list is re-read hourly because fetching it is a network hop, it is
// free, and a vendor price does not move within the hour — longer would make
// "their price moves ours" a claim with a day attached to it. Its failure is
// remembered for a minute: long enough that a dead upstream is asked once rather
// than once per request, short enough that a recovered one is quoted again
// promptly.
const (
	credLife  = 5 * time.Minute
	credRetry = time.Second
	cardLife  = time.Hour
	cardRetry = time.Minute
)

// fresh is a value worth keeping between calls: it ages out, and when it does
// exactly ONE caller refills it while the rest wait for that answer instead of
// starting their own. A lock dropped before the refill lets every concurrent
// caller miss the same cache and stampede the same upstream, which is the failure
// a cache exists to prevent.
//
// A FAILURE IS REMEMBERED TOO, for its own shorter while. Without that, holding
// the lock across the refill turns a broken upstream into a self-inflicted
// outage: every request in turn takes the lock, finds nothing kept, and waits out
// the whole timeout, so a hundred callers serialise into a hundred timeouts.
// Remembering the failure makes them share one. The two windows are separate
// because they answer different questions — how long an answer stays true, and
// how soon it is worth asking again — and one window would either re-ask a dead
// upstream on every request or leave a recovered one unasked for an hour.
//
// One mechanism, two uses — the credential and the price list. They keep their own
// locks because their refills call each other: fetching the price list needs the
// credential, and one lock over both would be one goroutine waiting for itself.
type fresh[T any] struct {
	life  time.Duration
	retry time.Duration

	mu  sync.Mutex
	v   T
	err error
	at  time.Time
}

func (f *fresh[T]) get(refill func() (T, error)) (T, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.at.IsZero() && time.Since(f.at) < f.window() {
		return f.v, f.err
	}
	f.v, f.err = refill()
	f.at = time.Now()
	return f.v, f.err
}

// window is how long what is already held stays in force: its life when it is an
// answer, the retry pause when it is a failure.
func (f *fresh[T]) window() time.Duration {
	if f.err != nil {
		return f.retry
	}
	return f.life
}

// pair is the vendor's HTTP Basic credential, kept together because neither half
// is usable alone and a cache holding one of them is a cache that can serve half a
// login.
type pair struct{ login, password string }

// state is everything the six ops share: where the vendor is, how to reach it,
// where secrets come from, and the two things worth remembering between calls.
type state struct {
	// base is the vendor's API root. It is a VALUE and not a package constant so a
	// suite can stand a stub in front of every op — which is the only way to
	// exercise a surface whose real upstream charges money for each call. It is
	// deliberately NOT read from the environment: where this package sends a
	// credential is not a deployment setting.
	base string
	http *http.Client
	kms  secret.Store

	cred fresh[pair]
	card fresh[map[string]charge]
}

// Mount registers the six ops and the rate card at /v1/seo.
//
// EVERY INHERITED CAPABILITY IS NAMED HERE:
//
//	IAM auth    SanitizeIdentity mints the verified org upstream (serve.go). This
//	            package never validates a token and never can.
//	principal   cloud.Bridge, which the composer installs, parks the validated
//	            principal on the context. Every op reads it and refuses without
//	            it — see run().
//	KMS         deps.KMS, used as the INTERFACE. Exactly one process holds the
//	            sealed store, so this one is handed a peer over the internal
//	            plane; a type assertion to the embedded client would yield nil
//	            here and every credential read would fail closed while blaming a
//	            master key that is correctly configured.
//	metering    cloud.NewBase gives the per-org Meter. The surface is
//	            declared Metered (plugin/seo/main.go), which means the edge
//	            charges nothing and the debit below is the whole charge — a
//	            promise this package keeps in run().
//	logs        cloud.NewBase gives the scoped luxlog.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "seo", build, routes)
}

func build(b cloud.Base) (*state, error) {
	if b.KMS == nil {
		return nil, fmt.Errorf("no KMS client: the vendor credential at %s could never be read", loginRef)
	}
	return &state{
		base: vendor,
		http: &http.Client{Timeout: callTimeout},
		kms:  b.KMS,
		cred: fresh[pair]{life: credLife, retry: credRetry},
		card: fresh[map[string]charge]{life: cardLife, retry: cardRetry},
	}, nil
}

// account resolves the vendor's basic-auth pair from KMS.
//
// FAIL CLOSED, ALWAYS. No store, a store that will not answer, or an empty record
// each return an error and never a credential — a blank pair would reach the
// vendor as an anonymous request, come back 401, and read as the vendor being
// down.
//
// The error names the REF and never the value.
//
// The pair is the platform's and one read serves every tenant, so the read acts
// for the org its refs name, on a context of its own: the store serves
// orgs/<org>/… only to a call acting for <org> (apps/kms attest.go admit), and a
// request's own identity outranks one stated on its context (zip.CallerOf). ctx
// still bounds the read.
func (s *state) account(ctx context.Context) (pair, error) {
	return s.cred.get(func() (pair, error) {
		rctx, cancel := context.WithCancel(cloud.For(context.Background(), cluster.Platform))
		defer cancel()
		defer context.AfterFunc(ctx, cancel)()
		login, err := s.secret(rctx, loginRef)
		if err != nil {
			return pair{}, err
		}
		password, err := s.secret(rctx, passwordRef)
		if err != nil {
			return pair{}, err
		}
		return pair{login, password}, nil
	})
}

func (s *state) secret(ctx context.Context, ref string) (string, error) {
	if s.kms == nil {
		return "", fmt.Errorf("no KMS client: cannot read %s", ref)
	}
	b, err := s.kms.GetSecret(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", ref, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", ref)
	}
	return v, nil
}
