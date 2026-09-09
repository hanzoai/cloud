package bot

// The notify family: how something reaches a person who is not looking at the
// screen, and how a line of text reaches an agent nobody is talking to.
//
// Two halves that share nothing but this file.
//
// Web Push is the browser half. A browser hands the gateway a subscription —
// an HTTPS endpoint minted by its push service plus the two keys that endpoint
// will only accept ciphertext under — and the gateway keeps it against the IAM
// principal that presented it. Later, a notification is encrypted to those
// keys (RFC 8291) under a JWT signed by the gateway's own key (RFC 8292) and
// posted to the endpoint. The signing key is the gateway's identity: a browser
// binds its subscription to the public half, so changing it invalidates every
// subscription in existence. It therefore lives in Hanzo KMS, is created once,
// and is never regenerated.
//
// wake is the agent half. It puts a line of text where an agent will find it
// and tells the bot's open connections, so an agent already listening acts on
// it now rather than at its next turn.
//
// OpenClaw binds a subscription to its own paired-device key. That handshake is
// not implemented here and the binding is the IAM principal instead: the caller
// IAM validated owns the row, and every later read, write and delete checks the
// same fact. There is no second identity to keep in step with the first.

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	// Quiet hours are stated in an IANA zone, and a deployment must be able to
	// resolve one whether or not its image carries a zone database.
	_ "time/tzdata"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/hanzoai/cloud"
)

func init() {
	Register("wake", Write, wake)
	Register("push.web.vapidPublicKey", Write, vapidPublicKey)
	Register("push.web.subscribe", Write, pushSubscribe)
	Register("push.web.unsubscribe", Write, pushUnsubscribe)
	Register("push.web.preferences.get", Read, pushPrefsGet)
	Register("push.web.preferences.set", Write, pushPrefsSet)
	Register("push.web.test", Write, pushTest)
	Declare("users.prefs.changed", "session.wake")
}

const (
	// pushDocs holds one document per browser endpoint.
	pushDocs = "push.web"
	// prefDocs holds one document per person: their notification defaults
	// across every browser they use.
	prefDocs = "push.user"
	// wakeDocs holds the wakes an org has raised, newest first.
	wakeDocs = "wake"

	// prefsKey names the preferences that moved, in the users.prefs.changed
	// event. The UI reruns its reconciliation only for this key.
	prefsKey = "notifications.web.v1"

	// vapidRef is where the gateway's signing key lives in KMS. It is
	// deployment-wide rather than per org: a browser holds one subscription per
	// origin, so an identity that changed with the org would invalidate that
	// subscription every time the person switched.
	//
	// This string is a coordinate in KMS, not a name in this package. Moving it
	// mints a new identity, and every browser already subscribed under the old
	// one goes silent — no error anywhere, the pushes simply stop being
	// accepted. It survived one rename of this package because nothing had
	// deployed yet; a later one must migrate the secret rather than follow the
	// package.
	vapidRef = "bot/vapid"
)

const (
	pushMaxEndpoint = 2048
	pushMaxKey      = 512
	pushMaxLabel    = 80  // runes of a device pushLabel
	pushMaxAgents   = 128 // agent ids in a filter, and the length of each
	wakeMaxText     = 16 << 10
	pushMaxSubs     = 1000           // subscriptions one test may fan out to
	pushTTL         = 60             // seconds a push service holds an undelivered test
	pushRecord      = 4096           // RFC 8188 record size
	pushExpiry      = 12 * time.Hour // life of a VAPID assertion; RFC 8292 caps it at 24h
	pushRead        = 8 << 10        // of a push service's answer, which is diagnostics
)

// pushDetails is the closed set of detail levels a notification may carry.
var pushDetails = map[string]bool{"private": true, "identified": true, "detailed": true}

// notifyStore opens the org's own file. A browser subscription and a person's
// notification defaults belong to the person and the tenant; a bot binding on
// the connection that happens to be asking says nothing about where they live,
// so this ignores it.
func notifyStore(c *Call) (*Store, error) {
	st, err := c.svc.State.stores.For(c.me.org, "")
	if err != nil {
		c.svc.Log.Error("open bot store", "org", c.me.org, "err", err)
		return nil, Unavailable("the store could not be opened")
	}
	return st, nil
}

// ---- preferences ----
//
// Three layers. A person states defaults that follow them to every browser; a
// browser overrides what it wants; the pushSender applies the resolution of the
// two. The merge is field-wise and the fields do not agree on what "override"
// means — categories merge per key, everything else replaces whole — so it is
// written once here and read from nowhere else.

