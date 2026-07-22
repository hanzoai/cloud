package team

// The workspace invite plane — the write half of the Slack-model tenancy contract.
//
// When a workspace admin invites someone by email, TWO relations must record it:
//
//   - IAM's Membership(User × Org × Role) — the source of truth the `orgs` claim
//     is minted from, so the invitee's NEXT token (and a mid-session
//     get-memberships refresh) carries the workspace's org and they can act in it
//     with no re-provisioning. Written via POST /v1/iam/add-membership as the
//     confidential hanzo-team app (the app allow-listed under
//     IAM_MEMBERSHIP_ADMIN_APPS / CapMembershipAdmin — the SAME credential
//     establishSession's confidential code-exchange uses, so no new secret).
//   - team's own members row — the local roster the transactor projects and the
//     workspace switcher reads, keyed by the account uuid derived from the
//     invitee's IAM subject.
//
// One invite, two writes, both idempotent. The IAM write first (it is the tenancy
// grant); the local row second (the projection). A caller must be an owner/admin
// of the target workspace — enforced here before either write.
//
// Front wiring (honest state): the team SPA's plugins/login-resources speaks the
// Huly accounts protocol whose invite verb is `sendInvite`; this implements that
// backend RPC (account method "sendInvite") plus "getMemberships" for the
// mid-session refresh. If the shipped SPA build has no invite button wired to
// sendInvite yet, the RPC is still the one backend seam the front binds to when
// the button lands — no second path.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/zap-proto/zip"

	model "github.com/hanzoai/iam/pkg/model"
)

// iamMaxInviteBody bounds an IAM response read — get-user / add-membership /
// get-memberships all return small JSON envelopes, never blobs.
const iamMaxInviteBody = 1 << 20

// iamUser is the subset of an IAM user row the invite path reads: the natural key
// (`owner/name`) add-membership takes, the subject `id` the account uuid is
// derived from, and the display fields for the local roster row.
type iamUser struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

// idKey is the `<owner>/<name>` composite IAM parses (the add-membership `user`).
func (u iamUser) idKey() string { return u.Owner + "/" + u.Name }

// iamAPIBase is the canonical IAM REST base (${IAMEndpoint}/v1/iam) — the SAME
// oauthBase every other IAM hop in this package derives, so one place owns the
// prefix.
func (g *api) iamAPIBase() string { return oauthBase(g.cfg.iamEndpoint) }

// iamBasicAuth is the confidential hanzo-team client's client_secret_basic header.
// It authenticates the app so IAM resolves it to the app principal whose
// CapMembershipAdmin authorizes the membership write — the SAME (id, secret) pair
// establishSession's code exchange presents.
func (g *api) iamBasicAuth() string {
	return "Basic " + basicToken(g.cfg.iamClientID, g.cfg.iamClientSecret)
}

