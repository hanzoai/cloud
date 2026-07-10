package jwt

import "errors"

var (
	MalformedToken            = errors.New("Token is malformed")
	InvalidNumberOfSegments   = errors.New("Token contains an invalid number of segments")
	UnavailableSigningMethod  = errors.New("Token is unverifiable, signing method is unavailable")
	UnspecifiedSigningMethod  = errors.New("Token is unverifiable, signing method is unspecified")
	TokenIsExpired            = errors.New("Token is expired")
	TokenIsNotValidYet        = errors.New("Token is not valid yet")
	TokenCouldNotBeDecoded    = errors.New("Token could not be decoded")
	SigningAlgorithmIncorrect = errors.New("Algorithm in header does not match signing algorithm")
)