// pushCats is the six things a notification can be about, every one of them
// answered. The protocol leaves humanMentioned optional in the wire schema; a
// resolved preference always carries all six.
type pushCats struct {
	Approval   bool `json:"approvalRequested"`
	Finished   bool `json:"agentFinished"`
	Question   bool `json:"agentQuestion"`
	Mentioned  bool `json:"humanMentioned"`
	Scheduled  bool `json:"scheduledTaskFailed"`
	Background bool `json:"backgroundTaskFailed"`
}

// pushCatsIn is the same six as they arrive and as a browser stores them: a
// pointer each, so a category nobody mentioned is told from one turned off.
// The distinction decides who is notified — approvalRequested defaults to on,
// so reading an absent key as false silently stops the one notification most
// deployments want.
type pushCatsIn struct {
	Approval   *bool `json:"approvalRequested,omitempty"`
	Finished   *bool `json:"agentFinished,omitempty"`
	Question   *bool `json:"agentQuestion,omitempty"`
	Mentioned  *bool `json:"humanMentioned,omitempty"`
	Scheduled  *bool `json:"scheduledTaskFailed,omitempty"`
	Background *bool `json:"backgroundTaskFailed,omitempty"`
}

// pushQuiet is a window in a person's own day during which nothing is sent.
// The window wraps midnight when start is after end.
type pushQuiet struct {
	Enabled bool   `json:"enabled"`
	Start   int    `json:"startMinute"`
	End     int    `json:"endMinute"`
	Zone    string `json:"timeZone"`
}

// pushPrefs is a person's defaults.
type pushPrefs struct {
	Cats   pushCats  `json:"categories"`
	Detail string    `json:"detailLevel"`
	Quiet  pushQuiet `json:"quietHours"`
	Agents []string  `json:"agentIds"`
}

// pushDevice is one browser's overrides. Every field but the first two is
// absent unless that browser said something about it, and absent inherits.
type pushDevice struct {
	Enabled bool        `json:"enabled"`
	Label   string      `json:"pushLabel"`
	Cats    *pushCatsIn `json:"categories,omitempty"`
	Detail  string      `json:"detailLevel,omitempty"`
	Quiet   *pushQuiet  `json:"quietHours,omitempty"`
	Agents  *[]string   `json:"agentIds,omitempty"`
}

// pushEffective is what a pushSender applies: the person's defaults with the
// browser's overrides folded in, plus the two fields only a browser has.
type pushEffective struct {
	Enabled bool      `json:"enabled"`
	Label   string    `json:"pushLabel"`
	Cats    pushCats  `json:"categories"`
	Detail  string    `json:"detailLevel"`
	Quiet   pushQuiet `json:"quietHours"`
	Agents  []string  `json:"agentIds"`
}

// pushDefaults is what a person has before they have said anything. Only
// approvals are on: a notification that interrupts someone is worth it when the
// agent is blocked waiting for them, and rarely otherwise.
func pushDefaults() pushPrefs {
	return pushPrefs{
		Cats:   pushCats{Approval: true},
		Detail: "private",
		Quiet:  pushQuiet{Start: 22 * 60, End: 7 * 60, Zone: "UTC"},
		Agents: []string{},
	}
}

// pushResolve folds a browser's overrides onto a person's defaults. Categories
// merge one key at a time; detail, quiet hours and the agent filter replace
// whole. Enabled and the pushLabel have no default half — they exist only per
// browser.
func pushResolve(user pushPrefs, dev pushDevice) pushEffective {
	e := pushEffective{
		Enabled: dev.Enabled,
		Label:   dev.Label,
		Cats:    user.Cats,
		Detail:  user.Detail,
		Quiet:   user.Quiet,
		Agents:  user.Agents,
	}
	if c := dev.Cats; c != nil {
		for _, over := range []struct {
			from *bool
			to   *bool
		}{
			{c.Approval, &e.Cats.Approval},
			{c.Finished, &e.Cats.Finished},
			{c.Question, &e.Cats.Question},
			{c.Mentioned, &e.Cats.Mentioned},
			{c.Scheduled, &e.Cats.Scheduled},
			{c.Background, &e.Cats.Background},
		} {
			if over.from != nil {
				*over.to = *over.from
			}
		}
	}
	if dev.Detail != "" {
		e.Detail = dev.Detail
	}
	if dev.Quiet != nil {
		e.Quiet = *dev.Quiet
	}
	if dev.Agents != nil {
		e.Agents = *dev.Agents
	}
	if e.Agents == nil {
		e.Agents = []string{}
	}
	return e
}

