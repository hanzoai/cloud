// Package infra provides infrastructure clients.
//
// This file implements the S3-compatible storage client (S3_URL) for product
// images and digital assets.
package infra

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"time"

	"github.com/hanzoai/storage-go"
	"github.com/hanzoai/storage-go/pkg/credentials"
)

// StorageConfig holds MinIO configuration
type StorageConfig struct {
	// Enabled enables the storage service
	Enabled bool

	// Endpoint is the MinIO server endpoint (host:port)
	Endpoint string

	// AccessKey is the access key ID
	AccessKey string

	// SecretKey is the secret access key
	SecretKey string

	// UseSSL enables SSL connection
	UseSSL bool

	// Bucket is the default bucket name
	Bucket string

	// Region is the bucket region
	Region string

	// PublicBaseURL is the public URL base for assets
	PublicBaseURL string
}

// StorageClient wraps the MinIO client
type StorageClient struct {
	config *StorageConfig
	client *minio.Client
}

// NewStorageClient creates a new MinIO storage client
func NewStorageClient(ctx context.Context, cfg *StorageConfig) (*StorageClient, error) {
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create minio client: %w", err)
	}

	sc := &StorageClient{
		config: cfg,
		client: client,
	}

	// Ensure default bucket exists
	if cfg.Bucket != "" {
		if err := sc.EnsureBucket(ctx, cfg.Bucket); err != nil {
			return nil, fmt.Errorf("failed to ensure bucket: %w", err)
		}
	}

	return sc, nil
}

// EnsureBucket creates a bucket if it doesn't exist
func (c *StorageClient) EnsureBucket(ctx context.Context, bucket string) error {
	exists, err := c.client.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("failed to check bucket: %w", err)
	}

	if !exists {
		err = c.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{
			Region: c.config.Region,
		})
		if err != nil {
			return fmt.Errorf("failed to create bucket: %w", err)
		}
	}

	return nil
}

// Upload uploads a file to storage
func (c *StorageClient) Upload(ctx context.Context, opts *UploadOptions) (*UploadResult, error) {
	if opts.Bucket == "" {
		opts.Bucket = c.config.Bucket
	}

	info, err := c.client.PutObject(ctx, opts.Bucket, opts.Key, opts.Reader, opts.Size, minio.PutObjectOptions{
		ContentType:  opts.ContentType,
		UserMetadata: opts.Metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upload object: %w", err)
	}

	return &UploadResult{
		Bucket:   info.Bucket,
		Key:      info.Key,
		ETag:     info.ETag,
		Size:     info.Size,
		Location: c.buildURL(opts.Bucket, opts.Key),
	}, nil
}

// UploadBytes uploads bytes to storage
func (c *StorageClient) UploadBytes(ctx context.Context, bucket, key string, data []byte, contentType string) (*UploadResult, error) {
	if bucket == "" {
		bucket = c.config.Bucket
	}

	reader := &bytesReader{data: data, pos: 0}
	return c.Upload(ctx, &UploadOptions{
		Bucket:      bucket,
		Key:         key,
		Reader:      reader,
		Size:        int64(len(data)),
		ContentType: contentType,
	})
}

// bytesReader implements io.Reader for bytes
type bytesReader struct {
	data []byte
	pos  int
}

func (r *bytesReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// Download downloads a file from storage
func (c *StorageClient) Download(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	if bucket == "" {
		bucket = c.config.Bucket
	}

	obj, err := c.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get object: %w", err)
	}

	return obj, nil
}

// DownloadBytes downloads a file as bytes
func (c *StorageClient) DownloadBytes(ctx context.Context, bucket, key string) ([]byte, error) {
	reader, err := c.Download(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

// Delete removes a file from storage
func (c *StorageClient) Delete(ctx context.Context, bucket, key string) error {
	if bucket == "" {
		bucket = c.config.Bucket
	}

	err := c.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}

	return nil
}

// DeleteMany removes multiple files from storage
func (c *StorageClient) DeleteMany(ctx context.Context, bucket string, keys []string) error {
	if bucket == "" {
		bucket = c.config.Bucket
	}

	objectsCh := make(chan minio.ObjectInfo)

	go func() {
		defer close(objectsCh)
		for _, key := range keys {
			objectsCh <- minio.ObjectInfo{Key: key}
		}
	}()

	for err := range c.client.RemoveObjects(ctx, bucket, objectsCh, minio.RemoveObjectsOptions{}) {
		if err.Err != nil {
			return fmt.Errorf("failed to delete object %s: %w", err.ObjectName, err.Err)
		}
	}

	return nil
}

// Exists checks if a file exists
func (c *StorageClient) Exists(ctx context.Context, bucket, key string) (bool, error) {
	if bucket == "" {
		bucket = c.config.Bucket
	}

	_, err := c.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "NoSuchKey" {
			return false, nil
		}
		return false, fmt.Errorf("failed to stat object: %w", err)
	}

	return true, nil
}

// Stat returns object info
func (c *StorageClient) Stat(ctx context.Context, bucket, key string) (*ObjectInfo, error) {
	if bucket == "" {
		bucket = c.config.Bucket
	}

	info, err := c.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to stat object: %w", err)
	}

	return &ObjectInfo{
		Key:          info.Key,
		Size:         info.Size,
		ContentType:  info.ContentType,
		ETag:         info.ETag,
		LastModified: info.LastModified,
		Metadata:     info.UserMetadata,
	}, nil
}

