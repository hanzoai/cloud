package websearch

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/planetest"
)

// peers is the two apps a search asks, each on a REAL plane socket: settings,
// holding the platform's engine setting, and kms, holding the platform's engine
// keys. A real peer and not a stub field: the setting and the key have to survive
// the same socket, typed op and stated caller that carry them in the fleet.
type peers struct {
	settings *planetest.Settings
	kms      *planetest.KMS
}

var (
	peersMu sync.Mutex
	peersOf = map[*testing.T]*peers{}
)

// fleetOf is this test's peers, stood up on first use. Every search in a test
// reads its engines from here, so no test reaches a real engine by default.
func fleetOf(t *testing.T) *peers {
	t.Helper()
	peersMu.Lock()
	defer peersMu.Unlock()
	if p, ok := peersOf[t]; ok {
		return p
	}
	good.Store(nil)
	// Every engine a test did not point somewhere answers an empty page from
	// here, so a lineup that falls back to the keyless engines never leaves the
	// machine. A test that names an engine's address first keeps it.
	quiet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body></body></html>")
	}))
	t.Cleanup(quiet.Close)
	for _, env := range []string{"WEBSEARCH_BING_URL", "WEBSEARCH_DDG_URL", "WEBSEARCH_MOJEEK_URL", "WEBSEARCH_MOJEEK_API_URL", "WEBSEARCH_BRAVE_URL"} {
		if _, set := os.LookupEnv(env); !set {
			t.Setenv(env, quiet.URL)
		}
	}
	p := &peers{settings: planetest.ServeSettings(t), kms: planetest.ServeKMS(t)}
	peersOf[t] = p
	t.Cleanup(func() {
		good.Store(nil)
		peersMu.Lock()
		delete(peersOf, t)
		peersMu.Unlock()
	})
	return p
}

// enable saves a setting that runs exactly these engines, in this order.
func (p *peers) enable(names ...string) {
	p.save(`{"enabled":["` + strings.Join(names, `","`) + `"]}`)
}

// save stores doc as the platform's engine setting verbatim; "" is nothing saved.
func (p *peers) save(doc string) { p.settings.Set(product, doc) }

// saved is the setting as stored.
func (p *peers) saved() string { return p.settings.Doc(product) }

// key seals the platform's key for an engine; "" removes it.
func (p *peers) key(engine, value string) { p.kms.Put(keyRef(engine), value) }

// held is the key KMS holds for an engine, "" when none.
func (p *peers) held(engine string) string { return p.kms.Get(keyRef(engine)) }

// kmsCalls is every org-and-ref kms was asked for.
func (p *peers) kmsCalls() []string { return p.kms.Asked() }
