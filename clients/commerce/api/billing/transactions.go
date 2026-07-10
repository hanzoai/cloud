package billing

import (
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// ListTransactions returns transactions for an IAM user, newest first.
//
//	GET /v1/billing/transactions?user=hanzo/alice&limit=100&offset=0&currency=usd
//
// Response: { "transactions": [...], "count": N, "user": "hanzo/alice" }
func ListTransactions(c *gin.Context) {
	org := middleware.GetOrganization(c)
	ctx := org.Namespaced(c)

	user := strings.ToLower(strings.TrimSpace(c.Query("user")))
	if user == "" {
		http.Fail(c, 400, "user query parameter is required", nil)
		return
	}

	datas, err := util.GetTransactions(ctx, user, "iam-user", org.TestMode())
	if err != nil {
		http.Fail(c, 500, "failed to query transactions", err)
		return
	}

	// Flatten all transactions across currencies.
	type txResponse struct {
		Id        string                 `json:"id"`
		Type      string                 `json:"type"`
		Amount    int64                  `json:"amount"`
		Currency  string                 `json:"currency"`
		Tags      string                 `json:"tags,omitempty"`
		Notes     string                 `json:"notes,omitempty"`
		Metadata  map[string]interface{} `json:"metadata,omitempty"`
		CreatedAt string                 `json:"createdAt"`
		ExpiresAt string                 `json:"expiresAt,omitempty"`
	}

	all := make([]txResponse, 0)
	curFilter := strings.ToLower(strings.TrimSpace(c.Query("currency")))

	for cur, data := range datas.Data {
		if curFilter != "" && string(cur) != curFilter {
			continue
		}
		for _, tx := range data.Transactions {
			r := txResponse{
				Id:        tx.Id(),
				Type:      string(tx.Type),
				Amount:    int64(tx.Amount),
				Currency:  string(tx.Currency),
				Tags:      tx.Tags,
				Notes:     tx.Notes,
				Metadata:  tx.Metadata,
				CreatedAt: tx.GetCreatedAt().Format("2006-01-02T15:04:05Z"),
			}
			if !tx.ExpiresAt.IsZero() {
				r.ExpiresAt = tx.ExpiresAt.Format("2006-01-02T15:04:05Z")
			}
			all = append(all, r)
		}
	}

	// Sort newest first.
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt > all[j].CreatedAt
	})

	// Apply limit + offset.
	limit := 100
	offset := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	total := len(all)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := all[offset:end]

	c.JSON(200, gin.H{
		"transactions": page,
		"count":        total,
		"user":         user,
	})
}
