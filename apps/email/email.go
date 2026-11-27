package email

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"errors"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
	luxlog "github.com/luxfi/log"
)

// Use registers the /v1/email capability on app.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("email: nil app")
	}
	log := luxlog.Default()
	if log != nil {
		log = log.New("subsystem", "email")
	}
	if err := Mount(app, deps.KMS, log); err != nil {
		return err
	}
	trail = deps.Audit
	return nil
}

// Mount wires and binds email verification and delivery onto app.
func Mount(app cloud.Router, kms secret.Store, log luxlog.Logger) error {
	if app == nil {
		return errors.New("email: nil app")
	}

	// Independent verification check engine
	verifier := NewVerifier(nil, 5*time.Second)

	// Independent organization credential resolver
	resolver := NewKMSCredentialResolver(kms)

	// The delivery service, whose ledger is the org's Base
	delivery := NewDeliveryService(resolver, nil, log)

	// HTTP Handlers
	h := NewHandlers(verifier, delivery, log)

	RegisterRoutes(app, h)
	exposeSend(h)

	if log != nil {
		log.Info("email surface mounted", "prefix", "/v1/email")
	}
	return nil
}

// RegisterRoutes binds the routes onto the router.
func RegisterRoutes(app cloud.Router, h *Handlers) {
	g := app.Group("/v1/email")

	// Messages: send, list, count, and read one's delivery status. The static
	// segments below are declared before /:id so they are never read as a message id.
	g.Post("", h.postMessage)
	g.Get("", h.listMessages)
	g.Get("/stats", h.stats)
	g.Put("/provider", h.setProvider)

	// Verify an address with explicitly selected checks.
	g.Post("/verify", h.postVerification)

	// Amazon SES v2 & SendGrid outreach status, DNS records, and event webhook ingestion.
	g.Get("/ses/status", h.getSESStatus)
	g.Get("/sendgrid/status", h.getSendGridStatus)

	// The providers call these, and sign what they send; each receiver answers 401
	// to a delivery it cannot verify and records nothing.
	g.Raw(http.MethodPost, "/webhooks/ses", h.sesWebhook)
	g.Raw(http.MethodPost, "/webhooks/sendgrid", h.sendGridWebhook)

	g.Get("/:id", h.getMessage)
}
