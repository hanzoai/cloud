// Package erp declares the ERPNext-core business model as DocType fixtures on the
// framework engine (clients/framework). Like clients/cms, ERP is NOT a bespoke
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
// This is the second app lane on the framework (CMS was the first), proving the
// thesis: one engine + one renderer renders every business app. It reuses the
// generic install path (POST /v1/framework/modules/erp/install) and the generic
// renderer verbatim; the only ERP-specific code is data (fixtures) and behavior
// (hooks) — zero UI, zero forked engine.
//
// # Naming (all names are slug-clean so the generic renderer can reach them)
//
//   - DocType names are slug-style with an "erp-" prefix, so they never collide
//     with the CMS lane's names (Author/Media/Page/…) or the CRM's, and never carry
//     a space that the console's `/cloud` path filter would reject.
//   - Transactional documents use a SERIES autoname ("erp-so-.#####" → "erp-so-00001"),
//     always slug-clean and monotonic per org.
//   - Masters use a field autoname ("field:item_code" …); the console slugifies the
//     naming field on write (savePayload), so the document name is always URL-safe.
//   - Ledger entries are hash-named (system-posted, never addressed by a human key).
package erp

import "github.com/hanzoai/cloud/clients/framework"

// Module is the framework module tag every ERP DocType carries. The console's ERP
// surface is the generic DocType renderer scoped to this module.
const Module = "erp"

// RoleErpUser is the operational ERP role the master/transaction DocTypes grant
// read/write/create/delete/submit/cancel. The org owner (System Manager, seeded
// trust-on-first-use) assigns it via /v1/framework/roles; a role-less member stays
// denied (secure by default). The immutable ledgers (GL, stock) are System-Manager
// read-only — accounting truth is widened to other roles only by explicit grant.
const RoleErpUser = "Erp User"

// Child DocType names — the line-item schemas embedded in their parents as Table
// fields. They are real DocTypes (the engine validates each row against them) but
// are never addressed standalone; the parent owns their rows atomically.
const (
	dtSalesOrderItem    = "erp-sales-order-item"
	dtSalesInvoiceItem  = "erp-sales-invoice-item"
	dtPurchaseOrderItem = "erp-purchase-order-item"
	dtStockEntryItem    = "erp-stock-entry-item"
	dtJournalAccount    = "erp-journal-entry-account"
)

// Parent / master / ledger DocType names.
const (
	dtItem          = "erp-item"
	dtWarehouse     = "erp-warehouse"
	dtCustomer      = "erp-customer"
	dtSupplier      = "erp-supplier"
	dtAccount       = "erp-account"
	dtDepartment    = "erp-department"
	dtEmployee      = "erp-employee"
	dtSalesOrder    = "erp-sales-order"
	dtSalesInvoice  = "erp-sales-invoice"
	dtPurchaseOrder = "erp-purchase-order"
	dtStockEntry    = "erp-stock-entry"
	dtJournalEntry  = "erp-journal-entry"
	dtPaymentEntry  = "erp-payment-entry"
	dtGLEntry       = "erp-gl-entry"
	dtStockLedger   = "erp-stock-ledger-entry"
)

// init registers the ERP content model AND its native-Go business hooks with the
// framework. Installing the "erp" module (POST /v1/framework/modules/erp/install)
// ensures these DocTypes exist in the caller's org; the hooks (registered by
// doctype name, applied per-org) then run on the generic submit lifecycle.
func init() {
	framework.RegisterModule(Module, DocTypes())
	registerHooks()
}

// DocTypes returns the canonical ERP model — the default DocTypes an org gets when
// it installs the ERP lane. Order is child-schemas first for readability, but the
// engine resolves Link/Table targets at document write (not at define), so the set
// is order-independent; install creates them all before any document is written.
func DocTypes() []framework.DocType {
	return []framework.DocType{
		// Line-item child schemas.
		salesOrderItem(), salesInvoiceItem(), purchaseOrderItem(), stockEntryItem(), journalEntryAccount(),
		// Masters.
		item(), warehouse(), customer(), supplier(), account(), department(), employee(),
		// Submittable transactions.
		salesOrder(), salesInvoice(), purchaseOrder(), stockEntry(), journalEntry(), paymentEntry(),
		// System-posted immutable ledgers (written by hooks, read-only to humans).
		glEntry(), stockLedgerEntry(),
	}
}

