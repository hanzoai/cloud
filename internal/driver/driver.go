// Package driver links the production backends, and nothing else: it holds blank
// imports only. Every plugin binary and cmd/cloud imports it, so a process that
// serves has them and a process that does not (a tool, a test of the framework)
// does not carry them. The SDK names an interface for each and ships a local default;
// the driver registers into it.
//
// The shared backends it links are: the KV that makes counts and the live set hold across pods, the
// pubsub cluster the bus rides on, and the ledger every debit and gate
// is weighed against (sdk/meter bills nothing until it is registered). sdk/coord answers alone, in
// memory, until a driver registers, and refuses KV_URL when none is linked. The stores
// live on their own SQLite in sdk/locus, which links no server: internal/driver/postgres is
// what makes a CLOUD_SQL_DSN mean something, and without it a binary told to use a
// server refuses to open a store (locus.ErrNoServer) and never quietly writes a file.
package driver

import (
	_ "github.com/hanzoai/cloud/internal/driver/datastore"
	_ "github.com/hanzoai/cloud/internal/driver/kv"
	_ "github.com/hanzoai/cloud/internal/driver/ledger"
	_ "github.com/hanzoai/cloud/internal/driver/postgres"
	_ "github.com/hanzoai/cloud/internal/driver/pubsub"
	_ "github.com/hanzoai/cloud/internal/driver/pubsub/stream"
	_ "github.com/hanzoai/cloud/internal/driver/s3"
)
