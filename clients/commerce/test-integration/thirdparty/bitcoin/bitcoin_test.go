package test

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains/blocktransaction"
	"github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/bitcoin"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var (
	ctx ae.Context
	c   *gin.Context
	db  *datastore.Datastore
)

func Test(t *testing.T) {
	Setup("thirdparty/bitcoin", t)
}

// Setup test context
var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	c = gincontext.New(ctx)
	db = datastore.New(ctx)

	fixtures.BlockchainNamespace(c)

	bt0 := blocktransaction.New(db)
	bt0.Address = "123"
	bt0.BitcoinTransactionUsed = false
	bt0.BitcoinTransactionType = blockchains.BitcoinTransactionTypeVOut
	bt0.BitcoinTransactionTxId = "Should Be Returned"
	bt0.BitcoinTransactionVOutIndex = 0
	bt0.BitcoinTransactionVOutValue = 1e8
	bt0.MustCreate()

	bt1 := blocktransaction.New(db)
	bt1.Address = "123"
	bt1.BitcoinTransactionUsed = false
	bt1.BitcoinTransactionType = blockchains.BitcoinTransactionTypeVOut
	bt1.BitcoinTransactionTxId = "Should Be Returned Too"
	bt1.BitcoinTransactionVOutIndex = 1
	bt1.BitcoinTransactionVOutValue = 1e9
	bt1.MustCreate()

	bt2 := blocktransaction.New(db)
	bt2.Address = "123"
	bt2.BitcoinTransactionUsed = true
	bt2.BitcoinTransactionType = blockchains.BitcoinTransactionTypeVOut
	bt2.BitcoinTransactionTxId = "Should Not Be Returned Because Used"
	bt1.BitcoinTransactionVOutIndex = 2
	bt1.BitcoinTransactionVOutValue = 2e9
	bt2.MustCreate()

	bt3 := blocktransaction.New(db)
	bt3.Address = "321"
	bt3.BitcoinTransactionUsed = false
	bt3.BitcoinTransactionType = blockchains.BitcoinTransactionTypeVOut
	bt3.BitcoinTransactionTxId = "Should Not Be Returned Because Address"
	bt3.BitcoinTransactionVOutIndex = 3
	bt3.BitcoinTransactionVOutValue = 3e9
	bt3.MustCreate()

	bt4 := blocktransaction.New(db)
	bt4.Address = "456"
	bt4.BitcoinTransactionUsed = true
	bt4.BitcoinTransactionType = blockchains.BitcoinTransactionTypeVOut
	bt4.BitcoinTransactionTxId = "Should Not Be Returned Because Address"
	bt4.BitcoinTransactionVOutIndex = 4
	bt4.BitcoinTransactionVOutValue = 4e9
	bt4.MustCreate()
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})