// ---- masters ----

// item is the stock/service item master. Named by item_code (the console slugifies
// it on write, so the document name is URL-safe); item_name is the human title.
func item() framework.DocType {
	return framework.DocType{
		Name: dtItem, Module: Module, Autoname: "field:item_code", TitleField: "item_name",
		Fields: []framework.DocField{
			{Fieldname: "item_code", Fieldtype: framework.FieldData, Label: "Item Code", Reqd: true, InListView: true},
			{Fieldname: "item_name", Fieldtype: framework.FieldData, Label: "Item Name", Reqd: true, InListView: true},
			{Fieldname: "item_group", Fieldtype: framework.FieldData, Label: "Item Group", InListView: true},
			{Fieldname: "stock_uom", Fieldtype: framework.FieldData, Label: "Unit", Default: "Nos"},
			{Fieldname: "standard_rate", Fieldtype: framework.FieldCurrency, Label: "Standard Rate", InListView: true},
			{Fieldname: "is_stock_item", Fieldtype: framework.FieldCheck, Label: "Maintain Stock", Default: "1"},
			{Fieldname: "description", Fieldtype: framework.FieldText, Label: "Description"},
		},
		Perms: erpPerms(),
	}
}

// warehouse is a stock location — the source/target a stock movement debits/credits.
func warehouse() framework.DocType {
	return framework.DocType{
		Name: dtWarehouse, Module: Module, Autoname: "field:warehouse_name", TitleField: "warehouse_name",
		Fields: []framework.DocField{
			{Fieldname: "warehouse_name", Fieldtype: framework.FieldData, Label: "Warehouse Name", Reqd: true, InListView: true},
			{Fieldname: "is_group", Fieldtype: framework.FieldCheck, Label: "Group Node"},
			{Fieldname: "address", Fieldtype: framework.FieldSmall, Label: "Address"},
		},
		Perms: erpPerms(),
	}
}

// customer is the sell-side party master (the Sales Order/Invoice Link target).
func customer() framework.DocType {
	return framework.DocType{
		Name: dtCustomer, Module: Module, Autoname: "field:customer_name", TitleField: "customer_name",
		Fields: append(partyFields("customer_name", "Customer Name", "customer_group", "Customer Group"),
			framework.DocField{Fieldname: "territory", Fieldtype: framework.FieldData, Label: "Territory"}),
		Perms: erpPerms(),
	}
}

// supplier is the buy-side party master (the Purchase Order Link target).
func supplier() framework.DocType {
	return framework.DocType{
		Name: dtSupplier, Module: Module, Autoname: "field:supplier_name", TitleField: "supplier_name",
		Fields: partyFields("supplier_name", "Supplier Name", "supplier_group", "Supplier Group"),
		Perms:  erpPerms(),
	}
}

// account is a chart-of-accounts node — the account_type classifies it for the
// ledger. Kept flat (no forced parent Link) so a fresh org needs no COA import.
func account() framework.DocType {
	return framework.DocType{
		Name: dtAccount, Module: Module, Autoname: "field:account_name", TitleField: "account_name",
		Fields: []framework.DocField{
			{Fieldname: "account_name", Fieldtype: framework.FieldData, Label: "Account Name", Reqd: true, InListView: true},
			{Fieldname: "account_number", Fieldtype: framework.FieldData, Label: "Account Number", InListView: true},
			{Fieldname: "account_type", Fieldtype: framework.FieldSelect, Label: "Type", Options: "Asset\nLiability\nEquity\nIncome\nExpense", InListView: true},
			{Fieldname: "currency", Fieldtype: framework.FieldData, Label: "Currency", Default: "USD"},
			{Fieldname: "is_group", Fieldtype: framework.FieldCheck, Label: "Group Node"},
		},
		Perms: erpPerms(),
	}
}

