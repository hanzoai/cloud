// Copyright © 2026 Hanzo AI. MIT License.

package integrations

// slack_join.go — walk into every public channel in the workspace.
//
// Being a member is what the whole Slack surface rests on: Slack delivers
// message.channels only for channels the app has joined, so a channel it is not
// in is one it cannot hear and cannot speak in. The alternative was
// chat:write.public, which posts into rooms it was never invited to and is
// declined in slack.go — joining is the visible version of the same reach, and
// everyone in the room sees it happen.
//
// Idempotent and resumable by construction: a channel already joined is skipped
// on its own is_member, so a run interrupted by Slack's rate limit is finished by
// running it again rather than by sleeping inside one.

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// slackJoinPage is the conversations.list page size. Slack's own maximum for the
// method is 1000 and its docs recommend well under it; 200 keeps each response
// small enough to read under the 1 MiB body cap with room to spare.
const slackJoinPage = 200

// slackJoinPages bounds the walk. A workspace with more public channels than this
// page limit reaches is finished by a second run — cheap, because everything
// already joined is skipped — and the bound is what stops a paging cursor that
// never terminates from spinning here forever.
const slackJoinPages = 50

// slackChannel is one row of conversations.list. is_member is the whole reason
// the list is read: it says which channels the walk still has work to do in.
type slackChannel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsMember bool   `json:"is_member"`
}

// joinFailure names one channel the bot could not enter, and why. Slack's own
// error code is carried through unchanged: `missing_scope` means the workspace
// installed before channels:join and must re-install, and no other answer here
// tells the operator that.
type joinFailure struct {
	// Channel is Slack's id for the room that refused.
	Channel string `json:"channel"`
	// Name is that room's human name, so the operator does not have to look the id up.
	Name string `json:"name"`
	// Error is Slack's own code, carried through unchanged.
	Error string `json:"error"`
}

type slackJoinOut struct {
	// Listed is every public, unarchived channel the workspace has.
	Listed int `json:"listed"`
	// Joined names the channels this run walked into — the change it made.
	Joined []string `json:"joined"`
	// Already counts the channels it was a member of before, kept apart from
	// Joined because only one of the two is a change.
	Already int `json:"already"`
	// Failed is per-channel, so one refusal does not hide the rest of the walk.
	Failed []joinFailure `json:"failed"`
}

// slackJoin joins every public channel in the caller org's workspace.
//
// Org admin, because it changes what the whole workspace sees: after it the agent
// is a member of every public room and answers in all of them.
func (o ops) slackJoin(ctx context.Context, _ *struct{}) (*slackJoinOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !orgAdmin(ctx) {
		return nil, zip.ErrForbidden("joining the workspace's channels requires org admin")
	}
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		return nil, zip.ErrBadRequest("slack is not connected for this org")
	}
	return slackJoinAll(ctx, string(tok))
}

// slackJoinAll is the walk itself, given the token — custody above, behaviour
// here, the same split as SendSlackAt / slackPostThreadTS, so what the walk DOES
// can be stated against a Slack stub with no key store behind it.
func slackJoinAll(ctx context.Context, botToken string) (*slackJoinOut, error) {
	out := &slackJoinOut{}
	cursor := ""
	for page := 0; page < slackJoinPages; page++ {
		chans, next, err := slackListPublic(ctx, botToken, cursor)
		if err != nil {
			// Nothing to salvage: without the list there is no walk. What already
			// joined stays joined, and a re-run resumes.
			return nil, zip.Errorf(http.StatusBadGateway, "slack conversations.list: %v", err)
		}
		out.Listed += len(chans)
		for _, ch := range chans {
			if ch.IsMember {
				out.Already++
				continue
			}
			if err := slackJoinOne(ctx, botToken, ch.ID); err != nil {
				out.Failed = append(out.Failed, joinFailure{Channel: ch.ID, Name: ch.Name, Error: err.Error()})
				continue
			}
			out.Joined = append(out.Joined, ch.Name)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return out, nil
}

// slackListPublic reads one page of the workspace's public, unarchived channels
// and the cursor for the next, empty when the list is exhausted.
func slackListPublic(ctx context.Context, botToken, cursor string) ([]slackChannel, string, error) {
	form := url.Values{
		"types":            {"public_channel"},
		"exclude_archived": {"true"},
		"limit":            {strconv.Itoa(slackJoinPage)},
	}
	if cursor != "" {
		form.Set("cursor", cursor)
	}
	var r struct {
		OK       bool           `json:"ok"`
		Error    string         `json:"error"`
		Channels []slackChannel `json:"channels"`
		Meta     struct {
			Cursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := slackPostForm(ctx, botToken, slackWebAPIBase+"/conversations.list", form, &r); err != nil {
		return nil, "", err
	}
	if !r.OK {
		return nil, "", errors.New(cmp.Or(r.Error, "unknown"))
	}
	return r.Channels, strings.TrimSpace(r.Meta.Cursor), nil
}

// slackJoinOne enters one channel. Already being in it is Slack's own no-op, so
// this is safe to repeat.
func slackJoinOne(ctx context.Context, botToken, channel string) error {
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := slackPostForm(ctx, botToken, slackWebAPIBase+"/conversations.join", url.Values{"channel": {channel}}, &r); err != nil {
		return err
	}
	if !r.OK {
		return errors.New(cmp.Or(r.Error, "unknown"))
	}
	return nil
}
