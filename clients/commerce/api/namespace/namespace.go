package namespace

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

type Organization struct {
	Name string
}

// Get id from namespace
func idFromNamespace(c *gin.Context) {
	namespace := c.Params.ByName("namespace")
	db := datastore.New(c)
	key, ok, err := db.Query("organization").Filter("Name=", namespace).KeysOnly().First(nil)
	if !ok {
		log.Panic("Query for organization failed", c)
	}
	if err != nil {
		log.Panic("Query for organization failed: %v", err, c)
	}
	if !ok {
		log.Panic("Failed to retrieve organization named '%v'", namespace, err, c)
	}

	id := key.IntID()
	c.String(200, strconv.Itoa(int(id)))
}

// Get namespace from id
func namespaceFromId(c *gin.Context) {
	v, err := strconv.Atoi(c.Params.ByName("id"))
	if err != nil {
		log.Panic("Unable to convert id to int", err, c)
	}
	id := int64(v)
	db := datastore.New(c)

	var org Organization
	key := db.NewKey("organization", "", id, nil)
	_, ok, err := db.Query("organization").Filter("__key__=", key).Project("Name").First(&org)
	if !ok {
		log.Panic("Query for organization failed", c)
	}
	if err != nil {
		log.Panic("Query for organization failed: %v", err, c)
	}
	if !ok {
		log.Panic("Failed to retrieve organization with IntID '%v'", id, err, c)
	}

	c.String(200, org.Name)
}