// iamDo performs one authenticated IAM request and decodes the /v1 envelope
// ({status,msg,data}). A non-ok status surfaces IAM's own message. The body is
// size-bounded and never logged (a user row carries email/salt). q is optional
// query params; body is an optional JSON payload.
func (g *api) iamDo(ctx context.Context, method, path string, q url.Values, body []byte) (json.RawMessage, error) {
	if g.cfg.iamClientID == "" || g.cfg.iamClientSecret == "" {
		return nil, fmt.Errorf("team: IAM app credentials not configured")
	}
	u := g.iamAPIBase() + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", g.iamBasicAuth())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iam unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, iamMaxInviteBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("iam denied (%d)", resp.StatusCode)
	}
	var env struct {
		Status string          `json:"status"`
		Msg    string          `json:"msg"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("iam non-envelope response (%d)", resp.StatusCode)
	}
	if env.Status != "ok" {
		msg := env.Msg
		if msg == "" {
			msg = fmt.Sprintf("iam status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("iam: %s", msg)
	}
	return env.Data, nil
}

// iamGetUserByEmail resolves the invitee's IAM identity by (org, email). The
// lookup is org-scoped — the Slack model invites someone INTO a workspace within
// your org, so the workspace's org is the lookup tenant. Returns a clean "not
// found" when IAM has no such user (so the invite refuses rather than writing a
// dangling row).
func (g *api) iamGetUserByEmail(ctx context.Context, org, email string) (iamUser, error) {
	data, err := g.iamDo(ctx, http.MethodGet, "/get-user", url.Values{"owner": {org}, "email": {email}}, nil)
	if err != nil {
		return iamUser{}, err
	}
	if len(data) == 0 || string(data) == "null" {
		return iamUser{}, fmt.Errorf("no IAM user for %q in org %q", email, org)
	}
	var u iamUser
	if err := json.Unmarshal(data, &u); err != nil {
		return iamUser{}, fmt.Errorf("iam get-user decode: %w", err)
	}
	if u.Name == "" || u.Owner == "" {
		return iamUser{}, fmt.Errorf("no IAM user for %q in org %q", email, org)
	}
	return u, nil
}

// iamAddMembership records that user (`<owner>/<name>`) may act in org with role —
// the tenancy grant the invitee's next `orgs` claim is minted from. Idempotent in
// IAM (EnsureMembership underneath).
func (g *api) iamAddMembership(ctx context.Context, user, org, role string) error {
	body, err := json.Marshal(map[string]string{"user": user, "org": org, "role": role})
	if err != nil {
		return err
	}
	_, err = g.iamDo(ctx, http.MethodPost, "/add-membership", nil, body)
	return err
}

// iamGetMemberships reads the LIVE org-membership set for a user (`<owner>/<name>`)
// straight from IAM — the mid-session refresh a client uses to pick up an org it
// was invited into without re-logging-in. Same shape as the `orgs` claim.
func (g *api) iamGetMemberships(ctx context.Context, user string) ([]model.OrgRef, error) {
	data, err := g.iamDo(ctx, http.MethodGet, "/get-memberships", url.Values{"user": {user}}, nil)
	if err != nil {
		return nil, err
	}
	var refs []model.OrgRef
	if err := json.Unmarshal(data, &refs); err != nil {
		return nil, fmt.Errorf("iam get-memberships decode: %w", err)
	}
	return refs, nil
}

// sendInvite is the account RPC "sendInvite" — a workspace admin invites an email
// into the workspace. It resolves the EXPLICIT target workspace (workspaceUrl)
// among the caller's orgs, requires the caller to be an owner/admin of it, then
// records the invite in BOTH relations (IAM membership + local roster row). The
// role defaults to member and must be one of the coarse tenancy roles.
func (g *api) sendInvite(c *zip.Ctx, params map[string]any) error {
	account, orgs, _, err := g.accountOrgs(c)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	email := strings.ToLower(strings.TrimSpace(str(params["email"])))
	if email == "" {
		return g.fail(c, statusBadRequest("email is required"))
	}
	role := strings.TrimSpace(str(params["role"]))
	if role == "" {
		role = "member"
	}
	if !validInviteRole(role) {
		return g.fail(c, statusBadRequest("role must be owner, admin, member or guest"))
	}
	wsURL := strings.TrimSpace(str(params["workspaceUrl"]))
	if wsURL == "" {
		return g.fail(c, statusBadRequest("workspaceUrl is required"))
	}
	ws, callerRole, err := g.resolveWorkspace(c.Context(), orgs, account, wsURL)
	if err != nil {
		if err == errAmbiguousWorkspace {
			return g.fail(c, statusAmbiguous(wsURL))
		}
		return g.fail(c, statusWorkspaceNotFound(wsURL))
	}
	if callerRole != "owner" && callerRole != "admin" {
		return g.fail(c, statusUnauthorized("only a workspace owner or admin can invite"))
	}
	org := ws.OwnerOrg

	// Resolve the invitee's IAM identity, then write the tenancy grant (IAM) and
	// the local roster row. IAM first: it is the source of truth the claim mints
	// from; the local row is the projection.
	u, err := g.iamGetUserByEmail(c.Context(), org, email)
	if err != nil {
		g.log.Warn("team: invite — invitee not resolvable in IAM", "org", org, "err", err)
		return g.fail(c, statusInvite("no Hanzo account for "+email+" in this org"))
	}
	// Least privilege: a WORKSPACE invite grants at most a coarse `member` at the
	// ORG level. The rich role (owner/admin) is workspace-scoped only (the local
	// roster row below); it must never escalate to an org-level IAM admin/owner
	// grant that other surfaces trust via IsAdmin. Org admin/owner is granted
	// through a deliberate org-admin path, not a per-workspace invite. Guest stays
	// guest (lesser privilege, and load-bearing for the team.guests cap).
	if err := g.iamAddMembership(c.Context(), u.idKey(), org, orgGrantRole(role)); err != nil {
		g.log.Error("team: invite — add-membership failed", "org", org, "err", err)
		return g.fail(c, statusInvite("could not record membership: "+err.Error()))
	}
	inviteeAccount := accountID(u.ID)
	if inviteeAccount == "" {
		return g.fail(c, statusInvite("invitee has no IAM subject"))
	}
	displayName := firstNonEmpty(u.DisplayName, u.Name, localPart(email))
	if err := g.accounts.AddMember(c.Context(), ws.ID, inviteeAccount, role, displayName); err != nil {
		g.log.Error("team: invite — local member row write failed", "err", err)
		return g.fail(c, statusInvite("could not add workspace member: "+err.Error()))
	}
	return g.ok(c, map[string]any{
		"invited":   email,
		"workspace": ws.UUID,
		"org":       org,
		"role":      role,
	})
}

// getMemberships is the account RPC "getMemberships" — the mid-session membership
// refresh. It reads the caller's LIVE org set from IAM (via the extra.user id the
// session token carries) so a user invited into a new org mid-session sees it
// without re-logging-in. On any IAM error, or a legacy token without extra.user,
// it falls back to the session's own signed orgs set — the refresh is best-effort
// and never strands the user.
func (g *api) getMemberships(c *zip.Ctx) error {
	t, _, err := sessionToken(c, g.cfg.serverSecret)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	session := orgsFromExtra(t.Extra)
	user, _ := t.Extra["user"].(string)
	if user == "" {
		return g.ok(c, session) // legacy token: no IAM id to refresh against
	}
	live, err := g.iamGetMemberships(c.Context(), user)
	if err != nil || len(live) == 0 {
		if err != nil {
			g.log.Warn("team: getMemberships — IAM refresh failed, serving session set", "err", err)
		}
		return g.ok(c, session)
	}
	return g.ok(c, live)
}

// validInviteRole reports whether role is one of the coarse tenancy roles the
// invite path accepts. Mirrors IAM's validMembershipRole plus team's guest role.
func validInviteRole(role string) bool {
	switch role {
	case "owner", "admin", "member", roleGuest:
		return true
	}
	return false
}

// orgGrantRole caps the ORG-level IAM membership a workspace invite may confer:
// owner/admin (workspace-scoped roles) collapse to a coarse `member` at the org
// level so a per-workspace invite can never mint an org IAM admin/owner. member
// and guest pass through unchanged (guest is lesser privilege and drives the
// team.guests cap). The rich role still lands on the local workspace roster row.
func orgGrantRole(role string) string {
	switch role {
	case "owner", "admin":
		return "member"
	default:
		return role
	}
}

// statusInvite is the platform Status for an invite that could not be recorded
// (invitee unknown, IAM write refused). Delivered in the RPC error envelope.
func statusInvite(msg string) Status {
	return Status{Severity: "ERROR", Code: "account:status:InviteFailed", Params: map[string]any{"message": msg}}
}

// basicToken encodes the client_secret_basic credential (id:secret, base64).
func basicToken(id, secret string) string {
	return base64.StdEncoding.EncodeToString([]byte(id + ":" + secret))
}
