// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// ucp.go — the business side of the Universal Commerce Protocol (release
// 2026-08-25), served by github.com/hanzoai/commerce/agentic/ucp over ucp.Commerce:
// the Checkout capability over REST, and the Order capability when the operator
// advertises it. A line's item id is the catalog's (credit-<cents> or a plan's slug),
// one of it, paid with a Stripe shared payment token through the payment handler the
// operator names. See agentic.go for when it mounts and who is buying.
//
//	POST /v1/commerce/ucp/checkout-sessions                 create_checkout
//	GET  /v1/commerce/ucp/checkout-sessions/{id}            get_checkout
//	PUT  /v1/commerce/ucp/checkout-sessions/{id}            update_checkout
//	POST /v1/commerce/ucp/checkout-sessions/{id}/complete   complete_checkout (charges)
//	POST /v1/commerce/ucp/checkout-sessions/{id}/cancel     cancel_checkout
//	GET  /v1/commerce/ucp/orders/{id}                       get_order (with the Order capability)
//	GET  /.well-known/ucp                                   the business profile
//
// Every response is signed (RFC 9421) with the business key the operator keeps in KMS,
// and its public half is in the profile. A request signature is verified against the
// keys of the platform profile its UCP-Agent names, fetched over https with no
// redirects and no special-use address, and only for a platform in the operator's
// registry.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/commerce/agentic/ucp"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ucpFile is the file CLOUD_UCP_CONFIG names. Every field is required unless it says
// it is optional; none has a default.
type ucpFile struct {
	// Origin is the https origin agents reach this deployment at. The REST endpoint is
	// Origin + /v1/commerce/ucp and the profile Origin + /.well-known/ucp.
	Origin string `json:"origin"`
	// Handlers are the payment handlers the profile advertises, keyed by reverse-domain
	// handler name, as schemas/payment_handler.json states one.
	Handlers map[string][]ucp.Handler `json:"handlers"`
	// Handler is the id, among Handlers, of the instance whose instruments carry the
	// Stripe shared payment token the checkout is charged with.
	Handler string `json:"handler"`
	// Order is the buyer's page of an order: an https URL holding {id}.
	Order string `json:"order"`
	// Continue is where a buyer finishes a checkout their bank asked to authenticate:
	// an https URL holding {id}. Optional; without it that case is a recoverable
	// payment_failed.
	Continue string `json:"continue,omitempty"`
	// Orders advertises the Order capability (get_order).
	Orders bool `json:"orders"`
	// SigningKey is the business key responses and webhooks are signed with: its key
	// id in the profile, and the KMS reference of its PKCS#8 PEM (ECDSA P-256 or
	// P-384, or Ed25519).
	SigningKey struct {
		KID    string `json:"kid"`
		Secret string `json:"secret"`
	} `json:"signingKey"`
	// Trusted is the registry of platforms this business deals with: the profile URLs
	// a UCP-Agent may name. Any other is refused profile_not_trusted before its
	// profile is fetched.
	Trusted []string `json:"trusted"`
	// RequireSignature refuses a request that carries no signature.
	RequireSignature bool `json:"requireSignature"`
	// Signatures bound a request signature: the oldest created time accepted, and the
	// clock difference tolerated.
	Signatures struct {
		WindowSeconds int `json:"windowSeconds"`
		SkewSeconds   int `json:"skewSeconds"`
	} `json:"signatures"`
	// Fetch bounds the reading of platform profiles: the timeout of one fetch, the
	// profiles cached, the longest a profile is cached (at least 60 seconds), and the
	// fetches in flight.
	Fetch struct {
		TimeoutSeconds int `json:"timeoutSeconds"`
		Entries        int `json:"entries"`
		MaxTTLSeconds  int `json:"maxTtlSeconds"`
		Concurrent     int `json:"concurrent"`
	} `json:"fetch"`
	// MaxBodyBytes bounds a request body.
	MaxBodyBytes int64 `json:"maxBodyBytes"`
	// Storefront is the https URL a negotiation failure hands a buyer to. Optional.
	Storefront string `json:"storefront,omitempty"`
	// Links are shown with every checkout: privacy policy, terms of service. Optional.
	Links []ucp.Link `json:"links,omitempty"`
	// Webhooks, with Orders, announces each order placed to the platforms that
	// negotiated the Order capability: deliveries tried, the wait after the first
	// failure (doubling), and the timeout of one delivery. Optional.
	Webhooks *struct {
		Attempts       int `json:"attempts"`
		BackoffSeconds int `json:"backoffSeconds"`
		TimeoutSeconds int `json:"timeoutSeconds"`
	} `json:"webhooks,omitempty"`
}

