package patrol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// camera_test.go asks what a caller learns when there is nothing to open. Every
// absence — no vault in this deployment, no credential registered, a row
// pointing somewhere that is not its own place, a credential that is not an
// address — is the same 404, so the route is no oracle on which cameras exist or
// on where their secrets are kept.

// wired mounts patrol over a vault and registers one camera at one site.
func wired(t *testing.T, v *vault, secret string) *zip.App {
	t.Helper()
	app := mountVault(t, v)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTCamera, map[string]any{
		"name": "cam-1", "site": "depot", "label": "Yard", "online": true, "secret": secret,
	})
	return app
}

// TestACameraWithNothingToOpenIsTheSameAbsence walks every way a mint can come
// up empty and proves they answer alike. A caller learns that this camera has no
// stream and nothing else — not which cameras have one, and not where a
// credential is kept.
func TestACameraWithNothingToOpenIsTheSameAbsence(t *testing.T) {
	mine := kept(org, "cam-1")
	for _, tc := range []struct {
		name   string
		secret string
		held   map[string]string
	}{
		{"no credential registered on the row", "", map[string]string{mine: "rtsp://admin:x@cam1/live"}},
		{"a row pointing somewhere that is not its own place", kept(org, "cam-2"),
			map[string]string{kept(org, "cam-2"): "rtsp://admin:x@cam1/live"}},
		{"a row pointing at its own place and nothing kept there", mine, map[string]string{}},
		{"a credential that is only whitespace", mine, map[string]string{mine: "   "}},
		{"a credential that is not an address", mine, map[string]string{mine: "hunter2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := wired(t, newVault(tc.held), tc.secret)
			code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, officer, nil)
			if code != http.StatusNotFound {
				t.Fatalf("want 404, got %d (%s)", code, first(raw, 200))
			}
			if strings.Contains(string(raw), "patrol/camera") || strings.Contains(string(raw), "hunter2") {
				t.Errorf("the refusal said where the credential is kept: %s", raw)
			}
		})
	}
}

// TestAMintNeedsAKeyItCanKeep proves the mint turns on the signing key as much
// as on the credential: a vault that will not keep one issues nothing, because a
// ticket nobody can verify opens nothing.
func TestAMintNeedsAKeyItCanKeep(t *testing.T) {
	mine := kept(org, "cam-1")
	v := newVault(map[string]string{mine: "rtsp://admin:hunter2@cam1.depot.local/live"})
	v.refuse = errors.New("vault is read-only")
	app := wired(t, v, mine)

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, officer, nil)
	if code != http.StatusNotFound {
		t.Fatalf("a mint with no key to sign with want 404, got %d (%s)", code, first(raw, 200))
	}
}

// TestAKeyThatCannotBeReadBackStillSignsThisMint proves what the mint does when
// the write lands and the read-back does not see it: it signs with the key it
// generated rather than refusing, so the caller is handed a ticket the same
// process will verify.
func TestAKeyThatCannotBeReadBackStillSignsThisMint(t *testing.T) {
	mine := kept(org, "cam-1")
	v := newVault(map[string]string{mine: "rtsp://admin:hunter2@cam1.depot.local/live"})
	v.swallow = true
	app := wired(t, v, mine)

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, officer, nil)
	if code != http.StatusOK {
		t.Fatalf("mint a stream want 200, got %d (%s)", code, first(raw, 200))
	}
	var got patrolStream
	data(t, raw, &got)
	if !strings.Contains(got.URL, "ticket=") || strings.Contains(got.URL, "hunter2") {
		t.Fatalf("the minted address must carry a ticket and not the credential: %q", got.URL)
	}
}

// TestASecondMintTakesTheKeyTheFirstWrote proves the key is minted once. A
// caller that found none and then found one — which is where the second caller
// stands while the first is writing — signs with what is there, because a ticket
// signed under a key that lost the race verifies nowhere.
func TestASecondMintTakesTheKeyTheFirstWrote(t *testing.T) {
	const existing = "the key the first caller wrote"
	v := newVault(map[string]string{keyRef(org): existing})
	v.missed = 1 // the read before the one that finds it
	o := ops{s: &cloud.Service[state]{Base: cloud.Base{KMS: v, Log: luxlog.New("test")}}}

	key, err := o.signing(context.Background(), org)
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	if string(key) != existing {
		t.Fatalf("the mint signed with %q, want the key already kept for the org", key)
	}
}

// TestNoVaultMeansNoKeyAndNoCredential proves a deployment with no vault has
// nothing to read and mints nothing: it answers the absence rather than
// generating a key it has nowhere to keep.
func TestNoVaultMeansNoKeyAndNoCredential(t *testing.T) {
	o := ops{s: &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}}
	ctx := context.Background()
	if _, err := o.held(ctx, org); !errors.Is(err, errNoStream) {
		t.Errorf("reading the signing key with no vault answered %v, want the absence", err)
	}
	if _, err := o.signing(ctx, org); !errors.Is(err, errNoStream) {
		t.Errorf("minting a signing key with no vault answered %v, want the absence", err)
	}
	if _, err := o.credential(ctx, org, "cam-1"); !errors.Is(err, errNoStream) {
		t.Errorf("reading a camera credential with no vault answered %v, want the absence", err)
	}
}

