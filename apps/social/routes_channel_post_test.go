package social

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestUnifiedChannelAndPostLifecycle(t *testing.T) {
	app := mountSocial(t)

	// Unauthenticated fails closed with 401 on both /v1/channel and /v1/post.
	if code, _ := req(t, app, http.MethodGet, "/v1/channel", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("anon GET /v1/channel = %d, want 401", code)
	}
	if code, _ := req(t, app, http.MethodGet, "/v1/post", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("anon GET /v1/post = %d, want 401", code)
	}

	// Register two channels in group "Antje Worring" and one in "Hanzo AI".
	code, b1 := req(t, app, http.MethodPost, "/v1/channel/connect", "hanzo", map[string]any{
		"provider": "x",
		"handle":   "@antje_worring",
		"group":    "Antje Worring",
	})
	if code != http.StatusOK {
		t.Fatalf("connect 1 = %d (%s)", code, b1)
	}
	var c1 struct {
		Connected bool `json:"connected"`
		Channel   struct {
			ID        string `json:"id"`
			GroupName string `json:"groupName"`
		} `json:"channel"`
	}
	_ = json.Unmarshal(b1, &c1)
	if !c1.Connected || c1.Channel.ID == "" || c1.Channel.GroupName != "Antje Worring" {
		t.Fatalf("unexpected connect 1: %s", b1)
	}

	code, b2 := req(t, app, http.MethodPost, "/v1/channel/connect", "hanzo", map[string]any{
		"provider": "linkedin",
		"handle":   "Antje Worring",
		"group":    "Antje Worring",
	})
	if code != http.StatusOK {
		t.Fatalf("connect 2 = %d (%s)", code, b2)
	}

	code, b3 := req(t, app, http.MethodPost, "/v1/channel/connect", "hanzo", map[string]any{
		"provider": "bluesky",
		"handle":   "@hanzoai.bsky.social",
		"group":    "Hanzo AI",
	})
	if code != http.StatusOK {
		t.Fatalf("connect 3 = %d (%s)", code, b3)
	}
	var c3 struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	_ = json.Unmarshal(b3, &c3)

	// List groups.
	code, grpBody := req(t, app, http.MethodGet, "/v1/channel/groups", "hanzo", nil)
	if code != http.StatusOK || !bytes.Contains(grpBody, []byte("Antje Worring")) || !bytes.Contains(grpBody, []byte("Hanzo AI")) {
		t.Fatalf("groups = %d (%s)", code, grpBody)
	}

	// Filter channels by group.
	code, listBody := req(t, app, http.MethodGet, "/v1/channel?group=Antje%20Worring", "hanzo", nil)
	if code != http.StatusOK {
		t.Fatalf("list filtered = %d (%s)", code, listBody)
	}
	var listedCh struct {
		Data []struct {
			ID     string `json:"id"`
			Handle string `json:"handle"`
		} `json:"data"`
	}
	_ = json.Unmarshal(listBody, &listedCh)
	if len(listedCh.Data) != 2 {
		t.Fatalf("filtered channel count = %d, want 2 (%s)", len(listedCh.Data), listBody)
	}

	// Schedule a post across the whole "Antje Worring" group -> creates 2 fanout posts!
	future := time.Now().Unix() + 3600
	code, createBody := req(t, app, http.MethodPost, "/v1/post", "hanzo", map[string]any{
		"content":    "Unified Hanzo Cloud social API",
		"group":      "Antje Worring",
		"scheduleAt": future,
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/post = %d (%s)", code, createBody)
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Posts  []struct {
			ID      string `json:"id"`
			Channel string `json:"channel"`
		} `json:"posts"`
	}
	_ = json.Unmarshal(createBody, &created)
	if created.Status != "scheduled" || len(created.Posts) != 2 {
		t.Fatalf("expected 2 scheduled fanout posts, got: %s", createBody)
	}

	// Filter posts by group.
	code, postListBody := req(t, app, http.MethodGet, "/v1/post?group=Antje%20Worring", "hanzo", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/post?group = %d (%s)", code, postListBody)
	}
	var listedPosts struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(postListBody, &listedPosts)
	if len(listedPosts.Data) != 2 {
		t.Fatalf("expected 2 posts for group, got %d (%s)", len(listedPosts.Data), postListBody)
	}

	// Channel and post analytics.
	code, chAn := req(t, app, http.MethodGet, "/v1/channel/"+c1.Channel.ID+"/analytics", "hanzo", nil)
	if code != http.StatusOK || !bytes.Contains(chAn, []byte(c1.Channel.ID)) {
		t.Fatalf("GET /v1/channel/:id/analytics = %d (%s)", code, chAn)
	}
	code, postAn := req(t, app, http.MethodGet, "/v1/post/"+created.ID+"/analytics", "hanzo", nil)
	if code != http.StatusOK || !bytes.Contains(postAn, []byte(created.ID)) {
		t.Fatalf("GET /v1/post/:id/analytics = %d (%s)", code, postAn)
	}

	// Reassign c3 group via PUT /v1/channel/:id/group.
	code, updBody := req(t, app, http.MethodPut, "/v1/channel/"+c3.Channel.ID+"/group", "hanzo", map[string]any{
		"group": "Brand Core",
	})
	if code != http.StatusOK || !bytes.Contains(updBody, []byte("Brand Core")) {
		t.Fatalf("setGroup = %d (%s)", code, updBody)
	}

	// Delete post & channel -> 204.
	if code, _ := req(t, app, http.MethodDelete, "/v1/post/"+created.ID, "hanzo", nil); code != http.StatusNoContent {
		t.Fatalf("DELETE /v1/post/:id = %d, want 204", code)
	}
	if code, _ := req(t, app, http.MethodDelete, "/v1/channel/"+c3.Channel.ID, "hanzo", nil); code != http.StatusNoContent {
		t.Fatalf("DELETE /v1/channel/:id = %d, want 204", code)
	}
}
