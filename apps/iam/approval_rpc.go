// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/zap-proto/zip"
)

// A person's waitlist state, published on the internal plane.
//
// admission gates a host on it, and until now it asked by REPLAYING the caller's
// own Cookie and Authorization header to IAM's get-account — cloud taking a
// user's raw credential and presenting it to another service so that service
// would answer about that user. It worked, and it is the wrong shape twice over:
// the credential is handled where it need not be, and the answer is only as good
// as a header that a gateway had already validated once.
//
// The plane already carries the validated principal. So the question becomes
// "what does the store say about the caller", asked with no credential in it at
// all, and the raw bearer stops leaving cloud.

// exposeApproval publishes the waitlist read. Mount calls it.
func exposeApproval() {
	zip.Post[struct{}, plane.Approval](cloud.Plane(), "/iam/approval",
		approval,
		zip.WithOperationID(plane.IAMApproval),
		zip.WithSummary("Whether the caller is off the waitlist"))
}

// approval answers with the CALLER'S OWN recorded approvalStatus.
//
// The subject is the caller's and can never be an argument. A caller able to name
// a subject could read another person's waitlist state, which is a small leak on
// its own and a membership oracle in bulk.
//
// It returns the raw status and judges nothing. Whether "pending" gates a person
// is admission's rule, and it lives with the gate — one place decides, and the
// identity store is not it.
//
// A subject with no user row is a REFUSAL, not an empty status. A machine token's
// app-id subject and a deleted user both land there, and admission reads an empty
// status as approved: answering "" for a principal the store does not know would
// wave through exactly the callers least entitled to it. The error path is
// admission's documented fail-open, which is a decision it makes knowingly about
// an IAM it could not reach — not one this handler makes for it silently.
func approval(ctx context.Context, _ *struct{}) (*plane.Approval, error) {
	who := cloud.Who(ctx)
	if who.User == "" {
		return nil, zip.ErrUnauthorized("approval: no subject on the call")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("approval: identity store not open in the process that owns it")
	}
	u, err := iamstore.GetUserBySubject(ctx, db, who.User)
	if err != nil {
		return nil, fmt.Errorf("approval: %w", err)
	}
	if u == nil {
		return nil, zip.ErrUnauthorized("approval: no such subject")
	}
	// Properties is a map, which cannot cross the plane at all (zapenc refuses
	// one), and should not: the caller asked one question and gets one answer.
	return &plane.Approval{Status: u.Properties["approvalStatus"]}, nil
}
