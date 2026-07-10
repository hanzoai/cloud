package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Waitlist connector. Name="waitlist", AuthType="none": it custodies nothing — it
// is the server-to-server bridge to the Hanzo Base waitlist plugin's
// POST /v1/waitlist/award seam. The deployment sources WAITLIST_URL and
// WAITLIST_AWARD_SECRET from KMS into the process env (the same WAITLIST_URL env
// convention clients/base's waitlist plugin reads). It fails closed when
// either is unset — a points award is never silently dropped, and the secret is
// never a client claim.
//
// award_points is the SHARED terminal step every verification flow ends with: a
// verifier (x.verify_follow, discord.verify_member, …) emits {verified, source,
// dedupKey}; a flow threads source+dedupKey into award_points, which credits the
// points idempotently (the Base plugin dedups per (entry, dedupKey), so a replay
// is a no-op).

const (
	waitlistURLEnv    = "WAITLIST_URL"
	waitlistSecretEnv = "WAITLIST_AWARD_SECRET"
)

var waitlistClient = &http.Client{Timeout: 15 * time.Second}

func init() {
	register(&Connector{
		Name:        "waitlist",
		DisplayName: "Hanzo Waitlist",
		AuthType:    "none",
		AuthReq:     false,
		Actions: map[string]*Action{
			"award_points": {
				Name:        "award_points",
				DisplayName: "Award Points",
				Description: "Credit waitlist points for a verified event (server-to-server, deduped per source).",
				Props: []PropSpec{
					{Name: "waitlist", Type: "string", Required: true, Description: "Waitlist slug."},
					{Name: "email", Type: "string", Description: "Entry email (email or refCode required)."},
					{Name: "refCode", Type: "string", Description: "Entry referral code (email or refCode required)."},
					{Name: "source", Type: "string", Required: true, Description: "Award source, e.g. social:x:follow."},
					{Name: "dedupKey", Type: "string", Description: "Idempotency key (defaults to source server-side)."},
					{Name: "points", Type: "number", Description: "Explicit amount — honored only for source=grant."},
				},
				Run: runWaitlistAward,
			},
		},
	})
}

func runWaitlistAward(ctx context.Context, rc RunContext) (any, error) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(waitlistURLEnv)), "/")
	secret := strings.TrimSpace(os.Getenv(waitlistSecretEnv))
	if base == "" || secret == "" {
		return nil, fmt.Errorf("waitlist award not configured: set %s and %s", waitlistURLEnv, waitlistSecretEnv)
	}
	waitlist := strInput(rc.Input, "waitlist")
	source := strInput(rc.Input, "source")
	if waitlist == "" || source == "" {
		return nil, fmt.Errorf("waitlist award_points: waitlist and source are required")
	}
	email := strInput(rc.Input, "email")
	refCode := strInput(rc.Input, "refCode")
	if email == "" && refCode == "" {
		return nil, fmt.Errorf("waitlist award_points: email or refCode is required")
	}

	payload := map[string]any{"waitlist": waitlist, "source": source}
	if email != "" {
		payload["email"] = email
	}
	if refCode != "" {
		payload["refCode"] = refCode
	}
	if dk := strInput(rc.Input, "dedupKey"); dk != "" {
		payload["dedupKey"] = dk
	}
	if pts := numInput(rc.Input, "points"); pts > 0 {
		payload["points"] = int(pts)
	}

	body, err := jsonBytes(payload)
	if err != nil {
		return nil, fmt.Errorf("waitlist award_points: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/waitlist/award", body)
	if err != nil {
		return nil, fmt.Errorf("waitlist award_points: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)

	resp, err := waitlistClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("waitlist award_points: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("waitlist award_points: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("waitlist award_points: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("waitlist award_points: decode: %w", err)
	}
	return out, nil
}
