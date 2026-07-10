package netlify

import (
	"context"
	"net/http"
	"time"
)

type OauthTransport struct {
	http.RoundTripper
	AccessToken string
}

func (t *OauthTransport) RoundTrip(req *http.Request) (res *http.Response, err error) {
	req.Header.Set("Authorization", "Bearer "+t.AccessToken)
	return t.RoundTripper.RoundTrip(req)
}

func newOauthClient(ctx context.Context, accessToken string) *http.Client {
	// Update timeout
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, time.Second*30)
	defer cancel()

	client := &http.Client{Timeout: 55 * time.Second}
	client.Transport = &OauthTransport{
		AccessToken:  accessToken,
		RoundTripper: http.DefaultTransport,
	}
	return client
}
