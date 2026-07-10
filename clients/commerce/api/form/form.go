package form

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/form"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// handle form submissions
func handleForm(c *gin.Context) {
	db := datastore.New(c)
	id := c.Params.ByName("formid")
	org := organization.New(db)
	f := form.New(db)

	// Set form key
	f.SetKey(id)

	// Get namepsace
	ns := f.Key().Namespace()

	// Get organization for form
	if err := org.GetById(ns); err != nil {
		log.Error("Organization not found: %v ?= %v,  %v", ns, org.Name, org.Id_, c)
		http.Fail(c, 404, fmt.Sprintf("Failed to retrieve organization '%v': %v", ns, err), err)
		return
	}
	log.Info("Organization: %v ?= %v,  %v", ns, org.Name, org.Id_, c)

	// Set namespace to match organization's
	f.SetNamespace(ns)

	// Mailing list doesn't exist
	if err := f.Get(nil); err != nil {
		http.Fail(c, 404, fmt.Sprintf("Failed to retrieve form '%v': %v", id, err), err)
		return
	}

	// Get namespaced db
	db = f.Datastore()

	switch f.Type {
	case form.Submit:
		submit(c, db, org, f)
	default:
		//case form.Subscribe:
		subscribe(c, db, org, f)
	}
}
