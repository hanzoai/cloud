package rest

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/reflect"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
	"github.com/hanzoai/cloud/clients/commerce/util/search"
)

var restApis = make([]*Rest, 0)

type route struct {
	url      string
	method   string
	handlers []gin.HandlerFunc
}

type Opts struct {
	DefaultNamespace bool
	DefaultSortField string
}

type routeMap map[string](map[string]route)

type Rest struct {
	DefaultNamespace bool
	DefaultSortField string
	Kind             string
	ParamId          string
	Prefix           string
	Permissions      Permissions
	Get              gin.HandlerFunc
	List             gin.HandlerFunc
	Create           gin.HandlerFunc
	Update           gin.HandlerFunc
	Patch            gin.HandlerFunc
	Delete           gin.HandlerFunc
	MethodOverride   gin.HandlerFunc

	middleware []gin.HandlerFunc
	routes     routeMap

	entityType reflect.Type
	sliceType  reflect.Type
}

type Pagination struct {
	Page    string                 `json:"page,omitempty"`
	Display string                 `json:"display,omitempty"`
	Count   int                    `json:"count"`
	Models  interface{}            `json:"models"`
	Facets  [][]search.FacetResult `json:"facets"`
}

func (r *Rest) Init(prefix string) {
	r.Prefix = prefix
	r.routes = make(routeMap)
}

func (r *Rest) InitModel(entity mixin.Kind) {
	// Get type of entity
	r.entityType = reflect.ValueOf(entity).Type()
	ptrType := reflect.ValueOf(r.newKind()).Type()
	r.sliceType = reflect.SliceOf(ptrType)
	r.Kind = r.newKind().Kind()
	r.ParamId = r.Kind + "id"
	r.routes = make(routeMap)

	if r.DefaultSortField != "" {
		return
	}

	// Introspect model to determine default sort field
	for _, name := range reflect.FieldNames(entity) {
		if name == "Slug" || name == "SKU" {
			r.DefaultSortField = name
			return
		}
	}

	// Use Id_ as default sort field if nothing is specified.
	if r.DefaultSortField == "" {
		r.DefaultSortField = "UpdatedAt"
	}
}

func New(entityOrPrefix interface{}, args ...interface{}) *Rest {
	r := new(Rest)
	r.routes = make(routeMap)

	if len(args) > 0 {
		opts := args[0].(Opts)
		r.DefaultNamespace = opts.DefaultNamespace
		r.DefaultSortField = opts.DefaultSortField
	}

	switch v := entityOrPrefix.(type) {
	case string:
		r.Init(v)
	case mixin.Kind:
		r.InitModel(v)
		restApis = append(restApis, r) // Keep track of all APIs globally
	}

	return r
}

var Namespaced = middleware.Namespace()

func (r *Rest) Route(router router.Router, mw ...gin.HandlerFunc) {
	prefix := r.Prefix + r.Kind
	prefix = "/" + strings.TrimLeft(prefix, "/")

	// Create group for our API routes
	group := router.Group(prefix)

	mw = append(r.middleware, mw...)

	if !r.DefaultNamespace {
		// Automatically namespace requests
		mw = append(mw, Namespaced)
	}

	// Setup default permissions
	if r.Permissions == nil {
		r.Permissions = DefaultPermissions[r.Kind]
	}

	// Add default routes
	for _, route := range r.defaultRoutes() {
		// log.Debug("%-7s %v", route.method, prefix+route.url)
		group.Handle(route.method, route.url, append(mw, route.handlers...)...)
	}

	// Custom sub-routes keep their OWN handler chain (each already carries the
	// middleware it needs, e.g. an explicit Namespace). We deliberately do NOT
	// blanket-apply the base-CRUD `mw` here: the base middleware is the CRUD's
	// authz (often adminRequired), which is the WRONG gate for sub-routes meant to
	// be caller-scoped rather than admin-only (e.g. GET /user/:id/wallet) —
	// forcing it 401/403s legitimate access.
	//
	// Red HIGH-4 (money sub-routes reachable by non-admin) is closed at the
	// authoritative layer instead: every money-moving handler calls
	// middleware.RequireAdmin FIRST (IAM-aware, and fail-closed even when no
	// route-level token middleware ran) — giftcard Redeem/Void, checkout Refund,
	// b2b Accept/Reject/Approve, wallet Send, transaction Create/Hold, wire
	// Credit. That is stricter and more precise than propagating the base gate,
	// and it works on the IAM path where TokenRequired(Admin) no-ops.
	for _, routes := range r.routes {
		for _, route := range routes {
			// log.Debug("%-7s %v", route.method, prefix+route.url)
			group.Handle(route.method, route.url, route.handlers...)
		}
	}
}

