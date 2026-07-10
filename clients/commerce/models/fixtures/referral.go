package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/referral"
)

var Referral = New("referral", func(c *gin.Context) *referral.Referral {
	// Get namespaced db
	db := getNamespaceDb(c)

	ord := Order(c)
	u := User(c)

	// Referral
	ref := referral.New(db)
	ref.UserId = u.Id()
	ref.OrderId = ord.Id()
	ref.GetOrCreate("OrderId=", ref.OrderId)
	ref.MustPut()

	return ref
})
