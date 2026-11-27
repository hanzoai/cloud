package graph

// erase.go is the right to erasure, and the ONE place this plane breaks its own
// rule. Everywhere else nothing is ever overwritten and a retraction is an
// assertion, because a reader must be able to see that something was retracted.
// A data subject who asks to be erased is asking for the opposite — to be gone,
// not retracted — so this removes rows, and it is kept narrow enough that it
// cannot be used for anything else:
//
//   - it removes exactly the assertions that NAME one entity, as subject or as
//     the entity an edge points at, in the caller's own (org, project) file;
//   - it removes nothing at all while any of those rows is under litigation hold;
//   - it requires an admin of the organization it acts in, and is never
//     cross-tenant: platform sudo does not reach another org's subjects;
//   - it is recorded in the audit trail before it commits, and refused on a
//     deployment that has no trail to record it in.
//
// What it keeps is a RECEIPT and not a tombstone. The receipt names the content
// addresses removed and a digest over them; the trail keeps only the digest and
// the count. Neither the entity nor anything asserted about it is written
// anywhere, because a record of what was erased that re-identifies the subject
// is the data again.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/claim"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// reasonMax bounds the one free-text field this op writes to the trail.
const reasonMax = 512

// eraseMax bounds one call, as a walk is bounded and for the same reason: the
// store holds one connection, so an erasure that runs long holds the org's
// write path. A subject named by more rows is erased over several calls, each
// with its own receipt, and Remaining says how many are left.
const eraseMax = walkBound

// A store's status on a receipt. There is no "failed": the graph either
// commits the whole erasure or answers an error with nothing removed, and no
// other store is called.
const (
	erased      = "erased"      // rows naming the entity were removed
	absent      = "absent"      // the store held none
	unsupported = "unsupported" // this op cannot reach the store; it may still hold the subject
)

// unreached is every other store in this binary that holds what an org says
// about a subject, and why this op does not reach it. It is on every receipt,
// because a receipt that listed only what it removed would read as complete.
var unreached = []graphStore{
	{
		Store: "knowledge", Status: unsupported,
		Detail: "kb pages, memories and sources — and their vectors in the org's namespace — carry no " +
			"subject key, and the only delete (framework_delete) removes one document by name; " +
			"there is no delete by subject to call",
	},
	{
		Store: "ai.memory", Status: unsupported,
		Detail: "/v1/ai/memory is served by github.com/hanzoai/ai, keyed by (org, user); a memory " +
			"carries no subject key, and POST /v1/ai/memory/delete removes one of the caller's own " +
			"memories by id; there is no org-wide delete by subject and no typed op to call it",
	},
}

// graphEraseIn names the subject to erase and says why.
type graphEraseIn struct {
	// Entity is the key to erase. Every assertion ABOUT it and every edge
	// pointing AT it is removed, under every spelling it may have been filed
	// under — its NFC key, that key decomposed, and the bytes sent — so a subject
	// filed before keys were folded goes with the rest; a property whose scalar
	// happens to spell it is not a reference and stays. Required, 512 bytes at
	// most.
	Entity string `json:"entity" validate:"required"`
	// Reason is why, as the audit trail will keep it — a request number, a legal
	// basis. It outlives the erasure, so it may not contain the entity: refused
	// when it does. Required, 512 bytes at most.
	Reason string `json:"reason" validate:"required"`
}

// graphEraseOut is the receipt. It can be checked later against the audit
// trail without anything erased being kept: recompute Digest from IDs and find
// the trail record whose resource is that digest.
type graphEraseOut struct {
	// Entity is the key that was erased, folded as every key here is (NFC, no
	// control characters) and echoed so a stored receipt says what it is for.
	// This plane keeps no copy of it.
	Entity string `json:"entity"`
	// IDs are the content addresses of the assertions removed, ascending.
	IDs []string `json:"ids"`
	// Digest is SHA-256, hex, over IDs in their ascending order, each written
	// `<length>:<id>` — the same content address an assertion's own ID is. The
	// audit trail records it in place of the ids, so the receipt verifies
	// against the trail and the trail names nothing.
	Digest string `json:"digest"`
	// Stores is every store in this deployment that may hold what the org says
	// about the subject, and what happened in each: this graph first, then the
	// stores this op cannot reach.
	Stores []graphStore `json:"stores"`
	// Complete is true only when no store may still hold the subject: every
	// store erased or absent and nothing Remaining. While a store is
	// unsupported it is false, and that store is handled out of band before
	// the erasure is done.
	Complete bool `json:"complete"`
	// Remaining is how many assertions still name the entity because one call
	// removes at most ten thousand. Non-zero means call again; each call is its
	// own receipt.
	Remaining int `json:"remaining"`
	// Sources are the documents the removed assertions were filed from. This
	// plane does not own them: ingesting one again files the subject again.
	Sources []string `json:"sources,omitempty"`
	// At is the instant of the erasure, RFC 3339, and the time on its audit
	// record.
	At string `json:"at"`
	// By is the identity that erased — `owner` or `owner/user` — from the
	// validated principal.
	By string `json:"by"`
}

