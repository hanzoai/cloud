package test

import (
	"net/url"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/oauthtoken"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/jwt"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ginclient"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"

	authApi "github.com/hanzoai/cloud/clients/commerce/api/auth"
)

func Test(t *testing.T) {
	Setup("api/auth", t)
}

var (
	ctx      ae.Context
	cl       *ginclient.Client
	db       *datastore.Datastore
	org      *organization.Organization
	refToken *oauthtoken.Token
	usr      *user.User
	usr2     *user.User
)

// Setup test context
var _ = BeforeSuite(func() {
	// Don't need this but switching to it to avoid long timeout.
	var err error
	ctx = ae.NewContext()
	Expect(err).NotTo(HaveOccurred())

	// Create mock gin context that we can use with fixtures
	c := gincontext.New(ctx)

	// Run fixtures
	org = fixtures.Organization(c).(*organization.Organization)

	// Setup client and add routes for account API tests.
	cl = ginclient.New(ctx)
	authApi.Route(cl.Router)

	// Create organization for tests, apiKey
	db = datastore.New(ctx)

	usr = user.New(db)
	usr.Email = "auth-test@hanzo.ai"
	usr.SetPassword("Z0rd0N")
	usr.Enabled = true
	usr.MustPut()

	usr2 = user.New(db)
	usr2.Email = "auth-test-disabled@hanzo.ai"
	usr2.SetPassword("ilikedragons")
	usr2.Enabled = false
	usr2.MustCreate()

	refToken, err = org.ResetReferenceToken(usr, oauthtoken.Claims{})
	Expect(err).NotTo(HaveOccurred())
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})

