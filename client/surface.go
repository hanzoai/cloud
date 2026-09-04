// Copyright © 2026 Hanzo AI. MIT License.

package client

import (
	"strings"
	"unicode"
)

// What the surface's MCP server is WILLING to say a tool is, and in what ORDER.
//
// [MCP.gather] asks every subsystem what it serves and returns the union. That
// is the right answer to "what exists" and the wrong answer to "what may an
// agent call", and until this file the MCP server had no second answer: on
// api.hanzo.ai it projected 1,323 tools with zero annotations, zero
// readOnlyHint, and no auth at the transport. Two independent facts made that
// concrete rather than theoretical:
//
//   - The list is sorted by name and clients TRUNCATE it. Slack keeps the first
//     128. Because zip's operation ids are PascalCase for the subsystems that
//     declare them and `<method>_<path>` for the rest, and 'C' < 'a' in ASCII,
//     the first 128 names were ALL o11y's declared ids — sorted(names)[127] ==
//     "GetUserPreference", exactly Slack's last saved tool. Zero product tools
//     (chat, deploy, run, git, search, code, model) were inside that window.
//   - Thirty-six of the tools inside it mint or disclose credentials.
//     CreateServiceAccountKey is, in its own description, "the one time the
//     secret is ever shown". CreateSessionByEmailPassword, CreateResetPasswordToken,
//     CreateUser, DeleteUser, CreateAuthDomain sat beside it.
//
// So the agent surface was the exact complement of the useful one. Two
// mechanisms fix that, and they are DIFFERENT mechanisms and stay separate:
//
//	[refuse] removes a tool from the projection entirely — for every client,
//	         from BOTH tools/list and tools/call, because gather is where the
//	         routing table is written and a name that is never written is never
//	         routable. This is a boundary, not a preference.
//
//	[rank]   orders what survives, so the flagship product tools occupy the
//	         head of the list a truncating client keeps. This is a preference,
//	         not a boundary: nothing is hidden, only moved.
//
// Both read the tool NAME and nothing else, because the name is all an MCP tool
// descriptor carries that a host can reason about — zip's mcpToolOf projects
// exactly {name, description, inputSchema} (zip@v1.25.1 mcp.go:448) and neither
// method nor path survives into it. The name is enough, because zip derives it
// from the route when nobody declared one: defaultOpID(method, path) is
// lower(method) + path with '/'→'_' and braces stripped (zip@v1.25.1
// openapi.go:274), so `POST /v1/chat/completions` IS `post_v1_chat_completions`.
// A declared id is a verb followed by its object — `CreateServiceAccountKey`.
// One tokenizer reads both.

// Refused is the _meta key under which tools/list reports what it withheld.
//
// It exists for the same reason [Unavailable] does, and it is the same defect
// if it is missing: a silently shortened list cannot be told apart from a surface
// that serves nothing. The MCP server already refuses to shorten quietly for an
// outage; refusing to shorten quietly for a POLICY is the same obligation. The
// count and the rule travel with the answer, so an operator who wonders where
// CreateServiceAccountKey went reads why rather than filing a bug against a
// subsystem that is serving it correctly.
const Refused = "hanzo.ai/refused"

// TheRule is the sentence [refuse] implements, carried on the wire under
// [Refused] so the answer explains itself.
const TheRule = "a tool is not projected when its name discloses a bearer secret at any verb, " +
	"or when a mutating verb acts on an identity or authority object"

