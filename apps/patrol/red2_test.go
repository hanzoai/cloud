package patrol

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
)

// red2_test.go — the camera ticket, the receiving centre's grant, and the feed
// replay.

// ---- R8: what a ticket is bound to ----

// TestATicketIsBoundToOneCameraAndOneMoment walks a minted ticket back: the
// camera it names, a camera it does not, a tag nobody signed, and a moment that
// has passed.
func TestATicketIsBoundToOneCameraAndOneMoment(t *testing.T) {
	v := newVault(map[string]string{
		"acme/patrol/camera/cam-1": "rtsp://admin:hunter2@cam1.depot.local/live",
		"acme/patrol/camera/cam-2": "rtsp://admin:hunter3@cam2.depot.local/live",
	})
	app := mountVault(t, v)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	for _, n := range []string{"cam-1", "cam-2"} {
		create(t, app, org, DTCamera, map[string]any{
			"name": n, "site": "depot", "label": n, "online": true,
			"secret": "acme/patrol/camera/" + n,
		})
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("mint want 200, got %d (%s)", code, first(raw, 200))
	}
	var got patrolStream
	data(t, raw, &got)
	u, err := url.Parse(got.URL)
	if err != nil {
		t.Fatalf("minted address: %v", err)
	}
	stamp, tag := u.Query().Get("expires"), u.Query().Get("ticket")

	if code, body := call(t, app, http.MethodGet,
		"/v1/patrol/camera/cam-1/stream/"+tag+"?expires="+stamp, org, controller, nil); code != http.StatusOK {
		t.Errorf("a live ticket for its own camera want 200, got %d (%s)", code, first(body, 200))
	}
	if code, body := call(t, app, http.MethodGet,
		"/v1/patrol/camera/cam-2/stream/"+tag+"?expires="+stamp, org, controller, nil); code != http.StatusNotFound {
		t.Errorf("cam-1's ticket opened cam-2 (%d): %s", code, first(body, 200))
	}
	past := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	if code, _ := call(t, app, http.MethodGet,
		"/v1/patrol/camera/cam-1/stream/"+tag+"?expires="+past, org, controller, nil); code != http.StatusNotFound {
		t.Errorf("a ticket presented under a moment it does not cover want 404, got %d", code)
	}
	if code, _ := call(t, app, http.MethodGet,
		"/v1/patrol/camera/cam-1/stream/"+strings.Repeat("0", len(tag))+"?expires="+stamp, org, controller,
		nil); code != http.StatusNotFound {
		t.Errorf("a made-up tag want 404, got %d", code)
	}
}

// TestTheTicketVerifierIsReachableByWhateverOpensTheCamera. The ticket exists so
// "whatever opens the camera can tell a live ticket from a made-up one". That
// reader is a relay, not a member of the operation — and the route it must ask
// admits only staff, which is a credential that can mint its own tickets. A
// check only the issuer can perform checks nothing.
func TestTheTicketVerifierIsReachableByWhateverOpensTheCamera(t *testing.T) {
	v := newVault(map[string]string{"acme/patrol/camera/cam-1": "rtsp://admin:hunter2@cam1.depot.local/live"})
	app := mountVault(t, v)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTCamera, map[string]any{
		"name": "cam-1", "site": "depot", "label": "Yard", "online": true,
		"secret": "acme/patrol/camera/cam-1"})
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("mint want 200, got %d", code)
	}
	var got patrolStream
	data(t, raw, &got)
	u, _ := url.Parse(got.URL)
	if u.User != nil || strings.Contains(got.URL, "hunter2") {
		t.Fatalf("the minted address carries the credential: %q", got.URL)
	}
	// The relay holds the ticket and nothing else.
	code, body := hit(t, app, http.MethodGet,
		"/v1/patrol/camera/cam-1/stream/"+u.Query().Get("ticket")+"?expires="+u.Query().Get("expires"),
		nil, map[string]string{})
	if code >= 400 {
		t.Errorf("the holder of a ticket cannot check it (%d: %s) — only a staff credential can, "+
			"and a staff credential mints its own", code, first(body, 200))
	}
}

// ---- R9: what the receiving centre's grant carries ----

