package timeutil

import (
	"time"
)

func IsZero(t time.Time) bool {
	if t.IsZero() {
		return true
	}

	// handle zero date from datastore
	if t.Unix() == -2177452800 {
		return true
	}

	return false
}

