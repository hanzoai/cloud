package cdn

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/api/cdn/analytics"
	"github.com/hanzoai/cloud/clients/commerce/api/cdn/form"
	"github.com/hanzoai/cloud/clients/commerce/api/cdn/native"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(r router.Router, args ...gin.HandlerFunc) {
	a := r.Group("/a/")
	a.GET(":organizationid", analytics.Js)
	a.GET(":organizationid/analytics.js", analytics.Js)
	a.GET(":organizationid/js", analytics.Js)

	f := r.Group("/f/")
	f.GET(":formid/form.js", form.Js)
	f.GET(":formid/js", form.Js)

	// DEPRECATED
	m := r.Group("/m/")
	m.GET(":formid/mailinglist.js", form.Js)
	m.GET(":formid/js", form.Js)

	n := r.Group("/n/")
	n.GET(":organizationid/native.js", native.Js)
}
