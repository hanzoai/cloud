# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package email

struct Choice {
    Provider text @0
}

struct Dispatch {
    Messages list<bytes> @0
    Refused  list<bytes> @8
}

struct GetMessageInput {
    ID text @0
}

struct ListMessagesInput {
    Limit     i64  @0
    Status    text @8
    Provider  text @16
    Since     text @24
    Until     text @32
    Recipient text @40
    Cursor    text @48
}

struct ListMessagesResult {
    Messages list<bytes> @0
    Next     text        @8
}

struct MessageRecord {
    ID             text       @0
    Org            text       @8
    Status         text       @16
    Origin         text       @24
    From           text       @32
    To             list<text> @40
    Subject        text       @48
    Provider       text       @56
    ProviderID     text       @64
    Campaign       text       @72
    CreatedAt      bytes      @80
    DeliveredAt    bytes      @88
    OpenedAt       bytes      @96
    ClickedAt      bytes      @104
    BouncedAt      bytes      @112
    Bounce         text       @120
    ComplainedAt   bytes      @128
    UnsubscribedAt bytes      @136
    Error          text       @144
    IdempotencyKey text       @152
}

struct SESStatusInput {
    Domain text @0
}

struct SESStatusResult {
    Org                     text        @0
    Configured              bool        @8
    ActiveProvider          text        @16
    Region                  text        @24
    FromEmail               text        @32
    FromName                text        @40
    ConfigurationSet        text        @48
    Domain                  text        @56
    MailFromDomain          text        @64
    Sandbox                 bool        @72
    SendingEnabled          bool        @73
    Max24HourSend           f64         @80
    MaxSendRate             f64         @88
    ProviderSentLast24Hours f64         @96
    QuotaError              text        @104
    SentLast24Hours         i64         @112
    DNSRecords              list<bytes> @120
}

struct SendGridStatusInput {
    Domain text @0
}

struct SendGridStatusResult {
    Org                      text        @0
    Configured               bool        @8
    ActiveProvider           text        @16
    SeparateFromTwilioNotify bool        @24
    FromEmail                text        @32
    FromName                 text        @40
    ReplyTo                  text        @48
    IPPool                   text        @56
    ASMGroupID               text        @64
    Domain                   text        @72
    CreditsTotal             i64         @80
    CreditsRemaining         i64         @88
    CreditsUsed              i64         @96
    QuotaError               text        @104
    SentLast24Hours          i64         @112
    DNSRecords               list<bytes> @120
}

struct Stats {
    Since     bytes       @0
    Until     bytes       @8
    Interval  text        @16
    Provider  text        @24
    Campaign  text        @32
    Totals    bytes       @40
    Providers list<bytes> @48
    Campaigns list<bytes> @56
    Series    list<bytes> @64
}

struct StatsInput {
    Since    text @0
    Until    text @8
    Interval text @16
    Provider text @24
    Campaign text @32
}

struct VerificationInput {
    Email       text       @0
    Checks      list<text> @8
    Suggestions list<bool> @16
    HelloName   text       @24
    FromEmail   text       @32
    ProxyURI    text       @40
}

struct VerificationResult {
    Email       text        @0
    Checks      bytes       @8
    Suggestions list<bytes> @16
}

