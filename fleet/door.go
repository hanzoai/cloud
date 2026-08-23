package fleet

// door.go mounts the fleet's GraphQL address.
//
// It is the HOST's, for the reason /v1/openapi.json is: the answer is about the
// whole fleet, and no plugin can see past itself. Left unclaimed the address falls
// through to whichever app holds the /v1 remainder, which then answers with a
// schema of its OWN registry — one field, honestly rendered, about the wrong
// thing.

import (
	"net/http"
	"sync"

	jsonenc "encoding/json"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// MountGraph serves the fleet schema at path: GET renders it, POST runs a request
// against it.
//
// The schema and the dispatch are built from ONE composed document, once, so the
// fields a caller can read are the fields a caller can send.
//
// Composed lazily and kept, for the reason the index is: composing reads every
// app's embedded subset, and a deployment that never receives a GraphQL request
// should not pay for one.
func MountGraph(app *zip.App, path string, subsets func() ([]openapi.Part, error), at At) {
	build := sync.OnceValues(func() (*door, error) {
		parts, err := subsets()
		if err != nil {
			return nil, err
		}
		d, err := openapi.Fleet(parts)
		if err != nil {
			return nil, err
		}
		return &door{graph: NewGraph(d, at), sdl: openapi.GraphQL(d)}, nil
	})

	app.Get(path, func(c *zip.Ctx) error {
		d, err := build()
		if err != nil {
			return zip.ErrInternal("the fleet document would not compose: " + err.Error())
		}
		c.SetHeader("Content-Type", "text/plain; charset=utf-8")
		return c.Bytes(http.StatusOK, []byte(d.sdl))
	})

	app.Post(path, func(c *zip.Ctx) error {
		d, err := build()
		if err != nil {
			return zip.ErrInternal("the fleet document would not compose: " + err.Error())
		}
		var req Request
		if err := jsonenc.Unmarshal(c.Fiber().Body(), &req); err != nil {
			return answer(c, Response{Errors: []Failure{{Message: "invalid request body: " + err.Error()}}})
		}
		return answer(c, d.graph.Run(req, c.Fiber().Request()))
	})
}

// door is the schema and the dispatch, built together from one document so they
// cannot describe different surfaces.
type door struct {
	graph *Graph
	sdl   string
}

// answer writes a GraphQL response. Nothing in Data means execution never began,
// so there is no partial answer to be right about and the REQUEST was refused.
// Once it has begun the answer is 200 even where fields failed, which is where a
// GraphQL client reads its errors from.
func answer(c *zip.Ctx, res Response) error {
	body, err := jsonenc.Marshal(res)
	if err != nil {
		return err
	}
	code := http.StatusOK
	if res.Data == nil {
		code = http.StatusBadRequest
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(code, body)
}
