package accesstoken

import (
	"encoding/json"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/jwt"
)

type Claims struct {
	jwt.Claims

	Name        string    `json:"name,omitempty"`
	UserId      string    `json:"uid,omitempty"`
	Permissions bit.Field `json:"bit,omitempty"`
}

func (c Claims) HasPermission(mask bit.Mask) bool {
	return c.Permissions.Has(mask)
}

func (c Claims) Clone() jwt.Claimable {
	return c
}

func (c Claims) JSON() string {
	j, _ := json.Marshal(c)
	return string(j)
}