// TestAnEmptySigningKeyIsNoKey proves a vault answering an empty value at the
// key's own place is an absence and not a key: the mint writes a real one rather
// than signing every ticket with nothing.
func TestAnEmptySigningKeyIsNoKey(t *testing.T) {
	v := newVault(map[string]string{keyRef(org): ""})
	o := ops{s: &cloud.Service[state]{Base: cloud.Base{KMS: v, Log: luxlog.New("test")}}}
	if _, err := o.held(context.Background(), org); !errors.Is(err, errNoStream) {
		t.Fatalf("an empty value at the key's place answered %v, want the absence", err)
	}
	key, err := o.signing(context.Background(), org)
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	if len(key) == 0 {
		t.Fatal("the mint settled on an empty signing key")
	}
}

// TestACredentialThatIsNotAnAddressMintsNothing proves the one thing mint
// requires of what it was handed: somewhere to dial.
func TestACredentialThatIsNotAnAddressMintsNothing(t *testing.T) {
	for _, credential := range []string{"hunter2", "rtsp://", "://cam1/live", ""} {
		if _, ok := mint(credential, []byte("k"), org, "cam-1", time.Now()); ok {
			t.Errorf("%q was minted into an address", credential)
		}
	}
	if _, ok := mint("rtsp://admin:x@cam1.depot.local:554/live", []byte("k"), org, "cam-1", time.Now()); !ok {
		t.Error("a credential that names a host was not minted")
	}
}

// TestATicketThatDoesNotVerifyIsNotThere walks the ways a presented ticket comes
// to nothing. A tag that does not verify is 404 — the route is no oracle on
// which tenants, cameras or tickets exist — and one that verifies and has passed
// its moment is 410, because that ticket was real and is over.
func TestATicketThatDoesNotVerifyIsNotThere(t *testing.T) {
	mine := kept(org, "cam-1")
	const credential = "rtsp://admin:hunter2@cam1.depot.local/live"
	v := newVault(map[string]string{mine: credential})
	app := wired(t, v, mine)

	// Mint one, so the org's signing key exists and the test can tag with it.
	if code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, officer, nil); code != http.StatusOK {
		t.Fatalf("mint a stream: %d (%s)", code, first(raw, 200))
	}
	key, err := ops{s: &cloud.Service[state]{Base: cloud.Base{KMS: v, Log: luxlog.New("test")}}}.
		held(context.Background(), org)
	if err != nil {
		t.Fatalf("read the signing key back: %v", err)
	}
	live := strconv.FormatInt(time.Now().UTC().Add(streamLife).Unix(), 10)
	stale := strconv.FormatInt(time.Now().UTC().Add(-time.Minute).Unix(), 10)

	for _, tc := range []struct {
		name, ticket, expires string
		want                  int
	}{
		{"a tag with no tenant on it", "deadbeef", live, http.StatusNotFound},
		{"a tenant with no tag", org + ".", live, http.StatusNotFound},
		{"an expiry that is not a moment", ticketOf(key, org, "cam-1", "soon"), "soon", http.StatusNotFound},
		{"a tag over another camera", ticketOf(key, org, "cam-2", live), live, http.StatusNotFound},
		{"a tag over another moment", ticketOf(key, org, "cam-1", stale), live, http.StatusNotFound},
		{"a tag naming a tenant it was not minted in", ticketOf(key, "other", org, live), live, http.StatusNotFound},
		{"a tag that verifies and has run out", ticketOf(key, org, "cam-1", stale), stale, http.StatusGone},
	} {
		path := fmt.Sprintf("/v1/patrol/camera/cam-1/stream/%s?expires=%s", tc.ticket, tc.expires)
		code, raw := hit(t, app, http.MethodGet, path, nil, nil)
		if code != tc.want {
			t.Errorf("%s: want %d, got %d (%s)", tc.name, tc.want, code, first(raw, 200))
		}
		if strings.Contains(string(raw), "hunter2") {
			t.Errorf("%s: the refusal carried the credential", tc.name)
		}
	}

	// A ticket that verifies opens the camera, and stops opening it the moment
	// the credential is taken out of the vault.
	path := fmt.Sprintf("/v1/patrol/camera/cam-1/stream/%s?expires=%s", ticketOf(key, org, "cam-1", live), live)
	code, raw := hit(t, app, http.MethodGet, path, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("a live ticket want 200, got %d (%s)", code, first(raw, 200))
	}
	var got patrolStream
	data(t, raw, &got)
	if got.URL != credential {
		t.Fatalf("a redeemed ticket answers what dials the camera, got %q", got.URL)
	}
	if err := v.DeleteSecret(context.Background(), mine); err != nil {
		t.Fatalf("remove the credential: %v", err)
	}
	if code, raw := hit(t, app, http.MethodGet, path, nil, nil); code != http.StatusNotFound {
		t.Fatalf("a live ticket for a camera with nothing to open want 404, got %d (%s)", code, first(raw, 200))
	}
}
