package form

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/form"
)

func Js(c *gin.Context) {
	id := c.Params.ByName("formid")
	db := datastore.New(c)

	f := form.New(db)

	// Set key and namespace correctly
	f.SetKey(id)
	log.Debug("form: %v", f, c)
	log.Debug("key: %v", f.Key(), c)
	namespace := f.Key().Namespace()
	log.Warn("namespace: %v", namespace, c)
	f.SetNamespace(namespace)

	if err := f.Get(nil); err != nil {
		log.Error("Failed to retrieve form '%s' in namespace '%s': %v", id, namespace, err, c)
		c.String(404, fmt.Sprintf("Failed to retrieve form '%v': %v", id, err))
		return
	}

	c.Writer.Header().Add("Content-Type", "application/javascript")
	c.String(200, f.Js())
}
