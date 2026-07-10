package wallet

import (
	"errors"
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"
)

type CreateAccountRequest struct {
	Name       string `json:"name"`
	Blockchain string `json:"blockchain"`
}

type PayFromAccountRequest struct {
	Name   string         `json:"name"`
	To     string         `json:"to"`
	Amount currency.Cents `json:"amount"`
	// GasPrice of Fee Per Byte
	Fee currency.Cents `json:"fee"`
}

type PayFromAccountResponse struct {
	TransactionId string `json:"transactionId"`
}

func Get(c *gin.Context) {
	org := middleware.GetOrganization(c)
	orgWallet, err := ReturnWallet(org)
	if err != nil || orgWallet == nil {
		http.Fail(c, 400, "Unable to retrieve wallet from datastore", err)
	}
	org.MustUpdate()

	http.Render(c, 200, orgWallet)
}

func GetAccount(c *gin.Context) {
	org := middleware.GetOrganization(c)
	orgWallet, err := ReturnWallet(org)
	if err != nil {
		http.Fail(c, 400, "Unable to retrieve wallet from datastore", err)
	}

	log.Debug("Requested account name: %v", c.Params.ByName("name"))
	account, success := orgWallet.GetAccountByName(c.Params.ByName("name"))
	if !success {
		http.Fail(c, 404, "Requested account name was not found.", errors.New("Requested account name was not found."))
		return
	}
	org.MustUpdate()

	http.Render(c, 200, account)
}

func CreateAccount(c *gin.Context) {
	org := middleware.GetOrganization(c)
	orgWallet, err := ReturnWallet(org)
	if err != nil {
		http.Fail(c, 400, "Unable to retrieve wallet from datastore", err)
	}
	request := CreateAccountRequest{}
	if err := json.Decode(c.Request.Body, &request); err != nil {
		http.Fail(c, 400, "Failed to decode request body", err)
		return
	}
	log.Debug("Blockchain requested for account creation: %v", request.Blockchain)
	blockchainType := blockchains.Type(request.Blockchain)
	account, err := orgWallet.CreateAccount(request.Name, blockchainType, []byte(org.WalletPassphrase))
	if err != nil {
		http.Fail(c, 400, "Failed to create requested account", err)
		return
	}
	org.MustUpdate()

	http.Render(c, 200, account)
}

func Send(c *gin.Context) {
	// Crypto withdrawal from the org wallet — the highest-value money move.
	// Admin-only, enforced inside the handler because the route middleware
	// no-ops on the IAM path (Red HIGH-4). The wallet lives on the caller's own
	// org record (resolved from the verified X-Org-Id), so it is already
	// tenant-scoped; this closes the missing authorization check.
	if !middleware.RequireAdmin(c) {
		return
	}
	org := middleware.GetOrganization(c)
	orgWallet, err := ReturnWallet(org)
	if err != nil {
		http.Fail(c, 400, "Unable to retrieve wallet from datastore", err)
	}
	request := PayFromAccountRequest{}
	if err := json.Decode(c.Request.Body, &request); err != nil {
		http.Fail(c, 400, "Failed to decode request body", err)
		return
	}
	account, success := orgWallet.GetAccountByName(request.Name)
	if !success {
		http.Fail(c, 404, "Requested account name was not found.", errors.New("Requested account name was not found."))
		return
	}
	transactionId, err := blockchain.MakePayment(middleware.GetContext(c), *account, request.To, request.Amount, request.Fee, []byte(org.WalletPassphrase))
	if err != nil {
		http.Fail(c, 400, "Failed to make payment.", err)
		return
	}
	org.MustUpdate()

	http.Render(c, 200, PayFromAccountResponse{transactionId})
}

func ReturnWallet(o *organization.Organization) (*wallet.Wallet, error) {
	ret, err := o.GetOrCreateWallet(o.Datastore())
	if err != nil {
		return nil, err
	}
	if o.WalletPassphrase == "" {
		o.WalletPassphrase = rand.SecretKey()
	}

	return ret, nil
}