// List lists objects in a bucket
func (c *StorageClient) List(ctx context.Context, bucket, prefix string, maxKeys int) ([]*ObjectInfo, error) {
	if bucket == "" {
		bucket = c.config.Bucket
	}

	var objects []*ObjectInfo
	count := 0

	for obj := range c.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", obj.Err)
		}

		objects = append(objects, &ObjectInfo{
			Key:          obj.Key,
			Size:         obj.Size,
			ETag:         obj.ETag,
			LastModified: obj.LastModified,
		})

		count++
		if maxKeys > 0 && count >= maxKeys {
			break
		}
	}

	return objects, nil
}

// Copy copies an object within storage
func (c *StorageClient) Copy(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) error {
	if srcBucket == "" {
		srcBucket = c.config.Bucket
	}
	if dstBucket == "" {
		dstBucket = c.config.Bucket
	}

	src := minio.CopySrcOptions{
		Bucket: srcBucket,
		Object: srcKey,
	}

	dst := minio.CopyDestOptions{
		Bucket: dstBucket,
		Object: dstKey,
	}

	_, err := c.client.CopyObject(ctx, dst, src)
	if err != nil {
		return fmt.Errorf("failed to copy object: %w", err)
	}

	return nil
}

// PresignedGetURL generates a presigned URL for downloading
func (c *StorageClient) PresignedGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error) {
	if bucket == "" {
		bucket = c.config.Bucket
	}

	u, err := c.client.PresignedGetObject(ctx, bucket, key, expiry, nil)
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned url: %w", err)
	}

	return u.String(), nil
}

// PresignedPutURL generates a presigned URL for uploading
func (c *StorageClient) PresignedPutURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error) {
	if bucket == "" {
		bucket = c.config.Bucket
	}

	u, err := c.client.PresignedPutObject(ctx, bucket, key, expiry)
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned url: %w", err)
	}

	return u.String(), nil
}

// PublicURL returns the public URL for an object
func (c *StorageClient) PublicURL(bucket, key string) string {
	if bucket == "" {
		bucket = c.config.Bucket
	}
	return c.buildURL(bucket, key)
}

// buildURL constructs the URL for an object
func (c *StorageClient) buildURL(bucket, key string) string {
	if c.config.PublicBaseURL != "" {
		return c.config.PublicBaseURL + "/" + path.Join(bucket, key)
	}

	scheme := "http"
	if c.config.UseSSL {
		scheme = "https"
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   c.config.Endpoint,
		Path:   path.Join(bucket, key),
	}

	return u.String()
}

// Health checks the MinIO connection
func (c *StorageClient) Health(ctx context.Context) HealthStatus {
	start := time.Now()

	_, err := c.client.ListBuckets(ctx)
	if err != nil {
		return HealthStatus{
			Healthy: false,
			Latency: time.Since(start),
			Error:   err.Error(),
		}
	}

	return HealthStatus{
		Healthy: true,
		Latency: time.Since(start),
	}
}

// Client returns the underlying MinIO client for advanced operations
func (c *StorageClient) Client() *minio.Client {
	return c.client
}

// UploadOptions configures an upload operation
type UploadOptions struct {
	Bucket      string
	Key         string
	Reader      io.Reader
	Size        int64
	ContentType string
	Metadata    map[string]string
}

// UploadResult contains the result of an upload
type UploadResult struct {
	Bucket   string
	Key      string
	ETag     string
	Size     int64
	Location string
}

// ObjectInfo contains information about an object
type ObjectInfo struct {
	Key          string
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
	Metadata     map[string]string
}
