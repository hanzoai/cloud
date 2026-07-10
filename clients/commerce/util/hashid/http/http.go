package http

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/util/hashid"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
	"github.com/hanzoai/cloud/clients/commerce/util/template"
)

func decodeKey(c *gin.Context) {
	ctx := middleware.GetContext(c)
	id := c.Params.ByName("id")
	key, err := hashid.DecodeKey(ctx, id)
	if err != nil {
		panic(err)
	}
	template.Render(c, "hashid.html",
		"id", id,
		"namespace", key.Namespace(),
		"kind", key.Kind(),
		"parent", key.Parent(),
		"intid", key.IntID(),
	)
}

// Setup handlers for HTTP registered tasks
func SetupRoutes(router router.Router) {
	// Redirects
	router.GET("/hashid", func(c *gin.Context) {
		template.Render(c, "hashid.html")
	})

	// Check a hashid
	router.GET("/hashid/:id", decodeKey)
	router.POST("/hashid/:id", decodeKey)
}