var _ = Describe("auth", func() {
	Context("Password Grant JSON", func() {
		It("Should allow login with password grant", func() {
			req := authApi.OAuthRequest{
				Username:  "auth-test@hanzo.ai",
				Password:  "Z0rd0N",
				ClientId:  org.Name,
				GrantType: "password",
			}

			j := json.Encode(req)
			log.Debug("Json %v", j)

			w := cl.PostRawJSON("/auth", j)
			res := authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(200))
			// TODO: should deconstruct token and test if the user id is in it
			Expect(res.AccessToken).ToNot(Equal(""))
			aClaims := oauthtoken.Claims{}
			err := jwt.Decode(res.AccessToken, org.SecretKey, oauthtoken.Algorithm, &aClaims)
			Expect(err).NotTo(HaveOccurred())

			Expect(string(aClaims.Type)).To(Equal(oauthtoken.Access))
			Expect(aClaims.UserId).To(Equal(refToken.Claims.UserId))
			Expect(aClaims.OrganizationName).To(Equal(refToken.Claims.OrganizationName))
			Expect(aClaims.Issuer).To(Equal(refToken.Id()))

			Expect(res.RefreshToken).ToNot(Equal(""))
			rClaims := oauthtoken.Claims{}
			err = jwt.Decode(res.RefreshToken, org.SecretKey, oauthtoken.Algorithm, &rClaims)
			Expect(err).NotTo(HaveOccurred())

			Expect(string(rClaims.Type)).To(Equal(oauthtoken.Refresh))
			Expect(rClaims.UserId).To(Equal(refToken.Claims.UserId))
			Expect(rClaims.OrganizationName).To(Equal(refToken.Claims.OrganizationName))
			Expect(rClaims.Issuer).To(Equal(refToken.Id()))
			Expect(int(rClaims.Permissions)).To(Equal(0))

			Expect(res.ExpiresIn).ToNot(Equal(24 * 7 * 3600))
			Expect(res.TokenType).To(Equal("jwt"))
		})
	})

	Context("Password Grant Form", func() {
		It("Should allow login with password grant", func() {
			data := url.Values{
				"username":   {"auth-test@hanzo.ai"},
				"password":   {"Z0rd0N"},
				"client_id":  {org.Name},
				"grant_type": {"password"},
			}

			w := cl.PostForm("/auth", data)
			res := authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(200))
			// TODO: should deconstruct token and test if the user id is in it
			Expect(res.AccessToken).ToNot(Equal(""))
			aClaims := oauthtoken.Claims{}
			err := jwt.Decode(res.AccessToken, org.SecretKey, oauthtoken.Algorithm, &aClaims)
			Expect(err).NotTo(HaveOccurred())

			Expect(string(aClaims.Type)).To(Equal(oauthtoken.Access))
			Expect(aClaims.UserId).To(Equal(refToken.Claims.UserId))
			Expect(aClaims.OrganizationName).To(Equal(refToken.Claims.OrganizationName))
			Expect(aClaims.Issuer).To(Equal(refToken.Id()))

			Expect(res.RefreshToken).ToNot(Equal(""))
			rClaims := oauthtoken.Claims{}
			err = jwt.Decode(res.RefreshToken, org.SecretKey, oauthtoken.Algorithm, &rClaims)
			Expect(err).NotTo(HaveOccurred())

			Expect(string(rClaims.Type)).To(Equal(oauthtoken.Refresh))
			Expect(rClaims.UserId).To(Equal(refToken.Claims.UserId))
			Expect(rClaims.OrganizationName).To(Equal(refToken.Claims.OrganizationName))
			Expect(rClaims.Issuer).To(Equal(refToken.Id()))
			Expect(int(rClaims.Permissions)).To(Equal(0))

			Expect(res.ExpiresIn).ToNot(Equal(24 * 7 * 3600))
			Expect(res.TokenType).To(Equal("jwt"))
		})

		It("Should disallow login with disabled account", func() {
			data := url.Values{
				"username":   {"auth-test-disabled@hanzo.ai"},
				"password":   {"ilikedragon"},
				"client_id":  {org.Name},
				"grant_type": {"password"},
			}

			w := cl.PostForm("/auth", data)
			res := authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(401))
		})

		It("Should disallow login with wrong password", func() {
			data := url.Values{
				"username":   {"auth-test@hanzo.ai"},
				"password":   {"z3d"},
				"client_id":  {org.Name},
				"grant_type": {"password"},
			}

			w := cl.PostForm("/auth", data)
			res := authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(401))
		})

		It("Should disallow login with wrong email", func() {
			data := url.Values{
				"username":   {"billy@blue.co.uk"},
				"password":   {"bloo"},
				"client_id":  {org.Name},
				"grant_type": {"password"},
			}

			w := cl.PostForm("/auth", data)
			res := authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(401))
		})

		It("Should disallow login with invalid grant", func() {
			data := url.Values{
				"username":   {"auth-test@hanzo.ai"},
				"password":   {"Z0rd0N"},
				"client_id":  {org.Name},
				"grant_type": {"not grant"},
			}

			w := cl.PostForm("/auth", data)
			res := authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(400))
		})
	})

	Context("Refresh Grant JSON", func() {
		It("Should allow login with refresh grant", func() {
			req := authApi.OAuthRequest{
				Username:  "auth-test@hanzo.ai",
				Password:  "Z0rd0N",
				ClientId:  org.Name,
				GrantType: "password",
			}

			j := json.Encode(req)
			log.Debug("Json %v", j)

			w := cl.PostRawJSON("/auth", j)
			res := authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(200))

			// Shove the issued refresh token back in
			req2 := authApi.OAuthRequest{
				RefreshToken: res.RefreshToken,
				GrantType:    "refresh_token",
			}

			j = json.Encode(req2)
			log.Debug("Json %v", j)

			w = cl.PostRawJSON("/auth", j)
			res = authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(200))

			// TODO: should deconstruct token and test if the user id is in it
			Expect(res.AccessToken).ToNot(Equal(""))
			aClaims := oauthtoken.Claims{}
			err := jwt.Decode(res.AccessToken, org.SecretKey, oauthtoken.Algorithm, &aClaims)
			Expect(err).NotTo(HaveOccurred())

			Expect(string(aClaims.Type)).To(Equal(oauthtoken.Access))
			Expect(aClaims.UserId).To(Equal(refToken.Claims.UserId))
			Expect(aClaims.OrganizationName).To(Equal(refToken.Claims.OrganizationName))
			Expect(aClaims.Issuer).To(Equal(refToken.Id()))

			Expect(res.RefreshToken).ToNot(Equal(""))
			rClaims := oauthtoken.Claims{}
			err = jwt.Decode(res.RefreshToken, org.SecretKey, oauthtoken.Algorithm, &rClaims)
			Expect(err).NotTo(HaveOccurred())

			Expect(string(rClaims.Type)).To(Equal(oauthtoken.Refresh))
			Expect(rClaims.UserId).To(Equal(refToken.Claims.UserId))
			Expect(rClaims.OrganizationName).To(Equal(refToken.Claims.OrganizationName))
			Expect(rClaims.Issuer).To(Equal(refToken.Id()))
			Expect(int(rClaims.Permissions)).To(Equal(0))

			Expect(res.ExpiresIn).ToNot(Equal(24 * 7 * 3600))
			Expect(res.TokenType).To(Equal("jwt"))
		})
	})

	Context("Refresh Grant Form", func() {
		It("Should allow login with refresh grant", func() {
			data := url.Values{
				"username":   {"auth-test@hanzo.ai"},
				"password":   {"Z0rd0N"},
				"client_id":  {org.Name},
				"grant_type": {"password"},
			}

			w := cl.PostForm("/auth", data)
			res := authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(200))

			// Shove the issued refresh token back in
			data = url.Values{
				"refresh_token": {res.RefreshToken},
				"grant_type":    {"refresh_token"},
			}

			w = cl.PostForm("/auth", data)
			res = authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(200))

			// TODO: should deconstruct token and test if the user id is in it
			Expect(res.AccessToken).ToNot(Equal(""))
			aClaims := oauthtoken.Claims{}
			err := jwt.Decode(res.AccessToken, org.SecretKey, oauthtoken.Algorithm, &aClaims)
			Expect(err).NotTo(HaveOccurred())

			Expect(string(aClaims.Type)).To(Equal(oauthtoken.Access))
			Expect(aClaims.UserId).To(Equal(refToken.Claims.UserId))
			Expect(aClaims.OrganizationName).To(Equal(refToken.Claims.OrganizationName))
			Expect(aClaims.Issuer).To(Equal(refToken.Id()))

			Expect(res.RefreshToken).ToNot(Equal(""))
			rClaims := oauthtoken.Claims{}
			err = jwt.Decode(res.RefreshToken, org.SecretKey, oauthtoken.Algorithm, &rClaims)
			Expect(err).NotTo(HaveOccurred())

			Expect(string(rClaims.Type)).To(Equal(oauthtoken.Refresh))
			Expect(rClaims.UserId).To(Equal(refToken.Claims.UserId))
			Expect(rClaims.OrganizationName).To(Equal(refToken.Claims.OrganizationName))
			Expect(rClaims.Issuer).To(Equal(refToken.Id()))
			Expect(int(rClaims.Permissions)).To(Equal(0))

			Expect(res.ExpiresIn).ToNot(Equal(24 * 7 * 3600))
			Expect(res.TokenType).To(Equal("jwt"))
		})

		It("Should disallow login with refresh grant for non-refresh token", func() {
			// Shove the issued refresh token back in
			data := url.Values{
				"refresh_token": {refToken.String},
				"grant_type":    {"refresh_token"},
			}
			w := cl.PostForm("/auth", data)
			res := authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(401))
		})
	})

	Context("Token Revokation", func() {
		It("Should deny grants if revoked", func() {
			var err error
			data := url.Values{
				"username":   {"auth-test@hanzo.ai"},
				"password":   {"Z0rd0N"},
				"client_id":  {org.Name},
				"grant_type": {"password"},
			}

			w := cl.PostForm("/auth", data)
			res := authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(200))

			refToken.Revoke()

			// Shove the issued refresh token back in
			data = url.Values{
				"refresh_token": {res.RefreshToken},
				"grant_type":    {"refresh_token"},
			}

			w = cl.PostForm("/auth", data)
			res = authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(403))

			data = url.Values{
				"username":   {"auth-test@hanzo.ai"},
				"password":   {"Z0rd0N"},
				"client_id":  {org.Name},
				"grant_type": {"password"},
			}

			w = cl.PostForm("/auth", data)
			res = authApi.OAuthResponse{}

			log.Debug("Res %v", w.Body)

			json.DecodeBuffer(w.Body, &res)

			Expect(w.Code).To(Equal(403))

			refToken, err = org.ResetReferenceToken(usr, oauthtoken.Claims{})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
