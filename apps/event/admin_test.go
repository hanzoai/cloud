// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

// One person's history reads across every tenant, which no other read on this
// plane may do — so the gate is asserted first, then who the person is matched
// by, and then that a recording plays back block by block from where the replay
// pipeline wrote it.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/s2"
	"github.com/zap-proto/zip"
)

var superHdr = map[string]string{"X-User-Id": "u-z", "X-User-Name": "z", "X-Org-Id": "admin", "X-User-Owner": "admin", "X-User-IsAdmin": "true"}

// store answers the reads a request makes, by the statement's shape, and keeps
// every statement and its args.
type store struct {
	linked, history, visits, events, recordings, blocks []map[string]any
	fail                                                string // a FROM target to fail on
	asked                                               []string
	args                                                [][]any
}

func (p *store) query(_ context.Context, sql string, args ...any) ([]map[string]any, error) {
	p.asked, p.args = append(p.asked, sql), append(p.args, args)
	if p.fail != "" && strings.Contains(sql, p.fail) {
		return nil, errors.New("down")
	}
	switch {
	case strings.Contains(sql, "GROUP BY anonymous_id"):
		return p.linked, nil
	case strings.Contains(sql, "groupArrayArray(block_first_timestamps)"):
		return p.blocks, nil
	case strings.Contains(sql, replayTable):
		return p.recordings, nil
	case strings.Contains(sql, "GROUP BY session_id"):
		return p.visits, nil
	case strings.Contains(sql, "ORDER BY time, org, id"):
		return p.events, nil
	}
	return p.history, nil
}

// adminPlane mounts the app over a plane and an IAM that knows acme/ada.
func adminPlane(t *testing.T, p *store, iamErr error) *zip.App {
	t.Helper()
	origReady, origQuery, origAccount, origRead := warehouseReady, warehouseQuery, account, readBlock
	warehouseReady = func() bool { return true }
	warehouseQuery = p.query
	account = func(_ *zip.Ctx, owner, name string) (string, string, error) {
		if iamErr != nil {
			return "", "", iamErr
		}
		if owner == "acme" && name == "ada" {
			return "u-ada", "ada@acme.test", nil
		}
		return "", "", errors.New("iam: not found")
	}
	t.Cleanup(func() {
		warehouseReady, warehouseQuery, account, readBlock = origReady, origQuery, origAccount, origRead
	})
	return mountApp(t)
}

func getJSON[T any](t *testing.T, app *zip.App, path string, hdr map[string]string) (int, T) {
	t.Helper()
	var out T
	code, body := send(t, app, http.MethodGet, path, hdr, "")
	if code == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("GET %s: %v (%s)", path, err, body)
		}
	}
	return code, out
}

func TestPersonHistoryIsSuperAdminOnly(t *testing.T) {
	p := &store{}
	app := adminPlane(t, p, nil)
	callers := map[string]map[string]string{
		"anon":     nil,
		"member":   {"X-Org-Id": "acme", "X-User-Id": "u-ada"},
		"orgadmin": {"X-Org-Id": "acme", "X-User-Id": "u-ada", "X-User-IsOrgAdmin": "true"},
		// The impersonated target is no SuperAdmin, whoever is at the keyboard.
		"operator as ada": {"X-Org-Id": "acme", "X-User-Id": "u-ada", zip.HeaderActedBy: "admin/z"},
		// And an impersonation carrying admin bits may not read this either.
		"operator as an admin": {"X-Org-Id": "admin", "X-User-Id": "u-root", "X-User-Owner": "admin", "X-User-IsAdmin": "true", zip.HeaderActedBy: "admin/z"},
	}
	for name, hdr := range callers {
		for _, path := range []string{"/v1/admin/event/acme/ada", "/v1/admin/event/acme/ada/sessions", "/v1/admin/event/acme/ada/sessions/s-1"} {
			if code, body := send(t, app, http.MethodGet, path, hdr, ""); code != http.StatusForbidden {
				t.Errorf("%s GET %s = %d (%s), want 403", name, path, code, body)
			}
		}
	}
	if len(p.asked) != 0 {
		t.Fatalf("a refused caller reached the warehouse: %v", p.asked)
	}
}

