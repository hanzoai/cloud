package query

import (
	"errors"

	"github.com/hanzoai/cloud/clients/commerce/db"
)

var (
	// Done signals end of iteration
	Done = errors.New("datastore: done")

	// Custom Errors - these are package-local to avoid conflicts with dot-imported utils
	InvalidKey  = db.ErrInvalidKey
	KeyNotFound = db.ErrNoSuchEntity
)
