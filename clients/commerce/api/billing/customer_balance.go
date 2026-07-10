package billing

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/balancetransaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// GetCustomerBalance retrieves the customer balance for a customer+currency.
//
//	GET /v1/billing/customer-balance?customerId=...&currency=...
func GetCustomerBalance(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	customerId := c.Query("customerId")
	if customerId == "" {
		http.Fail(c, 400, "customerId is required", nil)
		return
	}

	cur := currency.Type(c.DefaultQuery("currency", "usd"))

	cb, err := engine.GetOrCreateCustomerBalance(db, customerId, cur)
	if err != nil {
		log.Error("Failed to get customer balance: %v", err, c)
		http.Fail(c, 500, "failed to get balance", err)
		return
	}

	c.JSON(200, gin.H{
		"customerId": cb.CustomerId,
		"currency":   cb.Currency,
		"balance":    cb.Balance,
	})
}

type adjustBalanceRequest struct {
	CustomerId  string `json:"customerId"`
	Amount      int64  `json:"amount"` // positive = credit, negative = debit
	Currency    string `json:"currency,omitempty"`
	Description string `json:"description,omitempty"`
}

// AdjustCustomerBalance manually adjusts a customer's balance.
//
//	POST /v1/billing/customer-balance/adjustments
func AdjustCustomerBalance(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req adjustBalanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.CustomerId == "" {
		http.Fail(c, 400, "customerId is required", nil)
		return
	}
	if req.Amount == 0 {
		http.Fail(c, 400, "amount must be non-zero", nil)
		return
	}

	cur := currency.Type("usd")
	if req.Currency != "" {
		cur = currency.Type(req.Currency)
	}

	bt, err := engine.AdjustCustomerBalance(db, req.CustomerId, req.Amount, cur, "adjustment", req.Description)
	if err != nil {
		log.Error("Failed to adjust balance: %v", err, c)
		http.Fail(c, 500, "failed to adjust balance", err)
		return
	}

	c.JSON(200, balanceTransactionResponse(bt))
}

// ListBalanceTransactions lists balance transactions for a customer.
//
//	GET /v1/billing/balance-transactions?customerId=...
func ListBalanceTransactions(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	txns := make([]*balancetransaction.BalanceTransaction, 0)
	q := balancetransaction.Query(db).Ancestor(rootKey)

	if customerId := c.Query("customerId"); customerId != "" {
		q = q.Filter("CustomerId=", customerId)
	}

	iter := q.Order("-Created").Run()
	for {
		bt := balancetransaction.New(db)
		if _, err := iter.Next(bt); err != nil {
			break
		}
		txns = append(txns, bt)
	}

	results := make([]map[string]interface{}, len(txns))
	for i, bt := range txns {
		results[i] = balanceTransactionResponse(bt)
	}
	c.JSON(200, results)
}

func balanceTransactionResponse(bt *balancetransaction.BalanceTransaction) map[string]interface{} {
	resp := map[string]interface{}{
		"id":            bt.Id(),
		"customerId":    bt.CustomerId,
		"amount":        bt.Amount,
		"currency":      bt.Currency,
		"type":          bt.Type,
		"endingBalance": bt.EndingBalance,
		"created":       bt.Created,
	}
	if bt.Description != "" {
		resp["description"] = bt.Description
	}
	if bt.InvoiceId != "" {
		resp["invoiceId"] = bt.InvoiceId
	}
	if bt.CreditNoteId != "" {
		resp["creditNoteId"] = bt.CreditNoteId
	}
	if bt.SourceRef != "" {
		resp["sourceRef"] = bt.SourceRef
	}
	if bt.Metadata != nil {
		resp["metadata"] = bt.Metadata
	}
	return resp
}
