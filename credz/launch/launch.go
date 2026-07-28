// Package launch is how a launcher tells the credential broker which app it
// started.
//
// THE HOLE THIS CLOSES. The broker used to name its peer by reading
// /proc/<pid>/cmdline, reached through the pid SO_PEERCRED gave it. SO_PEERCRED
// is sound — the kernel records pid and uid at connect(2) from the connecting
// task, and the peer can neither set them nor replay another process's — but
// ARGV IS NOT. execve takes argv from the CALLER, so a process names itself: any
// same-uid process could exec a binary as `billing`, or as `cloud
// --enable=billing`, and be handed billing's KMS scope. At the socket the
// launcher's decision and the child's self-report were the same bytes.
//
// Only the launcher knows which app it started as which process. So identity
// comes from the launcher now: it stamps a token into that ONE child's
// environment at spawn, the child presents it, and the broker verifies it
// against the secret it holds. Nothing the peer chose about itself is consulted.
//
// ONE VARIABLE, NOT TWO. A token is `<app>:<hex hmac-sha256(secret, app)>` — the
// claim and its proof in a single string. Split across two variables, a child
// could pair its own name with a mac it lifted from somewhere else; joined, the
// mac is only ever a mac OF that name, and changing either half breaks the
// other. There is nothing to recombine.
//
// WHAT THIS IS NOT — READ IT BEFORE TREATING THE SCOPE AS A SANDBOX. The token
// lives in the child's environment, and on Linux a process's environment is
// readable at /proc/<pid>/environ by the SAME UID. Every plugin in the pod runs
// as that uid. So this is not a same-uid boundary: it raises the cost from
// "assert any identity, free, from any process, with no prerequisite" to "first
// read a running peer's environ and steal its token" — and a token names one
// app, so the theft only buys the app it was stolen from.
//
// What WOULD be a same-uid boundary is the socket itself as the credential: the
// launcher pre-connects and passes the fd as an ExtraFile, so identity is a
// capability no process can name, spell, or copy out of another's environ. That
// is a change to zip's spawn contract rather than to this package, which is why
// it is not here. Until then: a compromised plugin can still reach what it can
// steal, and the partition is against accident plus casual forgery, not against
// a peer reading its neighbours.
//
// STDLIB ONLY, ON PURPOSE. Both spawn sites import this — the fused binary,
// where the launcher IS the broker in-process, and cmd/cloud, which must stay
// light. Importing credz itself from the host would drag cek → modernc/sqlite +
// sqlcipher in behind it and push a ~395-package build past 415, and that build
// being small is the entire reason cmd/cloud exists. A leaf that depends on
// nothing can be imported by anything, so the launch contract lives in one place
// and both launchers spell it the same way.
package launch

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// TokenEnv carries the launcher's stamp to the child it started. It is
// per-child by construction — the token names ONE app — so it is set in
// zip.Plugin.Env and never in the launcher's own environment, where every child
// would inherit every other's.
const TokenEnv = "CREDZ_TOKEN"

// SecretEnv carries the signing secret to the one child that verifies tokens,
// and to nothing else. A process holding this can mint any app's identity, so it
// travels only on the edge from the launcher to the broker.
const SecretEnv = "CREDZ_LAUNCH_SECRET"

// Broker names the app whose process answers for credentials when the launcher
// is not itself the broker. It is the app that owns the sealed secret store:
// only the process holding the store can read it, so which app brokers is a fact
// about the deployment's topology and not a setting.
//
// In the fused binary this is unused — there the launcher and the broker are the
// same process, and the secret never leaves it.
const Broker = "kms"

// RootEnv carries the KMS master key (the base64 32-byte KEK). It is a
// launcher↔broker fact, which is why it lives in this leaf and not only in
// credz: a launcher that spawns children through zip — which builds each child's
// environment as append(os.Environ(), Plugin.Env...) — would hand the root key
// to EVERY child through os.Environ() and re-open the very hole credz closes
// (any child resolves the Root posture and can open any store). So the light
// host scrubs it: os.Unsetenv(RootEnv) on itself before it spawns anything, then
// re-adds RootEnv=<key> to the broker child's Plugin.Env ALONE. The broker is
// then the only child that is Root; every other child comes up with a scoped
// token and no key, and must ask the broker. credz.RootEnv is this same name.
const RootEnv = "CLOUD_KMS_MASTER_KEY_REF"

// Secret mints a launcher's signing secret, once per launcher process. It is
// never persisted and never written anywhere but the broker child's environment:
// a secret that outlives the process that minted it is a secret that can sign a
// token for a process nobody launched.
func Secret() string {
	b := make([]byte, 32)
	// crypto/rand.Read fills b entirely or panics (Go 1.24+). There is no short
	// read to loop on and no error to swallow.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Env is the environment entry a launcher adds to ONE child's zip.Plugin.Env,
// naming the app that child was started to be.
//
// It returns the whole `NAME=value` entry rather than the value because that is
// what both spawn sites need — zip appends Plugin.Env to the child's
// environment — and returning half of it would put the same join in two places.
//
// Per-plugin is the whole point: decorating os.Environ() instead would hand
// every child the same token and re-open the hole with extra steps.
func Env(secret, app string) string {
	return TokenEnv + "=" + app + ":" + hex.EncodeToString(sum(secret, app))
}

// Open verifies a token and returns the app it names, or "" for anything that
// does not verify — no error, because there is exactly one thing a broker can do
// with an unverifiable token and a caller that could distinguish the reasons
// would be tempted to act on them.
//
// An empty secret verifies nothing. A launcher that failed to mint one, or a
// broker that was never given one, therefore grants nothing rather than
// accepting everything, which is the direction this has to fail in.
func Open(secret, tok string) string {
	app, mac, ok := strings.Cut(tok, ":")
	if !ok || secret == "" || app == "" {
		return ""
	}
	got, err := hex.DecodeString(mac)
	if err != nil {
		return ""
	}
	// Constant-time, and false for a length mismatch — so a truncated mac is a
	// refusal, not a shorter comparison.
	if !hmac.Equal(got, sum(secret, app)) {
		return ""
	}
	return app
}

// sum is the proof: hmac-sha256 over the app name under the launcher's secret.
// The name is the entire message, so a token is bound to one app and to one
// launcher, and to nothing else — no time, no pid, nothing that would make a
// valid token invalid for a reason a child cannot see.
func sum(secret, app string) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(app))
	return h.Sum(nil)
}
