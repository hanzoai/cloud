// Copyright © 2026 Hanzo AI. MIT License.

package fleet

import (
	"strings"
	"testing"
)

// The rule, against the names it was written for.
//
// Every name below is a REAL operation id from this fleet's own composed
// document (openapi.yaml) or from plugin/o11y/openapi.json, with its route
// beside it, because a policy tested on names someone invented for the test is a
// policy tested on nothing. The o11y ones are specifically the ops MEASURED
// inside Slack's 128-tool window on api.hanzo.ai.

// refusals is the dangerous half: what must never reach an agent.
var refusals = []struct{ name, why string }{
	// --- clause 1, disclosure: the secret is the payload, at any verb. ---
	{"CreateServiceAccountKey", "POST /v1/o11y/service_accounts/{id}/keys — mints an API key; its own description says this is the one time the secret is shown"},
	{"CreateSessionByEmailPassword", "POST /v1/o11y/sessions/email_password — logs in as a user"},
	{"CreateResetPasswordToken", "PUT /v1/o11y/users/{id}/reset_password_tokens — mints a password reset"},
	{"GetResetPasswordToken", "GET of the same token: READING it is disclosing it, which is why clause 1 is verb-blind"},
	{"GetResetPasswordTokenDeprecated", "the deprecated spelling of the same disclosure"},
	{"VerifyResetPasswordToken", "verify still carries the token"},
	{"ForgotPassword", "POST /v1/o11y/factor_password/forgot"},
	{"UpdateMyPassword", "PUT /v1/o11y/users/me/factor_password"},
	{"GetConnectionCredentials", "GET /v1/o11y/cloud_integrations/{p}/credentials — a GET that returns cloud creds"},
	{"getToken", "POST /v1/iam/tokens/get"},
	{"addToken", "POST /v1/iam/tokens"},
	{"listTokens", "GET /v1/iam/tokens"},
	{"getWebauthnCredential", "POST /v1/iam/webauthn-credentials/get"},
	{"post_iam_oauth_token", "the OAuth token endpoint"},
	{"post_iam_registry_token", "a registry pull credential"},
	{"post_iam_mint-user-keys", "mints keys for a user"},
	{"get_kms_secrets", "the secret store, read"},
	{"delete_kms_secrets_by_wildcard1", "the secret store, written"},
	{"get_functions_secrets", "a function's environment secrets"},
	// Sessions that nothing owns are identity sessions — read or write.
	{"getSession", "POST /v1/iam/sessions/get — the session object IS the credential"},
	{"listSessions", "GET /v1/iam/sessions"},
	{"createSession", "POST /v1/iam/sessions/create"},
	{"DeleteSession", "DELETE /v1/o11y/sessions"},
	{"RotateSession", "POST /v1/o11y/sessions/rotate"},
	{"post_v1_ai_signin-sessions", "explicitly a sign-in session"},
	{"get_ai_signin-sessions", "…and reading one hands back what it holds"},

	// --- clause 2, authority mutation: the verb changes who may do what. ---
	{"CreateUser", "POST /v1/o11y/users"},
	{"DeleteUser", "DELETE /v1/o11y/users/{id}"},
	{"UpdateUser", "PUT /v1/o11y/users/{id}"},
	{"CreateAuthDomain", "POST /v1/o11y/domains — an auth domain, not a DNS one"},
	{"DeleteAuthDomain", "DELETE /v1/o11y/domains/{id}"},
	{"CreateRole", "POST /v1/o11y/roles"},
	{"DeleteRole", "DELETE /v1/o11y/roles/{id}"},
	{"SetRoleByUserID", "POST /v1/o11y/users/{id}/roles — grants a role"},
	{"RemoveUserRoleByUserIDAndRoleID", "DELETE of the same"},
	{"CreateServiceAccount", "POST /v1/o11y/service_accounts — a new principal"},
	{"CreateServiceAccountRole", "…and its authority"},
	{"RevokeServiceAccountKey", "DELETE /v1/o11y/service_accounts/{id}/keys/{fid}"},
	{"CreateInvite", "POST /v1/o11y/invite — adds a person to the org"},
	{"CreateBulkInvite", "POST /v1/o11y/invite/bulk"},
	{"CreateIngestionKey", "POST /v1/o11y/gateway/ingestion_keys"},
	{"CreateRoutePolicy", "POST /v1/o11y/route_policies"},
	{"post_iam_users", "POST /v1/iam/users"},
	{"post_iam_scim_v2_users", "SCIM user provisioning"},
	{"delete_framework_roles_user_role", "DELETE /v1/framework/roles/{user}/{role}"},
	{"post_git_keys", "POST /v1/git/keys — an SSH key is a credential even on the git surface"},
	{"delete_git_keys_id", "and removing one is still key management"},
	{"post_agents_targets_id_key", "POST /v1/agents/targets/{id}/key — enrols a machine agent"},
	{"delete_account_keys", "DELETE /v1/account/keys — the head resource, so no store owns it"},
}

