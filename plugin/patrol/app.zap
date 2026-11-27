# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package patrol

struct patrolAlarmIn {
    Ref     text  @0
    Account text  @8
    Site    text  @16
    Zone    text  @24
    Trigger text  @32
    Source  text  @40
    Raw     bytes @48
}

struct patrolAlarmOut {
    Data bytes @0
}

struct patrolCameraList {
    Data list<bytes> @0
}

struct patrolCameraQuery {
    Site text @0
}

struct patrolCameraRef {
    Name text @0
}

struct patrolCheckpointOut {
    Data bytes @0
}

struct patrolConfirmIn {
    Name   text @0
    Method text @8
    Lat    f64  @16
    Lon    f64  @24
    Photo  text @32
}

struct patrolEventIn {
    Level  text @0
    Title  text @8
    Detail text @16
    Site   text @24
    Unit   text @32
    Source text @40
}

struct patrolEventList {
    Data list<bytes> @0
}

struct patrolEventOut {
    Data bytes @0
}

struct patrolEventQuery {
    Level text @0
    Limit i64  @8
}

struct patrolFixIn {
    Name text @0
    Lat  f64  @8
    Lon  f64  @16
    At   text @24
}

struct patrolFixOut {
    Data bytes @0
}

struct patrolIncidentAct {
    Name    text @0
    Act     text @8
    Unit    text @16
    Outcome text @24
    False   bool @32
    Detail  text @40
}

struct patrolIncidentIn {
    Site    text @0
    Zone    text @8
    Ref     text @16
    Trigger text @24
}

struct patrolIncidentList {
    Data list<bytes> @0
}

struct patrolIncidentOut {
    Data bytes @0
}

struct patrolIncidentQuery {
    Open  i64 @0
    Limit i64 @8
}

struct patrolIncidentRef {
    Name text @0
}

struct patrolKeyList {
    Data list<bytes> @0
}

struct patrolKeyOut {
    Data bytes @0
}

struct patrolKeyRef {
    Name text @0
}

struct patrolMoveIn {
    Name   text @0
    Act    text @8
    Holder text @16
    Job    text @24
}

struct patrolNearList {
    Data list<bytes> @0
}

struct patrolNearQuery {
    Site  text @0
    Limit i64  @8
}

struct patrolNoteIn {
    Subject text @0
    Body    text @8
}

struct patrolNoteList {
    Data list<bytes> @0
}

struct patrolNoteOut {
    Data bytes @0
}

struct patrolNoteQuery {
    Subject text @0
}

struct patrolReportIn {
    Kind     text @0
    From     text @8
    To       text @16
    Incident text @24
    Client   text @32
}

struct patrolReportOut {
    Data bytes @0
}

struct patrolSiteEdit {
    Name     text @0
    Category text @8
    Town     text @16
    Eircode  text @24
    Tier     text @32
    SLA      i64  @40
    Sector   text @48
    Contact  text @56
    Status   text @64
    Notes    text @72
}

struct patrolSiteList {
    Data list<bytes> @0
}

struct patrolSiteOut {
    Data bytes @0
}

struct patrolSiteQuery {
    Limit  i64  @0
    Status text @8
}

struct patrolSiteRef {
    Name text @0
}

struct patrolSnapshotOut {
    Data bytes @0
}

struct patrolSnapshotQuery {
    Events i64 @0
}

struct patrolStreamOut {
    Data bytes @0
}

struct patrolTenantOut {
    Data bytes @0
}

struct patrolTenantRef {
    Org text @0
}

struct patrolTicket {
    Name    text @0
    Ticket  text @8
    Expires text @16
}

struct patrolTicketOut {
    Data bytes @0
}

struct patrolTourList {
    Data list<bytes> @0
}

struct patrolTourQuery {
    Date text @0
}

struct patrolUnitList {
    Data list<bytes> @0
}

struct patrolUnitOut {
    Data bytes @0
}

struct patrolUnitStateIn {
    Name  text @0
    State text @8
}

