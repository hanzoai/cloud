package wallet

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"

	"github.com/hanzoai/cloud/clients/commerce/util/crypto/aes"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"
)

type Account struct {
	Encrypted string `json:"encrypted"`
	Salt      string `json:"salt"`

	Name       string `json:"name"`
	PrivateKey string `json:"-" datastore:"-"`
	PublicKey  string `json:"-"`
	Address    string `json:"address,omitempty"`

	// Can this account be withdrawn from?  This is on the org.
	Withdrawable bool             `json:"-"`
	Deleted      bool             `json:"-"`
	Type         blockchains.Type `json:"type"`

	CreatedAt time.Time `json:"createdAt,omitempty"`

	// Ignore these, this is deprecated
	TestNetAddress string `json:"-"`
	AddressBackup  string `json:"-"`
}

// Encrypt the Account's Private Key
func (a *Account) Encrypt(withPassword []byte) error {
	if a.PrivateKey == "" {
		return ErrorNoPrivateKeySet
	}

	// generate salt
	salt := rand.SecretKey()
	a.Salt = salt

	key, err := aes.AES128KeyFromPassword(withPassword, []byte(salt))

	if err != nil {
		return err
	}

	e, err := aes.EncryptCBC(key, a.PrivateKey)

	if err != nil {
		return err
	}

	a.Encrypted = e

	return nil
}

// Decrypt the Account's Private Key
func (a *Account) Decrypt(withPassword []byte) error {
	if a.Encrypted == "" {
		return ErrorNoEncryptedKeyFound
	}

	if a.Salt == "" {
		return ErrorNoSaltSetError
	}

	key, err := aes.AES128KeyFromPassword(withPassword, []byte(a.Salt))

	p, err := aes.DecryptCBC(key, a.Encrypted)

	if err != nil {
		return err
	}

	a.PrivateKey = p

	return nil
}

func (a *Account) Delete() {
	a.Deleted = true
}
