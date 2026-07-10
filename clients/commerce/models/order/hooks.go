package order

import (
	"strings"
)

// Hooks
func (o *Order) BeforeCreate() error {
	o.Email = strings.ToLower(o.Email)
	o.GiftEmail = strings.ToLower(o.GiftEmail)
	return nil
}