// The person is matched by every id the plane files them under — the IAM id, the
// <owner>/<name> spelling, and the visitor ids their identify rows link — on all
// three id columns, across every tenant, newest first, with the operator's rows
// marked.
func TestPersonHistoryMatchesEveryIdOfThePerson(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 123456789, time.UTC)
	p := &store{
		linked: []map[string]any{{"anonymous_id": "anon-1"}, {"anonymous_id": "u-ada"}},
		history: []map[string]any{
			{"org": "hanzo", "id": "e2", "time": at, "signal": "act", "name": "page_viewed", "kind": "page", "distinct_id": "u-ada", "session_id": "s-1",
				"path": "/pricing", "url": "https://hanzo.ai/reset?token=abcdefghijkl", "operated": "true", "operator": "admin/z", "verified": "u-ada"},
			{"org": "acme", "id": "e1", "time": at.Add(-time.Minute), "signal": "error", "name": "error", "distinct_id": "anon-1", "message": "boom"},
		},
	}
	app := adminPlane(t, p, nil)
	code, got := getJSON[historyOut](t, app, "/v1/admin/event/acme/ada?limit=2", superHdr)
	if code != http.StatusOK || got.Status != "ok" {
		t.Fatalf("GET = %d %+v", code, got)
	}
	if want := []string{"u-ada", "acme/ada", "anon-1"}; !slices.Equal(got.Data.Person.IDs, want) {
		t.Errorf("ids = %v, want %v", got.Data.Person.IDs, want)
	}
	if want := []string{"u-ada", "acme/ada"}; !slices.Equal(got.Data.Person.Keys, want) || got.Data.Person.Partial {
		t.Errorf("keys = %v partial %v, want %v and whole", got.Data.Person.Keys, got.Data.Person.Partial, want)
	}
	if got.Data.Person.ID != "u-ada" || got.Data.Person.Email != "ada@acme.test" {
		t.Errorf("person = %+v, want IAM's id and email", got.Data.Person)
	}
	ev := got.Data.Events
	if len(ev) != 2 || ev[0].Org != "hanzo" || ev[1].Org != "acme" || ev[1].Signal != "error" || ev[1].Message != "boom" {
		t.Fatalf("events = %+v", ev)
	}
	if !ev[0].Impersonated || ev[0].ImpersonatedBy != "admin/z" || ev[1].Impersonated {
		t.Errorf("operator marks = %+v / %+v", ev[0], ev[1])
	}
	if !ev[0].Attested || ev[1].Attested {
		t.Errorf("attested = %v / %v, want only the verified write", ev[0].Attested, ev[1].Attested)
	}
	if strings.Contains(ev[0].URL, "abcdefghijkl") {
		t.Errorf("url %q carries a credential", ev[0].URL)
	}
	if ev[0].Timestamp != "2026-10-06T12:00:00.123456789Z" {
		t.Errorf("timestamp %q lost the plane's precision", ev[0].Timestamp)
	}
	sql, args := p.asked[1], p.args[1]
	if !strings.Contains(sql, "person_id IN (?, ?, ?) OR distinct_id IN (?, ?, ?) OR anonymous_id IN (?, ?, ?)") {
		t.Errorf("history must match every id on every id column: %q", sql)
	}
	if strings.Contains(sql, "org = ?") || strings.Contains(sql, "anon-1") {
		t.Errorf("history must span tenants and bind every id: %q", sql)
	}
	if len(args) != 4+9 {
		t.Errorf("args = %v, want signal pair, window and nine ids", args)
	}
	if got.Next == "" {
		t.Fatal("a full page must carry next")
	}

	// The next page continues strictly after the last row, at nanosecond precision.
	code, _ = getJSON[historyOut](t, app, "/v1/admin/event/acme/ada?limit=2&tenant=hanzo&cursor="+got.Next, superHdr)
	if code != http.StatusOK {
		t.Fatalf("page 2 = %d", code)
	}
	sql, args = p.asked[len(p.asked)-1], p.args[len(p.args)-1]
	if !strings.Contains(sql, "AND org = ?") || !strings.Contains(sql, "(time, org, id) < (toDateTime64(?, 9, 'UTC'), ?, ?)") {
		t.Errorf("page 2 = %q, want the tenant and the keyset", sql)
	}
	if tail := args[len(args)-4:]; tail[0] != "hanzo" || tail[1] != "2026-10-06 11:59:00.123456789" || tail[2] != "acme" || tail[3] != "e1" {
		t.Errorf("page 2 binds %v, want tenant then the last row's time, org and id", tail)
	}

	if code, _ := getJSON[historyOut](t, app, "/v1/admin/event/acme/ada?cursor=bogus", superHdr); code != http.StatusBadRequest {
		t.Errorf("a cursor this read never issued = %d, want 400", code)
	}
	if code, _ := getJSON[historyOut](t, app, "/v1/admin/event/acme/ada?range=forever", superHdr); code != http.StatusBadRequest {
		t.Errorf("an unknown range = %d, want 400", code)
	}
}

