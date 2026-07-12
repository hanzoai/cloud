package jwt

import (
	"encoding/json"
	"strings"

	"github.com/golang-jwt/jwt/v4"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

// Like Decode but no validation, don't forget to actually validate using decode
func Peek(tokenString string, claims Claimable) error {
	// Make Sure Claims is a struct pointer
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		log.Error("Invalid Segments")
		return InvalidNumberOfSegments
	}

	// Parse Claims
	var (
		claimBytes []byte
		err        error
	)

	if claimBytes, err = jwt.DecodeSegment(parts[1]); err != nil {
		log.Error("Malformed Token %v", err)
		return MalformedToken
	}
	if err := json.Unmarshal(claimBytes, claims); err != nil {
		log.Error("Malformed Token %v", err)
		return MalformedToken
	}

	return nil
}
