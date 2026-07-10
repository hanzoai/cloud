package paypal

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type PayKeyResponse struct {
	order.Order

	PayKey string `json:"payKey"`
}

func Confirm(c *gin.Context, org *organization.Organization, ord *order.Order) (err error) {
	// Per-org store: payments live in the order's org store (Red MED-1). Raw
	// datastore.New(c) drops the namespace (gin.Context → Background) AND binds
	// systemDB, so it would query the wrong store.
	db := datastore.NewNamespaced(org.Namespaced(c))

	payments := make([]*payment.Payment, 0)

	if payKey := c.Params.ByName("payKey"); payKey != "" {
		_, err = payment.Query(db).Filter("Account.PayKey=", payKey).GetAll(&payments)
		if err != nil {
			return PaymentDoesNotExist
		}
	}

	if len(payments) == 0 {
		return PaymentDoesNotExist
	}

	for _, pay := range payments {
		pay.Status = payment.Paid
	}

	ord.PaymentStatus = payment.Paid
	ord.Payments = payments
	ord.MustPut()

	return nil
}

func Cancel(c *gin.Context, org *organization.Organization, ord *order.Order) (err error) {
	// Per-org store (Red MED-1) — see Confirm above.
	db := datastore.NewNamespaced(org.Namespaced(c))

	var keys []iface.Key
	var payments []*payment.Payment

	payments = make([]*payment.Payment, 0)

	if payKey := c.Params.ByName("payKey"); payKey != "" {
		keys, err = payment.Query(db).Filter("Account.PayKey=", payKey).GetAll(&payments)
		if err != nil {
			return PaymentDoesNotExist
		}
	}

	if len(payments) == 0 {
		http.Fail(c, 404, "Failed to retrieve payment", PaymentDoesNotExist)
		return
	}

	for i, pay := range payments {
		pay.Init(db)
		pay.SetKey(keys[i])
		pay.Status = payment.Cancelled
		pay.Account.Error = PaymentCancelled.Error()
		pay.MustPut()
	}

	ord.Status = order.Cancelled
	ord.PaymentStatus = payment.Cancelled
	ord.MustPut()

	return nil
}
