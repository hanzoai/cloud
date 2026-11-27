package reward

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/catalog"

	"github.com/hanzoai/cloud/client"
	iampeer "github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/sdk"
)

// directoryFn is IAM's directory, read as the platform, a variable so a test answers it.
var directoryFn = func(ctx context.Context) (*client.Directory, error) {
	return iampeer.IAMDirectory(cloud.For(context.WithoutCancel(ctx), authz.AdminOrg))
}

// people is who the program knows, read once from IAM's directory: a person's
// subject by the "<org>/<name>" the usage ledger records them under, and each org's
// owner, the person an org's referrals are credited to.
type people struct {
	// created is when each person first signed up, by subject.
	created map[string]time.Time
	// bySlug is "<owner>/<name>" to subject, for the people who can earn.
	bySlug map[string]string
	// owner is an org's first live admin, by org.
	owner map[string]string
	// member is every subject that can earn.
	member map[string]bool
}

// roster reads the directory. A service account, an application, a barred person
// and a deleted one never earn; neither does anyone whose subject IAM did not give.
func roster(ctx context.Context) (*people, error) {
	dir, err := directoryFn(ctx)
	if err != nil {
		return nil, err
	}
	p := &people{created: map[string]time.Time{}, bySlug: map[string]string{}, owner: map[string]string{}, member: map[string]bool{}}
	type admin struct {
		sub, created string
	}
	first := map[string]admin{}
	for _, u := range dir.Users {
		if u.ID == "" || u.Forbidden || (u.Type != "" && u.Type != "normal-user") {
			continue
		}
		p.bySlug[u.Owner+"/"+u.Name] = u.ID
		p.member[u.ID] = true
		if t, err := time.Parse(time.RFC3339, u.Created); err == nil {
			p.created[u.ID] = t.UTC()
		}
		if u.Admin && !authz.IsHeldOrg(u.Owner) {
			if have, ok := first[u.Owner]; !ok || cmp.Or(strings.Compare(u.Created, have.created), strings.Compare(u.ID, have.sub)) < 0 {
				first[u.Owner] = admin{u.ID, u.Created}
			}
		}
	}
	for org, a := range first {
		p.owner[org] = a.sub
	}
	return p, nil
}

// Sent is the mark that a feeder's event was stated: an event id, under an id of its
// own. The engine is idempotent by event id, so a mark is only an economy, so a feeder
// that runs every few minutes asks the store one question per event and not the whole
// rule set.
type Sent struct {
	orm.Model[Sent]
}

// sentKind is the mark's kind.
const sentKind = "reward-sent"

func init() { orm.Register[Sent](sentKind, orm.WithStringKey[Sent]()) }

// once states ev unless an earlier run already did, and marks it stated only after
// the engine took it. A refused event is a feeder bug and is returned, not marked.
func (s *state) once(ctx context.Context, ev catalog.Event) (bool, error) {
	id := "sent:" + ev.ID
	if have, err := s.mark(ctx, id); err != nil || have {
		return false, err
	}
	if _, err := s.emit(ctx, ev); err != nil {
		return false, err
	}
	return true, s.mark1(ctx, ev)
}

// mark1 marks ev stated.
func (s *state) mark1(ctx context.Context, ev catalog.Event) error {
	m := orm.New[Sent](s.db)
	m.SetId("sent:" + ev.ID)
	m.CreatedAt, m.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	_, err := s.db.CreateIfAbsent(ctx, m.Key(), m)
	return err
}

// mark reports whether id is marked.
func (s *state) mark(ctx context.Context, id string) (bool, error) {
	m := orm.New[Sent](s.db)
	err := s.db.Get(ctx, s.db.NewKey(orm.Kind[Sent](), id, 0, nil), m)
	switch {
	case err == nil:
		return true, nil
	case orm.IsNotFound(err):
		return false, nil
	}
	return false, err
}

// Cursor is where a feeder read to: the UTC day of its last complete pass.
type Cursor struct {
	orm.Model[Cursor]
	Day string `json:"day"`
}

// cursorKind is the cursor's kind.
const cursorKind = "reward-cursor"

func init() { orm.Register[Cursor](cursorKind, orm.WithStringKey[Cursor]()) }

// cursorOf is the day the feeder name last passed, "" when it never has.
func (s *state) cursorOf(ctx context.Context, name string) (string, error) {
	c := orm.New[Cursor](s.db)
	err := s.db.Get(ctx, s.db.NewKey(orm.Kind[Cursor](), "cursor:"+name, 0, nil), c)
	switch {
	case err == nil:
		return c.Day, nil
	case orm.IsNotFound(err):
		return "", nil
	}
	return "", err
}

// moveCursor records that the feeder name passed day.
func (s *state) moveCursor(ctx context.Context, name, day string) error {
	c := orm.New[Cursor](s.db)
	c.SetId("cursor:" + name)
	c.Day = day
	c.CreatedAt, c.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	return c.PutCtx(ctx)
}

// today is the UTC day of now.
func today() string { return time.Now().UTC().Format(time.DateOnly) }

// sorted returns the keys of m in order.
func sorted[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