// ucpService is the configured server.
type ucpService struct{ server *ucp.Server }

// newUCP builds the server raw describes, reading the business key through secrets.
func newUCP(ctx context.Context, raw []byte, secrets secretReader, store ucp.Store, fault func(op string, err error)) (*ucpService, error) {
	var f ucpFile
	if err := decodeStrict(raw, &f); err != nil {
		return nil, err
	}
	base, err := origin("origin", f.Origin)
	if err != nil {
		return nil, err
	}
	permalink, err := template("order", f.Order)
	if err != nil {
		return nil, err
	}
	var cont func(string) string
	if f.Continue != "" {
		if cont, err = template("continue", f.Continue); err != nil {
			return nil, err
		}
	}
	if !offers(f.Handlers, f.Handler) {
		return nil, fmt.Errorf("handler %q is not the id of any instance in handlers", f.Handler)
	}
	if len(f.Trusted) == 0 {
		return nil, errors.New("trusted names no platform profile: list the platforms this business deals with")
	}
	for i, t := range f.Trusted {
		if err := httpsURL(fmt.Sprintf("trusted[%d]", i), t); err != nil {
			return nil, err
		}
	}
	switch {
	case f.Signatures.WindowSeconds <= 0 || f.Signatures.SkewSeconds <= 0:
		return nil, errors.New("signatures needs a positive windowSeconds and skewSeconds")
	case f.Fetch.TimeoutSeconds <= 0:
		return nil, errors.New("fetch.timeoutSeconds must be positive")
	case f.MaxBodyBytes <= 0:
		return nil, errors.New("maxBodyBytes must be positive")
	case f.SigningKey.KID == "":
		return nil, errors.New("signingKey.kid is required")
	case f.Webhooks != nil && !f.Orders:
		return nil, errors.New("webhooks announce orders, so they need orders: true")
	case f.Webhooks != nil && (f.Webhooks.Attempts <= 0 || f.Webhooks.BackoffSeconds <= 0 || f.Webhooks.TimeoutSeconds <= 0):
		return nil, errors.New("webhooks needs positive attempts, backoffSeconds and timeoutSeconds")
	}
	for i, l := range f.Links {
		if l.Type == "" {
			return nil, fmt.Errorf("links[%d] needs a type", i)
		}
		if err := httpsURL(fmt.Sprintf("links[%d].url", i), l.URL); err != nil {
			return nil, err
		}
	}
	key, err := signingKey(ctx, secrets, f.SigningKey.Secret)
	if err != nil {
		return nil, err
	}
	pub, err := ucp.KeyOf(f.SigningKey.KID, key.Public())
	if err != nil {
		return nil, err
	}
	signer := &ucp.Signer{KeyID: f.SigningKey.KID, Key: key}

	profile, err := ucp.NewProfile(ucp.ProfileConfig{Endpoint: base + ucpBase, Handlers: f.Handlers, Keys: []ucp.Key{pub}, Order: f.Orders})
	if err != nil {
		return nil, err
	}
	fetcher, err := ucp.NewFetcher(ucp.FetcherConfig{
		Client:     ucp.NewClient(seconds(f.Fetch.TimeoutSeconds)),
		Entries:    f.Fetch.Entries,
		MaxTTL:     seconds(f.Fetch.MaxTTLSeconds),
		Concurrent: f.Fetch.Concurrent,
	})
	if err != nil {
		return nil, err
	}
	backend, err := ucp.NewCommerce(ucp.Commerce{Handler: f.Handler, Permalink: permalink, Continue: cont})
	if err != nil {
		return nil, err
	}
	cfg := ucp.Config{
		Profile: profile,
		Backend: backend,
		Identify: func(r *http.Request) (ucp.Caller, error) {
			b, ok := buyerOf(r)
			if !ok {
				return ucp.Caller{}, ucp.ErrUnauthorized
			}
			return ucp.Caller{Org: b.org, Subject: b.subject, Host: b.host}, nil
		},
		Profiles: fetcher,
		Trusted:  func(p string) bool { return slices.Contains(f.Trusted, p) },
		Verifier: &ucp.Verifier{
			Keys:      ucp.AgentKeys{Profiles: fetcher},
			Authority: authorityOf,
			Window:    seconds(f.Signatures.WindowSeconds),
			Skew:      seconds(f.Signatures.SkewSeconds),
		},
		Require:    f.RequireSignature,
		Signer:     signer,
		Store:      store,
		Links:      f.Links,
		Storefront: f.Storefront,
		MaxBody:    f.MaxBodyBytes,
		Fault:      fault,
	}
	if w := f.Webhooks; w != nil {
		cfg.Webhooks = &ucp.Webhooks{
			Signer:   signer,
			Agent:    base + ucpWellKnown,
			Client:   ucp.NewClient(seconds(w.TimeoutSeconds)),
			Attempts: w.Attempts,
			Backoff:  seconds(w.BackoffSeconds),
		}
	}
	srv, err := ucp.New(cfg)
	if err != nil {
		return nil, err
	}
	return &ucpService{server: srv}, nil
}