interface patrol {
    # Lists the cameras a caller may OPEN. A row somebody can never reach is
    # a catalogue of a customer's premises and nothing else, so the admission the
    # stream enforces is asked here too rather than left to differ from it.
    # The stream credential is not part of a camera row — it is a KMS path the
    # document store never holds — so what comes back says where a camera is and
    # never how to reach it.
    get_patrol_camera(req: patrolCameraQuery) returns (rep: patrolCameraList)
    # Mints a short-lived way in to one camera.
    # The credential is kept in KMS, at the one place a camera's credential is kept
    # — the org's own patrol namespace, named for the camera — and what comes back
    # is minted from it rather than being it: the camera's address with the user and
    # password removed, bounded by an expiry, and signed so that whatever opens the
    # camera can tell a live ticket from a made-up one. The caller receives
    # something that opens one camera for five minutes and says nothing about how.
    # A camera whose row points anywhere but its own place, and one with no
    # credential registered, is 404.
    get_patrol_camera_by_name_stream(req: patrolCameraRef) returns (rep: patrolStreamOut)
    # Redeems a minted ticket for what opens the camera.
    # The mint is the operation's — staff only — and it hands back the camera's
    # address with the credential stripped out of it. This is where the holder of
    # that address gets what actually dials the camera, and it asks for no
    # credential of its own: whatever opens a camera is a relay, not a member of the
    # operation, and a check only the issuer could perform checks nothing.
    # The ticket IS the authority. It names one camera, it covers one moment, and it
    # is tagged with a key nobody but this deployment holds, so holding a live one is
    # the right to open that camera until it runs out — which is the whole of what
    # was issued, and the reason issuing one is staff-only and five minutes long.
    # A tag that does not verify is 404 — the route is no oracle on which tenants or
    # cameras exist, or which tickets were ever issued — and one that verifies but
    # has passed its moment is 410, because that ticket was real and is over.
    get_patrol_camera_by_name_stream_by_ticket(req: patrolTicket) returns (rep: patrolTicketOut)
    # Lists the feed, newest first. It is the same rows the live stream
    # carries, read back for a screen that has just opened — so the window is the
    # newest rows, or a screen that reloads reads a feed that stopped on the day the
    # tenant was installed.
    get_patrol_event(req: patrolEventQuery) returns (rep: patrolEventList)
    # Lists responses. ?open=1 narrows it to the ones still running.
    get_patrol_incident(req: patrolIncidentQuery) returns (rep: patrolIncidentList)
    # Returns one response whole: the incident, every act performed on it,
    # and the SLA clocks those acts settled. The clocks are derived from the stamps
    # and stored nowhere.
    get_patrol_incident_by_name(req: patrolIncidentRef) returns (rep: patrolIncidentOut)
    # Lists the key register. Neither the officer nor the client reaches it:
    # patrol.key is its own DocType and its permissions name neither, so there is no
    # key row for either to read.
    get_patrol_key() returns (rep: patrolKeyList)
    # Returns one key set from the register.
    get_patrol_key_by_name(req: patrolKeyRef) returns (rep: patrolKeyOut)
    # Lists internal remarks. The client never reaches them: patrol.note is
    # its own DocType and its permissions do not name the client, so there is no
    # note row for a customer to read.
    get_patrol_note(req: patrolNoteQuery) returns (rep: patrolNoteList)
    # Lists the estate. A client sees only the sites their own customer
    # record owns, filtered from their role rather than from anything they sent.
    get_patrol_site(req: patrolSiteQuery) returns (rep: patrolSiteList)
    # Returns one site whole: the register row plus the panel's zones and the
    # standing hazards, which ride inside the document. A client asking for a site
    # that is not theirs is 404.
    get_patrol_site_by_name(req: patrolSiteRef) returns (rep: patrolSiteOut)
    # Returns the whole picture in one read: sites, units with their
    # position trails, today's tours and checkpoints, the key register, the recent
    # feed and the open incident. It replaces the five separate list calls the
    # screen used to make. A client sees only their own sites, and the key register
    # is empty for anyone whose roles do not reach it.
    get_patrol_snapshot(req: patrolSnapshotQuery) returns (rep: patrolSnapshotOut)
    # Returns one org's public face — brand, company, operations, features
    # and terminology. It is the one anonymous route here: the front end loads it
    # before anyone signs in. It carries no contact list and nothing operational,
    # and an org with no tenant document is 404.
    # The org is a name a stranger supplied, so the read asks whether the deployment
    # already holds that tenant before it opens anything. Resolving a store CREATES
    # one, and a caller naming a fresh org on every request would leave a database
    # per name they invented.
    get_patrol_tenant_by_org(req: patrolTenantRef) returns (rep: patrolTenantOut)
    # Lists patrol rounds with their checkpoints — the proof-of-presence
    # record. ?date=today narrows it to the current day.
    get_patrol_tour(req: patrolTourQuery) returns (rep: patrolTourList)
    # Lists the fleet with each unit's position trail, which is what the map
    # draws. It is the operation's own: a unit names no site, so a customer reads
    # none of it — the officer crewing one is the company's staff, and the trail is
    # where that person has been all shift.
    get_patrol_unit() returns (rep: patrolUnitList)
    # Answers which units can reach a site soonest. Distance is a great-circle
    # haversine over the units' last reported positions, and an available unit sorts
    # ahead of a busy one at any distance — a controller asking for the nearest unit
    # is asking for the nearest free one.
    get_patrol_unit_near(req: patrolNearQuery) returns (rep: patrolNearList)
    # Takes one activation. It records the activation whatever happens, then
    # matches a site — the one the caller named, else the one whose account number
    # reported — and on a match opens an incident in state received with that site's
    # tier and SLA, puts the site into alarm, raises a red feed row and tells the
    # controllers. An activation that matches no site is still recorded, raises an
    # amber row, and answers 202: an activation is never dropped.
    # It admits the receiving centre's own credential and a controller keying one in
    # by hand. The centre holds the `centre` role its tenant granted it, which posts
    # an activation and reads nothing, like every other identity here.
    # The centre's own reference identifies the activation while the response it
    # opened is still running, so a redelivery is answered with that response rather
    # than dispatching a second unit. Centres recycle references, so once the
    # response has closed the same one is the next activation.
    post_patrol_alarm(req: patrolAlarmIn) returns (rep: patrolAlarmOut)
    # Writes one feed row by hand and pushes it to every screen watching.
    # It is how a controller puts something on the record that no automatic write
    # would have produced.
    post_patrol_event(req: patrolEventIn) returns (rep: patrolEventOut)
    # Records a response by hand, in state received and carrying the
    # site's tier and SLA. It is what a controller calls when an activation arrives
    # by phone rather than over the intake.
    post_patrol_incident(req: patrolIncidentIn) returns (rep: patrolIncidentOut)
    # Performs one act on an incident: it stamps the act's time, moves
    # the state, appends the act to the trail, and runs what the act sets in motion —
    # a unit assigned, a site secured, a report filed, a controller told.
    # The states run forward only. An act from a state it does not leave is 409
    # naming the state the incident is in, and a caller without the act's roles is
    # refused.
    post_patrol_incident_by_name_step(req: patrolIncidentAct) returns (rep: patrolIncidentOut)
    # Records a key set leaving, returning, or being counted, and moves the
    # register row to match. The register says where a set is now and the movements
    # say how it got there, so a movement is written once and never edited.
    # Who signed for the move is the caller, stamped from the request. A body that
    # named the signer would make the chain of custody a value the signer chose.
    post_patrol_key_by_name_move(req: patrolMoveIn) returns (rep: patrolKeyOut)
    # Records an internal remark, stamped with who wrote it.
    post_patrol_note(req: patrolNoteIn) returns (rep: patrolNoteOut)
    # Records presence at a checkpoint: who confirmed it, the method, where
    # they stood, and the photo key if one was taken. A confirmation after the
    # window shuts is recorded late rather than refused — the visit happened, and a
    # report that hides it is worth nothing.
    # An officer confirms their own round and no other, by the same rule a position
    # report and every incident act are held to: the checkpoint belongs to a tour,
    # the tour names a unit, and the unit names its officer. The record carries the
    # actor, so it says who claimed the visit and not merely that somebody did.
    post_patrol_point_by_name_confirm(req: patrolConfirmIn) returns (rep: patrolCheckpointOut)
    # Composes and files a report from one estate read, so the same
    # figures read the same a month later. The text is stored on the document; the
    # PDF is produced on read at /v1/patrol/report/{name}.
    # A client report naming a customer is an account of that customer's estate and
    # is theirs to download; naming none covers every customer, which is an account
    # of the operation.
    post_patrol_report(req: patrolReportIn) returns (rep: patrolReportOut)
    # Records where a unit is. It appends the position to the unit's trail,
    # moves the unit's own coordinates and last-seen stamp, and publishes one frame
    # to every screen watching. An officer may report only their own unit; the
    # control room may report any.
    post_patrol_unit_by_name_fix(req: patrolFixIn) returns (rep: patrolFixOut)
    # Moves a unit between states — on shift, on a break, off, or in
    # trouble. An officer may move only their own unit; the control room may move
    # any.
    post_patrol_unit_by_name_state(req: patrolUnitStateIn) returns (rep: patrolUnitOut)
    # Changes a site's contract and brief fields. It leaves absent fields
    # as they stand, and it never moves the account number, the coordinates or the
    # customer — those are register facts, changed through the framework surface.
    put_patrol_site_by_name(req: patrolSiteEdit) returns (rep: patrolSiteOut)
}

