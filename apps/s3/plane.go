package s3

// plane.go — the one door a sibling app has into an org's storage: a form a
// stranger posts one object through, never more than the app allows.

import (
	"context"
	"github.com/hanzoai/cloud/sdk/object"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	s3 "github.com/hanzos3/go"
	"github.com/zap-proto/zip"
)

// uploadForm signs a POST form for one object in a bucket of the caller's org: the
// key is fixed, the content type is fixed, and the size is bounded, so the form a
// browser is handed can carry nothing else. The bucket is made private when it is
// missing. The org is the plane caller's; no input names one.
func (o ops) uploadForm(ctx context.Context, in *client.UploadFormIn) (*client.UploadForm, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	if org == "" {
		return nil, zip.ErrForbidden("s3: no org on the call")
	}
	key, ok := cleanKey(in.Key)
	if !ok || !object.Friendly(in.Bucket) || in.ContentType == "" || in.MaxBytes <= 0 {
		return nil, zip.ErrBadRequest("s3 upload form: a bucket, a clean key, a content type and a size are required")
	}
	if !o.s.State.admin.PresignConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "s3: no public endpoint to sign a form against")
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	physical := physicalBucket(org, in.Bucket)
	if err := o.withinQuota(ctx, cli, org, physical, in.MaxBytes); err != nil {
		return nil, err
	}
	if ok, err := cli.BucketExists(ctx, physical); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "bucket check: %v", err)
	} else if !ok {
		if err := cli.MakeBucket(ctx, physical, s3.MakeBucketOptions{Region: o.s.State.admin.Region()}); err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "create bucket: %v", err)
		}
	}
	pub, err := o.s.State.admin.PublicClient()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	p := s3.NewPostPolicy()
	for _, err := range []error{
		p.SetBucket(physical), p.SetKey(key), p.SetExpires(time.Now().UTC().Add(presignTTL)),
		p.SetContentType(in.ContentType), p.SetContentLengthRange(1, in.MaxBytes),
	} {
		if err != nil {
			return nil, zip.ErrBadRequest("s3 upload form: " + err.Error())
		}
	}
	u, fields, err := pub.PresignedPostPolicy(ctx, p)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "presign form: %v", err)
	}
	out := &client.UploadForm{URL: u.String(), Key: key, Expiry: int64(presignTTL.Seconds())}
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		out.Fields = append(out.Fields, client.FormField{Name: k, Value: fields[k]})
	}
	return out, nil
}
