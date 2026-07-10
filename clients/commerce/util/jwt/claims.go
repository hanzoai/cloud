package jwt

import "encoding/json"

type Claimable interface {
	Validate() error
	AfterNbf(int64) bool
	BeforeExp(int64) bool
	Clone() Claimable
}

// Audience handles JWT "aud" which can be either a string or array of strings.
type Audience string

func (a *Audience) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*a = Audience(s)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	if len(arr) > 0 {
		*a = Audience(arr[0])
	}
	return nil
}

type Claims struct {
	IssuedAt       int64 `json:"iat,omitempty"`
	ExpirationTime int64 `json:"exp,omitempty"`
	NotBefore      int64 `json:"nbf,omitempty"`

	Issuer   string   `json:"iss,omitempty"`
	Subject  string   `json:"sub,omitempty"`
	Audience Audience `json:"aud,omitempty"`
	JTI      string   `json:"jti,omitempty"`

	ValidateFn func() error `json:"-" datastore:"-"`
}

func (c Claims) Validate() error {
	if c.ValidateFn == nil {
		return nil
	}

	return c.ValidateFn()
}

func (c Claims) AfterNbf(now int64) bool {
	return c.NotBefore != 0 && now < c.NotBefore
}

func (c Claims) BeforeExp(now int64) bool {
	return c.ExpirationTime != 0 && now > c.ExpirationTime
}

func (c Claims) Clone() Claimable {
	return c
}

func (c Claims) JSON() string {
	j, _ := json.Marshal(c)
	return string(j)
}
