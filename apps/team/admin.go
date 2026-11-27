package team

// The cross-org directory of team spaces, for the platform operator. The address
// is /v1/admin/<app>, the one place HIP-0139 lets an app answer outside /v1/<app>.
//
//	GET /v1/admin/team                every org's spaces, keyset-paged
//	GET /v1/admin/team/:org/:space    one space and the people in it, with roles
//
// The spaces table is this app's, so the directory is read here rather than
// assembled by apps/admin over the plane; who is in a space and as what is IAM's,
// read per space through the same roster call the /v1/team/members op makes, as
// the operator pointed at that tenant (cloud.As).
//
// SuperAdmin only, through core.Admit — the one predicate (owner == "admin") every
// /v1/admin read asks. The boundary audits every request under /v1/admin/ and every
// request a SuperAdmin makes, so each read here is on the trail without a second
// writer.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/orm/query"
)

// adminTeamsPrefix is the directory's address. A constant because cmd/zipdoc
// resolves a typed op's path from the constant value of its Group argument.
const adminTeamsPrefix = "/v1/admin/team"

// Page bounds for the directory. Each row costs one roster read, so the page size
// is also the fan-out width.
const (
	teamsDefaultLimit = 50
	teamsMaxLimit     = 100
	rosterFanout      = 8
)

// teamSorts maps each sort the directory accepts to the column it orders by.
// Anything else is refused rather than silently ignored.
var teamSorts = map[string]string{
	"name":    "name",
	"org":     "owner_org",
	"created": "created_at",
}

// adminTeams serves the directory. The store is the only state it needs.
type adminTeams struct {
	accounts *accountStore
}

// registerAdmin declares the directory. The group is built in this file so
// cmd/zipdoc resolves the prefix from here.
func registerAdmin(app cloud.Router, accounts *accountStore) {
	a := &adminTeams{accounts: accounts}
	g := app.Group(adminTeamsPrefix)
	g.Get("", a.list, zip.WithOperationID("adminTeams"))
	g.Get("/:org/:space", a.one, zip.WithOperationID("adminTeam"))
}

// teamsIn is the directory query.
type teamsIn struct {
	// Limit is rows per page, 1 to 100. Defaults to 50.
	Limit string `json:"limit"`
	// Cursor continues a listing: pass back the previous page's `next`. Omit it
	// for the first page.
	Cursor string `json:"cursor"`
	// Q is a case-insensitive substring match over the space's name, slug, org and
	// id.
	Q string `json:"q"`
	// Sort is `name`, `org` or `created`, with `:desc` for descending. Defaults to
	// `name`.
	Sort string `json:"sort"`
	// Org narrows the directory to one tenant.
	Org string `json:"org"`
	// User narrows it to the spaces one person is in: their IAM user id. Requires
	// Org, the tenant that person belongs to.
	User string `json:"user"`
}

// teamRow is one space in the directory.
type teamRow struct {
	// Org is the tenant that owns the space.
	Org string `json:"org"`
	// ID is the space's stable id, the one every channel and document in it is filed
	// under.
	ID string `json:"id"`
	// Name is the space's display name.
	Name string `json:"name"`
	// Slug is the space's URL handle, unique within its org.
	Slug string `json:"slug"`
	// Owner is the account that created the space.
	Owner string `json:"owner"`
	// OwnerName is the name IAM holds for that account, when they are still a
	// member.
	OwnerName string `json:"ownerName,omitempty"`
	// Members is how many people hold a role in the space. Absent when IAM did not
	// answer for this row, which `degraded` then names; never a zero standing in
	// for an unknown.
	Members *int `json:"members,omitempty"`
	// Created is when the space was created (RFC 3339).
	Created string `json:"created"`
}

// teamsOut is the directory envelope.
type teamsOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is one page of spaces. Null when the read failed.
	Data []teamRow `json:"data"`
	// Total counts every space matching the filters, across all pages. Absent when
	// the read failed.
	Total *int `json:"total,omitempty"`
	// Next is the cursor for the following page, empty on the last one.
	Next string `json:"next"`
	// Degraded names a source that did not answer — "iam" — so a missing member
	// count reads as an outage rather than as an empty space.
	Degraded []string `json:"degraded,omitempty"`
}

