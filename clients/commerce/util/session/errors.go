package session

import (
	"fmt"
)

// Missing key in session
type KeyError struct {
	key string
}

func (k KeyError) Error() string {
	return fmt.Sprintf("Missing Key: '%s'", k.key)
}