// offers reports whether id names an instance among the handlers.
func offers(handlers map[string][]ucp.Handler, id string) bool {
	for _, hs := range handlers {
		for _, h := range hs {
			if id != "" && h.ID == id {
				return true
			}
		}
	}
	return false
}

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }

const (
	// ucpSweepInterval is how often the elected leader sweeps expired UCP
	// idempotency records (older than ucp.Keep, 48h).
	ucpSweepInterval = time.Hour
	// ucpSweepBatch bounds records deleted per tick so a backlog drains steadily
	// without holding the datastore.
	ucpSweepBatch = 500
	// ucpSweepTimeout bounds one sweep tick.
	ucpSweepTimeout = 30 * time.Second
)

type ucpSweeper interface {
	Sweep(ctx context.Context, limit int) (int, error)
}

// sweepUCPOnce drains one batch of expired UCP idempotency rows under leader
// election. Safe with hanzoai/commerce >= v1.50.251 (#83), where Sweep reads
// only scope "ucp" and leaves billing webhook guards and other ledgers intact.
func sweepUCPOnce(ctx context.Context, lead *coord.Leader, lg luxlog.Logger, store ucpSweeper, limit int) {
	run := func(ctx context.Context) {
		tctx, cancel := context.WithTimeout(ctx, ucpSweepTimeout)
		defer cancel()
		n, err := store.Sweep(tctx, limit)
		if err != nil {
			lg.Warn("ucp idempotency sweep failed", "err", err)
			return
		}
		if n > 0 {
			lg.Info("ucp idempotency sweep deleted expired records", "deleted", n)
		}
	}
	if lead != nil {
		_ = lead.Do(ctx, run)
		return
	}
	run(ctx)
}

// startUCPSweep runs sweepUCPOnce every interval until ctx is cancelled.
func startUCPSweep(ctx context.Context, lead *coord.Leader, lg luxlog.Logger, store ucpSweeper, interval time.Duration, limit int) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweepUCPOnce(ctx, lead, lg, store, limit)
			}
		}
	}()
}

// mountUCP mounts UCP from the operator's file, over commerce's checkout and its
// durable idempotency store, and schedules the leader-elected sweep of expired
// UCP idempotency rows.
func mountUCP(app *zip.App, lg luxlog.Logger, secrets secretReader, raw []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := ucp.Idempotency{}
	s, err := newUCP(ctx, raw, secrets, store, func(op string, err error) { lg.Warn("ucp", "op", op, "err", err) })
	if err != nil {
		return err
	}
	s.mount(app)
	startUCPSweep(context.Background(), coord.Default().Lead("commerce:ucp-sweep"), lg, store, ucpSweepInterval, ucpSweepBatch)
	return nil
}

// mount registers the REST binding under commerce's root, where the profile's
// endpoint says it is, and the profile at the origin. Every method reaches the
// server, which answers 405 itself.
func (s *ucpService) mount(app *zip.App) {
	app.Group(ucpBase).Raw(zip.MethodAll, "/*", withBuyer(s.server))
	app.Group("/.well-known").Raw(zip.MethodAll, "/ucp", zip.AdaptNetHTTP(s.server.Profile()))
}
