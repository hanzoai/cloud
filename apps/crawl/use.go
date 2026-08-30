package crawl

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// crawlRequest is the /v1/crawl body. One URL per call: batching would make the
// response a partial-failure envelope that every caller then has to unpack, and no
// caller has asked for more than one.
//
// The three types here are named for their product rather than Request /
// Response / Document. They are DECLARED to the document (see the init below), a
// declared type's Go name IS its schema name, and that namespace is FLAT across
// the whole fleet — so the generic spelling would have claimed three of the most
// collidable names in the API for one small surface.
type crawlRequest struct {
	// URL is the page to read, absolute and http or https — no other scheme is
	// dialled. It is resolved from inside the cluster, so an address that turns out
	// to be loopback, link-local, private or multicast is refused at the dialer,
	// redirects included. Empty is not an error status: the answer comes back with
	// success false and the reason in error.
	URL string `json:"url"`
}

// crawlResult is the /v1/crawl response.
//
// Success is a field rather than an HTTP status because "the page could not be
// fetched" is a normal outcome of asking about a URL, not a fault of the request:
// the caller sent a well-formed ask and gets a well-formed answer saying the page
// was unreachable. Reserving non-2xx for auth and malformed input keeps a caller's
// error handling honest — a 200 means the surface worked, and Success says what it
// found.
type crawlResult struct {
	// Success is whether the page was fetched and read. FALSE with an Error is a
	// complete answer, not a fault — check this before reading Data.
	Success bool `json:"success"`
	// Data is the page, present exactly when Success.
	Data *crawlDocument `json:"data,omitempty"`
	// Error says what stopped the fetch: the host was refused, unreachable, or
	// served something that is not a document.
	Error string `json:"error,omitempty"`
}

// StatusCode is how this answer states the ONE non-2xx it carries as a domain
// body: a request with no url is 400 `{"success":false,"error":"missing url"}`,
// which is what the untyped handler answered and what its callers parse. Every
// other outcome — including a fetch that failed — is 200, because the surface
// worked and Success says what it found.
//
// It is the sanctioned way to spell this (zip typed.go, StatusCoder): the status
// rides the value the handler already returns, so the document publishes 400 with
// THIS schema and a generated client expects the body it will actually get. The
// alternative, returning zip.ErrBadRequest, renders the RFC 9457 problem members —
// a different body for a refusal this route has always answered in its own shape.
func (r *crawlResult) StatusCode() int {
	if r.Success {
		return http.StatusOK
	}
	if r.Error == missingURL {
		return http.StatusBadRequest
	}
	return http.StatusOK
}

// missingURL is the refusal spelled once, because [crawlResult.StatusCode]
// compares against it and the handler produces it.
const missingURL = "missing url"

