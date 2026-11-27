package social

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

func TestPostizToPostNeverScheduled(t *testing.T) {
	po := &postizPost{
		ID: "p1", Content: "hi", Channel: "x", IntegrationID: "cmw8ytjjf0003luan9en61245",
	}
	p := postizToPost(po)
	if p.Status != statusMirrored || p.Origin != "postiz" || p.ScheduleAt != 0 {
		t.Fatalf("want mirrored/postiz/schedule_at=0, got status=%q origin=%q scheduleAt=%d", p.Status, p.Origin, p.ScheduleAt)
	}
	if p.Channel != "x" || p.ExternalIDPostiz != "p1" {
		t.Fatalf("channel/external = %q %q", p.Channel, p.ExternalIDPostiz)
	}
}

func TestPollerUpsertDedupesAndStaysMirrored(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_SOCIAL_POSTIZ_POLL_INTERVAL", "off")
		kc := newSocialKMS(t)
		_ = mountSocialWith(t, kc)
		ctx := context.Background()

		first, inserted, err := mounted.State.store.UpsertMirrored(ctx, postizToPost(&postizPost{
			ID: "pp1", Content: "c", Channel: "x", IntegrationID: "cmw8ytjjf0003luan9en61245",
		}))
		if err != nil || !inserted {
			t.Fatalf("first upsert: inserted=%v err=%v", inserted, err)
		}
		if first.Status != statusMirrored || first.Origin != "postiz" || first.ScheduleAt != 0 {
			t.Fatalf("first = %+v", first)
		}
		_, inserted, err = mounted.State.store.UpsertMirrored(ctx, postizToPost(&postizPost{
			ID: "pp1", Content: "c2", Channel: "x", IntegrationID: "cmw8ytjjf0003luan9en61245",
		}))
		if err != nil || inserted {
			t.Fatalf("second upsert should update: inserted=%v err=%v", inserted, err)
		}
		posts, err := mounted.State.store.ListPosts(ctx, "hanzo", statusMirrored, 10)
		if err != nil || len(posts) != 1 || posts[0].Content != "c2" {
			t.Fatalf("after dedupe: %#v err=%v", posts, err)
		}
	})
}