// refuse reports whether the surface's MCP server will project a tool at all.
//
// The rule, in two clauses over the name's words:
//
//  1. DISCLOSURE. The name says it handles a bearer secret — a token, a
//     password, a credential, a private key. Refused at EVERY verb, because
//     reading `GET /v1/o11y/users/{id}/reset_password_tokens` hands the secret
//     over just as surely as the PUT that mints it. Verb-blindness is the whole
//     point of this clause.
//
//  2. AUTHORITY MUTATION. A mutating verb acts on an identity or authority
//     object — a user, a role, a policy, an invite, a key, a sign-in session, a
//     service account, an auth domain. Refused. The matching READ is not:
//     GetRole and GetUser survive, because knowing who holds a role is not the
//     same act as granting one, and an agent that cannot see the org cannot
//     reason about it.
//
// Neither clause is a list of ops. Both are lists of NOUNS and VERBS, so op
// 1,324 is classified the day it is written — which is the property a
// hand-maintained roster of 36 names cannot have. The words below are the whole
// policy; nothing else in this package decides.
func refuse(tool string) bool {
	w := words(tool)
	if len(w) == 0 {
		return true // a nameless tool is not routable and not projectable
	}
	if discloses(w) {
		return true
	}
	return mutates(w) && authority(w)
}

// discloses is clause 1: the name of a bearer secret, at any verb.
//
// Two of the words are NOT always secrets, and they are QUALIFIED rather than
// dropped, because dropping either one would open the exact hole this file
// closes:
//
//   - `token` is also a unit of text to an LLM and a unit of value on a chain.
//     A counting or identifying neighbour makes it one of those instead, which
//     is how `post_v1_messages_count_tokens` and `get_v1_validators_tokenId`
//     survive while `post_v1_iam_oauth_token` does not.
//   - `session` is a bearer object when NOTHING OWNS IT — when the word before
//     it is a verb, the version, or an auth marker (`getSession`,
//     `RotateSession`, `post_v1_ai_signin-sessions`). Owned by a resource, it is
//     that resource's unit of work and survives, which is what keeps the agent
//     loop — `post_v1_agents_sessions_by_id_message` and its siblings — on the
//     agent's own surface. Reading an identity session is refused as hard as
//     creating one, because the object read back IS the credential.
func discloses(w []string) bool {
	for i, t := range w {
		switch {
		case t == "token" || t == "tokens":
			if !measured(w, i) {
				return true
			}
		case t == "session" || t == "sessions":
			if unowned(w, i) {
				return true
			}
		case secretNoun[t]:
			return true
		}
	}
	return false
}

// measured reports whether `token` at index i is being counted or identified
// rather than presented — the nearest real neighbour on either side decides.
func measured(w []string, i int) bool {
	return tokenIsAQuantity[before(w, i)] || tokenIsAQuantity[after(w, i)] || tokenIsAnAsset[after(w, i)]
}

// unowned reports whether the session at index i belongs to nobody: the word
// before it is a verb, the API version, or an explicit auth marker. See
// [discloses].
func unowned(w []string, i int) bool {
	p := before(w, i)
	return p == "" || p == "v1" || verb[p] || authMarker[p]
}

// mutates reports whether the name's verb CHANGES something.
//
// It reads every word, not just the first, because the two naming conventions
// put the verb in different places: a declared id leads with it
// (`CreateServiceAccountKey`), a derived one leads with the HTTP method
// (`post_v1_iam_users`) — and for derived names the method IS the verb, which
// is why `post`/`put`/`patch`/`delete` are in the same set as `create`/`grant`.
func mutates(w []string) bool {
	for _, t := range w {
		if mutatingVerb[t] {
			return true
		}
	}
	return false
}