// pushPrefsIn is a person's defaults as they arrive. Every field is required by the
// protocol, and a pointer here so that a missing one is refused rather than
// read as its zero.
type pushPrefsIn struct {
	Cats   *pushCatsIn `json:"categories"`
	Detail *string     `json:"detailLevel"`
	Quiet  *pushQuiet  `json:"quietHours"`
	Agents *[]string   `json:"agentIds"`
}

// pushDeviceIn is a browser's overrides as they arrive. Only the first two are
// required; the rest are absent when unstated and stay absent when stored.
type pushDeviceIn struct {
	Enabled *bool       `json:"enabled"`
	Label   *string     `json:"pushLabel"`
	Cats    *pushCatsIn `json:"categories"`
	Detail  *string     `json:"detailLevel"`
	Quiet   *pushQuiet  `json:"quietHours"`
	Agents  *[]string   `json:"agentIds"`
}

// full turns the six arriving categories into six answers. Five are required;
// humanMentioned is not, and defaults off.
func (in *pushCatsIn) full() (pushCats, error) {
	if in == nil {
		return pushCats{}, Invalid("preferences.categories is required")
	}
	var out pushCats
	for _, f := range []struct {
		name string
		from *bool
		to   *bool
	}{
		{"approvalRequested", in.Approval, &out.Approval},
		{"agentFinished", in.Finished, &out.Finished},
		{"agentQuestion", in.Question, &out.Question},
		{"scheduledTaskFailed", in.Scheduled, &out.Scheduled},
		{"backgroundTaskFailed", in.Background, &out.Background},
	} {
		if f.from == nil {
			return pushCats{}, Invalid("preferences.categories.%s is required", f.name)
		}
		*f.to = *f.from
	}
	if in.Mentioned != nil {
		out.Mentioned = *in.Mentioned
	}
	return out, nil
}

// stated reports whether a browser said anything at all about categories.
func (in *pushCatsIn) stated() bool {
	return in != nil && (in.Approval != nil || in.Finished != nil || in.Question != nil ||
		in.Mentioned != nil || in.Scheduled != nil || in.Background != nil)
}

// pushCheckQuiet refuses a window that is not a window: a minute outside the day,
// or a zone no clock can be read in.
func pushCheckQuiet(q *pushQuiet) error {
	if q == nil {
		return nil
	}
	if q.Start < 0 || q.Start > 1439 || q.End < 0 || q.End > 1439 {
		return Invalid("quiet hours run from minute 0 to minute 1439")
	}
	zone := strings.TrimSpace(q.Zone)
	if zone == "" || len(zone) > 128 {
		return Invalid("quiet hours need a time zone")
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return Invalid("invalid notification quiet-hours time zone")
	}
	q.Zone = zone
	return nil
}

// pushCheckDetail refuses a detail level outside the closed set.
func pushCheckDetail(level *string) error {
	if level == nil {
		return nil
	}
	if !pushDetails[*level] {
		return Invalid("detailLevel must be private, identified or detailed")
	}
	return nil
}

// pushCheckAgents refuses a filter the protocol would not carry, then trims,
// deduplicates and keeps the order the person gave.
func pushCheckAgents(in *[]string) (*[]string, error) {
	if in == nil {
		return nil, nil
	}
	if len(*in) > pushMaxAgents {
		return nil, Invalid("agentIds carries at most %d ids", pushMaxAgents)
	}
	seen := make(map[string]bool, len(*in))
	out := make([]string, 0, len(*in))
	for _, raw := range *in {
		if len(raw) == 0 || len(raw) > pushMaxAgents {
			return nil, Invalid("an agent id is 1 to %d characters", pushMaxAgents)
		}
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return &out, nil
}

// pushLabel is what a person calls one of their browsers. Control and format
// characters are dropped rather than shown: a pushLabel is read next to a
// notification, and one that can hide or reorder its own text is a way to lie
// about which browser is asking.
func pushLabel(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r == ' ':
			b.WriteRune(r)
		case r < 0x20, r == 0x7f, r == 0xa0, r == 0x1680, r == 0x202f, r == 0x205f, r == 0x3000,
			r >= 0x2000 && r <= 0x200f, r >= 0x2028 && r <= 0x202e, r >= 0x2060 && r <= 0x206f,
			r == 0xfeff, r == 0x115f, r == 0x1160, r == 0x3164, r == 0xffa0:
		default:
			b.WriteRune(r)
		}
	}
	out := []rune(strings.TrimSpace(b.String()))
	if len(out) > pushMaxLabel {
		out = out[:pushMaxLabel]
	}
	return string(out)
}

// ---- subscriptions ----

