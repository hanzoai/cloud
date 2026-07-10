package analytics

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/fs"
)

var (
	jsTemplate   = ""
	requireRegex = regexp.MustCompile(`require\(['"]./index['"]\)|,\w\(['"]./index['"]\)`)
)

func Render(org *organization.Organization) string {
	if jsTemplate == "" {
		var cwd, _ = os.Getwd()
		bundleJs := string(fs.ReadFile(cwd + "/resources/analytics/bundle.js"))
		jsTemplate = string(fs.ReadFile(cwd + "/resources/analytics/analytics.js"))
		jsTemplate = requireRegex.ReplaceAllString(jsTemplate, ";"+bundleJs)
		jsTemplate = strings.Replace(jsTemplate, "analytics.initialize({})", "analytics.initialize(%s)", 1)
	}

	return fmt.Sprintf(jsTemplate, org.Analytics.SnippetJSON())
}

func Js(c *gin.Context) {
	id := c.Params.ByName("organizationid")

	// Passed organizationid as part of organization.js, strip extension.
	if strings.Contains(id, ".") {
		id = strings.Split(id, ".")[0]
	}

	db := datastore.New(c)

	org := organization.New(db)
	if err := org.GetById(id); err != nil {
		c.String(404, fmt.Sprintf("Failed to retrieve organization '%v': %v", id, err))
		return
	}

	c.Writer.Header().Add("Content-Type", "application/javascript")
	c.String(200, Render(org))
}
