# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package notify

struct notifyCredential {
    Provider text @0
    Key      text @8
    Value    text @16
}

struct notifyCredentialRef {
    Provider text @0
    Key      text @8
}

struct notifyDelivery {
    Items list<bytes> @0
}

struct notifyHealth {
    Service text @0
    Status  text @8
}

struct notifySend {
    To           list<text> @0
    Channel      text       @8
    Provider     text       @16
    Subject      text       @24
    Body         text       @32
    TemplateID   text       @40
    TemplateVars bytes      @48
    Event        text       @56
    Sync         text       @64
}

struct notifyStored {
    Provider text @0
    Key      text @8
    Stored   bool @16
}

interface notify {
    # Removes one of your org's notify provider credentials.
    # The value is forgotten in KMS. A provider missing a key it cannot send
    # without is no longer picked for its channel, and a send that pins it fails
    # with the key it lacks. Removing a key that is not set succeeds, since what was
    # asked for is already true. Org admin only.
    delete_notify_credentials_by_provider_by_key(req: notifyCredentialRef)
    # Reports that the notify send surface is mounted.
    # It is a pure liveness probe: it answers 200 whenever this subsystem is mounted
    # and checks nothing downstream, so an "ok" here says the routes are reachable, not
    # that any provider credential is configured. The body is notifyd's verbatim, so
    # probes and clients that keyed on the standalone service keep working unchanged.
    get_notify_health() returns (rep: notifyHealth)
    # Delivers one transactional message by email or SMS through the caller
    # org's own provider credential.
    # The channel comes from the body — sms or email — and the provider credential is
    # read from KMS at orgs/<org>/notify/<service>/<key>, never from the environment.
    # The org is the validated principal's, never a client-supplied value, so a caller
    # can only ever send as their own tenant; an unauthenticated caller gets 401.
    # Naming no provider picks the one whose credentials are actually configured
    # (Twilio, then Plivo for SMS; Twilio Email, then SMTP for email) and fails closed
    # when none is. Delivery is synchronous and per recipient: one recipient answers
    # the bare {message_id,status} outcome, several answer the {items:[…]} envelope. A
    # terminal provider failure is a 200 whose status is failed with the reason in
    # error, never a transport error. sync=true is REQUIRED — an async dispatch
    # answers 503, because the queue plane that would run it is owned elsewhere. The
    # message body wins verbatim when present; otherwise template_id (or the event
    # name) selects a built-in template rendered against template_vars.
    post_notify_send(req: notifySend) returns (rep: notifyDelivery)
    # Delivers one transactional email through the caller org's own
    # provider credential.
    # It is the channel-pinned form of the generic send: identical in every respect
    # except that the channel is fixed to email, OVERRIDING whatever the body names —
    # so a body that says sms still goes out as mail. The provider is the org's own
    # email credential from KMS (Twilio Email, then SMTP), resolved for the validated
    # principal's org; an unauthenticated caller gets 401. Subject is carried on the
    # email channel only.
    post_notify_send_email(req: notifySend) returns (rep: notifyDelivery)
    # Delivers one transactional SMS through the caller org's own provider
    # credential.
    # It is the channel-pinned form of the generic send: identical in every respect
    # except that the channel is fixed to sms, OVERRIDING whatever the body names —
    # so a body that says email still goes out as a text message. The provider is the
    # org's own SMS credential from KMS (Twilio, then Plivo), resolved for the
    # validated principal's org; an unauthenticated caller gets 401.
    post_notify_send_sms(req: notifySend) returns (rep: notifyDelivery)
    # Sets one of your org's notify provider credentials.
    # The value is sealed in KMS under your org and read by notify alone, at the
    # moment it sends; no route answers it back, this one included. Setting a key
    # that is already set replaces it, which is how a credential is rotated, and the
    # next send uses the new value. The key must be one the provider reads — Twilio's
    # account-sid, auth-token and from-number, say — and anything else is a 400
    # naming the keys it does read. An unknown provider is a 404.
    # Org admin only: a credential set here is what every message the org sends
    # goes out with.
    put_notify_credentials_by_provider_by_key(req: notifyCredential) returns (rep: notifyStored)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   notifyDelivery.Items  notify.notifyOutcome (list element)
