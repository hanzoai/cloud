package provider

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// A page to a channel the app has not joined joins it and goes out, once; the
// ops channel's membership is not something an alert should have to wait on.
func TestPostJoiningEntersTheChannelAndPostsAgain(t *testing.T) {
	joined := stubSlackChannels(t)
	token := func() (string, error) { return "xoxb-test", nil }
	posts := 0
	post := func() (string, error) {
		posts++
		if posts == 1 {
			return "", errors.New("slack chat.postMessage failed: not_in_channel")
		}
		return "1710000000.000001", nil
	}

	ts, err := postJoining(context.Background(), "#general", token, post)
	if err != nil || ts != "1710000000.000001" {
		t.Fatalf("got %q, %v; want the retried post's ts", ts, err)
	}
	if posts != 2 || !slices.Equal(*joined, []string{"C-general"}) {
		t.Fatalf("posts = %d, joined = %v; want two posts around one join of C-general", posts, *joined)
	}
}

// A join Slack refuses says so with Slack's own code and tells the reader what to
// do; any other refusal of the post is returned untouched, with no join tried.
func TestPostJoiningNamesARefusedJoin(t *testing.T) {
	joined := stubSlackChannels(t)
	token := func() (string, error) { return "xoxb-test", nil }
	refused := func() (string, error) { return "", errors.New("slack chat.postMessage failed: not_in_channel") }

	_, err := postJoining(context.Background(), "#locked", token, refused)
	if err == nil || !strings.Contains(err.Error(), "is_archived") || !strings.Contains(err.Error(), "/invite") {
		t.Fatalf("err = %v, want Slack's code and the /invite instruction", err)
	}

	other := func() (string, error) { return "", errors.New("slack chat.postMessage failed: msg_too_long") }
	if _, err := postJoining(context.Background(), "#general", token, other); err == nil || !strings.Contains(err.Error(), "msg_too_long") {
		t.Fatalf("err = %v, want the post's own refusal", err)
	}
	if len(*joined) != 0 {
		t.Fatalf("joined %v; only not_in_channel may lead to a join", *joined)
	}
}
