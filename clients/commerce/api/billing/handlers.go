package billing

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

// Route registers billing endpoints for service-to-service calls.
// These are internal endpoints used by Cloud-API; require admin token.
func Route(r router.Router, args ...gin.HandlerFunc) {
	adminRequired := middleware.TokenRequired(permission.Admin)

	// mintRequired gates the money-MINT routes (those that credit spendable
	// balance from a client-supplied amount) on the internal service token OR a
	// platform SuperAdmin ONLY — NEVER the org-level Admin bit. Without it,
	// TokenRequired(permission.Admin) admitted any org OWNER (org-level IAM
	// isAdmin → Admin|Live), who could then self-credit unlimited balance →
	// unlimited free inference (the real-money-GA blocker). cloud-api's
	// service-token money path is UNAFFECTED (the service-token branch grants the
	// marker mintRequired checks). See middleware/platformonly.go.
	mintRequired := middleware.PlatformOnly()

	api := r.Group("billing")
	api.Use(adminRequired)

	// Tier (tier-aware billing)
	api.GET("/tier", GetTier)

	// Included monthly usage allotment (plan free-tier credit).
	// grant/run mutate; usage-rollup is the read surface for console.
	api.POST("/allotment/grant", GrantAllotment)
	api.POST("/allotment/run", RunAllotments)
	api.GET("/usage-rollup", GetUsageRollup)

	// Balance & usage (existing)
	api.GET("/balance", GetBalance)
	api.GET("/balance/all", GetBalanceAll)
	api.GET("/usage", GetUsage)
	api.POST("/usage", RecordUsage)
	// Money-MINT routes: service-token / SuperAdmin ONLY (mintRequired).
	api.POST("/deposit", mintRequired, Deposit)
	api.POST("/refund", mintRequired, Refund)

	// Chain-backed credit ledger (HUSD): the indexer backfill/reconcile pass and
	// the metered-usage settlement sweep are platform-only (they move on-chain
	// money / write the money ledger); status is a read-only observability surface.
	api.POST("/husd/sync", mintRequired, SyncHUSD)
	api.POST("/husd/settle", mintRequired, SettleHUSD)
	api.POST("/husd/migrate", mintRequired, MigrateHUSD)
	api.GET("/husd/status", StatusHUSD)

	// SBOM-driven OSS-developer payout.
	//   POST /sbom               — arcd build pipeline ingests an image's SBOM
	//   GET  /sbom               — list stored SBOM records
	//   GET  /oss-accruals       — per-line accrual ledger reads
	//   GET  /oss-payout/summary — per-package payout rollup (disbursement view)
	api.POST("/sbom", IngestSBOM)
	api.GET("/sbom", ListSBOMs)
	api.GET("/oss-accruals", ListOSSAccruals)
	api.GET("/oss-payout/summary", GetOSSPayoutSummary)

	// Meters
	api.POST("/meters", CreateMeter)
	api.GET("/meters", ListMeters)
	api.GET("/meters/:id", GetMeter)

	// Meter events
	api.POST("/meter-events", RecordMeterEvents)
	api.GET("/meter-events/summary", GetMeterEventsSummary)

	// Tier check (lightweight model-access gate for Chat / white-label)
	api.GET("/tier-check", TierCheck)

	// Credit grants (money-MINT: service-token / SuperAdmin ONLY). Reads moved
	// to the user group below. Void is a grant mutation in the same resource
	// family — same platform-only bar, so an org owner can neither create nor
	// alter a grant.
	api.POST("/credit-grants", mintRequired, CreateCreditGrant)
	api.POST("/credit-grants/:id/void", mintRequired, VoidCreditGrant)

	// Starter credit grant (service-to-service, idempotent, no payment method
	// required). The on-signup welcome deposit invoked by chat / cloud-api on
	// a user's first use, keyed by an explicit per-user (or per-org) subject.
	// Money-MINT: service-token / SuperAdmin ONLY. (The user-facing,
	// fixed-amount, idempotent welcome credit is the SEPARATE user-group
	// POST /billing/credit → GrantStarterCredit, which stays self-service.)
	api.POST("/grant-starter", mintRequired, GrantStarter)

	// Pricing rules
	api.POST("/pricing-rules", CreatePricingRule)
	api.GET("/pricing-rules", ListPricingRules)
	api.DELETE("/pricing-rules/:id", DeletePricingRule)

	// Invoice preview (legacy)
	api.POST("/invoice-preview", InvoicePreview)

	// Billing invoices
	api.POST("/invoices", CreateInvoice)
	api.GET("/invoices", ListInvoices)
	api.GET("/invoices/upcoming", UpcomingInvoice)
	api.GET("/invoices/:id", GetInvoice)
	api.POST("/invoices/:id/finalize", FinalizeInvoice)
	api.POST("/invoices/:id/pay", PayInvoice)
	api.POST("/invoices/:id/void", VoidInvoice)

	// Billing subscriptions
	api.POST("/subscriptions", CreateBillingSubscription)
	api.GET("/subscriptions", ListBillingSubscriptions)
	api.GET("/subscriptions/:id", GetBillingSubscription)
	api.PATCH("/subscriptions/:id", UpdateBillingSubscription)
	api.POST("/subscriptions/:id/cancel", CancelBillingSubscription)
	api.POST("/subscriptions/:id/reactivate", ReactivateBillingSubscription)
	api.POST("/subscriptions/:id/renew", RenewBillingSubscription)

	// Payment intents
	api.POST("/payment-intents", CreatePaymentIntent)
	api.GET("/payment-intents", ListPaymentIntents)
	api.GET("/payment-intents/:id", GetPaymentIntent)
	api.POST("/payment-intents/:id/confirm", ConfirmPaymentIntent)
	api.POST("/payment-intents/:id/capture", CapturePaymentIntent)
	api.POST("/payment-intents/:id/cancel", CancelPaymentIntent)

	// Setup intents
	api.POST("/setup-intents", CreateSetupIntent)
	api.GET("/setup-intents/:id", GetSetupIntent)
	api.POST("/setup-intents/:id/confirm", ConfirmSetupIntent)
	api.POST("/setup-intents/:id/cancel", CancelSetupIntent)

	// Payment methods — moved to user group below (accepts both admin & user tokens)

	// Subscription items
	api.POST("/subscription-items", CreateSubscriptionItem)
	api.GET("/subscription-items", ListSubscriptionItems)
	api.GET("/subscription-items/:id", GetSubscriptionItem)
	api.PATCH("/subscription-items/:id", UpdateSubscriptionItem)
	api.DELETE("/subscription-items/:id", DeleteSubscriptionItem)

	// Refunds
	api.POST("/refunds", CreateRefund)
	api.GET("/refunds", ListRefunds)
	api.GET("/refunds/:id", GetRefund)

	// Credit notes
	api.POST("/credit-notes", CreateCreditNote)
	api.GET("/credit-notes", ListCreditNotes)
	api.GET("/credit-notes/:id", GetCreditNote)
	api.POST("/credit-notes/:id/void", VoidCreditNote)

	// Disputes
	api.GET("/disputes", ListDisputes)
	api.GET("/disputes/:id", GetDispute)
	api.PATCH("/disputes/:id", SubmitDisputeEvidence)
	api.POST("/disputes/:id/close", CloseDispute)

	// Customer balance (reads stay admin; the adjustment MINTS balance →
	// service-token / SuperAdmin ONLY).
	api.GET("/customer-balance", GetCustomerBalance)
	api.POST("/customer-balance/adjustments", mintRequired, AdjustCustomerBalance)
	api.GET("/balance-transactions", ListBalanceTransactions)

	// Payouts. Creating/cancelling a payout MOVES money out — money-MINT bar
	// (service-token / SuperAdmin ONLY). Reads stay admin-scoped.
	api.POST("/payouts", mintRequired, CreatePayout)
	api.GET("/payouts", ListPayouts)
	api.GET("/payouts/:id", GetPayout)
	api.POST("/payouts/:id/cancel", mintRequired, CancelPayout)

	// Billing events
	api.GET("/events", ListBillingEvents)
	api.GET("/events/:id", GetBillingEvent)

	// Webhook endpoints (outbound: for creating and listing webhook subscriptions)
	api.POST("/webhook-endpoints", CreateWebhookEndpoint)
	api.GET("/webhook-endpoints", ListWebhookEndpoints)
	api.GET("/webhook-endpoints/:id", GetWebhookEndpoint)
	api.PATCH("/webhook-endpoints/:id", UpdateWebhookEndpoint)
	api.DELETE("/webhook-endpoints/:id", DeleteWebhookEndpoint)

	// Inbound webhook ingress (unauthenticated — signature-verified per provider).
	// Registered outside the admin-token group because providers do not carry
	// commerce admin tokens; the provider's signature is the trust anchor.
	r.POST("/billing/webhooks/:provider", HandleProviderWebhook)

	// Customer portal
	api.GET("/portal/overview", PortalOverview)
	api.GET("/portal/invoices", PortalInvoices)
	api.GET("/portal/subscriptions", PortalSubscriptions)
	api.GET("/portal/payment-methods", PortalPaymentMethods)

	// Subscription schedules
	api.POST("/subscription-schedules", CreateSubscriptionSchedule)
	api.GET("/subscription-schedules", ListSubscriptionSchedules)
	api.GET("/subscription-schedules/:id", GetSubscriptionSchedule)
	api.PATCH("/subscription-schedules/:id", UpdateSubscriptionSchedule)
	api.POST("/subscription-schedules/:id/cancel", CancelSubscriptionSchedule)
	api.POST("/subscription-schedules/:id/release", ReleaseSubscriptionSchedule)

	// Bank transfer instructions
	api.POST("/bank-transfer-instructions", CreateBankTransferInstruction)
	api.GET("/bank-transfer-instructions", ListBankTransferInstructions)
	api.GET("/bank-transfer-instructions/:id", GetBankTransferInstruction)
	api.POST("/reconciliation/match", ReconcileInboundTransfer)

	// Invoice sub-resources
	api.POST("/invoices/:id/line-items", AddInvoiceLineItem)
	api.DELETE("/invoices/:id/line-items/:itemId", RemoveInvoiceLineItem)
	api.POST("/invoices/:id/apply-discount", ApplyInvoiceDiscount)
	api.POST("/invoices/:id/calculate-tax", CalculateInvoiceTax)

	// Capabilities
	api.GET("/capabilities", GetCapabilities)

	// Top-up: charge a saved payment method and credit user balance
	api.POST("/topup", Topup)

	// GPU billing (server-enforced prepaid-only + card-required). The cloud GPU
	// launch gate reads /gpu-eligibility before provisioning and POSTs /gpu-charge
	// to debit; a GPU charge NEVER draws credit grants (see api/billing/gpu_charge.go).
	api.GET("/gpu-eligibility", GPUChargeEligibility)
	api.POST("/gpu-charge", ChargeGPU)

	// ZAP protocol endpoint
	api.POST("/zap", ZapDispatch)

	// DNS billing endpoints
	dns := r.Group("dns")
	dns.Use(adminRequired)
	dns.POST("/usage", RecordDNSUsage)
	dns.GET("/usage/summary", GetDNSUsageSummary)

	// Billing cycle automation (platform scheduler / service). Collecting a
	// cycle charges cards across orgs — money-MINT bar (service-token /
	// SuperAdmin ONLY), never an org owner's Admin bit. run-all sweeps EVERY
	// org, so it is emphatically platform-only.
	api.POST("/cycle/run", mintRequired, RunBillingCycle)
	api.POST("/cycle/run-user", mintRequired, RunBillingCycleUser)
	api.POST("/cycle/run-all", mintRequired, RunBillingCycleAllOrgs)

	// Auto-recharge sweep (called by the platform scheduler / CronJob): charge
	// the default card for orgs whose balance dropped below their threshold.
	// Platform-wide card charging — money-MINT bar (service-token / SuperAdmin
	// ONLY). An org owner reaching this could sweep-charge saved cards across
	// every org.
	api.POST("/auto-recharge/run-all", mintRequired, RunAutoRechargeAllOrgs)

	// Test mode toggle: move an org between Square sandbox and production. This
	// flips whether charges hit real cards, so it is a money-mode change —
	// service-token / SuperAdmin ONLY, never an org owner.
	api.POST("/test-mode", mintRequired, SetOrgTestMode)

	// ── User-facing billing endpoints ─────────────────────────────────────
	// Called by billing.hanzo.ai with user OIDC tokens. Gated by a NO-MASK
	// TokenRequired(): any authenticated principal is admitted — an IAM user (via
	// the validated iam_authenticated identity) OR a non-IAM service token (via
	// the service-token branch). It deliberately does NOT require the Admin bit
	// the admin endpoints above use, so a normal user can manage their own
	// billing; per-user/per-org scoping is enforced in the handlers and by
	// EdgeAuth's billing-subject lock at the edge. (Masked gates DO enforce their
	// masks on the IAM path since v1.46.5 — see middleware/accesstoken.go.)
	userRequired := middleware.TokenRequired()

	user := r.Group("billing")
	user.Use(userRequired)

	// Invoice PDF download (user-scoped). Tenant-isolated: the handler loads the
	// invoice from the caller's OWN org namespace, so a foreign invoice id 404s.
	// A normal authenticated org member can download their own invoice; Console
	// opens this as a per-invoice download link.
	user.GET("/invoices/:id/pdf", DownloadInvoicePDF)

	// Card tokenization — S2S (no provider SDK on frontend)
	user.POST("/card/tokenize", TokenizeCard)

	// Public Square config for THIS org's Web Payments SDK (sandbox for test
	// orgs, production for live orgs) — so the browser tokenizes against the
	// same Square account commerce vaults/charges with.
	user.GET("/payment-config", GetPaymentConfig)

	// Plans (public catalog — cacheable, no writes).
	// CF caches for 1 hour; plans rarely change.
	user.GET("/plans", middleware.CachePublic(3600), middleware.CFCacheTags("plans"), ListPlans)
	user.GET("/plans/:id", middleware.CachePublic(3600), middleware.CFCacheTags("plans"), GetPlan)

	// DNS plans (public catalog, cacheable)
	dnsUser := r.Group("dns")
	dnsUser.Use(userRequired)
	dnsUser.GET("/plans", middleware.CachePublic(3600), middleware.CFCacheTags("dns-plans"), ListDNSPlans)

	// Auto-recharge config (user-scoped; one per org)
	user.GET("/auto-recharge", GetAutoRecharge)
	user.PUT("/auto-recharge", SetAutoRecharge)

	// Spend alerts + per-scope spend caps (issue #70). CRUD manages the budget
	// rows; /authorize is the per-request cap verdict the cloud metering gate
	// consumes (service-token S2S). Org-scoped via X-Org-Id, so a cap on org X
	// can never gate org Y.
	user.GET("/spend-alerts", ListSpendAlerts)
	user.POST("/spend-alerts", CreateSpendAlert)
	user.GET("/spend-alerts/authorize", AuthorizeSpendCap)
	user.PATCH("/spend-alerts/:id", UpdateSpendAlert)
	user.DELETE("/spend-alerts/:id", DeleteSpendAlert)

	// Billing status — hasPaymentMethod + creditBalance in one call (used by bot gateway)
	user.GET("/status", GetBillingStatus)

	// Self-service balance + welcome credit. Identity comes from the
	// gateway-injected X-Org-Id / X-User-Id headers; the caller never
	// needs an admin token (unlike POST /billing/credit) — it's the
	// on-signup grant that the playground SPA invokes from FundingGate on
	// first login. Idempotent (tag-deduped); no payment method required
	// (the card gates top-up, not the welcome credit).
	user.GET("/me/balance", GetMyBalance)
	user.POST("/me/welcome", PostMyWelcome)

	// Credit grants & balance (read-only, user-scoped)
	user.GET("/credit-grants", ListCreditGrants)
	user.GET("/credit-balance", GetCreditBalance)
	user.GET("/credit-balance/breakdown", GetCreditBalanceBreakdown)
	user.POST("/credit", GrantStarterCredit)

	// Transaction history / ledger (read-only, user-scoped). Derives identity
	// from the IAM org/user in context like the sibling reads above. Called by
	// billing.hanzo.ai's Transactions tab as GET /v1/billing/transactions.
	// Registered here so it lives under the CORS-enabled API group; an
	// unregistered route hits gin NoRoute (404, no Access-Control-Allow-Origin)
	// and the browser reports it as a CORS failure rather than an honest empty list.
	user.GET("/transactions", ListTransactions)

	// Withdraw (user-initiated: move funds out of Commerce balance).
	// Used by bot wallet funding (source=usd) to deduct from user's account.
	// Non-admin callers may only withdraw from their own account.
	user.POST("/withdraw", Withdraw)

	// Top-up with a Square Web Payments SDK nonce (no saved PM required)
	user.POST("/topup/token", TopupWithToken)

	// Payment methods (user-scoped CRUD)
	user.POST("/payment-methods", CreatePaymentMethod)
	user.GET("/payment-methods", ListPaymentMethods)
	user.GET("/payment-methods/:id", GetPaymentMethod)
	user.PATCH("/payment-methods/:id", UpdatePaymentMethod)
	user.DELETE("/payment-methods/:id", DetachPaymentMethod)
	user.POST("/customers/:id/default-payment-method", SetDefaultPaymentMethod)

	// Billing accounts (GCP-style funding entities, SEPARATE from the org — each
	// funds 1..N projects, dedicated or shared). CRUD is org-owner self-service: a
	// limit is a self-imposed budget, not a money mint, so it needs no platform bar
	// (mirrors spend-alerts). Freeze (Enabled=false) is a platform control, not
	// exposed here. Project bindings live UNDER an account (/:id/projects) so the
	// route tree has no static-vs-param conflict with /accounts/:id.
	user.GET("/accounts", ListBillingAccounts)
	user.POST("/accounts", CreateBillingAccount)
	user.GET("/accounts/:id", GetBillingAccount)
	user.PATCH("/accounts/:id", UpdateBillingAccount)
	user.DELETE("/accounts/:id", DeleteBillingAccount)
	user.GET("/accounts/:id/projects", ListAccountProjects)
	user.PUT("/accounts/:id/projects/:project", BindAccountProject)
	user.DELETE("/accounts/:id/projects/:project", UnbindAccountProject)
	user.GET("/accounts/:id/members", ListAccountMembers)
	user.POST("/accounts/:id/members", AddAccountMember)
	user.PATCH("/accounts/:id/members/:memberId", UpdateMemberRole)
	user.DELETE("/accounts/:id/members/:memberId", RemoveAccountMember)
}
