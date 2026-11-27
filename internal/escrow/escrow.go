package escrow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/secret"
)

// Env is the KMS environment slug a record's secrets are kept under.
const Env = "default"

// mark begins every reference a column holds.
const mark = "kms:"

// timeout bounds one KMS call, so a store that cannot reach KMS answers rather than waits.
const timeout = 10 * time.Second

// ErrNoKMS is every call on a process with no KMS: a credential is neither kept nor read.
var ErrNoKMS = errors.New("escrow: this process has no KMS, so a credential cannot be kept or read")

// ErrGone is a reference whose secret KMS does not hold.
var ErrGone = errors.New("escrow: KMS does not hold the secret this record names")

// KMS is the part of cloud.Store the escrow uses.
type KMS interface {
	GetSecret(ctx context.Context, ref string) ([]byte, error)
	PutSecret(ctx context.Context, ref string, value []byte) error
	DeleteSecret(ctx context.Context, ref string) error
}

// Escrow keeps one app's secrets.
type Escrow struct {
	kms KMS
	app string
}

// New is app's escrow over the KMS the process holds. With a nil KMS every call is
// ErrNoKMS: a store with no KMS never writes a credential anywhere else.
func New(kms KMS, app string) *Escrow { return &Escrow{kms: kms, app: app} }

// segment is what a record id or a field may be: it is a path segment of a ref, which
// cuts on "/" and "@".
var segment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Ref is where field of record, in org, is kept: the whole of what a column holds,
// after the mark.
func (e *Escrow) Ref(org, record, field string) (string, error) {
	if !segment.MatchString(record) || !segment.MatchString(field) {
		return "", fmt.Errorf("escrow: %q and %q are not path segments", record, field)
	}
	path := kms.OrgPath(org, e.app, record)
	if path == "" {
		return "", fmt.Errorf("escrow: %q is not an org a secret can be kept for", org)
	}
	return strings.TrimPrefix(path, "/") + "/" + field + "@" + Env, nil
}

// Held reports whether stored is a reference, not a value. The empty string is neither.
func Held(stored string) bool { return strings.HasPrefix(stored, mark) }

// call is ctx acting for org, bounded.
func call(ctx context.Context, org string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(plane.For(ctx, org), timeout)
}

// Put keeps value in KMS and answers what the record stores in its place. An empty value
// is kept nowhere and stored as the empty string.
func (e *Escrow) Put(ctx context.Context, org, record, field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if e == nil || e.kms == nil {
		return "", ErrNoKMS
	}
	ref, err := e.Ref(org, record, field)
	if err != nil {
		return "", err
	}
	ctx, cancel := call(ctx, org)
	defer cancel()
	if err := e.kms.PutSecret(ctx, ref, []byte(value)); err != nil {
		return "", fmt.Errorf("escrow: keep %s: %w", record, err)
	}
	return mark + ref, nil
}

// Get is the value stored names. The empty string names nothing and is the empty value.
// A stored value that is not a reference is an error: only the open of the store reads
// one, to move it.
func (e *Escrow) Get(ctx context.Context, org, stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	ref, ok := strings.CutPrefix(stored, mark)
	if !ok {
		return "", fmt.Errorf("escrow: a column holds a value where a reference belongs")
	}
	if e == nil || e.kms == nil {
		return "", ErrNoKMS
	}
	ctx, cancel := call(ctx, org)
	defer cancel()
	b, err := e.kms.GetSecret(ctx, ref)
	if err != nil {
		if errors.Is(err, secret.ErrNone) {
			return "", fmt.Errorf("%w: %s", ErrGone, ref)
		}
		return "", fmt.Errorf("escrow: read %s: %w", ref, err)
	}
	return string(b), nil
}

// Drop asks KMS to forget what stored names. A secret KMS does not hold, or a stored
// value that names nothing, is already forgotten.
func (e *Escrow) Drop(ctx context.Context, org, stored string) error {
	ref, ok := strings.CutPrefix(stored, mark)
	if !ok {
		return nil
	}
	if e == nil || e.kms == nil {
		return ErrNoKMS
	}
	ctx, cancel := call(ctx, org)
	defer cancel()
	if err := e.kms.DeleteSecret(ctx, ref); err != nil && !errors.Is(err, secret.ErrNone) {
		return fmt.Errorf("escrow: forget %s: %w", ref, err)
	}
	return nil
}
