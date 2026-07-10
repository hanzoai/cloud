package test

import (
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/util/jwt"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("util/jwt", t)
}

var (
	secret                             = []byte("secret")
	notValidError                      = errors.New("NotValid")
	TestValueEqualTestToken            = "eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJUZXN0VmFsdWUiOiJ0ZXN0In0.z5-7UZckrVZK37fEGrgvZsHyJSL3G_Rj_bJhbgxJkKA"
	TestValueEqualTestMissingPartToken = "eyJUZXN0VmFsdWUiOiJ0ZXN0In0.z5-7UZckrVZK37fEGrgvZsHyJSL3G_Rj_bJhbgxJkKA"
	TestValueEqualTestExtraPartToken   = "eyJUZXN0VmFsdWUiOiJ0ZXN0In0.eyJUZXN0VmFsdWUiOiJ0ZXN0In0.eyJUZXN0VmFsdWUiOiJ0ZXN0In0.z5-7UZckrVZK37fEGrgvZsHyJSL3G_Rj_bJhbgxJkKA"
	TestValueEqualTestForgedToken      = "eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzUxMyJ9.eyJUZXN0VmFsdWUiOiJ0ZXN0In0.QbvfWlwJLxkXK3kHb4PA5Wn1stvIXtcf92x9vmkvxF8"
)

type TestClaims struct {
	jwt.Claims

	TestValue string
}

var _ = Describe("JWT Encode", func() {
	It("Should Encode Correctly", func() {
		claims := TestClaims{
			TestValue: "test",
		}

		str, err := jwt.Encode(&claims, secret, "HS256")
		Expect(err).To(BeNil())
		Expect(str).To(Equal(TestValueEqualTestToken))
	})

	It("Should Check Encoding Algorithm Exists", func() {
		claims := TestClaims{
			TestValue: "test",
		}

		_, err := jwt.Encode(&claims, secret, "NOTREAL")
		Expect(err).To(Equal(jwt.UnspecifiedSigningMethod))
	})
})

var _ = Describe("JWT Decode", func() {
	It("Should Decode Correctly", func() {
		claims := &TestClaims{}
		str := TestValueEqualTestToken
		err := jwt.Decode(str, secret, "HS256", claims)
		Expect(err).To(BeNil())
		Expect(claims.TestValue).To(Equal("test"))
	})

	It("Should Peek Correctly", func() {
		claims := &TestClaims{}
		str := TestValueEqualTestToken
		err := jwt.Peek(str, claims)
		Expect(err).To(BeNil())
		Expect(claims.TestValue).To(Equal("test"))
	})

	It("Should Check Too Few Segments", func() {
		claims := &TestClaims{}
		str := TestValueEqualTestMissingPartToken
		err := jwt.Decode(str, secret, "HS256", claims)
		Expect(err).To(Equal(jwt.InvalidNumberOfSegments))
	})

	It("Should Check Too Many Segments", func() {
		claims := &TestClaims{}
		str := TestValueEqualTestExtraPartToken
		err := jwt.Decode(str, secret, "HS256", claims)
		Expect(err).To(Equal(jwt.InvalidNumberOfSegments))
	})

	It("Should Check Algorithm Matches One Used", func() {
		claims := &TestClaims{}
		str := TestValueEqualTestToken
		err := jwt.Decode(str, secret, "HS512", claims)
		Expect(err).To(Equal(jwt.SigningAlgorithmIncorrect))
	})

	It("Should Check Algorithm Exists", func() {
		claims := &TestClaims{}
		str := TestValueEqualTestForgedToken
		err := jwt.Decode(str, secret, "HS513", claims)
		Expect(err).To(Equal(jwt.UnspecifiedSigningMethod))
	})

	It("Should Fail On Validator Correctly", func() {
		valid := false
		validateFn := func() error {
			if !valid {
				return notValidError
			}

			return nil
		}

		claims := &TestClaims{
			Claims: jwt.Claims{
				ValidateFn: validateFn,
			},
		}

		str := TestValueEqualTestToken
		err := jwt.Decode(str, secret, "HS256", claims)
		Expect(err).To(Equal(notValidError))
	})

	It("Should Expire Correctly", func() {
		claims := &TestClaims{
			Claims: jwt.Claims{
				ExpirationTime: time.Now().Unix() - 10000,
			},
		}
		str := TestValueEqualTestToken
		err := jwt.Decode(str, secret, "HS256", claims)
		Expect(err).To(Equal(jwt.TokenIsExpired))
	})

	It("Should Wait Correctly", func() {
		claims := &TestClaims{
			Claims: jwt.Claims{
				NotBefore: time.Now().Unix() + 10000,
			},
		}
		str := TestValueEqualTestToken
		err := jwt.Decode(str, secret, "HS256", claims)
		Expect(err).To(Equal(jwt.TokenIsNotValidYet))
	})

	It("Should Expire And Wait Correctly", func() {
		claims := &TestClaims{
			Claims: jwt.Claims{
				NotBefore:      time.Now().Unix() + 10000,
				ExpirationTime: time.Now().Unix() - 10000,
			},
		}
		str := TestValueEqualTestToken
		err := jwt.Decode(str, secret, "HS256", claims)
		Expect(err).To(Equal(jwt.TokenIsExpired))
	})

	It("Should Clone Correctly", func() {
		claims := &TestClaims{
			Claims: jwt.Claims{
				JTI: "test",
			},
		}

		c2 := claims.Clone().(jwt.Claims)
		c2.JTI = "test2"

		Expect(c2.JTI).ToNot(Equal(claims.JTI))
	})
})
