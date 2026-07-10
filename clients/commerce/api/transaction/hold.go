package transaction

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

type CreateHoldReq struct {
	SourceId   string         `json:"sourceId"`
	SourceKind string         `json:"sourceKind"`
	Currency   currency.Type  `json:"currency"`
	Amount     currency.Cents `json:"amount"`
	Notes      string         `json:"notes"`
	Tags       string         `json:"tags"`
	Event      string         `json:"event"`
	Metadata   Map            `json:"metadata"`
}

func CreateHold(c *gin.Context) {
	// Money move (places a hold against a balance): admin-only, enforced inside
	// the handler (route middleware no-ops on the IAM path — Red HIGH-4).
	if !middleware.RequireAdmin(c) {
		return
	}
	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))
	req := &CreateHoldReq{}

	// Decode response body to create new request
	if err := json.Decode(c.Request.Body, req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	trans := transaction.New(db)
	trans.SourceId = req.SourceId
	trans.SourceKind = req.SourceKind
	trans.Currency = req.Currency
	trans.Amount = req.Amount
	trans.Notes = req.Notes
	trans.Tags = req.Tags
	trans.Event = req.Event
	trans.Metadata = req.Metadata
	trans.Type = transaction.Hold

	if trans.Amount == currency.Cents(0) {
		log.Error(ErrorPointlessTransaction.Error(), c)
		http.Fail(c, 500, ErrorPointlessTransaction.Error(), ErrorPointlessTransaction)
		return
	}

	if trans.Currency == "" {
		log.Error(ErrorCurrencyRequired.Error(), c)
		http.Fail(c, 500, ErrorCurrencyRequired.Error(), ErrorCurrencyRequired)
		return
	}

	if !org.Live {
		log.Info("Hold created in test mode.", c)
		trans.Test = true
	}

	err := db.RunInTransaction(func(db *datastore.Datastore) error {
		datas, err := util.GetTransactionsByCurrency(db.Context, trans.SourceId, trans.SourceKind, trans.Currency, !org.Live)
		if err != nil {
			return err
		}

		data := datas.Data[trans.Currency]

		if data == nil {
			log.Error("Source has no funds'%v'", c)
			return ErrorInsufficientFunds
		}

		if data.Balance-data.Holds < trans.Amount {
			log.Error("Source has insufficient funds '%v' - '%v' < '%v'", data.Balance, data.Holds, trans.Amount, c)
			return ErrorInsufficientFunds
		}

		return trans.Create()
	}, nil)

	if err != nil {
		http.Fail(c, 500, err.Error(), err)
	} else {
		c.Writer.Header().Add("Location", c.Request.URL.Path+"/"+trans.Id())
		http.Render(c, 201, trans)
	}
}

func RemoveHold(c *gin.Context) {
	// Money move (releases a hold): admin-only, enforced inside the handler
	// (route middleware no-ops on the IAM path — Red HIGH-4).
	if !middleware.RequireAdmin(c) {
		return
	}
	id := c.Params.ByName("id")

	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))

	trans := transaction.New(db)
	err := db.RunInTransaction(func(db *datastore.Datastore) error {
		if err := trans.GetById(id); err != nil {
			return err
		}

		trans.Type = transaction.HoldRemoved
		if err := trans.Update(); err != nil {
			return err
		}

		return nil
	}, nil)

	if err != nil {
		http.Fail(c, 500, err.Error(), err)
		return
	}

	http.Render(c, 201, trans)
}