// department is an HR org node.
func department() framework.DocType {
	return framework.DocType{
		Name: dtDepartment, Module: Module, Autoname: "field:department_name", TitleField: "department_name",
		Fields: []framework.DocField{
			{Fieldname: "department_name", Fieldtype: framework.FieldData, Label: "Department", Reqd: true, InListView: true},
			{Fieldname: "cost_center", Fieldtype: framework.FieldData, Label: "Cost Center", InListView: true},
		},
		Perms: erpPerms(),
	}
}

// employee is the HR master. Named by employee_id; department is an in-lane Link.
func employee() framework.DocType {
	return framework.DocType{
		Name: dtEmployee, Module: Module, Autoname: "field:employee_id", TitleField: "employee_name",
		Fields: []framework.DocField{
			{Fieldname: "employee_id", Fieldtype: framework.FieldData, Label: "Employee ID", Reqd: true, InListView: true},
			{Fieldname: "employee_name", Fieldtype: framework.FieldData, Label: "Full Name", Reqd: true, InListView: true},
			{Fieldname: "department", Fieldtype: framework.FieldLink, Label: "Department", Options: dtDepartment, InListView: true},
			{Fieldname: "designation", Fieldtype: framework.FieldData, Label: "Designation"},
			{Fieldname: "date_of_joining", Fieldtype: framework.FieldDate, Label: "Date of Joining"},
			{Fieldname: "status", Fieldtype: framework.FieldSelect, Label: "Status", Options: "Active\nInactive", Default: "Active", InListView: true},
			{Fieldname: "email", Fieldtype: framework.FieldData, Label: "Company Email"},
		},
		Perms: erpPerms(),
	}
}

// ---- submittable transactions ----

// salesOrder is a confirmed customer order. before_save computes line amounts and
// grand_total; on_submit gates a non-empty, positive order. docstatus is the
// lifecycle (0 draft → 1 submitted → 2 cancelled).
func salesOrder() framework.DocType {
	return framework.DocType{
		Name: dtSalesOrder, Module: Module, Autoname: "erp-so-.#####", TitleField: "customer", IsSubmittable: true,
		Fields: []framework.DocField{
			{Fieldname: "customer", Fieldtype: framework.FieldLink, Label: "Customer", Options: dtCustomer, Reqd: true, InListView: true},
			{Fieldname: "order_date", Fieldtype: framework.FieldDate, Label: "Order Date", InListView: true},
			{Fieldname: "delivery_date", Fieldtype: framework.FieldDate, Label: "Delivery Date"},
			{Fieldname: "items", Fieldtype: framework.FieldTable, Label: "Items", Options: dtSalesOrderItem},
			{Fieldname: "grand_total", Fieldtype: framework.FieldCurrency, Label: "Grand Total", ReadOnly: true, InListView: true},
		},
		Perms: erpPerms(),
	}
}

// salesInvoice is a submittable A/R invoice. on_submit posts a balanced GL pair
// (Dr receivable, Cr income) for grand_total — the "GL entry on invoice submit".
func salesInvoice() framework.DocType {
	return framework.DocType{
		Name: dtSalesInvoice, Module: Module, Autoname: "erp-sinv-.#####", TitleField: "customer", IsSubmittable: true,
		Fields: []framework.DocField{
			{Fieldname: "customer", Fieldtype: framework.FieldLink, Label: "Customer", Options: dtCustomer, Reqd: true, InListView: true},
			{Fieldname: "posting_date", Fieldtype: framework.FieldDate, Label: "Posting Date", InListView: true},
			{Fieldname: "items", Fieldtype: framework.FieldTable, Label: "Items", Options: dtSalesInvoiceItem},
			{Fieldname: "grand_total", Fieldtype: framework.FieldCurrency, Label: "Grand Total", ReadOnly: true, InListView: true},
			{Fieldname: "debit_to", Fieldtype: framework.FieldLink, Label: "Receivable Account", Options: dtAccount},
			{Fieldname: "income_account", Fieldtype: framework.FieldLink, Label: "Income Account", Options: dtAccount},
		},
		Perms: erpPerms(),
	}
}

