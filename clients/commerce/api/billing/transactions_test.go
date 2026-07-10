package billing

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// mountRoutes mounts the billing Route() onto a bare gin engine under the
// production "/v1" api prefix. A *gin.RouterGroup satisfies router.Router, so
// this exercises the real registration code path without router.New()'s global
// http.Handle side effects.
func mountRoutes(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Route(engine.Group("/v1"))
	return engine
}

// handlerName returns the fully-qualified function name a gin route is bound to
// (e.g. ".../api/billing.ListTransactions"), so we can prove a path resolves to
// the intended handler — not merely that some route exists.
func handlerName(h gin.HandlerFunc) string {
	return runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
}

// TestTransactionsRouteRegistered proves GET /v1/billing/transactions is routed
// and bound to ListTransactions. Regression guard: the handler existed but was
// never registered, so the billing dashboard's Transactions tab got a 404.
func TestTransactionsRouteRegistered(t *testing.T) {
	engine := mountRoutes(t)

	const wantMethod = http.MethodGet
	const wantPath = "/v1/billing/transactions"

	var found *gin.RouteInfo
	for i := range engine.Routes() {
		r := engine.Routes()[i]
		if r.Method == wantMethod && r.Path == wantPath {
			found = &engine.Routes()[i]
			break
		}
	}

	if found == nil {
		t.Fatalf("route %s %s is not registered (the defect): handler ListTransactions exists but was never routed", wantMethod, wantPath)
	}

	if got := handlerName(found.HandlerFunc); !strings.HasSuffix(got, ".ListTransactions") {
		t.Fatalf("route %s %s resolves to %q, want it bound to ListTransactions", wantMethod, wantPath, got)
	}
}

// TestTransactionsRouteNot404 confirms the path actually resolves at request
// time: an unauthenticated GET must reach the middleware/handler chain, never
// gin's "404 no route". (Auth makes it 401/400 here — the point is: not 404.)
func TestTransactionsRouteNot404(t *testing.T) {
	engine := mountRoutes(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/transactions", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("GET /v1/billing/transactions returned 404 (no route); the route is not wired")
	}
}
