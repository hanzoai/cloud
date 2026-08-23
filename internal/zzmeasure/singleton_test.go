package zzmeasure

// Production arrangement: each app is its own process (plugin/<app>/main.go links
// ONE app's Mount). A callee's package may be LINKED into the caller's binary, but
// its Mount never runs there — so its process-global is nil and every read of it
// answers the zero value.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/code"
	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/finance"
	iamclient "github.com/hanzoai/cloud/apps/iam"
	"github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/apps/knowledge"
	"github.com/hanzoai/cloud/apps/projects"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/apps/wallet"
)

func TestUnmountedSingletons(t *testing.T) {
	t.Logf("index.Ready()            = %v   (apps/search lexical leg)", index.Ready())
	t.Logf("code.Ready()             = %v   (apps/search code leg)", code.Ready())
	t.Logf("knowledge.SemanticReady()= %v   (apps/search semantic leg)", knowledge.SemanticReady())
	t.Logf("agents.Ready()           = %v   (apps/link stop/count sessions)", agents.Ready())
	t.Logf("projects.Ready()         = %v   (apps/catalog serving)", projects.Ready())
	t.Logf("wallet.Mounted()         = %v   (apps/x402 payee)", wallet.Mounted())
	t.Logf("finance.Current() == nil = %v   (apps/admin, apps/billing, apps/usage)", finance.Current() == nil)
	t.Logf("iamclient.DB() == nil    = %v   (apps/deploy iamProjects)", iamclient.DB() == nil)
	t.Logf("datastore.Ready()        = %v   (apps/usage, apps/o11y, apps/event...)", datastore.Ready())

	// the tool plane: every source registers into this process-global from ITS OWN Mount
	reg := tools.Default()
	list := reg.List(context.Background(), tools.Scope{Org: "acme"})
	t.Logf("tools.Default().List     = %d tools  (apps/tools process has no source providers)", len(list))

	// agents cross-process reads
	_, err := agents.ListForOrg(context.Background(), "acme")
	t.Logf("agents.ListForOrg        = err=%v   (apps/team bots)", err)
	_, err = agents.TargetsForOrg(context.Background(), "acme")
	t.Logf("agents.TargetsForOrg     = err=%v   (apps/visor board)", err)
	n, err := agents.SeedPersonalities(context.Background(), "acme")
	t.Logf("agents.SeedPersonalities = n=%d err=%v   (apps/team account)", n, err)

	_, _, ok := wallet.TreasuryAnchorSigner(context.Background(), "acme", "lux")
	t.Logf("wallet.TreasuryAnchorSigner ok=%v   (apps/treasury anchor_bind)", ok)
}