// An IAM that does not answer leaves the person matched by <owner>/<name> and the
// visitors linked to it, and the answer says which source is missing.
func TestPersonHistoryNamesAMissingIAM(t *testing.T) {
	p := &store{linked: []map[string]any{{"anonymous_id": "anon-1"}}}
	app := adminPlane(t, p, errors.New("iam unreachable"))
	code, got := getJSON[historyOut](t, app, "/v1/admin/event/acme/ada", superHdr)
	if code != http.StatusOK || got.Status != "ok" {
		t.Fatalf("GET = %d %+v", code, got)
	}
	if !slices.Equal(got.Degraded, []string{"iam"}) {
		t.Errorf("degraded = %v, want [iam]", got.Degraded)
	}
	if want := []string{"acme/ada", "anon-1"}; !slices.Equal(got.Data.Person.IDs, want) {
		t.Errorf("ids = %v, want %v", got.Data.Person.IDs, want)
	}
	if got.Data.Events == nil {
		t.Error("events must be empty, never null")
	}

	p.fail = factTable
	code, got = getJSON[historyOut](t, app, "/v1/admin/event/acme/ada", superHdr)
	if code != http.StatusOK || got.Status != "error" || got.Data != nil {
		t.Errorf("a warehouse failure = %d %+v, want the error envelope", code, got)
	}
}

// A caller that holds the account's id already (the console opened it from the
// directory) names it, and IAM is not asked: the history matches that id, the
// spelling <owner>/<name> and the visitors linked to either, with nothing degraded.
func TestPersonHistoryTakesTheIdTheCallerHolds(t *testing.T) {
	p := &store{linked: []map[string]any{{"anonymous_id": "anon-1"}}}
	app := adminPlane(t, p, errors.New("iam refuses this bearer"))
	code, got := getJSON[historyOut](t, app, "/v1/admin/event/acme/ada?id=u-ada", superHdr)
	if code != http.StatusOK || got.Status != "ok" {
		t.Fatalf("GET = %d %+v", code, got)
	}
	if len(got.Degraded) != 0 {
		t.Errorf("degraded = %v, want none: IAM was not asked", got.Degraded)
	}
	if want := []string{"u-ada", "acme/ada", "anon-1"}; !slices.Equal(got.Data.Person.IDs, want) {
		t.Errorf("ids = %v, want %v", got.Data.Person.IDs, want)
	}
	code, gotS := getJSON[sessionsOut](t, app, "/v1/admin/event/acme/ada/sessions?id=u-ada", superHdr)
	if code != http.StatusOK || len(gotS.Degraded) != 0 {
		t.Errorf("sessions with an id = %d degraded %v, want none", code, gotS.Degraded)
	}
}

// Visits carry their recording, matched by session AND by the visitor the recorder
// filed it under, with the pipeline's own first url redacted at the read.
func TestPersonSessionsCarryTheirRecording(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	p := &store{
		linked: []map[string]any{{"anonymous_id": "anon-1"}},
		visits: []map[string]any{
			{"session_id": "s-2", "orgs": []string{"hanzo", "acme"}, "started": at, "ended": at.Add(9 * time.Minute), "events": uint64(14), "pages": uint64(5), "surface": "site", "entry": "https://hanzo.ai/", "operated": uint8(0)},
			{"session_id": "s-1", "orgs": []string{"hanzo"}, "started": at.Add(-time.Hour), "ended": at.Add(-50 * time.Minute), "events": uint64(3), "pages": uint64(3), "operated": uint8(1)},
		},
		recordings: []map[string]any{{"session_id": "s-2", "project": int64(1), "started": at, "ended": at.Add(9 * time.Minute),
			"active": int64(40000), "clicks": int64(12), "keypresses": int64(30), "bytes": int64(90000), "blocks": uint64(2),
			"entry": "https://hanzo.ai/?token=abcdefghijkl"}},
	}
	app := adminPlane(t, p, nil)
	code, got := getJSON[sessionsOut](t, app, "/v1/admin/event/acme/ada/sessions?range=7d", superHdr)
	if code != http.StatusOK || got.Status != "ok" || len(got.Data) != 2 {
		t.Fatalf("GET = %d %+v", code, got)
	}
	late, early := got.Data[0], got.Data[1]
	if !slices.Equal(late.Orgs, []string{"acme", "hanzo"}) || late.Events != 14 || late.Pages != 5 || late.Impersonated {
		t.Errorf("visit s-2 = %+v", late)
	}
	if !early.Impersonated || early.Recording != nil {
		t.Errorf("visit s-1 = %+v, want operated and unrecorded", early)
	}
	r := late.Recording
	if r == nil || r.Blocks != 2 || r.Clicks != 12 || r.Active != 40000 {
		t.Fatalf("recording = %+v", r)
	}
	if strings.Contains(r.URL, "abcdefghijkl") {
		t.Errorf("recording url %q carries a credential", r.URL)
	}
	rec := p.asked[len(p.asked)-1]
	if !strings.Contains(rec, "session_id IN (?, ?)") || !strings.Contains(rec, "distinct_id IN (?, ?, ?)") || !strings.Contains(rec, "max(is_deleted) = 0") {
		t.Errorf("recordings must match session and visitor, live only: %q", rec)
	}

	p.fail = replayTable
	_, got = getJSON[sessionsOut](t, app, "/v1/admin/event/acme/ada/sessions", superHdr)
	if got.Status != "ok" || !slices.Equal(got.Degraded, []string{"replay"}) || got.Data[0].Recording != nil {
		t.Errorf("an unreadable replay table = %+v, want visits with degraded [replay]", got)
	}
}

