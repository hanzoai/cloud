package x402

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// contextKey is a private type to avoid collisions in context values.
type contextKey string

const (
	// PaymentReceiptKey is the gin context key for the payment receipt.
	PaymentReceiptKey = "x402.receipt"
)

// Middleware returns a Gin middleware that enforces x402 payment for configured routes.
// Routes not listed in cfg.Routes pass through without payment.
//
// Usage:
//
//	router.Use(x402.Middleware(cfg, facilitator))
func Middleware(cfg *PaywallConfig, facilitator *Facilitator) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Find matching route config for this request path.
		route := matchRoute(cfg, c.Request.URL.Path)
		if route == nil {
			// No payment required for this route.
			c.Next()
			return
		}

		// Check for payment authorization header.
		authHeader := c.GetHeader(HeaderPaymentAuthorization)
		if authHeader == "" {
			// No payment provided — return 402 with payment request.
			paymentReq := NewPaymentRequest(cfg, route, c.Request.URL.Path)
			c.Header(HeaderPaymentRequest, paymentReq.MarshalHeader())
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error":           "payment required",
				"payment_request": paymentReq,
			})
			c.Abort()
			return
		}

		// Parse the payment authorization.
		auth, err := ParsePaymentAuthorization(authHeader)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "invalid payment authorization: " + err.Error(),
			})
			c.Abort()
			return
		}

		// Check time bounds.
		if auth.IsExpired() {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": "payment authorization expired",
			})
			c.Abort()
			return
		}
		if auth.IsNotYetValid() {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": "payment authorization not yet valid",
			})
			c.Abort()
			return
		}

		// Validate the payment amount matches what we asked for.
		if auth.Value != route.Amount {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": "payment amount mismatch",
			})
			c.Abort()
			return
		}

		// Settle the payment via the facilitator.
		paymentReq := NewPaymentRequest(cfg, route, c.Request.URL.Path)
		receipt, err := facilitator.Settle(c.Request.Context(), paymentReq, auth)
		if err != nil {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": "payment settlement failed: " + err.Error(),
			})
			c.Abort()
			return
		}

		if !receipt.Success {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": "payment settlement rejected",
			})
			c.Abort()
			return
		}

		// Attach the receipt to the context and response.
		receiptJSON, _ := json.Marshal(receipt)
		c.Header(HeaderPaymentReceipt, string(receiptJSON))
		c.Set(PaymentReceiptKey, receipt)

		c.Next()
	}
}

// NetHTTPMiddleware returns a standard net/http middleware for x402 payment.
// For use outside Gin (e.g., chi, standard http.Handler chains).
func NetHTTPMiddleware(cfg *PaywallConfig, facilitator *Facilitator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route := matchRoute(cfg, r.URL.Path)
			if route == nil {
				next.ServeHTTP(w, r)
				return
			}

			authHeader := r.Header.Get(HeaderPaymentAuthorization)
			if authHeader == "" {
				paymentReq := NewPaymentRequest(cfg, route, r.URL.Path)
				w.Header().Set(HeaderPaymentRequest, paymentReq.MarshalHeader())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error":           "payment required",
					"payment_request": paymentReq,
				})
				return
			}

			auth, err := ParsePaymentAuthorization(authHeader)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{
					"error": "invalid payment authorization: " + err.Error(),
				})
				return
			}

			if auth.IsExpired() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				json.NewEncoder(w).Encode(map[string]string{
					"error": "payment authorization expired",
				})
				return
			}

			if auth.Value != route.Amount {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				json.NewEncoder(w).Encode(map[string]string{
					"error": "payment amount mismatch",
				})
				return
			}

			paymentReq := NewPaymentRequest(cfg, route, r.URL.Path)
			receipt, err := facilitator.Settle(r.Context(), paymentReq, auth)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				json.NewEncoder(w).Encode(map[string]string{
					"error": "payment settlement failed: " + err.Error(),
				})
				return
			}

			if !receipt.Success {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				json.NewEncoder(w).Encode(map[string]string{
					"error": "payment settlement rejected",
				})
				return
			}

			receiptJSON, _ := json.Marshal(receipt)
			w.Header().Set(HeaderPaymentReceipt, string(receiptJSON))
			next.ServeHTTP(w, r)
		})
	}
}

// GetReceipt retrieves the payment receipt from the Gin context.
// Returns nil if no payment was processed for this request.
func GetReceipt(c *gin.Context) *PaymentReceipt {
	val, exists := c.Get(PaymentReceiptKey)
	if !exists {
		return nil
	}
	receipt, ok := val.(*PaymentReceipt)
	if !ok {
		return nil
	}
	return receipt
}

// matchRoute finds the RouteConfig that matches the given path.
// Supports exact matches and prefix matches (paths ending with /*).
func matchRoute(cfg *PaywallConfig, path string) *RouteConfig {
	if cfg.Routes == nil {
		return nil
	}

	// Exact match first.
	if route, ok := cfg.Routes[path]; ok {
		return route
	}

	// Prefix match: check paths ending with /*
	for pattern, route := range cfg.Routes {
		if strings.HasSuffix(pattern, "/*") {
			prefix := strings.TrimSuffix(pattern, "/*")
			if strings.HasPrefix(path, prefix) {
				return route
			}
		}
	}

	return nil
}
