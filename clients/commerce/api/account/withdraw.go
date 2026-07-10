package account

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/api/organization/wallet"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

func withdraw(c *gin.Context) {
	org := middleware.GetOrganization(c)
	usr := middleware.GetUser(c)

	// Get hte wallet
	orgWallet, err := wallet.ReturnWallet(org)
	if err != nil {
		log.Error("Unable to retrieve wallet: %v", err, c)
		http.Fail(c, 400, "Unable to retrieve wallet", err)
		return
	}

	// Decode the request
	request := wallet.PayFromAccountRequest{}
	if err := json.Decode(c.Request.Body, &request); err != nil {
		http.Fail(c, 400, "Failed to decode request body", err)
		return
	}

	// Account on the org should be publically avaiable and withdrawable
	account, success := orgWallet.GetAccountByName(request.Name)
	if !success || account != nil || (success && !account.Withdrawable) {
		if !success {
			log.Error("Account %s does not exist", request.Name, c)
		}
		if account != nil && !account.Withdrawable {
			log.Error("Account %s is not withdrawable", request.Name, c)
		}
		http.Fail(c, 400, "Account not withdrawable", ErrorAccountNotWithdrawable)
		return
	}

	// Determine the currency
	var cur currency.Type

	switch account.Type {
	case blockchains.EthereumType, blockchains.EthereumRopstenType:
		cur = currency.ETH
	// Bitcoin accounts use BTC
	case blockchains.BitcoinType, blockchains.BitcoinTestnetType:
		cur = currency.BTC
	}

	var transactionId string

	nsDb := usr.Datastore()

	// Check against the balance
	err = nsDb.RunInTransaction(func(db *datastore.Datastore) error {
		test := !org.Live
		datas, err := util.GetTransactionsByCurrency(nsDb.Context, usr.Id(), "user", cur, test)
		if err != nil {
			return err
		}

		data, ok := datas.Data[cur]
		if !ok {
			log.Error("Source has no funds %v, %v", json.Encode(datas), !org.Live, c)
			return ErrorInsufficientFunds
		}

		if data.Balance-data.Holds < request.Amount {
			log.Error("Source has insufficient funds '%v' - '%v' < '%v'", data.Balance, data.Holds, request.Amount, c)
			return ErrorInsufficientFunds
		}

		transactionId, err = blockchain.MakePayment(middleware.GetContext(c), *account, request.To, request.Amount, request.Fee, []byte(org.WalletPassphrase))
		if err != nil {
			log.Error("Failed to create transaction %v", err, c)
			return err
		}

		trans := transaction.New(nsDb)
		trans.SourceId = usr.Id()
		trans.SourceKind = "user"
		trans.Amount = request.Amount
		trans.Currency = cur
		trans.Type = transaction.Withdraw
		trans.Test = test

		// log.Warn("... %v", json.Encode(trans))

		return trans.Create()
	}, nil)

	if err != nil {
		http.Fail(c, 500, err.Error(), err)
		return
	} else {
		http.Render(c, 200, wallet.PayFromAccountResponse{TransactionId: transactionId})
	}
}
