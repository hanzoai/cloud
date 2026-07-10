package billing

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// billingAccountMember is a simplified member record.
// In the current architecture, org members live in IAM, not in Commerce.
// We return stub data here; a richer implementation would call IAM's API.
type billingAccountMember struct {
	ID      string    `json:"id"`
	UserID  string    `json:"userId"`
	Email   string    `json:"email"`
	Name    string    `json:"name"`
	Role    string    `json:"role"`
	AddedAt time.Time `json:"addedAt"`
}

// ListBillingAccounts returns billing accounts visible to the caller.
// In Commerce each organization is one billing account. The authenticated
// org is returned as the single account for the current token.
//
//	GET /v1/billing/accounts
func ListBillingAccounts(c *gin.Context) {
	org := middleware.GetOrganization(c)

	account := gin.H{
		"id":        org.Id(),
		"name":      org.FullName,
		"orgId":     org.Id(),
		"orgName":   org.Name,
		"currency":  "usd",
		"createdAt": org.CreatedAt,
	}

	// Surface the caller's role if the gateway authenticated them.
	// claims is always non-nil; an empty Subject means anonymous and
	// we leave the "role" field unset rather than implying membership.
	if claims := iammiddleware.GetIAMClaims(c); claims.Subject != "" {
		role := "member"
		for _, r := range claims.Roles {
			if r == "admin" || r == "owner" {
				role = r
				break
			}
		}
		account["role"] = role
	}

	c.JSON(200, []gin.H{account})
}

// CreateBillingAccount is a no-op stub. Billing accounts are provisioned via
// IAM/console org creation; Commerce does not manage org lifecycle.
// Returns 501 to signal the caller to redirect to the org provisioning flow.
//
//	POST /v1/billing/accounts
func CreateBillingAccount(c *gin.Context) {
	http.Fail(c, 501, "billing account creation must be done via the Hanzo console", nil)
}

// ListAccountMembers returns the members of a billing account (org).
// Currently returns the requesting IAM user as the sole member, since
// Commerce does not store a full membership roster (that lives in IAM).
//
//	GET /v1/billing/accounts/:id/members
func ListAccountMembers(c *gin.Context) {
	org := middleware.GetOrganization(c)

	// Verify the requested account belongs to the authenticated org.
	if id := c.Param("id"); id != org.Id() {
		http.Fail(c, 403, "access denied to billing account", nil)
		return
	}

	members := make([]gin.H, 0)

	// claims is always non-nil; an empty Subject means anonymous and
	// the response stays an empty members list rather than synthesizing
	// a phantom row.
	if claims := iammiddleware.GetIAMClaims(c); claims.Subject != "" {
		role := "member"
		for _, r := range claims.Roles {
			if r == "admin" || r == "owner" {
				role = r
				break
			}
		}
		members = append(members, gin.H{
			"id":      claims.Subject,
			"userId":  claims.Subject,
			"email":   claims.Email,
			"role":    role,
			"addedAt": org.CreatedAt,
		})
	}

	c.JSON(200, members)
}

// AddAccountMember is a stub. Member management is done via IAM.
//
//	POST /v1/billing/accounts/:id/members
func AddAccountMember(c *gin.Context) {
	http.Fail(c, 501, "member management must be done via the Hanzo console", nil)
}

// UpdateMemberRole is a stub. Role updates are done via IAM.
//
//	PATCH /v1/billing/accounts/:id/members/:memberId
func UpdateMemberRole(c *gin.Context) {
	http.Fail(c, 501, "role updates must be done via the Hanzo console", nil)
}

// RemoveAccountMember is a stub. Member removal is done via IAM.
//
//	DELETE /v1/billing/accounts/:id/members/:memberId
func RemoveAccountMember(c *gin.Context) {
	http.Fail(c, 501, "member removal must be done via the Hanzo console", nil)
}
