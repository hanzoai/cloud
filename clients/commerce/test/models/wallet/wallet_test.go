package test

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains/blockaddress"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("Wallet", func() {
	Context("CreateAccount", func() {
		var wal *wallet.Wallet

		Before(func() {
			wal = wallet.New(db)
		})

		It("should create an account and save it automatically if wallet is in the datastore", func() {
			wal.MustCreate()

			password := "Th1$1s@b@dp@$$w0rd"
			acc, err := wal.CreateAccount("test", blockchains.EthereumRopstenType, []byte(password))

			Expect(err).ToNot(HaveOccurred())

			Expect(len(wal.Accounts)).To(Equal(1))
			Expect(wal.Accounts[0]).To(Equal(*acc))

			enc := acc.Encrypted
			priv := acc.PrivateKey
			pub := acc.PublicKey
			add := acc.Address

			w2 := wallet.New(db)
			w2.MustGetById(wal.Id())

			Expect(len(w2.Accounts)).To(Equal(1))

			acc2 := w2.Accounts[0]
			Expect(acc2.Name).To(Equal("test"))
			Expect(acc2.Encrypted).To(Equal(enc))
			Expect(acc2.PrivateKey).To(Equal(""))
			Expect(acc2.PublicKey).To(Equal(pub))
			Expect(acc2.Address).To(Equal(add))
			// Address should be lower case
			Expect(strings.ToLower(add)).To(Equal(add))
			// Address should start with 0x
			Expect(add[0:2]).To(Equal("0x"))

			err = acc2.Decrypt([]byte(password))
			Expect(err).ToNot(HaveOccurred())
			Expect(acc2.PrivateKey).To(Equal(priv))

			// Should create blockchain stuff
			ba := blockaddress.New(bcDb)
			ok, err := ba.Query().Filter("Address=", add).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(Equal(true))
			Expect(ba.WalletId).To(Equal(wal.Id()))
			Expect(ba.WalletNamespace).To(Equal("suchtees"))
			Expect(ba.Type).To(Equal(blockchains.EthereumRopstenType))
		})

		It("should throw errors for unknown types", func() {
			wal.MustCreate()

			password := "Th1$1s@b@dp@$$w0rd"
			acc, err := wal.CreateAccount("nope", blockchains.Type("nopecoin"), []byte(password))

			Expect(err).To(Equal(wallet.ErrorInvalidTypeSpecified))
			Expect(acc).To(BeNil())
		})
	})

	Context("GetAccountByName", func() {
		var wal *wallet.Wallet

		Before(func() {
			wal = wallet.New(db)
			wal.MustCreate()
		})

		It("should find an account by name", func() {
			password := "Th1$1s@b@dp@$$w0rd"
			_, err := wal.CreateAccount("test", blockchains.EthereumRopstenType, []byte(password))
			Expect(err).ToNot(HaveOccurred())

			acc, err := wal.CreateAccount("test2", blockchains.EthereumRopstenType, []byte(password))
			Expect(err).ToNot(HaveOccurred())

			Expect(len(wal.Accounts)).To(Equal(2))

			acc2, ok := wal.GetAccountByName("test2")
			Expect(ok).To(BeTrue())
			Expect(*acc2).To(Equal(*acc))
		})

		It("should not find an account by name", func() {
			password := "Th1$1s@b@dp@$$w0rd"
			_, err := wal.CreateAccount("test", blockchains.EthereumRopstenType, []byte(password))
			Expect(err).ToNot(HaveOccurred())

			Expect(len(wal.Accounts)).To(Equal(1))

			acc2, ok := wal.GetAccountByName("test2")
			Expect(ok).To(BeFalse())
			Expect(acc2).To(BeNil())
		})

		It("should phase out and deprecate TestNetAddress", func() {
			password := "Th1$1s@b@dp@$$w0rd"
			_, err := wal.CreateAccount("test", blockchains.BitcoinTestnetType, []byte(password))
			Expect(err).ToNot(HaveOccurred())
			Expect(len(wal.Accounts)).To(Equal(1))

			// Test Defaults
			Expect(wal.Accounts[0].TestNetAddress).To(Equal(""))
			Expect(wal.Accounts[0].Address).NotTo(Equal(""))
			Expect(wal.Accounts[0].AddressBackup).To(Equal(""))

			// Simulate a deprecated TestNetAddress Account
			wal.Accounts[0].TestNetAddress = "TestNetAddress"
			wal.Accounts[0].Address = "Address"
			wal.MustUpdate()

			Expect(wal.Accounts[0].TestNetAddress).To(Equal("TestNetAddress"))
			Expect(wal.Accounts[0].Address).To(Equal("Address"))
			Expect(wal.Accounts[0].AddressBackup).To(Equal(""))

			// Test to see if the Account has been updated to the new format
			a, ok := wal.GetAccountByName("test")
			Expect(ok).To(BeTrue())
			Expect(a.TestNetAddress).To(Equal(""))
			Expect(a.Address).To(Equal("TestNetAddress"))
			Expect(a.AddressBackup).To(Equal("Address"))

			// Make sure it was automagically saved
			wal2 := wallet.New(db)
			err = wal2.GetById(wal.Id())
			Expect(err).ToNot(HaveOccurred())
			Expect(len(wal2.Accounts)).To(Equal(1))

			a2 := wal2.Accounts[0]
			Expect(a2.TestNetAddress).To(Equal(""))
			Expect(a2.Address).To(Equal("TestNetAddress"))
			Expect(a2.AddressBackup).To(Equal("Address"))
		})

		It("should not do anything with regards to TestNetAddress for Bitcoin Type", func() {
			password := "Th1$1s@b@dp@$$w0rd"
			_, err := wal.CreateAccount("test", blockchains.BitcoinType, []byte(password))
			Expect(err).ToNot(HaveOccurred())
			Expect(len(wal.Accounts)).To(Equal(1))

			// Test Defaults
			Expect(wal.Accounts[0].TestNetAddress).To(Equal(""))
			Expect(wal.Accounts[0].Address).NotTo(Equal(""))
			Expect(wal.Accounts[0].AddressBackup).To(Equal(""))

			// Simulate a deprecated TestNetAddress Account
			wal.Accounts[0].TestNetAddress = "TestNetAddress"
			wal.Accounts[0].Address = "Address"
			wal.MustUpdate()

			Expect(wal.Accounts[0].TestNetAddress).To(Equal("TestNetAddress"))
			Expect(wal.Accounts[0].Address).To(Equal("Address"))
			Expect(wal.Accounts[0].AddressBackup).To(Equal(""))

			// Test to see if the Account has been updated to the new format
			a, ok := wal.GetAccountByName("test")
			Expect(ok).To(BeTrue())
			Expect(a.TestNetAddress).To(Equal("TestNetAddress"))
			Expect(a.Address).To(Equal("Address"))
			Expect(a.AddressBackup).To(Equal(""))

			// Make sure it was automagically saved
			wal2 := wallet.New(db)
			err = wal2.GetById(wal.Id())
			Expect(err).ToNot(HaveOccurred())
			Expect(len(wal2.Accounts)).To(Equal(1))

			a2 := wal2.Accounts[0]
			Expect(a2.TestNetAddress).To(Equal("TestNetAddress"))
			Expect(a2.Address).To(Equal("Address"))
			Expect(a2.AddressBackup).To(Equal(""))
		})

		It("should not do anything with regards to TestNetAddress for Ethereum Type", func() {
			password := "Th1$1s@b@dp@$$w0rd"
			_, err := wal.CreateAccount("test", blockchains.EthereumType, []byte(password))
			Expect(err).ToNot(HaveOccurred())
			Expect(len(wal.Accounts)).To(Equal(1))

			// Test Defaults
			Expect(wal.Accounts[0].TestNetAddress).To(Equal(""))
			Expect(wal.Accounts[0].Address).NotTo(Equal(""))
			Expect(wal.Accounts[0].AddressBackup).To(Equal(""))

			// Simulate a deprecated TestNetAddress Account
			wal.Accounts[0].TestNetAddress = "TestNetAddress"
			wal.Accounts[0].Address = "Address"
			wal.MustUpdate()

			Expect(wal.Accounts[0].TestNetAddress).To(Equal("TestNetAddress"))
			Expect(wal.Accounts[0].Address).To(Equal("Address"))
			Expect(wal.Accounts[0].AddressBackup).To(Equal(""))

			// Test to see if the Account has been updated to the new format
			a, ok := wal.GetAccountByName("test")
			Expect(ok).To(BeTrue())
			Expect(a.TestNetAddress).To(Equal("TestNetAddress"))
			Expect(a.Address).To(Equal("Address"))
			Expect(a.AddressBackup).To(Equal(""))

			// Make sure it was automagically saved
			wal2 := wallet.New(db)
			err = wal2.GetById(wal.Id())
			Expect(err).ToNot(HaveOccurred())
			Expect(len(wal2.Accounts)).To(Equal(1))

			a2 := wal2.Accounts[0]
			Expect(a2.TestNetAddress).To(Equal("TestNetAddress"))
			Expect(a2.Address).To(Equal("Address"))
			Expect(a2.AddressBackup).To(Equal(""))
		})

		It("should not do anything with regards to TestNetAddress for Ethereum Ropsten Type", func() {
			password := "Th1$1s@b@dp@$$w0rd"
			_, err := wal.CreateAccount("test", blockchains.EthereumRopstenType, []byte(password))
			Expect(err).ToNot(HaveOccurred())
			Expect(len(wal.Accounts)).To(Equal(1))

			// Test Defaults
			Expect(wal.Accounts[0].TestNetAddress).To(Equal(""))
			Expect(wal.Accounts[0].Address).NotTo(Equal(""))
			Expect(wal.Accounts[0].AddressBackup).To(Equal(""))

			// Simulate a deprecated TestNetAddress Account
			wal.Accounts[0].TestNetAddress = "TestNetAddress"
			wal.Accounts[0].Address = "Address"
			wal.MustUpdate()

			Expect(wal.Accounts[0].TestNetAddress).To(Equal("TestNetAddress"))
			Expect(wal.Accounts[0].Address).To(Equal("Address"))
			Expect(wal.Accounts[0].AddressBackup).To(Equal(""))

			// Test to see if the Account has been updated to the new format
			a, ok := wal.GetAccountByName("test")
			Expect(ok).To(BeTrue())
			Expect(a.TestNetAddress).To(Equal("TestNetAddress"))
			Expect(a.Address).To(Equal("Address"))
			Expect(a.AddressBackup).To(Equal(""))

			// Make sure it was automagically saved
			wal2 := wallet.New(db)
			err = wal2.GetById(wal.Id())
			Expect(err).ToNot(HaveOccurred())
			Expect(len(wal2.Accounts)).To(Equal(1))

			a2 := wal2.Accounts[0]
			Expect(a2.TestNetAddress).To(Equal("TestNetAddress"))
			Expect(a2.Address).To(Equal("Address"))
			Expect(a2.AddressBackup).To(Equal(""))
		})
	})
})
