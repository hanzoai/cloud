// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

// files.go keeps a Base's uploaded files in hanzoai/s3 and not on the pod's volume.
//
// A Base writes an uploaded file to a filesystem it is handed; left alone that is
// a directory under the Base's own (core.LocalStorageDirName), which is state on
// the pod. With the store configured (S3_ADMIN_*, the credential every subsystem
// of this binary reads through sdk/object) the host hands Base an S3 filesystem
// instead, over a bucket of the org's own ("base" under the org's bucket names,
// so one org's files are a bucket no other org's credential-less request can
// name). The credential is the host's and is read when the filesystem is opened;
// it is never written into the instance's settings row, which is why this does not
// use the instance's own S3 settings.
//
// FILES ALREADY ON THE VOLUME are copied once, when the Base opens: every file,
// with its original name and content type, is read back from the bucket and
// compared in size before a marker says so. The directory is left as it was (the
// copy never removes), which is what makes the cutover reversible.

import (
	"context"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"os"
	"path/filepath"
	"time"

	"github.com/hanzoai/base/tools/filesystem"

	"github.com/hanzoai/cloud/sdk/principal"
)

// storageDir is where a Base keeps uploaded files when it is given no filesystem
// (core.LocalStorageDirName), and filesMark is what says the copy to S3 is done.
const (
	storageDir = "storage"
	filesMark  = ".storage-in-s3"
)

// fileBucket is the friendly name of the bucket an org's Base files are in.
const fileBucket = "base"

// backupBucket is the friendly name of the bucket an org's Base backups are in.
const backupBucket = "base-backups"

// filesFor answers the filesystem funcs for a Base (its uploaded files, and its
// backups), and nil for both when the object store gives out no credential (local
// disk), in which case Base keeps them in its directory as it always did. The
// buckets are made if they are not there, and the files already on the volume are
// copied into the first, before the Base opens.
//
// Base writes with an S3 client of its own, so it takes the store's credential
// (object.Delegate): the whole store's key, which is all the store has to give.
func filesFor(ctx context.Context, org, dir string) (files, backups func() (*filesystem.System, error), err error) {
	// A deployment that promised an object store and has none must not quietly
	// keep its files on this volume.
	if err := object.Check(); err != nil {
		return nil, nil, err
	}
	st, ok := object.Delegate()
	if !ok {
		return nil, nil, nil
	}
	store := object.Default()
	opener := func(friendly string) (func() (*filesystem.System, error), error) {
		bucket := object.BucketName(orgOrReserved(org), friendly)
		if err := store.Ensure(ctx, bucket); err != nil {
			return nil, fmt.Errorf("make the bucket %s: %w", friendly, err)
		}
		return func() (*filesystem.System, error) {
			return filesystem.NewS3(bucket, st.Region, st.URL, st.AccessKey, st.Secret, st.PathStyle)
		}, nil
	}
	if files, err = opener(fileBucket); err != nil {
		return nil, nil, err
	}
	if backups, err = opener(backupBucket); err != nil {
		return nil, nil, err
	}
	if err := carry(ctx, dir, files); err != nil {
		return nil, nil, err
	}
	return files, backups, nil
}

func orgOrReserved(org string) string {
	if org == "" {
		return principal.Reserved
	}
	return org
}

// carry copies the files on the volume into the bucket, once.
func carry(ctx context.Context, dir string, open func() (*filesystem.System, error)) error {
	local := filepath.Join(dir, storageDir)
	mark := filepath.Join(dir, filesMark)
	if _, err := os.Stat(mark); err == nil {
		return nil
	}
	if fi, err := os.Stat(local); err != nil || !fi.IsDir() {
		return nil // nothing was ever uploaded
	}
	src, err := filesystem.NewLocal(local)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := open()
	if err != nil {
		return err
	}
	defer func() { _ = dst.Close() }()
	src.SetContext(ctx)
	dst.SetContext(ctx)

	list, err := src.List("")
	if err != nil {
		return fmt.Errorf("list the files on the volume: %w", err)
	}
	var n, bytes int64
	for _, obj := range list {
		if obj.IsDir {
			continue
		}
		f, err := src.GetReuploadableFile(obj.Key, true)
		if err != nil {
			return fmt.Errorf("read %s: %w", obj.Key, err)
		}
		if err := dst.UploadFile(f, obj.Key); err != nil {
			return fmt.Errorf("copy %s to the bucket: %w", obj.Key, err)
		}
		// Proven by reading the object back from the store, not by the write
		// having returned.
		got, err := dst.Attributes(obj.Key)
		if err != nil {
			return fmt.Errorf("read %s back: %w", obj.Key, err)
		}
		if got.Size != obj.Size {
			return fmt.Errorf("%s is %d bytes on the volume and %d in the bucket", obj.Key, obj.Size, got.Size)
		}
		n, bytes = n+1, bytes+obj.Size
	}
	note := fmt.Sprintf("%d files, %d bytes, copied %s\n", n, bytes, time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(mark, []byte(note), 0o600)
}
