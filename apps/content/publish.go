package content

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
)

const (
	// statusInProgress is the honest headline a publish returns when it could not win the
	// per-item lease within the wait budget: another publisher already holds it, so this
	// call posts NOTHING (no duplicate) and the caller retries. It is never an error/5xx.
	statusInProgress = "in_progress"

	// publishLeaseTTL bounds a single item's publish claim. It MUST exceed the worst-case
	// fan-out (each channel bounded by distributeTimeout=20s) so a LIVE publisher is never
	// pre-empted mid-flight; it also auto-expires so a crashed publisher never wedges the
	// item. 5 minutes covers a many-channel fan-out with every channel at its timeout while
	// keeping crash recovery bounded.
	publishLeaseTTL = 5 * time.Minute

	// publishLeaseWait is how long a CONTENDER blocks for the holder to finish before
	// answering statusInProgress. A fast winner (the common case) releases in well under a
	// second, so a contender confirms the real "distributed"; a stuck holder yields an
	// honest "in progress" rather than a hang or a 5xx.
	publishLeaseWait = 8 * time.Second
)

// publish.go is the distribution client: fan a published/queued content item OUT to
// social channels. The site half of "publish" needs no push — the site PULLS live
// content (karma.style reads GET /v1/framework/Post?filters=[["status","in",
// ["published"]]]), so becoming `published` IS site-publish. This file handles only
// the channel PUSH.
//
// Decomplected: Distributor is the edge (hanzoai/social, "the distribution edge; cloud
// orchestrates"); Publish is the stable orchestration that reads the CMS document,
// asks the Distributor to post, and records the returned channel post ids back onto the
// document for reconciliation (queued → published). The real Distributor (wired at
// Mount) calls the social Public API:
//
//	GET  {social}/public/v1/integrations?group=<brand>   → a brand's channels
//	POST {social}/public/v1/posts  {type:"now"|"schedule", date, posts:[{integration:{id},
//	     value:[{content,image}], settings:{__type:<provider>}}]}   (Authorization: <org API key>)
//
// with the per-brand key custodied in KMS (via clients/integrations), never in a manifest.
// The Distributor fails closed per call when a brand has no key, so a transition
// into distribution records "not_configured" and NEVER fails the status change.

// Channel is one connected distribution channel for a brand (a social integration).
type Channel struct {
	// ID is the social integration id a post targets. It is the exact value to put
	// in a content item's `channels` list to reach this one connected account.
	ID string `json:"id"`
	// Provider is the network behind the integration: "x", "instagram", "tiktok"
	// and the rest of what the org connected. Naming a provider in a publish
	// targets EVERY connected account of it, so it is the coarse handle where ID
	// is the precise one.
	Provider string `json:"provider"`
	// Name is the account label as the org connected it — the handle a human
	// recognises. It is never an address: a publish resolves channels by ID or by
	// Provider and never by this.
	Name string `json:"name"`
	// Disabled is true for a channel the org switched off at the social edge. It
	// is still listed — this is what the org has CONNECTED, not what it can post
	// to — but a publish never targets it, neither by name nor as part of the
	// "every channel" default.
	Disabled bool `json:"disabled"`
}

// MediaRef is one media attachment on a post.
type MediaRef struct {
	URL  string `json:"url"`
	Alt  string `json:"alt,omitempty"`
	Mime string `json:"mime,omitempty"`
}

// DistributeRequest is the provider-agnostic post the Distributor sends. Content is the
// caption/copy; Channels are provider ids (or integration ids) to target; ScheduleAt ""
// means publish now, otherwise an ISO-8601 future time. Distributed is the idempotency
// guard: the set of channel ids ALREADY posted for this item (from the doc's
// external_ids). A Distributor MUST skip any target already in this set so a
// re-transition or a retry can never post the same channel twice.
type DistributeRequest struct {
	Content     string
	Media       []MediaRef
	Channels    []string
	ScheduleAt  string
	Distributed map[string]bool
}

