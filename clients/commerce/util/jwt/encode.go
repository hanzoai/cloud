package jwt

import (
	"encoding/json"

	"github.com/golang-jwt/jwt"
)

func Encode(claims Claimable, secret []byte, algorithm string) (string, error) {
	var (
		headerJson, jsonValue []byte
		str, sig              string
		err                   error
	)

	method := jwt.GetSigningMethod(algorithm)

	// Lookup signature method
	if method == nil {
		return "", UnspecifiedSigningMethod
	}

	header := Header{
		Type:      "JWT",
		Algorithm: algorithm,
	}

	if headerJson, err = json.Marshal(header); err != nil {
		return "", err
	}

	if jsonValue, err = json.Marshal(claims); err != nil {
		return "", err
	}

	str = jwt.EncodeSegment(headerJson) + "." + jwt.EncodeSegment(jsonValue)

	if sig, err = method.Sign(str, secret); err != nil {
		return "", err
	}
	return str + "." + sig, nil
}
