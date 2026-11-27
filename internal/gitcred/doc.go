// Package gitcred names the git credential this deployment holds for a host.
//
// The credential for a host is the environment variable named after that host:
// GIT_MIRROR_TOKEN_<HOST>, with the host upper-cased and every character that
// cannot appear in a variable name folded to an underscore. So github.com is
// GIT_MIRROR_TOKEN_GITHUB_COM and git.hanzo.ai is GIT_MIRROR_TOKEN_GIT_HANZO_AI.
// The deployment projects each one from KMS; nothing here holds a value.
//
// It is the DEPLOYMENT's credential, so only the deployment's own work reads it:
// the community replica it keeps on its own account. A URL a tenant names is
// reached with the tenant's credential or none, so no git client falls back to
// this one. Keyed by host, a credential minted for one forge is never offered to
// another.
package gitcred
