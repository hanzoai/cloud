package team

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

// channelFixture builds a transactor bound to one seeded space and creates
// `n` named channels in it through the REAL write path (applyTx), so the tests
// below read exactly the documents a Team client would have produced.
func channelFixture(t *testing.T, org string, names ...string) (*channelBridge, *session, string) {
	t.Helper()
	srv, sess, ws := rosterServer(t, org, "u-1", "Human", nil)
	for i, name := range names {
		tx := map[string]any{
			"_class":      clTxCreate,
			"objectId":    "ch-" + name,
			"objectClass": clChannel,
			"objectSpace": "core:space:Space",
			"attributes":  map[string]any{"name": name, "private": false, "archived": false, "members": []any{"u-1"}},
			"modifiedBy":  acctSystem,
			"modifiedOn":  time.Now().UnixMilli() + int64(i),
		}
		raw, err := json.Marshal(tx)
		if err != nil {
			t.Fatalf("marshal create: %v", err)
		}
		sess.applyTx(raw)
	}
	return &channelBridge{trans: srv, accounts: srv.accounts}, sess, ws
}

// TestListChannelsReadsTheTransactorsOwnDocuments is the property the whole
// surface rests on: these ops are a second ENDPOINT, never a second store. A channel
// created through the transactor write path appears in the REST listing with no
// sync step, because both read the same per-space docs table.
func TestListChannelsReadsTheTransactorsOwnDocuments(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, ws := channelFixture(t, "acme", "bugfix-1010", "general")
		ctx := orgCtx(t, "acme")

		got, err := b.listChannels(ctx, nil)
		if err != nil {
			t.Fatalf("listChannels: %v", err)
		}
		if len(got.Channels) != 2 {
			t.Fatalf("channels = %d, want 2: %+v", len(got.Channels), got.Channels)
		}
		// Sorted by name, so the order is a fact a client can diff against.
		if got.Channels[0].Name != "bugfix-1010" || got.Channels[1].Name != "general" {
			t.Fatalf("order = %q,%q, want bugfix-1010,general", got.Channels[0].Name, got.Channels[1].Name)
		}
		c := got.Channels[0]
		if c.Space != ws {
			t.Errorf("space = %q, want %q", c.Space, ws)
		}
		// An unclassified channel reads as persistent with no bindings — never as a
		// hole. Every channel that predates the facet takes this path.
		if c.Life != lifeStanding {
			t.Errorf("kind = %q, want %q for a channel carrying no facet", c.Life, lifeStanding)
		}
		if c.Bindings == nil || len(c.Bindings) != 0 {
			t.Errorf("bindings = %#v, want an empty list (not null)", c.Bindings)
		}
		if c.Members == nil {
			t.Error("members = nil, want an empty list — absent members must not marshal to null")
		}
	})
}

// TestBindChannelRoundTrips proves the facet SURVIVES: it is written as a
// platform mixin through applyTx and read back off the stored document, so the
// value a caller set is the value the next reader sees.
func TestBindChannelRoundTrips(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, ws := channelFixture(t, "acme", "bugfix-1010")
		ctx := orgCtx(t, "acme")

		one, err := b.bindChannel(ctx, &teamChannelBind{
			ID: "ch-bugfix-1010", Space: ws, Life: lifeBound,
			Bindings: []string{"repo:hanzoai/cloud", "issue:1010"},
		})
		if err != nil {
			t.Fatalf("bindChannel: %v", err)
		}
		out := one.Channel
		if out.Life != lifeBound {
			t.Errorf("kind = %q, want %q", out.Life, lifeBound)
		}
		if len(out.Bindings) != 2 || out.Bindings[0] != "repo:hanzoai/cloud" || out.Bindings[1] != "issue:1010" {
			t.Errorf("bindings = %#v, want the two stated", out.Bindings)
		}
		// Read it again through the LIST op, which reads the store rather than the
		// value bindChannel just returned — this is what proves it was persisted and
		// not merely echoed.
		got, err := b.listChannels(ctx, nil)
		if err != nil {
			t.Fatalf("listChannels: %v", err)
		}
		if got.Channels[0].Life != lifeBound || len(got.Channels[0].Bindings) != 2 {
			t.Fatalf("re-read = %+v, want the facet the bind wrote", got.Channels[0])
		}
	})
}

