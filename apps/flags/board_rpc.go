// Copyright © 2026 Hanzo AI. MIT License.

package flags

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The operator switchboard and its one write, published on the internal plane.
//
// [Board] reads the process's own definition registry and resolves each value
// through `mounted`, the client this app's Use fills. In the admin binary the
// registry holds only admin's own declarations and that client is nil, so the
// cockpit rendered an EMPTY board with configured:false — and
// [SetPlatformSwitch] answered "flags: engine not configured", refusing every
// write. Loudly, which is the only reason this was a visibly broken feature
// rather than a silently wrong one.
//
// NEITHER OP CARRIES AN ORG. The tenant is the validated caller, read from the
// peer context; a board that took one as an argument would render any tenant's
// switches for whoever asked, and a write that took one would let a caller edit
// somebody else's.

// exposeBoard publishes the switchboard and its write. Use calls it.
func exposeBoard() {
	zip.Post[plane.Unit, plane.FlagBoard](cloud.Plane(), "/flags/board",
		board,
		zip.WithOperationID(plane.FlagsBoard),
		zip.WithSummary("The operator switchboard for the caller's org"))
	zip.Post[plane.FlagSetIn, plane.Unit](cloud.Plane(), "/flags/set",
		set,
		zip.WithOperationID(plane.FlagsSet),
		zip.WithSummary("Write one platform switch"))
}

// board answers the switchboard through this app's own [Board], so the plane and
// /v1/flag render one value. A second projection here would be a second answer to
// "what is in force", and the two would disagree the day a default moves.
func board(ctx context.Context, _ *plane.Unit) (*plane.FlagBoard, error) {
	if cloud.Who(ctx).Org == "" {
		return nil, zip.ErrUnauthorized("board: no org on the call")
	}
	b := Board()
	out := &plane.FlagBoard{
		Engine:     b.Engine,
		Configured: b.Configured,
		ManageURL:  b.ManageURL,
		AuditURL:   b.AuditURL,
		Switches:   make([]plane.FlagSwitch, 0, len(b.Switches)),
	}
	for _, s := range b.Switches {
		out.Switches = append(out.Switches, plane.FlagSwitch{
			Key: s.Key, Category: s.Category, Label: s.Label,
			Description: s.Description, Type: s.Type, Value: s.Value,
			Source: s.Source, Env: s.Env, ReadOnly: s.ReadOnly,
		})
	}
	return out, nil
}

// set writes one switch through [SetPlatformSwitch], the ONE write path, so the
// store's audit records this exactly as it records a write made here.
//
// The definition is kept byte-for-byte: it is the engine's document rather than
// this package's, carrying fields no Go type here names, and re-encoding it would
// drop whatever we do not model.
func set(ctx context.Context, in *plane.FlagSetIn) (*plane.Unit, error) {
	if cloud.Who(ctx).Org == "" {
		return nil, zip.ErrUnauthorized("set: no org on the call")
	}
	if in == nil || strings.TrimSpace(in.Key) == "" {
		return nil, zip.ErrBadRequest("set: a flag key is required")
	}
	if len(in.Definition) == 0 {
		return nil, zip.ErrBadRequest("set: a definition is required")
	}
	if err := SetPlatformSwitch(in.Key, json.RawMessage(in.Definition), in.Actor); err != nil {
		return nil, err
	}
	return &plane.Unit{}, nil
}