// encodeBlock encodes rrweb frames the way the replay ingester stores them.
func encodeBlock(t *testing.T, frames ...string) []byte {
	t.Helper()
	var b strings.Builder
	for _, f := range frames {
		b.WriteString(`["w-1",` + f + "]\n")
	}
	return s2.EncodeSnappy(nil, []byte(b.String()))
}

// A recorded visit plays back block by block, in order, from the replay bucket
// alone; a block that will not decode is reported and skipped, never guessed at.
func TestPlaybackReadsTheRecordingBlockByBlock(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	p := &store{
		visits: []map[string]any{{"session_id": "s-1", "orgs": []string{"hanzo"}, "started": at, "ended": at.Add(time.Minute), "events": uint64(2), "pages": uint64(1)}},
		events: []map[string]any{
			{"org": "hanzo", "id": "e1", "time": at, "signal": "act", "name": "page_viewed", "kind": "page", "session_id": "s-1", "distinct_id": "u-ada"},
		},
		recordings: []map[string]any{{"session_id": "s-1", "project": int64(1), "started": at, "ended": at.Add(time.Minute), "blocks": uint64(4)}},
		blocks: []map[string]any{{
			// Out of order on purpose: playback sorts by each block's first frame.
			"starts": []time.Time{at.Add(30 * time.Second), at, at.Add(40 * time.Second), at.Add(50 * time.Second)},
			"ends":   []time.Time{at.Add(40 * time.Second), at.Add(30 * time.Second), at.Add(50 * time.Second), at.Add(time.Minute)},
			"urls": []string{
				"s3://" + replayBucket + "/session_recordings/30d/b?range=bytes=10-19",
				"s3://" + replayBucket + "/session_recordings/30d/a?range=bytes=0-9",
				"s3://someone-elses/private/object?range=bytes=0-9",
				"s3://" + replayBucket + "/session_recordings/30d/c?range=bytes=20-29",
			},
		}},
	}
	app := adminPlane(t, p, nil)
	served := map[string][]byte{
		"session_recordings/30d/a": encodeBlock(t, `{"type":4,"timestamp":1}`, `{"type":2,"timestamp":2}`),
		"session_recordings/30d/b": encodeBlock(t, `{"type":3,"timestamp":3}`),
		"session_recordings/30d/c": []byte("sealed bytes, not snappy"),
	}
	var read []string
	readBlock = func(_ context.Context, bucket, key string, first, last int64) ([]byte, error) {
		read = append(read, bucket+"/"+key)
		return served[key], nil
	}
	code, got := getJSON[playbackOut](t, app, "/v1/admin/event/acme/ada/sessions/s-1", superHdr)
	if code != http.StatusOK || got.Status != "ok" {
		t.Fatalf("GET = %d %+v", code, got)
	}
	d := got.Data
	if d.Session.Recording == nil || len(d.Events) != 1 {
		t.Fatalf("visit = %+v", d)
	}
	var ts []int
	for _, f := range d.Frames {
		var e struct{ Timestamp int }
		_ = json.Unmarshal(f.Event, &e)
		ts = append(ts, e.Timestamp)
		if f.Window != "w-1" {
			t.Errorf("frame window %q, want w-1", f.Window)
		}
	}
	if !slices.Equal(ts, []int{1, 2, 3}) {
		t.Errorf("frames %v, want 1 2 3 in recording order", ts)
	}
	if len(d.Blocks) != 4 || d.Blocks[0].Frames != 2 || d.Blocks[1].Frames != 1 ||
		!strings.HasPrefix(d.Blocks[2].Error, "refused") || !strings.HasPrefix(d.Blocks[3].Error, "sealed") {
		t.Errorf("blocks = %+v", d.Blocks)
	}
	for _, r := range read {
		if !strings.HasPrefix(r, replayBucket+"/") {
			t.Errorf("read %q outside the replay bucket", r)
		}
	}
}

