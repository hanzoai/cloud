package email

import (
	"encoding/json"
	"time"
)

// CheckStatus represents the discrete outcome of a verification check.
// Conflating syntax, deliverability, and policy into a single boolean is deliberately avoided.
type CheckStatus string

const (
	StatusPass    CheckStatus = "pass"
	StatusFail    CheckStatus = "fail"
	StatusUnknown CheckStatus = "unknown"
	StatusSkipped CheckStatus = "skipped"
)

// CheckName names an individual verification check.
type CheckName string

const (
	CheckSyntax         CheckName = "syntax"
	CheckDomain         CheckName = "domain"
	CheckMX             CheckName = "mx"
	CheckClassification CheckName = "classification"
	CheckSMTP           CheckName = "smtp"
)

// SyntaxCheck evaluates address structure under RFC 5322.
type SyntaxCheck struct {
	// Status reports the check outcome: pass, fail, unknown, or skipped.
	Status CheckStatus `json:"status"`
	// Reason describes the failure or skip reason when Status is not pass.
	Reason string `json:"reason,omitempty"`
	// Username is the local-part of the email address.
	Username string `json:"username,omitempty"`
	// Domain is the domain-part of the email address.
	Domain string `json:"domain,omitempty"`
}

// DomainCheck evaluates the presence and reachability of the domain via DNS.
type DomainCheck struct {
	// Status reports the check outcome: pass, fail, unknown, or skipped.
	Status CheckStatus `json:"status"`
	// Reason describes the failure or skip reason when Status is not pass.
	Reason string `json:"reason,omitempty"`
}

// MXCheck evaluates DNS mail exchange records for the domain.
type MXCheck struct {
	// Status reports the check outcome: pass, fail, unknown, or skipped.
	Status CheckStatus `json:"status"`
	// Reason describes the failure or skip reason when Status is not pass.
	Reason string `json:"reason,omitempty"`
	// Records lists the discovered mail exchanger hostnames.
	Records []string `json:"records,omitempty"`
}

// ClassificationCheck reports domain attributes. Free, disposable, or role addresses
// are not inherently invalid; they describe account characteristics for business policy.
type ClassificationCheck struct {
	// Status reports the check outcome: pass, fail, unknown, or skipped.
	Status CheckStatus `json:"status"`
	// Reason describes the failure or skip reason when Status is not pass.
	Reason string `json:"reason,omitempty"`
	// Disposable reports whether the domain belongs to a temporary or throwaway provider.
	Disposable bool `json:"disposable"`
	// Free reports whether the domain belongs to a free consumer webmail provider.
	Free bool `json:"free"`
	// Role reports whether the address is a generic role or distribution account.
	Role bool `json:"role"`
}

// SMTPCheck details mailbox probing when explicitly requested.
type SMTPCheck struct {
	// Status reports the check outcome: pass, fail, unknown, or skipped.
	Status CheckStatus `json:"status"`
	// Reason describes the failure or skip reason when Status is not pass.
	Reason string `json:"reason,omitempty"`
	// HostExists reports whether the destination mail server host exists and responded.
	HostExists bool `json:"host_exists,omitempty"`
	// Deliverable reports whether the mailbox accepted the test recipient address.
	Deliverable bool `json:"deliverable,omitempty"`
	// CatchAll reports whether the host accepts all recipient addresses.
	CatchAll bool `json:"catch_all,omitempty"`
	// Disabled reports whether the recipient mailbox is disabled or inactive.
	Disabled bool `json:"disabled,omitempty"`
	// FullInbox reports whether the recipient mailbox is over quota.
	FullInbox bool `json:"full_inbox,omitempty"`
}

// VerificationChecks holds the outcomes of all evaluated checks.
type VerificationChecks struct {
	// Syntax details RFC 5322 structure verification.
	Syntax *SyntaxCheck `json:"syntax,omitempty"`
	// Domain details DNS domain host resolution.
	Domain *DomainCheck `json:"domain,omitempty"`
	// MX details DNS mail exchanger lookup.
	MX *MXCheck `json:"mx,omitempty"`
	// Classification details account attributes (disposable, free, role).
	Classification *ClassificationCheck `json:"classification,omitempty"`
	// SMTP details explicit mailbox probing.
	SMTP *SMTPCheck `json:"smtp,omitempty"`
}