// TestBindChannelSurvivesASubsequentClientWrite is the reason the facet is a
// MIXIN rather than a table beside the document. The Team client goes on writing
// to the same channel; txUpdate MERGES operations into the stored document, so a
// rename must not take the work facet with it.
func TestBindChannelSurvivesASubsequentClientWrite(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, sess, ws := channelFixture(t, "acme", "bugfix-1010")
		ctx := orgCtx(t, "acme")

		if _, err := b.bindChannel(ctx, &teamChannelBind{
			ID: "ch-bugfix-1010", Space: ws, Life: lifeBound, Bindings: []string{"issue:1010"},
		}); err != nil {
			t.Fatalf("bindChannel: %v", err)
		}
		// A client renames the channel and archives it — an ordinary TxUpdateDoc, the
		// shape the SPA sends.
		raw, err := json.Marshal(map[string]any{
			"_class":     clTxUpdate,
			"objectId":   "ch-bugfix-1010",
			"operations": map[string]any{"name": "bugfix-1010-done", "archived": true},
			"modifiedBy": acctSystem,
			"modifiedOn": time.Now().UnixMilli(),
		})
		if err != nil {
			t.Fatalf("marshal update: %v", err)
		}
		sess.applyTx(raw)

		got, err := b.listChannels(ctx, nil)
		if err != nil {
			t.Fatalf("listChannels: %v", err)
		}
		c := got.Channels[0]
		if c.Name != "bugfix-1010-done" {
			t.Errorf("name = %q, want the client's rename to have landed", c.Name)
		}
		// ARCHIVED IS THE PLATFORM'S, and this is the assertion that says so: the
		// client set it and the facet never held it, so there is one answer.
		if !c.Archived {
			t.Error("archived = false, want the client's archive to be what this surface reports")
		}
		if c.Life != lifeBound || len(c.Bindings) != 1 {
			t.Errorf("facet = (%q,%#v), want it untouched by an unrelated client write", c.Life, c.Bindings)
		}
	})
}

// TestBindChannelRefusesAnotherOrgsSpace is the tenancy gate, and the FIRST
// version of this test passed with the gate deleted — worth recording, because
// the reason is the same one that makes the gate worth having.
//
// Cross-tenant ACCESS is already impossible without any check here: the store
// keys every file on orgns.OrgNamespace(org, space) and the org comes from
// the validated principal, so a caller naming another org's space uuid opens
// a DIFFERENT file — an empty one — and gets a 404 either way. Asserting only the
// 404 therefore measures the store's physical isolation, not this gate.
//
// What the gate uniquely buys is that the empty file is never CREATED. cek.Open
// materialises on first use, so without it any caller could name arbitrary
// space uuids and mint one encrypted database per guess, on a shared volume,
// unbounded. So the assertion is on the open-handle cache: a refused pair must
// leave no store behind.
func TestBindChannelRefusesAnotherOrgsSpace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, ws := channelFixture(t, "acme", "bugfix-1010")

		// A caller validated as a DIFFERENT org names acme's space verbatim.
		_, err := b.bindChannel(orgCtx(t, "evil"), &teamChannelBind{
			ID: "ch-bugfix-1010", Space: ws, Life: lifeBound,
		})
		if err == nil {
			t.Fatal("bindChannel admitted a caller from another org")
		}
		// 404 and not 403: a caller who may not touch this space must not learn
		// from the status that it exists.
		if got := zipStatus(err); got != 404 {
			t.Errorf("status = %d, want 404 (a refusal must not be an existence oracle)", got)
		}
		// THE ASSERTION THAT BITES: the refusal opened no store for the unowned pair.
		evil, err := principal.OrgNamespace("evil", ws)
		if err != nil {
			t.Fatalf("namespace: %v", err)
		}
		b.trans.store.mu.Lock()
		_, opened := b.trans.store.dbs[evil]
		b.trans.store.mu.Unlock()
		if opened {
			t.Error("a refused space was opened as a store — an unbounded file-creation path")
		}
		// And acme's document is untouched.
		got, err := b.listChannels(orgCtx(t, "acme"), nil)
		if err != nil {
			t.Fatalf("listChannels: %v", err)
		}
		if got.Channels[0].Life != lifeStanding {
			t.Error("the refused write landed anyway")
		}
	})
}