// purchaseOrder is a submittable supplier order. before_save totals; on_submit gates.
func purchaseOrder() framework.DocType {
	return framework.DocType{
		Name: dtPurchaseOrder, Module: Module, Autoname: "erp-po-.#####", TitleField: "supplier", IsSubmittable: true,
		Fields: []framework.DocField{
			{Fieldname: "supplier", Fieldtype: framework.FieldLink, Label: "Supplier", Options: dtSupplier, Reqd: true, InListView: true},
			{Fieldname: "transaction_date", Fieldtype: framework.FieldDate, Label: "Date", InListView: true},
			{Fieldname: "items", Fieldtype: framework.FieldTable, Label: "Items", Options: dtPurchaseOrderItem},
			{Fieldname: "grand_total", Fieldtype: framework.FieldCurrency, Label: "Grand Total", ReadOnly: true, InListView: true},
		},
		Perms: erpPerms(),
	}
}

// stockEntry moves quantity between warehouses. on_submit appends an immutable
// stock-ledger-entry per movement (+qty at target, −qty at source) — the "stock
// update on submit". Race-free: the ledger is append-only, current stock is a SUM.
func stockEntry() framework.DocType {
	return framework.DocType{
		Name: dtStockEntry, Module: Module, Autoname: "erp-ste-.#####", TitleField: "stock_entry_type", IsSubmittable: true,
		Fields: []framework.DocField{
			{Fieldname: "stock_entry_type", Fieldtype: framework.FieldSelect, Label: "Type", Options: "Receipt\nIssue\nTransfer", Default: "Receipt", InListView: true},
			{Fieldname: "posting_date", Fieldtype: framework.FieldDate, Label: "Posting Date", InListView: true},
			{Fieldname: "items", Fieldtype: framework.FieldTable, Label: "Items", Options: dtStockEntryItem},
		},
		Perms: erpPerms(),
	}
}

// journalEntry is a manual double-entry voucher. before_save totals debits/credits;
// on_submit GATES debits == credits (> 0) then posts one GL entry per account row.
func journalEntry() framework.DocType {
	return framework.DocType{
		Name: dtJournalEntry, Module: Module, Autoname: "erp-je-.#####", TitleField: "user_remark", IsSubmittable: true,
		Fields: []framework.DocField{
			{Fieldname: "posting_date", Fieldtype: framework.FieldDate, Label: "Posting Date", InListView: true},
			{Fieldname: "accounts", Fieldtype: framework.FieldTable, Label: "Accounts", Options: dtJournalAccount},
			{Fieldname: "total_debit", Fieldtype: framework.FieldCurrency, Label: "Total Debit", ReadOnly: true, InListView: true},
			{Fieldname: "total_credit", Fieldtype: framework.FieldCurrency, Label: "Total Credit", ReadOnly: true, InListView: true},
			{Fieldname: "user_remark", Fieldtype: framework.FieldSmall, Label: "Remark"},
		},
		Perms: erpPerms(),
	}
}

