package app

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/oauthtoken"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/orm"
)

const (
	PublishedKey     = "pub-key"
	SecretKey        = "secret-key"
	TestPublishedKey = "test-pub-key"
	TestSecretKey    = "test-secret-key"
)

func init() { orm.Register[App]("app") }

type App struct {
	mixin.Model[App]

	Name string `json:"name"`

	ApiKeys []*oauthtoken.Token `json:"apiKeys,omitempty" datastore:"-"`

	SecretKey []byte `json:"-"`
}

func (a *App) NewApiKey(name string, claims oauthtoken.Claims) (*oauthtoken.Token, error) {
	a.RevokeApiKeyByName(name)

	tok := oauthtoken.New(a.Datastore())
	tok.Name = name

	claims.AppId = a.Id()
	claims.OrganizationName = a.Key().Namespace()
	claims.Type = oauthtoken.Api
	claims.JTI = tok.Id()
	claims.IssuedAt = time.Now().Unix()

	tok.Claims = claims
	tok.AccessPeriod = 24

	if _, err := tok.Encode(a.SecretKey); err != nil {
		return nil, err
	}

	tok.MustCreate()

	return tok, nil
}

func (a *App) GetApiKeyByName(name string) (*oauthtoken.Token, bool, error) {
	tok := oauthtoken.New(a.Datastore())

	if ok, err := tok.Query().Filter("Claims.AppId=", a.Id()).Filter("Claims.Type=", oauthtoken.Api).Filter("Revoked=", false).Filter("Name=", name).Get(); !ok {
		return nil, false, err
	}

	return tok, true, nil
}

func (a *App) RevokeApiKeyByName(name string) (*oauthtoken.Token, bool, error) {
	if tok, ok, err := a.GetApiKeyByName(name); !ok {
		return nil, false, err
	} else {
		tok.Revoke()
		return tok, true, nil
	}
}

func (a *App) LoadApiKeys() error {
	slice := make([]*oauthtoken.Token, 0)

	_, err := oauthtoken.Query(a.Datastore()).
		Filter("Claims.AppName=", a.Name).
		Filter("Claims.Type=", oauthtoken.Api).
		Filter("Revoked=", false).
		GetAll(&slice)

	a.ApiKeys = slice

	return err
}

func (a *App) ResetDefaultKeys() {
	pubClaims := oauthtoken.Claims{
		AppId:            a.Id(),
		OrganizationName: a.Key().Namespace(),
		Type:             oauthtoken.Api,
		Permissions:      bit.Field(permission.Published | permission.Live | permission.ReadCoupon | permission.ReadProduct | permission.WriteReferrer),
	}

	secretClaims := pubClaims.Clone().(oauthtoken.Claims)
	secretClaims.Permissions = bit.Field(permission.Admin | permission.Live)

	testPubClaims := pubClaims.Clone().(oauthtoken.Claims)
	testPubClaims.Test = true
	testPubClaims.Permissions = bit.Field(permission.Published | permission.Test | permission.ReadCoupon | permission.ReadProduct | permission.WriteReferrer)

	testSecretClaims := testPubClaims.Clone().(oauthtoken.Claims)
	testSecretClaims.Permissions = bit.Field(permission.Admin | permission.Test)

	var err error

	pubKey, err := a.NewApiKey(PublishedKey, pubClaims)
	if err != nil {
		panic(err)
	}

	secretKey, err := a.NewApiKey(SecretKey, secretClaims)
	if err != nil {
		panic(err)
	}

	testPubKey, err := a.NewApiKey(TestPublishedKey, testPubClaims)
	if err != nil {
		panic(err)
	}

	testSecretKey, err := a.NewApiKey(TestSecretKey, testSecretClaims)
	if err != nil {
		panic(err)
	}

	a.ApiKeys = []*oauthtoken.Token{
		pubKey,
		secretKey,
		testPubKey,
		testSecretKey,
	}
}

func New(db *datastore.Datastore) *App {
	a := new(App)
	a.Init(db)
	return a
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("app")
}
