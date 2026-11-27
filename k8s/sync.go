package k8s

import (
	"context"
	"errors"

	"encoding/json"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// ErrSyncRefused is wrapped by every refusal RequestSync makes before it writes:
// a sync already queued or running, or an Application CD has not compared with
// git. The wrapping error's message says which.
var ErrSyncRefused = errors.New("sync refused")

type syncRefusal string

func (r syncRefusal) Error() string        { return string(r) }
func (r syncRefusal) Is(target error) bool { return target == ErrSyncRefused }

// RequestSync asks CD to sync one Application: an operation on the Application,
// initiated by actor, applying the commit CD last compared with the Application's
// own sync options and no pruning. It is the request cd.hanzo.ai's Sync button
// makes; RequestSyncTo is the one implementation of it, and every surface that
// syncs a CD Application calls one of the two.
func RequestSync(ctx context.Context, dyn dynamic.Interface, obj *unstructured.Unstructured, actor, reason string) (*unstructured.Unstructured, error) {
	return RequestSyncTo(ctx, dyn, obj, "", actor, reason)
}

// RequestSyncTo is RequestSync aimed at one revision. Empty is the commit CD last
// compared (status.sync.revision), never a branch name, which would apply whatever
// the branch holds when CD gets to it. Anything else is a ROLLBACK: it must be a
// commit CD deployed before (status.history) and not the one it deployed last, and
// the Application must not sync itself, because CD would put git's head back on
// its next poll — a rollback there is a revert in git.
//
// The resourceVersion of obj rides the patch, so an Application another sync or a
// reconcile changed since it was read is refused with a conflict (apierrors
// IsConflict) rather than overwritten. It returns the Application as patched.
func RequestSyncTo(ctx context.Context, dyn dynamic.Interface, obj *unstructured.Unstructured, to, actor, reason string) (*unstructured.Unstructured, error) {
	name := obj.GetName()
	if _, pending, _ := unstructured.NestedMap(obj.Object, "operation"); pending {
		return nil, syncRefusal(name + " already has a sync queued")
	}
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "operationState", "phase")
	if phase == "Running" || phase == "Terminating" {
		return nil, syncRefusal(name + " is already syncing")
	}
	revision, _, _ := unstructured.NestedString(obj.Object, "status", "sync", "revision")
	if to != "" {
		if err := rollbackTarget(obj, to); err != nil {
			return nil, err
		}
		revision = to
	}
	if revision == "" {
		return nil, syncRefusal(name + " has not been compared with git yet, so there is no commit to sync; try again once CD has reconciled it")
	}
	options, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "syncPolicy", "syncOptions")
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"resourceVersion": obj.GetResourceVersion()},
		"operation": map[string]any{
			"initiatedBy": map[string]any{"username": actor},
			"info":        []any{map[string]any{"name": "Reason", "value": reason}},
			"sync":        map[string]any{"revision": revision, "prune": false, "syncOptions": options},
		},
	})
	if err != nil {
		return nil, err
	}
	return dyn.Resource(CDApplications).Namespace(obj.GetNamespace()).Patch(ctx, name, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
}

// rollbackTarget refuses a revision a rollback cannot go to: one CD never
// deployed, the one it deployed last, or any revision of an Application that
// syncs itself.
func rollbackTarget(obj *unstructured.Unstructured, to string) error {
	name := obj.GetName()
	if auto, ok, _ := unstructured.NestedMap(obj.Object, "spec", "syncPolicy", "automated"); ok && auto != nil {
		return syncRefusal(name + " syncs itself, so CD would apply git's head again on its next poll; roll it back by reverting the change in git")
	}
	history, _, _ := unstructured.NestedSlice(obj.Object, "status", "history")
	last := ""
	found := false
	for _, h := range history {
		entry, ok := h.(map[string]any)
		if !ok {
			continue
		}
		rev, _, _ := unstructured.NestedString(entry, "revision")
		last = rev
		if rev != "" && rev == to {
			found = true
		}
	}
	switch {
	case !found:
		return syncRefusal(to + " is not a revision CD deployed " + name + " at; name one from its history")
	case to == last:
		return syncRefusal(name + " already runs " + to + "; name an earlier revision from its history")
	}
	return nil
}
