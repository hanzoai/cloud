package site

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/site"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/netlify"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

func createDeploy(c *gin.Context) {
	ctx := middleware.GetContext(c)
	org := middleware.GetOrganization(c)
	siteid := c.Params.ByName("siteid")

	// Get associated site
	db := datastore.New(ctx)
	ste := site.New(db)
	err := ste.GetById(siteid)
	if err != nil {
		err := errors.New("Failed to get site")
		http.Fail(c, 500, err.Error(), err)
		return
	}

	// Decode digest
	digest := &netlify.Digest{}
	err = json.Decode(c.Request.Body, digest)
	if err != nil {
		err := errors.New("Failed to decode digest")
		http.Fail(c, 500, err.Error(), err)
	}

	// Get access token for organization
	accessToken := netlify.GetAccessToken(ctx, org.Name)

	// Create deploy
	client := netlify.New(ctx, accessToken)
	deploy, err := client.CreateDeploy(ste.Netlify(), digest, false)

	deploy.SiteId = siteid // Override netlify's site id with ours

	if err != nil {
		http.Fail(c, 500, "Failed to create deploy", err)
		return
	}

	http.Render(c, 201, deploy)
}

func getDeploy(c *gin.Context) {
}

func listDeploys(c *gin.Context) {
}

func restoreDeploy(c *gin.Context) {
}
