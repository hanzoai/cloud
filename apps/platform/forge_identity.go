package platform

import (
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/env"
)

const readerTokenPath = "/run/hanzo/forge/token"

func nativeForgeBuilds() bool { return env.Or("CLOUD_FORGE_BUILD_AUTH", "legacy") == "iam" }

// forgeReaderOrigin is where a build's reader token is admitted: the forge's public
// address, which is the one the solve fetches the context from. build-isolation
// lets a build pod reach the internet and no private range, so the in-cluster
// service (http://hanzo-git.hanzo.svc) is unreachable from it, and admission there
// failed every native build with "Forge reader admission is unreachable".
const forgeReaderOrigin = "https://git.hanzo.ai"

// iamOrigin is the IAM a build's init container exchanges its assertion at, and the audience that
// assertion names: this deployment's issuer.
func iamOrigin() string {
	return brand.Issuer(env.Or("CLOUD_IAM_ISSUER", ""), cloud.Brand())
}

// A build may read source and packages. Only the init container receives the
// projected assertion; the solve receives its short-lived read-only IAM bearer.
// Neither the control-plane credential nor a Secrets API write is needed.
func addForgeIdentity(pod map[string]any) {
	pod["serviceAccountName"] = "forge-reader"
	pod["automountServiceAccountToken"] = false
	pod["imagePullSecrets"] = []any{map[string]any{"name": "ghcr-secret"}}
	pod["volumes"] = append(pod["volumes"].([]any),
		map[string]any{"name": "forge-assertion", "projected": map[string]any{"sources": []any{map[string]any{"serviceAccountToken": map[string]any{"path": "token", "audience": iamOrigin(), "expirationSeconds": int64(3600)}}}}},
		map[string]any{"name": "forge-identity", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "1Mi"}})
	init := map[string]any{"name": "forge-identity", "image": env.Or("CLOUD_FORGE_TOKEN_IMAGE", ""), "command": []any{"/cloud", "secret", "token", "--out", readerTokenPath, "--forge-reader", forgeReaderOrigin}, "env": []any{envVar("HANZO_IAM_URL", iamOrigin()), envVar("HANZO_SA_TOKEN", "/run/hanzo/assertion/token")}, "volumeMounts": []any{map[string]any{"name": "forge-assertion", "mountPath": "/run/hanzo/assertion", "readOnly": true}, forgeIdentityMount(false)}, "securityContext": confined(), "resources": map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "32Mi"}, "limits": map[string]any{"cpu": "1", "memory": "128Mi"}}}
	old, _ := pod["initContainers"].([]any)
	pod["initContainers"] = append([]any{init}, old...)
}
func forgeIdentityMount(readOnly bool) map[string]any {
	return map[string]any{"name": "forge-identity", "mountPath": "/run/hanzo/forge", "readOnly": readOnly}
}
func forgeReaderSecret(s solveSecret) bool {
	return s.secret == forgeTokenSecret || s.secret == registryTokenSecret
}
func validForgeTokenImage() bool {
	return strings.Contains(env.Or("CLOUD_FORGE_TOKEN_IMAGE", ""), "@sha256:")
}
