package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLinkedInCommentsListAndReplyAsCompanyPage(t *testing.T) {
	var gotVersion, gotAuth, gotMethod, gotPath string
	var gotPayload map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("LinkedIn-Version")
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()

		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"elements": []map[string]any{{
					"commentUrn": "urn:li:comment:(urn:li:share:999,111)",
					"object":     "urn:li:share:999",
					"actor":      "urn:li:person:abc",
					"message":    map[string]string{"text": "How fast is Zen 6?"},
					"created":    map[string]int64{"time": 1794992312074},
				}},
			})
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&gotPayload)
			w.Header().Set("X-Restli-Id", "urn:li:comment:(urn:li:share:999,222)")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"commentUrn":"urn:li:comment:(urn:li:share:999,222)"}`))
		}
	}))
	defer srv.Close()

	prev := linkedinAPI
	linkedinAPI = srv.URL
	defer func() { linkedinAPI = prev }()

	comments, err := listLinkedInCommentsWithToken(context.Background(), "li-tok", "urn:li:share:999")
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(comments) != 1 || comments[0].Text != "How fast is Zen 6?" || comments[0].CommentURN != "urn:li:comment:(urn:li:share:999,111)" {
		t.Fatalf("unexpected comments: %+v", comments)
	}
	if gotVersion != linkedinSocialVersion || gotAuth != "Bearer li-tok" || !strings.Contains(gotPath, "/rest/socialActions/") {
		t.Fatalf("list headers/path mismatch: ver=%q auth=%q path=%q", gotVersion, gotAuth, gotPath)
	}

	actor := linkedinActorURN("page", "106896542")
	if actor != "urn:li:organization:106896542" {
		t.Fatalf("company actor want urn:li:organization:106896542, got %q", actor)
	}
	replyID, err := replyLinkedInCommentWithToken(
		context.Background(), "li-tok", actor,
		"urn:li:share:999", "urn:li:comment:(urn:li:share:999,111)",
		"Sub-second TTFT across 100+ models on api.hanzo.ai!",
	)
	if err != nil {
		t.Fatalf("reply comment: %v", err)
	}
	if gotMethod != http.MethodPost || replyID != "urn:li:comment:(urn:li:share:999,222)" {
		t.Fatalf("unexpected reply result: method=%q id=%q", gotMethod, replyID)
	}
	if gotPayload["actor"] != "urn:li:organization:106896542" {
		t.Fatalf("reply actor want org URN, got %v", gotPayload["actor"])
	}
	if gotPayload["parentComment"] != "urn:li:comment:(urn:li:share:999,111)" {
		t.Fatalf("reply parentComment mismatch: %v", gotPayload["parentComment"])
	}
}
