package tasks

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/clients/commerce/api/checkout/tasks"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/util"
	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/bitcoin"
	blockchainutil "github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/webhook"
)

var UnsupportedChainType = errors.New("Chain type is unsupported.")
var IntegrationNotInitialized = errors.New("Bitcoin Integration has no address.")
var PlatformWalletNotFound = errors.New("Platform Wallet Not Found.")
var PlatformAccountNotFound = errors.New("Platform Account Not Found.")
var PlatformAccountDecryptionFailed = errors.New("Platform Account Decryption Failed.")
var OrderWalletNotFound = errors.New("Order Wallet Not Found.")
var OrderAccountNotFound = errors.New("Order Account Not Found.")
var InsufficientTransfer = errors.New("Not Enough Transfer Balance To Cover Transaction Fee")

var BitcoinProcessPayment = delay.Func(
	"bitcon-process-payment",
	func(
		ctx context.Context,
		orgName,
		walletId,
		txId,
		chainType string,
		amount int64,
	) {
		if err := BitcoinProcessPaymentImpl(ctx, orgName, walletId, txId, chainType, amount); err != nil {
			panic(err)
		}
	})

func BitcoinProcessPaymentImpl(
	ctx context.Context,
	orgName,
	walletId,
	txId,
	ct string,
	amount int64,
) error {
	// convert chaintype
	chainType := blockchains.Type(ct)

	// Get Org
	db := datastore.New(ctx)
	org := organization.New(db)
	if err := org.GetById(orgName); err != nil {
		log.Warn("Could not find Org '%s': %v", orgName, err, ctx)
		return err
	}

	// Namespace the context
	nsCtx := org.Namespaced(ctx)

	// Namespace the datastore
	nsDb := datastore.New(nsCtx)

	// Get the basic order info
	usr, ord, w, err := blockchainutil.GetUserOrderByWallet(nsDb, walletId)
	if err != nil {
		log.Error("GetUserOrderAndWallet error: %v", err, ctx)
		return err
	}

	// Create payment, update order
	pay := payment.New(nsDb)
	if err := pay.RunInTransaction(func() error {
		// Make sure payment with TransactionHash does not exist (transaction
		// already processed)
		if ok, err := pay.Query().Filter("Account.BitcoinTransactionTxId=", txId).Get(); ok {
			if pay.Account.BitcoinTransferred {
				log.Warn("Payment already created for Wallet '%s', TxId '%s'", w.Id(), txId, ctx)
				return nil
			}
			log.Warn("Retrying payment for Wallet '%s', TxId '%s'", w.Id(), txId, ctx)
		} else if err != nil {
			log.Warn("No payment expected for Wallet '%s', TxId '%s' but error encountered: %v", w.Id(), txId, err, ctx)
			return err
		}

		pay.Account.BitcoinTransactionTxId = txId
		pay.Account.BitcoinChainType = chainType
		pay.Account.BitcoinAmount = currency.Cents(amount)

		log.Warn(ord.Currency)

		pay.Test = ord.Test
		pay.Status = payment.Paid
		pay.Type = ord.Type
		pay.Buyer = usr.Buyer()
		pay.Currency = ord.Currency
		pay.Parent = ord.Key()
		pay.OrderId = ord.Id()
		pay.UserId = usr.Id()
		pay.Amount = currency.Cents(amount)

		if err := pay.Create(); err != nil {
			log.Warn("Could not save payment for Order '%s', Wallet '%s', TxId '%s'", ord.Id(), w.Id(), txId, ctx)
			return err
		}

		// Update order status
		log.Info("Pay.Amount %v >=? Order.Total - Order.Paid %v", pay.Amount, ord.Total, ctx)
		if pay.Amount >= ord.Total-ord.Paid && ord.PaymentStatus != payment.Paid {
			ord.PaymentStatus = payment.Paid

			// Fees
			platformFees, partnerFees := org.Pricing()
			fee, fes, err := ord.CalculateFees(platformFees, partnerFees)
			if err != nil {
				log.Error("Fee calculation error: %v", err, ctx)
				return err
			}
			fees := fes
			pay.Fee = fee

			// Create client for transfering fees
			address := ""
			authUsername := ""
			authPassword := ""
			password := ""
			// var chainId ethereum.ChainId
			var account *wallet.Account

			pw := wallet.New(org.Datastore())
			if ok, err := pw.Query().Filter("Id_=", "platform-wallet").Get(); !ok {
				if err != nil {
					return err
				}
				return PlatformWalletNotFound
			}

			// Get the right account and credentials
			switch chainType {
			case blockchains.BitcoinType:
				if org.Bitcoin.Address == "" {
					return IntegrationNotInitialized
				}
				address = config.Bitcoin.MainNetNodes[0]
				authUsername = config.Bitcoin.MainNetUsernames[0]
				authPassword = config.Bitcoin.MainNetPasswords[0]
				password = config.Bitcoin.DepositPassword
				// chainId = ethereum.MainNet
				if a, ok := pw.GetAccountByName("Bitcoin Deposit Account"); ok {
					account = a
				} else {
					return PlatformAccountNotFound
				}
			case blockchains.BitcoinTestnetType:
				if org.Bitcoin.TestAddress == "" {
					return IntegrationNotInitialized
				}
				address = config.Bitcoin.TestNetNodes[0]
				authUsername = config.Bitcoin.TestNetUsernames[0]
				authPassword = config.Bitcoin.TestNetPasswords[0]
				password = config.Bitcoin.TestPassword
				// chainId = ethereum.Ropsten
				if a, ok := pw.GetAccountByName("Bitcoin Test Account"); ok {
					account = a
				} else {
					return PlatformAccountNotFound
				}
			default:
				return UnsupportedChainType
			}

			// Retrieve account information
			if err := account.Decrypt([]byte(password)); err != nil {
				return err
			}

			if account.PrivateKey == "" {
				return PlatformAccountDecryptionFailed
			}

			// Initialize client
			client := bitcoin.New(ctx, address, authUsername, authPassword)

			// Get From Address
			ow, err := ord.GetOrCreateWallet(ord.Datastore())
			if err != nil {
				log.Error("Order Wallet Not Found: %v", err, ctx)
				return OrderWalletNotFound
			}

			fromAccount, ok := ow.GetAccountByName("Receiver Account")
			if !ok {
				return OrderAccountNotFound
			}

			if err := fromAccount.Decrypt([]byte(ord.WalletPassphrase)); err != nil {
				return err
			}

			// Set the remaining transfer amount to the order total
			transferAmount := ord.Total

			// Get the origins
			origins, err := bitcoin.GetBitcoinTransactions(ctx, fromAccount.Address)
			if err != nil {
				log.Error("Failed Fetching Origins: %v", err, ctx)
				return err
			}

			// Get final origin form
			in := bitcoin.OriginsWithAmountToOrigins(origins)

			// Initialize Outputs
			out := []bitcoin.Destination{}

			// Start vout counter
			vout := 0

			// Link payments/fees
			for _, fe := range fees {
				fe.PaymentId = pay.Id()
				pay.FeeIds = append(pay.FeeIds, fe.Id())

				// Subtract fee value from the total transfer value
				feeAmount := fe.Amount
				transferAmount = transferAmount - feeAmount

				// Only supported fee at hte moment is the platform one
				if fe.Name == "Platform fee" {
					platformFee := fe
					platformAmount := platformFee.Amount

					if platformAmount > 0 {
						log.Info("Adding Platform Fee Destination[%v] '%v' to '%s'", vout, platformAmount, address, ctx)
						out = append(out,
							bitcoin.Destination{
								Value:   int64(platformAmount),
								Address: account.Address,
							})
					} else {
						log.Warn("Insufficient Fee To Cover Platform Fee Transaction, After Transaction Value is '%s'", platformAmount, ctx)
					}

					fe.Bitcoin.FinalAddress = account.Address
					fe.Bitcoin.FinalAmount = platformAmount
					fe.Bitcoin.FinalVOut = int64(vout)
					vout++
				}

				// fe.MustCreate()
			}

			finalCost := bitcoin.CalculateFee(len(in), vout+1, 0)
			transferAmount = transferAmount - currency.Cents(finalCost)

			// Use the ethereum address, alternatively use the test address
			// instead if provided well.  Both networks use the same signature
			// algo so it doesn't matter
			transferAddress := org.Bitcoin.Address

			if ord.Test && org.Bitcoin.TestAddress != "" {
				transferAddress = org.Bitcoin.TestAddress
			}

			if transferAmount > 0 {
				// Transfer rest of the ethereum
				log.Info("Adding Platform Fee Destination[%v] '%v' to '%s'", vout, transferAmount, address, ctx)
				out = append(out,
					bitcoin.Destination{
						Value:   int64(transferAmount),
						Address: transferAddress,
					})

				log.Info("Inputs %v", json.Encode(in), ctx)
				log.Info("Outputs %v", json.Encode(out), ctx)

				rawTrx, err := bitcoin.CreateTransaction(client, in, out, bitcoin.Sender{
					PrivateKey: fromAccount.PrivateKey,
					PublicKey:  fromAccount.PublicKey,
					Address:    fromAccount.Address,
				}, 0)

				finalTxId, err := client.SendRawTransaction(rawTrx)
				if err != nil {
					return err
				}

				pay.Account.BitcoinFinalTransactionTxId = finalTxId
			} else {
				log.Error("Insufficient Transfer To Cover Transaction, After Transaction Value is '%s'", transferAmount, ctx)
				return InsufficientTransfer
			}

			for _, fe := range fees {
				fe.Bitcoin.FinalTransactionTxId = pay.Account.BitcoinFinalTransactionTxId
				fe.MustCreate()
			}

			switch chainType {
			case blockchains.BitcoinType:
				pay.Account.BitcoinFinalAddress = org.Bitcoin.Address
			case blockchains.BitcoinTestnetType:
				pay.Account.BitcoinFinalAddress = org.Bitcoin.TestAddress
			}

			pay.Account.BitcoinFinalTransactionCost = currency.Cents(finalCost)
			pay.Account.BitcoinFinalAmount = transferAmount

			util.HandleDeposit(ord)
		}

		ord.Paid += pay.Amount
		ord.PaymentIds = append(ord.PaymentIds, pay.Id())

		pay.Account.BitcoinTransferred = true

		if err := pay.Update(); err != nil {
			log.Warn("Could not save payment for Order '%s', Wallet '%s', TxId '%s'", ord.Id(), w.Id(), txId, ctx)
			return err
		}

		if err := ord.Update(); err != nil {
			log.Warn("Could not update Order '%s' for Wallet '%s', TxId '%s'", ord.Id(), w.Id(), txId, ctx)
			return err
		}

		// Run through the standard capture stuff

		// TODO: Run in task(CaptureAsync), no need to block call on rest of this
		util.SaveRedemptions(ctx, ord)
		util.UpdateReferral(org, ord)
		util.UpdateCart(ctx, ord)
		util.UpdateStats(ctx, org, ord, []*payment.Payment{pay})

		buyer := pay.Buyer

		tasks.CaptureAsync.Call(org.Context(), org.Id(), ord.Id())
		tasks.SendOrderConfirmation.Call(org.Context(), org.Id(), ord.Id(), buyer.Email, buyer.FirstName, buyer.LastName)

		webhook.Emit(ctx, orgName, "order.paid", ord)

		return nil
	}, nil); err != nil {
		return err
	}

	return nil
}
