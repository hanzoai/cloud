package auto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// Waitlist connector. Name="waitlist", AuthType="none": it custodies nothing — it
// is the server-to-server bridge to the Hanzo Base waitlist plugin's
// POST /v1/waitlist/award client. The deployment sources WAITLIST_URL and
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
//
// It moves a platform waitlist with the platform's secret, so it is a SuperAdmin's
// act and nobody else's: the call must carry the SuperAdmin bit the identity
// boundary mints (principal.SuperFrom), whether it arrives as a tool, over MCP or
// as a flow step. The amount is never the caller's: source "grant", the one source
// the Base plugin pays at a caller-stated amount, is refused here, and no points
// are forwarded. Every call is on the audit trail with its waitlist, source and
// target: a refusal as deny, and an award as its intent before the waitlist is
// asked (a trail that will not take it stops the award) and its outcome after.
// With no trail there is no award.

const (
	waitlistURLEnv    = "WAITLIST_URL"
	waitlistSecretEnv = "WAITLIST_AWARD_SECRET"
)

var waitlistClient = &http.Client{Timeout: 15 * time.Second}

func init() {
	provider.RegisterActions("waitlist", "Hanzo Waitlist", map[string]*provider.Action{
		"award_points": {
			Name:        "award_points",
			DisplayName: "Award Points",
			Description: "Credit waitlist points for a verified event (server-to-server, deduped per source).",
			Props: []provider.PropSpec{
				{Name: "waitlist", Type: "string", Required: true, Description: "Waitlist slug."},
				{Name: "email", Type: "string", Description: "Entry email (email or refCode required)."},
				{Name: "refCode", Type: "string", Description: "Entry referral code (email or refCode required)."},
				{Name: "source", Type: "string", Required: true, Description: "Award source, e.g. social:x:follow."},
				{Name: "dedupKey", Type: "string", Description: "Idempotency key (defaults to source server-side)."},
			},
			Run: runWaitlistAward,
		},
	}, nil)
}

func runWaitlistAward(ctx context.Context, rc provider.RunContext) (any, error) {
	base := strings.TrimRight(env.Or(waitlistURLEnv, ""), "/")
	secret := env.Or(waitlistSecretEnv, "")
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
	award := waitlistAward{Waitlist: waitlist, Source: source, DedupKey: strInput(rc.Input, "dedupKey"), Target: target(email, refCode)}
	if mounted == nil || mounted.State.audit == nil {
		return nil, errUnaudited
	}
	if !principal.SuperFrom(ctx) {
		return nil, refuse(ctx, rc.Org, award, "SuperAdmin required")
	}
	if strings.EqualFold(source, "grant") {
		return nil, refuse(ctx, rc.Org, award, "source grant pays a stated amount and is never a tool's")
	}
	// The intent is on the trail before the waitlist is asked, or it is not asked.
	if err := awarded(ctx, rc.Org, award, audit.Outcome{Result: "intent"}); err != nil {
		return nil, err
	}

	payload := map[string]any{"waitlist": waitlist, "source": source}
	if email != "" {
		payload["email"] = email
	}
	if refCode != "" {
		payload["refCode"] = refCode
	}
	if award.DedupKey != "" {
		payload["dedupKey"] = award.DedupKey
	}
	out, code, err := postAward(ctx, base, secret, payload)
	if err != nil {
		return nil, errors.Join(err, awarded(ctx, rc.Org, award, audit.Outcome{Result: "error", Status: code, Reason: "the waitlist did not award"}))
	}
	award.Points, _ = out["awarded"].(float64)
	if err := awarded(ctx, rc.Org, award, audit.Outcome{Result: "success", Status: code}); err != nil {
		return nil, err
	}
	return out, nil
}

// postAward asks the waitlist to award, answering its reply and status.
func postAward(ctx context.Context, base, secret string, payload map[string]any) (map[string]any, int, error) {
	body, err := jsonBytes(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("waitlist award_points: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/waitlist/award", body)
	if err != nil {
		return nil, 0, fmt.Errorf("waitlist award_points: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := waitlistClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("waitlist award_points: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("waitlist award_points: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, resp.StatusCode, fmt.Errorf("waitlist award_points: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("waitlist award_points: decode: %w", err)
	}
	return out, resp.StatusCode, nil
}

// refuse records a refused award and answers 403.
func refuse(ctx context.Context, org string, award waitlistAward, why string) error {
	refused := zip.ErrForbidden("waitlist award_points: " + why)
	if err := awarded(ctx, org, award, audit.Outcome{Result: "deny", Status: http.StatusForbidden, Reason: why}); err != nil {
		return errors.Join(refused, err)
	}
	return refused
}

// waitlistAward is what the trail records of one award: never an address in full.
type waitlistAward struct {
	Waitlist string  `json:"waitlist"`
	Source   string  `json:"source"`
	DedupKey string  `json:"dedupKey,omitempty"`
	Target   string  `json:"target"`
	Points   float64 `json:"points"`
}

// target names the entry an award is for, the email masked.
func target(email, refCode string) string {
	if refCode != "" {
		return "ref:" + refCode
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" {
		return "email:*"
	}
	return "email:" + local[:1] + "***@" + domain
}

// errUnaudited refuses an award on a process with no trail, before it is made.
var errUnaudited = zip.Errorf(http.StatusServiceUnavailable, "waitlist award_points: no audit trail on this process, and an award is never made unrecorded")

// awarded records one step of one award on the trail as a privileged action: its
// intent, its outcome, or its refusal. A record the trail cannot take is a 503.
func awarded(ctx context.Context, org string, award waitlistAward, outcome audit.Outcome) error {
	who := cloud.Who(ctx)
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: who.User, Email: who.Email},
		Action:    "waitlist.award",
		Resource:  audit.Resource{Type: "waitlist", ID: award.Waitlist},
		Auth:      audit.AuthContext{Method: "in-process", IsAdmin: principal.SuperFrom(ctx)},
		Outcome:   outcome,
		RequestID: who.RequestID,
	}
	if who.Owner != "" && who.Owner != org {
		rec.Actor.Home = who.Owner
	}
	body, err := json.Marshal(award)
	if err != nil {
		return err
	}
	rec.After = audit.Redact(body)
	if _, err := mounted.State.audit.Append(ctx, rec); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "waitlist award_points: audit: %v", err)
	}
	return nil
}