var _ = Describe("thirdparty.bitcoin", func() {
	It("should generate appropriate key pairs", func() {
		priv, pub, err := bitcoin.GenerateKeyPair()
		Expect(err).NotTo(HaveOccurred())
		Expect(len(priv)).To(Equal(64))
		Expect(len(pub)).To(Equal(130))
	})

	It("should generate appropriate addresses", func() {
		straddr, byteaddr, err := bitcoin.PubKeyToAddress("0450863AD64A87AE8A2FE83C1AF1A8403CB53F53E486D8511DAD8A04887E5B23522CD470243453A299FA9E77237716103ABC11A1DF38855ED6F2EE187E9C582BA6", false)
		testaddr, testbyteaddr, err := bitcoin.PubKeyToAddress("0450863AD64A87AE8A2FE83C1AF1A8403CB53F53E486D8511DAD8A04887E5B23522CD470243453A299FA9E77237716103ABC11A1DF38855ED6F2EE187E9C582BA6", true)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(byteaddr)).To(Equal(25))
		Expect(len(straddr)).To(Equal(33))
		Expect(len(testbyteaddr)).To(Equal(25))
		Expect(len(testaddr)).To(Equal(34))
	})
	/*It("should not screw up during transaction creation", func() {
		senderPubKey, senderPrivKey, _ := bitcoin.GenerateKeyPair()
		receiver1PubKey, _, _ := bitcoin.GenerateKeyPair()
		receiver2PubKey, _, _ := bitcoin.GenerateKeyPair()

		receiver1TestNetAddress, _, _ := bitcoin.PubKeyToAddress(receiver1PubKey, true)
		receiver2TestNetAddress, _, _ := bitcoin.PubKeyToAddress(receiver2PubKey, true)

		in := []bitcoin.Input{bitcoin.Input{TxId: "5b60d0684a8201ddac20f713782a1f03682b508e90d99d0887b4114ad4ccfd2c", OutputIndex: 0}}
		out := []bitcoin.Destination{bitcoin.Destination{Value: 1000, Address: receiver1TestNetAddress}, bitcoin.Destination{Value: 5000, Address: receiver2TestNetAddress}}
		senderAddress, _, _ := bitcoin.PubKeyToAddress(senderPubKey, false)
		senderTestNetAddress, _, _ := bitcoin.PubKeyToAddress(senderPubKey, true)
		senderAccount := bitcoin.Sender{
			PrivateKey:     senderPrivKey,
			PublicKey:      senderPubKey,
			Address:        senderAddress,
			TestNetAddress: senderTestNetAddress,
		}
		rawTrx, _ := bitcoin.CreateTransaction(in, out, senderAccount)
		Expect(rawTrx).ToNot(BeNil())
	})*/

	It("should get unused transactions for an address", func() {
		origins, err := bitcoin.GetBitcoinTransactions(ctx, "123")
		Expect(err).NotTo(HaveOccurred())
		Expect(len(origins)).To(Equal(2))
		Expect(origins[0].TxId).To(Equal("Should Be Returned"))
		Expect(origins[0].OutputIndex).To(Equal(0))
		Expect(origins[0].Amount).To(Equal(int64(1e8)))

		Expect(origins[1].TxId).To(Equal("Should Be Returned Too"))
		Expect(origins[1].OutputIndex).To(Equal(1))
		Expect(origins[1].Amount).To(Equal(int64(1e9)))
	})

	It("should not get used transactions for an address", func() {
		origins, err := bitcoin.GetBitcoinTransactions(ctx, "456")
		Expect(err).NotTo(HaveOccurred())
		Expect(len(origins)).To(Equal(0))
	})

	It("should prune origins", func() {
		origins, err := bitcoin.GetBitcoinTransactions(ctx, "123")
		Expect(err).NotTo(HaveOccurred())

		origins2, err := bitcoin.PruneOriginsWithAmount(origins, 1e9)
		Expect(len(origins2)).To(Equal(2))
	})

	It("should prune origins if enough", func() {
		origins, err := bitcoin.GetBitcoinTransactions(ctx, "123")
		Expect(err).NotTo(HaveOccurred())

		origins2, err := bitcoin.PruneOriginsWithAmount(origins, 1e3)
		Expect(len(origins2)).To(Equal(1))
	})

	It("should prune origins but error is insufficient funds", func() {
		origins, err := bitcoin.GetBitcoinTransactions(ctx, "123")
		Expect(err).NotTo(HaveOccurred())

		origins2, err := bitcoin.PruneOriginsWithAmount(origins, 2e9)
		Expect(err).To(Equal(bitcoin.WeRequireAdditionalFunds))
		Expect(len(origins2)).To(Equal(2))
	})

	It("should convert []OriginWithAmount to []Origin", func() {
		origins, err := bitcoin.GetBitcoinTransactions(ctx, "123")
		Expect(err).NotTo(HaveOccurred())

		origins2 := bitcoin.OriginsWithAmountToOrigins(origins)
		Expect(len(origins2)).To(Equal(2))
		Expect(origins[0].Origin).To(Equal(origins2[0]))
		Expect(origins[1].Origin).To(Equal(origins2[1]))
	})
})
