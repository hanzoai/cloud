// Package giftcard is the HTTP surface for gift cards: base CRUD via rest plus
// money actions (redeem / balance / void). Every handler is tenant-scoped via
// middleware.GetOrganization(c) + org.Namespaced(c) — the same isolation the
// rest of the /v1 surface uses — so a caller only ever touches its own org's
// gift cards.
//
// Money actions are idempotent (see models/giftcard.Redeem): a redeem carries a
// caller idempotency key (X-Idempotency-Key header or body.idempotencyKey) and
// re-submitting it debits once, never twice.
package giftcard

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	giftcardModel "github.com/hanzoai/cloud/clients/commerce/models/giftcard"
	"github.com/hanzoai/cloud/clients/commerce/models/giftcardredemption"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	api := rest.New(giftcardModel.GiftCard{})
	// The model kind is "gift-card", so the generated base-CRUD id param would be
	// ":gift-cardid" — but the money subroutes below, getCard, and every caller use
	// ":giftcardid". Gin panics on that same-position wildcard-name mismatch
	// (":giftcardid" vs ":gift-cardid") the moment both are registered on the
	// /gift-card group, taking down the whole router. Pin the CRUD param to
	// "giftcardid" so base + subroutes agree (external URL /gift-card/<id>/… is
	// unchanged — the param name is internal to ByName()).
	api.ParamId = "giftcardid"
	api.POST("/:giftcardid/redeem", namespaced, Redeem)
	api.POST("/:giftcardid/void", namespaced, Void)
	api.GET("/:giftcardid/balance", namespaced, Balance)
	api.GET("/:giftcardid/redemptions", namespaced, ListRedemptions)
	api.Route(router, args...)

	// The redemption ledger is also exposed read-only for audit/reconciliation.
	rest.New(giftcardredemption.GiftCardRedemption{}).Route(router, args...)
}

// getCard loads the org-scoped gift card named in the path, or writes a 404 and
// returns nil.
func getCard(c *gin.Context) (*datastore.Datastore, *giftcardModel.GiftCard) {
	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))
	id := c.Params.ByName("giftcardid")
	g := giftcardModel.New(db)
	if err := g.GetById(id); err != nil {
		http.Fail(c, 404, "No gift card found with id: "+id, err)
		return nil, nil
	}
	return db, g
}

type redeemRequest struct {
	AmountCents    currency.Cents `json:"amountCents"`
	Currency       currency.Type  `json:"currency"`
	OrderId        string         `json:"orderId"`
	IdempotencyKey string         `json:"idempotencyKey"`
}

type redeemResponse struct {
	Redemption   *giftcardredemption.GiftCardRedemption `json:"redemption"`
	BalanceCents currency.Cents                         `json:"balanceCents"`
	GiftCardId   string                                 `json:"giftCardId"`
}

// Redeem draws amountCents from the card. Idempotent on the idempotency key.
// Admin-only: redeeming moves money off a card, so it is gated INSIDE the
// handler (the route middleware no-ops on the IAM path — Red HIGH-4).
func Redeem(c *gin.Context) {
	if !middleware.RequireAdmin(c) {
		return
	}
	db, g := getCard(c)
	if g == nil {
		return
	}

	var req redeemRequest
	if err := json.Decode(c.Request.Body, &req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	// Idempotency key precedence: explicit body field, else the standard header.
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		key = strings.TrimSpace(c.GetHeader("X-Idempotency-Key"))
	}

	line, bal, err := giftcardModel.Redeem(db, g, req.AmountCents, req.Currency, req.OrderId, key)
	if err != nil {
		http.Fail(c, redeemStatus(err), err.Error(), err)
		return
	}

	http.Render(c, 200, redeemResponse{Redemption: line, BalanceCents: bal, GiftCardId: g.Id()})
}

// redeemStatus maps money errors to HTTP codes. Client-correctable errors are
// 4xx; nothing here 500s (a 500 would suggest we moved money then failed).
func redeemStatus(err error) int {
	switch {
	case errors.Is(err, giftcardModel.ErrInsufficientFunds):
		return 402 // Payment Required
	case errors.Is(err, giftcardModel.ErrNotRedeemable),
		errors.Is(err, giftcardModel.ErrNonPositiveAmount),
		errors.Is(err, giftcardModel.ErrMissingIdempotency),
		errors.Is(err, giftcardModel.ErrCurrencyMismatch):
		return 400
	default:
		return 500
	}
}

type voidRequest struct {
	RedemptionId string `json:"redemptionId"`
}

// Void reverses a prior redemption (append-only compensating line). Idempotent.
// Admin-only (money move) — gated inside the handler like Redeem.
func Void(c *gin.Context) {
	if !middleware.RequireAdmin(c) {
		return
	}
	db, g := getCard(c)
	if g == nil {
		return
	}

	var req voidRequest
	if err := json.Decode(c.Request.Body, &req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}
	if strings.TrimSpace(req.RedemptionId) == "" {
		http.Fail(c, 400, "redemptionId is required", errors.New("missing redemptionId"))
		return
	}

	bal, err := giftcardModel.Void(db, g, req.RedemptionId)
	if err != nil {
		if errors.Is(err, datastore.ErrNoSuchEntity) {
			http.Fail(c, 404, "No redemption found with id: "+req.RedemptionId, err)
			return
		}
		http.Fail(c, 500, "Failed to void redemption", err)
		return
	}

	http.Render(c, 200, map[string]any{"balanceCents": bal, "giftCardId": g.Id()})
}

// Balance returns the projected spendable balance (initial − Σ redemptions).
func Balance(c *gin.Context) {
	db, g := getCard(c)
	if g == nil {
		return
	}
	bal, err := giftcardModel.BalanceCents(db, g)
	if err != nil {
		http.Fail(c, 500, "Failed to compute balance", err)
		return
	}
	http.Render(c, 200, map[string]any{
		"giftCardId":          g.Id(),
		"code":                g.Code,
		"currency":            g.Currency,
		"initialBalanceCents": g.InitialBalanceCents,
		"balanceCents":        bal,
	})
}

// ListRedemptions returns the append-only debit ledger for a card.
func ListRedemptions(c *gin.Context) {
	db, g := getCard(c)
	if g == nil {
		return
	}
	lines := make([]*giftcardredemption.GiftCardRedemption, 0, 16)
	if _, err := giftcardredemption.Query(db).Filter("GiftCardId=", g.Id()).GetAll(&lines); err != nil {
		http.Fail(c, 500, "Failed to list redemptions", err)
		return
	}
	http.Render(c, 200, lines)
}