// teamIn names one space.
type teamIn struct {
	// Org is the tenant that owns the space, from the path.
	Org string `json:"org"`
	// Space is the space id, from the path.
	Space string `json:"space"`
}

// teamMemberRow is one person in a space.
type teamMemberRow struct {
	// ID is the member's team account id.
	ID string `json:"id"`
	// Name is the name IAM holds for them.
	Name string `json:"name"`
	// Role is owner, admin, member or guest.
	Role string `json:"role"`
}

// teamDetail is one space and its people.
type teamDetail struct {
	// Team is the space, as the directory lists it.
	Team teamRow `json:"team"`
	// Roster is everyone who holds a role in the space, owners first.
	Roster []teamMemberRow `json:"roster"`
}

// teamOut is the one-space envelope.
type teamOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the space and its roster. Null when the read failed.
	Data *teamDetail `json:"data"`
}

// ListTeams returns every org's team spaces, one page at a time: the org, the
// space's id and name, who created it, how many people are in it and when it was
// created.
//
// Pages by keyset: pass `next` back as `cursor`, so a deep page costs what the
// first one does. `q` matches name, slug, org and id; `org` narrows to one tenant;
// `org` with `user` (an IAM user id) lists the spaces that person is in. Member
// counts come from IAM per row, and a row IAM did not answer for carries no count
// while `degraded` names the gap. SuperAdmin only.
//
// Example: {"status": "ok", "msg": "", "data": [{"org": "acme", "id": "5b1…", "name": "Acme", "slug": "acme", "owner": "9f2…", "ownerName": "Ada", "members": 4, "created": "2026-09-01T12:00:00Z"}], "total": 1, "next": ""}
func (a *adminTeams) list(ctx context.Context, in *teamsIn) (*teamsOut, error) {
	c, err := admitSuper(ctx)
	if err != nil {
		return nil, err
	}
	pg, err := parsePage(in)
	if err != nil {
		return nil, err
	}
	org := strings.TrimSpace(in.Org)
	user := strings.TrimSpace(in.User)
	if user != "" && org == "" {
		return nil, zip.ErrBadRequest("user needs org: a person's spaces are read inside the tenant they belong to")
	}
	var only []any
	if user != "" {
		got, err := iam.IAMMembers(cloud.As(c, org), &client.Scope{User: accountID(user), Any: true})
		if err != nil {
			return &teamsOut{Status: adminErr, Msg: "iam: " + err.Error()}, nil
		}
		for _, m := range got.Memberships {
			if m.Space != "" {
				only = append(only, m.Space)
			}
		}
		if len(only) == 0 {
			return &teamsOut{Status: adminOK, Data: []teamRow{}, Total: adminTotal(0)}, nil
		}
	}
	rows, total, err := a.accounts.directory(ctx, directoryQuery{
		page: pg, q: strings.TrimSpace(in.Q), org: org, spaces: only,
	})
	if err != nil {
		return &teamsOut{Status: adminErr, Msg: err.Error()}, nil
	}
	out := &teamsOut{Status: adminOK, Data: make([]teamRow, len(rows)), Total: adminTotal(total)}
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		failed bool
		slots  = make(chan struct{}, rosterFanout)
	)
	for i, w := range rows {
		out.Data[i] = rowOf(w)
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, w space) {
			defer wg.Done()
			defer func() { <-slots }()
			roster, err := a.accounts.rosterOf(cloud.As(c, w.OwnerOrg), w.OwnerOrg, w.UUID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = true
				return
			}
			n := len(roster)
			out.Data[i].Members = &n
			out.Data[i].OwnerName = nameIn(roster, w.Owner)
		}(i, w)
	}
	wg.Wait()
	if failed {
		out.Degraded = []string{"iam"}
	}
	if len(rows) == pg.limit {
		out.Next = pg.after(rows[len(rows)-1])
	}
	return out, nil
}

