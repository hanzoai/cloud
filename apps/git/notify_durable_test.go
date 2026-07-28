package git

import (
	"testing"

	"github.com/hanzoai/cloud"
)

// The idempotency key is the correctness crux of durable delivery: ExecuteWorkflow
// dedupes on it, so a key that is too COARSE collapses two distinct facts into one
// execution and silently drops a notice, while a key that varies per attempt defeats
// dedupe and double-posts.

func TestNotifyIDKeysOnTheFact(t *testing.T) {
	push := cloud.LifecycleEvent{
		Kind: cloud.LifecyclePushLanded, Org: "acme", Repo: "code",
		Branch: "main", After: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	// Same fact twice => same id, so a redelivery is delivered ONCE.
	if a, b := notifyID(push), notifyID(push); a != b {
		t.Fatalf("same fact must key identically: %q vs %q", a, b)
	}

	// A different commit is a different fact and must NOT be deduped away.
	other := push
	other.After = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if notifyID(push) == notifyID(other) {
		t.Fatal("two commits must key differently, else the second notice is dropped")
	}

	// Same commit, different KIND: a push landing and a deploy going live off that
	// commit are two facts and both must be delivered.
	deploy := push
	deploy.Kind = cloud.LifecycleDeployLive
	if notifyID(push) == notifyID(deploy) {
		t.Fatal("push.landed and deploy.live on one commit must key differently")
	}

	// Same commit in a different repo (or org) is a different fact.
	otherRepo := push
	otherRepo.Repo = "site"
	if notifyID(push) == notifyID(otherRepo) {
		t.Fatal("two repos must key differently")
	}
	otherOrg := push
	otherOrg.Org = "other"
	if notifyID(push) == notifyID(otherOrg) {
		t.Fatal("two orgs must key differently")
	}
}

func TestNotifyIDFallsBackToDeployID(t *testing.T) {
	// A deploy event carries no commit; the deployment id is its discriminator.
	dep := cloud.LifecycleEvent{
		Kind: cloud.LifecycleDeployLive, Org: "acme", Repo: "code", DeployID: "dep-1",
	}
	if notifyID(dep) == "" {
		t.Fatal("a deploy with a DeployID must be keyable")
	}
	two := dep
	two.DeployID = "dep-2"
	if notifyID(dep) == notifyID(two) {
		t.Fatal("two deployments must key differently")
	}
}

// A fact with NO discriminator cannot be deduped. It must report an empty id so the
// caller delivers inline — returning some constant instead would make every such
// event collapse into one execution and drop all but the first.
func TestNotifyIDEmptyWithoutDiscriminator(t *testing.T) {
	bare := cloud.LifecycleEvent{Kind: cloud.LifecycleDeployFailed, Org: "acme", Repo: "code"}
	if id := notifyID(bare); id != "" {
		t.Fatalf("a discriminator-free fact must not be keyed, got %q", id)
	}
}

func TestNotifiableKinds(t *testing.T) {
	for _, k := range []cloud.LifecycleKind{
		cloud.LifecyclePushLanded, cloud.LifecycleDeployLive, cloud.LifecycleDeployFailed,
	} {
		if !notifiable(k) {
			t.Fatalf("%s must be deliverable", k)
		}
	}
	// BuildStarted is deliberately not a notify kind.
	if notifiable(cloud.LifecycleBuildStarted) {
		t.Fatal("build.started must not be delivered")
	}
	if notifiable(cloud.LifecycleKind("nonsense")) {
		t.Fatal("an unknown kind must not be delivered")
	}
}
