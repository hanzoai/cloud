package netlify

import (
	"context"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

// This is really a token response, but for our purposes acts as a copy of
// netlify's representation of our user.
type User struct {
	Email       string    `json:"email"`             // Our users's email (typically fake email)
	Id          string    `json:"id,omitempy"`       // Netlify's id for access token
	Uid         string    `json:"uid,omitempty"`     // Netlify's copy of our supplied id (typically organization name)
	UserId      string    `json:"user_id,omitempty"` // Netlify's user_id for our org
	CreatedAt   time.Time `json:"created_at,omitempy"`
	AccessToken string    `json:"access_token,omitempy"`
}

type TokenReq struct {
	User User `json:"user"`
}

func (c *Client) AccessToken(userId, email string) (User, error) {
	buf := json.EncodeBuffer(TokenReq{User: User{Uid: userId, Email: email}})
	url := config.Netlify.BaseUrl + "/access_tokens?access_token=" + config.Netlify.AccessToken
	req, err := http.NewRequest("POST", url, buf)
	req.Header.Set("Content-Type", "application/json")

	if err != nil {
		log.Error("Error upon creating new request %v", err, c.ctx)
		return User{}, err
	}

	log.Debug("Requesting new access token for %s (%s)", userId, email, c.ctx)
	client := &http.Client{Timeout: 55 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return User{}, err
	}
	defer res.Body.Close()

	// Read response body
	b, _ := ioutil.ReadAll(res.Body)
	log.Debug("Response %v from Netlify: %v", res.StatusCode, string(b), c.ctx)

	// Decode JSON
	user := User{}
	if err := json.DecodeBytes(b, &user); err != nil {
		log.Error("Request came back with error %v", err, c.ctx)
		return user, err
	}

	return user, nil
}

// Get access token from cache (placeholder - memcache removed)
func getAccessToken(ctx context.Context, orgName string) string {
	// Note: memcache dependency removed - implement alternative caching if needed
	return ""
}

// Create new access token
func createAccessToken(ctx context.Context, orgName string) string {
	client := New(ctx, config.Netlify.AccessToken)
	user, err := client.AccessToken(orgName, orgName+"@hanzo.ai")

	if err != nil {
		log.Error("Unable to get Netlify Access Token: %v", err, ctx)
		return ""
	}

	return user.AccessToken
}

// Cache access token (placeholder - memcache removed)
func setAccessToken(ctx context.Context, accessToken string) {
	// Note: memcache dependency removed - implement alternative caching if needed
	log.Debug("Would cache access token: %v", accessToken, ctx)
}

// Get a client for netlify
func NewFromNamespace(ctx context.Context, orgName string) *Client {
	// Get user-level token for organization
	accessToken := GetAccessToken(ctx, orgName)

	return New(ctx, accessToken)
}

// Get access token
func GetAccessToken(ctx context.Context, orgName string) string {
	accessToken := getAccessToken(ctx, orgName)
	if accessToken == "" {
		accessToken = createAccessToken(ctx, orgName)
		setAccessToken(ctx, accessToken)
	}

	return accessToken
}
