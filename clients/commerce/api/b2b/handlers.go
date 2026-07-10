// Package b2b wires the B2B commerce HTTP surface: companies, employees (with
// their spending headroom), quotes (accept/reject + message thread), and
// spend-approvals. Every handler is tenant-scoped via the caller's
// organization namespace; the REST layer auto-namespaces the default CRUD
// routes and each custom sub-route passes the Namespace middleware explicitly.
package b2b

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/approval"
	"github.com/hanzoai/cloud/clients/commerce/models/company"
	"github.com/hanzoai/cloud/clients/commerce/models/employee"
	"github.com/hanzoai/cloud/clients/commerce/models/quote"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	rest.New(company.Company{}).Route(router, args...)

	employees := rest.New(employee.Employee{})
	employees.GET("/:employeeid/spending", namespaced, EmployeeSpending)
	employees.Route(router, args...)

	quotes := rest.New(quote.Quote{})
	quotes.POST("/:quoteid/accept", namespaced, AcceptQuote)
	quotes.POST("/:quoteid/reject", namespaced, RejectQuote)
	quotes.POST("/:quoteid/messages", namespaced, AddQuoteMessage)
	quotes.GET("/:quoteid/messages", namespaced, ListQuoteMessages)
	quotes.Route(router, args...)

	rest.New(quote.QuoteMessage{}).Route(router, args...)

	approvals := rest.New(approval.Approval{})
	approvals.POST("/:approvalid/approve", namespaced, ApproveApproval)
	approvals.POST("/:approvalid/reject", namespaced, RejectApproval)
	approvals.Route(router, args...)
}

// EmployeeSpending returns an employee's remaining spend headroom. The amount
// already committed is supplied by the caller (the cart/order in flight) via
// the `committed` query param in cents, defaulting to 0.
func EmployeeSpending(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))

	emp := employee.New(db)
	if err := emp.GetById(c.Params.ByName("employeeid")); err != nil {
		http.Fail(c, 404, "No employee found with id: "+c.Params.ByName("employeeid"), err)
		return
	}

	var committed currency.Cents
	if raw := c.Query("committed"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Fail(c, 400, "committed must be an integer number of cents", err)
			return
		}
		committed = currency.Cents(n)
	}

	http.Render(c, 200, gin.H{
		"employeeId":     emp.Id(),
		"limitCents":     emp.SpendingLimitCents,
		"committedCents": committed,
		"remainingCents": employee.RemainingSpendCents(committed, emp.SpendingLimitCents),
		"withinLimit":    employee.WithinLimit(committed, 0, emp.SpendingLimitCents),
	})
}

// AcceptQuote transitions a pending quote to accepted. Accepting a quote commits
// the org to a negotiated price, so it is admin-only — enforced inside the
// handler because the route middleware no-ops on the IAM path (Red HIGH-4).
func AcceptQuote(c *gin.Context) {
	if !middleware.RequireAdmin(c) {
		return
	}
	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))

	q := quote.New(db)
	if err := q.GetById(c.Params.ByName("quoteid")); err != nil {
		http.Fail(c, 404, "No quote found with id: "+c.Params.ByName("quoteid"), err)
		return
	}

	if !q.IsActionable() {
		http.Fail(c, 409, "Quote is not actionable in status: "+q.Status, errors.New("quote not actionable"))
		return
	}

	q.Status = quote.StatusAccepted
	if err := q.Update(); err != nil {
		http.Fail(c, 500, "Failed to accept quote", err)
		return
	}

	http.Render(c, 200, q)
}

// rejectQuoteRequest optionally names who rejected: customer (default) or
// merchant, selecting the corresponding rejected status.
type rejectQuoteRequest struct {
	By string `json:"by"`
}