// TestBindChannelRefusesAnUnreadableLife keeps the vocabulary closed. HIP-0523 §2
// names exactly two lives; a third value stored here would be one every future
// reader has to interpret.
func TestBindChannelRefusesAnUnreadableLife(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, ws := channelFixture(t, "acme", "bugfix-1010")
		if _, err := b.bindChannel(orgCtx(t, "acme"), &teamChannelBind{
			ID: "ch-bugfix-1010", Space: ws, Life: "ephemeral",
		}); err == nil {
			t.Fatal("bindChannel stored an unknown life")
		}
	})
}

// TestBindingsAreShapeCheckedAndNotResolved states the boundary exactly: a
// binding must be "<kind>:<ref>" so a reader can dispatch on the kind, and its
// REF is opaque here because the app that owns projects is the app that can say
// whether one exists.
func TestBindingsAreShapeCheckedAndNotResolved(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, ws := channelFixture(t, "acme", "bugfix-1010")
		ctx := orgCtx(t, "acme")

		for _, bad := range []string{"norefhere", "repo:", ":hanzoai/cloud"} {
			if _, err := b.bindChannel(ctx, &teamChannelBind{
				ID: "ch-bugfix-1010", Space: ws, Bindings: []string{bad},
			}); err == nil {
				t.Errorf("binding %q was accepted, want a refusal", bad)
			}
		}
		// A ref naming a project that does not exist is FINE — team does not resolve
		// it, and refusing here would put this app in the business of knowing every
		// other plane's nouns.
		if _, err := b.bindChannel(ctx, &teamChannelBind{
			ID: "ch-bugfix-1010", Space: ws, Bindings: []string{"project:no-such-project"},
		}); err != nil {
			t.Errorf("an unresolvable ref was refused: %v — shape is checked, existence is not", err)
		}
	})
}

// TestBindingsReplaceRatherThanMerge pins the one semantic a caller has to know:
// an explicit list REPLACES, so a wrong binding can be corrected and an empty
// list unbinds — while an ABSENT list leaves the channel alone, so a caller that
// only sets the kind does not silently erase what the channel is about.
func TestBindingsReplaceRatherThanMerge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, ws := channelFixture(t, "acme", "bugfix-1010")
		ctx := orgCtx(t, "acme")
		bind := func(in *teamChannelBind) *teamChannel {
			t.Helper()
			in.ID, in.Space = "ch-bugfix-1010", ws
			out, err := b.bindChannel(ctx, in)
			if err != nil {
				t.Fatalf("bindChannel: %v", err)
			}
			return &out.Channel
		}

		bind(&teamChannelBind{Bindings: []string{"repo:a", "repo:b"}})

		// Absent (nil) leaves them alone.
		if got := bind(&teamChannelBind{Life: lifeBound}); len(got.Bindings) != 2 {
			t.Fatalf("bindings = %#v after a kind-only write, want them untouched", got.Bindings)
		}
		// A stated list replaces.
		if got := bind(&teamChannelBind{Bindings: []string{"repo:c"}}); len(got.Bindings) != 1 || got.Bindings[0] != "repo:c" {
			t.Fatalf("bindings = %#v, want the stated list to have replaced", got.Bindings)
		}
		// An explicit empty list unbinds — and the kind survives it.
		got := bind(&teamChannelBind{Bindings: []string{}})
		if len(got.Bindings) != 0 {
			t.Fatalf("bindings = %#v, want an explicit empty list to unbind", got.Bindings)
		}
		if got.Life != lifeBound {
			t.Errorf("kind = %q, want unbinding to leave the lifecycle intent alone", got.Life)
		}
	})
}