// DistributeResult is what the edge returns: whether it was scheduled (vs posted
// now), the per-channel external post ids to record for reconciliation, and the
// honest per-channel outcome. ExternalIDs holds ONLY the channels that succeeded
// (channel id → external post id); Channels reports every attempted channel,
// including failures, so partial success is never flattened into a blanket error.
type DistributeResult struct {
	Scheduled   bool
	ExternalIDs map[string]string // channel id → external post id (successes only)
	Channels    []ChannelResult   // per-channel honest status (ok + failed)
}

// ChannelResult is the honest outcome for ONE channel of a distribution. A channel
// that posted carries its ExternalID; a channel that failed carries a short Error.
// This is what lets a partial fan-out (some channels ok, some down) report the truth
// instead of a 5xx.
type ChannelResult struct {
	Channel    string `json:"channel"`              // the social integration id targeted
	Provider   string `json:"provider,omitempty"`   // "x" | "instagram" | ... when known
	Status     string `json:"status"`               // "distributed" | "scheduled" | "failed"
	ExternalID string `json:"externalId,omitempty"` // social post id, when it went out
	Error      string `json:"error,omitempty"`      // short reason, when it failed
}

// Distributor is the channel edge. Channels lists a brand's connected channels;
// Publish posts (or schedules) one item to them. Implementations are the ONLY place a
// social provider/API is touched.
type Distributor interface {
	Channels(ctx context.Context, org string) ([]Channel, error)
	Publish(ctx context.Context, org string, req DistributeRequest) (DistributeResult, error)
}

// PublishInput identifies the CMS item to distribute. The item's channels/caption/media
// are read from the document, so callers name the item, not its content.
type PublishInput struct {
	// DocType is the content type holding the item: Campaign, SocialPost or Asset.
	// Any other name is refused as an unknown content type.
	DocType string `json:"doctype"`
	// Name is the document within that type — the item to distribute. Its caption,
	// media and channel list come off the stored document, so this names WHICH
	// item and says nothing about what goes out.
	Name string `json:"name"`
	// ScheduleAt hands a future go-live to the channel's own scheduler, as an
	// ISO-8601 time. Empty posts now.
	ScheduleAt string `json:"scheduleAt,omitempty"`
}

// PublishResult reports the distribution outcome. Status is "distributed" |
// "scheduled" | "failed" | "not_configured" so a caller (and a transition response)
// sees the honest state without an error being fatal. "failed" means EVERY targeted
// channel failed (the whole fan-out missed) — a partial success stays "distributed"/
// "scheduled" with the per-channel truth in Results. Results is the per-channel
// breakdown (which channel went out, which did not, and why).
type PublishResult struct {
	// Status is the ONE headline, drawn from: "distributed" (something is on
	// record and went out now), "scheduled" (same, handed to the channel's own
	// scheduler for later), "failed" (nothing is on record — this fan-out missed
	// entirely and no earlier one landed), "in_progress" (another publisher holds
	// the item, so this call posted NOTHING and the caller retries), and
	// "not_configured" (no distribution edge is wired; a transition records it
	// instead of failing). A partial fan-out is "distributed"/"scheduled", never
	// "failed" — the per-channel truth is in Results.
	Status string `json:"status"`
	// Channels is the channel list read off the content document — integration ids
	// or provider names, as the item declares them. Empty when the item names
	// none, which targets every connected, enabled channel. It is what was ASKED
	// for; Results is what happened.
	Channels []string `json:"channels,omitempty"`
	// ExternalIDs maps channel id → the post id that channel returned, merged with
	// everything earlier publishes recorded. Successes only, and it is the
	// idempotency ledger: a channel named here is skipped by every later publish
	// of this item, so the map only ever grows.
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
	// Results is the outcome per channel — which went out, which did not and why —
	// covering the whole fan-out including failures, so partial success is never
	// flattened into one verdict. A channel the org has not connected appears here
	// as failed with "channel not connected".
	Results []ChannelResult `json:"results,omitempty"`
}

