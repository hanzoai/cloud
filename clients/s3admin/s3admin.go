// Package s3admin is the ONE shared S3 access path for the unified cloud binary.
//
// Every subsystem that touches the shared object store — clients/projects (the
// deploy blob store) and clients/s3 (the /v1/s3 file-manager control plane) —
// builds its *minio.Client here, from the SAME admin credentials
// (S3_ADMIN_*). There is no second S3 client construction anywhere in the
// binary: one endpoint, one credential source, one connect path (DRY).
//
// The backend is the SeaweedFS S3 gateway (s3.hanzo.svc:9000), which speaks the
// S3 API, so minio-go is the client. The gateway is reached over the internal
// admin endpoint for control operations; a SEPARATE public-host client
// (PublicClient) is used only to MINT presigned URLs that a browser can follow,
// since a presign is a pure signature over the client's endpoint and never makes
// a network call — so the signed host is the browser-routable one.
//
// This package depends on nothing but minio-go: it is a leaf, so both
// projects and the s3 subsystem import it without any import cycle.
package s3admin

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	minio "github.com/hanzoai/s3-go"
	"github.com/hanzoai/s3-go/pkg/credentials"
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
// the exact variables clients/projects/blob.go already consumes, so the two
// subsystems resolve identical credentials and endpoint with no drift.
//
//	S3_ADMIN_ENDPOINT      internal admin host:port (default s3.hanzo.svc:9000)
//	S3_ADMIN_ACCESS_KEY    access key (no default — absence = not configured)
//	S3_ADMIN_SECRET_KEY    secret key (no default — absence = not configured)
//	S3_SECURE              TLS to the internal endpoint (default false)
//	S3_REGION              signing region (default us-east-1)
//	S3_PUBLIC_ENDPOINT     browser-routable host for presigned URLs
//	                             (default s3.hanzo.ai; strips any scheme)
//	S3_PUBLIC_SECURE       TLS for the public host (default true)
func New() Admin {
	return Admin{
		endpoint:       env("S3_ADMIN_ENDPOINT", "s3.hanzo.svc:9000"),
		publicEndpoint: hostOnly(env("S3_PUBLIC_ENDPOINT", "s3.hanzo.ai")),
		ak:             os.Getenv("S3_ADMIN_ACCESS_KEY"),
		sk:             os.Getenv("S3_ADMIN_SECRET_KEY"),
		secure:         boolEnv("S3_SECURE", false),
		publicSecure:   boolEnv("S3_PUBLIC_SECURE", true),
		region:         env("S3_REGION", "us-east-1"),
	}
}

// Configured reports whether admin credentials are present. A subsystem that
// finds this false must fail closed (honest 503), never fabricate a result.
func (a Admin) Configured() bool { return a.ak != "" && a.sk != "" }

// Region is the signing region (exposed so callers can pass it to MakeBucket).
func (a Admin) Region() string { return a.region }

// Client builds a minio client bound to the INTERNAL admin endpoint. Use it for
// every control/data operation the server performs itself (list, create, stat,
// delete, and streamed put/get through the server).
func (a Admin) Client() (*minio.Client, error) {
	if !a.Configured() {
		return nil, fmt.Errorf("s3admin: S3_ADMIN_ACCESS_KEY/SECRET_KEY not set")
	}
	return minio.New(a.endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(a.ak, a.sk, ""),
		Secure: a.secure,
		Region: a.region,
	})
}

// PresignConfigured reports whether a public host is available to sign
// browser-followable URLs. Absent it, callers must not offer presigned upload or
// download (they degrade to a server-streamed path or an honest error).
func (a Admin) PresignConfigured() bool { return a.Configured() && a.publicEndpoint != "" }

// PublicClient builds a minio client bound to the PUBLIC host. Its ONLY use is
// minting presigned URLs (PresignedGetObject / PresignedPutObject) — those sign
// over this client's endpoint without any network call, so the URL a browser
// receives targets the public, routable host and not the in-cluster admin one.
func (a Admin) PublicClient() (*minio.Client, error) {
	if !a.PresignConfigured() {
		return nil, fmt.Errorf("s3admin: no public endpoint configured for presigning")
	}
	return minio.New(a.publicEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(a.ak, a.sk, ""),
		Secure: a.publicSecure,
		Region: a.region,
	})
}

// hostOnly strips a scheme and any trailing slash from a configured public
// endpoint, since minio.New wants a bare host[:port]. "https://s3.hanzo.ai/" →
// "s3.hanzo.ai".
func hostOnly(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.TrimRight(s, "/")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func boolEnv(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