// pushSub is one browser endpoint and what may be sent to it.
type pushSub struct {
	// ID is what the gateway calls this subscription when reporting on it.
	ID string `json:"id"`
	// User is the IAM principal that presented the endpoint. Every read, write
	// and delete checks it: this field IS the binding.
	User string `json:"user"`
	// Endpoint is the URL the push service will accept ciphertext at.
	Endpoint string `json:"endpoint"`
	// P256dh is the browser's public key, and Auth the shared secret the
	// encryption is salted with. Both arrive base64url.
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
	// Device is this browser's overrides on its owner's defaults.
	Device pushDevice `json:"device"`
}

// pushID names the document one endpoint lives in. Hashing it keeps a
// bearer URL out of the key space while still giving one endpoint exactly one
// row, which is what makes a second registration an update and not a duplicate.
func pushID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

// pushCheckEndpoint refuses anything that is not an endpoint a push service could
// answer at.
func pushCheckEndpoint(endpoint string) error {
	if endpoint == "" || len(endpoint) > pushMaxEndpoint {
		return Invalid("endpoint is 1 to %d characters", pushMaxEndpoint)
	}
	if !strings.HasPrefix(endpoint, "https://") {
		return Invalid("endpoint must be an https URL")
	}
	if _, err := url.Parse(endpoint); err != nil {
		return Invalid("endpoint is not a URL")
	}
	return nil
}

// pushBound loads the subscription for an endpoint and refuses one that is not the
// caller's. A row nobody holds and a row somebody else holds are the same
// answer on purpose: neither tells the caller whether the endpoint is known.
func pushBound(c *Call, st *Store, endpoint string) (pushSub, error) {
	var sub pushSub
	err := st.Get(c.Context(), pushDocs, pushID(endpoint), &sub)
	if errors.Is(err, ErrNoDoc) || (err == nil && sub.User != c.User()) {
		return pushSub{}, Forbidden("subscription is not bound to this user")
	}
	if err != nil {
		return pushSub{}, err
	}
	return sub, nil
}

// pushWatch is the key a person's own connections listen on, so that a change
// one browser makes reaches their other browsers and nobody else's.
func pushWatch(user string) string { return "user:" + user }

// ---- methods ----

// vapidPublicKey answers with the public half of the gateway's signing key. A
// browser binds its subscription to these bytes, so the answer is also how the
// UI tells a subscription minted for this gateway from one minted for another.
func vapidPublicKey(c *Call) (any, error) {
	if err := c.Bind(&struct{}{}); err != nil {
		return nil, err
	}
	key, err := vapidKey(c.Context(), c.svc)
	if err != nil {
		return nil, err
	}
	pub, err := key.PublicKey.ECDH()
	if err != nil {
		return nil, err
	}
	return map[string]string{"vapidPublicKey": pushB64(pub.Bytes())}, nil
}

// pushSubscribe records a browser endpoint against the caller. It is an upsert
// keyed by the endpoint, because the browser sends it again on every connect;
// a second registration keeps the subscription's identity and that browser's
// own preferences rather than resetting them.
func pushSubscribe(c *Call) (any, error) {
	var p struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if err := pushCheckEndpoint(p.Endpoint); err != nil {
		return nil, err
	}
	if len(p.Keys.P256dh) == 0 || len(p.Keys.P256dh) > pushMaxKey ||
		len(p.Keys.Auth) == 0 || len(p.Keys.Auth) > pushMaxKey {
		return nil, Invalid("subscription keys are 1 to %d characters", pushMaxKey)
	}
	// The keys are what the payload is encrypted to. One we cannot encrypt to
	// is not a subscription, so it is refused now rather than at the first
	// notification nobody receives.
	if _, err := pushBrowserKey(p.Keys.P256dh); err != nil {
		return nil, Invalid("p256dh is not a P-256 public key")
	}
	if secret, err := pushUnb64(p.Keys.Auth); err != nil || len(secret) != 16 {
		return nil, Invalid("auth is not a 16-byte secret")
	}
	if c.User() == "" {
		return nil, Invalid("an authenticated user is required")
	}

	st, err := notifyStore(c)
	if err != nil {
		return nil, err
	}
	id := pushID(p.Endpoint)

	// Reading who holds this endpoint and writing the subscription are one act.
	// A browser that resubscribes keeps the id and the per-browser preferences
	// of the row it already had, and a second call reading the same row before
	// the first wrote it would issue a second id and drop those preferences.
	sub := pushSub{ID: mint("sub"), User: c.User(), Device: pushDevice{Enabled: true}}
	if err := st.Do(c.Context(), func(st *Store) error {
		var held pushSub
		switch err := st.Get(c.Context(), pushDocs, id, &held); {
		case err == nil && held.User != c.User():
			return Forbidden("endpoint is already bound to another user")
		case err == nil:
			sub.ID, sub.Device = held.ID, held.Device
		case errors.Is(err, ErrNoDoc):
		default:
			return err
		}
		sub.Endpoint, sub.P256dh, sub.Auth = p.Endpoint, p.Keys.P256dh, p.Keys.Auth
		return st.Put(c.Context(), pushDocs, id, sub)
	}); err != nil {
		return nil, err
	}
	return map[string]string{"subscriptionId": sub.ID}, nil
}