// Suggestion offers a candidate address without silently rewriting the submitted address.
type Suggestion struct {
	// Email is the suggested alternative address.
	Email string `json:"email"`
	// Reason describes why the suggestion was made (e.g. domain_typo).
	Reason string `json:"reason"`
}

// VerificationInput is the request payload for POST /v1/email/verify.
type VerificationInput struct {
	// Email is the address to evaluate.
	Email string `json:"email"`
	// Checks explicitly selects which checks to run.
	// Defaults to ["syntax", "domain", "mx", "classification"].
	Checks []string `json:"checks,omitempty"`
	// Suggestions requests domain typo suggestions without altering the original email.
	Suggestions *bool `json:"suggestions,omitempty"`
	// HelloName sets domain used in EHLO for SMTP probe when explicitly enabled.
	HelloName string `json:"hello_name,omitempty"`
	// FromEmail sets sender used in MAIL FROM for SMTP probe when explicitly enabled.
	FromEmail string `json:"from_email,omitempty"`
	// ProxyURI sets optional proxy for network lookups.
	ProxyURI string `json:"proxy_uri,omitempty"`
}

// VerificationResult is the response payload for POST /v1/email/verify.
type VerificationResult struct {
	// Email is the original submitted email address.
	Email string `json:"email"`
	// Checks contains the outcomes of all requested checks.
	Checks VerificationChecks `json:"checks"`
	// Suggestions lists potential corrections when requested.
	Suggestions []Suggestion `json:"suggestions,omitempty"`
}

// DeliveryStatus is what became of a message: the furthest thing that happened to
// it, and a bounce or a complaint over anything before.
type DeliveryStatus string

const (
	StatusQueued     DeliveryStatus = "queued"
	StatusSent       DeliveryStatus = "sent"
	StatusFailed     DeliveryStatus = "failed"
	StatusDelivered  DeliveryStatus = "delivered"
	StatusOpened     DeliveryStatus = "opened"
	StatusClicked    DeliveryStatus = "clicked"
	StatusBounced    DeliveryStatus = "bounced"
	StatusComplained DeliveryStatus = "complained"
)

// SendMessageInput is the request payload for POST /v1/email.
type SendMessageInput struct {
	// From is the sender address. If empty, the provider credential's from-email is used.
	From string `json:"from,omitempty"`
	// To lists destination recipient addresses. Each gets a message of its own.
	To []string `json:"to"`
	// Subject is the message subject.
	Subject string `json:"subject,omitempty"`
	// Text is the plain text body.
	Text string `json:"text,omitempty"`
	// Body is an alias for Text for backward compatibility.
	Body string `json:"body,omitempty"`
	// HTML is the HTML body.
	HTML string `json:"html,omitempty"`
	// IdempotencyKey names the send, so a retried request sends each recipient's
	// message once: the same key and recipient name the same message.
	IdempotencyKey string `json:"idempotency_key,omitempty" header:"Idempotency-Key"`
	// Provider pins the rail, ses or sendgrid. Empty is the org's default
	// (PUT /v1/email/provider), else ses, else sendgrid, whichever the org holds
	// credentials for.
	Provider string `json:"provider,omitempty"`
	// Campaign tags the messages: 1 to 128 letters, digits, '-' or '_', which is
	// what an SES message tag may hold. Every link carries it as utm_campaign, the
	// provider carries it back on every event, and the stats count by it.
	Campaign string `json:"campaign,omitempty"`
	// TemplateID optionally references a built-in template.
	TemplateID string `json:"template_id,omitempty"`
	// TemplateVars carries variables for template rendering.
	TemplateVars json.RawMessage `json:"template_vars,omitempty"`
	// Headers provides optional custom MIME headers.
	Headers map[string]string `json:"headers,omitempty"`
	// Sync is accepted for backwards compatibility with notify sends.
	Sync string `json:"sync,omitempty"`
}

