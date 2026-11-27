// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package event

// sourcemap_upload.go — the one way a sourcemap enters the plane.
//
//	PUT /v1/event/sourcemaps?release=<release>&file=<served path>   body: the .map
//
// A build uploads the map of each generated script under the release it stamped
// into the bundle; ingest reads it back to map that release's stack frames
// (symbolicate.go). There is no read route. A map is the org's source code, and the
// only reader is the ingest path in this process.
//
// WHO MAY UPLOAD: THE ORG'S OWN BUILD IDENTITY, AND NOBODY ELSE. The credential is
// an IAM application acting as itself — the token a pipeline run already holds. On
// GitHub, hanzoai/ci exchanges the run's own ID token for the org's CI application
// (IAM_REPOSITORIES); on Hanzo Git, the run logs in as its org's client. Either is
// the same kind of token the build door (/v1/build) admits as "that organization's
// own machine identity", and it is admitted here on the same terms: the org is read
// off the credential, the app cannot choose it, and the maps land under that org and
// no other. So there is no per-repository upload secret to mint, store or rotate.
//
// A person is refused, an org admin included: a map belongs to a build, and a build
// that says what it uploaded is the record worth having. An org API key is refused
// for the same reason, and because it resolves to a person. A token that names an
// actor (an operator acting as the app) is refused. A query naming any org other
// than the credential's is refused outright rather than ignored, so a pipeline
// misconfigured to write elsewhere hears about it.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/object"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// sourcemapPath is the address.
const sourcemapPath = "/v1/event/sourcemaps"

// maxRelease bounds the release a map is filed under. A git sha is 40, a tag a few
// dozen; anything longer is not a release.
const maxRelease = 128

// sourcemapReceipt is what an upload answers.
type sourcemapReceipt struct {
	// Release is the release the map was filed under, as sent.
	Release string `json:"release"`
	// File is the served path of the generated script the map belongs to, with any
	// scheme, host, query and fragment removed — the same reading ingest gives a
	// stack frame's file.
	File string `json:"file"`
	// Bytes is the size of the stored map.
	Bytes int `json:"bytes"`
}

// validRelease reports whether r can name a release: 1 to maxRelease printable
// ASCII characters with no space.
func validRelease(r string) bool {
	if r == "" || len(r) > maxRelease {
		return false
	}
	for i := 0; i < len(r); i++ {
		if r[i] <= ' ' || r[i] > '~' {
			return false
		}
	}
	return true
}

// buildOrg is the org a sourcemap upload is admitted for, or the refusal.
func buildOrg(c *zip.Ctx) (string, error) {
	org, ok := principal.Org(c)
	if !ok {
		return "", principal.Refused(c)
	}
	if !principal.IsApp(c) || strings.TrimSpace(c.Header(zip.HeaderActedBy)) != "" {
		return "", zip.ErrForbidden("sourcemaps are uploaded by the org's build identity (its CI application token), not by a person or an API key")
	}
	if named := strings.TrimSpace(c.Query("org")); named != "" && named != org {
		return "", zip.ErrForbidden("this credential uploads sourcemaps for its own org only")
	}
	return org, nil
}

// sourcemapUpload stores one map. Credential first, then the address, then the
// body, so a caller is told the first thing wrong with its request.
func sourcemapUpload(_ *cloud.Service[state], c *zip.Ctx) error {
	org, err := buildOrg(c)
	if err != nil {
		return err
	}
	release := strings.TrimSpace(c.Query("release"))
	if !validRelease(release) {
		return zip.ErrBadRequest("release is required: the git sha (or tag) the bundle was stamped with, at most 128 characters, no spaces")
	}
	file := servedPath(c.Query("file"))
	if file == "" {
		return zip.ErrBadRequest("file is required: the path the generated script is served at, such as /assets/index-3f9a.js (a full URL is accepted)")
	}
	body := c.Body()
	if len(body) == 0 {
		return zip.ErrBadRequest("empty upload: the body is the .map file")
	}
	if len(body) > maxMapBytes {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "sourcemap too large: %d bytes, limit %d", len(body), maxMapBytes)
	}
	// Parsed in full before it is stored, so ingest never meets a map it cannot read
	// and a caller hears about a bad one now rather than never.
	if _, err := parseSourceMap(body); err != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	ctx := c.Context()
	key, marker := mapKey(org, release, file), releaseKey(org, release)
	if err := sourcemaps.Put(ctx, key, body); err != nil {
		return storeUnavailable(c, err)
	}
	// The marker goes on AFTER the map, so a release is never seen as having maps
	// before the first one is readable.
	if err := sourcemaps.Put(ctx, marker, []byte(release)); err != nil {
		return storeUnavailable(c, err)
	}
	mapCache.forget(key, marker)
	return c.JSON(http.StatusCreated, sourcemapReceipt{Release: release, File: file, Bytes: len(body)})
}

func storeUnavailable(c *zip.Ctx, err error) error {
	if !errors.Is(err, object.ErrDisabled) {
		c.Log().Warn("sourcemap store write failed", "err", err)
	}
	return zip.Errorf(http.StatusServiceUnavailable, "sourcemap storage unavailable")
}

func init() {
	spec.Register(sourcemapPath, http.MethodPut, spec.Binary{}, sourcemapReceipt{})
	spec.Params(sourcemapPath, http.MethodPut, []map[string]any{
		{"name": "release", "in": "query", "required": true, "schema": map[string]any{"type": "string", "maxLength": maxRelease},
			"description": "The release the bundle was stamped with: the git sha of the build, the same value its error events send as `release`."},
		{"name": "file", "in": "query", "required": true, "schema": map[string]any{"type": "string"},
			"description": "The path the generated script is served at, such as /assets/index-3f9a.js. A full URL is accepted; scheme, host, query and fragment are dropped."},
	})
	spec.Describe(sourcemapPath, http.MethodPut,
		"Upload a sourcemap for a release",
		"Stores the sourcemap of one generated script under a release, so browser errors from that release "+
			"are reported, and grouped, by their original file, line and function. Frames are mapped when an "+
			"error event is ingested, before it is grouped, so a fault keeps one issue across deploys that "+
			"rebuild its bundle.\n\n"+
			"The body is the .map file (Source Map v3; index maps with `sections` are refused 422). `release` "+
			"is the value the bundle sends as its events' `release` — the build's git sha — and `file` is the "+
			"path the generated script is served at. Uploading the same release and file again replaces the map.\n\n"+
			"MAPS ARE PRIVATE. They are kept in the org's storage and never served; there is no route that reads "+
			"one back. A build should remove the .map files from what it publishes once they are uploaded.\n\n"+
			"WHO MAY CALL: the org's own build identity — an IAM application token acting as itself, such as the "+
			"CI application a hanzoai/ci run exchanges its ID token for. The map is filed under the org the "+
			"token belongs to; naming another org is 403. A person's token, an org API key, and a token acting "+
			"for someone else are 403; no credential is 401.\n\n"+
			"BOUNDS: 413 over 15 MiB of body. 503 when storage is unavailable.")
}
