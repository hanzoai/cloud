package billing

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/autorecharge"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/kms"
	jsonhttp "github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// loadAutoRecharge returns the org's auto-recharge config (keyed by the org
// slug) or nil if none exists.
func loadAutoRecharge(db *datastore.Datastore, userId string) *autorecharge.AutoRecharge {
	rootKey := db.NewKey("synckey", "", 1, nil)
	cfgs := make([]*autorecharge.AutoRecharge, 0, 1)
	if _, err := autorecharge.Query(db).Ancestor(rootKey).Filter("UserId=", userId).GetAll(&cfgs); err != nil {
		return nil
	}
	if len(cfgs) == 0 {
		return nil
	}
	return cfgs[0]
}

// defaultPaymentMethod returns the customer's default payment method, or nil.
func defaultPaymentMethod(db *datastore.Datastore, customerId string) *paymentmethod.PaymentMethod {
	rootKey := db.NewKey("synckey", "", 1, nil)
	pms := make([]*paymentmethod.PaymentMethod, 0, 1)
	if _, err := paymentmethod.Query(db).Ancestor(rootKey).
		Filter("CustomerId=", customerId).
		Filter("IsDefault=", true).GetAll(&pms); err != nil {
		return nil
	}
	if len(pms) == 0 {
		return nil
	}
	return pms[0]
}

func autoRechargeResponse(cfg *autorecharge.AutoRecharge, userId string) gin.H {
	if cfg == nil {
		return gin.H{
			"userId":         userId,
			"enabled":        false,
			"thresholdCents": 0,
			"amountCents":    0,
			"currency":       "usd",
		}
	}
	return gin.H{
		"userId":          cfg.UserId,
		"enabled":         cfg.Enabled,
		"thresholdCents":  cfg.ThresholdCents,
		"amountCents":     cfg.AmountCents,
		"currency":        cfg.Currency,
		"lastRechargedAt": cfg.LastRechargedAt,
	}
}

// GetAutoRecharge returns the org's auto-recharge config (or disabled defaults).
//
//	GET /v1/billing/auto-recharge
func GetAutoRecharge(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	cfg := loadAutoRecharge(db, org.Name)
	c.JSON(200, autoRechargeResponse(cfg, org.Name))
}

type autoRechargeRequest struct {
	Enabled        bool   `json:"enabled"`
	ThresholdCents int64  `json:"thresholdCents"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency,omitempty"`
}

// SetAutoRecharge upserts the org's auto-recharge config.
//
//	PUT /v1/billing/auto-recharge
//
// Enabling requires a default payment method on file (the card that will be
// charged off-session when the balance runs low).
func SetAutoRecharge(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req autoRechargeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonhttp.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Enabled {
		if req.AmountCents <= 0 {
			jsonhttp.Fail(c, 400, "amountCents must be positive", nil)
			return
		}
		if req.ThresholdCents < 0 {
			jsonhttp.Fail(c, 400, "thresholdCents must not be negative", nil)
			return
		}
		if defaultPaymentMethod(db, org.Name) == nil {
			jsonhttp.Fail(c, 400, "a default payment method is required to enable auto-recharge", nil)
			return
		}
	}

	cur := strings.ToLower(strings.TrimSpace(req.Currency))
	if cur == "" {
		cur = "usd"
	}

	cfg := loadAutoRecharge(db, org.Name)
	if cfg == nil {
		cfg = autorecharge.New(db)
		cfg.UserId = org.Name
	}
	cfg.Enabled = req.Enabled
	cfg.ThresholdCents = req.ThresholdCents
	cfg.AmountCents = req.AmountCents
	cfg.Currency = cur

	var err error
	if cfg.Id() == "" {
		err = cfg.Create()
	} else {
		err = cfg.Update()
	}
	if err != nil {
		log.Error("Failed to save auto-recharge config for org %q: %v", org.Name, err, c)
		jsonhttp.Fail(c, 500, "failed to save auto-recharge config", err)
		return
	}

	c.JSON(200, autoRechargeResponse(cfg, org.Name))
}

