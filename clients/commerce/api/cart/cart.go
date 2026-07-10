package cart

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/cart"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"
	"github.com/hanzoai/cloud/clients/commerce/util/hashid"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
)

type SetReq struct {
	Quantity    int    `json:"quantity"`
	ProductId   string `json:"productId"`
	ProductSlug string `json:"productSlug"`
	VariantSKU  string `json:"variantSku"`
}

type CartResponse struct {
	Id string `json:"id"`
}

func Set(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Params.ByName("cartid")

	// Get cart, fail if it doesn't exist
	car := cart.New(db)
	if err := car.GetById(id); err != nil {
		http.Fail(c, 404, "No cart found with id: "+id, err)
		return
	}

	// Decode request
	req := SetReq{}
	if err := json.Decode(c.Request.Body, &req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	var setId string

	// Try to figure out what sort of item we are setting
	var typ string
	if req.ProductId != "" {
		key, err := hashid.DecodeKey(db.Context, req.ProductId)
		if err != nil {
			http.Fail(c, 400, "Failed to decode id", err)
			return
		}
		setId = req.ProductId

		typ = key.Kind()
	} else if req.ProductSlug != "" {
		typ = "product"
		setId = req.ProductSlug
	} else if req.VariantSKU != "" {
		typ = "variant"
		setId = req.VariantSKU
	} else {
		http.Fail(c, 400, "No product or variant specified", errors.New("No product or variant specified"))
		return
	}

	// Update cart with new item quantity information
	if err := car.SetItem(db, setId, typ, req.Quantity); err != nil {
		http.Fail(c, 400, "Failed to update line item", err)
		return
	}

	if car.Mailchimp.CheckoutUrl == "" {
		car.Mailchimp.CheckoutUrl = org.Mailchimp.CheckoutUrl
	}

	// Update cart in datastore
	if err := car.Update(); err != nil {
		http.Fail(c, 500, "Failed to update cart", err)
	} else {
		http.Render(c, 200, car)
	}

	// Determine store to use
	storeId := car.StoreId
	if storeId == "" {
		storeId = org.DefaultStore
	}

	// Update Mailchimp cart
	if car.UserId != "" || car.Email != "" {
		client := mailchimp.New(db.Context, org.Mailchimp)
		client.UpdateOrCreateCart(storeId, car)
	}
}

func Discard(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Params.ByName("cartid")

	// Get cart, fail if it doesn't exist
	car := cart.New(db)
	if err := car.GetById(id); err != nil {
		http.Fail(c, 404, "No cart found with id: "+id, err)
		return
	}

	car.Status = cart.Discarded

	// Update cart in datastore
	if err := car.Update(); err != nil {
		http.Fail(c, 500, "Failed to update cart", err)
	} else {
		http.Render(c, 200, CartResponse{Id: car.Id()})
	}

	// Determine store to use
	storeId := car.StoreId
	if storeId == "" {
		storeId = org.DefaultStore
	}

	// Update Mailchimp cart
	if car.UserId != "" || car.Email != "" {
		client := mailchimp.New(db.Context, org.Mailchimp)
		client.DeleteCart(storeId, car)
	}
}

func create(r *rest.Rest) func(*gin.Context) {
	return func(c *gin.Context) {
		if !r.CheckPermissions(c, "create") {
			return
		}

		org := middleware.GetOrganization(c)
		db := datastore.New(org.Namespaced(c))
		car := cart.New(db)

		if err := json.Decode(c.Request.Body, car); err != nil {
			r.Fail(c, 400, "Failed decode request body", err)
			return
		}

		if car.Mailchimp.CheckoutUrl == "" {
			car.Mailchimp.CheckoutUrl = org.Mailchimp.CheckoutUrl
		}

		if err := car.Create(); err != nil {
			r.Fail(c, 500, "Failed to create "+r.Kind, err)
			return
		}

		// Determine store to use
		storeId := car.StoreId
		if storeId == "" {
			storeId = org.DefaultStore
		}

		// Create Mailchimp cart
		if car.UserId != "" || car.Email != "" {
			client := mailchimp.New(db.Context, org.Mailchimp)
			client.CreateCart(storeId, car)
		}

		c.Writer.Header().Add("Location", c.Request.URL.Path+"/"+car.Id())
		r.Render(c, 201, car)
	}
}

// Completely replaces an cart for given `id`.
func update(r *rest.Rest) func(*gin.Context) {
	return func(c *gin.Context) {
		if !r.CheckPermissions(c, "update") {
			return
		}

		id := c.Params.ByName(r.ParamId)

		org := middleware.GetOrganization(c)
		db := datastore.New(org.Namespaced(c))
		car := cart.New(db)

		// Try to retrieve key from datastore
		key, ok, err := car.IdExists(id)
		if !ok {
			r.Fail(c, 404, "No "+r.Kind+" found with id: "+id, err)
			return
		}

		if err != nil {
			r.Fail(c, 500, "Failed to retrieve key for "+id, err)
			return
		}

		// Decode response body to create new cart
		if err := json.Decode(c.Request.Body, car); err != nil {
			r.Fail(c, 400, "Failed decode request body", err)
			return
		}

		if car.Mailchimp.CheckoutUrl == "" {
			car.Mailchimp.CheckoutUrl = org.Mailchimp.CheckoutUrl
		}

		// Use same key to save cart
		car.SetKey(key)

		// Replace whatever was in the datastore with our new updated cart
		if err := car.Update(); err != nil {
			r.Fail(c, 500, "Failed to update "+r.Kind, err)
		} else {
			r.Render(c, 200, car)
		}

		// Determine store to use
		storeId := car.StoreId
		if storeId == "" {
			storeId = org.DefaultStore
		}

		// Update Mailchimp cart
		if car.UserId != "" || car.Email != "" {
			client := mailchimp.New(db.Context, org.Mailchimp)
			client.UpdateOrCreateCart(storeId, car)
		}
	}
}

// Partially updates pre-existing cart by given `id`.
func patch(r *rest.Rest) func(*gin.Context) {
	return func(c *gin.Context) {
		if !r.CheckPermissions(c, "patch") {
			return
		}

		id := c.Params.ByName(r.ParamId)

		org := middleware.GetOrganization(c)
		db := datastore.New(org.Namespaced(c))
		car := cart.New(db)

		err := car.GetById(id)

		if err != nil {
			r.Fail(c, 404, "No "+r.Kind+" found with id: "+id, err)
			return
		}

		if err := json.Decode(c.Request.Body, car); err != nil {
			r.Fail(c, 400, "Failed decode request body", err)
			return
		}

		if car.Mailchimp.CheckoutUrl == "" {
			car.Mailchimp.CheckoutUrl = org.Mailchimp.CheckoutUrl
		}

		if err := car.Update(); err != nil {
			r.Fail(c, 500, "Failed to update "+r.Kind, err)
		} else {
			r.Render(c, 200, car)
		}

		// Determine store to use
		storeId := car.StoreId
		if storeId == "" {
			storeId = org.DefaultStore
		}

		// Update Mailchimp cart
		if car.UserId != "" || car.Email != "" {
			client := mailchimp.New(db.Context, org.Mailchimp)
			client.UpdateOrCreateCart(storeId, car)
		}
	}
}
