package network

// invites_admin.go — THE OPERATOR'S INVITES. A SuperAdmin issues invite codes, reads
// them and revokes them, under /v1/admin/network/invites.
//
// SUPERADMIN, TWICE. The group refuses first on the routed path (cloud.Gate), and each
// op asks again with principal.SuperFrom, because MCP and the call plane invoke an
// op with no route in front of it. The refusal is audited, naming who asked.
//
// AUDITED FIRST. An issue or a revoke puts its intent on the audit trail BEFORE it
// writes anything, and its outcome after; a trail that cannot take the intent
// answers 503 and nothing is written (apps/benchmark claims.go does the same). The
// trail names how many were issued, the note and the ids, never a code.
//
// A CODE IS SHOWN ONCE. Issuing answers the codes in clear, and nothing keeps them:
// the store holds the HMAC, the list shows the id, and a lost code is revoked by id
// and issued again.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

const (
	// maxIssue is the most codes one issue makes, and defaultLife and maxLife bound
	// how long a code lives, in days.
	maxIssue    = 100
	defaultLife = 30
	maxLife     = 365
	// defaultList and maxList bound a page of invites.
	defaultList = 200
	maxList     = 1000
)

// The audited acts, one per op.
const (
	issueAction  = "network.invites.issue"
	listAction   = "network.invites.list"
	revokeAction = "network.invites.revoke"
)

// idShape is an invite's id: 15 lower-case hex characters.
var idShape = regexp.MustCompile(`^[0-9a-f]{15}$`)

// InviteIssueIn asks for invite codes.
type InviteIssueIn struct {
	// Count is how many codes to issue, 1 to 100. Absent or 0 means one.
	Count int `json:"count"`
	// Note says what the codes are for, such as "launch week". Optional; one line,
	// clipped at 200 characters, and kept with each code.
	Note string `json:"note"`
	// ExpiresDays is how many days the codes last, 1 to 365. Absent or 0 means 30.
	ExpiresDays int `json:"expires_days"`
}

// IssuedInvite is one code just issued.
type IssuedInvite struct {
	// ID is the invite's id, which the list shows and a revoke names. It is not the
	// code and does not reveal it.
	ID string `json:"id"`
	// Code is the invite code in clear, such as HNZ-7K9Q2-MXZ4P. This is the only time it
	// is shown: nothing keeps it.
	Code string `json:"code"`
	// ExpiresAt is when the code stops being valid.
	ExpiresAt time.Time `json:"expires_at"`
}

// InviteIssued is the codes just issued.
type InviteIssued struct {
	// Invites are the new codes, in clear, once.
	Invites []IssuedInvite `json:"invites"`
}

// InviteQuery pages the invite list.
type InviteQuery struct {
	// Limit is how many invites a page holds; 0 means 200 and nothing above 1000 is
	// honoured.
	Limit int `json:"limit"`
	// Cursor is the previous page's next; empty for the first page. It is opaque: a
	// value that was not given as a next is a 400.
	Cursor string `json:"cursor"`
}

// InviteView is one invite as the operator reads it. It never holds the code.
type InviteView struct {
	// ID is the invite's id, which a revoke names.
	ID string `json:"id"`
	// Status is active, revoked, or expired: an active invite past its expiry.
	Status string `json:"status"`
	// Note is what the invite was issued for.
	Note string `json:"note"`
	// IssuedAt is when it was issued.
	IssuedAt time.Time `json:"issued_at"`
	// ExpiresAt is when it stops being valid.
	ExpiresAt time.Time `json:"expires_at"`
	// CreatedBy is the SuperAdmin who issued it, by their IAM user.
	CreatedBy string `json:"created_by"`
}

// InviteList is a page of invites, newest first.
type InviteList struct {
	// Data is the page; an empty array when none has been issued.
	Data []InviteView `json:"data"`
	// Next continues Data: pass it back as cursor. Empty on the last page.
	Next string `json:"next,omitempty"`
}

// InviteRef addresses one invite.
type InviteRef struct {
	// ID is the invite id from the path, as issue and the list show it.
	ID string `json:"id"`
}

// superadmin admits a SuperAdmin and answers who they are. Anyone else, or no one,
// is refused 403 and the refusal is on the audit trail with what they asked.
func (o ops) superadmin(ctx context.Context, action string, detail any) (string, error) {
	who := cloud.Who(ctx)
	if principal.SuperFrom(ctx) {
		return strings.TrimSpace(who.User), nil
	}
	org, _ := principal.OrgFrom(ctx)
	_ = o.audit(ctx, action, "", org, strings.TrimSpace(who.User), false,
		audit.Outcome{Result: "deny", Status: http.StatusForbidden, Reason: "SuperAdmin required"}, detail)
	return "", cloud.Super.Refusal()
}