// paymentEntry records a receipt/payment. on_submit gates a positive amount and
// posts a balanced GL pair (Cash vs the party account) — reusing the SAME postGL.
func paymentEntry() framework.DocType {
	return framework.DocType{
		Name: dtPaymentEntry, Module: Module, Autoname: "erp-pe-.#####", TitleField: "party", IsSubmittable: true,
		Fields: []framework.DocField{
			{Fieldname: "payment_type", Fieldtype: framework.FieldSelect, Label: "Type", Options: "Receive\nPay", Default: "Receive", InListView: true},
			{Fieldname: "party_type", Fieldtype: framework.FieldSelect, Label: "Party Type", Options: "Customer\nSupplier", Default: "Customer"},
			{Fieldname: "party", Fieldtype: framework.FieldData, Label: "Party", InListView: true},
			{Fieldname: "paid_amount", Fieldtype: framework.FieldCurrency, Label: "Amount", Reqd: true, InListView: true},
			{Fieldname: "posting_date", Fieldtype: framework.FieldDate, Label: "Posting Date", InListView: true},
			{Fieldname: "reference_no", Fieldtype: framework.FieldData, Label: "Reference No"},
		},
		Perms: erpPerms(),
	}
}

// ---- line-item child schemas ----

// salesOrderItem is one sales-order line: an item Link, quantity, unit rate, and a
// hook-computed amount (readOnly — before_save fills it, the client never sends it).
func salesOrderItem() framework.DocType    { return lineItem(dtSalesOrderItem) }
func salesInvoiceItem() framework.DocType  { return lineItem(dtSalesInvoiceItem) }
func purchaseOrderItem() framework.DocType { return lineItem(dtPurchaseOrderItem) }

// stockEntryItem is one stock movement line: an item, a signed quantity, and the
// source/target warehouses the qty flows between.
func stockEntryItem() framework.DocType {
	return framework.DocType{
		Name: dtStockEntryItem, Module: Module,
		Fields: []framework.DocField{
			{Fieldname: "item", Fieldtype: framework.FieldLink, Label: "Item", Options: dtItem, Reqd: true, InListView: true},
			{Fieldname: "qty", Fieldtype: framework.FieldFloat, Label: "Quantity", Reqd: true, Default: "1", InListView: true},
			{Fieldname: "source_warehouse", Fieldtype: framework.FieldLink, Label: "Source", Options: dtWarehouse, InListView: true},
			{Fieldname: "target_warehouse", Fieldtype: framework.FieldLink, Label: "Target", Options: dtWarehouse, InListView: true},
		},
		Perms: erpPerms(),
	}
}

// journalEntryAccount is one JE line: an account and its debit/credit.
func journalEntryAccount() framework.DocType {
	return framework.DocType{
		Name: dtJournalAccount, Module: Module,
		Fields: []framework.DocField{
			{Fieldname: "account", Fieldtype: framework.FieldLink, Label: "Account", Options: dtAccount, Reqd: true, InListView: true},
			{Fieldname: "debit", Fieldtype: framework.FieldCurrency, Label: "Debit", Default: "0", InListView: true},
			{Fieldname: "credit", Fieldtype: framework.FieldCurrency, Label: "Credit", Default: "0", InListView: true},
		},
		Perms: erpPerms(),
	}
}

// ---- immutable ledgers (written by hooks, read-only to humans) ----

// glEntry is a general-ledger posting: an account, a debit OR credit, and the
// source voucher. Written ONLY by the posting hooks (via the store, which bypasses
// perms); ledgerPerms makes it System-Manager read-only, so no human edits the
// accounting truth through the generic surface.
func glEntry() framework.DocType {
	return framework.DocType{
		Name: dtGLEntry, Module: Module, TitleField: "account",
		Fields: []framework.DocField{
			{Fieldname: "posting_date", Fieldtype: framework.FieldData, Label: "Posting Date", InListView: true},
			{Fieldname: "account", Fieldtype: framework.FieldData, Label: "Account", InListView: true},
			{Fieldname: "debit", Fieldtype: framework.FieldCurrency, Label: "Debit", InListView: true},
			{Fieldname: "credit", Fieldtype: framework.FieldCurrency, Label: "Credit", InListView: true},
			{Fieldname: "voucher_type", Fieldtype: framework.FieldData, Label: "Voucher Type", InListView: true},
			{Fieldname: "voucher_no", Fieldtype: framework.FieldData, Label: "Voucher No", InListView: true},
			{Fieldname: "remarks", Fieldtype: framework.FieldSmall, Label: "Remarks"},
		},
		Perms: ledgerPerms(),
	}
}

