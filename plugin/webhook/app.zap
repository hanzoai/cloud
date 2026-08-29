# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package webhook

struct Endpoint {
    ID           text       @0
    Org          text       @8
    URL          text       @16
    Events       list<text> @24
    Secret       text       @32
    Status       text       @40
    Description  text       @48
    CreatedAt    text       @56
    UpdatedAt    text       @64
    Deliveries7d i64        @72
    Failures7d   i64        @80
}

struct createEndpointIn {
    URL         text       @0
    Events      list<text> @8
    Status      text       @16
    Description text       @24
}

struct deliveryList {
    Data list<bytes> @0
}

struct endpointList {
    Data list<bytes> @0
}

struct endpointRef {
    ID text @0
}

struct listDeliveriesIn {
    ID     text @0
    Limit  i64  @8
    Status text @16
}

struct testResult {
    Delivered  bool @0
    HTTPStatus i64  @8
    DurationMs i64  @16
    Error      text @24
}

struct updateEndpointIn {
    ID          text       @0
    URL         text       @8
    Events      list<text> @16
    Status      text       @24
    Description text       @32
}

interface webhook {
    # Removes one of the caller org's webhook endpoints and answers
    # 204 with no body. Delivery stops immediately and the endpoint's signing secret
    # is gone with it; its recorded delivery history goes too. An id another org owns
    # reads as not found.
    delete_webhook_by_id(req: endpointRef)
    # Returns every webhook endpoint the caller's org has registered,
    # newest first, each with its 7-day delivery and failure counts. Signing secrets
    # are redacted here — a secret leaves the server only on create and on rotate.
    # The listing is physically org-scoped, so another tenant's endpoints are not
    # reachable from this route at all.
    get_webhook() returns (rep: endpointList)
    # Returns one of the caller org's webhook endpoints with its 7-day
    # delivery and failure counts, signing secret redacted. An id another org owns
    # reads as not found, so the response cannot confirm that it exists.
    get_webhook_by_id(req: endpointRef) returns (rep: Endpoint)
    # Returns one endpoint's per-attempt delivery log, newest first —
    # the record of what was sent, what the subscriber answered, and how long it
    # took. One event that retried three times appears as three rows sharing a
    # delivery id. It is org-scoped exactly like every other route here: the endpoint
    # lookup only ever finds THIS org's endpoint, so another org's id is a 404 and
    # never a window onto its logs.
    get_webhook_by_id_deliveries(req: listDeliveriesIn) returns (rep: deliveryList)
    # Registers a new webhook subscription for the caller's org and
    # answers 201 with the endpoint INCLUDING its freshly minted signing secret.
    # This is one of only two responses that ever carry that secret (the other is
    # rotate) — store it now, because no later read returns it. The org is stamped by
    # the server from the validated principal, so a body can never register an
    # endpoint in another tenant.
    post_webhook(req: createEndpointIn) returns (rep: Endpoint)
    # Mints a NEW HMAC signing secret for the endpoint and answers the
    # endpoint WITH it — the only other response besides create that ever carries a
    # secret. The old secret stops working the instant this returns: every subsequent
    # delivery signs with the new one, with no overlap window. Call it when the
    # subscriber is ready to swap the value on its side, not before.
    post_webhook_by_id_secret(req: endpointRef) returns (rep: Endpoint)
    # Sends ONE signed test event to the endpoint right now and answers
    # the outcome inline, so the console can show whether the subscriber is reachable
    # without waiting for real traffic. It takes the same attempt path the bus
    # dispatcher takes — one attempt, 10s timeout, no retry ladder — and records the
    # result in the endpoint's delivery log. It works on a DISABLED endpoint too:
    # validating one you have paused is the whole point.
    post_webhook_by_id_test(req: endpointRef) returns (rep: testResult)
    # Replaces the editable fields of one of the caller org's
    # endpoints — url, events, status and description — and answers the stored row
    # with its secret redacted. It is a full replace, not a patch: an omitted field
    # is written as its empty value, and an omitted or empty events list resubscribes
    # the endpoint to EVERY event. The signing secret and the creation time are
    # immutable here; rotate the secret with POST /v1/webhook/{id}/secret.
    put_webhook_by_id(req: updateEndpointIn) returns (rep: Endpoint)
}

# ---------------------------------------------------------------------
# 8 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   deliveryList.Data  webhook.DeliveryRow (list element)
#   endpointList.Data  webhook.Endpoint (list element)