// TestDirectMessagesAreChannels is the taxonomy, held as a test: a channel between
// people is a channel with no name, not a different kind of thing, so one bind op
// works on either and the kind is DERIVED from the class.
//
// And it is the privacy half of the same fact: a direct message belongs to the
// people in it. The org lane — a validated principal with no team account,
// which names no person — reads the space's public channels and never a
// conversation between particular people. The member lane that DOES read it is
// TestChannelsListIsWhatTheCallerMaySee (channels_http_test.go).
func TestDirectMessagesAreChannels(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, sess, ws := channelFixture(t, "acme", "general")
		ctx := orgCtx(t, "acme")

		raw, err := json.Marshal(map[string]any{
			"_class":      clTxCreate,
			"objectId":    "dm-1",
			"objectClass": clDirectMessage,
			"objectSpace": "core:space:Space",
			"attributes":  map[string]any{"members": []any{"u-1", "u-2"}, "private": true},
			"modifiedBy":  acctSystem,
			"modifiedOn":  time.Now().UnixMilli(),
		})
		if err != nil {
			t.Fatalf("marshal dm: %v", err)
		}
		sess.applyTx(raw)

		got, err := b.listChannels(ctx, nil)
		if err != nil {
			t.Fatalf("listChannels: %v", err)
		}
		if len(got.Channels) != 1 || got.Channels[0].Name != "general" || got.Channels[0].Kind != kindPublic {
			t.Fatalf("org lane channels = %+v, want the public channel alone — a DM is its members' to read", got.Channels)
		}
		dm, err := b.trans.store.get("acme", ws, "dm-1")
		if err != nil || dm == nil {
			t.Fatalf("dm: %v", err)
		}
		if v := channelOf(ws, dm); !v.Direct || !v.Private || v.Kind != kindDM || len(v.Members) != 2 {
			t.Errorf("dm = %+v, want direct+private, kind dm, with both members", v)
		}
		// And the org lane cannot write its facet: a DM is its people's, and an
		// application binds public channels only — the same 404 a missing channel gets.
		if _, err := b.bindChannel(ctx, &teamChannelBind{ID: "dm-1", Space: ws, Life: lifeBound}); zipStatus(err) != http.StatusNotFound {
			t.Errorf("the org lane bound a direct message: %v, want 404", err)
		}
	})
}

// TestOrgLaneIsForApplicationsOnly: a validated PERSON with no team session
// is refused (401) on every org-lane read and write; the same org as an
// application reads its public channels.
func TestOrgLaneIsForApplicationsOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, ws := channelFixture(t, "acme", "general")
		person := boundCtx(t, map[string]string{"X-User-Id": "u-9", "X-Org-Id": "acme"})
		if _, err := b.listChannels(person, nil); zipStatus(err) != http.StatusUnauthorized {
			t.Errorf("a person with no team session listed channels: %v, want 401", err)
		}
		if _, err := b.bindChannel(person, &teamChannelBind{ID: "ch-general", Space: ws, Life: lifeBound}); zipStatus(err) != http.StatusUnauthorized {
			t.Errorf("a person with no team session bound a channel: %v, want 401", err)
		}
		if _, err := b.openChannel(person, &teamChannelNew{Name: "x", Space: ws}); zipStatus(err) != http.StatusUnauthorized {
			t.Errorf("a person with no team session opened a channel: %v, want 401", err)
		}
		if _, err := b.listMessages(person, &teamMessageRead{ID: "ch-general", Space: ws}); zipStatus(err) != http.StatusUnauthorized {
			t.Errorf("a person with no team session read messages: %v, want 401", err)
		}
		got, err := b.listChannels(orgCtx(t, "acme"), nil)
		if err != nil || len(got.Channels) != 1 {
			t.Fatalf("the application lane = %+v, %v, want the public channel", got, err)
		}
	})
}

