// Copyright (c) 2026 Hanzo AI Inc.

package s3

// blob.go carries an object's BYTES through this binary, which is the half of
// the file plane a presigned URL cannot serve when there is nowhere public to
// sign against.
//
// PRESIGNING IS STILL THE DEFAULT and stays the better path where it works: the
// browser talks to the store directly, the bytes never touch this process, and a
// large upload costs the API nothing. It buys that with a requirement — a host
// the BROWSER can reach that is also the store. A deployment whose object store
// is reachable only inside the cluster has no such host, and s3admin already
// names the consequence: "callers must not offer presigned upload or download
// (they degrade to a server-streamed path or an honest error)." Only the error
// half existed, so a file plane with no public store answered 503 to every
// upload and every download — a Drive that lists and cannot carry a byte.
//
// This is the other half. The bytes ride the ONE public door the deployment
// already has, and the two mints answer with this address instead of refusing.
// A caller does not choose between them and does not learn which it got: it
// receives {url, method} and follows it, exactly as before.

import (
	"bytes"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/hanzoai/cloud/internal/fare"
	"github.com/hanzoai/cloud/openapi"
	s3 "github.com/hanzos3/go"
	"github.com/zap-proto/zip"
)

// maxBlob bounds one streamed body. The presigned path has no such ceiling —
// there the store takes the bytes and this process never holds them — so the
// bound belongs here, where they do pass through, and is what keeps one upload
// from sitting in this binary's memory unbounded.
const maxBlob = 64 << 20

// blobPath is where an object's bytes live, and it is a DIFFERENT address from
// the object's record. /objects/<key> is the thing — list it, delete it, mint a
// URL for it — and /blob/<key> is its contents. Two nouns because they answer
// two questions: apps/team draws the same line between a file's row and its
// bytes, and collapsing them would put two response types on one address.
const blobPath = "/buckets/:bucket/blob/+"

// bytePath is blobPath as the ROUTER holds it — the group prefix composed in.
// Describe keys on the router's path and not the document's: the doc renders
// ":bucket" as "{bucket}" and "+" as "{wildcard1}" for display, but the lookup
// that attaches prose runs before that rendering, against the address the route
// was actually registered at.
const bytePath = "/v1/s3" + blobPath

// putBlob writes a request body into one object.
//
// The org comes from the admission this handler is wrapped in, never from the
// request, so the bucket a caller names is resolved inside their own tenant and a
// key cannot address another's.
func (o ops) putBlob(c *zip.Ctx) error {
	org, bucket, key, err := o.addressed(c)
	if err != nil {
		return err
	}
	cl, err := o.s.State.admin.Client()
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	// The BODY is what gets stored, so the bound is measured on it rather than on
	// Content-Length, which is the client's claim about it.
	data := c.Body()
	if len(data) > maxBlob {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "file too large (max %d bytes)", maxBlob)
	}
	if len(data) == 0 {
		return zip.ErrBadRequest("empty upload")
	}
	kind := strings.TrimSpace(c.Header("Content-Type"))
	if kind == "" {
		kind = "application/octet-stream"
	}
	if _, err := cl.PutObject(c.Context(), physicalBucket(org, bucket), key,
		bytes.NewReader(data), int64(len(data)),
		s3.PutObjectOptions{ContentType: kind}); err != nil {
		return zip.Errorf(http.StatusBadGateway, "object storage unavailable")
	}
	return c.JSON(http.StatusOK, map[string]any{"key": key, "size": len(data)})
}

// getBlob answers one object's bytes.
//
// Served INERT — octet-stream, attachment, nosniff — because the type would
// otherwise be the uploader's claim about someone else's download, and a store
// this general holds whatever an org put in it. The same rule apps/team applies
// to a space blob, for the same reason.
func (o ops) getBlob(c *zip.Ctx) error {
	org, bucket, key, err := o.addressed(c)
	if err != nil {
		return err
	}
	cl, err := o.s.State.admin.Client()
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	obj, err := cl.GetObject(c.Context(), physicalBucket(org, bucket), key, s3.GetObjectOptions{})
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "object storage unavailable")
	}
	defer func() { _ = obj.Close() }()
	// A miss is only known on the READ with this client — GetObject is lazy — so
	// the 404 is here rather than on a stat that would double the round trips.
	data, err := io.ReadAll(io.LimitReader(obj, maxBlob+1))
	if err != nil {
		return zip.ErrNotFound("object not found")
	}
	c.SetHeader("X-Content-Type-Options", "nosniff")
	c.SetHeader("Content-Type", "application/octet-stream")
	c.SetHeader("Content-Disposition", `attachment; filename="`+path.Base(key)+`"`)
	return c.Bytes(http.StatusOK, data)
}

// addressed resolves the three things both handlers open with — whose org, which
// bucket, which key — refusing before the store is touched. One reader, so the
// two cannot come to disagree about what a caller may address.
func (o ops) addressed(c *zip.Ctx) (org, bucket, key string, err error) {
	org, err = fare.Org(c.Context())
	if err != nil {
		return "", "", "", err
	}
	bucket, ok := friendlyParam(c.Param("bucket"))
	if !ok {
		return "", "", "", zip.ErrBadRequest("invalid bucket name")
	}
	key, ok = cleanKey(c.Param("+1"))
	if !ok {
		return "", "", "", zip.ErrBadRequest("key is required and must be a clean object path")
	}
	return org, bucket, key, nil
}

// The two byte routes state their prose HERE, beside the route, because zipdoc
// lifts a doc comment only from a typed op and these are raw by construction —
// the same shape apps/agents uses for the conversation surface. Describe is keyed
// on (method, path) and renders only while the router actually serves the route,
// so it cannot invent an operation.
func init() {
	openapi.Describe(bytePath, http.MethodPut,
		"Write one object's bytes",
		"Stores the request body as the object at this address and answers its key and "+
			"size. The body IS the object: whatever is sent is what is stored, and the "+
			"declared Content-Type is kept with it.\n\n"+
			"This is the address POST /v1/s3/buckets/{bucket}/objects hands back on a "+
			"deployment whose object store has no browser-routable host. Where one exists "+
			"that mint answers a presigned URL against the store instead and the bytes never "+
			"pass through this API — the caller follows {url, method} either way and does not "+
			"choose.\n\n"+
			"Bounded: a body over the limit is refused 413 rather than held. An empty body is "+
			"refused — a zero-byte object is almost always a failed read upstream.")
	openapi.Describe(bytePath, http.MethodGet,
		"Read one object's bytes",
		"Answers the object's stored bytes.\n\n"+
			"Served INERT — application/octet-stream, attachment, nosniff — because the "+
			"content type would otherwise be one caller's claim about another caller's "+
			"download, and this store holds whatever an org put in it. A caller that knows "+
			"what it stored reads the bytes; a browser saves them rather than rendering "+
			"them.\n\n"+
			"The counterpart of the PUT above, and the address GET "+
			"/v1/s3/buckets/{bucket}/objects/{key} hands back where there is no public store "+
			"to sign against.")
}