// MessageRecord is one message of the org's ledger.
type MessageRecord struct {
	// ID is our id for the message (msg_…): every link in it carries it as from=,
	// and the event stream names it by it.
	ID string `json:"id"`
	// Org is the organization owning this message.
	Org string `json:"org,omitempty"`
	// Status is queued, sent, failed, delivered, opened, clicked, bounced or
	// complained.
	Status DeliveryStatus `json:"status"`
	// Origin is api for a message POST /v1/email sent, provider for one the
	// provider's events told of, sent from the org's own tools.
	Origin string `json:"origin"`
	// From is the sender address, when known.
	From string `json:"from,omitempty"`
	// To lists the recipients.
	To []string `json:"to"`
	// Subject is the message subject line, when known.
	Subject string `json:"subject,omitempty"`
	// Provider is the rail it went by: ses or sendgrid.
	Provider string `json:"provider,omitempty"`
	// ProviderID is the provider's own id for the message, which its events name
	// it by; empty until the provider answers one.
	ProviderID string `json:"provider_id,omitempty"`
	// Campaign is the campaign the message was tagged with.
	Campaign string `json:"campaign,omitempty"`
	// CreatedAt is when the message was sent, or accepted to be.
	CreatedAt time.Time `json:"created_at"`
	// DeliveredAt is when the receiving server first took it.
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	// OpenedAt is when it was first opened.
	OpenedAt *time.Time `json:"opened_at,omitempty"`
	// ClickedAt is when a link in it was first clicked.
	ClickedAt *time.Time `json:"clicked_at,omitempty"`
	// BouncedAt is when it bounced, and Bounce whether hard or soft.
	BouncedAt *time.Time `json:"bounced_at,omitempty"`
	// Bounce is hard or soft, for a message that bounced.
	Bounce string `json:"bounce,omitempty"`
	// ComplainedAt is when its recipient reported it as spam.
	ComplainedAt *time.Time `json:"complained_at,omitempty"`
	// UnsubscribedAt is when its recipient unsubscribed from it.
	UnsubscribedAt *time.Time `json:"unsubscribed_at,omitempty"`
	// Error says why it failed, bounced, was deferred or was rejected.
	Error string `json:"error,omitempty"`
	// IdempotencyKey is the key the message was sent under.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Dispatch is what POST /v1/email did, recipient by recipient.
type Dispatch struct {
	// Messages is one message per recipient that was sent, each with its own
	// unsubscribe link; a message the provider refused is here with status failed.
	Messages []*MessageRecord `json:"messages"`
	// Refused is every recipient the suppression check refused. Nothing was sent
	// to them.
	Refused []Refused `json:"refused,omitempty"`
}

// Refused is one recipient a commercial message was not sent to.
type Refused struct {
	// Address is the recipient.
	Address string `json:"address"`
	// Status is 422: the address may not be mailed.
	Status int `json:"status"`
	// Reason is why: unsubscribed, company unsubscribed (the address's domain
	// opted out of the org's mail) or blocklisted (the platform's list).
	Reason string `json:"reason"`
}

// GetMessageInput is the request payload for GET /v1/email/:id.
type GetMessageInput struct {
	// ID is our id for the message (msg_…).
	ID string `path:"id" json:"id"`
}

// ListMessagesInput narrows GET /v1/email.
type ListMessagesInput struct {
	// Limit is the most messages on the page (default 50, max 200).
	Limit int `query:"limit" json:"limit,omitempty"`
	// Status is one status to list: queued, sent, failed, delivered, opened,
	// clicked, bounced or complained.
	Status string `query:"status" json:"status,omitempty"`
	// Provider is ses or sendgrid.
	Provider string `query:"provider" json:"provider,omitempty"`
	// Since is the earliest send time listed, RFC 3339.
	Since string `query:"since" json:"since,omitempty"`
	// Until is the send time listing stops before, RFC 3339.
	Until string `query:"until" json:"until,omitempty"`
	// Recipient lists the messages to one address.
	Recipient string `query:"recipient" json:"recipient,omitempty"`
	// Cursor is the next of the page before; empty is the newest page.
	Cursor string `query:"cursor" json:"cursor,omitempty"`
}

// ListMessagesResult is one page of the org's messages, newest first.
type ListMessagesResult struct {
	// Messages is the page.
	Messages []*MessageRecord `json:"messages"`
	// Next is the cursor of the page after this one; empty on the last page.
	Next string `json:"next,omitempty"`
}

// StatsInput is the range and narrowing of GET /v1/email/stats.
type StatsInput struct {
	// Since is where the range starts, RFC 3339, rounded down to its day or hour.
	// Empty is 30 days before Until by day, 48 hours by hour.
	Since string `query:"since" json:"since,omitempty"`
	// Until is where the range ends, RFC 3339, rounded up to its day or hour.
	// Empty is now.
	Until string `query:"until" json:"until,omitempty"`
	// Interval is day (default, at most 400 days) or hour (at most 31 days), in UTC.
	Interval string `query:"interval" json:"interval,omitempty"`
	// Provider counts one rail only: ses or sendgrid.
	Provider string `query:"provider" json:"provider,omitempty"`
	// Campaign counts one campaign only.
	Campaign string `query:"campaign" json:"campaign,omitempty"`
}

// Tally is the org's mail counted. Each counts messages, once each: a message
// opened three times is one opened.
type Tally struct {
	// Sent is messages the provider accepted.
	Sent int64 `json:"sent"`
	// Delivered is messages the receiving server took.
	Delivered int64 `json:"delivered"`
	// Opened is messages opened.
	Opened int64 `json:"opened"`
	// Clicked is messages a link of which was clicked.
	Clicked int64 `json:"clicked"`
	// Bounced is messages that bounced, hard or soft (SendGrid's dropped included).
	Bounced int64 `json:"bounced"`
	// Complained is messages reported as spam.
	Complained int64 `json:"complained"`
	// Unsubscribed is messages whose recipient unsubscribed at the provider.
	Unsubscribed int64 `json:"unsubscribed"`
}

// Part is the tally of one provider or one campaign.
type Part struct {
	// Name is the provider (ses, sendgrid) or the campaign.
	Name string `json:"name"`
	// Tally is its mail counted over the range.
	Tally Tally `json:"tally"`
}

// Point is the tally of one day or hour of the range.
type Point struct {
	// At is the start of the day or hour, UTC.
	At time.Time `json:"at"`
	// Tally is its mail counted.
	Tally Tally `json:"tally"`
}

// Stats is the org's mail counted over a range: in all, by provider, by campaign,
// and by day or hour. Each count lands in the day or hour it happened in.
type Stats struct {
	// Since is the start of the range, UTC.
	Since time.Time `json:"since"`
	// Until is the end of the range, UTC, not included.
	Until time.Time `json:"until"`
	// Interval is day or hour: the step of Series.
	Interval string `json:"interval"`
	// Provider is the one rail counted, when the request named one.
	Provider string `json:"provider,omitempty"`
	// Campaign is the one campaign counted, when the request named one.
	Campaign string `json:"campaign,omitempty"`
	// Totals is the whole range.
	Totals Tally `json:"totals"`
	// Providers is the range by provider.
	Providers []Part `json:"providers"`
	// Campaigns is the range by campaign, the most sent first, at most 200; empty
	// when the request named a campaign.
	Campaigns []Part `json:"campaigns"`
	// Series is every day or hour of the range in order, empty ones included.
	Series []Point `json:"series"`
}

// Choice names the provider an org's mail leaves by when a request pins none.
type Choice struct {
	// Provider is ses or sendgrid.
	Provider string `json:"provider"`
}

// SESStatusInput is the request payload for GET /v1/email/ses/status.
type SESStatusInput struct {
	// Domain optionally specifies the sending domain to generate Easy DKIM and MAIL FROM records for.
	Domain string `query:"domain" json:"domain,omitempty"`
}

// SESDNSRecord describes one DNS record required for Amazon SES domain identity verification.
type SESDNSRecord struct {
	// Purpose describes the role of the DNS record (e.g. DKIM, MAIL FROM MX, MAIL FROM SPF, DMARC).
	Purpose string `json:"purpose"`
	// Type is the DNS record type (CNAME, MX, TXT).
	Type string `json:"type"`
	// Host is the DNS record host or subdomain name.
	Host string `json:"host"`
	// Value is the DNS record target or text value.
	Value string `json:"value"`
	// Priority is the MX priority when Type is MX.
	Priority int `json:"priority,omitempty"`
	// Verified reports whether the DNS record is verified for the sending identity.
	Verified bool `json:"verified"`
}

// SESStatusResult is the response payload for GET /v1/email/ses/status.
type SESStatusResult struct {
	// Org is the organization identifier.
	Org string `json:"org"`
	// Configured reports whether the org holds the SES credentials a send needs
	// (access-key-id, secret-access-key, from-email).
	Configured bool `json:"configured"`
	// ActiveProvider is the provider the org's mail leaves by when a request pins none.
	ActiveProvider string `json:"active_provider"`
	// Region is the configured Amazon SES AWS region (defaults to us-east-1).
	Region string `json:"region"`
	// FromEmail is the default verified sender address.
	FromEmail string `json:"from_email,omitempty"`
	// FromName is the default sender display name.
	FromName string `json:"from_name,omitempty"`
	// ConfigurationSet is the SES configuration set messages go out under, whose
	// event destination publishes their events (hanzo-crm-events unless the
	// credential names another).
	ConfigurationSet string `json:"configuration_set,omitempty"`
	// Domain is the sending domain inspected.
	Domain string `json:"domain"`
	// MailFromDomain is the custom MAIL FROM subdomain (bounce.<domain>).
	MailFromDomain string `json:"mail_from_domain"`
	// Sandbox is whether SES says the account is still in its sandbox.
	Sandbox bool `json:"sandbox"`
	// SendingEnabled is whether SES says the account may send.
	SendingEnabled bool `json:"sending_enabled"`
	// Max24HourSend is the account's 24-hour sending quota, as SES answers it.
	Max24HourSend float64 `json:"max_24_hour_send"`
	// MaxSendRate is the account's most messages a second, as SES answers it.
	MaxSendRate float64 `json:"max_send_rate"`
	// ProviderSentLast24Hours is what SES counts sent by the account in the last
	// 24 hours, from anywhere.
	ProviderSentLast24Hours float64 `json:"provider_sent_last_24_hours"`
	// QuotaError says why the account's limits could not be read; the limits are
	// then 0.
	QuotaError string `json:"quota_error,omitempty"`
	// SentLast24Hours is what the org's ledger counts sent by SES in the last 24
	// hours: through POST /v1/email, and every message SES's events told of.
	SentLast24Hours int `json:"sent_last_24_hours"`
	// DNSRecords lists the Easy DKIM, SPF, MX, and DMARC records for the sending domain.
	DNSRecords []SESDNSRecord `json:"dns_records"`
}

// SendGridStatusInput specifies query parameters for GET /v1/email/sendgrid/status.
type SendGridStatusInput struct {
	// Domain is an optional sending domain to generate SendGrid whitelabel DKIM, Return-Path, and DMARC records for.
	Domain string `json:"domain,omitempty" query:"domain"`
}

// SendGridStatusResult reports the organization's SendGrid outreach configuration (separate from standard Twilio notify) and DNS records.
type SendGridStatusResult struct {
	// Org is the organization identifier.
	Org string `json:"org"`
	// Configured reports whether the org holds the SendGrid credentials a send
	// needs (api-key, from-email).
	Configured bool `json:"configured"`
	// ActiveProvider is the provider the org's mail leaves by when a request pins none.
	ActiveProvider string `json:"active_provider"`
	// SeparateFromTwilioNotify confirms that SendGrid outreach uses its own dedicated API key, sender identity, and IP pool distinct from standard Twilio transactional notifications.
	SeparateFromTwilioNotify bool `json:"separate_from_twilio_notify"`
	// FromEmail is the configured SendGrid outreach sender email address.
	FromEmail string `json:"from_email,omitempty"`
	// FromName is the configured SendGrid outreach sender display name.
	FromName string `json:"from_name,omitempty"`
	// ReplyTo is the configured Reply-To address for outreach replies.
	ReplyTo string `json:"reply_to,omitempty"`
	// IPPool is the SendGrid IP pool the credential names, if any.
	IPPool string `json:"ip_pool,omitempty"`
	// ASMGroupID is the optional SendGrid suppression group ID.
	ASMGroupID string `json:"asm_group_id,omitempty"`
	// Domain is the sending domain used for SendGrid Domain Authentication and DMARC.
	Domain string `json:"domain"`
	// CreditsTotal, CreditsRemaining and CreditsUsed are the account's sending
	// credits for the period, as SendGrid answers them.
	CreditsTotal int64 `json:"credits_total"`
	// CreditsRemaining is the credits left in the period.
	CreditsRemaining int64 `json:"credits_remaining"`
	// CreditsUsed is the credits used in the period.
	CreditsUsed int64 `json:"credits_used"`
	// QuotaError says why the account's credits could not be read; they are then 0.
	QuotaError string `json:"quota_error,omitempty"`
	// SentLast24Hours is what the org's ledger counts sent by SendGrid in the last
	// 24 hours: through POST /v1/email, and every message SendGrid's events told of.
	SentLast24Hours int `json:"sent_last_24_hours"`
	// DNSRecords lists the required SendGrid Domain Authentication CNAME, Link Branding, SPF, and DMARC DNS records.
	DNSRecords []SESDNSRecord `json:"dns_records"`
}
