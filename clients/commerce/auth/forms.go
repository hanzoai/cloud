package auth

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/util/form"
)

type LoginForm struct {
	Email    string
	Password string
}

func (f LoginForm) PasswordHashAndCompare(hash []byte) bool {
	return password.HashAndCompare(hash, f.Password)
}

func (f LoginForm) PasswordHash() ([]byte, error) {
	return password.Hash(f.Password)
}

func (f *LoginForm) Parse(c *gin.Context) error {
	if err := form.Parse(c, f); err != nil {
		return err
	}

	f.Email = strings.TrimSpace(strings.ToLower(f.Email))

	return nil
}
