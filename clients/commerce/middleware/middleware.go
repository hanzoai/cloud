package middleware

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/mintauth"
)

// RequestContext extracts the standard Go context from the HTTP request
// and stores it in the Gin context for downstream handlers.
//
// It also marks the stored context mint-gated (mintauth.WithGate): every inbound
// request is a potential untrusted principal, so any spendable-balance mint that
// flows from it must carry mint authorization or the ledger sink refuses it. This
// backs up the primary gate in Organization.Namespaced for the rare handler that
// builds a datastore from middleware.GetContext(c) directly. Authorization
// (PlatformOnly / settled payment / server-fixed grant) rides on top.
func RequestContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := mintauth.WithGate(c.Request.Context())
		c.Set("context", ctx)
	}
}

// AppEngine is a legacy alias for RequestContext.
// Deprecated: use RequestContext instead.
var AppEngine = RequestContext

// Automatically get the Host header so we can decide what to do with a given
// request.
func AddHost() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("host", c.Request.Header.Get("Host"))
	}
}

func LiveReload() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		// If 200 and text/html content-type inject bebop script for live reload
		if c.Writer.Status() == 200 && c.Writer.Header().Get("Content-Type") == "text/html; charset=utf-8" {
			injectScript := []byte(`<!-- Live reload via bebop -->
<script src="http://localhost:8090/bebop.min.js"></script>
<script>try {(new Bebop({port: 8090})).connect()} catch(err) {}</script>`)
			c.Writer.Write(injectScript)
		}
	}
}

// GetContext retrieves the request context from the Gin context.
func GetContext(c *gin.Context) context.Context {
	if ctx, exists := c.Get("context"); exists {
		return ctx.(context.Context)
	}
	return c.Request.Context()
}

// GetAppEngine is a legacy alias for GetContext.
// Deprecated: use GetContext instead.
var GetAppEngine = GetContext
