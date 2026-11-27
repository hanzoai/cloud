// Package erp declares the ERPNext-core business model as DocType fixtures on the
// framework engine (apps/framework). Like apps/cms, ERP is NOT a bespoke
// subsystem and mounts NO HTTP surface of its own: an ERP master (Item, Customer,
// Account…) IS a framework DocType in module "erp", a transaction (Sales Order,
// Sales Invoice, Stock Entry…) IS a submittable framework document with child
// Tables, and posting IS a Go lifecycle hook (RegisterHook) that appends immutable
// ledger documents. All CRUD, permissions, tenant isolation, install, submit/cancel
// docstatus, and rendering are the framework's generic, already-live surface
// (/v1/framework/*) and the SAME generic @hanzo/ui DocType renderer that draws CMS.
// This package only DECLARES the fixtures and the native-Go business hooks and
// registers them with the engine at init — the ONE source of truth for the ERP
// model, per-org on Base/SQLite.
//
// # Naming (all names are slug-clean so the generic renderer can reach them)
//
//   - DocType names are slug-style and bare — "item", addressed as erp.item. The
//     module keeps them clear of the cms lane, and none carries a space
//     that the console's `/cloud` path filter would reject.
//   - Transactional documents use a SERIES autoname ("erp-so-.#####" → "erp-so-00001"),
//     always slug-clean and monotonic per org.
//   - Masters use a field autoname ("field:item_code" …); the console slugifies the
//     naming field on write (savePayload), so the document name is always URL-safe.
//   - Ledger entries are hash-named (system-posted, never addressed by a human key).
package erp
