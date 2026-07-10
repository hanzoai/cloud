package checkout

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/api/checkout/ethereum"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/wire"
	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/idempotencykey"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/kms"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

var orderEndpoint = config.UrlFor("api", "/order/")

// authedOrg returns the authenticated caller's organization — set by the route's
// TokenRequired branch (service token / legacy org-bound token) or the IAM
// identity middleware — with its payment credentials hydrated from KMS. It is the
// ONE org source for every checkout money handler: the org is always the
// validated principal's, never client-supplied. Returns ok=false when no
// authenticated org is present so callers fail closed (401).
func authedOrg(c *gin.Context) (*organization.Organization, bool) {
	org, ok := middleware.GetOrganizationOK(c)
	if !ok || org == nil {
		return nil, false
	}

	// Hydrate payment credentials from KMS.
	if v, ok := c.Get("kms"); ok {
		if kmsClient, ok := v.(*kms.CachedClient); ok {
			if err := kms.Hydrate(kmsClient, org); err != nil {
				log.Error("KMS hydration failed for org %q: %v", org.Name, err, c)
			}
		}
	}
	return org, true
}

func getOrganizationAndOrder(c *gin.Context) (*organization.Organization, *order.Order, error) {
	// Org is the authenticated principal's (fail closed if the auth middleware
	// set none) — never client-supplied.
	org, ok := authedOrg(c)
	if !ok {
		err := errors.New("no authenticated organization")
		http.Fail(c, 401, "Authentication required", err)
		return nil, nil, err
	}

	// Set up the db with the namespaced context. NewNamespaced routes to the
	// caller org's OWN physical store (per-org SQLite via db.Manager.Org) so an
	// order — and every money move on it — is physically isolated per tenant,
	// never the shared systemDB/Postgres pool (Red CRIT-2).
	ctx := org.Namespaced(c)
	db := datastore.NewNamespaced(ctx)

	// Create order that's properly namespaced
	ord := order.New(db)

	// Get order if an existing order was referenced
	if id := c.Params.ByName("orderid"); id != "" {
		if err := ord.GetById(id); err != nil {
			http.Fail(c, 404, "Failed to retrieve order", OrderDoesNotExist)
			return nil, nil, OrderDoesNotExist
		}
	}

	return org, ord, nil
}

