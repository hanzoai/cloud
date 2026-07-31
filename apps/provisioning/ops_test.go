package provisioning

// The four typed ops (provisioning.go), adapted back to plain routes for the tests
// that drive ONE handler in isolation rather than the whole mount. They add no
// behaviour — each binds the op's input the way zip binds it and writes the status
// the registration declares — so a test exercises the SAME implementation
// production serves, and there is still exactly one implementation.
//
// cloud.Bridge() belongs in the chain beside them: a typed op reads the request —
// and therefore the validated org — off the context Bridge parks it in, exactly as
// routes() installs it around the real routes. bridged() composes it so a caller
// registers one handler and gets the same request facts Mount would.

import (
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// bridged wraps one handler in the request bridge, so a route registered with it
// alone behaves as it does under Mount.
func bridged(h zip.Handler) []zip.Handler { return []zip.Handler{cloud.Bridge(), h} }

// create composes the SAME money gate routes() composes around the real create, so
// a test that drives this handler crosses the real tenant + balance boundary.
func create(s *cloud.Service[state], kind string) []zip.Handler {
	o := ops{s, kind}
	return bridged(gate(o)(func(c *zip.Ctx) error {
		var in createReq
		if err := c.Bind(&in); err != nil {
			return err
		}
		out, err := o.create(c.Context(), &in)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, out)
	}))
}

func list(s *cloud.Service[state], kind string) []zip.Handler {
	return bridged(func(c *zip.Ctx) error {
		out, err := ops{s, kind}.list(c.Context(), &noArgs{})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, out)
	})
}

func get(s *cloud.Service[state], kind string) []zip.Handler {
	return bridged(func(c *zip.Ctx) error {
		out, err := ops{s, kind}.get(c.Context(), &resourceRef{Name: c.Param("name")})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, out)
	})
}

func drop(s *cloud.Service[state], kind string) []zip.Handler {
	return bridged(func(c *zip.Ctx) error {
		if _, err := (ops{s, kind}).drop(c.Context(), &resourceRef{Name: c.Param("name")}); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
}