type autoRechargeRunResult struct {
	OrgName       string `json:"orgName"`
	UserId        string `json:"userId"`
	Charged       bool   `json:"charged"`
	AmountCents   int64  `json:"amountCents,omitempty"`
	BalanceCents  int64  `json:"balanceCents,omitempty"`
	TransactionId string `json:"transactionId,omitempty"`
	Error         string `json:"error,omitempty"`
}

// RunAutoRechargeAllOrgs iterates every organization and, for those with
// auto-recharge enabled whose available balance is below the threshold, charges
// the default payment method and credits the balance. Intended to be invoked on
// a recurring schedule (CronJob) by the platform.
//
//	POST /v1/billing/auto-recharge/run-all
func RunAutoRechargeAllOrgs(c *gin.Context) {
	var kmsClient *kms.CachedClient
	if v, ok := c.Get("kms"); ok {
		kmsClient, _ = v.(*kms.CachedClient)
	}

	rootDb := datastore.New(c)
	orgs := make([]*organization.Organization, 0)
	if _, err := organization.Query(rootDb).GetAll(&orgs); err != nil {
		log.Error("Failed to list organizations for auto-recharge: %v", err, c)
		jsonhttp.Fail(c, 500, "failed to list organizations", err)
		return
	}

	results := make([]autoRechargeRunResult, 0)
	charged := 0

	for _, org := range orgs {
		db := datastore.New(org.Namespaced(c))

		cfg := loadAutoRecharge(db, org.Name)
		if cfg == nil || !cfg.Enabled || cfg.AmountCents <= 0 {
			continue
		}

		cur := currency.Type(strings.ToLower(cfg.Currency))
		if cur == "" {
			cur = "usd"
		}

		// Available = balance - holds. Skip if still above the threshold.
		var available int64
		if datas, err := util.GetTransactionsByCurrency(org.Namespaced(c), org.Name, "iam-user", cur, org.TestMode()); err == nil {
			if data, ok := datas.Data[cur]; ok {
				available = int64(data.Balance) - int64(data.Holds)
			}
		}
		if available >= cfg.ThresholdCents {
			continue
		}

		pm := defaultPaymentMethod(db, org.Name)
		if pm == nil {
			results = append(results, autoRechargeRunResult{
				OrgName: org.Name, UserId: org.Name, Charged: false,
				Error: "no default payment method",
			})
			continue
		}

		// Hydrate this org's payment credentials so ProcessorsForOrg sees real
		// Square creds for the off-session charge.
		if kmsClient != nil {
			if err := kms.Hydrate(kmsClient, org); err != nil {
				log.Error("KMS hydration failed for org %q during auto-recharge: %v", org.Name, err, c)
			}
		}

		desc := fmt.Sprintf("Auto-recharge %d %s for %s", cfg.AmountCents, cur, org.Name)
		txID, balanceCents, err := chargeAndCredit(c, org, db, pm, cfg.AmountCents, cur, org.Name, desc)
		if err != nil {
			log.Error("Auto-recharge charge failed for org %q: %v", org.Name, err, c)
			results = append(results, autoRechargeRunResult{
				OrgName: org.Name, UserId: org.Name, Charged: false, Error: err.Error(),
			})
			continue
		}

		cfg.LastRechargedAt = time.Now().UTC().Format(time.RFC3339)
		_ = cfg.Update()

		charged++
		results = append(results, autoRechargeRunResult{
			OrgName:       org.Name,
			UserId:        org.Name,
			Charged:       true,
			AmountCents:   cfg.AmountCents,
			BalanceCents:  int64(balanceCents),
			TransactionId: txID,
		})
	}

	c.JSON(200, gin.H{
		"orgs":    len(orgs),
		"charged": charged,
		"results": results,
	})
}
