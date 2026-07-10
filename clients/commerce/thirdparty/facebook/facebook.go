package facebook

import (
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	fb "github.com/huandu/facebook"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/util/cache"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	// "github.com/hanzoai/cloud/clients/commerce/models"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

var appId = config.Facebook.AppId

var appSecret = config.Facebook.AppSecret

var _redirectUri string

// Sets _redirectUri as necessary for dev machines
// Uses config.UrlFor in production and staging.
func redirectUri(c *gin.Context) string {
	// if config.IsDevelopment && _redirectUri == "" {
	// 	client := urlfetch.Client(middleware.GetContext(c))
	// 	req, _ := http.NewRequest("GET", "http://checkip.amazonaws.com", nil)
	// 	res, _ := client.Do(req)
	// 	defer res.Body.Close()
	// 	b, _ := ioutil.ReadAll(res.Body)
	// 	ip := string(b)
	// 	ip = ip[0 : len(ip)-1]
	// 	_redirectUri = "http://" + ip + ":8080/store/auth/facebook_callback"
	// } else if (config.IsProduction || config.IsStaging) && _redirectUri == "" {
	// 	_redirectUri = url.QueryEscape("http://" + config.UrlFor("store", "/auth/facebook_callback/"))
	// }
	// return _redirectUri
	return ""
}

var graphVersion = config.Facebook.GraphVersion

var app *fb.App

func newSession(c *gin.Context, accessToken string) *fb.Session {
	if app == nil {
		app = fb.New(appId, appSecret)
		app.RedirectUri = redirectUri(c)
	}
	session := app.Session(accessToken)
	session.HttpClient = &http.Client{Timeout: 55 * time.Second}
	return session
}

// GET /auth/facebook_callback
func Callback(c *gin.Context) {
	// req := c.Request
	// e := req.URL.Query().Get("error")
	// if e != "" {
	// 	description := req.URL.Query().Get("error_description")
	// 	reason := req.URL.Query().Get("error_reason")
	// 	log.Info(
	// 		"Error in facebook callback \n %s \n %s \n %s",
	// 		e, reason, description,
	// 	)
	// 	return
	// }

	// if resState := req.URL.Query().Get("state"); true {
	// 	log.Debug(resState)
	// 	ctx := middleware.GetContext(c)
	// 	_, err := memcache.Get(ctx, resState)
	// 	if err != nil {
	// 		log.Panic("CSRF attempt \n\tExpected: %s", resState)
	// 	}
	// }

	// code := req.URL.Query().Get("code")
	// if code == "" {
	// 	log.Panic("No code found")
	// }
	// accessToken, err := exchangeCode(c, code)
	// if err != nil {
	// 	log.Panic(err)
	// }

	// session := newSession(c, accessToken)
	// if err := session.Validate(); err != nil {
	// 	log.Panic("AccessToken is invalid %s", session.AccessToken())
	// }

	// me, err := session.Get("/me", nil)
	// if err != nil {
	// 	log.Panic("Error accessing Graph API with accessToken", err)
	// }

	// user := new(models.User)
	// if err := me.Decode(&user.Facebook); err != nil {
	// 	log.Debug("%v")
	// }

	// if user.Facebook.Verified {
	// 	log.Debug("Verified")

	// 	user.FirstName = user.Facebook.FirstName
	// 	user.LastName = user.Facebook.LastName
	// 	user.Email = user.Facebook.Email

	// 	q := queries.New(c)
	// 	if err := q.UpsertUser(user); err != nil {
	// 		log.Debug("Failed to upsert user")
	// 		return
	// 	}

	// 	if err := auth.Login(c, user.Email); err != nil {
	// 		log.Debug("Failed to login")
	// 		log.Debug("%#v", err)
	// 		c.Redirect(302, config.UrlFor("store", "/login"))
	// 		return
	// 	}
	// 	c.Redirect(302, config.UrlFor("store"))
	// } else {
	// 	auth.Logout(c)
	// 	c.Redirect(302, config.UrlFor("store", "/login"))
	// }
}

// Generates a token and inserts it into memcache
// The token has a TTL of 3 minutes
var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

func CSRFToken(c *gin.Context) string {
	size := 16
	b := make([]rune, size)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	token := string(b)

	item := &cache.Item{
		Key:        token,
		Value:      []byte(token),
		Expiration: 3 * time.Minute,
	}

	ctx := middleware.GetContext(c)
	cache.Set(ctx, item)
	return url.QueryEscape(token)
}

// GET /auth/facebook
func LoginUser(c *gin.Context) {
	// state := CSRFToken(c)
	// log.Debug(state)
	// url := fmt.Sprintf(
	// 	"https://www.facebook.com/dialog/oauth?client_id=%s&state=%s&redirect_uri=%s&response_type=%s&scope=%s",
	// 	appId, state,
	// 	redirectUri(c), "code",
	// 	"email",
	// )

	// if auth.IsLoggedIn(c) {
	// 	log.Debug("Logged in")
	// 	user, _ := auth.GetUser(c)
	// 	if user.Facebook.AccessToken != "" {
	// 		session := newSession(c, user.Facebook.AccessToken)
	// 		if err := session.Validate(); err == nil {
	// 			// Token is still valid
	// 			auth.Login(c, user.Email)
	// 			c.Redirect(302, "/profile")
	// 			return
	// 		}
	// 	}
	// }

	// log.Debug("Not logged in")
	// c.Redirect(302, url)
}

func exchangeCode(c *gin.Context, code string) (token string, err error) {
	endpoint := fmt.Sprintf(
		"https://graph.facebook.com/oauth/access_token?client_id=%s&redirect_uri=%s&client_secret=%s&code=%s",
		appId, redirectUri(c), appSecret, code,
	)
	log.Debug(endpoint)
	client := &http.Client{Timeout: 55 * time.Second}
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		log.Panic(err)
	}
	res, err := client.Do(req)
	if err != nil {
		log.Panic(err)
	}
	defer res.Body.Close()
	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Panic(err)
	}
	body := string(b)

	values, err := url.ParseQuery(body)
	if err != nil {
		return token, err
	}

	token = values.Get("access_token")
	if token == "" {
		return token, errors.New(body)
	}
	return token, nil
}
