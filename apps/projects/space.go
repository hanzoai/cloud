// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package projects

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/base"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// submissions is the ONE collection every project's Base data space carries for
// generic data collection — the form, forum and data submissions a deployed site
// POSTs to /v1/base/collections/submissions/records. Anyone may create one (so an
// anonymous published page can submit out of the box); every other rule is nil,
// so only the org reads them.
//
// It is this app's schema, so it lives here, and it reaches the org's Base the one
// way any app writes there: an import over the plane, which grows the Base and
// never changes what already exists.
var submissions = json.RawMessage(`[{
	"name": "submissions",
	"type": "base",
	"createRule": "",
	"fields": [
		{"name": "form", "type": "text", "max": 128},
		{"name": "data", "type": "json", "maxSize": 1048576},
		{"name": "created", "type": "autodate", "onCreate": true}
	]
}]`)

// ensureSpace grows org's Base with the submissions collection. Idempotent.
func ensureSpace(ctx context.Context, org string) error {
	_, err := base.BaseImport(plane.For(ctx, org), &client.BaseImportIn{Collections: submissions})
	return err
}

// spaceDeferred reports whether err says Bases are not hosted here (the base app
// is not deployed, or its embed is off), which defers a space rather than failing
// one: the next project, deploy or submission ensures it again.
func spaceDeferred(err error) bool {
	if errors.Is(err, plane.ErrNoPeer) {
		return true
	}
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusServiceUnavailable
}
