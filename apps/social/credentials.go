package social

import (
	"context"
	"errors"
	"strings"

	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/internal/appcred"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/zap-proto/zip"
)

// An organization's own developer app for one network. The client id comes
// back. The secret never does: secretSet says whether one is sealed. Omitting
// clientSecret on a write leaves the sealed value. An empty string clears it.

// orgApp is one network's app as a read returns it.
type orgApp struct {
	// Provider is the network: x, facebook, instagram, linkedin, tiktok,
	// youtube or threads.
	Provider string `json:"provider"`
	// ClientID is the public app id, in the clear. The screen masks it.
	ClientID string `json:"clientId"`
	// SecretSet is whether a secret is sealed for this app. The secret itself
	// is not in this response.
	SecretSet bool `json:"secretSet"`
}

// orgApps is the list of apps this org has sealed. A network with no app is
// absent, not an empty row. Data is the same list, the envelope the other
// social collections use.
type orgApps struct {
	// Apps is the org's sealed developer apps.
	Apps []orgApp `json:"apps"`
	// Data is Apps again, so a client that reads either envelope sees the list.
	Data []orgApp `json:"data"`
}

// orgAppWrite is one network in a PUT. Only the providers in the body are
// written.
type orgAppWrite struct {
	// Provider is the network to write.
	Provider string `json:"provider" url:"-"`
	// ClientID is the full app id, never a mask. Empty clears the app.
	ClientID string `json:"clientId" url:"-"`
	// ClientSecret is the secret. Absent leaves the sealed value. An empty
	// string clears it.
	ClientSecret *string `json:"clientSecret,omitempty" url:"-"`
}

// orgAppsWrite is the PUT body.
type orgAppsWrite struct {
	// Apps are the networks the caller is changing. The rest stay as they are.
	Apps []orgAppWrite `json:"apps" url:"-"`
}

const appSecretEnv = "default"

func appPath(org, provider string) string {
	return kms.OrgPath(org, "social", "apps", provider)
}

func appRef(path, name string) string {
	return path + "/" + name + "@" + appSecretEnv
}

// credentials lists the apps this org has sealed. The secret is not included.
func (o ops) credentials(ctx context.Context, _ *cloud.Unit) (*orgApps, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	apps := make([]orgApp, 0)
	for _, p := range providerOrder {
		if len(appcred.Pairs(p)) == 0 {
			continue
		}
		id, set, ok := o.readApp(org, p)
		if !ok {
			continue
		}
		apps = append(apps, orgApp{Provider: p, ClientID: id, SecretSet: set})
	}
	return &orgApps{Apps: apps, Data: apps}, nil
}

func (o ops) readApp(org, provider string) (id string, secretSet, ok bool) {
	if o.s.KMS == nil {
		return "", false, false
	}
	path := appPath(org, provider)
	if path == "" {
		return "", false, false
	}
	raw, err := o.s.KMS.GetSecret(context.Background(), appRef(path, "client_id"))
	if err != nil && !errors.Is(err, secret.ErrNone) {
		return "", false, false
	}
	sec, err := o.s.KMS.GetSecret(context.Background(), appRef(path, "client_secret"))
	if err != nil && !errors.Is(err, secret.ErrNone) {
		return "", false, false
	}
	if len(raw) == 0 && len(sec) == 0 {
		return "", false, false
	}
	return string(raw), len(sec) > 0, true
}

// putCredentials seals the apps in the body. A network left out is unchanged.
func (o ops) putCredentials(ctx context.Context, in *orgAppsWrite) (*orgApps, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if o.s.KMS == nil {
		return nil, zip.Errorf(503, "the credential store is unavailable on this deployment")
	}
	for _, w := range in.Apps {
		p := strings.ToLower(strings.TrimSpace(w.Provider))
		if len(appcred.Pairs(p)) == 0 {
			return nil, zip.ErrBadRequest("unknown provider " + p)
		}
		path := appPath(org, p)
		if path == "" {
			return nil, zip.ErrBadRequest("org names no store")
		}
		id := strings.TrimSpace(w.ClientID)
		if id == "" {
			_ = o.s.KMS.DeleteSecret(context.Background(), appRef(path, "client_id"))
			_ = o.s.KMS.DeleteSecret(context.Background(), appRef(path, "client_secret"))
			o.s.Log.Info("social app cleared", "org", org, "provider", p)
			continue
		}
		if err := o.s.KMS.PutSecret(context.Background(), appRef(path, "client_id"), []byte(id)); err != nil {
			return nil, zip.Errorf(503, "the credential store is unavailable on this deployment")
		}
		if w.ClientSecret != nil {
			if *w.ClientSecret == "" {
				_ = o.s.KMS.DeleteSecret(context.Background(), appRef(path, "client_secret"))
			} else if err := o.s.KMS.PutSecret(context.Background(), appRef(path, "client_secret"), []byte(*w.ClientSecret)); err != nil {
				return nil, zip.Errorf(503, "the credential store is unavailable on this deployment")
			}
		}
		o.s.Log.Info("social app saved", "org", org, "provider", p, "secret", w.ClientSecret != nil)
	}
	return o.credentials(ctx, nil)
}

// orgConfigured reports whether this org sealed a complete app for the network.
func orgConfigured(k secret.Store, org, provider string) bool {
	if k == nil {
		return false
	}
	path := appPath(org, provider)
	if path == "" {
		return false
	}
	id, err := k.GetSecret(context.Background(), appRef(path, "client_id"))
	if err != nil || len(id) == 0 {
		return false
	}
	sec, err := k.GetSecret(context.Background(), appRef(path, "client_secret"))
	return err == nil && len(sec) > 0
}
