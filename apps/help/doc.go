// Package help is a support desk: customers file tickets, your team answers them.
//
// The Hanzo Support product: the Frappe-Helpdesk model rebuilt as DocType
// fixtures on the native framework engine (apps/framework), plus a thin
// /v1/help subsystem (subsystem.go) for the one plane the generic,
// secure-by-default framework surface deliberately cannot serve — the PUBLIC
// help center.
//
// # Two planes, one engine
//
// A ticket IS a framework document in module "help"; its lifecycle IS a status
// field; an agent / team / SLA / canned response / KB article / conversation
// message are framework documents. So the AGENT plane — triage a ticket, author an
// article, read or post a conversation thread — is the framework's generic,
// already-live, role-gated surface (/v1/framework/hd-*) and the SAME generic
// @hanzo/ui DocType renderer that draws CMS and ERP. That plane needs ZERO code
// here beyond the fixtures.
//
// What the generic surface CANNOT do is serve help.hanzo.ai's PUBLIC face: framework
// is secure-by-default (every read/write needs a validated principal AND a role), so
// there is no anonymous "read the public knowledge base" or "a customer files a
// ticket" path. subsystem.go adds exactly that — the public plane — the same way the
// knowledge lane adds /v1/knowledge for retrieval the generic surface lacks. It owns NO
// store: every read/write delegates to the framework in-process API (Ingest/Get/
// Search), so there is ONE storage engine and no duplicated CRUD.
//
// # Migration
//
// This is the native-Go endgame for the Frappe Helpdesk (Vue frontend + Python
// Frappe backend on a Werkzeug dev server): the model moves onto the native DocType
// engine (no Frappe, no Python, no Werkzeug), the product surface is native Go, and
// the whole desk ships in the ONE cloud binary on Base.
//
// Names are slug-style and bare — "ticket", addressed as help.ticket. The module
// keeps the lane clear of the CMS, ERP and KB lanes without any of them spelling
// the others out of the way, and nothing carries a space the console's `/cloud`
// path filter would reject. Tickets use a monotonic series name ("hd-tkt-.#####");
// the masters use a field autoname the console slugifies on write.
package help