// crawlDocument is the crawled page.
type crawlDocument struct {
	// URL is the address actually read, after redirects.
	URL string `json:"url"`
	// Title is the document's title, when it carried one.
	Title string `json:"title,omitempty"`
	// Markdown is the page's content, extracted and rendered to markdown. This is
	// the field to read.
	Markdown string `json:"markdown"`
	// Metadata is whatever the document said about itself — description, og:*,
	// language — plus the response status, the final URL and the content type. It
	// is an OPEN key space, so it is carried as raw JSON: `map[string]any`
	// publishes `additionalProperties:{"type":"object"}`, which the integer
	// `status` inside it refutes, while raw JSON publishes `{}` — "any JSON",
	// which is true. The bytes are identical either way (encoding/json writes a
	// map's keys sorted, and this IS that marshalling).
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// serviceKey is the shared key a service caller presents. It is deliberately the
// SAME credential the web-search surface takes (WEBSEARCH_API_KEY), not a second
// one: both surfaces exist for the same caller — the chat server, reaching cloud
// service-to-service with no user principal — and a second key would be a second
// thing to mint, mount and rotate, with nothing distinguishing when to use which.
//
// The name still says WEBSEARCH because that is the key that is minted and mounted
// today; renaming a live credential is its own coordinated change, and doing it
// inside this one would put a rename in the path of a fix.
func serviceKey() string { return strings.TrimSpace(os.Getenv("WEBSEARCH_API_KEY")) }

// Go drops comments at compile time, so cmd/zipdoc is the ONLY path from the
// handler's prose to the published document, the SDKs and the MCP tool
// description. Its output is committed; `make zipdoc-check` fails on drift.
//
// Without this directive the package builds, the tests pass, and the typed op
// below publishes a summary with no description — openapi.Complete accepts
// either, so the gap is invisible to every gate and visible in every SDK.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Path is the address this API is called on, spelled once so the registration,
// the admission middleware and the prose cannot disagree.
//
// It used to be registered twice — `g.Post("", serve)` and `g.Post("/", serve)`
// on a Group("/v1/crawl") — and they were the SAME route: zip normalises an empty
// leaf to "/", so both composed to "/v1/crawl/" and the second was dead. The
// consequence was not on the wire (the router is non-strict, so both URLs are
// served either way) but in the ARTIFACTS: op.Path is the identity every
// projection reads, so the published document, the operation id, the MCP tool and
// the URL every generated SDK calls all carried a trailing slash for a path this
// API's callers do not use.
const Path = "/v1/crawl"

// Mount registers /v1/crawl.
//
// The gate mirrors /v1/websearch/search exactly, and it is not optional here. This
// surface fetches a URL the caller chooses, from inside the cluster — an open one
// is a proxy into the private network, and the address guard in crawl.go is the
// second line of that defence, not the first. A caller is admitted with EITHER a
// validated principal (a signed-in user, already authenticated and metered) OR the
// shared service key. Neither ⇒ refused. An unset key 503s rather than defaulting
// open, so a misconfigured deploy fails closed and loudly.
//
// The two halves of that gate now live in two places, and they have to: a typed op
// is also an MCP tool and a CLI command, and tools/call invokes it with no route
// and therefore no middleware. So the KEY is checked in middleware, where a
// request is (it is a header, which a typed op cannot see), and the DECISION is
// made in the handler, where every caller reaches it. The middleware only ever
// adds a fact; it never admits by itself.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("crawl.Use:  nil app")
	}
	logger := luxlog.Default()
	if logger == nil {
		return fmt.Errorf("crawl.Use:  nil luxlog.Default()")
	}
	logger = logger.New("subsystem", "crawl")

	// Bind the corpus to the ONE object client the binary already has. A deployment
	// with no object store keeps crawling and keeps nothing — see Bind.
	Bind(deps.VFS)
	// And the meter that pays for a render, bound the same way and for the same
	// reason: escalation is reached from Read, which the answer engine calls
	// in-process with no deps to thread. See meter.go.
	bindMeter(cloud.NewMeter(deps, "crawl"))

	// The two request facts a typed op cannot reach, checked where a request is.
	//
	// cloud.RoutePath, not c.Path(): fiber routes case-insensitively and ignores a
	// trailing slash, so the raw spelling is what the CLIENT sent and RoutePath is
	// the form THE ROUTER MATCHED. `POST /V1/CRAWL` would otherwise miss a
	// lowercase prefix test and reach the fetcher with no credential checked.
	app.Use(zip.H(func(c *zip.Ctx) error {
		if cloud.RoutePath(c.Path()) != Path {
			return c.Continue()
		}
		if !bounded(c) {
			return c.JSON(http.StatusBadRequest, crawlResult{Error: missingURL})
		}
		if principal.Validated(c) {
			return c.Continue()
		}
		return admitKey(c)
	}))

	reg := cloud.ZipApp(app)
	if reg == nil {
		return fmt.Errorf("crawl.Use:  router carries no typed-op registry")
	}
	// ONE registration, at the address this API is called on. Declaring the whole
	// path here — rather than a leaf on a Group — is the fix LLM.md prescribes for
	// the trailing-slash class described on [Path].
	// Named, not derived. A POST to /v1/crawl derives `create_crawl`, which reads
	// as "make a crawl" — a job this surface does not have and cannot start. What
	// it does is read ONE page that is already addressed, and a model choosing
	// from an `op` enum picks by that name before it reads any description: the
	// derived name offered a crawler and the op is a reader. It is the verb over
	// the noun, beside search_web and research_web.
	zip.Post(reg, Path, fetch,
		zip.WithOperationID("read_page"),
		zip.WithSummary("Fetch one URL and read it back as markdown"),
		// 400 is DECLARED because this op answers it with its OWN body rather than
		// with zip's error envelope. See [crawlResult.StatusCode].
		zip.WithStatus(http.StatusOK, http.StatusBadRequest))

	// No "archive" field: it used to log deps.VFS != nil, which is ALWAYS true —
	// deps.VFS is guaranteed non-nil by contract (R-7, so consumers never
	// nil-deref) and is a fail-closed STUB when no object store is configured. So
	// the field reported an archive whether or not one byte could ever be stored,
	// and read as reassurance exactly when the corpus was off. Whether the store
	// works is not knowable here without doing I/O on the boot path, which is its
	// own hazard; a field that cannot be false should not be printed at all.
	logger.Info("crawl surface mounted (native in-process fetch + extract; no external crawler)")
	return nil
}

