// Package producttaxonomy is the HTTP surface for the product-builder taxonomy:
// options (+values), categories (hierarchical), tags, types, and the
// return/refund reason lookups. All tenant-scoped via middleware.Namespace, so a
// caller only ever touches its own org's taxonomy.
package producttaxonomy

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/productcategory"
	"github.com/hanzoai/cloud/clients/commerce/models/productoption"
	"github.com/hanzoai/cloud/clients/commerce/models/productoptionvalue"
	"github.com/hanzoai/cloud/clients/commerce/models/producttag"
	"github.com/hanzoai/cloud/clients/commerce/models/producttype"
	"github.com/hanzoai/cloud/clients/commerce/models/refundreason"
	"github.com/hanzoai/cloud/clients/commerce/models/returnreason"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	// Options + their values (the variant builder's raw material).
	// The nested param reuses rest's derived ParamId ("product-optionid" =
	// kind + "id"), so the /values subroute shares the same wildcard slot as
	// the default /:product-optionid route. gin panics on sibling wildcards
	// with differing names, so a dash-free "productoptionid" here would panic
	// at wiring. The dash lives only in the internal param name, never in the
	// URL path a client sends.
	options := rest.New(productoption.ProductOption{})
	options.GET("/:product-optionid/values", namespaced, ListOptionValues)
	options.POST("/:product-optionid/values", namespaced, AddOptionValue)
	options.Route(router, args...)

	rest.New(productoptionvalue.ProductOptionValue{}).Route(router, args...)

	// Hierarchical categories. Same wildcard-slot reasoning as options above:
	// the children subroute reuses the derived "product-categoryid" ParamId.
	categories := rest.New(productcategory.ProductCategory{})
	categories.GET("/:product-categoryid/children", namespaced, ListCategoryChildren)
	categories.Route(router, args...)

	rest.New(producttag.ProductTag{}).Route(router, args...)
	rest.New(producttype.ProductType{}).Route(router, args...)

	// Reason lookups for the returns/refunds flows.
	rest.New(returnreason.ReturnReason{}).Route(router, args...)
	rest.New(refundreason.RefundReason{}).Route(router, args...)
}

// ListOptionValues returns the values belonging to a product option.
func ListOptionValues(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	optionId := c.Params.ByName("product-optionid")

	// Verify the option exists (scopes the 404 to this org's data).
	opt := productoption.New(db)
	if err := opt.GetById(optionId); err != nil {
		http.Fail(c, 404, "No product option found with id: "+optionId, err)
		return
	}

	values := make([]*productoptionvalue.ProductOptionValue, 0, 16)
	if _, err := productoptionvalue.Query(db).Filter("OptionId=", opt.Id()).GetAll(&values); err != nil {
		http.Fail(c, 500, "Failed to list option values", err)
		return
	}
	http.Render(c, 200, values)
}

// AddOptionValue creates a value under a product option.
func AddOptionValue(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	optionId := c.Params.ByName("product-optionid")

	opt := productoption.New(db)
	if err := opt.GetById(optionId); err != nil {
		http.Fail(c, 404, "No product option found with id: "+optionId, err)
		return
	}

	v := productoptionvalue.New(db)
	if err := json.Decode(c.Request.Body, v); err != nil {
		http.Fail(c, 400, "Failed to decode request body", err)
		return
	}
	if v.Value == "" {
		http.Fail(c, 400, "value is required", errors.New("missing value"))
		return
	}
	v.OptionId = opt.Id() // the path option wins over any body value
	if err := v.Create(); err != nil {
		http.Fail(c, 500, "Failed to create option value", err)
		return
	}
	http.Render(c, 201, v)
}

// ListCategoryChildren returns the direct children of a category.
func ListCategoryChildren(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	categoryId := c.Params.ByName("product-categoryid")

	parent := productcategory.New(db)
	if err := parent.GetById(categoryId); err != nil {
		http.Fail(c, 404, "No product category found with id: "+categoryId, err)
		return
	}

	children := make([]*productcategory.ProductCategory, 0, 16)
	if _, err := productcategory.Query(db).Filter("ParentId=", parent.Id()).GetAll(&children); err != nil {
		http.Fail(c, 500, "Failed to list category children", err)
		return
	}
	http.Render(c, 200, children)
}
