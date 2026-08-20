// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// Using a credential without holding it.
//
// Sign already lets a caller use a private key it never sees: hand over a
// payload, get back a signature. A cloud credential wants that same shape —
// the caller asks for the effect ("list the droplets") and the token stays
// here. One credential, one home, and every use of it carries a named caller
// in our own record, which matters because the vendors issue no per-token
// attribution of their own: a leaked key cannot be traced back from their side.
//
// The destination is NOT a request parameter, and that is the whole security
// argument. A caller who could name the host could name one they run, and read
// the credential straight out of the request header — the exfiltration would BE
// the feature. So the reachable host comes from the table below, keyed by the
// secret's own path. Holding write access to a secret does not let you redirect
// it.

// vendor is the one host a credential may reach and how it is presented there.
type vendor struct {
	host   string
	header string
	format string
}

// A secret stored at <...>/<vendor> is callable when <vendor> appears here.
// This table is source, not data, so it cannot be edited by anyone who can
// merely write secrets.
var vendors = map[string]vendor{
	"digitalocean": {host: "api.digitalocean.com", header: "Authorization", format: "Bearer %s"},
}

// Verbs a caller may ask for. An allowlist rather than a denylist so a new
// method is a decision, not an accident.
var verbs = map[string]bool{
	http.MethodGet:    true,
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
	http.MethodHead:   true,
}

type callRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   string `json:"body,omitempty"`
}

// out never follows a redirect: a 3xx can name a host this credential is not
// allowed to reach, and following it would carry the header there. The caller
// sees the 3xx and decides.
var out = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// reachable accepts a path and rejects anything that could name an authority.
// A leading "//" is scheme-relative and resolves to a different host, and any
// "://" carries a scheme of its own; both would defeat the fixed host.
func reachable(p string) bool {
	return strings.HasPrefix(p, "/") &&
		!strings.HasPrefix(p, "//") &&
		!strings.Contains(p, "://") &&
		strings.IndexFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}

// vendorOf reads the vendor from the secret's own path — its last segment.
func vendorOf(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

// compose builds the outbound request. The host comes from the vendor and the
// caller contributes only a path, so there is no input through which the
// credential can be aimed somewhere else.
func compose(ctx context.Context, v vendor, cred, method, path, body string) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, "https://"+v.host+path, bytes.NewReader([]byte(body)))
	if err != nil {
		return nil, err
	}
	r.Header.Set(v.header, fmt.Sprintf(v.format, cred))
	r.Header.Set("Content-Type", "application/json")
	return r, nil
}

// call performs one request against the credential's vendor and returns the
// vendor's status and body. The credential is never part of the answer.
func call(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	env := envOr(ctx.Query("env"))
	if !validEnv(env) {
		return zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	path, name, ok := targetOf(org, reqWildcard(ctx))
	if !ok {
		return zip.ErrBadRequest("secret name is required and must be a clean '/'-separated path")
	}
	v, known := vendors[vendorOf(path)]
	if !known {
		return zip.ErrBadRequest("secret is not callable: its path names no known vendor")
	}

	var req callRequest
	if err := json.Unmarshal(ctx.Body(), &req); err != nil {
		return zip.Errorf(http.StatusBadRequest, "invalid JSON body: %v", err)
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if !verbs[method] {
		return zip.ErrBadRequest("'method' must be one of GET, POST, PUT, PATCH, DELETE, HEAD")
	}
	if !reachable(req.Path) {
		return zip.ErrBadRequest("'path' must be an absolute path on the vendor, not a URL")
	}

	cred, err := s.State.kms.Get(path, name, env)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return zip.ErrNotFound("secret not found")
		}
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}

	r, err := compose(ctx.Context(), v, string(cred), method, req.Path, req.Body)
	if err != nil {
		return zip.Errorf(http.StatusBadRequest, "%v", err)
	}

	resp, err := out.Do(r)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}

	// The record the vendor cannot give us: who asked, for what, and what came
	// back. Never the path's value, and never the credential.
	s.Log.Info("kms call",
		"org", org, "vendor", vendorOf(path), "method", method,
		"path", req.Path, "status", resp.StatusCode,
	)
	return ctx.JSON(http.StatusOK, map[string]any{
		"status": resp.StatusCode,
		"body":   string(body),
	})
}