// survivors is the useful half: what an agent is FOR. Several of these are here
// because an earlier draft of the rule refused them — the note says which word
// did it, so re-adding that word turns this red.
var survivors = []struct{ name, why string }{
	// The inference surface, whole.
	{"post_v1_chat_completions", "POST /v1/chat/completions — the flagship"},
	{"post_v1_responses", "POST /v1/responses"},
	{"post_v1_embeddings", "POST /v1/embeddings"},
	{"post_v1_rerank", "POST /v1/rerank"},
	{"get_models", "GET /v1/models"},
	{"post_v1_messages_count_tokens", "POST /v1/messages/count_tokens — `token` is a UNIT here; the counting neighbour says so"},
	{"get_validators_tokenId", "a chain token id, not a bearer token"},

	// The agent loop. Every one of these was refused while `session` was an
	// unqualified authority noun.
	{"post_agents_sessions", "POST /v1/agents/sessions — an agent session is a unit of WORK"},
	{"post_agents_sessions_by_id_message", "the turn itself"},
	{"post_agents_sessions_by_id_stop", "…and stopping it"},
	{"patch_agents_sessions_id", "…and steering it"},
	{"get_agents_sessions_stream", "…and watching it"},
	{"post_agents_by_ref_run", "POST /v1/agents/{ref}/run"},
	{"post_agents_targets_id_claim", "claiming a target is not minting its key"},

	// Code, search, git, deploy, exec.
	{"post_code_ask", "POST /v1/code/ask"},
	{"post_code_index", "POST /v1/code/index"},
	{"get_code_search", "GET /v1/code/search"},
	{"post_search", "POST /v1/search"},
	{"post_git_repos", "POST /v1/git/repos — repos are not credentials"},
	{"post_git_repos_name_push", "POST /v1/git/repos/{name}/push"},
	{"post_deploy_applications_by_name_sync", "POST /v1/deploy/applications/{name}/sync"},
	{"post_exec", "POST /v1/exec"},

	// The fleet's path to the live internet. These names have to be checked
	// against the rule rather than assumed past it: the rule reads the NAME, so
	// whether a capability projects is a property of what its operation is
	// CALLED. Both are mutating verbs over nouns that confer no authority.
	{"search_web", "POST /v1/websearch — searching the web grants nothing"},
	{"post_crawl", "POST /v1/crawl — reading a page grants nothing"},

	// Reads of the identity surface survive: knowing who holds a role is not
	// granting one, and an agent that cannot see the org cannot reason about it.
	{"GetUser", "GET /v1/o11y/users/{id}"},
	{"GetRole", "GET /v1/o11y/roles/{id}"},
	{"GetRolesByUserID", "GET /v1/o11y/users/{id}/roles"},
	{"GetUserPreference", "GET /v1/o11y/user/preferences/{name} — Slack's 128th tool, and harmless"},
	{"GetMyUser", "GET /v1/o11y/users/me"},

	// Words that LOOK dangerous and are not. Each names a store entry or a
	// schema name, not a credential — see keyOfAStore.
	{"get_o11y_deployments_attribute_keys", "metric label names"},
	{"delete_pubsub_kv_bucket_key", "DELETE /v1/pubsub/kv/{bucket}/{key}"},
	{"delete_flags_defs_key", "a feature-flag key"},
	{"delete_todo_projects_key", "a todo project key, e.g. CLOUD-1"},
	{"patch_todo_projects_key_issues_num", "…and an issue under it"},
	{"delete_commerce_store_by_storeid_listing_by_key", "the `by_` filler must not become the key’s context"},
	{"delete_cloudflare_kv_namespaces_namespace_values_key", "a KV value"},

	// The whole ai CRUD surface, which zip names `by_owner_by_name`. All 45 of
	// these were refused while `owner` was an authority noun.
	{"patch_v1_ai_chats_by_owner_by_name", "PATCH /v1/ai/chats/{owner}/{name}"},
	{"delete_v1_ai_workflows_by_owner_by_name", "DELETE /v1/ai/workflows/{owner}/{name}"},
	{"post_v1_ai_deployments_by_owner_by_name_deploy", "POST …/deploy"},

	// Money is a different boundary and this rule does not claim it. Named here
	// so the scope is a decision on the record rather than an oversight.
	{"post_research_grants", "a research grant is money, not authority"},
}

