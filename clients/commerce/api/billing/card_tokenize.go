package billing

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type cardTokenizeRequest struct {
	Number      string `json:"number"`
	ExpiryMonth string `json:"expiry_month"` // "01"-"12"
	ExpiryYear  string `json:"expiry_year"`  // "2026"
	CVC         string `json:"cvc"`
	Name        string `json:"name,omitempty"`
	Zip         string `json:"zip,omitempty"`
}

type cardTokenizeResponse struct {
	Token       string `json:"token"`
	Brand       string `json:"brand"`
	Last4       string `json:"last4"`
	ExpiryMonth string `json:"expiry_month"`
	ExpiryYear  string `json:"expiry_year"`
	Provider    string `json:"provider"`
}

// TokenizeCard accepts raw card data server-side and returns a provider token.
// Raw PAN is never stored; it is forwarded directly to the configured payment
// provider and discarded.
//
// Card tokenization should be done client-side using the Square Web Payments
// SDK. This endpoint returns 503 as server-side tokenization requires PCI DSS
// Level 1 compliance. Use the Square Web Payments SDK (SqPaymentForm) instead.
//
// POST /v1/billing/card/tokenize
func TokenizeCard(c *gin.Context) {
	var req cardTokenizeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	num := strings.ReplaceAll(req.Number, " ", "")
	if len(num) < 13 || len(num) > 19 {
		http.Fail(c, 400, "invalid card number", nil)
		return
	}
	if req.ExpiryMonth == "" || req.ExpiryYear == "" {
		http.Fail(c, 400, "expiry_month and expiry_year are required", nil)
		return
	}
	if req.CVC == "" {
		http.Fail(c, 400, "cvc is required", nil)
		return
	}

	// Server-side card tokenization is not supported. Use the Square Web
	// Payments SDK on the client to obtain a payment token (nonce).
	http.Fail(c, 503, "server-side card tokenization not available; use Square Web Payments SDK", nil)
}
