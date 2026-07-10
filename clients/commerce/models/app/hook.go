package app

import (
	"github.com/hanzoai/cloud/clients/commerce/util/rand"
)

// Hooks
func (a *App) BeforeCreate() error {
	a.SecretKey = []byte(rand.SecretKey())

	return nil
}

func (a *App) AfterCreate() error {
	a.ResetDefaultKeys()

	return nil
}
