package team

// The directory of channels an org has published, and the join that lets somebody
// from another org into one. HIP-1327.
//
// A channel is already public or private, and public is the default. But public
// means anyone in the OWNING org: `listChannels` resolves the caller's org, asks
// SpacesForOrg for its spaces, and reads each with byClasses(org, space, …) —
// which reaches s.db(org, space). THE ORG SELECTS A DATABASE. So "list the
// public channels across orgs" cannot be a predicate added to that read: answering
// it there means opening every tenant's store on a caller's behalf, which is
// what the physical partition exists to prevent.
//
// This is the somewhere-else to look. One platform-scoped table, holding only
// what an org published by making a channel public, and never a message, a roster
// or a private channel. It is a PROJECTION: when it disagrees with the owning
// store, the store is right and the row is stale — which is why the join
// re-reads the store before admitting anybody. The row says what is FINDABLE;
// the store says what is JOINABLE, and the stricter of the two wins.

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// publicIndex is the cross-org directory. It belongs to no org, so it lives in
// the system namespace — the partition no tenant name can render.
type publicIndex struct{ db *sql.DB }

func openPublicIndex(dir string) (*publicIndex, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "public_rooms", dir, func(db *sql.DB) (*publicIndex, error) {
		// The key is (org, space, channel) because that triple is what addresses a channel
		// in its owning store, and a directory row that cannot be resolved back to
		// one is a result nobody can open. `members` is a COUNT: how busy a channel is
		// is what somebody choosing between channels is asking, while who is in it is
		// that org's to disclose and not this table's to publish.
		const ddl = `
CREATE TABLE IF NOT EXISTS public_rooms (
  org     TEXT    NOT NULL,
  space   TEXT    NOT NULL,
  room    TEXT    NOT NULL,
  name    TEXT    NOT NULL,
  topic   TEXT    NOT NULL DEFAULT '',
  members BIGINT  NOT NULL DEFAULT 0,
  updated BIGINT  NOT NULL,
  PRIMARY KEY (org, space, room)
);
CREATE INDEX IF NOT EXISTS public_rooms_updated ON public_rooms (updated DESC);`
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, ddl)
			return err
		}); err != nil {
			return nil, fmt.Errorf("team: init public index: %w", err)
		}
		return &publicIndex{db: db}, nil
	})
}

// publish records a public channel, or updates the row it already has.
func (x *publicIndex) publish(org, space, channel, name, topic string, members int) error {
	if x == nil {
		return nil
	}
	if err := dialect.Text(org, space, channel, name, topic); err != nil {
		return fmt.Errorf("team: publish %s: %w", channel, err)
	}
	_, err := x.db.Exec(
		`INSERT INTO public_rooms (org, space, room, name, topic, members, updated)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (org, space, room) DO UPDATE SET
		   name = excluded.name, topic = excluded.topic,
		   members = excluded.members, updated = excluded.updated`,
		org, space, channel, name, topic, members, time.Now().Unix(),
	)
	return err
}

// withdraw removes a channel from the directory.
//
// It must run in the SAME write that makes a channel private or archives it. The
// gap between the two is a window in which a private channel is advertised, and a
// sweep that closes it later is a sweep that leaves the window open.
func (x *publicIndex) withdraw(org, space, channel string) error {
	if x == nil {
		return nil
	}
	_, err := x.db.Exec(`DELETE FROM public_rooms WHERE org = $1 AND space = $2 AND room = $3`, org, space, channel)
	return err
}

// listed is one row as the directory answers it. Every field here is one an org
// published by making a channel public; adding another is a disclosure decision.
type listed struct {
	// Org owns the channel. It is also what a caller filters by to browse one org.
	Org string `json:"org"`
	// Space is where the channel lives inside that org.
	Space string `json:"space"`
	// Channel addresses it in the owning store — what a join is called with.
	Channel string `json:"channel"`
	// Name is what a person sees, without the sigil a client draws.
	Name string `json:"name"`
	// Topic is the channel's one-line subject, empty when it has none.
	Topic string `json:"topic,omitempty"`
	// Members counts the channel, and never names anybody in it.
	Members int `json:"members"`
	// Updated is when this row was last written, unix seconds.
	Updated int64 `json:"updated"`
}