// orgCtx builds the context a typed op receives from an APPLICATION of `org`
// — an org's own machine credential, the one caller the org lane admits. It is
// the request the identity boundary would have minted (a validated user claim,
// the org, and the app kind), bound through the SAME cloud.Bridge a served
// route runs, so tenancy and kind resolve exactly as they do on the wire.
func orgCtx(t *testing.T, org string) context.Context {
	t.Helper()
	return boundCtx(t, map[string]string{"X-User-Id": "app-" + org, "X-Org-Id": org, "X-User-IsApp": "true"})
}

// boundCtx is a typed op's context for a request carrying these headers: a
// real request through cloud.Bridge, held OPEN until the test ends so the
// request the context names is never recycled underneath it.
func boundCtx(t *testing.T, headers map[string]string) context.Context {
	t.Helper()
	ready, done := make(chan context.Context), make(chan struct{})
	app := zip.New(zip.Config{})
	app.Use(cloud.Bridge())
	app.Raw(http.MethodGet, "/", func(c *zip.Ctx) error {
		ready <- c.Context()
		<-done
		return nil
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	go func() { _, _ = app.Test(req, zip.TestConfig{Timeout: time.Hour}) }()
	t.Cleanup(func() { close(done) })
	return <-ready
}

// zipStatus reads the HTTP status off a refusal. A refusal's STATUS is part of
// its contract here — 404-not-403 on the tenancy gate is the whole point of that
// assertion — so a test that only checked for non-nil would miss the property.
func zipStatus(err error) int {
	if he, ok := errors.AsType[*zip.HTTPError](err); ok {
		return he.Status
	}
	return 0
}

// A channel document written before HIP-0523 §2.1 carries its work facet under
// mixinRenamed. The first open of its space moves it to mixinChannel and leaves
// the facet's value as it was; a message in the same space is not touched, and a
// second open finds nothing left to move.
func TestTheWorkFacetMovesToItsChannelKeyOnOpen(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir() // each handle is closed before the next opens, so either codec serves it
		const org, space = "acme", "ws-facet"
		old := newStore(dir)
		work := map[string]any{"life": lifeBound, "bindings": []any{"issue:1010"}}
		if err := old.put(org, space, map[string]any{"_id": "ch-1", "_class": clChannel, "space": spaceChannels, "name": "bugfix-1010", mixinRenamed: work}); err != nil {
			t.Fatal(err)
		}
		if err := old.put(org, space, map[string]any{"_id": "msg-1", "_class": "chunter:class:ChatMessage", "space": "ch-1", mixinRenamed: "a message is not a channel"}); err != nil {
			t.Fatal(err)
		}
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}

		for open := 1; open <= 2; open++ {
			st := newStore(dir)
			doc, err := st.get(org, space, "ch-1")
			if err != nil || doc == nil {
				t.Fatalf("open %d: channel = %v, %v", open, doc, err)
			}
			if _, ok := doc[mixinRenamed]; ok {
				t.Fatalf("open %d: the facet is still under %s: %v", open, mixinRenamed, doc)
			}
			got, _ := doc[mixinChannel].(map[string]any)
			if got["life"] != lifeBound || len(toStringSlice(got["bindings"])) != 1 {
				t.Fatalf("open %d: facet under %s = %v, want %v", open, mixinChannel, doc[mixinChannel], work)
			}
			msg, err := st.get(org, space, "msg-1")
			if err != nil || msg[mixinRenamed] != "a message is not a channel" {
				t.Fatalf("open %d: a message was rewritten: %v, %v", open, msg, err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
		}
	})
}
