# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package admission

struct waitlistModeView {
    Host         text @0
    Service      text @8
    WaitlistMode bool @16
    Known        bool @17
}

struct waitlistQuery {
    Host text @0
}

interface admission {
    # Reports whether ONE host is currently gated by the launch waitlist.
    # It resolves the host to the service that governs it and reads that service's
    # waitlist switch, so a guard sitting in front of a hosted surface can decide in one
    # call whether to show the waitlist or the product. It answers for the ONE host
    # asked about and never enumerates the registry, which is why it needs no
    # credential. It FAILS OPEN: an unregistered host, an unmounted registry and a store
    # fault all answer known=false with mode=false, so a request is never gated pre-boot
    # or on a registry fault.
    get_admission_waitlist(req: waitlistQuery) returns (rep: waitlistModeView)
}
