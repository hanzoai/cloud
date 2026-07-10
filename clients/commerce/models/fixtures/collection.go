package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/collection"
)

var Collection = New("collection", func(c *gin.Context) *collection.Collection {
	db := getNamespaceDb(c)

	collection := collection.New(db)
	collection.Slug = "such-tees-pack"
	collection.GetOrCreate("Slug=", collection.Slug)
	collection.Name = "Such tees pack"
	collection.Description = "Much tees in one pack!"
	collection.Published = true
	collection.Available = true

	collection.MustPut()
	return collection
})
