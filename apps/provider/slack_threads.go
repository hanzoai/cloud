package provider

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// threadTTL is how long a summons or an answer keeps a Slack thread the agent's.
// A thread it has neither been summoned to nor answered in for longer takes a
// fresh @mention.
const threadTTL = 30 * 24 * time.Hour

// slackFollow records a thread the agent answered in, under the team of org's
// Slack workspace. A record that fails is logged: the answer is already posted,
// and the cost is one more @mention.
func slackFollow(ctx context.Context, org, channel, thread string) {
	s := mounted
	conn, ok := ConnectionFor(org, "slack", "")
	if s == nil || !ok || conn.ExternalID == "" {
		return
	}
	if err := s.State.store.JoinThread(ctx, conn.ExternalID, channel, thread); err != nil {
		s.Log.Warn("slack: answered thread not recorded", "org", org, "err", err)
	}
}

// slackRooms caches slackRoomPublic's answers, per team and channel.
var slackRooms sync.Map

type slackRoomSeen struct {
	public bool
	at     time.Time
}

// slackRoomTTL bounds how long a room's visibility is taken as known.
const slackRoomTTL = time.Hour

// slackRoomPublic answers whether a room is a public channel, for the one event
// that does not say — an app_mention. Slack is asked at most once per room per
// slackRoomTTL, and any failure answers false: a turn then files nothing a
// private room would not.
func slackRoomPublic(ctx context.Context, org, team, channel string) bool {
	key := team + ":" + channel
	if v, ok := slackRooms.Load(key); ok {
		if seen := v.(slackRoomSeen); time.Since(seen.at) < slackRoomTTL {
			return seen.public
		}
	}
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		return false
	}
	var r struct {
		OK      bool `json:"ok"`
		Channel struct {
			IsChannel bool `json:"is_channel"`
			IsPrivate bool `json:"is_private"`
		} `json:"channel"`
	}
	if err := slackPostForm(ctx, string(tok), slackWebAPIBase+"/conversations.info",
		url.Values{"channel": {channel}}, &r); err != nil || !r.OK {
		return false
	}
	public := r.Channel.IsChannel && !r.Channel.IsPrivate
	slackRooms.Store(key, slackRoomSeen{public: public, at: time.Now()})
	return public
}

// JoinThread records that the agent was summoned into a thread, and reaps the
// records past threadTTL. Joining again renews the record.
func (s *Store) JoinThread(ctx context.Context, team, channel, thread string) error {
	if team == "" || channel == "" || thread == "" {
		return nil
	}
	if err := dialect.Text(team, channel, thread); err != nil {
		return fmt.Errorf("join slack thread: %w", err)
	}
	now := time.Now()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO slack_threads (team, channel, thread, created_at) VALUES ($1, $2, $3, $4)
		 ON CONFLICT(team, channel, thread) DO UPDATE SET created_at = excluded.created_at`,
		team, channel, thread, now.Unix()); err != nil {
		return fmt.Errorf("join slack thread: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM slack_threads WHERE created_at < $1`,
		now.Add(-threadTTL).Unix()); err != nil {
		return fmt.Errorf("gc slack threads: %w", err)
	}
	return nil
}

// InThread reports whether the agent was summoned into this thread within
// threadTTL.
func (s *Store) InThread(ctx context.Context, team, channel, thread string) (bool, error) {
	if team == "" || channel == "" || thread == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM slack_threads WHERE team = $1 AND channel = $2 AND thread = $3 AND created_at >= $4`,
		team, channel, thread, time.Now().Add(-threadTTL).Unix()).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("read slack thread: %w", err)
	}
	return n > 0, nil
}
