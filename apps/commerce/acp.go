// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// acp.go — the seller side of the Agentic Commerce Protocol (API 2026-04-17), served
// by github.com/hanzoai/commerce/agentic/acp over acp.Commerce: one unit of one
// catalog item (a credit pack, credit-<cents>, or a plan's slug) per checkout, in USD,
// paid with the Stripe shared payment token the buyer granted the agent. See
// agentic.go for when it mounts and who is buying.
//
//	POST /v1/commerce/acp/checkout_sessions                 create
//	GET  /v1/commerce/acp/checkout_sessions/{id}            retrieve
//	POST /v1/commerce/acp/checkout_sessions/{id}            update
//	POST /v1/commerce/acp/checkout_sessions/{id}/complete   complete (charges)
//	POST /v1/commerce/acp/checkout_sessions/{id}/cancel     cancel
//	GET  /.well-known/acp.json                              discovery
//
// Not wired: request signatures (the spec leaves the algorithm and keys out of band,
// so a Verifier is the operator's to name), and the order webhook (Events).

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hanzoai/commerce/agentic/acp"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// acpFile is the file CLOUD_ACP_CONFIG names. Every field is required unless it says
// it is optional; none has a default.
type acpFile struct {
	// Origin is the https origin agents reach this deployment at. The API base the
	// discovery document names is Origin + /v1/commerce/acp.
	Origin string `json:"origin"`
	// Handler is the tokenized-card payment handler every session declares: the id
	// agents name in payment_data.handler_id, the payment service provider, and the
	// merchant's account there.
	Handler struct {
		ID         string `json:"id"`
		PSP        string `json:"psp"`
		MerchantID string `json:"merchantId"`
	} `json:"handler"`
	// Order is the buyer's page of an order: an https URL holding {id}.
	Order string `json:"order"`
	// RetryAfterSeconds is the Retry-After of an idempotency_in_flight answer.
	RetryAfterSeconds int `json:"retryAfterSeconds"`
	// MaxBodyBytes bounds a request body.
	MaxBodyBytes int64 `json:"maxBodyBytes"`
	// Links are the policy and support URLs a session lists. Optional.
	Links []acp.Link `json:"links,omitempty"`
	// Documentation is the documentation URL the discovery document names. Optional.
	Documentation string `json:"documentation,omitempty"`
	// Locales are the locales the discovery document names. Optional.
	Locales []string `json:"locales,omitempty"`
}

// acpService is the configured service and its discovery document.
type acpService struct {
	api       http.Handler
	discovery http.Handler
}

// newACP builds the service raw describes. backend is acp.Commerce in production.
func newACP(raw []byte, backend acp.Backend, ledger acp.Ledger, report func(error)) (*acpService, error) {
	var f acpFile
	if err := decodeStrict(raw, &f); err != nil {
		return nil, err
	}
	base, err := origin("origin", f.Origin)
	if err != nil {
		return nil, err
	}
	switch {
	case f.Handler.ID == "" || f.Handler.MerchantID == "":
		return nil, errors.New("handler needs an id and a merchantId")
	case f.Handler.PSP != "stripe":
		// acp.Commerce charges Stripe shared payment tokens and refuses every other
		// handler at completion; declaring one would offer a payment that always fails.
		return nil, fmt.Errorf("handler.psp is %q; the commerce backend charges Stripe shared payment tokens, so it must be \"stripe\"", f.Handler.PSP)
	case f.RetryAfterSeconds <= 0:
		return nil, errors.New("retryAfterSeconds must be positive")
	case f.MaxBodyBytes <= 0:
		return nil, errors.New("maxBodyBytes must be positive")
	}
	order, err := template("order", f.Order)
	if err != nil {
		return nil, err
	}
	for i, l := range f.Links {
		if l.Type == "" {
			return nil, fmt.Errorf("links[%d] needs a type", i)
		}
		if err := httpsURL(fmt.Sprintf("links[%d].url", i), l.URL); err != nil {
			return nil, err
		}
	}
	if f.Documentation != "" {
		if err := httpsURL("documentation", f.Documentation); err != nil {
			return nil, err
		}
	}
	handler, err := acp.TokenizedCard(f.Handler.ID, f.Handler.PSP, map[string]any{"merchant_id": f.Handler.MerchantID})
	if err != nil {
		return nil, err
	}
	svc, err := acp.New(acp.Config{
		Backend: backend,
		Identify: func(r *http.Request) (acp.Caller, error) {
			b, ok := buyerOf(r)
			if !ok {
				return acp.Caller{}, errNoBuyer
			}
			return acp.Caller{Org: b.org, Subject: b.subject, Host: b.host}, nil
		},
		Handlers: func(context.Context, acp.Caller) ([]acp.PaymentHandler, error) {
			return []acp.PaymentHandler{handler}, nil
		},
		Order:      order,
		Links:      f.Links,
		Ledger:     ledger,
		RetryAfter: f.RetryAfterSeconds,
		Report:     report,
	})
	if err != nil {
		return nil, err
	}
	disc, err := svc.Discover(base+acpBase, f.Documentation, f.Locales)
	if err != nil {
		return nil, err
	}
	return &acpService{
		// The service answers paths relative to its base, so the mount prefix is
		// stripped; its Signature check reads the request target, which keeps it.
		api:       http.StripPrefix(acpBase, http.MaxBytesHandler(svc.API(), f.MaxBodyBytes)),
		discovery: disc,
	}, nil
}

// mountACP mounts ACP from the operator's file, over commerce's checkout and its
// durable idempotency ledger.
func mountACP(app *zip.App, lg luxlog.Logger, _ secretReader, raw []byte) error {
	s, err := newACP(raw, acp.Commerce{}, acp.Durable{}, func(err error) { lg.Warn("acp", "err", err) })
	if err != nil {
		return err
	}
	s.mount(app)
	return nil
}

// mount registers the API under commerce's root and the discovery document at the
// origin. Every method reaches the service, which answers 405 itself.
func (s *acpService) mount(app *zip.App) {
	app.Group(acpBase).Raw(zip.MethodAll, "/*", withBuyer(s.api))
	app.Group("/.well-known").Raw(zip.MethodAll, "/acp.json", zip.AdaptNetHTTP(s.discovery))
}
