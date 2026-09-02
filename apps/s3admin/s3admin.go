// Package s3admin is the one way to reach the object store: an internal client
// for control and data, and a public one that mints presigned URLs.
//
// Both are built from the one shared admin credential (S3_ADMIN_*).
//
// Its consumers are apps/s3 (the /v1/s3 object plane), apps/projects (the
// deploy blob store), apps/sites (static site serving), clients/s3vfs (deps.VFS,
// team blobs) and build.go. One credential source, one endpoint, one connect
// path — but NOT yet the only construction site: apps/provisioning builds its own
// *s3.Client from the SAME S3_ADMIN_* variables (provisioner.go newS3), which is
// the second site this package exists to retire.
//
// The backend is the SeaweedFS S3 gateway (s3.hanzo.svc:9000), which speaks the
// S3 API, so hanzos3/go is the client. The gateway is reached over the internal
// admin endpoint for control operations; a SEPARATE public-host client
// (PublicClient) is used only to MINT presigned URLs that a browser can follow,
// since a presign is a pure signature over the client's endpoint and never makes
// a network call — so the signed host is the browser-routable one.
//
// This package depends on nothing but hanzos3/go: it is a leaf, so both
// projects and the s3 subsystem import it without any import cycle.
package s3admin

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/internal/environ"
	s3 "github.com/hanzos3/go"
	"github.com/hanzos3/go/pkg/credentials"
)

// Admin holds the shared S3 admin connection parameters, sourced once from the
// environment the operator injects (the k8s secret hanzo-s3 → S3_ADMIN_*).
// It is a value: construct it with New() and pass it around; it holds no live
// connection, so it is safe to copy and to build clients from concurrently.
type Admin struct {
	// endpoint is the INTERNAL admin host:port used for control operations
	// (list/create/delete/stat). Never exposed to a tenant.
	endpoint string
	// publicEndpoint is the browser-routable host used ONLY to sign presigned
	// URLs. Empty means presigning is disabled (no public host configured).
	publicEndpoint string
	ak             string
	sk             string
	// secure is TLS for the INTERNAL endpoint; publicSecure for the PUBLIC one
	// (the public host is almost always https even when the in-cluster hop is not).
	secure       bool
	publicSecure bool
	region       string
}

// New reads the shared S3 admin configuration from the environment. It mirrors
// the exact variables apps/projects/blob.go already consumes, so the two
// subsystems resolve identical credentials and endpoint with no drift.
//
//	S3_ADMIN_ENDPOINT      internal admin host:port (default s3.hanzo.svc:9000)
//	S3_ADMIN_ACCESS_KEY    access key (no default — absence = not configured)
//	S3_ADMIN_SECRET_KEY    secret key (no default — absence = not configured)
//	S3_SECURE              TLS to the internal endpoint (default false)
//	S3_REGION              signing region (default us-east-1)
//	S3_PUBLIC_ENDPOINT     browser-routable host for presigned URLs
//	                             (default s3.hanzo.ai; strips any scheme;
//	                             SET AND EMPTY disables presigning)
//	S3_PUBLIC_SECURE       TLS for the public host (default true)
//
// publicHost is the browser-routable host, and the one setting here where SET AND
// EMPTY differs from ABSENT: an operator with no such host must be able to say so,
// and presigning then refuses honestly rather than signing URLs against a default
// nobody can reach. os.LookupEnv is what separates the two — a reader that trims
// cannot, and the old way to say "none" was a value holding SPACES, which worked
// only for as long as nothing trimmed it.
func publicHost() string {
	if v, ok := os.LookupEnv("S3_PUBLIC_ENDPOINT"); ok {
		return strings.TrimSpace(v)
	}
	return "s3.hanzo.ai"
}

func New() Admin {
	return Admin{
		endpoint:       environ.Or("S3_ADMIN_ENDPOINT", "s3.hanzo.svc:9000"),
		publicEndpoint: hostOnly(publicHost()),
		ak:             environ.Or("S3_ADMIN_ACCESS_KEY", ""),
		sk:             environ.Or("S3_ADMIN_SECRET_KEY", ""),
		secure:         boolEnv("S3_SECURE", false),
		publicSecure:   boolEnv("S3_PUBLIC_SECURE", true),
		region:         environ.Or("S3_REGION", "us-east-1"),
	}
}

