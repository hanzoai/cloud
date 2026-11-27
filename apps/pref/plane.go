package pref

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// The run's side of this app, on the internal plane.
//
// A coding run is started in the agents process for a person, and what that
// person set for their runs — their instructions, the model and effort a run
// takes when it names none — is in their preference document here. So the run
// asks, as the person: the op keys on the org and the person on the call, the
// same `<owner>/<name>` GET /v1/pref keys on, and takes no argument that could
// name anyone else.

// expose publishes the op. Use calls it.
func expose(s *service) {
	cloud.Plane().Post("/pref/own", s.own,
		zip.WithOperationID(client.PrefOwn),
		zip.WithSummary("The caller's own preference document"))
}

// own answers the caller's preference document verbatim, `{}` for a person who
// has saved nothing. A call that names no person is refused: a background call
// acting only for an org has no one whose preferences these would be.
func (s *service) own(ctx context.Context, _ *client.PrefsIn) (*client.Prefs, error) {
	who := cloud.Who(ctx)
	subject, ok := key(who.Org, who.User)
	if !ok {
		return nil, zip.ErrForbidden("pref: the call names no person")
	}
	p, err := s.store.Get(ctx, subject)
	switch {
	case err == errNotFound:
		return &client.Prefs{Doc: "{}"}, nil
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "pref: %v", err)
	}
	return &client.Prefs{Doc: p.Doc}, nil
}