// find answers the directory, newest-written first.
//
// `q` matches the name and the topic; `org` narrows to one org. Both are
// optional, and a caller that states neither is browsing everything published,
// which is the point of having a directory at all.
func (x *publicIndex) find(q, org string, limit int) ([]listed, error) {
	if x == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if org = strings.TrimSpace(org); org != "" {
		args = append(args, org)
		where = append(where, "org = $"+strconv.Itoa(len(args)))
	}
	if q = strings.TrimSpace(q); q != "" {
		if err := dialect.Text(q); err != nil {
			return nil, fmt.Errorf("team: find public: %w", err)
		}
		// Both sides lowered, because LIKE folds case on SQLite and not on PostgreSQL.
		like := "%" + q + "%"
		args = append(args, like, like)
		where = append(where, "(lower(name) LIKE lower($"+strconv.Itoa(len(args)-1)+") OR lower(topic) LIKE lower($"+strconv.Itoa(len(args))+"))")
	}
	sqlText := `SELECT org, space, room, name, topic, members, updated FROM public_rooms`
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	sqlText += " ORDER BY updated DESC LIMIT $" + strconv.Itoa(len(args))

	rows, err := x.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]listed, 0, limit)
	for rows.Next() {
		var one listed
		if err := rows.Scan(&one.Org, &one.Space, &one.Channel, &one.Name, &one.Topic, &one.Members, &one.Updated); err != nil {
			return nil, err
		}
		out = append(out, one)
	}
	return out, rows.Err()
}

// holds reports whether the directory carries this channel. It is the FINDABLE
// half of the join decision, and never the whole of it.
func (x *publicIndex) holds(org, space, channel string) (bool, error) {
	if x == nil {
		return false, nil
	}
	var one int
	err := x.db.QueryRow(
		`SELECT 1 FROM public_rooms WHERE org = $1 AND space = $2 AND room = $3`,
		org, space, channel,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (x *publicIndex) Close() error {
	if x == nil || x.db == nil {
		return nil
	}
	return x.db.Close()
}

// publicQuery narrows the directory. Both terms are optional: a caller that
// states neither is browsing everything published, which is what a directory is
// for.
type publicQuery struct {
	// Q matches a channel's name or its topic.
	Q string `json:"q,omitempty" url:"q"`
	// Org narrows to one org's published channels.
	Org string `json:"org,omitempty" url:"org"`
	// Limit caps the page, 50 when unstated and 200 at most. An unparseable
	// value reads as unstated rather than as zero — zero pages is not an answer
	// anybody asked for.
	Limit int `json:"limit,omitempty" url:"limit"`
}

// publicChannels is what the directory answers.
type publicChannels struct {
	// Channels is every published channel the query matched, newest-written first.
	Channels []listed `json:"channels"`
}

// registerPublic declares the directory read as a TYPED op.
//
// The group is built here, from the one prefix constant, because a typed op's
// path is its group's prefix composed with its leaf and cmd/zipdoc resolves that
// prefix from the assignment in the SAME file. Same shape channel.go and message.go
// use.
func (b *channelBridge) registerPublic(app cloud.Router) {
	g := app.Group(teamPrefix)
	g.Get("/public", b.findPublic)
}

// FindPublic lists the channels orgs have published, across every org.
//
// It is NOT part of GET /channels, and the separation is the point: that address
// answers the CALLER'S channels, so folding these in would put strangers' channels
// in somebody's own sidebar.
//
// It reads the directory and never a tenant's store. Every field it can answer
// with is one an org published by making a channel public, so there is nothing here
// to scope by org — a directory only its own org can read is not a directory.
// An authenticated principal is still required, because an anonymous crawler is
// not who this is for.
//
// Example: {"channels": [{"org": "hanzo", "channel": "6543", "name": "general", "members": 12}]}
func (b *channelBridge) findPublic(ctx context.Context, in *publicQuery) (*publicChannels, error) {
	if b.degraded {
		return nil, unavailable()
	}
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	found, err := b.public.find(in.Q, in.Org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(500, "team: public channels: %v", err)
	}
	if found == nil {
		found = []listed{}
	}
	return &publicChannels{Channels: found}, nil
}

// publishChannel records a public channel in the directory, and never fails a caller
// for it.
//
// The channel IS open by the time this runs. Failing the create because a
// projection row did not land would lose the thing that matters in order to
// keep an index tidy — so the row is best-effort and the failure is said out
// loud rather than swallowed.
func (b *channelBridge) publishChannel(org, space, channel, name, topic string, members int) {
	if err := b.public.publish(org, space, channel, name, topic, members); err != nil {
		luxlog.Default().New("subsystem", "team").
			Warn("publish public channel", "org", org, "channel", channel, "err", err)
	}
}
