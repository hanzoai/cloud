package test

import (
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/oauthtoken"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("models/tokens", t)
}

var (
	ctx       ae.Context
	db        *datastore.Datastore
	secretKey []byte
	refClaims oauthtoken.Claims
	apiClaims oauthtoken.Claims
)

// Setup test context
var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	db = datastore.New(ctx)

	secretKey = []byte("SECRET")

	refClaims = oauthtoken.Claims{}
	refClaims.OrganizationName = "ORG"
	refClaims.UserId = "USER"
	refClaims.Type = oauthtoken.Reference
	refClaims.JTI = "JTI"
	refClaims.IssuedAt = time.Now().Unix()

	apiClaims = oauthtoken.Claims{}
	apiClaims.AppId = "APPID"
	apiClaims.OrganizationName = "ORG"
	apiClaims.Type = oauthtoken.Api
	apiClaims.JTI = "JTI"
	apiClaims.IssuedAt = time.Now().Unix()
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})

var _ = Describe("models/token", func() {
	Context("Should Create and Validate Tokens", func() {
		It("Should validate a Ref Token", func() {
			Expect(oauthtoken.IsReference(refClaims)).To(Equal(true))
			Expect(oauthtoken.IsRefresh(refClaims)).To(Equal(false))
			Expect(oauthtoken.IsAccess(refClaims)).To(Equal(false))
			Expect(oauthtoken.IsApi(refClaims)).To(Equal(false))
			Expect(oauthtoken.IsCustomer(refClaims)).To(Equal(false))
		})
		It("Should validate a Refresh Token", func() {
			tok := oauthtoken.New(db)
			tok.Claims = refClaims
			str, err := tok.IssueRefreshToken("USER", secretKey)
			Expect(err).ToNot(HaveOccurred())

			tok2 := oauthtoken.New(db)
			tok2.Decode(str, secretKey)
			claims := tok2.Claims

			Expect(oauthtoken.IsReference(claims)).To(Equal(false))
			Expect(oauthtoken.IsRefresh(claims)).To(Equal(true))
			Expect(oauthtoken.IsAccess(claims)).To(Equal(false))
			Expect(oauthtoken.IsApi(claims)).To(Equal(false))
			Expect(oauthtoken.IsCustomer(claims)).To(Equal(false))
		})
		It("Should validate an Access Token", func() {
			tok := oauthtoken.New(db)
			tok.Claims = refClaims
			str, err := tok.IssueAccessToken("USER", secretKey)
			Expect(err).ToNot(HaveOccurred())

			tok2 := oauthtoken.New(db)
			tok2.Claims = oauthtoken.Claims{}
			tok2.Decode(str, secretKey)
			claims := tok2.Claims

			Expect(oauthtoken.IsReference(claims)).To(Equal(false))
			Expect(oauthtoken.IsRefresh(claims)).To(Equal(false))
			Expect(oauthtoken.IsAccess(claims)).To(Equal(true))
			Expect(oauthtoken.IsApi(claims)).To(Equal(false))
			Expect(oauthtoken.IsCustomer(claims)).To(Equal(false))
		})
		It("Should validate a API Key", func() {
			Expect(oauthtoken.IsReference(apiClaims)).To(Equal(false))
			Expect(oauthtoken.IsRefresh(apiClaims)).To(Equal(false))
			Expect(oauthtoken.IsAccess(apiClaims)).To(Equal(false))
			Expect(oauthtoken.IsApi(apiClaims)).To(Equal(true))
			Expect(oauthtoken.IsAccess(apiClaims)).To(Equal(false))
		})
		It("Should validate an Customer Token", func() {
			tok := oauthtoken.New(db)
			tok.Claims = apiClaims
			str, err := tok.IssueAccessToken("USER", secretKey)
			Expect(err).ToNot(HaveOccurred())

			tok2 := oauthtoken.New(db)
			tok2.Claims = oauthtoken.Claims{}
			tok2.Decode(str, secretKey)
			claims := tok2.Claims

			Expect(oauthtoken.IsReference(claims)).To(Equal(false))
			Expect(oauthtoken.IsRefresh(claims)).To(Equal(false))
			Expect(oauthtoken.IsAccess(claims)).To(Equal(false))
			Expect(oauthtoken.IsApi(claims)).To(Equal(false))
			Expect(oauthtoken.IsCustomer(claims)).To(Equal(true))
		})
	})
})