func (r Rest) CheckPermissions(c *gin.Context, method string) bool {
	// Get permissions of current token
	tok := middleware.GetPermissions(c)

	// Lookup permission
	permissions, ok := r.Permissions[method]

	// Unsupported method, need to define permissions
	if !ok {
		// TODO: Use more strict checks
		// msg := "Unsupported method for API access"
		// r.Fail(c, 500, msg, errors.New(msg))
		// return false
		msg := fmt.Sprintf("No permissions found matching method: '%s', skipping permission check.", method)
		log.Warn(msg, c)
		return true
	}

	// See if token matches any of the supported permissions
	for _, perm := range permissions {
		if tok.Has(perm) {
			return true
		}
	}

	// Token lacks valid permission
	msg := "Token lacks permission to " + method + " " + r.Kind
	r.Fail(c, 403, msg, errors.New(msg))
	return false
}

func (r Rest) defaultRoutes() []route {
	if r.Kind == "" {
		// Only supported on model APIs
		return []route{}
	}

	// Setup default handlers
	if r.Get == nil {
		r.Get = r.get
	}

	if r.List == nil {
		r.List = r.list
	}

	if r.Create == nil {
		r.Create = r.create
	}

	if r.Update == nil {
		r.Update = r.update
	}

	if r.Patch == nil {
		r.Patch = r.patch
	}

	if r.Delete == nil {
		r.Delete = r.delete
	}

	if r.MethodOverride == nil {
		r.MethodOverride = r.methodOverride
	}

	return []route{
		route{
			method:   "POST",
			url:      "",
			handlers: []gin.HandlerFunc{r.Create},
		},
		route{
			method:   "GET",
			url:      "",
			handlers: []gin.HandlerFunc{r.List},
		},
		route{
			method:   "GET",
			url:      "/:" + r.ParamId,
			handlers: []gin.HandlerFunc{r.Get},
		},
		route{
			method:   "PUT",
			url:      "/:" + r.ParamId,
			handlers: []gin.HandlerFunc{r.Update},
		},
		route{
			method:   "DELETE",
			url:      "/:" + r.ParamId,
			handlers: []gin.HandlerFunc{r.Delete},
		},
		route{
			method:   "POST",
			url:      "/:" + r.ParamId,
			handlers: []gin.HandlerFunc{r.MethodOverride},
		},
		route{
			method:   "PATCH",
			url:      "/:" + r.ParamId,
			handlers: []gin.HandlerFunc{r.Patch},
		},
	}
}

func (r Rest) newKind() mixin.Kind {
	return reflect.New(r.entityType).Interface().(mixin.Kind)
}

// Returns a new interface of this entity type
func (r Rest) newEntity(c *gin.Context) mixin.Entity {
	ctx := middleware.GetContext(c)

	// Create a new entity bound to the CALLER ORG's own per-org store. Every
	// generic REST merchant model (product/order/user/store/collection/discount/
	// variant/…) is physically isolated per org: NewNamespaced routes reads AND
	// writes to db.Manager.Org(<caller org>) keyed by the namespace the auth
	// middleware resolved from the gateway/EdgeAuth-minted X-Org-Id (verified JWT
	// owner). Global/DefaultNamespace kinds (no namespace) fall back to the shared
	// default DB; an unresolvable namespace fails closed (no DB), never the pool.
	db := datastore.NewNamespaced(ctx)
	entity := reflect.New(r.entityType).Interface().(mixin.Entity)

	// Wire up mixin.BaseModel if the entity uses the legacy embedding.
	// Model[T]-based models are wired via Init() instead.
	// Note: embedded mixin.BaseModel fields are named "BaseModel" in Go reflection.
	val := reflect.Indirect(reflect.ValueOf(entity))
	baseModelType := reflect.TypeOf(mixin.BaseModel{})
	if field := val.FieldByName("BaseModel"); field.IsValid() && field.Type() == baseModelType {
		model := mixin.BaseModel{Db: db, Entity: entity}

		// Disable Put/Delete if in test mode
		if middleware.GetPermissions(c).Has(permission.Test) {
			model.Mock = false // force mock off due to testing issues
		}

		field.Set(reflect.ValueOf(model))
	}

	// Initialize entity (works for both BaseModel and Model[T] models)
	entity.Init(db)

	return entity
}

// helper which returns a slice which is compatible with this entity
func (r Rest) newEntitySlice(length, capacity int) interface{} {
	// Create pointer to a slice value and set it to the slice
	slice := reflect.MakeSlice(r.sliceType, length, capacity)
	for i := 0; i < length; i++ {
		slice.Index(i).Set(reflect.New(r.entityType))
	}

	ptr := reflect.New(slice.Type())
	ptr.Elem().Set(slice)
	return ptr.Interface()
}

