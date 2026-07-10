package billing

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type withdrawRequest struct {
	User     string `json:"user"`
	Currency string `json:"currency"`
	Amount   int64  `json:"amount"` // cents
	Notes    string `json:"notes"`
	Tags     string `json:"tags"`
}

// Withdraw creates a withdrawal transaction for an IAM user.
//
//	POST /v1/billing/withdraw
//
// Used when a user explicitly moves funds out of their Commerce balance
// (e.g. funding a bot wallet, manual withdrawal). Non-admin callers may
// only withdraw from their own account; admin callers may withdraw on
// behalf of any user.
//
// Fails with 402 if the user has insufficient available balance.
func Withdraw(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req withdrawRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	req.User = strings.ToLower(strings.TrimSpace(req.User))
	if req.User == "" {
		http.Fail(c, 400, "user is required", nil)
		return
	}

	if req.Amount <= 0 {
		http.Fail(c, 400, "amount must be positive", nil)
		return
	}

	// Non-admin users may only withdraw from their own account. claims
	// is always non-nil (gateway-trust): IsAdmin=false when the
	// X-User-IsAdmin header is missing, so the check fails closed.
	if claims := iammiddleware.GetIAMClaims(c); !claims.IsAdmin {
		ownerUser := strings.ToLower(claims.Owner + "/" + claims.Name)
		if ownerUser != req.User {
			http.Fail(c, 403, "cannot withdraw from another user's account", nil)
			return
		}
	}

	cur := currency.Type(strings.ToLower(req.Currency))
	if cur == "" {
		cur = "usd"
	}

	// Check available balance before creating the withdrawal.
	datas, err := util.GetTransactionsByCurrency(org.Namespaced(c), req.User, "iam-user", cur, org.TestMode())
	if err != nil {
		http.Fail(c, 500, "failed to check balance", err)
		return
	}

	var available currency.Cents
	if data, ok := datas.Data[cur]; ok {
		available = data.Balance - data.Holds
		if available < 0 {
			available = 0
		}
	}

	if currency.Cents(req.Amount) > available {
		http.Fail(c, 402, fmt.Sprintf("insufficient balance: have %d cents, need %d cents", available, req.Amount), nil)
		return
	}

	notes := req.Notes
	if notes == "" {
		notes = fmt.Sprintf("Withdraw: %d cents %s", req.Amount, cur)
	}

	trans := transaction.New(db)
	trans.Type = transaction.Withdraw
	trans.SourceId = req.User
	trans.SourceKind = "iam-user"
	trans.Currency = cur
	trans.Amount = currency.Cents(req.Amount)
	trans.Notes = notes
	trans.Tags = req.Tags

	if org.TestMode() {
		trans.Test = true
	}

	if err := trans.Create(); err != nil {
		log.Error("Failed to create withdrawal transaction: %v", err, c)
		http.Fail(c, 500, "failed to create withdrawal", err)
		return
	}

	c.JSON(201, gin.H{
		"transactionId": trans.Id(),
		"user":          req.User,
		"amount":        req.Amount,
		"currency":      cur,
		"type":          "withdraw",
		"tags":          req.Tags,
	})
}
