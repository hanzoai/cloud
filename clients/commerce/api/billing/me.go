package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/credit"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// welcomeDepositKey derives the DETERMINISTIC storage key for a user's one-time
// welcome credit. Making the welcome deposit's id a pure function of the subject
// means two concurrent first-time grants (the check-then-create TOCTOU) collapse
// onto the SAME row via the storage ON CONFLICT(id,kind,namespace) upsert — so
// the welcome credits AT MOST ONCE even under an exact race, the same money-safe
// primitive giftcard redemptions use. The key is parented under the synckey root
// so the ancestor-scoped balance query still finds it, and the datastore is
// org-namespaced, so the id need only be unique per subject. hashid.EncodeKey
// returns a string-id verbatim, so trans.Id() stays a clean, stable string.
func welcomeDepositKey(db *datastore.Datastore, user string) datastore.Key {
	sum := sha256.Sum256([]byte("welcome-credit\x00" + user))
	rootKey := db.NewKey("synckey", "", 1, nil)
	return db.NewKey("transaction", "welcome_"+hex.EncodeToString(sum[:16]), 0, rootKey)
}

// orgBillingKey returns the canonical per-org billing key: the org slug.
//
// Billing is per-org — one balance covers the whole org (see the LLM gate
// in hanzoai/ai routers/filter_balance.go, which reads
// GET /v1/billing/balance?user=<orgSlug> with X-Org-Id=<orgSlug>, and
// debits usage against SourceId=<orgSlug>). The deposit, usage, and read
// paths MUST all resolve the same key or a customer tops up one key and
// reads another. The key is the resolved org's own slug (org.Name), which
// equals the namespace we read/write — guaranteeing destination-key ==
// namespace-slug, the exact invariant the proven gate relies on.
//
// Returns "" when no org is resolved (or the privileged "platform" org,
// which has no namespace) — callers should 401.
func orgBillingKey(c *gin.Context) string {
	org := middleware.GetOrganization(c)
	if org == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(org.Name))
}

// GetMyBalance returns the calling user's balance for a given currency.
// Identity comes from the gateway-injected X-Org-Id / X-User-Id headers;
// no admin token required.
//
//	GET /v1/billing/me/balance?currency=usd
func GetMyBalance(c *gin.Context) {
	user := orgBillingKey(c)
	if user == "" {
		http.Fail(c, 401, "missing identity headers", nil)
		return
	}

	org := middleware.GetOrganization(c)
	ctx := org.Namespaced(c)

	cur := currency.Type(strings.ToLower(c.DefaultQuery("currency", "usd")))

	split, err := bucketedSplit(ctx, user, cur, org.TestMode())
	if err != nil {
		http.Fail(c, 500, "failed to query balance", err)
		return
	}
	card := getCardOnFile(datastore.New(ctx), user)

	resp := gin.H{
		"user":      user,
		"currency":  cur,
		"balance":   int64(split.Balance),
		"holds":     int64(split.Holds),
		"available": int64(split.Available),
	}
	for k, v := range bucketFields(split, card) {
		resp[k] = v
	}
	c.JSON(200, resp)
}

// PostMyWelcome grants the welcome credit (idempotent, tag-deduped) to
// the calling user. Unlike POST /billing/credit — which requires an admin
// token and an explicit user — this endpoint is callable with just an IAM
// bearer token (user inferred from headers), designed to be invoked by the
// playground SPA on first successful login.
//
// Idempotent: if the credit was already granted (or zapped), returns
// 200 with `granted: false` instead of failing.
//
//	POST /v1/billing/me/welcome
func PostMyWelcome(c *gin.Context) {
	user := orgBillingKey(c)
	if user == "" {
		http.Fail(c, 401, "missing identity headers", nil)
		return
	}

	org := middleware.GetOrganization(c)

	// Step 4: chain-backed path mints the welcome credit on-chain (idempotent by
	// the shared deterministic welcome key), replacing the DB deposit write.
	if chainCreditEnabled() {
		rc, err := chainMintCredit(c, org, user, int64(credit.StarterCreditCents), treasury.BucketCredit,
			"welcome-credit:iam_first_login", welcomeMintKey(org, user))
		if err != nil {
			log.Error("chain welcome credit mint failed for %s: %v", user, err, c)
			http.Fail(c, 502, "on-chain welcome credit mint failed", err)
			return
		}
		c.JSON(200, gin.H{
			"user":     user,
			"granted":  !rc.Replayed,
			"amount":   int64(credit.StarterCreditCents),
			"currency": "usd",
			"onChain":  true,
			"txHash":   rc.TxHash,
		})
		return
	}

	db := datastore.New(org.Namespaced(c))
	rootKey := db.NewKey("synckey", "", 1, nil)

	// Already granted? — return 200 with granted=false. The tag query covers
	// EVERY historical grant (this endpoint, /credit, and pre-deterministic-key
	// rows), so sequential retries stay idempotent regardless of how the credit
	// was first minted.
	existing := make([]*transaction.Transaction, 0)
	q := transaction.Query(db).Ancestor(rootKey).
		Filter("DestinationId=", user).
		Filter("Tags=", credit.StarterCreditTag)
	if _, err := q.Limit(1).GetAll(&existing); err == nil && len(existing) > 0 {
		c.JSON(200, gin.H{
			"user":    user,
			"granted": false,
			"reason":  "already_granted",
		})
		return
	}

	trans := transaction.New(db)
	// DETERMINISTIC key: two concurrent first-time grants that both pass the tag
	// check above collapse onto ONE row (ON CONFLICT upsert), so the welcome
	// credits at most once even in the TOCTOU race the tag query alone leaves open.
	if err := trans.SetKey(welcomeDepositKey(db, user)); err != nil {
		log.Error("welcome credit key set failed for %s: %v", user, err, c)
		http.Fail(c, 500, "failed to grant welcome credit", err)
		return
	}
	trans.Type = transaction.Deposit
	trans.DestinationId = user
	trans.DestinationKind = "iam-user"
	trans.Currency = "usd"
	trans.Amount = currency.Cents(credit.StarterCreditCents)
	trans.Notes = "Welcome credit: $5.00 USD (expires in 365 days)"
	trans.Tags = credit.StarterCreditTag
	trans.ExpiresAt = time.Now().AddDate(0, 0, credit.StarterCreditDays)
	trans.Metadata = Map{
		"creditType": "starter",
		"expiryDays": credit.StarterCreditDays,
		"trigger":    "iam_first_login",
	}
	if org.TestMode() {
		trans.Test = true
	}

	// A server-FIXED (StarterCreditCents), tag-idempotent welcome credit to the
	// caller's OWN subject — the amount is not attacker-controlled — so authorize
	// this write at the ledger sink.
	trans.SetContext(mintauth.WithAuthorized(trans.Context()))
	if err := trans.Create(); err != nil {
		log.Error("welcome credit create failed for %s: %v", user, err, c)
		http.Fail(c, 500, "failed to grant welcome credit", err)
		return
	}

	c.JSON(201, gin.H{
		"user":      user,
		"granted":   true,
		"amount":    int64(credit.StarterCreditCents),
		"currency":  "usd",
		"expiresAt": trans.ExpiresAt,
	})
}
