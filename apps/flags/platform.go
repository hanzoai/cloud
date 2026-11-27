package flags

import (
	"strconv"

	"github.com/hanzoai/cloud/client"
)

// The canonical PLATFORM launch switches — the runtime knobs the SuperAdmin flips from
// admin.hanzo.ai. Each is an Insights feature flag (the engine); this table names it,
// categorizes it, and pins a hardcoded Default. RUNTIME flags resolve from /v1/flag
// (the DB engine) → Default, with NO env gate — /v1/flag is the single source of truth,
// flipped live, no redeploy. Only BOOT-TIME, ReadOnly rows (subsystem activation applied
// via CLOUD_ENABLE/CLOUD_*; network-id display) pin an Env, because that IS their boot
// mechanism, not a redundant runtime override. Subsystems may Register MORE at init —
// this seed is the launch set, not a closed list.
func init() {
	for _, d := range []Def{
		// ── Launch / Waitlist (runtime flags: /v1/flag is the single source of truth; DB → Default, no env gate) ──
		{Key: "waitlist_open", Category: "Launch", Label: "Waitlist intake open", Desc: "Accept new waitlist signups. Off closes intake platform-wide.", Type: TypeBool, Default: "true"},
		{Key: "waitlist_access_capacity", Category: "Launch", Label: "Access capacity", Desc: "How many waitlisted users are granted product access (Insights flag payload = the number).", Type: TypeInt, Default: "0"},
		{Key: "waitlist_points_referral", Category: "Launch", Label: "Points · referral", Desc: "Queue-position points awarded per referral.", Type: TypeInt, Default: "0"},
		{Key: "waitlist_points_social", Category: "Launch", Label: "Points · social", Desc: "Queue-position points awarded for a social share.", Type: TypeInt, Default: "0"},
		{Key: "waitlist_points_hanzod", Category: "Launch", Label: "Points · run hanzod", Desc: "Queue-position points awarded for running hanzod.", Type: TypeInt, Default: "0"},
		{Key: "waitlist_turnstile", Category: "Launch", Label: "Turnstile challenge", Desc: "Require the Cloudflare Turnstile challenge on waitlist signup.", Type: TypeBool, Default: "false"},
		{Key: "waitlist_signup_rate_limit", Category: "Launch", Label: "Signup rate limit", Desc: "Max waitlist signups per source per minute (0 = unlimited).", Type: TypeInt, Default: "0"},

		// ── Access (decided per caller: its validated org and user; access.go) ──
		{Key: Beta, Category: "Access", Label: "Beta", Desc: "Who sees what is not yet generally available: beta products in the console and beta catalog items. On for the callers its rules name — an org (\"org\") or a person (\"user\", their IAM id) — and for the share of orgs its rollout takes (a person, in a brand's shared sign-up org). On for staff (SuperAdmins) whatever is stored, and, with an \"optin\" rule, for everyone who opted in to public betas and whose org did not block it. Off for everyone else, and with nothing stored. Only a SuperAdmin sets it.", Type: TypeBool, Default: "false", Principal: true},
		{Key: client.NetTerms, Category: "Access", Label: "Net terms", Desc: "Organizations approved to start a plan they ordered by invoice before they pay it, by name, comma-separated: the plan opens with the order and the invoice stays open until it is paid or voided. Empty approves none.", Type: TypeString, Default: ""},
		{Key: client.OnAccount, Category: "Access", Label: "On account", Desc: "Organizations billed in arrears and settled out of band, by name, comma-separated: no gate refuses their calls for money, while their usage is metered, limited and logged as anyone's and accrues on the org's own account as what it owes, until a recorded wire pays it down. A member's own wallet in the sign-up org is not on account. Empty puts none on account.", Type: TypeString, Default: ""},
		{Key: client.OnAccountAlert, Category: "Access", Label: "On account alert", Desc: "Tell the SuperAdmins when an organization on account owes this many US cents or more: an audit row and a pulse on the live feed, once a UTC day per organization while it does. 0 tells nobody.", Type: TypeInt, Default: "0"},

		// ── Platform (the build door; apps/platform reads them over the plane) ──
		{Key: client.PlatformBuilds, Category: "Platform", Label: "Builds at once, whole cluster", Desc: "How many builds run at once across every org. A build past it waits its turn and starts as one finishes, orgs taking turns; none is refused.", Type: TypeInt, Default: strconv.Itoa(client.PlatformBuildsSeed)},
		{Key: client.PlatformBuildsPerOrg, Category: "Platform", Label: "Builds at once, per org", Desc: "How many builds one org runs at once. A build past it waits its turn. An org's line is held to ten times this.", Type: TypeInt, Default: strconv.Itoa(client.PlatformBuildsPerOrgSeed)},

		// ── Signup (runtime flag: /v1/flag only) ──
		{Key: "public_signup", Category: "Signup", Label: "Public open signup", Desc: "Allow anyone to create an account (off = invite / waitlist only).", Type: TypeBool, Default: "false"},

		// ── Subsystem activation (boot-time; applying a flip needs an operator reconcile) ──
		{Key: "subsystem_iam_active", Category: "Subsystems", Label: "IAM (canary auth cutover)", Desc: "Serve identity from the embedded clean-room iam-v2 (zip-native, beego-free; the legacy iam-v1 embed is retired). CANARY-GATED staged auth cutover; applied at boot via CLOUD_ENABLE. Gate before flipping: the IAM cutover parity suite (universe e2e/50-iam-cutover-parity) must be green.", Type: TypeBool, Default: "false", ReadOnly: true},
		{Key: "subsystem_ingress_active", Category: "Subsystems", Label: "Ingress edge", Desc: "Serve the embedded ingress edge (routes/TLS/ACME). Applied at boot via CLOUD_ENABLE.", Type: TypeBool, Default: "false", ReadOnly: true},

		// ── Network (canonical Lux primary network ids — read-only display + override knob) ──
		{Key: "network_id_mainnet", Category: "Network", Label: "Network ID · mainnet", Desc: "Lux primary network id for mainnet (convention-fixed = 1).", Type: TypeInt, Env: "LUX_NETWORK_ID_MAINNET", Default: "1", ReadOnly: true},
		{Key: "network_id_testnet", Category: "Network", Label: "Network ID · testnet", Desc: "Lux primary network id for testnet (= 2).", Type: TypeInt, Env: "LUX_NETWORK_ID_TESTNET", Default: "2", ReadOnly: true},
		{Key: "network_id_local", Category: "Network", Label: "Network ID · local", Desc: "Lux primary network id for local (= 3).", Type: TypeInt, Env: "LUX_NETWORK_ID_LOCAL", Default: "3", ReadOnly: true},
		{Key: "network_id_localnet", Category: "Network", Label: "Network ID · localnet", Desc: "Lux primary network id for localnet (= 1337).", Type: TypeInt, Env: "LUX_NETWORK_ID_LOCALNET", Default: "1337", ReadOnly: true},
	} {
		Register(d)
	}
}