// GetTeam returns one space with everyone who holds a role in it: their account
// id, the name IAM holds for them, and the role — owners first, then admins,
// members and guests. A space the org does not own is 404. SuperAdmin only.
//
// Example: {"status": "ok", "msg": "", "data": {"team": {"org": "acme", "id": "5b1…", "name": "Acme", "slug": "acme", "owner": "9f2…", "ownerName": "Ada", "members": 1, "created": "2026-09-01T12:00:00Z"}, "roster": [{"id": "9f2…", "name": "Ada", "role": "owner"}]}}
func (a *adminTeams) one(ctx context.Context, in *teamIn) (*teamOut, error) {
	c, err := admitSuper(ctx)
	if err != nil {
		return nil, err
	}
	org, id := strings.TrimSpace(in.Org), strings.TrimSpace(in.Space)
	if org == "" || id == "" {
		return nil, zip.ErrBadRequest("org and space are required")
	}
	w, err := a.accounts.SpaceByUUID(ctx, org, id)
	if err != nil {
		if errors.Is(err, errNoSpace) {
			return nil, zip.ErrNotFound("no such space in " + org)
		}
		return &teamOut{Status: adminErr, Msg: err.Error()}, nil
	}
	roster, err := a.accounts.rosterOf(cloud.As(c, org), org, w.UUID)
	if err != nil {
		return &teamOut{Status: adminErr, Msg: "iam: " + err.Error()}, nil
	}
	d := &teamDetail{Team: rowOf(w), Roster: make([]teamMemberRow, 0, len(roster))}
	n := len(roster)
	d.Team.Members = &n
	d.Team.OwnerName = nameIn(roster, w.Owner)
	for _, m := range roster {
		d.Roster = append(d.Roster, teamMemberRow{ID: m.User, Name: m.Name, Role: m.Role})
	}
	sortRoster(d.Roster)
	return &teamOut{Status: adminOK, Data: d}, nil
}

// rowOf projects a stored space onto the wire row, without the IAM half.
func rowOf(w space) teamRow {
	return teamRow{
		Org: w.OwnerOrg, ID: w.UUID, Name: w.Name, Slug: w.Slug, Owner: w.Owner,
		Created: time.UnixMilli(w.CreatedAt).UTC().Format(time.RFC3339),
	}
}

// nameIn is the name the roster holds for one account, empty when they are not in
// it.
func nameIn(roster []client.Membership, account string) string {
	for _, m := range roster {
		if m.User == account {
			return m.Name
		}
	}
	return ""
}

// roleRank orders a roster: owners, admins, members, guests, then anything else.
var roleRank = map[string]int{"owner": 0, "admin": 1, "member": 2, "guest": 3}

func sortRoster(r []teamMemberRow) {
	rank := func(role string) int {
		if n, ok := roleRank[role]; ok {
			return n
		}
		return len(roleRank)
	}
	sort.SliceStable(r, func(i, j int) bool {
		if ri, rj := rank(r[i].Role), rank(r[j].Role); ri != rj {
			return ri < rj
		}
		return r[i].Name < r[j].Name
	})
}

// teamsPage is a parsed page request: size, order and where the last page ended.
type teamsPage struct {
	limit  int
	sort   string // key of teamSorts
	desc   bool
	cursor *teamsCursor
}

// teamsCursor is the last row of the previous page: its sort value and id.
type teamsCursor struct {
	Sort  string `json:"s"`
	Value string `json:"v"`
	ID    string `json:"i"`
}

// parsePage parses and bounds the page query. A value outside the contract is a
// 400, never a silent default the caller did not ask for.
func parsePage(in *teamsIn) (teamsPage, error) {
	pg := teamsPage{limit: teamsDefaultLimit, sort: "name"}
	if s := strings.TrimSpace(in.Limit); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > teamsMaxLimit {
			return pg, zip.ErrBadRequest(fmt.Sprintf("limit must be 1 to %d", teamsMaxLimit))
		}
		pg.limit = n
	}
	if s := strings.TrimSpace(in.Sort); s != "" {
		field, dir, _ := strings.Cut(s, ":")
		if _, ok := teamSorts[field]; !ok || (dir != "" && dir != "desc") {
			return pg, zip.ErrBadRequest("sort must be name, org or created, optionally with :desc")
		}
		pg.sort, pg.desc = field, dir == "desc"
	}
	if s := strings.TrimSpace(in.Cursor); s != "" {
		raw, err := base64.RawURLEncoding.DecodeString(s)
		var c teamsCursor
		if err != nil || json.Unmarshal(raw, &c) != nil || c.ID == "" {
			return pg, zip.ErrBadRequest("cursor is not one this listing issued")
		}
		if c.Sort != pg.sort {
			return pg, zip.ErrBadRequest("cursor belongs to a different sort; start from the first page")
		}
		pg.cursor = &c
	}
	return pg, nil
}

