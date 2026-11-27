# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package destination

struct destinationDisconnected {
    Disconnected bool @0
}

struct destinationRef {
    Platform text @0
}

struct destinationTest {
    Error   list<text> @0
    Message list<text> @8
    OK      bool       @16
    Sent    list<i64>  @24
}

interface destination {
    # Forgets a destination for the caller's org: every credential held in
    # KMS, then the stored config. Idempotent, and it requires org admin.
    delete_destination_by_platform(req: destinationRef) returns (rep: destinationDisconnected)
    # Sends ONE synthetic conversion through the connected destination end to
    # end and reports what the platform said. Where the platform can check one without
    # recording it, it is a lead and nothing is kept: GA4 answers from its validation
    # server, Google Ads validates without importing, and Meta files it under the
    # connection's Test Event Code (and is refused without one). Anywhere else it is a
    # page view. A send the platform refuses is reported as data — {"ok": false,
    # "error": …} at 200 — so the console shows the platform's own words rather than
    # an error about Hanzo. It requires org admin.
    post_destination_by_platform_test(req: destinationRef) returns (rep: destinationTest)
}

# ---------------------------------------------------------------------
# 2 op(s) here. What follows is what this schema does not carry.
#
# blocked (2) — the op is absent; the field has no wire form:
#   get_destination  destinationList.Destinations  []destination.DestinationStatus  (no wire form)
#   get_destination_by_platform  DestinationStatus.Config  destination.Config  (map)
