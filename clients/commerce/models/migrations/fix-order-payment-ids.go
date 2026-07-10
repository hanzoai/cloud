package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("flag-order-payment-ids",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "bellabeat")
		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		var pays []*payment.Payment
		update := false

		for _, pid := range ord.PaymentIds {
			pay := payment.New(db)
			if err := pay.GetById(pid); err != nil {
				update = true
				break
			}
		}

		if !update {
			return
		}

		if _, err := payment.Query(db).Filter("OrderId=", ord.Id()).GetAll(&pays); err != nil {
			log.Error(err, db.Context)
			return
		}

		paymentIds := make([]string, len(pays))
		for i, pay := range pays {
			paymentIds[i] = pay.Id()
		}

		ord.PaymentIds = paymentIds
		if err := ord.Put(); err != nil {
			log.Error(err, db.Context)
		}
	},
)
