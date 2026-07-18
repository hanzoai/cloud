package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// teams_events.go is ADAPTER #3 of the ChatBridge core: the Microsoft Teams / Bot
// Framework messaging webhook. Its three edges —
//
//	inbound-auth : verifyBotFrameworkJWT (RS256 JWT against Bot Framework's JWKS,
//	               audience = MICROSOFT_APP_ID, serviceurl-bound) — NOT HMAC
//	parse        : parseTeamsActivity (pure)
//	reply        : teamsSendActivity (POST the Bot Connector at the activity's serviceUrl,
//	               authed with an AAD client-credentials app token)
//
// — delegating dedupe/org-resolve/pool/brain/link to the core. ISOLATION ROOT: the
// org comes ONLY from OrgForExternalID("teams", channelData.tenant.id), and the
// tenant id is trustworthy only because the activity's JWT is verified first.
//
// MOUNT HANDOFF (integrations owner adds these before the /:provider wildcards):
//
//	app.Post("/v1/integrations/teams/events",         s.teamsEvents)
//	app.Get("/v1/integrations/teams/link",            s.teamsLink)
//	app.Get("/v1/integrations/teams/link/aad",        s.teamsLinkAAD)
//	app.Get("/v1/integrations/teams/link/callback",   s.teamsLinkCallback)

// teamsEvents is the Bot Framework messaging webhook. It parses the activity (to
// obtain serviceUrl), verifies the Bot Framework JWT (audience-bound + serviceurl-
// bound), and routes a message to an on-behalf-of agent run — acking 200 fast and
// replying proactively via the Bot Connector async, deduped durably on activity id.
func teamsEvents(s *cloud.Service[state], c *zip.Ctx) error {
	bridgeReady()
	if !teamsConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "teams events not configured")
	}
	act, ok := parseTeamsActivity(slackReadBody(c))
	if !ok {
		return c.NoContent(http.StatusOK) // unparseable / non-activity — nothing to do
	}
	// INBOUND AUTH: the RS256 Bot Framework JWT, bound to our app id AND to this
	// activity's serviceUrl (so a valid token can't redirect our reply elsewhere).
	if !verifyBotFrameworkJWT(c.Context(), c.Header("Authorization"), teamsAppID(), act.ServiceURL, 0) {
		return zip.ErrUnauthorized("bad bot framework token")
	}
	if act.Type != "message" || strings.TrimSpace(act.Text) == "" {
		return c.NoContent(http.StatusOK)
	}
	tenant := act.TenantID
	if tenant == "" {
		return c.NoContent(http.StatusOK)
	}
	// ISOLATION ROOT, resolved SYNC (so the per-org limiter keys on the real tenant
	// and a shed happens BEFORE anything is recorded): org comes ONLY from the
	// tenant→org bind for the JWT-verified tenant id.
	org, ok := OrgForExternalID("teams", tenant)
	if !ok {
		s.Log.Warn("teams: activity for unconnected tenant", "tenant", tenant)
		return c.NoContent(http.StatusOK)
	}
	// SHED BEFORE the dedupe write (Red M-1): acquire a pool slot first; if the pool
	// is full, record NOTHING and return a retriable NON-2xx so the Bot Connector
	// re-delivers when a slot frees (no lost message, no double-run).
	if !bridgeLim.acquire(org) {
		s.Log.Warn("teams: at capacity, shedding for retry", "org", org)
		return zip.Errorf(http.StatusTooManyRequests, "teams agent pool at capacity")
	}
	// Slot held. DURABLE dedupe (billed path): release on every path that does NOT
	// dispatch. Fail CLOSED on a dedupe error.
	fresh, err := s.State.store.MarkEvent(c.Context(), "teams", act.ID)
	if err != nil {
		bridgeLim.release(org)
		s.Log.Warn("teams: dedupe error, skipping", "err", err)
		return c.NoContent(http.StatusOK)
	}
	if !fresh {
		bridgeLim.release(org)
		return c.NoContent(http.StatusOK)
	}
	if _, gerr := s.State.store.GCEvents(c.Context(), staleEventCutoff()); gerr != nil {
		s.Log.Warn("teams: dedupe gc", "err", gerr)
	}
	user := act.UserAADObjectID
	if user == "" {
		user = act.UserID
	}
	in := Inbound{
		Provider: "teams", ExternalID: tenant, User: user,
		Channel: act.ConversationID, Text: stripTeamsMentions(act.Text), DedupeKey: act.ID,
	}
	emitIngress(org, in, act.ServiceURL)
	reply := teamsReplier(act.ServiceURL, act.ConversationID)
	bridgeSpawn(s, org, func() { runBridgeTurn(s, org, in, reply) })
	return c.NoContent(http.StatusOK)
}

