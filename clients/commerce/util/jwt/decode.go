package jwt

import (
	"encoding/json"
	"strings"

	"github.com/golang-jwt/jwt"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

// Call to get verification, claims need to be decoded either way so there's not point in just running the validation in isolation
func Decode(tokenString string, secret []byte, algorithm string, claims Claimable) error {
	// Make Sure Claims is a struct pointer
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		log.Error("Invalid Segments")
		return InvalidNumberOfSegments
	}

	var err error

	// Parse Header
	var headerBytes []byte
	if headerBytes, err = jwt.DecodeSegment(parts[0]); err != nil {
		log.Error("Malformed Token %v", err)
		return MalformedToken
	}

	var header = &Header{}

	if err = json.Unmarshal(headerBytes, header); err != nil {
		log.Error("Malformed Token %v", err)
		return MalformedToken
	}

	// Check Header
	alg := header.Algorithm

	if alg != algorithm {
		log.Error("Signing Algorithm Mismatch")
		return SigningAlgorithmIncorrect
	}

	method := jwt.GetSigningMethod(alg)

	// Lookup signature method
	if method == nil {
		log.Error("Signing Algorithm Does Not Exist")
		return UnspecifiedSigningMethod
	}

	// Parse Claims
	var claimBytes []byte
	if claimBytes, err = jwt.DecodeSegment(parts[1]); err != nil {
		log.Error("Malformed Token %v", err)
		return MalformedToken
	}
	if err = json.Unmarshal(claimBytes, claims); err != nil {
		log.Error("Malformed Token %v", err)
		return MalformedToken
	}

	// Check Claims
	if err := claims.Validate(); err != nil {
		log.Warn("Claims Invalid %v", err)
		return err
	}

	// Check expiration times
	now := jwt.TimeFunc().Unix()

	if claims.BeforeExp(now) {
		log.Warn("Token is Expired")
		return TokenIsExpired
	}

	if claims.AfterNbf(now) {
		log.Warn("Token not yet Valid")
		return TokenIsNotValidYet
	}

	// Perform validation
	sig := parts[2]
	if err = method.Verify(strings.Join(parts[0:2], "."), sig, secret); err != nil {
		log.Warn("Verify %v, %v, %v, %v", claims, strings.Join(parts[0:2], "."), sig, string(secret))
		return err
	}

	return nil
}
