package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	s3 "github.com/hanzoai/s3-go"
	"github.com/hanzoai/s3-go/pkg/credentials"
)

// backend is the one S3 endpoint this subsystem talks to, read once from the
// environment. It holds no live connection, so it is safe to copy.
//
//	S3_ENDPOINT    host:port of the S3 server (default 127.0.0.1:9000)
//	S3_ACCESS_KEY  access key (no default — absent means not configured)
//	S3_SECRET_KEY  secret key (no default — absent means not configured)
//	S3_SECURE      TLS to the endpoint (default false)
//	S3_REGION      signing region (default us-east-1)
//
// Presigned URLs are signed over the same endpoint, so a browser on the same
// machine follows them directly.
type backend struct {
	endpoint string
	ak       string
	sk       string
	secure   bool
	region   string
}

func newBackend() backend {
	return backend{
		endpoint: env("S3_ENDPOINT", "127.0.0.1:9000"),
		ak:       os.Getenv("S3_ACCESS_KEY"),
		sk:       os.Getenv("S3_SECRET_KEY"),
		secure:   boolEnv("S3_SECURE", false),
		region:   env("S3_REGION", "us-east-1"),
	}
}

// Configured reports whether credentials are present. When false the subsystem
// fails closed (honest 503), never fabricating a result.
func (b backend) Configured() bool { return b.ak != "" && b.sk != "" }

// Client builds an S3 client bound to the endpoint.
func (b backend) Client() (*s3.Client, error) {
	if !b.Configured() {
		return nil, fmt.Errorf("storage: S3_ACCESS_KEY/S3_SECRET_KEY not set")
	}
	return s3.New(b.endpoint, &s3.Options{
		Creds:  credentials.NewStaticV4(b.ak, b.sk, ""),
		Secure: b.secure,
		Region: b.region,
	})
}

// BucketName is the tenant→S3-bucket name for (org, friendly name):
// "o"<orgHash>-<name>, with '_' folded to '-' so it is a legal bucket name.
func BucketName(org, name string) string {
	return bucketName("o" + orgHash(org) + "_" + strings.ReplaceAll(name, "-", "_"))
}

// BucketPrefix is the S3-bucket-name prefix ALL of an org's buckets share.
func BucketPrefix(org string) string { return bucketName("o"+orgHash(org)) + "-" }

// orgHash is a fixed-width tag for an org slug: the first 16 hex chars (64 bits)
// of SHA-256(org). The fixed width makes the org→name boundary unambiguous, so
// two distinct orgs never fold onto one bucket.
func orgHash(org string) string {
	sum := sha256.Sum256([]byte(org))
	return hex.EncodeToString(sum[:])[:16]
}

// bucketName maps a physical id to a DNS-safe S3 bucket name.
func bucketName(physical string) string {
	b := strings.Trim(strings.ToLower(strings.ReplaceAll(physical, "_", "-")), "-")
	if len(b) > 63 {
		b = strings.Trim(b[:63], "-")
	}
	return b
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
