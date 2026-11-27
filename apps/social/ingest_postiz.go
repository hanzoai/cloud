package social

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/hanzoai/authz"
	"github.com/hanzoai/namespace"
)

// ingestPostiz handles POST /v1/social/ingest/postiz/:ingestToken. The path token IS the
// auth — it is compared in constant time against the KMS secret POSTIZ_INGEST_TOKEN
// at path social/postiz. No bearer, no org from the caller: an ingested post lands in
// the org whose integration is named by the Postiz integration id in the body.
//
// A Send-Test post is ignored (no mirrored upsert) but a test event is recorded so the
// sender can confirm the webhook is wired. Every other accepted delivery is idempotent:
// the same body recorded twice writes one event row, and the mirrored post upserts on
// (org, origin=postiz, externalIdPostiz, externalIntegrationId).
func ingestPostiz(ctx context.Context, s *cloud.Service[state], token string, body []byte) (int, any, error) {
	if s.KMS == nil {
		return http.StatusInternalServerError, nil, secret.ErrNone
	}
	// Social runs as a plugin: deps.KMS is KMSPeer. A ref under orgs/admin/… is
	// served only to a call acting for admin (apps/kms admit); Background alone
	// acts as the brand and is refused — same pattern as websearch's keyCtx.
	want, err := s.KMS.GetSecret(cloud.For(context.Background(), authz.AdminOrg), postizSecretRef(postizIngestSecretName))
	if err != nil || !hmac.Equal([]byte(token), want) {
		return http.StatusUnauthorized, map[string]string{"error": "bad token"}, nil
	}

	items, err := decodePostizIngest(body)
	if err != nil {
		return http.StatusBadRequest, map[string]string{"error": "malformed body"}, nil
	}
	if len(items) == 0 {
		return http.StatusAccepted, map[string]string{"status": "empty"}, nil
	}

	// Idempotent on the raw body first: replay of the same delivery is a no-op.
	now := time.Now().Unix()
	orgHint := "hanzo"
	recorded, err := s.State.store.RecordEvent(ctx, orgHint, "social.post.delivery", body, now)
	if err != nil {
		return http.StatusInternalServerError, nil, err
	}
	if !recorded {
		return http.StatusAccepted, map[string]string{"status": "replayed"}, nil
	}

	accepted, ignored := 0, 0
	for _, in := range items {
		// Send-Test: id OR integration id match → record test event, no upsert.
		if in.PostID == postizTestPostID || in.ExternalIntegrationID == postizTestIntegrationID {
			_, _ = s.State.store.RecordEvent(ctx, orgHint, "social.post.test", []byte(in.PostID+"|"+in.ExternalIntegrationID), now)
			ignored++
			continue
		}
		org := orgFromIntegration(in.ExternalIntegrationID)
		if org == "" {
			org = orgHint
		}
		p := socialPost{
			ID:                    mint.ID("mirror"),
			Org:                   org,
			Content:               text.Trim(in.Content, maxContent),
			Channel:               normChannel(in.Channel),
			Status:                statusMirrored,
			Origin:                "postiz",
			ExternalIDPostiz:      in.PostID,
			ExternalIntegrationId: in.ExternalIntegrationID,
			ExternalGroup:         in.ExternalGroup,
			ReleaseURL:            in.ReleaseURL,
			AccountID:             in.AccountID,
			CreatedAt:             now,
			UpdatedAt:             now,
		}
		if _, _, err := s.State.store.UpsertMirrored(ctx, p); err != nil {
			return http.StatusInternalServerError, nil, err
		}
		emitPublished(ctx, s, org, p.ID)
		accepted++
	}
	return http.StatusAccepted, map[string]any{"status": "accepted", "accepted": accepted, "ignored": ignored}, nil
}

// postizIngest is one Postiz delivery element after normalization.
type postizIngest struct {
	PostID                string
	Content               string
	Channel               string
	ExternalIntegrationID string
	ExternalGroup         string
	ReleaseURL            string
	AccountID             string
}

// postizWire is the public Postiz webhook element (array member).
type postizWire struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	PublishDate string `json:"publishDate"`
	ReleaseURL  string `json:"releaseURL"`
	State       string `json:"state"`
	Integration struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		ProviderIdentifier string `json:"providerIdentifier"`
	} `json:"integration"`
}