// audit appends one record: an act's intent before anything is written, its outcome
// after, or its refusal. The actor is the verified caller. A trail that is absent or
// cannot take it is 503, so the caller never goes on without one.
func (o ops) audit(ctx context.Context, action, id, org, by string, super bool, outcome audit.Outcome, after any) error {
	chain := o.s.State.trail
	if chain == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "network: the audit trail is unavailable, nothing was written")
	}
	who := cloud.Who(ctx)
	body, err := json.Marshal(after)
	if err != nil {
		return err
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: by, Email: who.Email},
		Action:    action,
		Resource:  audit.Resource{Type: action[:strings.LastIndex(action, ".")], ID: id},
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: super},
		Outcome:   outcome,
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		After:     audit.Redact(body),
	}
	if home := strings.TrimSpace(who.Owner); super && home != "" && home != org {
		rec.Actor.Home = home
	}
	if c, ok := cloud.Request(ctx); ok {
		rec.Method, rec.Path, rec.UserAgent = c.Method(), c.Path(), c.Header("User-Agent")
	}
	if _, err := chain.Append(ctx, rec); err != nil {
		o.s.Log.Error("network: audit failed", "action", action, "result", outcome.Result, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "network: the audit trail is unavailable, nothing was written")
	}
	return nil
}