// maxRequest is what this route will read FROM A CALLER. One URL does not need
// more, and this surface dials a caller-chosen address from inside the cluster.
// (crawl.go's maxBody is the other direction — what is read from a fetched page.)
const maxRequest = 1 << 20

// bounded reports whether a body is one this route will read: at most
// [maxRequest] bytes, and JSON if there is any of it.
//
// Both facts are invisible to a typed op — it receives its DECODED In and never
// sees the raw bytes — so they are asked where the bytes still are. This is not a
// second admission gate: no credential is read here and nothing is admitted, only
// a body that has ALWAYS been refused is refused in the shape its callers parse.
// The untyped handler did both through one io.LimitReader(r.Body, 1<<20) whose
// decode failure WAS this 400; zip's op.invoke would answer its own flat
// {status,code,error} instead, a different body for a refusal that has not
// changed.
//
// It is a PREDICATE and writes nothing. An earlier version answered the refusal
// itself and returned c.Continue() otherwise — which runs the whole rest of the
// chain from inside it, so the handler ran before the caller had been admitted
// and then the middleware continued a second time. A middleware step either
// continues or it does not; a helper that does both is neither.
func bounded(c *zip.Ctx) bool {
	b := c.Body()
	return len(b) <= maxRequest && (len(b) == 0 || json.Valid(b))
}

// admitKey admits a caller holding the shared service key, presented as either
// X-API-Key or a Bearer. Both are accepted because the two clients that reach this
// surface already differ on that point and neither is wrong; requiring one would
// break a working caller to no benefit.
//
// It writes crawl's OWN refusal body, which is what its callers parse, and it
// records the admission on the CONTEXT rather than letting the request through —
// so the handler makes the one decision and this only ever supplies a fact.
func admitKey(c *zip.Ctx) error {
	want := serviceKey()
	if want == "" {
		return c.JSON(http.StatusServiceUnavailable, crawlResult{Error: "crawl not configured"})
	}
	got := strings.TrimSpace(c.Header("X-API-Key"))
	if got == "" {
		got = strings.TrimSpace(strings.TrimPrefix(c.Header("Authorization"), "Bearer "))
	}
	// Constant-time: a byte-at-a-time comparison leaks the key's prefix to a
	// caller willing to time enough requests.
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return c.JSON(http.StatusUnauthorized, crawlResult{Error: "invalid api key"})
	}
	c.SetContext(admit(c.Context()))
	return c.Continue()
}

// admittedKey names the context slot [admitKey] records the service key in.
// Unexported zero-size type, so only this package can mint or read one.
type admittedKey struct{}

func admit(ctx context.Context) context.Context {
	return context.WithValue(ctx, admittedKey{}, true)
}

