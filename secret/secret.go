// Package secret reads a service's OWN secret material at boot, into memory.
//
// A service needs a handful of values it cannot obtain for itself — a signing
// key, a database credential, the client secret of its own machine identity.
// The two usual homes for those are both wrong here. A Kubernetes Secret is a
// base64 field in the API server that every `kubectl get -o yaml` in the
// namespace can read, mounted into a filesystem where anything in the pod can
// read it again. An environment variable is worse: it is inherited by every
// child process, printed by every crash dumper, and shipped whole by anything
// that reports the process environment.
//
// So the material arrives over the network instead, at boot, held only in the
// process's own memory, and the caller proves who it is with the token the
// platform already vouches for: the projected ServiceAccount token at
// HANZO_SA_TOKEN. IAM exchanges that assertion for a bearer (RFC 7523,
// grant_type urn:ietf:params:oauth:grant-type:jwt-bearer), and KMS answers the
// reads that bearer's owner is scoped to. Nothing new is issued to a service to
// let it do this — the identity is the one it already has.
//
// The scope is a service's own material, and only that. Credentials for an
// upstream a request travels to are egress's business, resolved per call
// against the caller's org; this package is the boot fact — what THIS process
// needs to open its own doors, read once, before it serves anything.
//
// Boot is the whole API for a Go service. `cloud secret fetch` is the same read
// for a workload that is not Go: an init container writes the values into a
// memory-backed emptyDir the app then reads (fetch.go).
package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/environ"
)

const (
	// tokenVar names the file holding the projected ServiceAccount token —
	// the assertion this process authenticates with. The default is where the
	// platform projects it.
	tokenVar  = "HANZO_SA_TOKEN"
	tokenFile = "/var/run/secrets/hanzo/iam/token"

	// iamVar is the identity origin the assertion is exchanged at. The public
	// issuer is the default because a service outside the cluster has no other
	// address; in-cluster deployments point it at the service so the exchange
	// does not leave the cluster to come back.
	iamVar     = "HANZO_IAM_URL"
	iamHost    = "https://hanzo.id"
	tokenRoute = "/v1/iam/oauth/token"

	// kmsVar is where the values live. The default is the in-cluster KMS.
	kmsVar  = "KMS_URL"
	kmsHost = "http://kms.hanzo.svc:8010"

	// envVar selects the environment a value is resolved in: one name in two
	// environments is two secrets, and reading the wrong one is a 404 rather
	// than a wrong value, which is the failure a caller can act on.
	envVar     = "HANZO_ENV"
	envDefault = "prod"

	// devVar admits the one env fallback, for a laptop with no ServiceAccount
	// token to project. See Boot.
	devVar = "HANZO_DEV"

	// bearerGrant is RFC 7523 §2.1: a JWT presented as an authorization grant.
	// The ServiceAccount token IS that JWT, so no second credential exists to
	// be distributed, rotated or leaked.
	bearerGrant = "urn:ietf:params:oauth:grant-type:jwt-bearer"

	// maxBody bounds either upstream's answer. A token response is a few
	// hundred bytes and a secret is a key, not a corpus.
	maxBody = 1 << 20
)

// exchange is the shared client for both upstreams. Boot runs before a service
// listens, so a hung IAM or KMS must fail the boot rather than hold it open
// forever with nothing to time it out.
var exchange = &http.Client{Timeout: 15 * time.Second}

// Boot resolves each key beneath path and returns them by name.
//
// One login, one read per key, and the values never touch disk. path is the
// coordinate beneath the caller's own org root (the org comes from the bearer,
// so it is neither named here nor nameable); each key is a bare name — a
// subpath belongs in path, which is what keeps `cloud secret fetch` writing a
// file per key and never a directory.
//
// EVERY key must resolve. A missing one fails the whole Boot naming it, because
// the alternative is a map short one entry and a service that starts, serves,
// and fails at the first request that needed it.
//
// On a laptop there is no ServiceAccount token to project. With HANZO_DEV=1 and
// no token file, Boot reads the keys from the environment instead and says so.
// That is the only path by which a secret reaches this process from the
// environment; in production an absent token is an error, not a fallback.
func Boot(ctx context.Context, path string, keys ...string) (map[string]string, error) {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return nil, errors.New("secret: path is required")
	}
	if len(keys) == 0 {
		return nil, errors.New("secret: name at least one key")
	}
	for _, key := range keys {
		if !name(key) {
			return nil, fmt.Errorf("secret: %q is not a key name — a subpath belongs in path", key)
		}
	}

	file := environ.Or(tokenVar, tokenFile)
	assertion, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && environ.Or(devVar, "") == "1" {
			return fromEnv(keys)
		}
		return nil, fmt.Errorf("secret: read the service account token at %s: %w", file, err)
	}

	bearer, err := login(ctx, string(assertion))
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(keys))
	// A bearer that expired between two reads is worth exactly one more login.
	// A second 401 is a real refusal — the identity is not admitted — and
	// retrying that is how a boot loop becomes a login flood against IAM.
	relogged := false
	for _, key := range keys {
		value, status, err := read(ctx, bearer, path, key)
		if status == http.StatusUnauthorized && !relogged {
			relogged = true
			if bearer, err = login(ctx, string(assertion)); err != nil {
				return nil, err
			}
			value, _, err = read(ctx, bearer, path, key)
		}
		if err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, nil
}

