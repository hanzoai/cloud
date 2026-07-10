package subscription

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/kms"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

var subscriptionEndpoint = config.UrlFor("api", "/subscription/")

// hydrateOrg populates payment credentials from KMS onto the org.
func hydrateOrg(c *gin.Context, org *organization.Organization) {
	if v, ok := c.Get("kms"); ok {
		if kmsClient, ok := v.(*kms.CachedClient); ok {
			if err := kms.Hydrate(kmsClient, org); err != nil {
				log.Error("KMS hydration failed for org %q: %v", org.Name, err, c)
			}
		}
	}
}

func getSubscription(c *gin.Context) (*subscription.Subscription, error) {
	// Get organization for this user
	org := middleware.GetOrganization(c)

	// Set up the db with the namespaced context
	ctx := org.Namespaced(c)
	db := datastore.New(ctx)

	// Create order that's properly namespaced
	sub := subscription.New(db)

	// Get order if an existing order was referenced
	if id := c.Params.ByName("subscriptionid"); id != "" {
		if err := sub.GetById(id); err != nil {
			return nil, err
		}
	}

	return sub, nil
}

func Subscribe(c *gin.Context) {
	org := middleware.GetOrganization(c)
	hydrateOrg(c, org)

	sub, _, err := subscribe(c, org)
	if err != nil {
		http.Fail(c, 500, "Error during subscribe", err)
		return
	}

	c.Writer.Header().Add("Location", subscriptionEndpoint+sub.Id())
	num, err := sub.NumberFromId()
	if err != nil {
		http.Fail(c, 500, "Error during subscribe", err)
		return
	}
	sub.Number = num

	http.Render(c, 200, sub)
}

func GetSubscribe(c *gin.Context) {
	sub, err := getSubscription(c)
	if err != nil {
		http.Fail(c, 404, "No subscription found", err)
		return
	}

	num, err := sub.NumberFromId()
	if err != nil {
		http.Fail(c, 500, "Error during subscribe", err)
		return
	}
	sub.Number = num
	http.Render(c, 200, sub)
}

func UpdateSubscribe(c *gin.Context) {
	org := middleware.GetOrganization(c)
	hydrateOrg(c, org)
	sub, err := getSubscription(c)
	if err != nil {
		http.Fail(c, 404, "No subscription found", err)
		return
	}

	_, err = updateSubscribe(c, org, sub)
	if err != nil {
		http.Fail(c, 500, "Error during subscribe", err)
		return
	}

	num, err := sub.NumberFromId()
	if err != nil {
		http.Fail(c, 500, "Error during subscribe", err)
		return
	}
	sub.Number = num

	http.Render(c, 200, sub)
}

func Unsubscribe(c *gin.Context) {
	org := middleware.GetOrganization(c)
	hydrateOrg(c, org)
	sub, err := getSubscription(c)
	if err != nil {
		http.Fail(c, 404, "No subscription found", err)
		return
	}

	_, err = unsubscribe(c, org, sub)
	if err != nil {
		http.Fail(c, 500, "Error during subscribe", err)
		return
	}

	num, err := sub.NumberFromId()
	if err != nil {
		http.Fail(c, 500, "Error during subscribe", err)
		return
	}
	sub.Number = num

	http.Render(c, 200, sub)
}

func Route(router router.Router, args ...gin.HandlerFunc) {
	api := router.Group("")
	api.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
	})

	publishedRequired := middleware.TokenRequired(permission.Admin, permission.Published)

	// Charge Payment API
	api.POST("/subscribe", publishedRequired, Subscribe)
	api.GET("/subscribe/:subscriptionid", publishedRequired, GetSubscribe)
	api.PATCH("/subscribe/:subscriptionid", publishedRequired, UpdateSubscribe)
	api.DELETE("/subscribe/:subscriptionid", publishedRequired, Unsubscribe)
}