// Publish distributes a CMS content item to its channels and records the returned post
// ids back onto the document (best effort — a distribution outage never wedges the CMS).
// It is the ONE publish path: the HTTP handler, the content_publish automation action,
// and a transition's side effect all call it. org MUST be the validated tenant.
func Publish(ctx context.Context, org string, in PublishInput) (PublishResult, error) {
	s := mounted
	if s == nil {
		return PublishResult{}, errNotMounted
	}
	if _, ok := publishable(in.DocType); !ok {
		return PublishResult{}, errUnknownDocType
	}

	// TOCTOU interlock: serialize concurrent publishes of the SAME item. The channel
	// fan-out is NOT idempotent on its own — two publishers that both read an empty
	// external_ids BEFORE either records one would both post the full fan-out (the
	// per-channel guard below only closes the SEQUENTIAL retry, since it reads-then-writes
	// with no interlock). Optimistic CAS at the END cannot help: the non-idempotent post
	// already went out before the write is even attempted. So the read-skipset → fan-out →
	// record section runs under a per-item lease. It is STORE-backed, not an in-memory
	// mutex: the subsystem is multi-driver and multi-pod, so publishes for one item are
	// NOT guaranteed same-process — only a shared-store lease makes them contend. Whoever
	// holds it publishes; a contender re-reads the now-recorded skip-set after it releases
	// and posts only what is left (nothing, in the common case) — exactly once per channel.
	lease, ok, err := framework.AcquireLease(ctx, org, publishLeaseKey(in.DocType, in.Name), publishLeaseTTL, publishLeaseWait)
	if err != nil {
		return PublishResult{}, err
	}
	if !ok {
		// A live publisher holds the item for the whole wait window: answer honestly,
		// post nothing (no duplicate), never a 5xx. The caller retries.
		return PublishResult{Status: statusInProgress}, nil
	}
	defer func() { _ = lease.Release(ctx) }()

	return publishHeld(ctx, org, in)
}

// publishHeld is the fan-out itself: read the skip-set, post what is missing, record
// what came back. THE CALLER HOLDS the item's publish lease (publishLeaseKey) for the
// whole of it — that is the precondition the idempotency argument above rests on, and
// it is a precondition rather than something this function takes itself because the
// section that must be serialized is LARGER than the fan-out for one caller.
//
// Transition is that caller. Its status write carries the whole document (UpdateData
// replaces it — a field left out of the map is dropped, so external_ids cannot simply
// be omitted), and the snapshot it writes is read BEFORE the fan-out. Leasing only the
// fan-out therefore cannot see the erasure: publisher A records external_ids and
// releases, B's already-stale snapshot writes the skip-set back to empty, and B's
// fan-out — correctly leased, correctly re-reading — finds nothing to skip and posts
// the item a second time. Whoever must serialize the write serializes the fan-out with
// it, on the same key, in one section.
func publishHeld(ctx context.Context, org string, in PublishInput) (PublishResult, error) {
	s := mounted
	if s == nil {
		return PublishResult{}, errNotMounted
	}

	id, ok := publishable(in.DocType)
	if !ok {
		return PublishResult{}, errUnknownDocType
	}
	doc, err := framework.Get(ctx, org, id, in.Name)
	if err != nil {
		return PublishResult{}, err
	}

	// Idempotency: read the channels already posted for this item and hand the
	// Distributor that set so it never re-posts them. Under the lease this read is
	// authoritative — no other publisher can be mid-fan-out — so a re-read by a contender
	// after this call releases sees the ids this call recorded and skips them.
	existing := existingExternalIDs(doc.Data)
	req := distributeRequestFromDoc(doc.Data)
	req.ScheduleAt = strings.TrimSpace(in.ScheduleAt)
	req.Distributed = boolSet(existing)

	res, err := s.State.dist.Publish(ctx, org, req)
	if err != nil {
		return PublishResult{}, err
	}

	// MERGE the freshly-posted ids into what was already on record (never clobber the
	// prior fan-out's ids). Persist only when the set actually grew — a fully-idempotent
	// re-publish writes nothing. The write is a TRUSTED server write (external_ids is
	// server-managed; the before_save guard rejects a CLIENT write of it). Best effort:
	// the post already went out, so a record failure must not surface as a 5xx.
	merged := mergeExternalIDs(existing, res.ExternalIDs)
	if len(merged) > len(existing) {
		data := cloneData(doc.Data)
		data["external_ids"] = merged
		if err := framework.UpdateData(withTrustedWrite(ctx), org, id, in.Name, data); err != nil {
			s.Log.Warn("record external ids failed (post already sent)",
				"doctype", in.DocType, "name", in.Name, "err", err)
		}
	}

	return PublishResult{
		Status:      overallStatus(res, merged),
		Channels:    req.Channels,
		ExternalIDs: merged,
		Results:     res.Channels,
	}, nil
}

