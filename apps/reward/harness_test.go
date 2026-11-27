package reward

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/engine"

	clientpkg "github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	// devmaster keys this test binary: cek opens nothing without a master.
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// rig is a mounted reward app on a real SQLite store (a file per test) and the
// state it was mounted with. Every outward reader a feeder has is replaced by a
// fake the test controls; nothing here dials the network.
type rig struct {
	t   *testing.T
	app *zip.App
	s   *state
}

func newRig(t *testing.T) *rig {
	t.Helper()
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("IAM_STORE_BACKEND", "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	mounted.Lock()
	s := mounted.state
	mounted.Unlock()
	if s == nil {
		t.Fatal("reward mounted no state")
	}
	return &rig{t: t, app: app, s: s}
}

// stub replaces a package variable for the test.
func stub[T any](t *testing.T, v *T, to T) {
	t.Helper()
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

var (
	platformHeaders = map[string]string{"X-User-Id": "staff", "X-User-IsAdmin": "true", "X-Org-Id": "admin", "X-User-Email": "staff@hanzo.ai"}
	nobodyHeaders   = map[string]string{}
)

func memberHeaders(sub, org string) map[string]string {
	return map[string]string{"X-User-Id": sub, "X-Org-Id": org, "X-User-Name": sub}
}

func (r *rig) do(method, path string, headers map[string]string, body any) (int, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := r.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// claims are the claims a subject holds, by event type.
func (r *rig) claims(subject string) []*claim.Claim {
	r.t.Helper()
	all, err := r.s.eng.Claims(context.Background(), engine.Filter{Subject: subject, Limit: 500})
	if err != nil {
		r.t.Fatal(err)
	}
	return all
}

// claimsOf are the subject's claims for one event type.
func (r *rig) claimsOf(subject, event string) []*claim.Claim {
	var out []*claim.Claim
	for _, c := range r.claims(subject) {
		if c.Event == event {
			out = append(out, c)
		}
	}
	return out
}

// fakeWarehouse answers the usage rollup from rows the test sets, by a substring
// of the query.
type fakeWarehouse struct {
	mu      sync.Mutex
	answers map[string][]map[string]any
	asked   []string
}

func (f *fakeWarehouse) query(_ context.Context, q string, _ ...any) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, q)
	for sub, rows := range f.answers {
		if bytes.Contains([]byte(q), []byte(sub)) {
			return rows, nil
		}
	}
	return nil, nil
}

func (r *rig) warehouse(answers map[string][]map[string]any) *fakeWarehouse {
	f := &fakeWarehouse{answers: answers}
	stub(r.t, &queryFn, f.query)
	stub(r.t, &readyFn, func() bool { return true })
	return f
}

// directory stubs IAM's directory.
func (r *rig) directory(users ...cloudUser) {
	stub(r.t, &directoryFn, func(context.Context) (*clientDirectory, error) {
		d := &clientDirectory{}
		for _, u := range users {
			d.Users = append(d.Users, u.row())
		}
		return d, nil
	})
}

var _ = catalog.Yes
var _ = http.StatusOK

// cloudUser is a person in the stubbed directory.
type cloudUser struct {
	ID, Owner, Name, Created string
	Admin                    bool
	Type                     string
}

type clientDirectory = clientpkg.Directory

func (u cloudUser) row() clientpkg.DirectoryUser {
	return clientpkg.DirectoryUser{ID: u.ID, Owner: u.Owner, Name: u.Name, Created: u.Created, Admin: u.Admin, Type: u.Type}
}
