package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/events"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
)

// product_events.go publishes catalog lifecycle events from the product REST path —
// the reverse half of the storefront loop (the content lane consumes product.created
// to auto-render the product's ecom asset, keyed by design == slug).
//
// It mirrors the order publishers (checkout/authorize.go): read the process Publisher
// off the gin context, fire the event as a fire-and-forget goroutine, and be a total
// no-op when no publisher is wired (NATS absent) — never blocking or failing the write.
// It is route middleware rather than a custom handler so the generic REST create/update
// stays the ONE write path; the middleware only observes its outcome.

// publishProductEvents is product-route middleware: after a successful create/replace it
// fires the matching commerce.product.* event. The body is peeked BEFORE the handler
// runs (the handler consumes it) and restored; the event is sent only when the write
// actually succeeded (2xx). Fail-safe throughout: a missing publisher, an unresolved
// org, a body without a slug, or a non-2xx is a silent no-op.
func publishProductEvents(c *gin.Context) {
	subject := writeSubject(c)
	var body productBody
	if subject != "" {
		body = peekProductBody(c) // buffers + restores c.Request.Body for the handler
	}

	c.Next()

	if subject == "" || body.Slug == "" {
		return
	}
	if st := c.Writer.Status(); st < 200 || st >= 300 {
		return // the create/update did not commit — publish nothing
	}
	pub, ok := c.Get("publisher")
	if !ok {
		return
	}
	p, ok := pub.(*events.Publisher)
	if !ok || p == nil {
		return
	}
	org, ok := middleware.GetOrganizationOK(c)
	if !ok || org == nil {
		return
	}
	orgName := org.Name
	productID := eventProductID(c)
	go func() {
		ctx := context.Background()
		var err error
		if subject == events.SubjectProductCreated {
			err = p.PublishProductCreated(ctx, orgName, productID, body.Slug, body.Name, body.SKU)
		} else {
			err = p.PublishProductUpdated(ctx, orgName, productID, body.Slug, body.Name, body.SKU)
		}
		if err != nil {
			log.Error("publish catalog event %q: %v", subject, err, c)
		}
	}()
}

// writeSubject classifies the request into the catalog event it should emit, or "" when
// it is not a create/replace. POST to the collection is a create; PUT/PATCH to an item
// is a replace/patch. POST to an item (the legacy method-override) is deliberately NOT
// classified — its effective verb (patch/update/delete) is ambiguous here, so it is left
// to callers using the explicit verbs.
func writeSubject(c *gin.Context) string {
	switch c.Request.Method {
	case http.MethodPost:
		if strings.TrimSpace(c.Param("productid")) == "" {
			return events.SubjectProductCreated
		}
		return ""
	case http.MethodPut, http.MethodPatch:
		return events.SubjectProductUpdated
	default:
		return ""
	}
}

// productBody is the minimal slice of a product write the event carries: the slug is the
// join key content renders against (design == slug); name/sku are context.
type productBody struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	SKU  string `json:"sku"`
}

// peekProductBody reads and restores the request body so both this middleware and the
// downstream handler can decode it. A read/decode error yields the zero body (no event),
// never a failed request.
func peekProductBody(c *gin.Context) productBody {
	if c.Request.Body == nil {
		return productBody{}
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	_ = c.Request.Body.Close()
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return productBody{}
	}
	var b productBody
	_ = json.Unmarshal(raw, &b)
	b.Slug = strings.TrimSpace(b.Slug)
	return b
}

// eventProductID resolves the product id for the event: the item param on an update, or
// the id the create handler wrote into the Location header (path/<id>). Best-effort — the
// consumer joins on slug, so an empty id is harmless.
func eventProductID(c *gin.Context) string {
	if id := strings.TrimSpace(c.Param("productid")); id != "" {
		return id
	}
	loc := c.Writer.Header().Get("Location")
	if i := strings.LastIndexByte(loc, '/'); i >= 0 {
		return strings.TrimSpace(loc[i+1:])
	}
	return ""
}
