package share

import "strings"

// replaceToken substitutes the {token} placeholder in a zrok URL template.
func replaceToken(template, token string) string {
	return strings.ReplaceAll(template, "{token}", token)
}