# ---------------------------------------------------------------------
# 27 op(s) here. What follows is what this schema does not carry.
#
# opaque (23) — crosses, arrives without its name:
#   patrolAlarmOut.Data  patrol.patrolActivation
#   patrolCameraList.Data  patrol.patrolCamera (list element)
#   patrolCheckpointOut.Data  patrol.patrolCheckpoint
#   patrolEventList.Data  patrol.patrolEvent (list element)
#   patrolEventOut.Data  patrol.patrolEvent
#   patrolFixOut.Data  patrol.patrolFixed
#   patrolIncidentList.Data  patrol.patrolIncident (list element)
#   patrolIncidentOut.Data  patrol.patrolIncident
#   patrolKeyList.Data  patrol.patrolKey (list element)
#   patrolKeyOut.Data  patrol.patrolKey
#   patrolNearList.Data  patrol.patrolNearest (list element)
#   patrolNoteList.Data  patrol.patrolNote (list element)
#   patrolNoteOut.Data  patrol.patrolNote
#   patrolReportOut.Data  patrol.patrolReport
#   patrolSiteList.Data  patrol.patrolSite (list element)
#   patrolSiteOut.Data  patrol.patrolSite
#   patrolSnapshotOut.Data  patrol.patrolEstate
#   patrolStreamOut.Data  patrol.patrolStream
#   patrolTenantOut.Data  patrol.patrolTenant
#   patrolTicketOut.Data  patrol.patrolStream
#   patrolTourList.Data  patrol.patrolTour (list element)
#   patrolUnitList.Data  patrol.patrolUnit (list element)
#   patrolUnitOut.Data  patrol.patrolUnit
