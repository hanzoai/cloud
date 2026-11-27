package social

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

const testIngestToken = "test-ingest-token-not-a-secret-for-prod"

func seedPostizIngestToken(t *testing.T, kc secret.Store) {
	t.Helper()
	if err := kc.PutSecret(cloud.For(context.Background(), authz.AdminOrg), postizSecretRef(postizIngestSecretName), []byte(testIngestToken)); err != nil {
		t.Fatalf("seed ingest token: %v", err)
	}
}

func postizWebhookBody(id, integrationID, provider, content, releaseURL string) []byte {
	b, _ := json.Marshal([]map[string]any{{
		"id": id, "content": content, "publishDate": "2026-11-09T12:00:00.000Z",
		"releaseURL": releaseURL, "state": "PUBLISHED",
		"integration": map[string]any{
			"id": integrationID, "name": "Hanzo", "providerIdentifier": provider, "type": "social",
		},
	}})
	return b
}

func TestIngestPostizValidTokenMirrorsOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_SOCIAL_POSTIZ_POLL_INTERVAL", "off")
		kc := newSocialKMS(t)
		seedPostizIngestToken(t, kc)
		app := mountSocialWith(t, kc)
		body := postizWebhookBody("cm_real_1", "cmw8ytjjf0003luan9en61245", "x", "hello mirror", "https://x.com/hanzoai/1")

		code, out := rawPost(t, app, "/v1/social/ingest/postiz/"+testIngestToken, body)
		if code != http.StatusAccepted {
			t.Fatalf("want 202, got %d %s", code, out)
		}
		posts, err := mounted.State.store.ListPosts(context.Background(), "hanzo", statusMirrored, 10)
		if err != nil || len(posts) != 1 {
			t.Fatalf("mirrored posts = %d err=%v", len(posts), err)
		}
		p := posts[0]
		if p.Origin != "postiz" || p.Status != statusMirrored || p.ExternalIDPostiz != "cm_real_1" || p.ExternalIntegrationId != "cmw8ytjjf0003luan9en61245" || p.ReleaseURL == "" {
			t.Fatalf("post = %+v", p)
		}

		code, _ = rawPost(t, app, "/v1/social/ingest/postiz/"+testIngestToken, body)
		if code != http.StatusAccepted {
			t.Fatalf("replay want 202, got %d", code)
		}
		posts, err = mounted.State.store.ListPosts(context.Background(), "hanzo", statusMirrored, 10)
		if err != nil || len(posts) != 1 {
			t.Fatalf("after replay mirrored = %d err=%v", len(posts), err)
		}
	})
}

func TestIngestPostizBadTokenIs401(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_SOCIAL_POSTIZ_POLL_INTERVAL", "off")
		kc := newSocialKMS(t)
		seedPostizIngestToken(t, kc)
		app := mountSocialWith(t, kc)
		body := postizWebhookBody("cm_real_2", "cmw8ytjjf0003luan9en61245", "x", "nope", "")
		code, out := rawPost(t, app, "/v1/social/ingest/postiz/wrong-token", body)
		if code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d %s", code, out)
		}
	})
}

func TestIngestPostizSendTestIgnored(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_SOCIAL_POSTIZ_POLL_INTERVAL", "off")
		kc := newSocialKMS(t)
		seedPostizIngestToken(t, kc)
		app := mountSocialWith(t, kc)
		body := postizWebhookBody(postizTestPostID, postizTestIntegrationID, "x", "sample", "")
		code, out := rawPost(t, app, "/v1/social/ingest/postiz/"+testIngestToken, body)
		if code != http.StatusAccepted {
			t.Fatalf("want 202, got %d %s", code, out)
		}
		posts, err := mounted.State.store.ListPosts(context.Background(), "hanzo", statusMirrored, 10)
		if err != nil || len(posts) != 0 {
			t.Fatalf("Send-Test must not upsert: %d err=%v", len(posts), err)
		}
		recorded, err := mounted.State.store.RecordEvent(context.Background(), "hanzo", "social.post.test", []byte(postizTestPostID+"|"+postizTestIntegrationID), 1)
		if err != nil {
			t.Fatal(err)
		}
		if recorded {
			t.Fatal("expected test event already recorded (replay should be false)")
		}
	})
}

func TestPublishMirroredReturns409(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_SOCIAL_POSTIZ_POLL_INTERVAL", "off")
		kc := newSocialKMS(t)
		app := mountSocialWith(t, kc)
		ctx := context.Background()
		p, _, err := mounted.State.store.UpsertMirrored(ctx, socialPost{
			ID: "m1", Org: "hanzo", Content: "x", Channel: "x", Status: statusMirrored,
			Origin: "postiz", ExternalIDPostiz: "ext1", ExternalIntegrationId: "cmw8ytjjf0003luan9en61245",
			CreatedAt: 1, UpdatedAt: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		code, body := req(t, app, http.MethodPost, "/v1/social/posts/"+p.ID+"/publish", "hanzo", nil)
		if code != http.StatusConflict || !strings.Contains(string(body), "origin=postiz is read-only") {
			t.Fatalf("want 409 read-only, got %d %s", code, body)
		}
		_, claimed, err := mounted.State.store.ClaimForPublish(ctx, "hanzo", p.ID, 2)
		if err != nil || claimed {
			t.Fatalf("ClaimForPublish claimed=%v err=%v", claimed, err)
		}
	})
}

func TestUnknownSocialPathIsJSON404(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_SOCIAL_POSTIZ_POLL_INTERVAL", "off")
		kc := newSocialKMS(t)
		app := mountSocialWith(t, kc)
		code, body := req(t, app, http.MethodGet, "/v1/social/nope", "hanzo", nil)
		if code != http.StatusNotFound {
			t.Fatalf("want 404, got %d %s", code, body)
		}
		if strings.Contains(strings.ToLower(string(body)), "<html") {
			t.Fatalf("HTML SPA leaked: %s", body)
		}
		if ct := ""; true {
			_ = ct
			if !strings.Contains(string(body), "{") {
				t.Fatalf("want JSON body, got %s", body)
			}
		}
	})
}

func rawPost(t *testing.T, app *zip.App, path string, body []byte) (int, []byte) {
	t.Helper()
	return raw(t, app, http.MethodPost, path, "", strings.NewReader(string(body)), true)
}