func (r Rest) Render(c *gin.Context, status int, data interface{}) {
	http.Render(c, status, data)
}

func (r Rest) Fail(c *gin.Context, status int, message interface{}, err error) {
	http.Fail(c, status, message, err)
}

func (r Rest) get(c *gin.Context) {
	if !r.CheckPermissions(c, "get") {
		return
	}

	id := c.Params.ByName(r.ParamId)

	entity := r.newEntity(c)

	if err := entity.GetById(id); err != nil {
		// TODO: When is this a 404?
		r.Fail(c, 404, "Failed to get "+r.Kind, err)
	} else {
		r.Render(c, 200, entity)
	}
}

// list returns a page of this kind scoped to the caller's org.
//
// SECURITY — per-tenant isolation. listBasic reads the datastore
// `WHERE namespace = <ns>`, where <ns> is the namespace middleware.Namespace()
// derives from the authenticated, gateway/EdgeAuth-minted X-Org-Id (a
// JWT-verified `owner` claim). That is the SAME scoping get/create/update/delete
// already apply, so a caller lists ONLY its own org's rows. No search backend is
// wired (search.Open returns a no-op empty index — historically every list came
// back empty), so the datastore is the one and only list path.
//
// The namespace IS the owner filter, so require it explicitly here: a request
// with no resolved org namespace (DefaultNamespace/global kinds, or any route
// that did not pass Namespace()) is NEVER served an un-scoped full-table scan —
// that would cross tenants. Fail closed to an empty page; those kinds stay
// reachable by id via get.
func (r Rest) list(c *gin.Context) {
	if !r.CheckPermissions(c, "list") {
		return
	}

	query := c.Request.URL.Query()

	// Default sort order.
	sortField := query.Get("sort")
	if sortField == "" {
		sortField = r.DefaultSortField
	}

	pageStr := query.Get("page")
	displayStr := query.Get("display")
	limitStr := query.Get("limit")

	entity := r.newEntity(c)

	// Fail closed unless the request carries a concrete org namespace — the
	// exact value listBasic scopes every query by. Empty ⇒ no per-tenant scope,
	// so serve an empty page rather than risk crossing tenants.
	if nscontext.GetNamespace(entity.Context()) == "" {
		r.Render(c, 200, Pagination{
			Page:    pageStr,
			Display: displayStr,
			Models:  r.newEntitySlice(0, 0),
			Count:   0,
			Facets:  [][]search.FacetResult{},
		})
		return
	}

	r.listBasic(c, entity, pageStr, displayStr, limitStr, sortField)
}

func (r Rest) listBasic(c *gin.Context, entity mixin.Entity, pageStr, displayStr, limitStr, sortField string) {
	// Create query
	q := entity.Query().All().Order(sortField)

	var display int
	var err error

	// if we have pagination values, then trigger pagination calculations
	if displayStr != "" {
		if display, err = strconv.Atoi(displayStr); err == nil && display > 0 {
			q = q.Limit(display)
		} else {
			r.Fail(c, 500, "'display' must be positive and non-zero.", err)
			return
		}
	}

	if pageStr != "" && displayStr != "" {
		if page, err := strconv.Atoi(pageStr); err == nil && page > 0 {
			q = q.Offset(display * (page - 1))
		} else {
			r.Fail(c, 500, "'page' must be positive and non-zero.", err)
			return
		}
	}

	entities := r.newEntitySlice(0, 0)
	if _, err := q.GetAll(entities); err != nil {
		r.Fail(c, 500, "Failed to list "+r.Kind, err)
		return
	}

	count, err := entity.Query().All().Count()
	if err != nil {
		r.Fail(c, 500, "Could not count the models.", err)
		return
	}

	if limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			count = limit
		}
	}

	r.Render(c, 200, Pagination{
		Page:    pageStr,
		Display: displayStr,
		Models:  entities,
		Count:   count,
		Facets:  [][]search.FacetResult{},
	})
}

func (r Rest) create(c *gin.Context) {
	if !r.CheckPermissions(c, "create") {
		return
	}

	entity := r.newEntity(c)

	if err := json.Decode(c.Request.Body, entity); err != nil {
		r.Fail(c, 400, "Failed decode request body", err)
		return
	}

	if err := entity.Create(); err != nil {
		r.Fail(c, 500, "Failed to create "+r.Kind, err)
	} else {
		c.Writer.Header().Add("Location", c.Request.URL.Path+"/"+entity.Id())
		r.Render(c, 201, entity)
	}
}

