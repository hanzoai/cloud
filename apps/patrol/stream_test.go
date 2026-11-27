package patrol

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// stream_test.go holds the camera route to the word the design uses for it. A
// minted address is derived, bounded and revocable; echoing the stored
// credential hands the camera's own password to every caller and the expiry
// beside it says nothing, because nothing was issued that could stop.

// vault is a KMS stub. It answers what was put at a ref and remembers every ref
// it was asked for, which is how a test sees where a route reached.
type vault struct {
	mu    sync.Mutex
	held  map[string]string
	asked []string
	// refuse, when set, is what a write answers instead of landing.
	refuse error
	// swallow accepts a write and keeps nothing: a vault whose read-back does
	// not see what was just put there.
	swallow bool
	// missed is how many more reads answer that a secret is not there before the
	// one that finds it, which is how a test stands where a second caller stands
	// when the first has just written the key.
	missed int
}

func newVault(held map[string]string) *vault { return &vault{held: held} }

func (v *vault) GetSecret(_ context.Context, ref string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.asked = append(v.asked, ref)
	if v.missed > 0 {
		v.missed--
		return nil, fmt.Errorf("no secret at %s", ref)
	}
	s, ok := v.held[ref]
	if !ok {
		return nil, fmt.Errorf("no secret at %s", ref)
	}
	return []byte(s), nil
}

func (v *vault) PutSecret(_ context.Context, ref string, value []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.refuse != nil {
		return v.refuse
	}
	if v.swallow {
		return nil
	}
	v.held[ref] = string(value)
	return nil
}

func (v *vault) DeleteSecret(_ context.Context, ref string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.held, ref)
	return nil
}

func (v *vault) Sign(context.Context, string, []byte) ([]byte, error) { return nil, nil }

func (v *vault) reached(ref string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Contains(v.asked, ref)
}

// mountVault is mount with a KMS the test holds, so what the stream route reads
// and what it hands back are both visible.
func mountVault(t *testing.T, v *vault) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	planetest.Roled(t, func(_, user string) []string { return grants[user] })
	planetest.Entitled(t, func(_, product string) bool { return product == Module })
	deps := cloud.Deps{KMS: v}
	if err := framework.Use(app, deps); err != nil {
		t.Fatalf("mount framework: %v", err)
	}
	if err := Use(app, deps); err != nil {
		t.Fatalf("mount patrol: %v", err)
	}
	t.Cleanup(func() { _ = framework.Shutdown() })
	return app
}

// TestStreamMintsRatherThanEchoes holds the stream route to the word the design
// uses for it. A minted address is derived, bounded and revocable; echoing the
// stored credential hands the camera's own password to every caller and the
// `expires` beside it says nothing, because nothing was issued that could stop.
func TestStreamMintsRatherThanEchoes(t *testing.T) {
	const ref = "acme/patrol/camera/cam-1"
	const credential = "rtsp://admin:hunter2@cam1.depot.local/Streaming/Channels/101"
	v := newVault(map[string]string{ref: credential})
	app := mountVault(t, v)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTCamera, map[string]any{
		"name": "cam-1", "site": "depot", "label": "Yard", "online": true, "secret": ref,
	})

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, officer, nil)
	if code != http.StatusOK {
		t.Fatalf("mint a stream want 200, got %d (%s)", code, raw)
	}
	var got patrolStream
	data(t, raw, &got)
	if strings.Contains(got.URL, "hunter2") || got.URL == credential {
		t.Fatalf("the stream response carried the camera's stored credential verbatim: %+v", got)
	}
}

// TestStreamReadsOnlyItsOwnCameraPath proves the KMS ref the route reads is not
// a value a caller chose. patrol.camera.secret is ReadOnly on the fixture and an
// officer holds Write on the DocType, so if the generic surface lets them move
// the path, the stream route reads whatever they point it at and answers with
// the bytes.
func TestStreamReadsOnlyItsOwnCameraPath(t *testing.T) {
	const mine = "acme/patrol/camera/cam-1"
	const theirs = "acme/notify/twilio"
	v := newVault(map[string]string{
		mine:   "rtsp://admin:hunter2@cam1.depot.local/live",
		theirs: "AC0000000000000000000000000000:8f3b1d0c9a7e4f2b",
	})
	app := mountVault(t, v)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTCamera, map[string]any{
		"name": "cam-1", "site": "depot", "label": "Yard", "online": true, "secret": mine,
	})

	moved, body := call(t, app, http.MethodPut, "/v1/framework/"+DTCamera.String()+"/cam-1", org, officer,
		map[string]any{"name": "cam-1", "site": "depot", "label": "Yard", "online": true, "secret": theirs})
	if moved == http.StatusOK {
		code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, officer, nil)
		var got patrolStream
		if code == http.StatusOK {
			data(t, raw, &got)
		}
		t.Fatalf("an officer moved patrol.camera.secret to %q through /v1/framework and the stream route "+
			"read it (%d, url=%q; reached=%v)", theirs, code, got.URL, v.reached(theirs))
	}
	if moved != http.StatusForbidden {
		t.Fatalf("an officer writing a ReadOnly KMS path want 403, got %d (%s)", moved, body)
	}
}