interface email {
    # Reads a page of the org's ledger, newest first: every message sent
    # through POST /v1/email, and every message SES or SendGrid told of by its events,
    # sent from anywhere on the org's account. It narrows by status, provider, a range
    # of send time (since, until) and one recipient, and pages by cursor: pass the
    # answer's next to read the page after.
    # GET /v1/email
    get_email(req: ListMessagesInput) returns (rep: ListMessagesResult)
    # Reads one message of the org's ledger, sent through this API or told of
    # by its provider, with what became of it.
    # GET /v1/email/:id
    get_email_by_id(req: GetMessageInput) returns (rep: MessageRecord)
    # Reports the org's SendGrid configuration: whether it holds the
    # credentials a send needs, the account's credits as SendGrid answers them, what
    # the org's ledger counted sent by SendGrid in the last 24 hours, and the DNS
    # records of the sending domain.
    # GET /v1/email/sendgrid/status
    get_email_sendgrid_status(req: SendGridStatusInput) returns (rep: SendGridStatusResult)
    # Reports the org's Amazon SES configuration: whether it holds the
    # credentials a send needs, the account's limits as SES answers them (sandbox,
    # sending enabled, 24-hour quota, rate, sent in the last 24 hours), what the org's
    # ledger counted sent by SES in the last 24 hours, and the DNS records of the
    # sending domain.
    # GET /v1/email/ses/status
    get_email_ses_status(req: SESStatusInput) returns (rep: SESStatusResult)
    # Counts the org's mail over a range: sent, delivered, opened, clicked,
    # bounced, complained and unsubscribed, each a count of messages, in all, by
    # provider, by campaign and by day or hour (UTC). Mail sent around this API, from
    # the org's own tools on its SES or SendGrid account, is counted from the
    # providers' events like mail sent through it.
    # GET /v1/email/stats
    get_email_stats(req: StatsInput) returns (rep: Stats)
    # Evaluates an email address using explicitly selected, composable checks.
    # A signed-in caller only: the SMTP check connects to the address's mail server
    # from ours, so an anonymous caller could probe mailboxes and get our addresses
    # blocked. Each caller may run 120 checks an hour and 20 of them SMTP probes, and
    # the deployment 6,000 and 400; past either is 429.
    # POST /v1/email/verify
    post_email_verify(req: VerificationInput) returns (rep: VerificationResult)
    # Sets the provider the org's mail leaves by when a send pins none:
    # ses or sendgrid. With none set it is SES, else SendGrid, whichever the org holds
    # credentials for; a send that pins one goes by it. Org admin only, and on the
    # audit trail (email.provider.set) before it takes effect.
    # PUT /v1/email/provider
    put_email_provider(req: Choice) returns (rep: Choice)
}

# ---------------------------------------------------------------------
# 7 op(s) here. What follows is what this schema does not carry.
#
# dropped (9) — the value does not cross, and nothing fails:
#   MessageRecord.BouncedAt  time.Time  (empty message)
#   MessageRecord.ClickedAt  time.Time  (empty message)
#   MessageRecord.ComplainedAt  time.Time  (empty message)
#   MessageRecord.CreatedAt  time.Time  (empty message)
#   MessageRecord.DeliveredAt  time.Time  (empty message)
#   MessageRecord.OpenedAt  time.Time  (empty message)
#   MessageRecord.UnsubscribedAt  time.Time  (empty message)
#   Stats.Since  time.Time  (empty message)
#   Stats.Until  time.Time  (empty message)
#
# blocked (1) — the op is absent; the field has no wire form:
#   post_email  SendMessageInput.Headers  map[string]string  (map)
#
# opaque (20) — crosses, arrives without its name:
#   Dispatch.Messages  email.MessageRecord (list element)
#   Dispatch.Refused  email.Refused (list element)
#   ListMessagesResult.Messages  email.MessageRecord (list element)
#   MessageRecord.BouncedAt  time.Time
#   MessageRecord.ClickedAt  time.Time
#   MessageRecord.ComplainedAt  time.Time
#   MessageRecord.CreatedAt  time.Time
#   MessageRecord.DeliveredAt  time.Time
#   MessageRecord.OpenedAt  time.Time
#   MessageRecord.UnsubscribedAt  time.Time
#   SESStatusResult.DNSRecords  email.SESDNSRecord (list element)
#   SendGridStatusResult.DNSRecords  email.SESDNSRecord (list element)
#   Stats.Campaigns  email.Part (list element)
#   Stats.Providers  email.Part (list element)
#   Stats.Series  email.Point (list element)
#   Stats.Since  time.Time
#   Stats.Totals  email.Tally
#   Stats.Until  time.Time
#   VerificationResult.Checks  email.VerificationChecks
#   VerificationResult.Suggestions  email.Suggestion (list element)
