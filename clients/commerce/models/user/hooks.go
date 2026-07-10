package user

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/util/crypto/sha256"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/webhook"
)

// Hooks
func (u *User) BeforeCreate() error {
	u.Username = strings.ToLower(u.Username)
	u.Email = strings.ToLower(u.Email)
	return nil
}

func (u *User) BeforeUpdate(prev *User) error {
	u.Username = strings.ToLower(u.Username)
	u.Email = strings.ToLower(u.Email)
	u.KYC.Hash = sha256.Hash(string(json.EncodeBytes(&u.KYC.KYCData)))

	return nil
}

func (u *User) AfterCreate() error {
	webhook.Emit(u.Context(), u.Namespace(), "user.created", u)

	return nil
}

func (u *User) AfterUpdate(prev *User) error {
	webhook.Emit(u.Context(), u.Namespace(), "user.updated", u)
	return nil
}

func (u *User) AfterDelete() error {
	webhook.Emit(u.Context(), u.Namespace(), "user.deleted", u)

	return nil
}
