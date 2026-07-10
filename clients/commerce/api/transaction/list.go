package transaction

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

func List(c *gin.Context) {
	id := c.Params.ByName("id")
	kind := c.Params.ByName("kind")

	org := middleware.GetOrganization(c)
	ctx := org.Namespaced(c)

	res, err := util.GetTransactions(ctx, id, kind, !org.Live)

	if err != nil {
		log.Error("transaction/%v/%v error: '%v'", id, kind, err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	http.Render(c, 200, res)
}