// issueInvites issues invite codes and answers them in clear, once. It is a
// SuperAdmin's, and no one else may call it: another caller is refused 403 and the
// refusal is audited. Each code is HNZ- and ten characters of Crockford base32, shown
// as HNZ-XXXXX-XXXXX, from crypto/rand and unique among the codes issued; only an HMAC
// of it is stored, so a code
// shown here and lost is revoked by id and issued again. The audit trail takes the
// intent, with the count and the note and never a code, before anything is written,
// and answers 503 with nothing written when it cannot.
//
// A count is 1 to 100 and a life 1 to 365 days, and an absent one is one code for
// 30 days; anything else is 400.
//
// Example: {"count": 5, "note": "launch week", "expires_days": 14}
func (o ops) issueInvites(ctx context.Context, in *InviteIssueIn) (*InviteIssued, error) {
	by, err := o.superadmin(ctx, issueAction, in)
	if err != nil {
		return nil, err
	}
	count, days := in.Count, in.ExpiresDays
	if count == 0 {
		count = 1
	}
	if days == 0 {
		days = defaultLife
	}
	if count < 0 || count > maxIssue {
		return nil, zip.ErrBadRequest("count is 1 to 100")
	}
	if days < 0 || days > maxLife {
		return nil, zip.ErrBadRequest("expires_days is 1 to 365")
	}
	note := text.Trim(text.OneLine(in.Note), maxLine)
	org, _ := principal.OrgFrom(ctx)
	intent := struct {
		Count       int    `json:"count"`
		Note        string `json:"note"`
		ExpiresDays int    `json:"expires_days"`
	}{count, note, days}
	if err := o.audit(ctx, issueAction, "", org, by, true, audit.Outcome{Result: "intent"}, intent); err != nil {
		return nil, err
	}
	fail := func(status int, reason string, err error) error {
		_ = o.audit(ctx, issueAction, "", org, by, true, audit.Outcome{Result: "error", Status: status, Reason: reason}, nil)
		return err
	}

	home, key, err := o.door(ctx, true)
	if err == nil {
		err = o.hold(ctx, home)
	}
	if err != nil {
		return nil, fail(http.StatusServiceUnavailable, "the intake is not configured", err)
	}
	now := time.Now().UTC()
	rows, codes, err := o.mint(ctx, home, key, count, newCode, issuance{
		by: by, note: note, issued: now, expires: now.Add(time.Duration(days) * 24 * time.Hour),
	})
	if err != nil {
		return nil, fail(http.StatusServiceUnavailable, "the Base did not answer", err)
	}
	if err := writeInvites(ctx, home, rows); err != nil {
		return nil, fail(http.StatusServiceUnavailable, "the Base did not take the invites", o.unavailable("the Base did not take the invites", err))
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	// The invites are written and the intent covers them: a trail that cannot take the
	// outcome is logged by audit, and the codes, which exist nowhere else, are answered.
	_ = o.audit(ctx, issueAction, "", org, by, true, audit.Outcome{Result: "success", Status: http.StatusCreated}, map[string]any{"ids": ids})
	return &InviteIssued{Invites: codes}, nil
}

// issuance is what one issue stamps on every invite it makes.
type issuance struct {
	by, note        string
	issued, expires time.Time
}

// mint makes count invites under key, each from a code that draw answers, and the rows
// to store and the codes to show. A code whose id is already an invite's, or one this
// issue has made, is never written over: it is drawn again. The rows are not yet written.
func (o ops) mint(ctx context.Context, home string, key []byte, count int, draw func() (string, error), is issuance) ([]inviteRow, []IssuedInvite, error) {
	rows := make([]inviteRow, 0, count)
	out := make([]IssuedInvite, 0, count)
	taken := map[string]bool{}
	for tries := 0; len(rows) < count; tries++ {
		if tries > 20*count+20 {
			return nil, nil, zip.Errorf(http.StatusInternalServerError, "network: no fresh code could be drawn")
		}
		code, err := draw()
		if err != nil {
			return nil, nil, zip.Errorf(http.StatusInternalServerError, "network: no randomness")
		}
		sum := mac(key, code)
		id := inviteID(sum)
		if taken[id] {
			continue
		}
		held, err := readInvite(ctx, home, id)
		if err != nil {
			return nil, nil, o.unavailable("the Base did not answer an invite lookup", err)
		}
		if held != nil {
			continue
		}
		taken[id] = true
		rows = append(rows, inviteRow{
			ID: id, CodeHMAC: hex.EncodeToString(sum), Status: statusActive, Note: is.note, CreatedBy: is.by,
			IssuedAt: stamp(is.issued), ExpiresAt: stamp(is.expires),
		})
		out = append(out, IssuedInvite{ID: id, Code: code, ExpiresAt: is.expires})
	}
	return rows, out, nil
}

// listInvites lists the invites, newest first: each one's id, status, note, issue
// time, expiry and who issued it. It never returns a code, nor anything a code can
// be got from. It is a SuperAdmin's; another caller is refused 403, audited. A
// status is active, revoked, or expired for an active invite past its expiry.
//
// Example: {"limit": 50}
func (o ops) listInvites(ctx context.Context, in *InviteQuery) (*InviteList, error) {
	if _, err := o.superadmin(ctx, listAction, in); err != nil {
		return nil, err
	}
	home, err := o.home(ctx)
	if err != nil {
		return nil, err
	}
	limit, page := in.Limit, 1
	if limit <= 0 {
		limit = defaultList
	}
	if in.Cursor != "" {
		if page, err = pageOf(in.Cursor); err != nil {
			return nil, zip.ErrBadRequest("cursor is not one this list gave")
		}
	}
	rows, more, err := readInvites(ctx, home, min(limit, maxList), page)
	if err != nil {
		return nil, o.unavailable("the Base did not answer an invite list", err)
	}
	now := time.Now()
	out := &InviteList{Data: make([]InviteView, 0, len(rows))}
	for _, r := range rows {
		status := r.Status
		if status == statusActive && !now.Before(unstamp(r.ExpiresAt)) {
			status = "expired"
		}
		out.Data = append(out.Data, InviteView{
			ID: r.ID, Status: status, Note: r.Note, IssuedAt: unstamp(r.IssuedAt),
			ExpiresAt: unstamp(r.ExpiresAt), CreatedBy: r.CreatedBy,
		})
	}
	if more {
		out.Next = base64.RawURLEncoding.EncodeToString([]byte("page:" + strconv.Itoa(page+1)))
	}
	return out, nil
}

// pageOf is the page a cursor continues at.
func pageOf(cursor string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	n, ok := strings.CutPrefix(string(raw), "page:")
	if !ok {
		return 0, zip.ErrBadRequest("cursor")
	}
	page, err := strconv.Atoi(n)
	if err != nil || page < 2 || page > 1<<20 {
		return 0, zip.ErrBadRequest("cursor")
	}
	return page, nil
}

// revokeInvite revokes one invite and answers 204: its code stops verifying at once.
// Revoking one that is already revoked is the same 204, and an id that names no
// invite is 404. It is a SuperAdmin's; another caller is refused 403, audited. Every
// attempt is on the audit trail with its outcome, and the trail takes the intent before
// an invite is changed and answers 503 with nothing changed when it cannot.
//
// Example: {"id": "3fa85f6457174a2"}
func (o ops) revokeInvite(ctx context.Context, in *InviteRef) (*cloud.Unit, error) {
	by, err := o.superadmin(ctx, revokeAction, in)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	org, _ := principal.OrgFrom(ctx)
	// Every attempt leaves a record, whatever became of it: a revoke that finds nothing or
	// finds it done changes nothing, so its one record is its outcome.
	done := func(result string, status int, why string, err error) error {
		_ = o.audit(ctx, revokeAction, id, org, by, true, audit.Outcome{Result: result, Status: status, Reason: why}, in)
		return err
	}
	if !idShape.MatchString(id) {
		return nil, done("error", http.StatusNotFound, "not an invite id", zip.ErrNotFound("invite not found"))
	}
	home, err := o.home(ctx)
	if err != nil {
		return nil, err
	}
	row, err := readInvite(ctx, home, id)
	if err != nil {
		return nil, o.unavailable("the Base did not answer an invite lookup", err)
	}
	if row == nil {
		return nil, done("error", http.StatusNotFound, "no such invite", zip.ErrNotFound("invite not found"))
	}
	if row.Status == statusRevoked {
		return nil, done("success", http.StatusNoContent, "already revoked", nil)
	}
	if err := o.audit(ctx, revokeAction, id, org, by, true, audit.Outcome{Result: "intent"}, in); err != nil {
		return nil, err
	}
	row.Status, row.RevokedAt, row.RevokedBy = statusRevoked, stamp(time.Now()), by
	if err := writeInvites(ctx, home, []inviteRow{*row}); err != nil {
		_ = o.audit(ctx, revokeAction, id, org, by, true, audit.Outcome{Result: "error", Status: http.StatusServiceUnavailable, Reason: "the Base did not take the change"}, nil)
		return nil, o.unavailable("the Base did not take a revoke", err)
	}
	_ = o.audit(ctx, revokeAction, id, org, by, true, audit.Outcome{Result: "success", Status: http.StatusNoContent}, nil)
	return nil, nil
}
