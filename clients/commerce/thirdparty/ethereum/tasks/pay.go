package tasks

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/api/checkout/tasks"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/util"
	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/ethereum"
	blockchainutil "github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/webhook"
)

var SimpleTransactionGasUsed = big.NewInt(21000)
var UnsupportedChainType = errors.New("Chain type is unsupported.")
var IntegrationNotInitialized = errors.New("Ethereum Integration has no address.")
var PlatformWalletNotFound = errors.New("Platform Wallet Not Found.")
var PlatformAccountNotFound = errors.New("Platform Account Not Found.")
var PlatformAccountDecryptionFailed = errors.New("Platform Account Decryption Failed.")
var OrderWalletNotFound = errors.New("Order Wallet Not Found.")
var OrderAccountNotFound = errors.New("Order Account Not Found.")
var OrderAccountDecryptionFailed = errors.New("Order Account Decryption Failed.")
var InsufficientFee = errors.New("Not Enough Fee Balance To Cover Transaction Fee")
var InsufficientTransfer = errors.New("Not Enough Transfer Balance To Cover Transaction Fee")

var GasPrice = big.NewInt(ethereum.DefaultGasPrice)
var LastGasPriceCheck = time.Time{}

var EthereumProcessPayment = delay.Func(
	"ethereum-process-payment",
	func(
		ctx context.Context,
		orgName,
		walletId,
		txHash,
		from,
		to,
		chainType string,
		amount *big.Int,
	) {
		if err := EthereumProcessPaymentImpl(ctx, orgName, walletId, txHash, from, to, chainType, amount); err != nil {
			panic(err)
		}
	})

