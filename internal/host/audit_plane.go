package host

// The audit trail over the plane: how a process that does not hold it appends to
// it and reads it.
//
// ONE STORE. admin holds the deployment's one tamper-evident chain — the one
// GET /v1/admin/audit reads — and is its one writer. Every other process builds
// an audit.Remote recorder over auditPeer, so a call site keeps its one shape,
// rec.Append(ctx, audit.Record), and the record lands in the chain the operator
// reads. A failed append is an error at that call site, which fails closed on it
// exactly as it did on a local write that failed (AU-5).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"

	adminpeer "github.com/hanzoai/cloud/client/admin"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// auditPeerTimeout bounds one call to the trail. Every audited write in a process
// that does not hold it waits on this, so it is short; a write whose record did
// not land in time is refused by its caller.
const auditPeerTimeout = 5 * time.Second

// auditPeer is the trail, as every process but admin's reaches it.
type auditPeer struct{}

// Append hands one record to admin and answers it sealed. The call is DETACHED
// from the request: a client that hangs up must not take its own record out of
// the trail, and the request behind ctx is recycled once its handler returns.
func (auditPeer) Append(ctx context.Context, rec audit.Record) (audit.Record, error) {
	call, cancel := context.WithTimeout(zip.Detach(ctx), auditPeerTimeout)
	defer cancel()
	out, err := adminpeer.AdminAuditAppend(call, adminpeer.PlaneRecord(rec))
	if err != nil {
		return audit.Record{}, fmt.Errorf("audit: %s did not take the record: %w", adminpeer.App, err)
	}
	return adminpeer.TrailRecord(out), nil
}

// Query reads the trail for ONE org. The org rides the CALLER, never the argument
// (client/peer.go): the request is dropped so the tenant this read states is the
// one the call carries, and the reader's own cancellation still stops it. A read
// that names no org is refused here — the fleet-wide read is admin's own.
func (auditPeer) Query(ctx context.Context, f audit.Filter) ([]audit.Record, int, error) {
	org := strings.TrimSpace(f.Org)
	if org == "" {
		return nil, 0, errors.New("audit: a read of the trail from outside " + adminpeer.App + " names its org")
	}
	call, cancel := context.WithTimeout(cloud.For(zip.Detach(ctx), org), auditPeerTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	out, err := adminpeer.AdminAuditQuery(call, adminpeer.PlaneFilter(f))
	if err != nil {
		return nil, 0, fmt.Errorf("audit: read from %s: %w", adminpeer.App, err)
	}
	rows := make([]audit.Record, 0, len(out.Records))
	for i := range out.Records {
		rows = append(rows, adminpeer.TrailRecord(&out.Records[i]))
	}
	return rows, out.Total, nil
}
