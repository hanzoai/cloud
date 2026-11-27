// Package reward mounts the rewards rules engine (github.com/hanzoai/reward) at
// /v1/reward, with the admin queue at /v1/admin/reward, and feeds it what really
// happened.
//
// The module owns every rule of the program: rules, claims, the append-only points
// ledger, levels, badges, budgets and caps, and the routes that serve them. This
// package hands it the two things only cloud has — the store it keeps rows in
// (hanzo-sql in production) and the happenings — and nothing of its own to decide.
// It awards nothing: it states events, the engine makes a claim for each rule that
// reads one, and every rule is manual until staff switch it, so a person at
// admin.hanzo.ai verifies each claim against the evidence it carries.
//
// THE FEEDERS state events in process, idempotent by the event's own id, so a
// feeder that runs twice or on two replicas claims nothing twice:
//
//	usage.tokens   token milestones, one event for each threshold crossed (usage.go)
//	model.used     the first successful task on a model (usage.go)
//	referral.*     activation and retention of a referred org (referral.go)
//	github.*       the provider's verified GitHub deliveries; stars, follows and
//	               forks the member asks to have checked; the README badge (github.go)
//	social.post.*  a post proven public, the member's, and carrying their link,
//	               held a week and looked at every day of it (post.go)
//
// There is no service token and no auth of its own here: IAM owns identity, the
// module reads the headers the edge writes, and the platform principal that states
// events is the process itself.
package reward