func EthereumProcessPaymentImpl(
	ctx context.Context,
	orgName,
	walletId,
	txHash,
	from,
	to,
	ct string,
	amount *big.Int,
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
		if ok, err := pay.Query().Filter("Account.EthereumTransactionHash=", txHash).Get(); ok {
			if pay.Account.EthereumTransferred {
				log.Warn("Payment already created for Wallet '%s', TxHash '%s'", w.Id(), txHash, ctx)
				return nil
			}
			log.Warn("Retrying payment for Wallet '%s', TxHash '%s'", w.Id(), txHash, ctx)
		} else if err != nil {
			log.Warn("No payment expected for Wallet '%s', TxHash '%s' but error encountered: %v", w.Id(), txHash, err, ctx)
			return err
		}

		pay.Account.EthereumTransactionHash = txHash
		pay.Account.EthereumFromAddress = from
		pay.Account.EthereumToAddress = to
		pay.Account.EthereumChainType = chainType
		pay.Account.EthereumAmount = blockchains.BigNumber(amount.String())

		log.Warn(ord.Currency)

		pay.Test = ord.Test
		pay.Status = payment.Paid
		pay.Type = ord.Type
		pay.Buyer = usr.Buyer()
		pay.Currency = ord.Currency
		pay.Parent = ord.Key()
		pay.OrderId = ord.Id()
		pay.UserId = usr.Id()
		pay.Amount = pay.Currency.FromMinimalUnits(amount)

		if err := pay.Create(); err != nil {
			log.Warn("Could not save payment for Order '%s', Wallet '%s', TxHash '%s'", ord.Id(), w.Id(), txHash, ctx)
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
			password := ""
			var chainId ethereum.ChainId
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
			case blockchains.EthereumType:
				if org.Ethereum.Address == "" {
					log.Error("Ethereum Integration not initialized for %v", org.FullName, ctx)
					return IntegrationNotInitialized
				}
				address = config.Ethereum.MainNetNodes[0]
				password = config.Ethereum.DepositPassword
				chainId = ethereum.MainNet
				if a, ok := pw.GetAccountByName("Ethereum Deposit Account"); ok {
					account = a
				} else {
					return PlatformAccountNotFound
				}
			case blockchains.EthereumRopstenType:
				if org.Ethereum.Address == "" && org.Ethereum.TestAddress == "" {
					log.Error("Ethereum Integration not initialized for %v", org.FullName, ctx)
					return IntegrationNotInitialized
				}
				address = config.Ethereum.TestNetNodes[0]
				password = config.Ethereum.TestPassword
				chainId = ethereum.Ropsten
				if a, ok := pw.GetAccountByName("Ethereum Ropsten Test Account"); ok {
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
			client := ethereum.New(ctx, address)

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

			// Get current gas price so we can calculate transfer costs
			// gasPrice, err := client.GasPrice()
			// if err != nil {
			// 	log.Error("Could not get gas price: %v", err, ctx)
			// 	return err
			// }

			// Use default gasprice
			timeCheck := time.Now().Add(-5 * time.Minute)
			if LastGasPriceCheck.Before(timeCheck) || LastGasPriceCheck.IsZero() {
				gp, err := client.GasPrice()
				if err != nil {
					return err
				}

				GasPrice = gp
			}

			gasPrice := big.NewInt(0).Set(GasPrice)

			// Set the remaining transfer amount to the order total
			transferAmount := ord.Currency.ToMinimalUnits(ord.Total)

			// Link payments/fees
			for _, fe := range fees {
				fe.PaymentId = pay.Id()
				pay.FeeIds = append(pay.FeeIds, fe.Id())

				// Subtract fee value from the total transfer value
				feeAmount := fe.Currency.ToMinimalUnits(fe.Amount)
				transferAmount = transferAmount.Sub(transferAmount, feeAmount)

				// Only supported fee at hte moment is the platform one
				if fe.Name == "Platform fee" {
					cost := big.NewInt(0).Set(SimpleTransactionGasUsed)
					cost = cost.Mul(cost, gasPrice)

					platformFee := fe
					platformAmount := platformFee.Currency.ToMinimalUnits(platformFee.Amount)
					platformAmount = platformAmount.Sub(platformAmount, cost)

					if platformAmount.Cmp(big.NewInt(0)) > 0 {
						// Only deal with sending the platform fee for now
						log.Info("Transfering Platform Fee '%s' to '%s'", platformAmount.String(), address, ctx)
						if finalTxHash, err := client.SendTransaction(
							chainId,
							fromAccount.PrivateKey,
							fromAccount.Address,
							account.Address,
							platformAmount,
							big.NewInt(0).Set(SimpleTransactionGasUsed),
							gasPrice,
							[]byte{},
						); err != nil {
							return err
						} else {
							fe.Ethereum.FinalTransactionHash = finalTxHash
						}
					} else {
						log.Warn("Insufficient Fee To Cover Platform Fee Transaction, After Transaction Value is '%s'", platformAmount.String(), ctx)
					}
					fe.Ethereum.FinalAddress = account.Address
					fe.Ethereum.FinalTransactionCost = blockchains.BigNumber(cost.String())
					fe.Ethereum.FinalAmount = blockchains.BigNumber(platformAmount.String())
				}

				fe.MustCreate()
			}

			finalCost := big.NewInt(0).Set(SimpleTransactionGasUsed)
			finalCost = finalCost.Mul(finalCost, gasPrice)

			transferAmount = transferAmount.Sub(transferAmount, finalCost)

			// Use the ethereum address, alternatively use the test address
			// instead if provided well.  Both networks use the same signature
			// algo so it doesn't matter
			transferAddress := org.Ethereum.Address

			if ord.Test && org.Ethereum.TestAddress != "" {
				transferAddress = org.Ethereum.TestAddress
			}

			if transferAmount.Cmp(big.NewInt(0)) > 0 {
				// Transfer rest of the ethereum
				log.Info("Transfering '%s' to '%s'", transferAmount.String(), address, ctx)
				if txHash, err := client.SendTransaction(
					chainId,
					fromAccount.PrivateKey,
					fromAccount.Address,
					transferAddress,
					transferAmount,
					big.NewInt(0).Set(SimpleTransactionGasUsed),
					gasPrice,
					[]byte{},
				); err != nil {
					return err
				} else {
					pay.Account.EthereumFinalTransactionHash = txHash
				}
			} else {
				log.Error("Insufficient Transfer To Cover Transaction, After Transaction Value is '%s'", transferAmount.String(), ctx)
				return InsufficientTransfer
			}

			pay.Account.EthereumFinalAddress = org.Ethereum.TestAddress
			if pay.Account.EthereumFinalAddress == "" {
				pay.Account.EthereumFinalAddress = org.Ethereum.Address
			}
			pay.Account.EthereumFinalTransactionCost = blockchains.BigNumber(finalCost.String())
			pay.Account.EthereumFinalAmount = blockchains.BigNumber(transferAmount.String())

			util.HandleDeposit(ord)
		}

		ord.Paid += pay.Amount
		ord.PaymentIds = append(ord.PaymentIds, pay.Id())

		pay.Account.EthereumTransferred = true

		if err := pay.Update(); err != nil {
			log.Warn("Could not save payment for Order '%s', Wallet '%s', TxHash '%s'", ord.Id(), w.Id(), txHash, ctx)
			return err
		}

		if err := ord.Update(); err != nil {
			log.Warn("Could not update Order '%s' for Wallet '%s', TxHash '%s'", ord.Id(), w.Id(), txHash, ctx)
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