// authority is clause 2's object test: does this name act on identity?
//
// Three words need a neighbour before they count, and each for a reason that is
// about English rather than about security policy:
//
//   - `account` is a billing relationship far more often than a principal, so
//     only the pair `service account` counts. CreateServiceAccount does;
//     CreateAccount, which connects a cloud integration, does not.
//   - `domain` is a DNS name we SELL. Only `auth domain` counts, which is how
//     CreateAuthDomain is refused while the whole `/v1/domain` product is not.
//   - `key` is the hard one, and it is qualified by EXCLUSION rather than by
//     inclusion — a key is a credential unless its neighbour says it is an
//     entry in a store or a name in a schema. That direction is deliberate: an
//     inclusion list ("api key, ssh key, signing key, …") fails OPEN on the
//     credential nobody thought of, and this clause must fail closed. So
//     `delete_v1_pubsub_kv_bucket_key` and `patch_v1_todo_projects_key_issues_num`
//     survive on their neighbours, while `delete_v1_git_keys_id`,
//     `post_v1_agents_targets_id_key` and `delete_v1_keys` do not.
//
// Note what is NOT here. `grant` is a mutating VERB but not an authority noun,
// because in this surface a grant is nearly always money — adminGrantCredit,
// post_v1_research_grants. Nothing identity-shaped needs it: GrantRole and
// grantPermission are already refused on their objects, so the noun only ever
// bought false positives. `owner` was here, and it was wrong: zip renders a path
// param as `by_<name>`, so every `/v1/ai/{owner}/{name}` route in the surface —
// 45 of them, the whole ai CRUD surface — reads as an authority mutation on a
// word that is really a namespace. `admin` is absent for a different reason:
// it names an AUDIENCE, not an identity object, and sweeping it in would make
// this a general blast-radius policy rather than the identity boundary it is.
// Blast radius on infra and money is a real question and a DIFFERENT one; it
// belongs to whatever authenticates the transport, not to a name filter.
func authority(w []string) bool {
	for i, t := range w {
		switch {
		case authorityNoun[t]:
			return true
		case t == "account" || t == "accounts":
			if before(w, i) == "service" {
				return true
			}
		case t == "domain" || t == "domains":
			if before(w, i) == "auth" {
				return true
			}
		case t == "key" || t == "keys":
			if !keyOfAStore[before(w, i)] {
				return true
			}
		}
	}
	return false
}

// secretNoun names a thing whose VALUE is the credential. `token` and `session`
// are handled separately in [discloses] because they need a neighbour.
var secretNoun = set(
	"password", "passwords",
	"credential", "credentials",
	"secret", "secrets",
	"apikey", "apikeys",
	"jwt", "jwts",
	"otp", "totp",
	"passkey", "passkeys",
	"privatekey", "privatekeys",
	"keypair", "keypairs",
	"mnemonic", "mnemonics",
	"seedphrase", "seedphrases",
)

// tokenIsAQuantity are the neighbours that turn `token` into a unit rather than
// a credential — LLM accounting and chain identifiers.
var tokenIsAQuantity = set(
	"count", "counts", "counting",
	"usage", "used", "limit", "limits", "budget",
	"max", "min", "total", "per",
	"price", "pricing", "cost",
)

// tokenIsAnAsset are the neighbours that turn `token` into a coin whose field is
// being named — `tokenId`, `tokenSymbol`, `tokenSupply`, `tokenBalance`.
//
// They qualify only when they FOLLOW, and that asymmetry is the whole point.
// Read on either side, `id` matched the parent identifier of a REST subresource:
//
//	GET /v1/integration/connectors/{id}/token   →  get | connectors | by | id | token
//
// The id there is the CONNECTOR's. The token is exactly what the word says, and
// the gate whose one job is to withhold bearer secrets projected it to every
// model as `get_connector_token`. A quantity reads either way because English
// puts it on both sides — "count tokens", "token count" — but an asset's field
// is a suffix, so requiring the suffix costs nothing and closes the path shape.
var tokenIsAnAsset = set("id", "ids", "symbol", "supply", "balance")

// keyOfAStore are the neighbours that turn `key` into an entry in a store or a
// name in a schema rather than a credential. See [authority] for why this list
// runs in the exclusion direction.
var keyOfAStore = set(
	"kv", "bucket", "buckets", "namespace", "namespaces",
	"value", "values", "map", "cache", "store", "listing", "listings",
	"project", "projects", "def", "defs", "definition", "definitions",
	"attribute", "attributes", "field", "fields", "label", "labels",
	"tag", "tags", "index", "indexes", "column", "columns", "dimension",
	"partition", "shard", "prefix", "sort", "group", "primary", "foreign",
	"idempotency", "row", "rows", "entry", "entries", "item", "items", "object",
)