func TestRefuse_DangerousOpsAreNotProjected(t *testing.T) {
	for _, c := range refusals {
		if !refuse(c.name) {
			t.Errorf("refuse(%q) = false — this op WOULD reach an agent.\n    %s\n    words: %v",
				c.name, c.why, words(c.name))
		}
	}
}

func TestRefuse_ProductOpsSurvive(t *testing.T) {
	for _, c := range survivors {
		if refuse(c.name) {
			t.Errorf("refuse(%q) = true — the rule ate a tool an agent needs.\n    %s\n    words: %v",
				c.name, c.why, words(c.name))
		}
	}
}

// TestRefuse_TheTwoSetsAgreeWithTheRuleStatement checks the CLAUSE that fired,
// not just the verdict — so a name refused for the wrong reason (a bug that
// would pass the two tests above) is still caught.
func TestRefuse_TheTwoSetsAgreeWithTheRuleStatement(t *testing.T) {
	for _, c := range refusals {
		w := words(c.name)
		if !discloses(w) && !(mutates(w) && authority(w)) {
			t.Errorf("%q is refused by neither stated clause, so refuse() and %s disagree", c.name, "TheRule")
		}
	}
	// A read of an authority object must fail clause 2 on the VERB, not sneak
	// past on the object — otherwise GetRole surviving would be an accident.
	for _, name := range []string{"GetRole", "GetUser", "GetRolesByUserID"} {
		w := words(name)
		if !authority(w) {
			t.Errorf("%q does not read as an authority object; the survival of its READ is then untested", name)
		}
		if mutates(w) {
			t.Errorf("%q reads as a mutation; it is a GET", name)
		}
	}
}

// TestRefuse_IsVerbBlindAboutSecrets is clause 1's whole point, isolated: the
// same object, four verbs, four refusals. GetResetPasswordToken is the op that
// proves a mutation-only rule would have been wrong.
func TestRefuse_IsVerbBlindAboutSecrets(t *testing.T) {
	for _, n := range []string{"GetResetPasswordToken", "CreateResetPasswordToken", "VerifyResetPasswordToken", "listTokens"} {
		if !refuse(n) {
			t.Errorf("refuse(%q) = false; clause 1 must not depend on the verb", n)
		}
	}
}

// TestRefuse_ClassifiesNamesItHasNeverSeen is the property a hand-typed roster
// of 36 op names cannot have, and the reason the rule is made of nouns.
func TestRefuse_ClassifiesNamesItHasNeverSeen(t *testing.T) {
	// Shapes that do not exist in this fleet today. If someone adds them
	// tomorrow, they are already classified.
	for _, n := range []string{
		"CreateOrganizationApiKey", "post_iam_users_by_id_impersonate",
		"MintDelegatedCredential", "put_billing_saml_metadata",
		"RotateSigningKey", "post_notify_channels_by_id_oauth_authorize",
	} {
		if !refuse(n) {
			t.Errorf("refuse(%q) = false — a NEW dangerous op slipped through; words: %v", n, words(n))
		}
	}
	for _, n := range []string{
		"post_chat_conversations", "get_zen_models", "post_code_review",
		"get_agents_sessions_by_id_diff", "post_search_reindex",
	} {
		if refuse(n) {
			t.Errorf("refuse(%q) = true — a NEW product op was eaten; words: %v", n, words(n))
		}
	}
}