func isAdmitted(ctx context.Context) bool {
	ok, _ := ctx.Value(admittedKey{}).(bool)
	return ok
}

// scopeOf is the ONE admission decision, and it never reads the body.
//
// A validated principal is admitted and scoped to its own org and project. A
// caller with no principal is admitted only on the marker [admitKey] leaves, and
// takes the shared corpus, which is the same thing the untyped route did through
// an empty scope. Neither ⇒ refused, so the fetcher is closed on every caller
// including the ones with no request behind them: the CLI projection runs an op
// with no request at all, and it lands here.
//
// A service caller (the chat server, holding the shared key) has no user
// principal and therefore no org — its pages land in the shared "_" prefix that
// seg() produces for an empty segment. That is deliberate: a service-wide corpus
// is the honest home for pages fetched on nobody's behalf, and inventing an org
// for it would file them under a tenant that did not ask.
//
// It reads the CONTEXT and never the request. All three facts are server-minted
// identity that cloud.Bridge parks in one expression, so holding the raw request
// to re-read them would take back what typing bought for nothing: the org is the
// tenant, the project only narrows within it, and neither is a fact a caller
// supplies.
func scopeOf(ctx context.Context) (Scope, error) {
	if principal.ValidatedFrom(ctx) {
		org, _ := principal.OrgFrom(ctx)
		return Scope{Org: org, Project: principal.ProjectFrom(ctx)}, nil
	}
	if isAdmitted(ctx) {
		return Scope{}, nil
	}
	return Scope{}, zip.ErrUnauthorized("crawling requires a validated principal or the service key")
}

// fetch reads one URL and answers with the page as markdown.
//
// It fetches a single URL from inside the cluster and answers with the address it
// actually landed on, the document's title, its content rendered to MARKDOWN, and
// whatever the page said about itself. One URL per call: batching would make the
// answer a partial-failure envelope every caller then has to unpack.
//
// A PAGE THAT COULD NOT BE FETCHED IS A NORMAL ANSWER, not a fault. An
// unreachable host, a refused address and a content type that is not a document
// all answer 200 with `success:false` and the reason in `error`, because the
// caller sent a well-formed ask and gets a well-formed answer. Non-2xx is reserved
// for a caller problem — 400 with the same body when there is no url, 401 for a
// bad key, 503 when the surface is unconfigured — so error handling can trust the
// status. Check `success` before reading `data`.
//
// Admission is either a validated principal or the shared service key, presented
// as X-API-Key or a Bearer; neither is refused, and an unset key fails closed
// rather than opening the fetcher to the private network. Pages are archived under
// the scope of the VERIFIED principal and NEVER a scope named in the body, so a
// URL already read under that scope is answered from the archive without touching
// the network; a service caller has no org and its pages land in the shared
// corpus.
//
// The URL is caller-supplied and dialled from INSIDE the cluster, which makes this
// a request-forgery primitive by construction. Only http and https are accepted,
// and every address actually dialled must be public unicast — loopback,
// link-local, private and multicast are refused. The check lives in the DIALER
// rather than on the hostname, because resolving a name to validate it and then
// letting the transport resolve it again is a gap DNS rebinding walks straight
// through; redirects re-enter the same dialer.
func fetch(ctx context.Context, in *crawlRequest) (*crawlResult, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.URL) == "" {
		return &crawlResult{Error: missingURL}, nil
	}
	page, err := Read(ctx, s, in.URL)
	if err != nil {
		// 200 with Success:false — see the note on crawlResult. The message is the
		// error verbatim: a caller debugging a failed crawl needs to know whether the
		// host was refused, unreachable, or served the wrong type.
		return &crawlResult{Error: err.Error()}, nil
	}
	meta, err := json.Marshal(page.Metadata)
	if err != nil {
		return nil, zip.ErrInternal("crawl: the page's metadata will not encode")
	}
	return &crawlResult{
		Success: true,
		Data: &crawlDocument{
			URL:      page.URL,
			Title:    page.Title,
			Markdown: page.Markdown,
			Metadata: meta,
		},
	}, nil
}
