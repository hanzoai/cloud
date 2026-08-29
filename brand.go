package cloud

// White-label brand facts now live in their own leaf, github.com/hanzoai/cloud/brand,
// so the light host (cmd/cloud) and the webui console can reach them without
// linking package cloud. These aliases keep the in-package spelling —
// cloud.BrandForHost, cloud.IssuerForBrand, cloud.DefaultBrand — that config,
// serve, identity and the apps already use; the logic is one copy, in brand.
import "github.com/hanzoai/cloud/brand"

// DefaultBrand is the fallback brand when CLOUD_BRAND is unknown.
const DefaultBrand = brand.Default

var (
	// BrandFor returns the brand.Info for a brand id (Hanzo default for unknown).
	BrandFor = brand.For
	// BrandDisplay renders a brand id for display ("hanzo" → "Hanzo"). One rule:
	// the registry keys are lowercase ascii, so the first rune is the whole job,
	// and an empty id is the default brand rather than an empty title.
	BrandDisplay = brand.Display
	// IssuerForBrand returns the canonical OIDC issuer for a brand id.
	IssuerForBrand = brand.IssuerFor
	// BrandForHostOK resolves a request Host to a brand id, ok=false if no brand
	// domain matches.
	BrandForHostOK = brand.ForHostOK
	// BrandForHost is BrandForHostOK with the Hanzo default for an unmatched Host.
	BrandForHost = brand.ForHost
	// BrandIssuers returns the OIDC issuer of every configured white-label brand.
	BrandIssuers = brand.Issuers
)