// TestRank_PutsTheProductSurfaceInFrontOfTheConsole is mechanism (b).
//
// The failure it encodes is the measured one: Slack keeps the first 128 tools,
// alphabetical order handed it 128 o11y console ops and zero product tools, and
// 'C' < 'a' means no amount of renaming on the product side would have fixed it.
func TestRank_PutsTheProductSurfaceInFrontOfTheConsole(t *testing.T) {
	chat := rank("post_v1_chat_completions")
	if chat != 0 {
		t.Errorf("rank(post_v1_chat_completions) = %d, want 0 — chat leads the surface", chat)
	}
	console := rank("AgentCheckIn") // sorts FIRST alphabetically, fleet-wide
	if console != len(productStems) {
		t.Errorf("rank(AgentCheckIn) = %d — a declared PascalCase id carries no path and cannot match a stem", console)
	}
	if chat >= console {
		t.Fatal("the flagship tool does not outrank the tool that used to be first; the truncation window is unchanged")
	}
	// Stem matching is on a '_' boundary, so a longer name under the prefix is
	// promoted and an unrelated one that merely starts with the same letters is not.
	if got := rank("get_agents_sessions_stream"); got == len(productStems) {
		t.Error("a route UNDER a product stem must inherit its rank")
	}
	if got := rank("get_agentsomething"); got != len(productStems) {
		t.Errorf("rank(get_agentsomething) = %d — `v1_agent` must not match across a word boundary", got)
	}
	if got := rank("CreateUserFromGit"); got != len(productStems) {
		t.Errorf("rank(%q) = %d — a name with no HTTP-method word has no path to rank by", "CreateUserFromGit", got)
	}
}

// TestRank_TheProductSurfaceFitsATruncatingClient.
//
// Ordering only helps if the promoted set is SMALLER than the window. Measured
// against the fleet's own document, the stems through `v1_exec` promote 126 ops
// — so a client that keeps 128 keeps chat, models, the agent loop, code, search,
// git, deploy and exec. `projects` and `websearch` are last precisely
// because they are the two that spill.
func TestRank_TheProductSurfaceFitsATruncatingClient(t *testing.T) {
	const window = 128
	head := 0
	for i, stem := range productStems {
		if stem == "projects" {
			head = i
		}
	}
	if head == 0 {
		t.Fatal("projects left the surface; this test's premise is stale")
	}
	if head >= len(productStems) {
		t.Fatal("projects is last; nothing is being kept inside the window")
	}
	// The claim is about counts measured elsewhere (see the doc comment); what
	// is checkable HERE is that the spill-over stems really are at the end.
	for i := head; i < len(productStems); i++ {
		if rank("post_"+productStems[i]) < head {
			t.Errorf("%q ranks inside the head of the surface", productStems[i])
		}
	}
	if head > window {
		t.Errorf("the surface has %d stems before the spill, which cannot fit a %d-tool window", head, window)
	}
}