// decodePostizIngest accepts a JSON array or a single object in Postiz webhook shape
// (or the flat test shape with postId/externalIntegrationId).
func decodePostizIngest(body []byte) ([]postizIngest, error) {
	var arr []postizWire
	if err := json.Unmarshal(body, &arr); err == nil {
		out := make([]postizIngest, 0, len(arr))
		for _, w := range arr {
			out = append(out, postizIngest{
				PostID:                w.ID,
				Content:               w.Content,
				Channel:               w.Integration.ProviderIdentifier,
				ExternalIntegrationID: w.Integration.ID,
				ReleaseURL:            w.ReleaseURL,
			})
		}
		return out, nil
	}
	var one postizWire
	if err := json.Unmarshal(body, &one); err == nil && (one.ID != "" || one.Integration.ID != "") {
		return []postizIngest{{
			PostID:                one.ID,
			Content:               one.Content,
			Channel:               one.Integration.ProviderIdentifier,
			ExternalIntegrationID: one.Integration.ID,
			ReleaseURL:            one.ReleaseURL,
		}}, nil
	}
	var flat struct {
		PostID                string `json:"postId"`
		Content               string `json:"content"`
		Channel               string `json:"channel"`
		ExternalIntegrationID string `json:"externalIntegrationId"`
		ExternalGroup         string `json:"externalGroup"`
		ReleaseURL            string `json:"releaseUrl"`
		AccountID             string `json:"accountId"`
	}
	if err := json.Unmarshal(body, &flat); err != nil {
		return nil, err
	}
	return []postizIngest{{
		PostID: flat.PostID, Content: flat.Content, Channel: flat.Channel,
		ExternalIntegrationID: flat.ExternalIntegrationID, ExternalGroup: flat.ExternalGroup,
		ReleaseURL: flat.ReleaseURL, AccountID: flat.AccountID,
	}}, nil
}

// orgFromIntegration resolves the org that owns a Postiz integration id. The mapping
// is not stored here — the integration id IS the org's own Postiz account coordinate,
// so it is used as the org slug directly (sanitized).
func orgFromIntegration(integrationID string) string {
	if integrationID == "" {
		return ""
	}
	// Known Postiz integration ids for Hanzo land in org hanzo. Anything else is
	// sanitized as its own slug so a foreign webhook cannot write into hanzo by
	// guessing an id — it still needs the ingest token.
	if _, ok := postizIntegrationMap[integrationID]; ok {
		return "hanzo"
	}
	if strings.HasPrefix(integrationID, "cmw") {
		return "hanzo"
	}
	return namespace.Sanitize(integrationID)
}

// normChannel lower-cases and defaults an unknown channel to x.
func normChannel(ch string) string {
	p, ok := normProvider(ch)
	if !ok {
		return "x"
	}
	return p
}

// postizSecretRef builds the KMS ref for a Postiz secret at path social/postiz.
func postizSecretRef(name string) string {
	return postizSecretPath + "/" + name + "@default"
}

// emitPublished emits social.post.published on the event plane. The emitter is
// injectable so tests can capture what was emitted without a network.
var emitPublished = func(ctx context.Context, s *cloud.Service[state], org, id string) {
	cloud.EmitLifecycle(context.WithoutCancel(ctx), cloud.LifecycleEvent{
		Kind:   cloud.LifecycleKind("social.post.published"),
		Org:    org,
		Detail: id,
	})
}

// emitPostMirrored emits social.post.mirrored when the poller mirrors a post.
var emitPostMirrored = func(ctx context.Context, s *cloud.Service[state], org, id string) {
	cloud.EmitLifecycle(context.WithoutCancel(ctx), cloud.LifecycleEvent{
		Kind:   cloud.LifecycleKind("social.post.mirrored"),
		Org:    org,
		Detail: id,
	})
}

// emitAccountChanged emits social.account.changed when the poller sees an integration.
var emitAccountChanged = func(ctx context.Context, s *cloud.Service[state], org string) {
	cloud.EmitLifecycle(context.WithoutCancel(ctx), cloud.LifecycleEvent{
		Kind: cloud.LifecycleKind("social.account.changed"),
		Org:  org,
	})
}

// emitAlert emits social.alert when the poller hits an error or missing key.
var emitAlert = func(ctx context.Context, s *cloud.Service[state], msg string) {
	cloud.EmitLifecycle(context.WithoutCancel(ctx), cloud.LifecycleEvent{
		Kind:   cloud.LifecycleKind("social.alert"),
		Org:    "",
		Detail: msg,
	})
}