// publishLeaseKey is the per-item lease key: an org holds ONE publish lease per
// (doctype, name), so concurrent publishes of DIFFERENT items never contend. The org is
// a separate lease column, so the key needs only the item identity.
func publishLeaseKey(doctype, name string) string {
	return "content.publish\x00" + doctype + "\x00" + name
}

// overallStatus folds a fan-out into the ONE honest headline, judged against the MERGED
// reconciliation set (this call's posts + everything already on record). Any channel on
// record makes the item "distributed" (or "scheduled" when this call scheduled) — the
// per-channel failures are still itemised in Results. Only when NOTHING is on record —
// this fan-out missed entirely and nothing was posted before — is it "failed". A caller
// never has to guess: the headline plus the per-channel Results are always consistent.
func overallStatus(res DistributeResult, merged map[string]string) string {
	if len(merged) == 0 {
		return "failed"
	}
	if res.Scheduled {
		return "scheduled"
	}
	return "distributed"
}

// existingExternalIDs reads the channel-id → post-id map already recorded on the doc
// (framework round-trips JSON, so it comes back as map[string]any). Blank/oddly-typed
// entries are dropped — never panics on stored data.
func existingExternalIDs(data map[string]any) map[string]string {
	out := map[string]string{}
	raw, ok := data["external_ids"].(map[string]any)
	if !ok {
		return out
	}
	for k, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out[k] = s
		}
	}
	return out
}

// boolSet is the idempotency guard the Distributor consumes: the set of channel ids
// already posted. nil when nothing has been distributed yet (no filtering to do).
func boolSet(m map[string]string) map[string]bool {
	if len(m) == 0 {
		return nil
	}
	set := make(map[string]bool, len(m))
	for k := range m {
		set[k] = true
	}
	return set
}

// mergeExternalIDs unions the prior external ids with this call's, prior kept (a
// re-post is impossible — the Distributor skipped already-posted channels — so the two
// never actually collide, but prior-wins keeps the guarantee explicit).
func mergeExternalIDs(prior, fresh map[string]string) map[string]string {
	out := make(map[string]string, len(prior)+len(fresh))
	maps.Copy(out, fresh)
	maps.Copy(out, prior)
	return out
}

// distributeRequestFromDoc extracts the provider-agnostic post from a content document:
// caption (falling back to body), the comma-separated channels, and the media array.
func distributeRequestFromDoc(data map[string]any) DistributeRequest {
	content, _ := data["caption"].(string)
	if strings.TrimSpace(content) == "" {
		content, _ = data["body"].(string)
	}
	return DistributeRequest{
		Content:  strings.TrimSpace(content),
		Channels: splitCSV(dataString(data, "channels")),
		Media:    mediaRefsFrom(data["media"]),
	}
}

// mediaRefsFrom coerces the JSON `media` field ([{url,alt,mime}]) into []MediaRef,
// tolerating a missing/oddly-typed value (returns nil) — never panics on client data.
func mediaRefsFrom(v any) []MediaRef {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]MediaRef, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		url, _ := m["url"].(string)
		if strings.TrimSpace(url) == "" {
			continue
		}
		alt, _ := m["alt"].(string)
		mime, _ := m["mime"].(string)
		out = append(out, MediaRef{URL: url, Alt: alt, Mime: mime})
	}
	return out
}

// splitCSV splits a trimmed, non-empty comma list into its non-empty items.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