func TestWords_ReadsBothNamingConventions(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"CreateServiceAccountKey", []string{"create", "service", "account", "key"}},
		{"CreateLLMScore", []string{"create", "llm", "score"}},
		{"GetRolesByUserID", []string{"get", "roles", "by", "user", "id"}},
		{"delete_v1_ai_signin-sessions_by_owner_by_name",
			[]string{"delete", "v1", "ai", "signin", "sessions", "by", "owner", "by", "name"}},
		{"post_git_by_org_by_repo_git-upload-pack",
			[]string{"post", "git", "by", "org", "by", "repo", "git", "upload", "pack"}},
	} {
		got := words(c.in)
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("words(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestRefuse_ANamelessToolIsNotProjectable: the door must not carry a descriptor
// it cannot route or reason about.
func TestRefuse_ANamelessToolIsNotProjectable(t *testing.T) {
	for _, n := range []string{"", "___", "-"} {
		if !refuse(n) {
			t.Errorf("refuse(%q) = false", n)
		}
	}
}

// TestRank_AFoldedAddressKeepsItsBucket pins the two ops whose address moved.
//
// The coding run ranked in the TAIL once, for a reason worth keeping written
// down: `productStems` carried "code" — code intelligence at /v1/code, which is
// ask/context/index — and the run answered at /v1/coding, a different product
// that was simply never listed. "coding" does not match the stem "code", so
// rank() sent it to the tail.
//
// The tail is not merely last. subsystemTool (grouped.go) skips prose for exactly
// that bucket, so the op reached the model as a bare name with no sentence, and
// any client that truncates its tool list drops the tail first. One missing stem,
// two symptoms: unranked and undescribed.
//
// Both addresses have since folded, and a fold is where that defect recurs: rank
// reads the PATH, so an op that moves takes whatever bucket its new first segment
// names. The run is at /v1/agents/coding and ranks with agents; lsp left
// /v1/code/lsp, where it had been ranking on code's stem, and needed one of its
// own or it would have fallen exactly as the run once did.
func TestRank_AFoldedAddressKeepsItsBucket(t *testing.T) {
	tail := len(productStems)
	for _, name := range []string{"post_agents_coding", "post_lsp_hover", "post_lsp_locate"} {
		if got := rank(name); got == tail {
			t.Errorf("rank(%q) = %d, the tail bucket — the op sorts last and loses its prose", name, got)
		}
	}
	// Neither is a spelling of code intelligence: folding one into the other would
	// let a stem match a name it does not name.
	if rank("post_agents_coding") == rank("post_code_ask") {
		t.Error("the coding run and code intelligence share a bucket — a run is not a search")
	}
	if rank("post_lsp_hover") == rank("post_code_ask") {
		t.Error("lsp and code intelligence share a bucket — they are two reads, at two roots")
	}
}

// TestAParentIdDoesNotQualifyASecret is the hole this pair of sets was opened by.
//
// `id` marks a token as NAMED rather than presented — `tokenId` on a chain is an
// asset's number, not a bearer secret. But the neighbour test read either side,
// and in a REST path the id before a subresource names the PARENT:
//
//	GET /v1/integrations/connectors/{id}/token   →  get | connectors | by | id | token
//
// The id there is the connector's. The token is exactly what it says, and the
// door projected it to every model as `get_connector_token` — a live OAuth
// bearer for a customer's connector, one tools/call away, offered by the gate
// whose whole job is to withhold it. Measured against the deployed fleet, it was
// one of two ops carrying a secret noun that survived; the other is the counted
// one this asserts still survives.
//
// So an asset's field qualifies only when it FOLLOWS. A quantity still reads
// either way, because English puts it on both sides — "count tokens" and "token
// count" are the same claim.
func TestAParentIdDoesNotQualifyASecret(t *testing.T) {
	for name, want := range map[string]bool{
		"get_connectors_by_id_token": true,  // the parent's id; the token is the object
		"post_messages_count_tokens": false, // counted, not presented
		"get_token_count":            false, // the same claim, the other way round
		"get_validators_by_token_id": false, // an asset's number on a chain
		"get_token_symbol":           false,
		"post_iam_oauth_token":       true, // the thing this file exists for
	} {
		if got := refuse(name); got != want {
			verb := map[bool]string{true: "must be refused", false: "must be projected"}[want]
			t.Errorf("%s %s, but refuse() said %v — words=%v", name, verb, got, words(name))
		}
	}
}

// TestNoSecretNounSurvivesTheRealFleet judges the fleet that exists rather than
// names invented here, so an op written tomorrow is measured by the same rule.
// The one admitted exception is named, not counted: a rule that allowed "some"
// survivors would pass while the wrong one survived.
func TestNoSecretNounSurvivesTheRealFleet(t *testing.T) {
	counted := map[string]bool{"post_messages_count_tokens": true}
	for _, op := range Corpus(t) {
		if refuse(op.ID) || counted[op.ID] {
			continue
		}
		for _, w := range words(op.ID) {
			if w == "token" || w == "tokens" || secretNoun[w] {
				t.Errorf("%s/%s carries %q and is projected to every model", op.App, op.ID, w)
			}
		}
	}
}
