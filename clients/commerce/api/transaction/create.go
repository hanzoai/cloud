package transaction

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

func Create(c *gin.Context) {
	// Money move (deposit/withdraw/transfer on the ledger): admin-only, enforced
	// inside the handler (route middleware no-ops on the IAM path — Red HIGH-4).
	if !middleware.RequireAdmin(c) {
		return
	}
	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))
	trans := transaction.New(db)

	// Decode response body to create new transaction
	if err := json.Decode(c.Request.Body, trans); err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	if trans.Id_ != "" {
		log.Warn("Id_ should not be set, set to '%v', setting to ''", trans.Id_, c)
		trans.Id_ = ""
	}

	if trans.Type == transaction.Hold || trans.Type == transaction.HoldRemoved {
		log.Error("Transaction type should not be a hold: '%v'", trans.Type, c)
		http.Fail(c, 500, ErrorUseHoldApi.Error(), ErrorUseHoldApi)
		return
	}

	if trans.Type != transaction.Deposit && trans.Type != transaction.Withdraw && trans.Type != transaction.Transfer {
		log.Error("Transaction type is invalid: '%v'", trans.Type, c)
		http.Fail(c, 500, ErrorInvalidType.Error(), ErrorInvalidType)
		return
	}

	if trans.SourceId == trans.DestinationId && trans.SourceKind == trans.DestinationKind {
		log.Error("SourceId, SourceKind should not equal DestinationID, DestinationKind, set to '%v','%v'", trans.SourceId, trans.SourceKind, c)
		http.Fail(c, 500, ErrorCircularTransaction.Error(), ErrorCircularTransaction)
		return
	}

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

	// C1-b: this generic ledger endpoint reaches the SAME mint sink as
	// /v1/billing/deposit. A Deposit (no funded source) or a credit into the
	// gateway-spendable IAM-user wallet MINTS money, so the mint case is gated on
	// MayMintMoney (internal service token / platform global admin) — NOT the
	// org-level RequireAdmin above, which admits any org owner. Withdraws and
	// transfers between the org's OWN funded accounts are not mints and remain
	// org-admin. mintauth.Enforce at the datastore sink is the fail-closed
	// backstop; this gate returns a clean 403 and authorizes the legitimate write.
	if trans.MintRequiresAuthorization() {
		if !middleware.MayMintMoney(c) {
			http.Fail(c, 403,
				"minting spendable balance requires platform-administrator or internal-service credentials",
				errors.New("transaction mint: caller is neither the internal service token nor a platform global admin"))
			return
		}
		trans.SetContext(mintauth.WithAuthorized(trans.Context()))
	}

	if !org.Live {
		log.Info("Transaction created in test mode.", c)
		trans.Test = true
	}

	err := db.RunInTransaction(func(db *datastore.Datastore) error {
		if trans.Type == transaction.Transfer || trans.Type == transaction.Withdraw {
			datas, err := util.GetTransactionsByCurrency(db.Context, trans.SourceId, trans.SourceKind, trans.Currency, !org.Live)
			if err != nil {
				return err
			}

			if trans.SourceId == "" || trans.SourceKind == "" {
				log.Error("SourceId and SourceKind are required, provided with '%v', '%v'", trans.SourceId, trans.SourceKind, c)
				return ErrorSourceRequired
			}

			if trans.Type == transaction.Transfer && (trans.DestinationId == "" || trans.DestinationKind == "") {
				log.Error("DestinationId and DestinationKind are required, provided with '%v', '%v'", trans.DestinationId, trans.DestinationKind, c)
				return ErrorDestinationRequired
			} else if trans.Type == transaction.Withdraw {
				// Withdraw has no destination
				trans.DestinationId = ""
				trans.DestinationKind = ""
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
		} else if trans.Type == transaction.Deposit {
			trans.SourceId = ""
			trans.SourceKind = ""

			if trans.DestinationId == "" || trans.DestinationKind == "" {
				log.Error("DestinationId and DestinationKind are required, provided with '%v', '%v'", trans.DestinationId, trans.DestinationKind, c)
				return ErrorDestinationRequired
			}
		}
		return trans.Create()
	}, nil)

	if err != nil {
		http.Fail(c, 500, err.Error(), err)
		return
	} else {
		c.Writer.Header().Add("Location", c.Request.URL.Path+"/"+trans.Id())
		http.Render(c, 201, trans)
	}
}
