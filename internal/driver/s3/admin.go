package s3

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/object"
	hs3 "github.com/hanzos3/go"
	"github.com/hanzos3/go/pkg/credentials"
)

// Admin holds the shared S3 admin connection parameters, sourced once from the
// environment the operator injects (the k8s secret hanzo-s3 -> S3_ADMIN_*). It is a
// value: it holds no live connection, so it is safe to copy and to build clients
// from concurrently.
type Admin struct {
	// endpoint is the INTERNAL admin host:port used for control operations. Never
	// exposed to a tenant.
	endpoint string
	// publicEndpoint is the browser-routable host used ONLY to sign presigned URLs.
	// Empty means presigning is disabled.
	publicEndpoint string
	ak             string
	sk             string
	// secure is TLS for the INTERNAL endpoint; publicSecure for the PUBLIC one (the
	// public host is almost always https even when the in-cluster hop is not).
	secure       bool
	publicSecure bool
	region       string
}

// publicHost is the browser-routable host, and the one setting here where SET AND
// EMPTY differs from ABSENT: an operator with no such host must be able to say so,
// and presigning then refuses honestly rather than signing URLs against a default
// nobody can reach.
func publicHost() string {
	if v, ok := os.LookupEnv("S3_PUBLIC_ENDPOINT"); ok {
		return strings.TrimSpace(v)
	}
	return "s3.hanzo.ai"
}

// New reads the shared S3 admin configuration from the environment.
//
//	S3_ADMIN_ENDPOINT      internal admin host:port (default s3.hanzo.svc:9000)
//	S3_ADMIN_ACCESS_KEY    access key (no default; absence = not configured)
//	S3_ADMIN_SECRET_KEY    secret key (no default; absence = not configured)
//	S3_SECURE              TLS to the internal endpoint (default false)
//	S3_REGION              signing region (default us-east-1)
//	S3_PUBLIC_ENDPOINT     browser-routable host for presigned URLs
//	                       (default s3.hanzo.ai; strips any scheme; SET AND EMPTY
//	                       disables presigning)
//	S3_PUBLIC_SECURE       TLS for the public host (default true)
func New() Admin {
	return Admin{
		endpoint:       env.Or("S3_ADMIN_ENDPOINT", "s3.hanzo.svc:9000"),
		publicEndpoint: hostOnly(publicHost()),
		ak:             env.Or("S3_ADMIN_ACCESS_KEY", ""),
		sk:             env.Or("S3_ADMIN_SECRET_KEY", ""),
		secure:         boolEnv("S3_SECURE", false),
		publicSecure:   boolEnv("S3_PUBLIC_SECURE", true),
		region:         env.Or("S3_REGION", "us-east-1"),
	}
}

// Configured reports whether admin credentials are present.
func (a Admin) Configured() bool { return a.ak != "" && a.sk != "" }

// Region is the signing region.
func (a Admin) Region() string { return a.region }

// Client builds a client bound to the INTERNAL admin endpoint.
func (a Admin) Client() (*hs3.Client, error) {
	if !a.Configured() {
		return nil, fmt.Errorf("s3: S3_ADMIN_ACCESS_KEY/SECRET_KEY not set")
	}
	return hs3.New(a.endpoint, &hs3.Options{
		Creds:  credentials.NewStaticV4(a.ak, a.sk, ""),
		Secure: a.secure,
		Region: a.region,
	})
}

// PresignConfigured reports whether a public host is available to sign
// browser-followable URLs.
func (a Admin) PresignConfigured() bool { return a.Configured() && a.publicEndpoint != "" }

// PublicClient builds a client bound to the PUBLIC host. Its only use is signing
// URLs: a signature makes no network call, so the URL a browser receives targets
// the public, routable host and not the in-cluster admin one.
func (a Admin) PublicClient() (*hs3.Client, error) {
	if !a.PresignConfigured() {
		return nil, object.ErrNoPresign
	}
	return hs3.New(a.publicEndpoint, &hs3.Options{
		Creds:  credentials.NewStaticV4(a.ak, a.sk, ""),
		Secure: a.publicSecure,
		Region: a.region,
	})
}

func hostOnly(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.TrimRight(s, "/")
}

func boolEnv(key string, def bool) bool {
	if v := env.Or(key, ""); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// Delegate is what a process that writes into the store ITSELF needs: the address,
// the region and the key pair. It is not a narrower credential than Client's and
// does not pretend to be: the gateway exposes no STS endpoint (AssumeRole answers
// 405), so there are no scoped, expiring keys to mint, and whoever takes this pair
// holds the whole store. The call site is the place to say so.
func (a Admin) Delegate() (object.Delegation, bool) {
	if !a.Configured() {
		return object.Delegation{}, false
	}
	scheme := "http://"
	if a.secure {
		scheme = "https://"
	}
	return object.Delegation{
		URL:       scheme + a.endpoint,
		Region:    a.region,
		AccessKey: a.ak,
		Secret:    a.sk,
		// PathStyle: the store is always reached at an explicit endpoint and never
		// at AWS's virtual-host DNS; a client that guesses `<bucket>.s3.hanzo.svc`
		// resolves nothing, and the upload fails after the work was done.
		PathStyle: true,
	}, true
}
