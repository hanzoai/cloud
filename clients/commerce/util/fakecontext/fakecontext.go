package fakecontext

import (
	"context"
	"encoding/gob"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

// Request that can be almost completely be serialized to/from a real Request
type Request struct {
	Close            bool
	ContentLength    int64
	Form             url.Values
	Header           http.Header
	Host             string
	Method           string
	PostForm         url.Values
	Proto            string
	ProtoMajor       int
	ProtoMinor       int
	RemoteAddr       string
	RequestURI       string
	Trailer          http.Header
	TransferEncoding []string
}

func (r Request) Request() (req *http.Request, err error) {
	req = &http.Request{
		Close:            r.Close,
		ContentLength:    r.ContentLength,
		Form:             r.Form,
		Header:           r.Header,
		Host:             r.Host,
		Method:           r.Method,
		PostForm:         r.PostForm,
		Proto:            r.Proto,
		ProtoMajor:       r.ProtoMajor,
		ProtoMinor:       r.ProtoMinor,
		RemoteAddr:       r.RemoteAddr,
		RequestURI:       r.RequestURI,
		Trailer:          r.Trailer,
		TransferEncoding: r.TransferEncoding,
	}

	// Rebuild URL object
	rawurl := r.RequestURI
	justAuthority := r.Method == "CONNECT" && !strings.HasPrefix(rawurl, "/")
	if justAuthority {
		rawurl = "http://" + rawurl
	}

	if req.URL, err = url.ParseRequestURI(rawurl); err != nil {
		return nil, err
	}

	return req, nil
}

func NewRequest(r *http.Request) *Request {
	return &Request{
		Close:            r.Close,
		ContentLength:    r.ContentLength,
		Form:             r.Form,
		Header:           r.Header,
		Host:             r.Host,
		Method:           r.Method,
		PostForm:         r.PostForm,
		Proto:            r.Proto,
		ProtoMajor:       r.ProtoMajor,
		ProtoMinor:       r.ProtoMinor,
		RemoteAddr:       r.RemoteAddr,
		RequestURI:       r.RequestURI,
		Trailer:          r.Trailer,
		TransferEncoding: r.TransferEncoding,
	}
}

// gin.Context replacement that can be almost completely be serialized to/from
// a gin.Context
type Context struct {
	Keys    map[string]interface{}
	Params  gin.Params
	Request *Request
}

func (c *Context) cloneKeys(keys map[any]any) {
	for k, v := range keys {
		// Convert key to string (gin v1.11+ uses map[any]any)
		keyStr, ok := k.(string)
		if !ok {
			continue
		}

		// Skip context keys that cannot be serialized
		if keyStr == "context" {
			continue
		}

		// save organization id so we can fetch it on the other side
		if keyStr == "organization" {
			c.Keys["organization-id"] = (v.(*organization.Organization)).Id()
			continue
		}

		c.Keys[keyStr] = v
	}
}

func (c Context) Context(ctx context.Context) (ginCtx *gin.Context, err error) {
	ginCtx = new(gin.Context)
	ginCtx.Errors = ginCtx.Errors[0:0]
	// Convert map[string]interface{} to map[any]any for gin v1.11+
	ginCtx.Keys = make(map[any]any, len(c.Keys))
	for k, v := range c.Keys {
		ginCtx.Keys[k] = v
	}
	ginCtx.Params = c.Params

	ginCtx.Request, err = c.Request.Request()
	if err != nil {
		log.Warn("Failed to create Request from Request: %v", err)
	}

	// If we don't have a context, this is all we can do for now
	if ctx == nil {
		return ginCtx, err
	}

	// ...otherwise use context to update gin context
	ginCtx.Set("context", ctx)

	// Fetch organization if organization-id is set
	if value, ok := ginCtx.Get("organization-id"); !ok {
		if id, ok := value.(string); ok {
			db := datastore.New(ctx)
			org := organization.New(db)
			org.GetById(id)
			ginCtx.Set("organization", org)
		}
	}
	return ginCtx, err
}

func NewContext(c *gin.Context) *Context {
	ctx := new(Context)

	ctx.Keys = make(map[string]interface{}, 0)

	ctx.Params = c.Params

	// Need to create request context, because c.Request cannot be gob-encoded
	if c.Request != nil {
		ctx.Request = NewRequest(c.Request)
	} else {
		ctx.Request = &Request{}
	}

	// Clone keys, skipping context (can't gob encode, also no point)
	ctx.cloneKeys(c.Keys)

	return ctx
}

func init() {
	gob.Register(&Context{})
}
