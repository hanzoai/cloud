// Copyright (c) 2026 Hanzo AI Inc.

package s3

import (
	"context"
	"github.com/hanzoai/cloud/sdk/object"
	"math"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	flagsplane "github.com/hanzoai/cloud/client/flag"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/principal"
	s3 "github.com/hanzos3/go"
	"github.com/zap-proto/zip"
)

// orgQuota is the total storage an organization may hold across all of its S3
// buckets on monthly plans (100 GiB by default, overrideable via S3_QUOTA_MIB).
var orgQuota = int64(env.Int("S3_QUOTA_MIB", 100<<10)) << 20

// maxObjectBytes is the largest single object an upload may store (2 GiB by
// default, overrideable via S3_MAX_OBJECT_MIB).
var maxObjectBytes = int64(env.Int("S3_MAX_OBJECT_MIB", 2<<10)) << 20

// effectiveQuota resolves an organization's S3 storage quota in bytes:
//  1. A per-organization override set in admin.hanzo.ai (flags.StorageOrgKey(org));
//  2. The highest quota configured for any active subscription plan the organization
//     holds (flags.StoragePlanKey(plan));
//  3. The platform default switch (flags.StorageOrgQuotaGiB);
//  4. The compiled/env fallback (orgQuota, 100 GiB by default).
//
// A configured value of -1 GiB means unlimited storage (math.MaxInt64).
func effectiveQuota(ctx context.Context, org string) int64 {
	if org != "" {
		if v, err := flagsplane.FlagsValue(cloud.For(ctx, principal.Reserved), &client.FlagValueIn{Key: flags.StorageOrgKey(org), Kind: "int"}); err == nil && v != nil && v.Set {
			if v.N == -1 {
				return math.MaxInt64
			}
			if v.N > 0 {
				return int64(min(v.N, flags.MaxStorageQuotaGiB)) << 30
			}
		}
		if subs, err := commercepeer.BillingSubscriptions(cloud.For(ctx, org), &client.SubsIn{}); err == nil && subs != nil {
			var planMax int64
			for _, s := range subs.Rows {
				if !strings.EqualFold(strings.TrimSpace(s.Status), "active") {
					continue
				}
				slugs := []string{s.PlanID, s.Plan.Slug}
				for _, line := range s.Lines {
					if line.Plan != "" {
						slugs = append(slugs, line.Plan)
					}
				}
				for _, slug := range slugs {
					if slug == "" {
						continue
					}
					v, err := flagsplane.FlagsValue(cloud.For(ctx, principal.Reserved), &client.FlagValueIn{Key: flags.StoragePlanKey(slug), Kind: "int"})
					if err != nil || v == nil || !v.Set {
						continue
					}
					if v.N == -1 {
						return math.MaxInt64
					}
					if v.N > 0 {
						if b := int64(min(v.N, flags.MaxStorageQuotaGiB)) << 30; b > planMax {
							planMax = b
						}
					}
				}
			}
			if planMax > 0 {
				return planMax
			}
		}
	}
	if v, err := flagsplane.FlagsValue(cloud.For(ctx, principal.Reserved), &client.FlagValueIn{Key: flags.StorageOrgQuotaGiB, Kind: "int"}); err == nil && v != nil && v.Set {
		if v.N == -1 {
			return math.MaxInt64
		}
		if v.N > 0 {
			return int64(min(v.N, flags.MaxStorageQuotaGiB)) << 30
		}
	}
	return orgQuota
}

func effectiveMaxObjectBytes(ctx context.Context) int64 {
	if v, err := flagsplane.FlagsValue(cloud.For(ctx, principal.Reserved), &client.FlagValueIn{Key: flags.StorageMaxFileMiB, Kind: "int"}); err == nil && v != nil && v.Set && v.N > 0 {
		return int64(v.N) << 20
	}
	return maxObjectBytes
}

// withinQuota refuses an upload that exceeds the per-file ceiling (2 GiB) or
// would push the organization's total stored bytes across its S3 buckets
// (`o<orghash>-*`) past its organization limit (100 GiB on monthly plans, or
// the Enterprise / custom plan / org override configured in admin.hanzo.ai).
func (o ops) withinQuota(ctx context.Context, cli *s3.Client, org, targetBucket string, more int64) error {
	maxFile := effectiveMaxObjectBytes(ctx)
	if more > maxFile {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "s3: file exceeds the %d GiB per-file limit", max(1, maxFile>>30))
	}
	limit := effectiveQuota(ctx, org)
	if limit == math.MaxInt64 {
		return nil
	}
	if more > limit {
		return zip.Errorf(http.StatusInsufficientStorage, "s3: this would keep more than your organization's %d GiB limit; delete something first", max(1, limit>>30))
	}
	prefix := object.BucketPrefix(org)
	var buckets []string
	if all, err := cli.ListBuckets(ctx); err == nil {
		for _, b := range all {
			if strings.HasPrefix(b.Name, prefix) {
				buckets = append(buckets, b.Name)
			}
		}
	} else if targetBucket != "" {
		buckets = []string{targetBucket}
	}
	var held int64
	for _, b := range buckets {
		for obj := range cli.ListObjects(ctx, b, s3.ListObjectsOptions{Recursive: true}) {
			if obj.Err != nil {
				break
			}
			held += obj.Size
			if held+more > limit {
				return zip.Errorf(http.StatusInsufficientStorage, "s3: this would keep more than your organization's %d GiB limit; delete something first", max(1, limit>>30))
			}
		}
	}
	return nil
}
