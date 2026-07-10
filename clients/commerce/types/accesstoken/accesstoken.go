package accesstoken

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/jwt"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"
)

const (
	Algorithm = "HS256"
)

type AccessToken struct {
	Claims

	String string `json:"string"`
}

func New(name string, subject string, permissions bit.Mask) *AccessToken {
	tok := new(AccessToken)
	tok.JTI = rand.ShortId()
	tok.Subject = subject
	tok.IssuedAt = time.Now().Unix()
	tok.Name = name
	tok.Permissions = bit.Field(permissions)
	return tok
}

func (a *AccessToken) Encode(secret []byte) string {
	if str, err := jwt.Encode(a.Claims, secret, Algorithm); err != nil {
		panic(err)
	} else {
		a.String = str
		return str
	}
}

func (a *AccessToken) Decode(str string, secret []byte) error {
	return jwt.Decode(str, secret, Algorithm, &a.Claims)
}

func (a *AccessToken) Peek(str string) error {
	return jwt.Peek(str, &a.Claims)
}

func (a *AccessToken) Verify(secret []byte) (bool, error) {
	if err := jwt.Decode(a.String, secret, Algorithm, &a.Claims); err != nil {
		return false, err
	}

	return true, nil
}