// A session the person sent nothing under is not theirs to replay.
func TestPlaybackIsTheirsOnly(t *testing.T) {
	app := adminPlane(t, &store{}, nil)
	if code, body := send(t, app, http.MethodGet, "/v1/admin/event/acme/ada/sessions/s-9", superHdr, ""); code != http.StatusNotFound {
		t.Errorf("someone else's session = %d (%s), want 404", code, body)
	}
	if code, body := send(t, app, http.MethodGet, "/v1/admin/event/acme/ada/sessions/not%20a%20session", superHdr, ""); code != http.StatusBadRequest {
		t.Errorf("a malformed session = %d (%s), want 400", code, body)
	}
}

func TestBlockAddress(t *testing.T) {
	b, k, lo, hi, err := blockAddress("s3://hanzo-sessions/session_recordings/30d/170-ab?range=bytes=0-489")
	if err != nil || b != "hanzo-sessions" || k != "session_recordings/30d/170-ab" || lo != 0 || hi != 489 {
		t.Fatalf("parsed %q %q %d %d %v", b, k, lo, hi, err)
	}
	for _, bad := range []string{
		"https://hanzo-sessions/k?range=bytes=0-1",
		"s3://hanzo-sessions/k",
		"s3://hanzo-sessions/k?range=bytes=9-1",
		"s3://hanzo-sessions/k?range=bytes=0-99999999999",
		"s3:///k?range=bytes=0-1",
	} {
		if _, _, _, _, err := blockAddress(bad); err == nil {
			t.Errorf("%q parsed as a block address", bad)
		}
	}
}

// Block arrays that do not pair up are refused, never cut to a guess.
func TestPlaybackRefusesMisalignedBlocks(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	p := &store{
		visits:     []map[string]any{{"session_id": "s-1", "orgs": []string{"hanzo"}, "started": at, "ended": at}},
		recordings: []map[string]any{{"session_id": "s-1", "started": at, "ended": at, "blocks": uint64(2)}},
		blocks: []map[string]any{{"starts": []time.Time{at}, "ends": []time.Time{at, at},
			"urls": []string{"s3://" + replayBucket + "/a?range=bytes=0-1", "s3://" + replayBucket + "/b?range=bytes=0-1"}}},
	}
	app := adminPlane(t, p, nil)
	readBlock = func(context.Context, string, string, int64, int64) ([]byte, error) {
		t.Fatal("a misaligned recording must not be read")
		return nil, nil
	}
	_, got := getJSON[playbackOut](t, app, "/v1/admin/event/acme/ada/sessions/s-1", superHdr)
	if got.Status != "ok" || !slices.Equal(got.Degraded, []string{"replay"}) || len(got.Data.Frames) != 0 {
		t.Fatalf("misaligned blocks = %+v, want no frames and degraded [replay]", got)
	}
	if !strings.Contains(p.asked[len(p.asked)-1], "arrayFilter(u -> u != '', groupArrayArray(block_urls))") {
		t.Errorf("block urls must drop the empty ones the pipeline writes: %q", p.asked[len(p.asked)-1])
	}
}

// Links a verified write made come first, and a cut is reported, not silent.
func TestPersonLinksAreBoundedAndSaySo(t *testing.T) {
	linked := make([]map[string]any, maxLinked+1)
	for i := range linked {
		linked[i] = map[string]any{"anonymous_id": "anon-" + itoa(i)}
	}
	p := &store{linked: linked}
	app := adminPlane(t, p, nil)
	_, got := getJSON[historyOut](t, app, "/v1/admin/event/acme/ada", superHdr)
	if !got.Data.Person.Partial || len(got.Data.Person.IDs) != 2+maxLinked {
		t.Fatalf("partial %v with %d ids, want partial and %d", got.Data.Person.Partial, len(got.Data.Person.IDs), 2+maxLinked)
	}
	if sql := p.asked[0]; !strings.Contains(sql, "ORDER BY verified DESC, seen DESC LIMIT 501") {
		t.Errorf("links must put verified writes first and ask one past the cap: %q", sql)
	}
}
