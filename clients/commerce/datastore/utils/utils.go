package utils

import (
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

// ErrFieldMismatch is returned when a field in the entity does not match
// the datastore property.
type ErrFieldMismatch struct {
	StructType string
	FieldName  string
	Reason     string
}

func (e *ErrFieldMismatch) Error() string {
	return "datastore: cannot load field " + e.StructType + "." + e.FieldName + ": " + e.Reason
}

// Helper to ignore tedious field mismatch errors (but warn appropriately
// during development)
func IgnoreFieldMismatch(err error) error {
	if err == nil {
		return nil
	}

	if _, ok := err.(*ErrFieldMismatch); ok {
		log.Warn("Ignoring, %v", err)
		return nil
	}

	// Also handle multi-errors that might contain field mismatch errors
	if me, ok := err.(MultiError); ok {
		hasRealErrors := false
		for _, e := range me {
			if e != nil {
				if _, ok := e.(*ErrFieldMismatch); !ok {
					hasRealErrors = true
					break
				}
			}
		}
		if !hasRealErrors {
			log.Warn("Ignoring field mismatch errors: %v", err)
			return nil
		}
	}

	return err
}

// Completely ignore them even during development
func ReallyIgnoreFieldMismatch(err error) error {
	if err == nil {
		return nil
	}

	if _, ok := err.(*ErrFieldMismatch); ok {
		return nil
	}

	return err
}

// MultiError is a slice of errors returned from batch operations
type MultiError []error

func (m MultiError) Error() string {
	s := ""
	n := 0
	for _, e := range m {
		if e != nil {
			if n == 0 {
				s = e.Error()
			}
			n++
		}
	}
	switch n {
	case 0:
		return "(0 errors)"
	case 1:
		return s
	case 2:
		return s + " (and 1 other error)"
	}
	return s + " (and " + string(rune('0'+n-1)) + " other errors)"
}

// Standard errors - aliased from db package for compatibility
var (
	ErrNoSuchEntity      = db.ErrNoSuchEntity
	ErrInvalidKey        = db.ErrInvalidKey
	ErrInvalidEntityType = db.ErrInvalidEntityType
)
