package iam

import (
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/ai/address"
)

// identifiers is the sign-in screen's identifier lookups, counted across every pod
// the deployment runs: a client walking a list of addresses is held to IAM's bound
// once between the pods, not once by each.
var identifiers = cloud.NewWindow("iam:identifier", 0, 0)

// admitIdentifier is IAM's identifier count (server.SetIdentifierAdmit) on that
// window: the client against its own limit, then every client together against the
// ceiling, which is charged only for what the client's limit admitted.
func admitIdentifier(client string, limit, ceiling int, window time.Duration) bool {
	scale := max(ceiling/max(limit, 1), 1)
	left, _ := identifiers.Spend([]address.Bucket{
		{Key: "client:" + client, Scale: 1},
		{Key: "all", Scale: scale},
	}, limit, window)
	return left >= 0
}
