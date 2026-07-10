package native

import (
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/fs"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

var jsTemplate string

func Js(c *gin.Context) {
	db := datastore.New(c)

	id := c.Params.ByName("organizationid")
	org := organization.New(db)
	if err := org.GetById(id); err != nil {
		http.Fail(c, 400, "Failed to get organization", err)
		return
	}

	if jsTemplate == "" {
		var cwd, _ = os.Getwd()
		jsTemplate = string(fs.ReadFile(cwd + "/js/native.js"))
	}

	c.Writer.Header().Add("Content-Type", "application/javascript")

	script := strings.Replace(jsTemplate, "%%%%%url%%%%%", config.UrlFor("analytics", "/"+org.Id()+"/"), -1)

	c.String(200, script)
}