// after is the cursor that continues past w.
func (p teamsPage) after(w space) string {
	v := map[string]string{"name": w.Name, "org": w.OwnerOrg, "created": strconv.FormatInt(w.CreatedAt, 10)}[p.sort]
	raw, _ := json.Marshal(teamsCursor{Sort: p.sort, Value: v, ID: w.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// likeTerm makes a search term literal: the LIKE wildcards, and the escape character
// itself, stop being operators.
var likeTerm = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)

// directoryQuery is one directory read: the page plus the filters.
type directoryQuery struct {
	page   teamsPage
	q      string
	org    string
	spaces []any // space uuids; nil means every space
}

// directory is the cross-org space listing — the one read in this store that is
// not scoped to a caller's org, and the reason it is reachable only through the
// SuperAdmin gate above. Keyset-paged on (sort column, id), so page N costs what
// page 1 does, and counted with the same filters.
func (s *accountStore) directory(ctx context.Context, dq directoryQuery) ([]space, int, error) {
	var where []query.Expression
	if dq.org != "" {
		where = append(where, query.HashExp{"owner_org": dq.org})
	}
	if dq.spaces != nil {
		where = append(where, query.In("uuid", dq.spaces...))
	}
	if dq.q != "" {
		if err := dialect.Text(dq.q); err != nil {
			return nil, 0, zip.ErrBadRequest("q: " + err.Error())
		}
		// A substring of any of four columns, ignoring case. LIKE folds case on SQLite
		// and not on PostgreSQL, so both sides are lowered; the term is escaped so a
		// % or _ in it is itself.
		like := "%" + likeTerm.Replace(dq.q) + "%"
		where = append(where, query.NewExp(
			`(lower(name) LIKE lower({:q}) ESCAPE '\' OR lower(slug) LIKE lower({:q}) ESCAPE '\'`+
				` OR lower(owner_org) LIKE lower({:q}) ESCAPE '\' OR lower(uuid) LIKE lower({:q}) ESCAPE '\')`,
			query.Params{"q": like}))
	}
	filter := query.And(where...)

	total, err := s.spacesIn("spaces").Where(filter).Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("directory count: %w", err)
	}

	col := teamSorts[dq.page.sort]
	dir, cmp := "ASC", ">"
	if dq.page.desc {
		dir, cmp = "DESC", "<"
	}
	read := s.spacesIn("spaces").Where(filter)
	if c := dq.page.cursor; c != nil {
		var v any = c.Value
		if col == "created_at" {
			n, err := strconv.ParseInt(c.Value, 10, 64)
			if err != nil {
				return nil, 0, zip.ErrBadRequest("cursor is not one this listing issued")
			}
			v = n
		}
		// col is a teamSorts value, never caller text; the values are bound.
		read = read.AndWhere(query.NewExp(
			fmt.Sprintf("(%[1]s %[2]s {:v} OR (%[1]s = {:v} AND id %[2]s {:id}))", col, cmp),
			query.Params{"v": v, "id": c.ID}))
	}
	rows, err := read.OrderBy(col+" "+dir, "id "+dir).Limit(int64(dq.page.limit)).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("directory: %w", err)
	}
	return rows, int(total), nil
}

const (
	adminOK  = "ok"
	adminErr = "error"
)

func adminTotal(n int) *int { return &n }

func admitSuper(ctx context.Context) (*zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok || !principal.Super(c) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	return c, nil
}