// teamsReplier builds the reply closure: POST a message activity to the Bot
// Connector at the (JWT-verified) serviceUrl, authed with an AAD app token. Teams
// has no per-user ephemeral in a channel; a link prompt is safe because when Teams
// linking is unconfigured the prompt carries no URL (bridgeReply), and when it is
// configured the link re-verifies the AAD user.
func teamsReplier(serviceURL, conversationID string) replyFunc {
	return func(ctx context.Context, text string, _ephemeral bool) error {
		return teamsSendActivity(ctx, serviceURL, conversationID, text)
	}
}

// ── Bot Connector reply (serviceUrl + AAD app token) ────────────────────────

var (
	teamsTokMu  sync.Mutex
	teamsTok    string
	teamsTokExp time.Time
)

// teamsReplyToken returns a cached AAD app token for the Bot Connector
// (client-credentials, scope api.botframework.com/.default). Cached until ~1 min
// before expiry.
func teamsReplyToken(ctx context.Context) (string, error) {
	teamsTokMu.Lock()
	defer teamsTokMu.Unlock()
	if teamsTok != "" && time.Now().Before(teamsTokExp.Add(-time.Minute)) {
		return teamsTok, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {teamsAppID()},
		"client_secret": {teamsAppPassword()},
		"scope":         {"https://api.botframework.com/.default"},
	}
	endpoint := teamsAADBase + "/botframework.com/oauth2/v2.0/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, bridgeMaxBody))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("teams reply token http %d", resp.StatusCode)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return "", fmt.Errorf("teams reply token decode: %w", err)
	}
	if t.Error != "" || t.AccessToken == "" {
		return "", fmt.Errorf("teams reply token: %s", nonEmpty(t.Error, "no access_token"))
	}
	teamsTok = t.AccessToken
	teamsTokExp = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return teamsTok, nil
}

// teamsSendActivity posts a message activity to the Bot Connector at serviceUrl. The
// app token is never logged.
func teamsSendActivity(ctx context.Context, serviceURL, conversationID, text string) error {
	tok, err := teamsReplyToken(ctx)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(serviceURL, "/") + "/v3/conversations/" + conversationID + "/activities"
	payload, _ := json.Marshal(map[string]string{"type": "message", "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("teams send activity http %d", resp.StatusCode)
	}
	return nil
}

// ── parse (pure — Teams' parse edge) ────────────────────────────────────────

// teamsActivity is the normalized subset of a Bot Framework activity we act on.
type teamsActivity struct {
	Type            string
	ID              string
	Text            string
	ServiceURL      string
	UserID          string
	UserAADObjectID string
	ConversationID  string
	TenantID        string
}

type teamsRawActivity struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Text       string `json:"text"`
	ServiceURL string `json:"serviceUrl"`
	From       struct {
		ID          string `json:"id"`
		AadObjectID string `json:"aadObjectId"`
	} `json:"from"`
	Conversation struct {
		ID string `json:"id"`
	} `json:"conversation"`
	ChannelData struct {
		Tenant struct {
			ID string `json:"id"`
		} `json:"tenant"`
	} `json:"channelData"`
}

// parseTeamsActivity extracts a teamsActivity. ok=false only for a structurally
// invalid payload (no serviceUrl to reply to / no id).
func parseTeamsActivity(raw []byte) (teamsActivity, bool) {
	var r teamsRawActivity
	if err := json.Unmarshal(raw, &r); err != nil || r.ServiceURL == "" || r.ID == "" {
		return teamsActivity{}, false
	}
	return teamsActivity{
		Type: r.Type, ID: r.ID, Text: r.Text, ServiceURL: r.ServiceURL,
		UserID: r.From.ID, UserAADObjectID: r.From.AadObjectID,
		ConversationID: r.Conversation.ID, TenantID: r.ChannelData.Tenant.ID,
	}, true
}

// stripTeamsMentions removes Teams <at>…</at> mention spans (the @Hanzo mention),
// leaving the user's actual prompt. Teams delivers mentions as HTML-ish tags in the
// text plus a parallel entities[] array; removing the spans is sufficient and pure.
func stripTeamsMentions(text string) string {
	for {
		start := strings.Index(text, "<at")
		if start < 0 {
			break
		}
		end := strings.Index(text[start:], "</at>")
		if end < 0 {
			// Unclosed tag — drop from the tag start to end of string, then stop.
			text = text[:start]
			break
		}
		text = text[:start] + text[start+end+len("</at>"):]
	}
	return strings.TrimSpace(text)
}