// Completely replaces an entity for given `id`.
func (r Rest) update(c *gin.Context) {
	if !r.CheckPermissions(c, "update") {
		return
	}

	id := c.Params.ByName(r.ParamId)

	entity := r.newEntity(c)

	// Try to retrieve key from datastore
	key, ok, err := entity.IdExists(id)
	if !ok {
		if err != nil {
			r.Fail(c, 500, "Failed to retrieve key for "+id, err)
			return
		}

		r.Fail(c, 404, "No "+r.Kind+" found with id: "+id, err)
		return
	}

	// Preserve original key
	entity.SetKey(key)

	// Decode response body to create new entity
	if err := json.Decode(c.Request.Body, entity); err != nil {
		r.Fail(c, 400, "Failed decode request body", err)
		return
	}

	// Replace whatever was in the datastore with our new updated entity
	if err := entity.Update(); err != nil {
		r.Fail(c, 500, "Failed to update "+r.Kind, err)
	} else {
		r.Render(c, 200, entity)
	}
}

// Partially updates pre-existing entity by given `id`.
func (r Rest) patch(c *gin.Context) {
	if !r.CheckPermissions(c, "patch") {
		return
	}

	id := c.Params.ByName(r.ParamId)

	entity := r.newEntity(c)
	err := entity.GetById(id)
	if err != nil {
		r.Fail(c, 404, "No "+r.Kind+" found with id: "+id, err)
		return
	}

	if err := json.Decode(c.Request.Body, entity); err != nil {
		r.Fail(c, 400, "Failed decode request body", err)
		return
	}

	if err := entity.Update(); err != nil {
		r.Fail(c, 500, "Failed to update "+r.Kind, err)
	} else {
		r.Render(c, 200, entity)
	}
}

// Deletes an entity by given `id`
func (r Rest) delete(c *gin.Context) {
	if !r.CheckPermissions(c, "delete") {
		return
	}

	id := c.Params.ByName(r.ParamId)
	entity := r.newEntity(c)
	err := entity.GetById(id)
	if err != nil {
		r.Fail(c, 404, "No "+r.Kind+" found with id: "+id, err)
		return
	}

	db := entity.Datastore()
	key := db.NewIncompleteKey("deleted", nil)
	if _, err := db.Put(key, entity); err != nil {
		r.Fail(c, 500, "Failed to start deletion "+r.Kind, err)
		return
	}

	if err := entity.Delete(); err != nil {
		r.Fail(c, 500, "Failed to delete "+r.Kind, err)
	} else {
		c.Data(204, "application/json", make([]byte, 0))
	}
}

var methodOverride = middleware.MethodOverride()

// This should be handled by middleware
func (r Rest) methodOverride(c *gin.Context) {

	// Override request method
	methodOverride(c)

	switch c.Request.Method {
	case "PATCH":
		r.Patch(c)
	case "POST":
		r.Patch(c)
	case "PUT":
		r.Update(c)
	case "DELETE":
		r.Delete(c)
	default:
		r.Fail(c, 405, "Method not allowed", errors.New("Method not allowed"))
	}
}

func (r *Rest) Handle(method, url string, handlers []gin.HandlerFunc) {
	routes, ok := r.routes[url]
	if !ok {
		routes = make(map[string]route)
	}

	routes[method] = route{
		method:   method,
		url:      url,
		handlers: handlers,
	}

	r.routes[url] = routes
}

func (r *Rest) Use(handlers ...gin.HandlerFunc) {
	r.middleware = append(r.middleware, handlers...)
}

func (r *Rest) GET(url string, handlers ...gin.HandlerFunc) {
	r.Handle("GET", url, handlers)
}

func (r *Rest) POST(url string, handlers ...gin.HandlerFunc) {
	r.Handle("POST", url, handlers)
}

func (r *Rest) DELETE(url string, handlers ...gin.HandlerFunc) {
	r.Handle("DELETE", url, handlers)
}

func (r *Rest) PATCH(url string, handlers ...gin.HandlerFunc) {
	r.Handle("PATCH", url, handlers)
}

func (r *Rest) PUT(url string, handlers ...gin.HandlerFunc) {
	r.Handle("PUT", url, handlers)
}

func (r *Rest) HEAD(url string, handlers ...gin.HandlerFunc) {
	r.Handle("HEAD", url, handlers)
}

func (r *Rest) OPTIONS(url string, handlers ...gin.HandlerFunc) {
	r.Handle("OPTIONS", url, handlers)
}

func (r *Rest) LINK(url string, handlers ...gin.HandlerFunc) {
	r.Handle("LINK", url, handlers)
}

func (r *Rest) UNLINK(url string, handlers ...gin.HandlerFunc) {
	r.Handle("UNLINK", url, handlers)
}