// Configured reports whether admin credentials are present. A subsystem that
// finds this false must fail closed (honest 503), never fabricate a result.
func (a Admin) Configured() bool { return a.ak != "" && a.sk != "" }

// Region is the signing region (exposed so callers can pass it to MakeBucket).
func (a Admin) Region() string { return a.region }

// Client builds a S3 client bound to the INTERNAL admin endpoint. Use it for
// every control/data operation the server performs itself (list, create, stat,
// delete, and streamed put/get through the server).
func (a Admin) Client() (*s3.Client, error) {
	if !a.Configured() {
		return nil, fmt.Errorf("s3admin: S3_ADMIN_ACCESS_KEY/SECRET_KEY not set")
	}
	return s3.New(a.endpoint, &s3.Options{
		Creds:  credentials.NewStaticV4(a.ak, a.sk, ""),
		Secure: a.secure,
		Region: a.region,
	})
}

// PresignConfigured reports whether a public host is available to sign
// browser-followable URLs. Absent it, callers must not offer presigned upload or
// download (they degrade to a server-streamed path or an honest error).
func (a Admin) PresignConfigured() bool { return a.Configured() && a.publicEndpoint != "" }

// PublicClient builds a S3 client bound to the PUBLIC host. Its ONLY use is
// minting presigned URLs (PresignedGetObject / PresignedPutObject) — those sign
// over this client's endpoint without any network call, so the URL a browser
// receives targets the public, routable host and not the in-cluster admin one.
func (a Admin) PublicClient() (*s3.Client, error) {
	if !a.PresignConfigured() {
		return nil, fmt.Errorf("s3admin: no public endpoint configured for presigning")
	}
	return s3.New(a.publicEndpoint, &s3.Options{
		Creds:  credentials.NewStaticV4(a.ak, a.sk, ""),
		Secure: a.publicSecure,
		Region: a.region,
	})
}

// hostOnly strips a scheme and any trailing slash from a configured public
// endpoint, since s3.New wants a bare host[:port]. "https://s3.hanzo.ai/" →
// "s3.hanzo.ai".
func hostOnly(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.TrimRight(s, "/")
}

func boolEnv(key string, def bool) bool {
	if v := environ.Or(key, ""); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// Store is what a process that writes into the object store ITSELF needs: the
// address, the region and the key pair, as a value.
//
// It is not a narrower credential than Client's and does not pretend to be. The
// gateway exposes no STS endpoint — AssumeRole answers 405 — so there are no
// scoped, expiring keys to mint, and a presigned URL cannot be handed to a writer
// that names its own objects as it goes. The shared admin pair is the only
// delegation this store has, so whoever takes one holds the whole store and the
// call site is the place to say so.
//
// It exists so that a subsystem which cannot use hanzos3/go — because the writer
// is another process with its own S3 client — still reads the deployment's ONE
// configuration rather than the environment a second time. A second reader is how
// a recording ends up in a bucket nothing else can find.
type Store struct {
	// URL carries the SCHEME, unlike the internal endpoint this is built from:
	// hanzos3/go takes host[:port] plus a bool, and every other S3 client takes a
	// URL, so the conversion belongs here rather than at each call site.
	URL       string
	Region    string
	AccessKey string
	Secret    string
	// PathStyle addresses a bucket as the first path segment rather than as a DNS
	// label. It is true because this store is always reached at an explicit
	// endpoint and never at AWS's virtual-host DNS: a client that guesses
	// `<bucket>.s3.hanzo.svc` resolves nothing, and the upload fails after the
	// recording has already been made.
	PathStyle bool
}

// Store returns the credential above, and false when none is configured — the
// same fact Configured reports, in the shape a caller has to pass on.
func (a Admin) Store() (Store, bool) {
	if !a.Configured() {
		return Store{}, false
	}
	scheme := "http://"
	if a.secure {
		scheme = "https://"
	}
	return Store{
		URL:       scheme + a.endpoint,
		Region:    a.region,
		AccessKey: a.ak,
		Secret:    a.sk,
		PathStyle: true,
	}, true
}
