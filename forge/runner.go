package forge

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// RunnerToken mints a registration token for one Actions runner. The forge
// hands the same kind of token to a person on its runner page; this is the
// machine asking for it, because the runner it registers is the one the cloud
// launches for a job the forge has just queued, and it lives exactly that long.
//
// Global, not scoped to an owner: the job's labels decide which runner takes
// it, and a runner registered under one namespace cannot take another's job
// even when the labels match. The token itself is single-use and short-lived.
func (c *Client) RunnerToken(ctx context.Context) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	if err := c.send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/admin/actions/runners/registration-token",
		out:    &out,
	}); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Token) == "" {
		return "", errors.New("forge: empty runner registration token")
	}
	return out.Token, nil
}