// TestTheReceivingCentreReachesOnlyTheIntake. The intake is admitted by the
// control-room role, so the ARC's machine credential holds it. That role is not
// "may post an activation": it is the control room, and the control room reads
// the key register — where every customer's keys are kept and who signed for
// them — and opens any camera at any premises.
func TestTheReceivingCentreReachesOnlyTheIntake(t *testing.T) {
	v := newVault(map[string]string{"acme/patrol/camera/cam-1": "rtsp://admin:hunter2@cam1.depot.local/live"})
	app := mountVault(t, v)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTKey, map[string]any{
		"ref": "K-17", "site": "depot", "where": "safe 2, shelf 3", "holder": "gf-01"})
	create(t, app, org, DTCamera, map[string]any{
		"name": "cam-1", "site": "depot", "label": "Yard", "online": true,
		"secret": "acme/patrol/camera/cam-1"})

	// The centre's own credential. It exists to post activations.
	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, centre,
		map[string]any{"ref": "ARC-9", "account": "1001", "trigger": "Intruder"}); code != http.StatusCreated {
		t.Fatalf("the intake must admit the centre, got %d", code)
	}
	if code, raw := call(t, app, http.MethodGet, "/v1/patrol/key", org, centre, nil); code == http.StatusOK &&
		strings.Contains(string(raw), "safe 2, shelf 3") {
		t.Errorf("the receiving centre read the key register: %s", first(raw, 240))
	}
	if code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, centre, nil); code == http.StatusOK {
		t.Errorf("the receiving centre minted a way into a customer's camera: %s", first(raw, 240))
	}
	if code, raw := call(t, app, http.MethodGet, "/v1/patrol/note", org, centre, nil); code == http.StatusOK {
		t.Errorf("the receiving centre read the operation's internal notes (%d): %s", code, first(raw, 160))
	}
}

// ---- R10: the signing key is minted more than once ----

// TestTheStreamKeyIsMintedOnce. signing() reads, generates, writes, then reads
// back — and the read-back is what is claimed to settle a race. It does not: two
// callers that both miss both write, and each returns whatever its OWN read-back
// saw. A ticket minted under the loser's key verifies nowhere.
func TestTheStreamKeyIsMintedOnce(t *testing.T) {
	v := newVault(map[string]string{})
	o := ops{s: &cloud.Service[state]{KMS: v, Log: luxlog.New("test")}}
	const racers = 24
	var wg sync.WaitGroup
	keys := make([]string, racers)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k, err := o.signing(context.Background(), org)
			if err != nil {
				t.Errorf("signing: %v", err)
				return
			}
			keys[i] = string(k)
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k] = true
	}
	if len(seen) > 1 {
		t.Errorf("a first mint under load settled on %d different signing keys; every ticket signed "+
			"with any but the last verifies nowhere", len(seen))
	}
}

// ---- R11: the feed replay is trimmed, not narrowed ----

// TestAClientsFeedReplayIsNarrowedInTheQuery. ?since= replays the last
// feedBacklog rows of the WHOLE operation and then drops the ones that are not
// the caller's. It is the window bug seen() was written to close, still open on
// the one read a reconnecting screen makes.
func TestAClientsFeedReplayIsNarrowedInTheQuery(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	mark := time.Now().UTC().Add(-time.Minute).Format(stampLayout)

	// The customer's own row, then a busy hour of somebody else's.
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller, map[string]any{
		"level": LevelRed, "title": "Intruder at theirs", "site": "theirs"}); code != http.StatusCreated {
		t.Fatalf("seed the customer's row: %d %s", code, first(raw, 160))
	}
	for i := range feedBacklog {
		if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller, map[string]any{
			"level": LevelCyan, "title": "routine " + strconv.Itoa(i), "site": "rivals"}); code != http.StatusCreated {
			t.Fatalf("seed the operation's rows: %d %s", code, first(raw, 160))
		}
	}

	r := reach{client: "northside", sites: map[string]bool{"theirs": true}}
	o := ops{s: &cloud.Service[state]{Log: luxlog.New("test")}}
	replay := o.backlog(context.Background(), org, mark, r)
	if len(replay) == 0 {
		t.Errorf("a customer reconnecting with ?since= replayed none of their own rows: the window is "+
			"the whole operation's newest %d and theirs is behind them", feedBacklog)
	}
}
