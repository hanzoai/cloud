package flags

// The canonical PLATFORM launch switches — the runtime knobs the SuperAdmin flips from
// admin.hanzo.ai. Each is an Insights feature flag (the engine); this table names it,
// categorizes it, and pins a hardcoded Default. RUNTIME flags resolve from /v1/flags
// (the DB engine) → Default, with NO env gate — /v1/flags is the single source of truth,
// flipped live, no redeploy. Only BOOT-TIME, ReadOnly rows (subsystem activation applied
// via CLOUD_ENABLE/CLOUD_*; network-id display) pin an Env, because that IS their boot
// mechanism, not a redundant runtime override. Subsystems may Register MORE at init —
// this seed is the launch set, not a closed list.
func init() {
	for _, d := range []Def{
		// ── Launch / Waitlist (runtime flags: /v1/flags is the single source of truth; DB → Default, no env gate) ──
		{Key: "waitlist_open", Category: "Launch", Label: "Waitlist intake open", Desc: "Accept new waitlist signups. Off closes intake platform-wide.", Type: TypeBool, Default: "true"},
		{Key: "waitlist_access_capacity", Category: "Launch", Label: "Access capacity", Desc: "How many waitlisted users are granted product access (Insights flag payload = the number).", Type: TypeInt, Default: "0"},
		{Key: "waitlist_points_referral", Category: "Launch", Label: "Points · referral", Desc: "Queue-position points awarded per referral.", Type: TypeInt, Default: "0"},
		{Key: "waitlist_points_social", Category: "Launch", Label: "Points · social", Desc: "Queue-position points awarded for a social share.", Type: TypeInt, Default: "0"},
		{Key: "waitlist_points_hanzod", Category: "Launch", Label: "Points · run hanzod", Desc: "Queue-position points awarded for running hanzod.", Type: TypeInt, Default: "0"},
		{Key: "waitlist_turnstile", Category: "Launch", Label: "Turnstile challenge", Desc: "Require the Cloudflare Turnstile challenge on waitlist signup.", Type: TypeBool, Default: "false"},
		{Key: "waitlist_signup_rate_limit", Category: "Launch", Label: "Signup rate limit", Desc: "Max waitlist signups per source per minute (0 = unlimited).", Type: TypeInt, Default: "0"},

		// ── Signup (runtime flag: /v1/flags only) ──
		{Key: "public_signup", Category: "Signup", Label: "Public open signup", Desc: "Allow anyone to create an account (off = invite / waitlist only).", Type: TypeBool, Default: "false"},

		// ── Subsystem activation (boot-time; applying a flip needs an operator reconcile) ──
		{Key: "subsystem_iam_active", Category: "Subsystems", Label: "IAM (canary auth cutover)", Desc: "Serve identity from the embedded IAM. CANARY-GATED staged auth cutover; applied at boot via CLOUD_ENABLE.", Type: TypeBool, Default: "false", ReadOnly: true},
		{Key: "subsystem_iam2_active", Category: "Subsystems", Label: "IAM v2 (clean-room, beego-free)", Desc: "Serve identity from the clean-room iam2 instead of the beego/Casdoor embed. Selected at boot via CLOUD_IAM_IMPL=iam2 (one selector, applied on the next reconcile). Gate before flipping: the IAM cutover parity suite (universe e2e/50-iam-cutover-parity) must be green against the iam2 shadow.", Type: TypeBool, Default: "false", ReadOnly: true},
		{Key: "subsystem_ingress_active", Category: "Subsystems", Label: "Ingress edge", Desc: "Serve the embedded ingress edge (routes/TLS/ACME). Applied at boot via CLOUD_ENABLE.", Type: TypeBool, Default: "false", ReadOnly: true},
		{Key: "subsystem_pubsub_active", Category: "Subsystems", Label: "PubSub (NATS+JetStream)", Desc: "Serve the embedded messaging plane. Applied at boot via CLOUD_PUBSUB_ENABLED.", Type: TypeBool, Env: "CLOUD_PUBSUB_ENABLED", Default: "false", ReadOnly: true},

		// ── Gateway (full rate-limit / quota / CORS config lives at /v1/gateway) ──
		{Key: "gateway_rate_limit_rpm", Category: "Gateway", Label: "Rate limit (req/min)", Desc: "Default per-key request rate limit. Full CORS / quota config lives at /v1/gateway.", Type: TypeInt, Default: "0"},
		{Key: "gateway_quota_daily", Category: "Gateway", Label: "Daily quota", Desc: "Default per-key daily request quota (0 = unlimited).", Type: TypeInt, Default: "0"},

		// ── Network (canonical Lux primary network ids — read-only display + override knob) ──
		{Key: "network_id_mainnet", Category: "Network", Label: "Network ID · mainnet", Desc: "Lux primary network id for mainnet (convention-fixed = 1).", Type: TypeInt, Env: "LUX_NETWORK_ID_MAINNET", Default: "1", ReadOnly: true},
		{Key: "network_id_testnet", Category: "Network", Label: "Network ID · testnet", Desc: "Lux primary network id for testnet (= 2).", Type: TypeInt, Env: "LUX_NETWORK_ID_TESTNET", Default: "2", ReadOnly: true},
		{Key: "network_id_local", Category: "Network", Label: "Network ID · local", Desc: "Lux primary network id for local (= 3).", Type: TypeInt, Env: "LUX_NETWORK_ID_LOCAL", Default: "3", ReadOnly: true},
		{Key: "network_id_localnet", Category: "Network", Label: "Network ID · localnet", Desc: "Lux primary network id for localnet (= 1337).", Type: TypeInt, Env: "LUX_NETWORK_ID_LOCALNET", Default: "1337", ReadOnly: true},
	} {
		Register(d)
	}
}