// fromEnv is the development path: the keys, read from the process environment.
//
// It holds the same all-or-nothing contract as the real read, so a laptop fails
// the same way the cluster does — at boot, naming what is missing — instead of
// discovering it at the first request.
func fromEnv(keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		if !ok {
			return nil, fmt.Errorf("secret: %s is not in the environment (%s=1, no service account token at %s)", key, devVar, environ.Or(tokenVar, tokenFile))
		}
		out[key] = value
	}
	slog.Warn("secrets read from the environment: no service account token and "+devVar+"=1", "keys", len(keys))
	return out, nil
}

// login exchanges the ServiceAccount token for a bearer at IAM.
//
// The assertion is form-encoded like every other grant IAM serves, so this is
// the same endpoint and the same shape as the client_credentials exchange
// beside it — one token endpoint, one encoding, a different grant.
func login(ctx context.Context, assertion string) (string, error) {
	form := url.Values{
		"grant_type": {bearerGrant},
		"assertion":  {strings.TrimSpace(assertion)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host(iamVar, iamHost)+tokenRoute, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("secret: iam token: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := exchange.Do(req)
	if err != nil {
		return "", fmt.Errorf("secret: iam token: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("secret: iam token: status %d", resp.StatusCode)
	}
	var answer struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return "", fmt.Errorf("secret: iam token: %w", err)
	}
	if answer.AccessToken == "" {
		// IAM may answer 200 with {"error":...}; no token is an auth failure
		// whatever the status said.
		return "", errors.New("secret: iam token: the answer carried no access_token")
	}
	return answer.AccessToken, nil
}

// read GETs one value.
//
// It returns the HTTP status alongside the error so Boot can tell an expired
// bearer (401, worth one more login) from a secret that is not there (404,
// worth nothing but naming it).
func read(ctx context.Context, bearer, path, key string) (string, int, error) {
	env := environ.Or(envVar, envDefault)
	addr := host(kmsVar, kmsHost) + "/v1/kms/secrets/" + escape(path) + "/" + url.PathEscape(key) +
		"?env=" + url.QueryEscape(env)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return "", 0, fmt.Errorf("secret: read %s/%s: %w", path, key, err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")

	resp, err := exchange.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("secret: read %s/%s: %w", path, key, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound:
		return "", resp.StatusCode, fmt.Errorf("secret: %s/%s is not in kms under env %s", path, key, env)
	default:
		return "", resp.StatusCode, fmt.Errorf("secret: read %s/%s: status %d", path, key, resp.StatusCode)
	}
	var answer struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return "", resp.StatusCode, fmt.Errorf("secret: read %s/%s: %w", path, key, err)
	}
	return answer.Value, resp.StatusCode, nil
}

// name reports whether key addresses one secret rather than a place.
//
// A key is a bare name: no separator, and neither of the two names that mean a
// directory. That is what the address wants anyway — the subpath is the path's
// half — and it is also what makes `cloud secret fetch` safe by construction,
// since a name that cannot hold a separator or a `..` cannot write outside the
// directory it was given.
func name(key string) bool {
	switch key {
	case "", ".", "..":
		return false
	}
	return key == strings.TrimSpace(key) && !strings.ContainsAny(key, `/\`)
}

// host reads a base URL out of the environment without its trailing slash, so
// the routes below compose by concatenation and never produce a double slash.
func host(key, def string) string {
	return strings.TrimRight(environ.Or(key, def), "/")
}

// escape percent-encodes a `/`-separated path one segment at a time, so the
// separators survive as separators and everything else is data.
func escape(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
