package store

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"

	checkoutApi "github.com/hanzoai/cloud/clients/commerce/api/checkout"
)

func setStore(c *gin.Context) error {
	ctx := middleware.GetContext(c)
	db := datastore.New(ctx)
	id := c.Params.ByName("storeid")

	// Get store
	stor := store.New(db)
	if err := stor.GetById(id); err != nil {
		http.Fail(c, 500, fmt.Sprintf("Failed to retrieve store '%v': %v", id, err), err)
		return err
	}

	// Set store and do authorize
	c.Set("store", stor)
	return nil
}

func authorize(c *gin.Context) {
	if err := setStore(c); err == nil {
		checkoutApi.Authorize(c)
	}
}

func capture(c *gin.Context) {
	if err := setStore(c); err == nil {
		checkoutApi.Capture(c)
	}
}

func charge(c *gin.Context) {
	if err := setStore(c); err == nil {
		checkoutApi.Charge(c)
	}
}

func confirm(c *gin.Context) {
	if err := setStore(c); err == nil {
		checkoutApi.Confirm(c)
	}
}

func cancel(c *gin.Context) {
	if err := setStore(c); err == nil {
		checkoutApi.Cancel(c)
	}
}
