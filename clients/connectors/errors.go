package connectors

import "errors"

// errNoIdentity is what an identity call returns when the provider answered
// successfully but named no account. It is not a failure of the connect — see
// the exchange in oauth2.go.
var errNoIdentity = errors.New("connectors: provider named no account")
