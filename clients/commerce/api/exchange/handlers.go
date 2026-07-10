// Package exchange wires the order-exchange HTTP surface: CRUD on exchanges
// plus confirm/cancel transitions. Every handler is tenant-scoped via the
// caller's organization namespace; custom sub-routes pass the Namespace
// middleware explicitly since the REST layer only auto-namespaces default CRUD.
package exchange

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	exchangeModel "github.com/hanzoai/cloud/clients/commerce/models/exchange"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	api := rest.New(exchangeModel.Exchange{})
	api.POST("/:exchangeid/confirm", namespaced, Confirm)
	api.POST("/:exchangeid/cancel", namespaced, Cancel)
	api.Route(router, args...)
}

// Confirm transitions an open exchange to confirmed.
func Confirm(c *gin.Context) {
	transition(c, exchangeModel.StatusConfirmed, "confirm")
}

// Cancel transitions an open exchange to canceled.
func Cancel(c *gin.Context) {
	transition(c, exchangeModel.StatusCanceled, "cancel")
}

// transition sets an open (requested) exchange's status to target. Only open
// exchanges may be confirmed or canceled; a resolved exchange is rejected.
func transition(c *gin.Context, target, verb string) {
	org := middleware.GetOrganization(c)
	// Per-org store: exchanges are CREATED via NewNamespaced, so the confirm/
	// cancel transition MUST read from the same per-org store. datastore.New binds
	// the shared systemDB regardless of the namespace in the context, so with the
	// resolver installed in prod it would miss the exchange (Red MED-1).
	db := datastore.NewNamespaced(org.Namespaced(c))

	id := c.Params.ByName("exchangeid")

	e := exchangeModel.New(db)
	if err := e.GetById(id); err != nil {
		http.Fail(c, 404, "No exchange found with id: "+id, err)
		return
	}

	if !e.IsOpen() {
		http.Fail(c, 409, "Cannot "+verb+" exchange in status: "+e.Status, errors.New("exchange not open"))
		return
	}

	e.Status = target
	if err := e.Update(); err != nil {
		http.Fail(c, 500, "Failed to "+verb+" exchange", err)
		return
	}

	http.Render(c, 200, e)
}
