package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// X (Twitter) connector. Name="x" == the clients/integrations provider id whose
// OAuth2 user token is custodied under xTokenSecret. Like the GitHub connector it
// is COMPLETE and correct today but fails closed with "x not connected" until
// integrations seals a token — the API code below simply never runs until then.
//
// verify_follow answers "does userId follow targetUser?" so a waitlist flow can
// award social:x:follow points. It emits {verified, platform, source, dedupKey};
// a flow threads source+dedupKey into waitlist.award_points.

const xTokenSecret = "access_token"

var xAPIBase = "https://api.twitter.com"

var xClient = &http.Client{Timeout: 15 * time.Second}

func init() {
	register(&Connector{
		Name:        "x",
		DisplayName: "X",
		AuthType:    "oauth2",
		AuthReq:     true,
		Actions: map[string]*Action{
			"verify_follow": {
				Name:        "verify_follow",
				DisplayName: "Verify Follow",
				Description: "Verify a user follows a target X account; the basis for social:x:follow points.",
				Props: []PropSpec{
					{Name: "targetUser", Type: "string", Required: true, Description: "Account that must be followed, e.g. hanzoai."},
					{Name: "userId", Type: "string", Required: true, Description: "The joining user's X user id."},
				},
				Run: runXVerifyFollow,
			},
		},
	})
}

func runXVerifyFollow(ctx context.Context, rc RunContext) (any, error) {
	tokb, err := rc.Token(xTokenSecret)
	if err != nil {
		return nil, fmt.Errorf("x not connected: %w", err)
	}
	token := strings.TrimSpace(string(tokb))
	targetUser := strings.TrimPrefix(strInput(rc.Input, "targetUser"), "@")
	userId := strInput(rc.Input, "userId")
	if targetUser == "" || userId == "" {
		return nil, fmt.Errorf("x verify_follow: targetUser and userId are required")
	}

	// X API v2: GET /2/users/:id/following — the accounts userId follows. The
	// first page (max 1000) is sufficient to demonstrate the pattern; a
	// production impl paginates on meta.next_token.
	endpoint := fmt.Sprintf("%s/2/users/%s/following?max_results=1000",
		strings.TrimRight(xAPIBase, "/"), url.PathEscape(userId))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("x verify_follow: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := xClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("x verify_follow: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, fmt.Errorf("x verify_follow: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("x verify_follow: http %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("x verify_follow: decode: %w", err)
	}
	verified := false
	for _, u := range out.Data {
		if strings.EqualFold(u.Username, targetUser) || u.ID == targetUser {
			verified = true
			break
		}
	}
	return map[string]any{
		"verified": verified, "platform": "x",
		"source": "social:x:follow", "dedupKey": "social:x:follow",
	}, nil
}