// mutatingVerb is every word that means "this CHANGES something", including the
// four HTTP methods that mean it in a derived name. See [mutates].
var mutatingVerb = set(
	// HTTP methods, as they appear in a derived operation id.
	"post", "put", "patch", "delete",
	// Declared verbs.
	"create", "update", "upsert", "set", "add", "insert", "write",
	"remove", "destroy", "purge", "drop", "reset", "rotate",
	"grant", "revoke", "assign", "unassign", "issue", "mint", "sign",
	"invite", "register", "enroll", "provision", "deprovision",
	"enable", "disable", "activate", "deactivate", "suspend",
	"promote", "demote", "elevate", "impersonate", "assume",
	"attach", "detach", "bind", "unbind", "link", "unlink",
	"login", "logout", "signin", "signout", "signup", "authenticate", "forgot",
)

// verb is every word that leads a name, mutating or not. It exists only for
// [unowned]: a session preceded by a VERB is preceded by no resource, and
// `getSession` hands back the same credential `createSession` mints.
var verb = union(mutatingVerb, set(
	"get", "head", "options",
	"list", "read", "fetch", "show", "describe", "find", "search", "query",
	"count", "stream", "watch", "export", "verify", "check", "validate",
))

// authorityNoun names a thing that CONFERS or CARRIES authority. See [authority].
var authorityNoun = set(
	"user", "users",
	"role", "roles",
	"permission", "permissions",
	"policy", "policies",
	"member", "members", "membership", "memberships",
	"principal", "principals",
	"identity", "identities",
	"invite", "invites", "invitation", "invitations",
	"auth", "authz", "authn", "oauth", "oidc", "saml", "sso", "scim", "mfa", "2fa",
	"webauthn", "superuser", "superusers", "impersonation",
	"acl", "acls", "rbac", "iam", "kms",
)

// authMarker marks a session as an IDENTITY session. See [unowned].
var authMarker = set("signin", "signon", "login", "auth", "oauth", "sso", "user", "email", "cookie", "bearer")

// filler are the positional words a derived operation id carries that name
// nothing: zip renders a path param as `by_<name>`, so the word before `key` in
// `delete_v1_commerce_store_by_storeid_listing_by_key` is grammar, not context. Every
// neighbour test skips them, in ONE place — see [before] and [after].
var filler = set("by", "the", "a", "an", "of", "for", "my", "me", "all", "and")

// productStems is the CURATED PRODUCT SURFACE, in the order it is offered.
//
// This is the answer to the truncation half of the problem, and the mechanism is
// stated here so nobody has to infer it: a client that keeps only the first N
// tools keeps the ones in this slice, in this order, because [rank] buckets by
// index here and the sort is (bucket, name). Nothing is hidden — every surviving
// tool is still in the list, the unlisted ones simply follow.
//
// The entries are ROUTE STEMS, not tool names, matched against a derived
// operation id's path half on a '_' boundary. That is the whole reason the list
// is short enough to read: `git` covers all 32 git ops forever, including the
// ones written next year, and adding a route under an existing product prefix
// promotes it with no edit here. A subsystem that declares its own PascalCase
// ids carries no path in its names, so it can never match a stem — which is
// correct, because those subsystems (o11y, iam) are the console's surface and
// not the agent's.
//
// The head of the list is deliberately the inference surface: a client with a
// tiny window should get chat before it gets anything else.
var productStems = []string{
	"chat",        // POST /v1/chat/completions — the flagship
	"completions", //
	"responses",   //
	"messages",    //
	"embeddings",  //
	"rerank",      //
	"models",      // what can it call
	"agent",       // the agent loop: conversations, presets, sessions, runs,
	               // targets, and the run in a sandbox
	"code",        // code intelligence: ask, context, index, search
	"lsp",         // …and the live language server beside it
	"search",      //
	"git",         // source control
	"deploy",      // ship it
	"exec",        // run it
	"project",    // …and the things shipped
	"websearch",   //
}

