package gincontext

import (
	"testing"

	"context"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

func New(ctx context.Context) *gin.Context {
	// Setup default context.Context for tests
	c := new(gin.Context)
	SetDefaults(c, ctx)
	return c
}

func SetDefaults(c *gin.Context, ctx context.Context) {
	c.Set("context", ctx)
	c.Set("verbose", testing.Verbose())
	c.Set("test", false)
	db := datastore.New(ctx)
	c.Set("organization", organization.New(db))
}
