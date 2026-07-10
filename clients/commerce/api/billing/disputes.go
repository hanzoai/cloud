package billing

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/dispute"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// GetDispute retrieves a dispute by ID.
//
//	GET /v1/billing/disputes/:id
func GetDispute(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	d := dispute.New(db)
	if err := d.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "dispute not found", err)
		return
	}

	c.JSON(200, disputeResponse(d))
}

// ListDisputes lists disputes.
//
//	GET /v1/billing/disputes?paymentIntentId=...
func ListDisputes(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	disputes := make([]*dispute.Dispute, 0)
	q := dispute.Query(db).Ancestor(rootKey)

	if piId := c.Query("paymentIntentId"); piId != "" {
		q = q.Filter("PaymentIntentId=", piId)
	}

	iter := q.Order("-Created").Run()
	for {
		d := dispute.New(db)
		if _, err := iter.Next(d); err != nil {
			break
		}
		disputes = append(disputes, d)
	}

	results := make([]map[string]interface{}, len(disputes))
	for i, d := range disputes {
		results[i] = disputeResponse(d)
	}
	c.JSON(200, results)
}

type submitEvidenceRequest struct {
	CustomerName         string `json:"customerName,omitempty"`
	CustomerEmailAddress string `json:"customerEmailAddress,omitempty"`
	ProductDescription   string `json:"productDescription,omitempty"`
	ServiceDate          string `json:"serviceDate,omitempty"`
	UncategorizedText    string `json:"uncategorizedText,omitempty"`
}

// SubmitDisputeEvidence submits evidence for a dispute.
//
//	PATCH /v1/billing/disputes/:id
func SubmitDisputeEvidence(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	d := dispute.New(db)
	if err := d.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "dispute not found", err)
		return
	}

	var req submitEvidenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	d.Evidence = &dispute.DisputeEvidence{
		CustomerName:         req.CustomerName,
		CustomerEmailAddress: req.CustomerEmailAddress,
		ProductDescription:   req.ProductDescription,
		ServiceDate:          req.ServiceDate,
		UncategorizedText:    req.UncategorizedText,
	}
	d.Status = dispute.UnderReview

	if err := d.Update(); err != nil {
		log.Error("Failed to submit dispute evidence: %v", err, c)
		http.Fail(c, 500, "failed to submit evidence", err)
		return
	}

	c.JSON(200, disputeResponse(d))
}

// CloseDispute closes a dispute.
//
//	POST /v1/billing/disputes/:id/close
func CloseDispute(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	d := dispute.New(db)
	if err := d.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "dispute not found", err)
		return
	}

	d.Status = dispute.Lost // default to lost when merchant closes

	if err := d.Update(); err != nil {
		log.Error("Failed to close dispute: %v", err, c)
		http.Fail(c, 500, "failed to close dispute", err)
		return
	}

	c.JSON(200, disputeResponse(d))
}

func disputeResponse(d *dispute.Dispute) map[string]interface{} {
	resp := map[string]interface{}{
		"id":              d.Id(),
		"paymentIntentId": d.PaymentIntentId,
		"amount":          d.Amount,
		"currency":        d.Currency,
		"status":          d.Status,
		"created":         d.Created,
	}
	if d.Reason != "" {
		resp["reason"] = d.Reason
	}
	if !d.EvidenceDueBy.IsZero() {
		resp["evidenceDueBy"] = d.EvidenceDueBy
	}
	if d.Evidence != nil {
		resp["evidence"] = d.Evidence
	}
	if d.ProviderRef != "" {
		resp["providerRef"] = d.ProviderRef
	}
	if d.Metadata != nil {
		resp["metadata"] = d.Metadata
	}
	return resp
}
