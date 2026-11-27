package s3

import (
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/sdk/object"
	hs3 "github.com/hanzos3/go"
)

// wrap renders a client error in sdk/object's terms. The store's own error stays
// in the chain, so a log still says what the store said.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	resp, ok := errors.AsType[hs3.ErrorResponse](err)
	if !ok {
		return err
	}
	var sentinel error
	switch resp.Code {
	case "NoSuchBucket":
		sentinel = object.ErrNoBucket
	case "NoSuchKey":
		sentinel = object.ErrNoKey
	case "NoSuchUpload":
		sentinel = object.ErrNoUpload
	case "BucketNotEmpty":
		sentinel = object.ErrNotEmpty
	case "PreconditionFailed", "ConditionalRequestConflict":
		sentinel = object.ErrConflict
	default:
		switch {
		case resp.StatusCode == 412:
			sentinel = object.ErrConflict
		case resp.StatusCode == 408 || resp.StatusCode == 429:
			// Not now: the caller retries, and it is not a refusal.
		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			sentinel = object.ErrRefused
		}
	}
	if sentinel == nil {
		return err
	}
	return fmt.Errorf("%w: %w", sentinel, err)
}
