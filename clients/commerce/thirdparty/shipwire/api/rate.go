package api

import (
	"fmt"
	"net/http/httputil"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

func rate(c *gin.Context) {
	dump, _ := httputil.DumpRequest(c.Request, true)
	log.Info("Rate request:\n%s", dump, c)

	ord := new(order.Order)
	if err := json.Decode(c.Request.Body, ord); err != nil {
		http.Fail(c, 400, fmt.Errorf("Failed to decode request body: %v", err), err)
		return
	}

	org := middleware.GetOrganization(c)
	client := shipwire.New(c, org.Shipwire.Username, org.Shipwire.Password)
	rates, res, err := client.Rate(ord)
	if err == nil {
		http.Render(c, 200, rates)
	} else {
		http.Fail(c, res.Status, fmt.Errorf("Failed to get rates from Shipwire: %v", err), err)
	}
}