func Authorize(c *gin.Context) {
	org, ord, err := getOrganizationAndOrder(c)
	if err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	if _, err = authorize(c, org, ord); err != nil {
		log.Error("Error %v %v", err.Error(), err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	log.JSON(ord)
	c.Writer.Header().Add("Location", orderEndpoint+ord.Id())
	log.JSON(ord)
	http.Render(c, 200, ord)
}

func Capture(c *gin.Context) {
	org, ord, err := getOrganizationAndOrder(c)
	if err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	if err = capture(c, org, ord); err != nil {
		log.Error("Error during capture %v", err, c)
		http.Fail(c, 400, "Error during capture", err)
		return
	}

	c.Writer.Header().Add("Location", orderEndpoint+ord.Id())
	http.Render(c, 200, ord)
}

func Charge(c *gin.Context) {
	org, ord, err := getOrganizationAndOrder(c)
	if err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	// Do authorization
	if _, err = authorize(c, org, ord); err != nil {
		log.Error("Error %v %v", err.Error(), err, c)
		http.Fail(c, 400, "Error during authorize", err)
		return
	}

	// Do capture using order from authorization
	if err = capture(c, org, ord); err != nil {
		log.Error("Error during capture %v", err, c)
		http.Fail(c, 400, "Error during capture", err)
		return
	}

	c.Writer.Header().Add("Location", orderEndpoint+ord.Id())
	http.Render(c, 200, ord)
}

// refundLocks serializes refunds PER order (keyed by namespace+orderId). A
// refund is a check-then-write against ord.Refunded (square.Refund guards
// Refunded+amt > Total), which is not atomic; two concurrent refunds with
// DIFFERENT idempotency keys could both pass the guard against the same
// pre-state and over-refund. Serializing per order closes that window.
// Correct for the single-pod deployment (replicas:1 + Recreate, RWO SQLite).
var refundLocks sync.Map // map[string]*sync.Mutex

func refundLockFor(key string) *sync.Mutex {
	m, _ := refundLocks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func Refund(c *gin.Context) {
	// Money move: enforce admin INSIDE the handler. The route's
	// TokenRequired(Admin) middleware is a no-op on the IAM path (Red HIGH-4),
	// so a refund must verify admin itself, IAM-aware.
	if !middleware.RequireAdmin(c) {
		return
	}

	org, ord, err := getOrganizationAndOrder(c)
	if err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	// Serialize all refunds for THIS order so concurrent distinct-key requests
	// can't both pass square.Refund's over-refund guard against the same
	// pre-state and over-refund. Scoped by namespace+order so other
	// orders/tenants never contend.
	mu := refundLockFor(nscontext.GetNamespace(middleware.GetContext(c)) + "\x00" + ord.Id())
	mu.Lock()
	defer mu.Unlock()

	// Re-read the order INSIDE the lock so its Refunded/Paid reflect the latest
	// COMMITTED state. getOrganizationAndOrder loaded ord BEFORE the lock; two
	// concurrent distinct-AMOUNT refunds would otherwise each evaluate
	// square.Refund's over-refund guard against the same STALE ord.Refunded and
	// both pass (3000 + 3000 on a 5000 order ⇒ 6000 refunded). Reloading under
	// the lock makes the check-then-write atomic per order (Red HIGH-3),
	// mirroring giftcard.Redeem which reads the balance inside its per-card lock.
	// NewNamespaced keeps the order in the caller org's own store (Red CRIT-2).
	db := datastore.NewNamespaced(org.Namespaced(c))
	fresh := order.New(db)
	if err := fresh.GetById(ord.Id()); err != nil {
		http.Fail(c, 404, "Failed to retrieve order", OrderDoesNotExist)
		return
	}
	ord = fresh

	// Idempotency guard (money-critical). A refund is a non-idempotent money
	// move: two identical POSTs (a client retry, a double-click, a proxy replay)
	// would otherwise each hit the gateway and double-refund. When the caller
	// supplies an idempotency key, a replay returns the FIRST result instead of
	// refunding again. The guard is scoped per order so keys never cross orders.
	//
	// Scope of protection: this de-dups OUR ledger + replays OUR response. For
	// the narrow concurrent-first-submit window (two brand-new identical
	// requests racing before either records the guard) the gateway is the final
	// backstop — Square/Stripe both honor an idempotency key on the refund call.
	// square.Refund already guards over-refund (Refunded+amt > Total ⇒ reject);
	// the guard here adds retry/replay safety on top.
	idemKey := strings.TrimSpace(c.GetHeader("X-Idempotency-Key"))
	if idemKey != "" {
		scope := "refund:" + ord.Id()
		rec, replay, gerr := idempotencykey.Begin(db, scope, idemKey)
		if gerr != nil {
			http.Fail(c, 500, "idempotency guard failed", gerr)
			return
		}
		if replay {
			// A prior request with this key already ran. Return its stored
			// outcome verbatim; do NOT refund again.
			if rec.Status == idempotencykey.StatusCompleted && rec.Response != "" {
				c.Data(200, "application/json", []byte(rec.Response))
				return
			}
			// In-flight (started but not completed): a concurrent request owns
			// this key. Fail closed with 409 so the caller retries, rather than
			// risk a parallel double refund.
			http.Fail(c, 409, "a refund with this idempotency key is already in progress", errRefundInFlight)
			return
		}

		if err := refund(c, org, ord); err != nil {
			http.Fail(c, 400, err.Error(), err)
			return
		}
		// Record the successful outcome for future replays.
		if resp, merr := json.Marshal(ord); merr == nil {
			_ = idempotencykey.Complete(rec, string(resp))
		}
		http.Render(c, 200, ord)
		return
	}

	// No idempotency key supplied — legacy behavior (over-refund guard still
	// applies in square.Refund). Callers that move money SHOULD send a key.
	if err := refund(c, org, ord); err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	http.Render(c, 200, ord)
}

func Cancel(c *gin.Context) {
	org, ord, err := getOrganizationAndOrder(c)
	if err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	if err := cancel(c, org, ord); err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	http.Render(c, 200, ord)
}

func Confirm(c *gin.Context) {
	org, ord, err := getOrganizationAndOrder(c)
	if err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	if err := confirm(c, org, ord); err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	http.Render(c, 200, ord)
}

func route(router router.Router, prefix string) {
	adminRequired := middleware.TokenRequired(permission.Admin)
	publishedRequired := middleware.TokenRequired(permission.Admin, permission.Published)

	api := router.Group(prefix)
	api.Use(middleware.AccessControl("*"))

	// Hosted checkout sessions. AUTHENTICATED like every sibling: a valid
	// service token / per-org Published storefront token (or IAM principal) is
	// required BEFORE any org resolution or Square call. There is no anonymous
	// path to mint a payment link — the org is taken from the validated token,
	// never the request body.
	if prefix == "/checkout" {
		api.POST("/sessions", publishedRequired, Sessions)
	}

	// Auth and Capture Flow (Two-step Payment)
	api.POST("/authorize", publishedRequired, Authorize)
	api.POST("/authorize/:orderid", publishedRequired, Authorize)
	api.POST("/capture/:orderid", publishedRequired, Capture)

	// Charge Flow (implicit Auth+Capture)
	api.POST("/charge", publishedRequired, Charge)

	// Confirm / Cancel Flow
	api.POST("/confirm/:orderid", publishedRequired, Confirm)
	api.POST("/cancel/:orderid", publishedRequired, Cancel)

	// Deprecated (should use normal authorization flow to initiate)
	api.POST("/paypal", publishedRequired, Authorize)
	api.POST("/paypal/pay", publishedRequired, Authorize)

	api.GET("/ethereum/lookup/:proxyaddress", adminRequired, ethereum.Lookup)

	// Wire transfer endpoints
	api.GET("/wire/instructions", wire.Instructions)
	api.POST("/wire/credit", adminRequired, wire.Credit)
}

func Route(router router.Router, args ...gin.HandlerFunc) {
	route(router, "") // Deprecated
	route(router, "/checkout")
}