// httpMethod is the leading word of a DERIVED operation id — the half [rank]
// strips before matching a stem.
var httpMethod = set("get", "post", "put", "patch", "delete", "head", "options")

// rank is the sort bucket for a tool: its index in [productStems], or the tail.
//
// A name is ranked by its PATH, which a derived id carries verbatim after the
// method word. `post_chat_completions` → "chat_completions" → stem "chat" matches
// on the '_' boundary → bucket 0. A name with no method word (`GetUserPreference`)
// has no path to match and takes the tail bucket, as does any route under no
// product prefix.
//
// A LEADING VERSION IS STEPPED OVER, the way [route] steps over it. zip stopped
// emitting one — it names nothing every address does not already carry — and the
// stems here spelled it, so for a while every product tool matched nothing and
// fell to the tail: the console led the list, and a client that keeps only the
// first few tools kept the console instead of chat. A subsystem pinned at an
// older zip still publishes one, so `post_v1_chat_completions` and
// `post_chat_completions` are one operation named twice; stepping over the
// version ranks them alike and keeps the stems a list of products instead of a
// list of spellings.
func rank(tool string) int {
	head, tail, found := strings.Cut(tool, "_")
	if !found || !httpMethod[strings.ToLower(head)] {
		return len(productStems)
	}
	tail = strings.ToLower(tail)
	if v, rest, ok := strings.Cut(tail, "_"); ok && isVersion(v) {
		tail = rest
	}
	for i, stem := range productStems {
		if tail == stem || strings.HasPrefix(tail, stem+"_") {
			return i
		}
	}
	return len(productStems)
}

// words splits a tool name into lowercase words under BOTH naming conventions
// at once: separators (`_`, `-`, `.`, `/`) and camel/Pascal case boundaries,
// with acronym runs kept whole. `CreateLLMScore` → [create llm score];
// `delete_v1_ai_signin-sessions_by_owner_by_name` → [delete v1 ai signin
// sessions by owner by name]; `GetRolesByUserID` → [get roles by user id].
func words(name string) []string {
	rs := []rune(name)
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for i, r := range rs {
		switch {
		case r == '_' || r == '-' || r == '.' || r == '/' || r == ' ' || r == ':':
			flush()
		case unicode.IsUpper(r):
			// lower→UPPER starts a word; UPPER→UPPER→lower ends an acronym.
			if i > 0 && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1])) {
				flush()
			} else if i > 0 && unicode.IsUpper(rs[i-1]) && i+1 < len(rs) && unicode.IsLower(rs[i+1]) {
				flush()
			}
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

// before and after are the nearest MEANINGFUL neighbours of w[i] — the ones a
// qualification test asks about — with [filler] skipped. Empty when there is
// none, which every caller reads as "no context", never as a match.
func before(w []string, i int) string {
	for j := i - 1; j >= 0; j-- {
		if !filler[w[j]] {
			return w[j]
		}
	}
	return ""
}

func after(w []string, i int) string {
	for j := i + 1; j < len(w); j++ {
		if !filler[w[j]] {
			return w[j]
		}
	}
	return ""
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

func union(ms ...map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, m := range ms {
		for k := range m {
			out[k] = true
		}
	}
	return out
}


// Withheld answers whether an operation is kept off the agent surface, applying
// the same rule ([TheRule]) the endpoint applies when it assembles a tool list.
//
// It is exported for the generator that projects this surface for a CLIENT
// (plugin/gen-mcp-catalog), which must offer exactly what the endpoint serves. A
// client deriving its set from the raw catalog offers what this withholds, so
// the policy would hold on one transport and not the other — and the half left
// unenforced is the one where an agent is already holding the tool.
func Withheld(op string) bool { return refuse(op) }
