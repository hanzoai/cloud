package api

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

// RouteFn is an optional side-car wirer for API routes that don't ship
// in-tree — typically sub-modules like luxfi/cevm which would
// otherwise transitively pull heavy deps (geth, warp, etc.) into consumers
// that don't need them.
type RouteFn func(r router.Router, args ...gin.HandlerFunc)

var extraRoutes []RouteFn

// RegisterRoute wires an additional route function. The parent api.Route
// invokes every registered function after its own wiring is done. Consumers
// who want the route must blank-import the sub-module (e.g.
// `import _ "github.com/hanzoai/cloud/clients/commerce/luxfi/cevm/api"`).
func RegisterRoute(fn RouteFn) {
	extraRoutes = append(extraRoutes, fn)
}

func applyExtraRoutes(r router.Router, args ...gin.HandlerFunc) {
	for _, fn := range extraRoutes {
		fn(r, args...)
	}
}
