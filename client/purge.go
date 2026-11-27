package client

import (
	"context"
	"errors"
	"time"

	"github.com/hanzoai/cloud/sdk/plane"
)

// PurgeEdge clears every URL under host+path, for each path, from the edge cache:
// what a writer calls once it has changed an answer the edge keeps (cloud build.go
// publicCatalog). It asks the project app, which holds the one edge client with its
// credential and the deployment's purge ceiling, as the platform. A deployment that
// runs no project app keeps no edge copy to clear, and that is not an error; any
// other failure is returned for the caller to log, never to fail its write on,
// since every edge copy also lapses by its own TTL.
//
// The call is made on a context with no request behind it: a request's own caller
// would stand in place of the platform (For), and the purge has to outlive the
// write that asked for it.
func PurgeEdge(platform, host string, paths ...string) error {
	if host == "" || len(paths) == 0 {
		return nil
	}
	in := &EdgePurgeIn{Prefixes: make([]string, 0, len(paths))}
	for _, p := range paths {
		in.Prefixes = append(in.Prefixes, host+p)
	}
	ctx, cancel := context.WithTimeout(plane.For(context.Background(), platform), 30*time.Second)
	defer cancel()
	_, err := plane.Call[EdgePurgeIn, EdgePurgeOut](ctx, "project", SitesPurge, in)
	if errors.Is(err, plane.ErrNoPeer) {
		return nil
	}
	return err
}