// RejectQuote transitions a pending quote to a rejected state. Admin-only (a
// quote state decision) — gated inside the handler like AcceptQuote.
func RejectQuote(c *gin.Context) {
	if !middleware.RequireAdmin(c) {
		return
	}
	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))

	q := quote.New(db)
	if err := q.GetById(c.Params.ByName("quoteid")); err != nil {
		http.Fail(c, 404, "No quote found with id: "+c.Params.ByName("quoteid"), err)
		return
	}

	if !q.IsActionable() {
		http.Fail(c, 409, "Quote is not actionable in status: "+q.Status, errors.New("quote not actionable"))
		return
	}

	var req rejectQuoteRequest
	if c.Request.Body != nil {
		// Body is optional; ignore decode errors on an empty body.
		_ = json.Decode(c.Request.Body, &req)
	}

	status := quote.StatusCustomerRejected
	switch req.By {
	case "", quote.AuthorCustomer:
		status = quote.StatusCustomerRejected
	case quote.AuthorMerchant:
		status = quote.StatusMerchantRejected
	default:
		http.Fail(c, 400, "by must be customer or merchant", errors.New("invalid rejecter"))
		return
	}

	q.Status = status
	if err := q.Update(); err != nil {
		http.Fail(c, 500, "Failed to reject quote", err)
		return
	}

	http.Render(c, 200, q)
}

// addQuoteMessageRequest is the body for appending a message to a quote thread.
type addQuoteMessageRequest struct {
	AuthorId   string `json:"authorId"`
	AuthorType string `json:"authorType"`
	Text       string `json:"text"`
	ItemId     string `json:"itemId"`
}

// AddQuoteMessage appends a message to a quote's negotiation thread.
func AddQuoteMessage(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))

	quoteId := c.Params.ByName("quoteid")

	q := quote.New(db)
	if err := q.GetById(quoteId); err != nil {
		http.Fail(c, 404, "No quote found with id: "+quoteId, err)
		return
	}

	var req addQuoteMessageRequest
	if err := json.Decode(c.Request.Body, &req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	if req.Text == "" {
		http.Fail(c, 400, "text is required", errors.New("missing text"))
		return
	}
	switch req.AuthorType {
	case quote.AuthorCustomer, quote.AuthorMerchant:
	default:
		http.Fail(c, 400, "authorType must be customer or merchant", errors.New("invalid authorType"))
		return
	}

	msg := quote.NewMessage(db)
	msg.QuoteId = q.Id()
	msg.AuthorId = req.AuthorId
	msg.AuthorType = req.AuthorType
	msg.Text = req.Text
	msg.ItemId = req.ItemId

	if err := msg.Create(); err != nil {
		http.Fail(c, 500, "Failed to create quote message", err)
		return
	}

	http.Render(c, 201, msg)
}

// ListQuoteMessages lists the messages on a quote's thread.
func ListQuoteMessages(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))

	quoteId := c.Params.ByName("quoteid")

	q := quote.New(db)
	if err := q.GetById(quoteId); err != nil {
		http.Fail(c, 404, "No quote found with id: "+quoteId, err)
		return
	}

	var messages []*quote.QuoteMessage
	if _, err := quote.QueryMessages(db).Filter("QuoteId=", q.Id()).GetAll(&messages); err != nil {
		http.Fail(c, 500, "Failed to list quote messages", err)
		return
	}

	http.Render(c, 200, messages)
}

// resolveApprovalRequest optionally names the resolver, recorded on HandledBy.
type resolveApprovalRequest struct {
	HandledBy string `json:"handledBy"`
}

// ApproveApproval approves a pending approval.
func ApproveApproval(c *gin.Context) {
	resolveApproval(c, approval.ActionApprove)
}

// RejectApproval rejects a pending approval.
func RejectApproval(c *gin.Context) {
	resolveApproval(c, approval.ActionReject)
}

// resolveApproval applies an approve/reject action to an approval, enforcing
// the pending-only transition via approval.NextStatus. Admin-only: an approval
// authorizes spend, so both ApproveApproval and RejectApproval gate here inside
// the handler (the route middleware no-ops on the IAM path — Red HIGH-4).
func resolveApproval(c *gin.Context, action string) {
	if !middleware.RequireAdmin(c) {
		return
	}
	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))

	id := c.Params.ByName("approvalid")

	a := approval.New(db)
	if err := a.GetById(id); err != nil {
		http.Fail(c, 404, "No approval found with id: "+id, err)
		return
	}

	next, err := approval.NextStatus(a.Status, action)
	if err != nil {
		http.Fail(c, 409, err.Error(), err)
		return
	}

	var req resolveApprovalRequest
	if c.Request.Body != nil {
		_ = json.Decode(c.Request.Body, &req)
	}

	a.Status = next
	a.HandledBy = req.HandledBy
	if err := a.Update(); err != nil {
		http.Fail(c, 500, "Failed to update approval", err)
		return
	}

	http.Render(c, 200, a)
}
