package order

import (
	"github.com/gin-gonic/gin"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	// "github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

type StatusResponse struct {
	Id            string         `json:"id"`
	Total         currency.Cents `json:"total"`
	Paid          currency.Cents `json:"paid"`
	Currency      currency.Type  `json:"currency"`
	Status        order.Status   `json:"status"`
	PaymentStatus payment.Status `json:"paymentStatus"`
	Wallet        *wallet.Wallet `json:"wallet,omitempty"`
}

func Status(c *gin.Context) {
	id := c.Params.ByName("orderid")
	// Per-org store (Red MED-1): read the order from the caller org's store, not
	// the shared systemDB (datastore.New drops the namespace + binds systemDB).
	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))
	ord := order.New(db)

	// Ensure order exists
	if err := ord.GetById(id); err != nil {
		http.Fail(c, 404, "No order found with id: "+id, err)
		return
	}

	wal, err := ord.GetOrCreateWallet(db)
	if err != nil {
		log.Warn("Order '%v' has no wallet due to error: '%v'", ord.Id_, err, c)
	}

	res := &StatusResponse{
		Id:            ord.Id_,
		Total:         ord.Total,
		Paid:          ord.Paid,
		Currency:      ord.Currency,
		Status:        ord.Status,
		PaymentStatus: ord.PaymentStatus,
		Wallet:        wal,
	}

	http.Render(c, 200, res)
}