// graphStore is what one store did with an erasure.
type graphStore struct {
	// Store names it: `graph` is the caller's own (org, project) assertion
	// file, its full-text index included; `knowledge` is the org's wiki pages,
	// memories and sources with their vectors; `ai.memory` is /v1/ai/memory.
	Store string `json:"store"`
	// Status is erased (rows naming the entity were removed), absent (it held
	// none) or unsupported (this op cannot reach it, so it may still hold the
	// subject).
	Status string `json:"status"`
	// Removed is how many of its rows were removed.
	Removed int `json:"removed"`
	// Detail says what an unsupported store is missing before an erasure could
	// reach it.
	Detail string `json:"detail,omitempty"`
}

// erase removes every assertion that names an entity and returns a receipt.
//
// It requires an admin of the organization it acts in; platform sudo is not
// enough, so erasure is never cross-tenant. Nothing is removed while a matching
// assertion is under litigation hold (409) or where the erasure cannot be
// recorded (503). The audit trail keeps who, when, why, how many and the
// receipt's digest, never the entity, and holds it before the erasure commits.
//
// Time: erasure is the one act outside the two times. The rows go at every
// as_of and as_known, so no past read recovers them.
//
// Example: {"entity": "acme/person/42", "reason": "DSR-1187, GDPR Art. 17"}
func (o ops) erase(ctx context.Context, in *graphEraseIn) (*graphEraseOut, error) {
	c, ok := cloud.Request(ctx)
	if !ok || !admin(ctx) {
		return nil, zip.ErrForbidden("erasure requires an admin of the organization it acts in; platform sudo does not reach another tenant's subjects")
	}
	rec := o.s.State.audit
	if rec == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"no durable audit store is configured on this deployment; an erasure is recorded before it is made")
	}
	entity, reason := key(in.Entity), strings.TrimSpace(in.Reason)
	if err := bounded("entity", entity, entityMax); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if err := bounded("reason", reason, reasonMax); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if strings.Contains(strings.ToLower(key(reason)), strings.ToLower(entity)) {
		return nil, zip.ErrBadRequest("reason contains the entity; the audit trail keeps the reason after the subject is gone")
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	sc, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Truncate(time.Second)
	out := &graphEraseOut{Entity: entity, At: now.Format(time.RFC3339), By: sc.by}
	// entry is the trail's record of this erasure, set once the trail holds it.
	var entry *audit.Record
	witness := func(gone []Fact) error {
		out.IDs = make([]string, 0, len(gone))
		for _, f := range gone {
			out.IDs = append(out.IDs, f.ID)
			out.Sources = append(out.Sources, f.Source)
		}
		slices.Sort(out.IDs)
		slices.Sort(out.Sources)
		out.Sources = slices.Compact(out.Sources)
		out.Digest = claim.Digest(out.IDs...)
		r, err := act(c, org, "graph.erase", out.Digest, now, map[string]any{
			"graph": sc.ns.String(), "reason": reason, "removed": len(out.IDs), "digest": out.Digest,
		})
		if err != nil {
			return err
		}
		if _, err := rec.Append(ctx, r); err != nil {
			return err
		}
		entry = &r
		return nil
	}
	gone, held, remaining, err := st.erase(ctx, in.Entity, eraseMax, witness)
	switch {
	case errors.Is(err, errUncheckpointed):
		// Committed and recorded, so the receipt stands: every read has lost the
		// rows. The bytes leave the file at its next checkpoint.
		o.s.Log.Error("graph: erased and not checkpointed", "graph", sc.ns.String(), "err", err)
	case err != nil:
		o.s.Log.Error("graph: erasure failed; nothing was removed", "graph", sc.ns.String(), "err", err)
		if entry != nil {
			// The trail took the erasure and the file did not.
			o.s.State.unmade(ctx, *entry, sc.ns.String())
		}
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the erasure could not be recorded and made, so nothing was removed; retry")
	case held > 0:
		return nil, zip.Errorf(http.StatusConflict,
			"%d assertions naming this entity are under litigation hold, so nothing was removed", held)
	}
	here := graphStore{Store: "graph", Status: absent}
	if len(gone) > 0 {
		here = graphStore{Store: "graph", Status: erased, Removed: len(gone)}
	}
	out.Stores = append([]graphStore{here}, unreached...)
	out.Remaining = remaining
	out.Complete = remaining == 0 && !slices.ContainsFunc(out.Stores, func(g graphStore) bool {
		return g.Status == unsupported
	})
	o.s.Log.Info("graph: erased", "graph", sc.ns.String(), "by", sc.by,
		"removed", len(gone), "remaining", remaining, "digest", out.Digest)
	return out, nil
}
