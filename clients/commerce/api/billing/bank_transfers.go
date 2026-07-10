package billing

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/banktransferinstruction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createBankTransferInstructionRequest struct {
	CustomerId    string `json:"customerId"`
	Currency      string `json:"currency"`
	Type          string `json:"type"` // "ach" | "wire" | "sepa"
	BankName      string `json:"bankName"`
	AccountHolder string `json:"accountHolder,omitempty"`
	AccountNumber string `json:"accountNumber"`
	RoutingNumber string `json:"routingNumber,omitempty"`
	IBAN          string `json:"iban,omitempty"`
	BIC           string `json:"bic,omitempty"`
}

// CreateBankTransferInstruction creates bank transfer details for a customer.
//
//	POST /v1/billing/bank-transfer-instructions
func CreateBankTransferInstruction(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createBankTransferInstructionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.CustomerId == "" {
		http.Fail(c, 400, "customerId is required", nil)
		return
	}

	if req.Type == "" {
		http.Fail(c, 400, "type is required (ach, wire, sepa)", nil)
		return
	}

	if req.Type != "ach" && req.Type != "wire" && req.Type != "sepa" {
		http.Fail(c, 400, "type must be one of: ach, wire, sepa", nil)
		return
	}

	if req.BankName == "" {
		http.Fail(c, 400, "bankName is required", nil)
		return
	}

	if req.AccountNumber == "" {
		http.Fail(c, 400, "accountNumber is required", nil)
		return
	}

	inst := banktransferinstruction.New(db)
	inst.CustomerId = req.CustomerId
	inst.Type = req.Type
	inst.BankName = req.BankName
	inst.AccountHolder = req.AccountHolder
	inst.AccountNumber = maskAccountNumber(req.AccountNumber)
	inst.RoutingNumber = req.RoutingNumber
	inst.IBAN = req.IBAN
	inst.BIC = req.BIC

	// Generate unique payment reference (first 8 chars of UUID)
	inst.Reference = uuid.New().String()[:8]

	if req.Currency != "" {
		inst.Currency = currency.Type(strings.ToLower(req.Currency))
	}

	if err := inst.Create(); err != nil {
		log.Error("Failed to create bank transfer instruction: %v", err, c)
		http.Fail(c, 500, "failed to create bank transfer instruction", err)
		return
	}

	c.JSON(201, instructionResponse(inst))
}

// ListBankTransferInstructions lists bank transfer instructions, optionally
// filtered by customerId.
//
//	GET /v1/billing/bank-transfer-instructions?customerId=...
func ListBankTransferInstructions(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	q := banktransferinstruction.Query(db).Ancestor(rootKey)

	customerId := strings.TrimSpace(c.Query("customerId"))
	if customerId != "" {
		q = q.Filter("CustomerId=", customerId)
	}

	status := strings.TrimSpace(c.Query("status"))
	if status != "" {
		q = q.Filter("Status=", status)
	}

	iter := q.Order("-Created").Run()
	items := make([]map[string]interface{}, 0)
	for {
		item := banktransferinstruction.New(db)
		if _, err := iter.Next(item); err != nil {
			break
		}
		items = append(items, instructionResponse(item))
	}

	c.JSON(200, gin.H{
		"instructions": items,
		"count":        len(items),
	})
}

// GetBankTransferInstruction returns a single bank transfer instruction by ID.
//
//	GET /v1/billing/bank-transfer-instructions/:id
func GetBankTransferInstruction(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	inst := banktransferinstruction.New(db)
	if err := inst.GetById(id); err != nil {
		http.Fail(c, 404, "bank transfer instruction not found", err)
		return
	}

	c.JSON(200, instructionResponse(inst))
}

type reconcileRequest struct {
	Reference string `json:"reference"`
	Amount    int64  `json:"amount"` // cents
	Currency  string `json:"currency"`
}

// ReconcileInboundTransfer matches an incoming bank transfer by reference
// and creates a balance transaction for the customer.
//
//	POST /v1/billing/bank-transfer-instructions/reconciliation/match
func ReconcileInboundTransfer(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req reconcileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Reference == "" {
		http.Fail(c, 400, "reference is required", nil)
		return
	}

	if req.Amount <= 0 {
		http.Fail(c, 400, "amount must be positive", nil)
		return
	}

	// Look up instruction by reference
	rootKey := db.NewKey("synckey", "", 1, nil)
	instructions := make([]*banktransferinstruction.BankTransferInstruction, 0)
	q := banktransferinstruction.Query(db).Ancestor(rootKey).
		Filter("Reference=", req.Reference).
		Filter("Status=", "active")

	if _, err := q.GetAll(&instructions); err != nil || len(instructions) == 0 {
		http.Fail(c, 404, "no active instruction found for reference", nil)
		return
	}

	inst := instructions[0]

	cur := currency.Type("usd")
	if req.Currency != "" {
		cur = currency.Type(strings.ToLower(req.Currency))
	}

	// Create a balance transaction via the engine
	description := fmt.Sprintf("Bank transfer received (ref: %s, type: %s)", inst.Reference, inst.Type)
	bt, err := engine.AdjustCustomerBalance(db, inst.CustomerId, req.Amount, cur, "bank_transfer", description)
	if err != nil {
		log.Error("Failed to reconcile bank transfer: %v", err, c)
		http.Fail(c, 500, "failed to reconcile transfer", err)
		return
	}

	c.JSON(200, gin.H{
		"matched":      true,
		"reference":    inst.Reference,
		"customerId":   inst.CustomerId,
		"amount":       req.Amount,
		"currency":     cur,
		"transferType": inst.Type,
		"transaction": map[string]interface{}{
			"id":            bt.Id(),
			"amount":        bt.Amount,
			"endingBalance": bt.EndingBalance,
			"type":          bt.Type,
			"created":       bt.CreatedAt,
		},
	})
}

// maskAccountNumber returns last 4 digits prefixed with asterisks.
// If the input is 4 characters or fewer, it is returned as-is.
func maskAccountNumber(acct string) string {
	if len(acct) <= 4 {
		return acct
	}
	return "****" + acct[len(acct)-4:]
}

func instructionResponse(i *banktransferinstruction.BankTransferInstruction) map[string]interface{} {
	resp := map[string]interface{}{
		"id":            i.Id(),
		"customerId":    i.CustomerId,
		"currency":      i.Currency,
		"type":          i.Type,
		"reference":     i.Reference,
		"bankName":      i.BankName,
		"accountNumber": i.AccountNumber,
		"status":        i.Status,
		"createdAt":     i.CreatedAt,
		"updatedAt":     i.UpdatedAt,
	}
	if i.AccountHolder != "" {
		resp["accountHolder"] = i.AccountHolder
	}
	if i.RoutingNumber != "" {
		resp["routingNumber"] = i.RoutingNumber
	}
	if i.IBAN != "" {
		resp["iban"] = i.IBAN
	}
	if i.BIC != "" {
		resp["bic"] = i.BIC
	}
	if i.Metadata != nil {
		resp["metadata"] = i.Metadata
	}
	return resp
}
