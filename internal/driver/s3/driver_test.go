package s3

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/object"
	hs3 "github.com/hanzos3/go"
)

func configured(t *testing.T) Admin {
	t.Helper()
	t.Setenv("S3_ADMIN_ENDPOINT", "s3.internal:9000")
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIATEST")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secrettest")
	t.Setenv("S3_PUBLIC_ENDPOINT", "https://s3.example.test/")
	t.Setenv("S3_SECURE", "")
	t.Setenv("S3_PUBLIC_SECURE", "")
	return New()
}

func TestTheEnvironmentSelectsTheDriver(t *testing.T) {
	for _, k := range []string{"S3_ADMIN_ACCESS_KEY", "S3_ADMIN_SECRET_KEY"} {
		t.Setenv(k, "")
	}
	if New().Configured() {
		t.Fatal("no credential is configured")
	}
	a := configured(t)
	if !a.Configured() || !a.PresignConfigured() {
		t.Fatal("configured")
	}
	d, ok := a.Delegate()
	if !ok || d.URL != "http://s3.internal:9000" || d.AccessKey != "AKIATEST" || !d.PathStyle || d.Region != "us-east-1" {
		t.Fatalf("delegation = %+v %v", d, ok)
	}
	if _, ok := (Admin{}).Delegate(); ok {
		t.Fatal("an unconfigured admin delegated a credential")
	}
}

func TestSetAndEmptyPublicEndpointRefusesLinks(t *testing.T) {
	a := configured(t)
	t.Setenv("S3_PUBLIC_ENDPOINT", "")
	a = New()
	s, err := Open(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Share(context.Background(), object.Share{Bucket: "bkt", Key: "k", TTL: time.Minute}); !errors.Is(err, object.ErrNoPresign) {
		t.Fatalf("share = %v, want ErrNoPresign", err)
	}
	if _, err := s.Form(context.Background(), object.Form{Bucket: "bkt", Prefix: "p/", TTL: time.Minute, MaxBytes: 1}); !errors.Is(err, object.ErrNoPresign) {
		t.Fatalf("form = %v, want ErrNoPresign", err)
	}
}

func TestLinksAreSignedForThePublicHost(t *testing.T) {
	s, err := Open(configured(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, sh := range map[string]object.Share{
		"get":    {Bucket: "bkt", Key: "a b/c", TTL: time.Minute, Query: url.Values{"response-content-type": {"text/plain"}}},
		"put":    {Method: http.MethodPut, Bucket: "bkt", Key: "k", TTL: time.Minute},
		"head":   {Method: http.MethodHead, Bucket: "bkt", Key: "k", TTL: time.Minute},
		"header": {Method: http.MethodPut, Bucket: "bkt", Key: "k", TTL: time.Minute, Header: http.Header{"Content-Type": {"text/plain"}}},
		"part":   {Method: http.MethodPut, Bucket: "bkt", Key: "k", TTL: time.Minute, Query: url.Values{"partNumber": {"1"}, "uploadId": {"u"}}},
	} {
		l, err := s.Share(ctx, sh)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		u, err := url.Parse(l.URL)
		if err != nil || u.Scheme != "https" || u.Host != "s3.example.test" || u.Query().Get("X-Amz-Signature") == "" {
			t.Fatalf("%s: url = %s", name, l.URL)
		}
	}
	f, err := s.Form(ctx, object.Form{Bucket: "bkt", Prefix: "acme/run/", TTL: time.Minute, MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := f.Fields["Content-Type"]; has || f.Fields["policy"] == "" || f.URL == "" {
		t.Fatalf("form = %+v: the empty Content-Type placeholder must not travel", f)
	}
}

func TestStoreRefusalsAreTheSDKsTerms(t *testing.T) {
	for code, want := range map[string]error{
		"NoSuchBucket":               object.ErrNoBucket,
		"NoSuchKey":                  object.ErrNoKey,
		"NoSuchUpload":               object.ErrNoUpload,
		"BucketNotEmpty":             object.ErrNotEmpty,
		"PreconditionFailed":         object.ErrConflict,
		"ConditionalRequestConflict": object.ErrConflict,
	} {
		got := wrap(hs3.ErrorResponse{Code: code})
		if !errors.Is(got, want) {
			t.Fatalf("%s wrapped to %v, want %v", code, got, want)
		}
		var resp hs3.ErrorResponse
		if !errors.As(got, &resp) || resp.Code != code {
			t.Fatalf("%s: the store's own answer must stay in the chain", code)
		}
	}
	if got := wrap(hs3.ErrorResponse{Code: "AccessDenied"}); object.Absent(got) || object.Conflict(got) {
		t.Fatalf("AccessDenied wrapped to %v", got)
	}
	if got := wrap(hs3.ErrorResponse{Code: "Anything", StatusCode: 412}); !object.Conflict(got) {
		t.Fatalf("412 = %v", got)
	}
	if wrap(nil) != nil {
		t.Fatal("wrap(nil)")
	}
}

func TestLinkingTheDriverRegistersIt(t *testing.T) {
	configured(t)
	s, err := object.Open(&object.Local{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*object.Local); ok {
		t.Fatalf("a configured process fell back to local disk: %T", s)
	}
	if d, ok := s.(object.Delegator); !ok {
		t.Fatalf("%T cannot delegate", s)
	} else if _, ok := d.Delegate(); !ok {
		t.Fatal("no credential to delegate")
	}
}

// AWS S3 refuses a conditional write that races another with 409
// ConditionalRequestConflict rather than 412. Nothing was written either way, so both
// are a lost compare-and-set, and a fence counts the racer as a loser.
func TestRefusedConditionalWritesAreConflicts(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{412, "PreconditionFailed"},
		{409, "ConditionalRequestConflict"},
		{409, "OperationAborted"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + tc.code +
				`</Code><Message>refused</Message><Key>k</Key><BucketName>orgs</BucketName></Error>`))
		}))
		t.Setenv("S3_ADMIN_ENDPOINT", strings.TrimPrefix(srv.URL, "http://"))
		t.Setenv("S3_ADMIN_ACCESS_KEY", "id")
		t.Setenv("S3_ADMIN_SECRET_KEY", "secret")
		t.Setenv("S3_SECURE", "")
		s, err := Open(New())
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Put(context.Background(), "orgs", "k", strings.NewReader("x"), 1, object.Put{IfMatch: `"etag"`})
		if !object.Conflict(err) {
			t.Errorf("%d %s: err = %v, want a conflict", tc.status, tc.code, err)
		}
		srv.Close()
	}
}
