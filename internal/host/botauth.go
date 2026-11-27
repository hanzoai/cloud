package host

// The deployment's Web Bot Auth signer (hanzo-inc/cloud#423), built once per
// process from the KMS handle BuildDeps just made, so every outbound path that
// signs through sdk/botauth signs with the same key wherever it runs: the crawl
// app, ai's fetch tool, the answer engine's reads, websearch's page reads.

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/botauth"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/secret"
)

// botKeyRef is the key set's KMS reference beneath the brand's org: one Ed25519
// seed per line, the first signing (sdk/botauth). The subtree is custodied
// (apps/kms custody.go), so no member of the org reads it over REST; SuperAdmin
// rotates it there, audited.
func botKeyRef(org string) string { return "orgs/" + org + "/botauth/WEB_BOT_AUTH_KEYS@prod" }

// useBotAuth installs this process's signer. The signing identity is the
// deployment's own brand: its key under the brand's org, its directory on the
// brand's API host, so api.hanzo.ai publishes the key Hanzo's agents sign with.
func useBotAuth(kms secret.Store) {
	if kms == nil {
		botauth.Use(nil)
		return
	}
	id := cloud.Brand()
	botauth.Use(&botauth.Signer{
		Agent: "https://" + brand.APIHost(id) + botauth.Path,
		Store: botKeys{kms: kms, org: id},
	})
}

// botKeys keeps the key set in KMS. It reads and writes acting for the brand's
// org on a context of its own: the key is the deployment's whichever org's
// request the fetch was made for, and an inbound request's org would win over a
// stated one.
type botKeys struct {
	kms secret.Store
	org string
}

func (k botKeys) Load(ctx context.Context) ([]byte, error) {
	v, err := k.kms.GetSecret(cloud.For(context.WithoutCancel(ctx), k.org), botKeyRef(k.org))
	if errors.Is(err, secret.ErrNone) {
		return nil, botauth.ErrNoKeys
	}
	return v, err
}

func (k botKeys) Save(ctx context.Context, set []byte) error {
	return k.kms.PutSecret(cloud.For(context.WithoutCancel(ctx), k.org), botKeyRef(k.org), set)
}