// pushUnsubscribe forgets one endpoint. The UI calls it both to stop
// notifications and to drop a row that belongs to a gateway this one is not, so
// it must remove exactly the caller's own row and never another's.
func pushUnsubscribe(c *Call) (any, error) {
	var p struct {
		Endpoint string `json:"endpoint"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if err := pushCheckEndpoint(p.Endpoint); err != nil {
		return nil, err
	}
	st, err := notifyStore(c)
	if err != nil {
		return nil, err
	}
	if _, err := pushBound(c, st, p.Endpoint); err != nil {
		return nil, err
	}
	if err := st.Delete(c.Context(), pushDocs, pushID(p.Endpoint)); err != nil {
		return nil, err
	}
	return map[string]bool{"removed": true}, nil
}

// pushPrefsGet answers the three layers at once: what the person set, what this
// browser overrode, and what a pushSender would actually apply. The UI renders all
// three, so computing the resolution here is what keeps the screen and the
// pushSender from disagreeing.
func pushPrefsGet(c *Call) (any, error) {
	var p struct {
		Endpoint string `json:"endpoint"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if err := pushCheckEndpoint(p.Endpoint); err != nil {
		return nil, err
	}
	st, err := notifyStore(c)
	if err != nil {
		return nil, err
	}
	sub, err := pushBound(c, st, p.Endpoint)
	if err != nil {
		return nil, err
	}
	user, err := pushStored(c, st)
	if err != nil {
		return nil, err
	}
	// From here on this connection hears when the person's defaults move,
	// whichever of their browsers moved them.
	c.Watch(pushWatch(c.User()))
	return map[string]any{
		"durableIdentity": c.User() != "",
		"user":            user,
		"device":          sub.Device,
		"effective":       pushResolve(user, sub.Device),
	}, nil
}

// pushPrefsSet writes one layer whole. There are no partial edits: the UI sends
// the complete object it is showing, and the answer is the normalized value it
// will read back, not the request echoed.
func pushPrefsSet(c *Call) (any, error) {
	var p struct {
		Endpoint string          `json:"endpoint"`
		Scope    string          `json:"scope"`
		Prefs    json.RawMessage `json:"preferences"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if err := pushCheckEndpoint(p.Endpoint); err != nil {
		return nil, err
	}
	st, err := notifyStore(c)
	if err != nil {
		return nil, err
	}
	// The endpoint has to be this caller's before anything is written under it,
	// whichever layer the write names.
	if _, err := pushBound(c, st, p.Endpoint); err != nil {
		return nil, err
	}

	switch p.Scope {
	case "user":
		var in pushPrefsIn
		if err := pushClosed(p.Prefs, &in); err != nil {
			return nil, err
		}
		if err := pushCheckQuiet(in.Quiet); err != nil {
			return nil, err
		}
		if err := pushCheckDetail(in.Detail); err != nil {
			return nil, err
		}
		if c.User() == "" {
			return nil, Invalid("user defaults require a durable authenticated profile")
		}
		cats, err := in.Cats.full()
		if err != nil {
			return nil, err
		}
		if in.Detail == nil || in.Quiet == nil || in.Agents == nil {
			return nil, Invalid("user defaults state every field: categories, detailLevel, quietHours, agentIds")
		}
		agents, err := pushCheckAgents(in.Agents)
		if err != nil {
			return nil, err
		}
		out := pushPrefs{Cats: cats, Detail: *in.Detail, Quiet: *in.Quiet, Agents: *agents}
		if err := st.Put(c.Context(), prefDocs, c.User(), out); err != nil {
			return nil, err
		}
		c.Watch(pushWatch(c.User()))
		// The defaults are the person's and live in the org's own file, so
		// every browser of theirs hears it — including one whose socket bound
		// to a bot, which reads that same file.
		PublishOrg(c.Org(), pushWatch(c.User()), "users.prefs.changed", map[string]any{
			"profileId": c.User(),
			"keys":      []string{prefsKey},
		})
		return map[string]any{"scope": "user", "preferences": out}, nil

	case "device":
		var in pushDeviceIn
		if err := pushClosed(p.Prefs, &in); err != nil {
			return nil, err
		}
		if err := pushCheckQuiet(in.Quiet); err != nil {
			return nil, err
		}
		if err := pushCheckDetail(in.Detail); err != nil {
			return nil, err
		}
		if in.Enabled == nil || in.Label == nil {
			return nil, Invalid("device preferences state enabled and pushLabel")
		}
		agents, err := pushCheckAgents(in.Agents)
		if err != nil {
			return nil, err
		}
		out := pushDevice{Enabled: *in.Enabled, Label: pushLabel(*in.Label), Quiet: in.Quiet, Agents: agents}
		if in.Cats.stated() {
			out.Cats = in.Cats
		}
		if in.Detail != nil {
			out.Detail = *in.Detail
		}
		// The browser's preferences live on its subscription row, and the row
		// also carries the keys a notification is encrypted to. Reading it and
		// writing it back are one act: a browser that resubscribed with fresh
		// keys in between would otherwise have the old ones restored by this
		// write, and every notification after that goes nowhere.
		if err := st.Do(c.Context(), func(st *Store) error {
			held, err := pushBound(c, st, p.Endpoint)
			if err != nil {
				return err
			}
			held.Device = out
			return st.Put(c.Context(), pushDocs, pushID(p.Endpoint), held)
		}); err != nil {
			return nil, err
		}
		return map[string]any{"scope": "device", "preferences": out}, nil
	}
	return nil, Invalid(`scope is "user" or "device"`)
}

// pushTest sends one notification to every browser the caller has registered,
// so an operator can see whether delivery works at all. It reaches the caller's
// own subscriptions and no colleague's: what is being tested is this person's
// browsers, and a test that rang someone else's phone would be a surprise
// rather than a result.
//
// It applies no preference and no quiet hours. A test that a preference could
// swallow tells the operator nothing about the transport.
func pushTest(c *Call) (any, error) {
	var p struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	title, body := strings.TrimSpace(p.Title), strings.TrimSpace(p.Body)
	if title == "" {
		title = c.svc.Brand
	}
	if title == "" {
		title = "Notification"
	}
	if body == "" {
		body = "Web push test notification"
	}

	st, err := notifyStore(c)
	if err != nil {
		return nil, err
	}
	docs, err := st.List(c.Context(), pushDocs, pushMaxSubs, 0)
	if err != nil {
		return nil, err
	}
	subs := make([]pushSub, 0, len(docs))
	for _, d := range docs {
		var sub pushSub
		if err := json.Unmarshal(d.Doc, &sub); err != nil || sub.User != c.User() {
			continue
		}
		subs = append(subs, sub)
	}
	if len(subs) == 0 {
		return nil, Invalid("no web push subscriptions registered")
	}

	key, err := vapidKey(c.Context(), c.svc)
	if err != nil {
		return nil, err
	}
	subject := strings.TrimSpace(c.svc.Domain)
	if subject == "" {
		return nil, Unavailable("the deployment states no domain to sign a push assertion with")
	}
	payload, err := json.Marshal(map[string]string{"title": title, "body": body})
	if err != nil {
		return nil, err
	}

	results := make([]pushResult, 0, len(subs))
	var reached bool
	for _, sub := range subs {
		r := pushDeliver(c.Context(), key, "https://"+subject, sub, payload)
		results = append(results, r)
		reached = reached || r.OK
		// A push service reports a subscription the browser has abandoned by
		// refusing it. Keeping the row would mean retrying it forever.
		if r.Status == http.StatusGone || r.Status == http.StatusNotFound {
			if err := st.Delete(c.Context(), pushDocs, pushID(sub.Endpoint)); err != nil {
				c.Log().Error("drop expired push subscription", "org", c.Org(), "err", err)
			}
		}
	}
	if !reached {
		return nil, &Fault{
			Code:      codeUnavail,
			Message:   "all web push deliveries failed",
			Details:   map[string]any{"results": results},
			Retryable: true,
		}
	}
	return map[string]any{"results": results}, nil
}

// pushResult is what one delivery attempt came to.
type pushResult struct {
	OK     bool   `json:"ok"`
	ID     string `json:"subscriptionId"`
	Status int    `json:"statusCode,omitempty"`
	Error  string `json:"error,omitempty"`
}

// pushStored reads a person's defaults, answering the defaults themselves when
// they have never set any.
func pushStored(c *Call, st *Store) (pushPrefs, error) {
	out := pushDefaults()
	err := st.Get(c.Context(), prefDocs, c.User(), &out)
	if errors.Is(err, ErrNoDoc) {
		return pushDefaults(), nil
	}
	if err != nil {
		return pushPrefs{}, err
	}
	if out.Agents == nil {
		out.Agents = []string{}
	}
	return out, nil
}

// ---- wake ----

// wake puts a line of text where an agent will find it, and tells the bot's
// open connections so one already listening acts on it now.
//
// Both halves are needed and only one of them is durable. The record is what
// survives an agent that is not running; the event is what makes "now" mean
// now. A wake that is only published is lost the moment nobody is listening,
// and a wake that is only recorded waits for a turn that may be hours away.
func wake(c *Call) (any, error) {
	// The one method in this family whose parameters are open: wake senders
	// attach metadata of their own, and the protocol says so.
	var p struct {
		Mode    string  `json:"mode"`
		Text    string  `json:"text"`
		Session *string `json:"sessionKey"`
		Agent   *string `json:"agentId"`
	}
	if len(c.Params()) > 0 {
		if err := json.Unmarshal(c.Params(), &p); err != nil {
			return nil, Invalid("wake params: %v", err)
		}
	}
	if p.Mode != "now" && p.Mode != "next-heartbeat" {
		return nil, Invalid("mode is now or next-heartbeat")
	}
	if p.Text == "" {
		return nil, Invalid("text is required")
	}
	if len(p.Text) > wakeMaxText {
		return nil, Invalid("text is at most %d bytes", wakeMaxText)
	}
	session, err := wakeNamed("sessionKey", p.Session)
	if err != nil {
		return nil, err
	}
	agent, err := wakeNamed("agentId", p.Agent)
	if err != nil {
		return nil, err
	}

	text := strings.TrimSpace(p.Text)
	if text == "" {
		return map[string]bool{"ok": false}, nil
	}
	// A wakeSubagent session is a lane an agent opened for its own work. An
	// operator wake belongs on the conversation, not inside one of its steps.
	if wakeSubagent(session) {
		return nil, Invalid("wake sessionKey cannot target a wakeSubagent session")
	}
	// Two names for one target that disagree is a caller that has lost track of
	// which one it meant. Rewriting one to match the other would pick for it.
	if owner := wakeAgentOf(session); agent != "" && owner != "" && !strings.EqualFold(agent, owner) {
		return nil, Invalid("wake agentId contradicts the agent that owns sessionKey; pass a single canonical wake target")
	}

	st, err := notifyStore(c)
	if err != nil {
		return nil, err
	}
	record := map[string]any{
		"mode": p.Mode, "text": text, "sessionKey": session,
		"agentId": agent, "by": c.User(), "at": time.Now().UnixMilli(),
	}
	if err := st.Put(c.Context(), wakeDocs, mint("wake"), record); err != nil {
		return nil, err
	}
	// Addressed to the bot the wake was raised on. No method here subscribes a
	// connection to one session's wakes, so a wake published under a session
	// key would reach nobody; the record names the session it is for, and a
	// listener reads that.
	Publish(c.Org(), c.Bot(), "", "session.wake", record)
	return map[string]bool{"ok": true}, nil
}

// wakeNamed reads an optional non-empty string. Present and empty is a caller that
// meant to omit it, and the protocol refuses it rather than guess; present and
// blank is treated as omitted, which is what the trim is for.
func wakeNamed(field string, v *string) (string, error) {
	if v == nil {
		return "", nil
	}
	if *v == "" {
		return "", Invalid("%s must not be empty; omit it instead", field)
	}
	return strings.TrimSpace(*v), nil
}

// wakeSplit reads a session key of the shape "agent:<id>:<rest>", which is how
// a key says which agent owns the lane it names. Anything else owns nothing and
// splits into nothing.
func wakeSplit(key string) (agent, rest string) {
	if len(key) < 6 || !strings.EqualFold(key[:6], "agent:") {
		return "", ""
	}
	end := strings.Index(key[6:], ":")
	if end < 0 {
		return "", ""
	}
	agent, rest = strings.TrimSpace(key[6:6+end]), key[6+end+1:]
	if agent == "" || rest == "" || strings.HasPrefix(rest, ":") {
		return "", ""
	}
	return agent, rest
}

func wakeAgentOf(key string) string { agent, _ := wakeSplit(key); return agent }

// wakeSubagent reports a session key that names a wakeSubagent lane, whether it says so
// at the front or after the agent that owns it.
func wakeSubagent(key string) bool {
	if strings.HasPrefix(strings.ToLower(key), "wakeSubagent:") {
		return true
	}
	_, rest := wakeSplit(key)
	return strings.HasPrefix(strings.ToLower(rest), "wakeSubagent:")
}

// ---- the gateway's signing key ----

// vapidGen serializes creation. Two requests that both find no key would
// otherwise each make one, and the loser's browsers would be bound to a public
// key the gateway no longer holds the private half of.
var vapidGen sync.Mutex

// vapidKey returns the gateway's signing key, creating it the first time it is
// asked for. It is read from KMS on every call rather than cached: the key is
// the gateway's identity, a cached copy is a copy that can go stale against a
// rotation nobody told this process about, and the read is a local one.
func vapidKey(ctx context.Context, s *cloud.Service[state]) (*ecdsa.PrivateKey, error) {
	kms := s.KMS
	if kms == nil {
		return nil, Unavailable("web push has no signing identity: KMS is not configured")
	}
	if der, err := kms.GetSecret(ctx, vapidRef); err == nil && len(der) > 0 {
		return parseVapid(der)
	}

	vapidGen.Lock()
	defer vapidGen.Unlock()
	if der, err := kms.GetSecret(ctx, vapidRef); err == nil && len(der) > 0 {
		return parseVapid(der)
	}
	fresh, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(fresh)
	if err != nil {
		return nil, err
	}
	if err := kms.PutSecret(ctx, vapidRef, der); err != nil {
		s.Log.Error("create web push signing key", "err", err)
		return nil, Unavailable("the web push signing identity could not be created")
	}
	// Read back what was committed, so a process that lost a race to another
	// deployment replica uses the identity that won rather than its own.
	der, err = kms.GetSecret(ctx, vapidRef)
	if err != nil {
		return nil, err
	}
	return parseVapid(der)
}

func parseVapid(der []byte) (*ecdsa.PrivateKey, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("the stored web push signing key is not a P-256 key")
	}
	return ec, nil
}

// ---- delivery ----

// pushSender posts ciphertext to a push service. It follows no redirect — a push
// endpoint answers where it stands, and a redirect is somewhere the browser did
// not name — and gives up rather than hold a connection open.
var pushSender = &http.Client{
	Timeout:       15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// pushDeliver encrypts one payload to one browser and posts it. It reports what
// happened rather than failing: a fan-out over several browsers is several
// independent outcomes, and one unreachable phone is not a failed request.
//
// The content encryption of RFC 8291 and the assertion of RFC 8292 are
// webpush-go's. The signing key is this gateway's, and it is handed over in the
// encoding that library reads: the raw P-256 scalar and the uncompressed public
// point, base64url. The point is the same bytes vapidPublicKey answers with, so
// what signs a notification is what the browser bound its subscription to.
func pushDeliver(ctx context.Context, key *ecdsa.PrivateKey, subject string, sub pushSub, payload []byte) pushResult {
	out := pushResult{ID: sub.ID}
	signer, err := key.ECDH()
	if err != nil {
		out.Error = err.Error()
		return out
	}
	res, err := webpush.SendNotificationWithContext(ctx, payload,
		&webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
		},
		&webpush.Options{
			HTTPClient:      pushSender,
			Subscriber:      subject,
			TTL:             pushTTL,
			RecordSize:      pushRecord,
			VAPIDPublicKey:  pushB64(signer.PublicKey().Bytes()),
			VAPIDPrivateKey: pushB64(signer.Bytes()),
			VapidExpiration: time.Now().Add(pushExpiry),
		})
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer res.Body.Close() //nolint:errcheck
	note, _ := io.ReadAll(io.LimitReader(res.Body, pushRead))
	out.Status = res.StatusCode
	out.OK = res.StatusCode >= 200 && res.StatusCode < 300
	if !out.OK {
		out.Error = strings.TrimSpace(string(note))
		if out.Error == "" {
			out.Error = res.Status
		}
	}
	return out
}

// pushBrowserKey reads the browser's public key off the wire.
func pushBrowserKey(p256dh string) (*ecdh.PublicKey, error) {
	raw, err := pushUnb64(p256dh)
	if err != nil {
		return nil, err
	}
	return ecdh.P256().NewPublicKey(raw)
}

// pushB64 and pushUnb64 are the padding-free base64url the push protocols speak
// throughout. pushUnb64 also accepts the padded spelling, which some browsers emit.
func pushB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func pushUnb64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// pushClosed decodes a nested object and refuses a field it does not declare —
// the same reading of the protocol's closed objects that Call.Bind applies to
// the parameters themselves, applied one level down to the arm of a union.
func pushClosed(raw []byte, v any) error {
	if len(raw) == 0 {
		return Invalid("preferences is required")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return Invalid("preferences: %v", err)
	}
	return nil
}
