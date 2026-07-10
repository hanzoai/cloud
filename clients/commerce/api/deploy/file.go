package site

import (
	"github.com/gin-gonic/gin"

	// "github.com/hanzoai/cloud/clients/commerce/datastore"
	// "github.com/hanzoai/cloud/clients/commerce/models/site"
	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/netlify"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

func listFiles(c *gin.Context) {
}

func getFile(c *gin.Context) {
}

func putFile(c *gin.Context) {
	// siteid := c.Param("siteid") // oursiteid
	deployid := c.Param("deployid")
	filepath := c.Param("filepath")

	// db := datastore.New(c)
	// ste := site.New(db)
	// err := ste.GetById(siteid)
	// if err != nil {
	// 	msg := fmt.Sprintf("Site '%s' not found", siteid)
	// 	http.Fail(c, 404, msg, nil)
	// 	return
	// }

	ctx := middleware.GetContext(c)
	org := middleware.GetOrganization(c)
	accessToken := netlify.GetAccessToken(ctx, org.Name)

	url := config.Netlify.BaseUrl + "/deploys/" + deployid + "/files" + filepath
	url += "?access_token=" + accessToken
	log.Debug("Returning redirect, upload file to: %s", url, c)
	c.Redirect(307, url)
}
