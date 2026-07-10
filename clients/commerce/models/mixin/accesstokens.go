package mixin

import (
	"errors"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/types/accesstoken"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
)

// Error for expired jti's
var ErrorExpiredToken = errors.New("This token is expired")
var TokenNotFound = errors.New("Token not found")
var TokenNotFoundByName = errors.New("Token not found by name")

// AccessTokens is a mixin for securing objects with an AccessTokens
type AccessTokens struct {
	// Entity is a struct with a Entity mixin
	Entity Entity `json:"-" datastore:"-"`

	// JWT secret (persisted for token verification)
	SecretKey []byte `json:"secretKey,omitempty"`

	// UseTokenId as JWT "jti" param, randomly generate upon generating a new key to expire all existing keys
	Tokens []accesstoken.AccessToken `json:"tokens,omitempty"`
}

func (at *AccessTokens) Init(e Entity) {
	at.Entity = e
}

func (at *AccessTokens) AddToken(name string, permissions bit.Mask) string {
	// Generate a new TokenId to invalidate previous key
	tok := accesstoken.New(name, at.Entity.Id(), permissions)
	tok.Encode(at.SecretKey)

	at.Tokens = append(at.Tokens, *tok)
	return tok.String
}

func (at *AccessTokens) CompareToken(tok1, tok2 *accesstoken.AccessToken) error {
	if tok1.JTI != tok2.JTI {
		return ErrorExpiredToken
	}

	if tok1.Permissions != tok2.Permissions {
		return ErrorExpiredToken
	}

	return nil
}

func (at *AccessTokens) GetTokenByName(name string) (*accesstoken.AccessToken, error) {
	for _, tok := range at.Tokens {
		if tok.Name == name {
			return &tok, nil
		}
	}

	log.Warn("Token not found by name '%s'", name)
	return nil, TokenNotFoundByName
}

func (at *AccessTokens) MustGetTokenByName(name string) *accesstoken.AccessToken {
	tok, err := at.GetTokenByName(name)
	if err != nil {
		panic(err)
	}
	return tok
}

func (at *AccessTokens) GetToken(accessToken string) (*accesstoken.AccessToken, error) {
	tok := &accesstoken.AccessToken{}
	err := tok.Peek(accessToken)
	if err != nil {
		return tok, err
	}

	tok.String = accessToken

	// Try to fetch model using EntityId on token
	if err := at.Entity.GetById(tok.Subject); err != nil {
		return tok, err
	}

	for _, _tok := range at.Tokens {
		if tok.Name == _tok.Name {
			return tok, at.CompareToken(tok, &_tok)
		}
	}

	log.Warn("Token not found: %v", tok)
	return tok, TokenNotFound
}

func (at *AccessTokens) RemoveToken(name string) {
	num := len(at.Tokens)
	tokens := make([]accesstoken.AccessToken, 0)
	if num <= 0 {
		at.Tokens = tokens
		return
	}

	// Loop over tokens looking for token to delete. We need to check every
	// token in case a duplicate was saved
	for i := 0; i < num; i++ {
		if at.Tokens[i].Name != name {
			tokens = append(tokens, at.Tokens[i])
		}
	}

	at.Tokens = tokens
}

func (at *AccessTokens) ClearTokens() {
	at.Tokens = make([]accesstoken.AccessToken, 0)
}

func (at *AccessTokens) GetWithAccessToken(accessToken string) (*accesstoken.AccessToken, error) {
	tok, err := at.GetToken(accessToken)
	if err != nil {
		log.Warn("Failed to get %v using token '%v': %v, %s", at.Entity.Kind(), accessToken, err)
		return tok, err
	}

	return tok, nil
}