// stockLedgerEntry is one immutable stock movement: item, warehouse, signed qty,
// and the source voucher. Current stock for (item, warehouse) is SUM(qty).
func stockLedgerEntry() framework.DocType {
	return framework.DocType{
		Name: dtStockLedger, Module: Module, TitleField: "item",
		Fields: []framework.DocField{
			{Fieldname: "posting_date", Fieldtype: framework.FieldData, Label: "Posting Date", InListView: true},
			{Fieldname: "item", Fieldtype: framework.FieldData, Label: "Item", InListView: true},
			{Fieldname: "warehouse", Fieldtype: framework.FieldData, Label: "Warehouse", InListView: true},
			{Fieldname: "qty", Fieldtype: framework.FieldFloat, Label: "Quantity", InListView: true},
			{Fieldname: "voucher_type", Fieldtype: framework.FieldData, Label: "Voucher Type", InListView: true},
			{Fieldname: "voucher_no", Fieldtype: framework.FieldData, Label: "Voucher No", InListView: true},
		},
		Perms: ledgerPerms(),
	}
}

// ---- shared field / permission builders ----

// lineItem is the shared sales/purchase line schema (item, qty, rate, amount) —
// three DocTypes share it, so it is one builder, not three copies.
func lineItem(name string) framework.DocType {
	return framework.DocType{
		Name: name, Module: Module,
		Fields: []framework.DocField{
			{Fieldname: "item", Fieldtype: framework.FieldLink, Label: "Item", Options: dtItem, Reqd: true, InListView: true},
			{Fieldname: "qty", Fieldtype: framework.FieldFloat, Label: "Quantity", Reqd: true, Default: "1", InListView: true},
			{Fieldname: "rate", Fieldtype: framework.FieldCurrency, Label: "Rate", Reqd: true, InListView: true},
			{Fieldname: "amount", Fieldtype: framework.FieldCurrency, Label: "Amount", ReadOnly: true, InListView: true},
		},
		Perms: erpPerms(),
	}
}

// partyFields is the shared customer/supplier field set (name, group, contact).
func partyFields(nameField, nameLabel, groupField, groupLabel string) []framework.DocField {
	return []framework.DocField{
		{Fieldname: nameField, Fieldtype: framework.FieldData, Label: nameLabel, Reqd: true, InListView: true},
		{Fieldname: groupField, Fieldtype: framework.FieldData, Label: groupLabel, InListView: true},
		{Fieldname: "email", Fieldtype: framework.FieldData, Label: "Email", InListView: true},
		{Fieldname: "phone", Fieldtype: framework.FieldData, Label: "Phone"},
		{Fieldname: "tax_id", Fieldtype: framework.FieldData, Label: "Tax ID"},
	}
}

// erpPerms is the shared master/transaction permission set: the org owner (System
// Manager) has full rights; a granted Erp User may operate. A role-less member is
// denied — secure by default (the engine's define-time seed is a System-Manager
// grant, so this is an explicit widening, never a loosening).
func erpPerms() []framework.DocPerm {
	return []framework.DocPerm{
		{Role: framework.RoleSystemManager, Read: true, Write: true, Create: true, Delete: true, Submit: true, Cancel: true},
		{Role: RoleErpUser, Read: true, Write: true, Create: true, Delete: true, Submit: true, Cancel: true},
	}
}

// ledgerPerms makes a system-posted ledger read-only: only the System Manager may
// read it, and NO role may create/write/delete it through the generic surface. The
// posting hooks write it via the store (which does not consult DocPerm), so the
// ledger is append-only-by-hook and immutable-to-humans.
func ledgerPerms() []framework.DocPerm {
	return []framework.DocPerm{{Role: framework.RoleSystemManager, Read: true}}
}
