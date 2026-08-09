// Package environ reads settings out of the process environment.
//
// A LEAF, for the reason internal/mint is one: several of the packages root cloud
// imports read the environment too, and they cannot import cloud back.
package environ

import (
	"os"
	"strings"
)

// Or returns the variable named by key, or def when it is unset or blank.
//
// BLANK MEANS BLANK. A variable holding only spaces is not a value, and twenty-five
// packages disagreed about that — ten read it raw, so CLOUD_BRAND="   " named a
// brand of three spaces, and the blank travelled into a chain name and a model
// name before anything noticed. Trimming here is what makes "set" mean the same
// thing everywhere.
//
// The value is returned TRIMMED, since the padding was never part of it.
func Or(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
