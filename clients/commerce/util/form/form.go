package form

import (
	"github.com/gin-gonic/gin"
	"github.com/gorilla/schema"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

var decoder = schema.NewDecoder()

func Parse(c *gin.Context, form interface{}) error {
	decoder.IgnoreUnknownKeys(true)
	c.Request.ParseForm()
	err := decoder.Decode(form, c.Request.PostForm)
	if err != nil {
		log.Panic("Parsing form %#v", err)
	}
	return err
}

// // TODO: Make this go away
// func SchemaFix(order *models.Order) {
// 	// Schema creates the Order.Items slice sized to whatever is the largest
// 	// index form item. This creates a slice with a huge number of nil structs,
// 	// so we create a new slice of items and use that instead.
// 	items := make([]models.LineItem, 0)
// 	for _, item := range order.Items {
// 		if item.SKU() != "" {
// 			items = append(items, item)
// 		}
// 	}
// 	order.Items = items
// }
