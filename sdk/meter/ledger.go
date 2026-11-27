package meter

// ledger.go — who pays, and the books they pay from.
//
// The meter checks a balance before work and records its cost after. WHERE the
// balance lives is not the meter's business: it may be a prepaid wallet in this
// process, a commerce service over HTTP, or another process on the internal plane.
// The SDK names that as a [Ledger] and links none of them. A deployment registers the
// one it runs (the host links internal/driver/ledger), exactly as sdk/coord takes its
// shared backends, and a [Client] is a Ledger behind the meter's own rules.
//
// With nothing registered a process meters nothing: [Free] allows every call and
// charges none, which is the whole deployment when it is a laptop or a library user.
// A process that was TOLD to bill — a named deployment (CLOUD_ENV) or a commerce
// BaseURL — and has no ledger linked refuses instead, because a binary that
// quietly stopped debiting is a revenue outage that looks like success.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/env"
)

// Ledger is the books a metered act is weighed against and written to. Authorize,
// Record and Post have already been given a usage that names its user and org and
// is sealed; a Ledger answers for the money and nothing else.
type Ledger interface {
	// Enabled reports whether the books answer from THIS process (a commerce base
	// is configured). False means they live elsewhere or nowhere; it is not "off".
	Enabled() bool
	// AuthorizeVerdict is the pre-request gate: funds first and, only when funded,
	// the per-scope spend cap. A balance or cap it cannot read is an error, never
	// permission.
	AuthorizeVerdict(ctx context.Context, in AuthInput) (Verdict, error)
	// Balance is the subject's spendable prepaid balance in cents, inside org's
	// books, rounded down.
	Balance(ctx context.Context, subject, org, currency string) (int64, error)
	// Record writes one debit. A deployment with no biller anywhere returns (nil, nil).
	Record(ctx context.Context, u Usage) (*RecordResult, error)
	// Post records one served call durably and returns; it falls back to Record
	// when the hand-off cannot take it.
	Post(ctx context.Context, u Usage) error
}

// Drainer is a Ledger that owns the books and posts the debits queued for them.
type Drainer interface {
	Drain(ctx context.Context, logf func(msg string, kv ...any))
}

// Doer is the HTTP client a ledger speaks to a commerce base with.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config configures a Client. An empty BaseURL does not mean off: the books are then
// asked wherever the deployment keeps them.
type Config struct {
	// BaseURL is the commerce service base, e.g.
	// "http://commerce.hanzo.svc.cluster.local:8001". No trailing /v1 — the
	// ledger appends the canonical billing paths itself.
	BaseURL string

	// Timeout bounds each commerce HTTP call. Default 5s (the gateway's value).
	Timeout time.Duration

	// HTTPClient overrides the underlying HTTP client. When nil a client with
	// Timeout is created.
	HTTPClient Doer
}

// ErrNoDriver is what a Client answers, to every call, when its process was told to
// bill and no ledger is linked into the binary. It is a refusal, never a free call.
var ErrNoDriver = errors.New("meter: this process is configured to bill but no ledger is linked into the binary")

var (
	mu      sync.Mutex
	factory func(Config) (Ledger, error)
)

// Register installs the function that opens a deployment's ledger from a Config. The
// host calls it once, from init; a second registration is a programming error and
// panics, as sql.Register does.
func Register(open func(Config) (Ledger, error)) {
	mu.Lock()
	defer mu.Unlock()
	if factory != nil {
		panic("meter: a ledger is already registered")
	}
	factory = open
}

func registered() func(Config) (Ledger, error) {
	mu.Lock()
	defer mu.Unlock()
	return factory
}

// told reports whether this process was told to bill: it runs in a named deployment,
// or its Config names a commerce base. A process that was told and has no ledger
// refuses; one that was not meters nothing.
func (cfg Config) told() bool {
	return cfg.BaseURL != "" || env.Or("CLOUD_ENV", "") != ""
}

// Free is the unmetered ledger: it allows every call and charges none.
func Free() Ledger { return free{} }

type free struct{}

func (free) Enabled() bool { return false }
func (free) AuthorizeVerdict(context.Context, AuthInput) (Verdict, error) {
	return Verdict{Allow: true}, nil
}
func (free) Balance(context.Context, string, string, string) (int64, error) { return 0, nil }
func (free) Record(context.Context, Usage) (*RecordResult, error)           { return nil, nil }
func (free) Post(context.Context, Usage) error                              { return nil }

// refusing is the ledger of a process that was told to bill and has none.
type refusing struct{}

func (refusing) Enabled() bool { return false }
func (refusing) AuthorizeVerdict(context.Context, AuthInput) (Verdict, error) {
	return Verdict{}, ErrNoDriver
}
func (refusing) Balance(context.Context, string, string, string) (int64, error) {
	return 0, ErrNoDriver
}
func (refusing) Record(context.Context, Usage) (*RecordResult, error) { return nil, ErrNoDriver }
func (refusing) Post(context.Context, Usage) error                    { return ErrNoDriver }
