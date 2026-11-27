"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
var __export = (target, all) => {
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: !0 });
}, __copyProps = (to, from, except, desc) => {
  if (from && typeof from == "object" || typeof from == "function")
    for (let key of __getOwnPropNames(from))
      !__hasOwnProp.call(to, key) && key !== except && __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  return to;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: !0 }), mod);
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/index.ts
var index_exports = {};
__export(index_exports, {
  oracleFusionCloudErp: () => oracleFusionCloudErp
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework24 = require("@activepieces/pieces-framework"), import_pieces_framework25 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/invoices/create-invoice.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), authDesc = `
Connect to your Oracle Fusion Cloud ERP instance using Basic Authentication.

**Required:**
- **Server URL**: Your Oracle Fusion instance URL (e.g., https://your-instance.fa.us2.oraclecloud.com)
- **Username**: Your Oracle Cloud username with API access
- **Password**: Your Oracle Cloud password

Contact your Oracle administrator if you need REST API access enabled.
`, oracleFusionCloudErpAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: authDesc,
  required: !0,
  props: {
    serverUrl: import_pieces_framework.Property.ShortText({
      displayName: "Server URL",
      description: "Your Oracle Fusion Cloud instance URL (e.g., https://your-instance.fa.us2.oraclecloud.com)",
      required: !0
    }),
    username: import_pieces_framework.Property.ShortText({
      displayName: "Username",
      description: "Your Oracle Cloud username",
      required: !0
    }),
    password: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Password",
      description: "Your Oracle Cloud password",
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var OracleFusionAPIClient = class {
  constructor(serverUrl, username, password) {
    __publicField(this, "serverUrl", serverUrl);
    __publicField(this, "credentials");
    this.credentials = Buffer.from(`${username}:${password}`).toString("base64");
  }
  async makeRequest(method, resourceUri, query, body, headers) {
    let baseUrl = `${this.serverUrl}/fscmRestApi/resources/11.13.18.05`, params = {}, requestHeaders = __spreadValues({
      "Content-Type": "application/json",
      Authorization: `Basic ${this.credentials}`
    }, headers);
    if (query)
      for (let [key, value] of Object.entries(query))
        value != null && (params[key] = String(value));
    let request = {
      method,
      url: baseUrl + resourceUri,
      headers: requestHeaders,
      queryParams: params,
      body
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  }
  async createRecord(endpoint, request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      endpoint,
      void 0,
      request,
      { "Content-Type": "application/vnd.oracle.adf.resourceitem+json" }
    );
  }
  async updateRecord(endpoint, request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.PATCH,
      endpoint,
      void 0,
      request,
      { "Content-Type": "application/vnd.oracle.adf.resourceitem+json" }
    );
  }
  async getRecord(endpoint) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, endpoint);
  }
  async deleteRecord(endpoint) {
    await this.makeRequest(import_pieces_common.HttpMethod.DELETE, endpoint);
  }
  async searchRecords(endpoint, params) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      endpoint,
      params
    );
  }
  async getBusinessObjects() {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/");
  }
  async executeAction(actionEndpoint, payload) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      actionEndpoint,
      void 0,
      payload,
      { "Content-Type": "application/vnd.oracle.adf.action+json" }
    );
  }
};
function makeClient(auth) {
  return new OracleFusionAPIClient(
    auth.serverUrl,
    auth.username,
    auth.password
  );
}

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/invoices/create-invoice.ts
var createInvoice = (0, import_pieces_framework2.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "create_invoice",
  displayName: "Create Invoice",
  description: "Creates a new payables invoice in Oracle Fusion Cloud ERP.",
  audience: "both",
  aiMetadata: { description: "Create a new payables (supplier) invoice, requiring invoice number, business unit, supplier and supplier site, amount, currencies, key dates, and payment method. Not idempotent: each call inserts a new invoice and repeats create duplicates (the supplier invoice number is not deduplicated here). Use for bills you owe suppliers; for customer billing use Create Receivables Invoice instead.", idempotent: !1 },
  props: {
    invoiceNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Invoice Number",
      description: "Unique invoice number from the supplier.",
      required: !0
    }),
    businessUnit: import_pieces_framework2.Property.ShortText({
      displayName: "Business Unit",
      description: "The business unit name for the invoice.",
      required: !0
    }),
    supplier: import_pieces_framework2.Property.ShortText({
      displayName: "Supplier",
      description: "The supplier name on the invoice.",
      required: !0
    }),
    supplierSite: import_pieces_framework2.Property.ShortText({
      displayName: "Supplier Site",
      description: "The supplier site from where goods/services are rendered.",
      required: !0
    }),
    invoiceAmount: import_pieces_framework2.Property.Number({
      displayName: "Invoice Amount",
      description: "The total invoice amount.",
      required: !0
    }),
    invoiceCurrency: import_pieces_framework2.Property.ShortText({
      displayName: "Invoice Currency",
      description: "Currency code (e.g., USD, EUR).",
      required: !0,
      defaultValue: "USD"
    }),
    paymentCurrency: import_pieces_framework2.Property.ShortText({
      displayName: "Payment Currency",
      description: "Currency used for payment.",
      required: !0,
      defaultValue: "USD"
    }),
    invoiceDate: import_pieces_framework2.Property.ShortText({
      displayName: "Invoice Date",
      description: "Invoice date in YYYY-MM-DD format.",
      required: !0
    }),
    accountingDate: import_pieces_framework2.Property.ShortText({
      displayName: "Accounting Date",
      description: "Accounting date in YYYY-MM-DD format.",
      required: !0
    }),
    termsDate: import_pieces_framework2.Property.ShortText({
      displayName: "Terms Date",
      description: "Date used with payment terms to calculate due dates (YYYY-MM-DD).",
      required: !0
    }),
    paymentMethodCode: import_pieces_framework2.Property.ShortText({
      displayName: "Payment Method Code",
      description: "Payment method code (e.g., CHECK, EFT).",
      required: !0
    }),
    paymentTerms: import_pieces_framework2.Property.ShortText({
      displayName: "Payment Terms",
      description: "Payment terms used to calculate due dates.",
      required: !1
    }),
    description: import_pieces_framework2.Property.LongText({
      displayName: "Description",
      description: "Description of the invoice.",
      required: !1
    }),
    invoiceGroup: import_pieces_framework2.Property.ShortText({
      displayName: "Invoice Group",
      description: "Group name for reporting and payment purposes.",
      required: !1
    }),
    invoiceType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Invoice Type",
      description: "Type of invoice.",
      required: !1,
      options: {
        options: [
          { label: "Standard", value: "Standard" },
          { label: "Prepayment", value: "Prepayment" },
          { label: "Credit Memo", value: "Credit Memo" },
          { label: "Debit Memo", value: "Debit Memo" }
        ]
      }
    }),
    requester: import_pieces_framework2.Property.ShortText({
      displayName: "Requester",
      description: "Name of the person who requested the goods or services.",
      required: !1
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), {
      invoiceNumber,
      businessUnit,
      supplier,
      supplierSite,
      invoiceAmount,
      invoiceCurrency,
      paymentCurrency,
      invoiceDate,
      accountingDate,
      termsDate,
      paymentMethodCode,
      paymentTerms,
      description,
      invoiceGroup,
      invoiceType,
      requester
    } = context.propsValue, payload = {
      InvoiceNumber: invoiceNumber,
      BusinessUnit: businessUnit,
      Supplier: supplier,
      SupplierSite: supplierSite,
      InvoiceAmount: invoiceAmount,
      InvoiceCurrency: invoiceCurrency,
      PaymentCurrency: paymentCurrency,
      InvoiceDate: invoiceDate,
      AccountingDate: accountingDate,
      TermsDate: termsDate,
      PaymentMethodCode: paymentMethodCode
    };
    return paymentTerms && (payload.PaymentTerms = paymentTerms), description && (payload.Description = description), invoiceGroup && (payload.InvoiceGroup = invoiceGroup), invoiceType && (payload.InvoiceType = invoiceType), requester && (payload.Requester = requester), await client.createRecord("/invoices", payload);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/invoices/get-invoice.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var getInvoice = (0, import_pieces_framework3.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "get_invoice",
  displayName: "Get Invoice",
  description: "Retrieves details of a specific payables invoice by ID.",
  audience: "both",
  aiMetadata: { description: "Fetch one payables (supplier) invoice by its InvoiceId. Read-only and idempotent. Use when you already have the payables invoice ID; to look one up by number, supplier, or status use Find Invoices first. Distinct from Get Receivables Invoice, which retrieves customer-billing (AR) invoices.", idempotent: !0 },
  props: {
    invoiceId: import_pieces_framework3.Property.ShortText({
      displayName: "Invoice ID",
      description: "The unique identifier of the invoice (InvoiceId or invoicesUniqID).",
      required: !0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { invoiceId } = context.propsValue;
    return await client.getRecord(`/invoices/${invoiceId}`);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/invoices/find-invoices.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var findInvoices = (0, import_pieces_framework4.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "find_invoices",
  displayName: "Find Invoices",
  description: "Search for payables invoices with optional filters.",
  audience: "both",
  aiMetadata: { description: "List or search payables (supplier) invoices, optionally filtering by invoice number, supplier, business unit, or validation status, with limit/offset paging (default 25, max 500). Read-only and idempotent. Use to discover supplier bills or resolve an InvoiceId; for customer-billing invoices use Find Receivables Invoices instead.", idempotent: !0 },
  props: {
    invoiceNumber: import_pieces_framework4.Property.ShortText({
      displayName: "Invoice Number",
      description: "Filter by invoice number.",
      required: !1
    }),
    supplier: import_pieces_framework4.Property.ShortText({
      displayName: "Supplier",
      description: "Filter by supplier name.",
      required: !1
    }),
    businessUnit: import_pieces_framework4.Property.ShortText({
      displayName: "Business Unit",
      description: "Filter by business unit.",
      required: !1
    }),
    validationStatus: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Validation Status",
      description: "Filter by validation status.",
      required: !1,
      options: {
        options: [
          { label: "Not Validated", value: "Not Validated" },
          { label: "Validated", value: "Validated" },
          { label: "Needs Revalidation", value: "Needs Revalidation" }
        ]
      }
    }),
    limit: import_pieces_framework4.Property.Number({
      displayName: "Limit",
      description: "Maximum number of records to return (default: 25, max: 500).",
      required: !1,
      defaultValue: 25
    }),
    offset: import_pieces_framework4.Property.Number({
      displayName: "Offset",
      description: "Number of records to skip for pagination.",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { invoiceNumber, supplier, businessUnit, validationStatus, limit, offset } = context.propsValue, queryParams = {
      limit: Math.min(limit || 25, 500),
      offset: offset || 0
    }, filters = [];
    return invoiceNumber && filters.push(`InvoiceNumber="${invoiceNumber}"`), supplier && filters.push(`Supplier="${supplier}"`), businessUnit && filters.push(`BusinessUnit="${businessUnit}"`), validationStatus && filters.push(`ValidationStatus="${validationStatus}"`), filters.length > 0 && (queryParams.q = filters.join(" AND ")), await client.searchRecords("/invoices", queryParams);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/invoices/update-invoice.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var updateInvoice = (0, import_pieces_framework5.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "update_invoice",
  displayName: "Update Invoice",
  description: "Updates an existing payables invoice. Note: Only certain attributes can be updated and cascade defaulting does not apply.",
  audience: "both",
  aiMetadata: { description: "Modify an existing payables (supplier) invoice by InvoiceId; only a limited set of fields (description, payment terms, pay group, invoice group, payment method, remittance messages) can be changed and cascade defaulting does not apply. At least one field must be supplied or the call fails. Effectively idempotent since it sets the given values.", idempotent: !0 },
  props: {
    invoiceId: import_pieces_framework5.Property.ShortText({
      displayName: "Invoice ID",
      description: "The unique identifier of the invoice to update.",
      required: !0
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "Updated description for the invoice.",
      required: !1
    }),
    paymentTerms: import_pieces_framework5.Property.ShortText({
      displayName: "Payment Terms",
      description: "Updated payment terms.",
      required: !1
    }),
    payGroup: import_pieces_framework5.Property.ShortText({
      displayName: "Pay Group",
      description: "Updated pay group.",
      required: !1
    }),
    invoiceGroup: import_pieces_framework5.Property.ShortText({
      displayName: "Invoice Group",
      description: "Updated invoice group.",
      required: !1
    }),
    paymentMethodCode: import_pieces_framework5.Property.ShortText({
      displayName: "Payment Method Code",
      description: "Updated payment method code.",
      required: !1
    }),
    remittanceMessageOne: import_pieces_framework5.Property.ShortText({
      displayName: "Remittance Message 1",
      description: "Remittance message for payment processing.",
      required: !1
    }),
    remittanceMessageTwo: import_pieces_framework5.Property.ShortText({
      displayName: "Remittance Message 2",
      description: "Additional remittance message.",
      required: !1
    }),
    remittanceMessageThree: import_pieces_framework5.Property.ShortText({
      displayName: "Remittance Message 3",
      description: "Additional remittance message.",
      required: !1
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), {
      invoiceId,
      description,
      paymentTerms,
      payGroup,
      invoiceGroup,
      paymentMethodCode,
      remittanceMessageOne,
      remittanceMessageTwo,
      remittanceMessageThree
    } = context.propsValue, payload = {};
    if (description !== void 0 && description !== "" && (payload.Description = description), paymentTerms && (payload.PaymentTerms = paymentTerms), payGroup && (payload.PayGroup = payGroup), invoiceGroup && (payload.InvoiceGroup = invoiceGroup), paymentMethodCode && (payload.PaymentMethodCode = paymentMethodCode), remittanceMessageOne && (payload.RemittanceMessageOne = remittanceMessageOne), remittanceMessageTwo && (payload.RemittanceMessageTwo = remittanceMessageTwo), remittanceMessageThree && (payload.RemittanceMessageThree = remittanceMessageThree), Object.keys(payload).length === 0)
      throw new Error("At least one field must be provided to update the invoice.");
    return await client.updateRecord(`/invoices/${invoiceId}`, payload);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/invoices/delete-invoice.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var deleteInvoice = (0, import_pieces_framework6.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "delete_invoice",
  displayName: "Delete Invoice",
  description: "Deletes a payables invoice from Oracle Fusion Cloud ERP.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a payables (supplier) invoice by its InvoiceId. Destructive; idempotent in effect since a removed invoice cannot be deleted again. Oracle typically allows deletion only for unvalidated/unpaid invoices; to back out a validated invoice without removing it use Cancel Invoice instead.", idempotent: !0 },
  props: {
    invoiceId: import_pieces_framework6.Property.ShortText({
      displayName: "Invoice ID",
      description: "The unique identifier of the invoice to delete.",
      required: !0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { invoiceId } = context.propsValue;
    return await client.deleteRecord(`/invoices/${invoiceId}`), { success: !0, message: `Invoice ${invoiceId} deleted successfully.` };
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/invoices/validate-invoice.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var validateInvoice = (0, import_pieces_framework7.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "validate_invoice",
  displayName: "Validate Invoice",
  description: "Validates a payables invoice in Oracle Fusion Cloud ERP.",
  audience: "both",
  aiMetadata: { description: "Run Oracle validation on a payables (supplier) invoice by InvoiceId, checking it and applying any holds so it can proceed to payment. State-changing but effectively idempotent: re-validating an already-validated invoice has no further effect. Use before paying a supplier bill; this does not create or cancel the invoice.", idempotent: !0 },
  props: {
    invoiceId: import_pieces_framework7.Property.ShortText({
      displayName: "Invoice ID",
      description: "The unique identifier of the invoice to validate.",
      required: !0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { invoiceId } = context.propsValue;
    return await client.executeAction("/invoices/action/validateInvoice", {
      InvoiceId: invoiceId
    });
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/invoices/cancel-invoice.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var cancelInvoice = (0, import_pieces_framework8.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "cancel_invoice",
  displayName: "Cancel Invoice",
  description: "Cancels a payables invoice in Oracle Fusion Cloud ERP.",
  audience: "both",
  aiMetadata: { description: "Cancel a payables (supplier) invoice by InvoiceId, voiding its amount so it is no longer payable. State-changing; effectively idempotent since an already-cancelled invoice stays cancelled, though a paid or validated invoice may be rejected by Oracle. Use to back out a supplier bill in place; to remove it entirely use Delete Invoice.", idempotent: !0 },
  props: {
    invoiceId: import_pieces_framework8.Property.ShortText({
      displayName: "Invoice ID",
      description: "The unique identifier of the invoice to cancel.",
      required: !0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { invoiceId } = context.propsValue;
    return await client.executeAction("/invoices/action/cancelInvoice", {
      InvoiceId: invoiceId
    });
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/receivables-invoices/create-receivables-invoice.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var createReceivablesInvoice = (0, import_pieces_framework9.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "create_receivables_invoice",
  displayName: "Create Receivables Invoice",
  description: "Creates a new receivables (AR) invoice in Oracle Fusion Cloud ERP.",
  audience: "both",
  aiMetadata: { description: "Create a new accounts-receivable (customer-billing) invoice, requiring at minimum a business unit, transaction type, and bill-to customer account number. Not idempotent: each call creates a new invoice, and an omitted transaction number is auto-generated, so re-running produces duplicates. Use for billing customers; for supplier bills you owe, use Create Invoice (payables) instead.", idempotent: !1 },
  props: {
    businessUnit: import_pieces_framework9.Property.ShortText({
      displayName: "Business Unit",
      description: "The business unit under which the invoice is created.",
      required: !0
    }),
    transactionType: import_pieces_framework9.Property.ShortText({
      displayName: "Transaction Type",
      description: "The transaction type assigned to the invoice.",
      required: !0
    }),
    transactionSource: import_pieces_framework9.Property.ShortText({
      displayName: "Transaction Source",
      description: "The transaction source assigned to the invoice.",
      required: !1
    }),
    transactionNumber: import_pieces_framework9.Property.ShortText({
      displayName: "Transaction Number",
      description: "The transaction number assigned to the invoice. If not provided, it will be auto-generated.",
      required: !1
    }),
    transactionDate: import_pieces_framework9.Property.ShortText({
      displayName: "Transaction Date",
      description: "The date when the invoice was created (YYYY-MM-DD).",
      required: !1
    }),
    accountingDate: import_pieces_framework9.Property.ShortText({
      displayName: "Accounting Date",
      description: "The accounting date assigned to the invoice (YYYY-MM-DD).",
      required: !1
    }),
    billToCustomerNumber: import_pieces_framework9.Property.ShortText({
      displayName: "Bill-to Customer Account Number",
      description: "The account number of the bill-to customer.",
      required: !0
    }),
    billToCustomerName: import_pieces_framework9.Property.ShortText({
      displayName: "Bill-to Customer Name",
      description: "The name of the bill-to customer.",
      required: !1
    }),
    billToSite: import_pieces_framework9.Property.ShortText({
      displayName: "Bill-to Site",
      description: "The number that identifies the bill-to customer site.",
      required: !1
    }),
    invoiceCurrencyCode: import_pieces_framework9.Property.ShortText({
      displayName: "Invoice Currency",
      description: "The currency code of the invoice (e.g., USD, EUR).",
      required: !1,
      defaultValue: "USD"
    }),
    paymentTerms: import_pieces_framework9.Property.ShortText({
      displayName: "Payment Terms",
      description: "The payment terms assigned to the invoice.",
      required: !1
    }),
    dueDate: import_pieces_framework9.Property.ShortText({
      displayName: "Due Date",
      description: "The date when the invoice is due (YYYY-MM-DD).",
      required: !1
    }),
    purchaseOrder: import_pieces_framework9.Property.ShortText({
      displayName: "PO Number",
      description: "The purchase order number on the invoice.",
      required: !1
    }),
    comments: import_pieces_framework9.Property.LongText({
      displayName: "Comments",
      description: "Comments that accompany the invoice.",
      required: !1
    }),
    invoiceStatus: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Invoice Status",
      description: "The completion status of the invoice. Must be Complete when creating.",
      required: !1,
      defaultValue: "Complete",
      options: {
        options: [
          { label: "Complete", value: "Complete" },
          { label: "Incomplete", value: "Incomplete" }
        ]
      }
    }),
    shipToCustomerNumber: import_pieces_framework9.Property.ShortText({
      displayName: "Ship-to Customer",
      description: "The registry identifier of the ship-to customer.",
      required: !1
    }),
    shipToCustomerName: import_pieces_framework9.Property.ShortText({
      displayName: "Ship-to Customer Name",
      description: "The name of the customer who receives the goods or services.",
      required: !1
    }),
    legalEntityIdentifier: import_pieces_framework9.Property.ShortText({
      displayName: "Legal Entity Identifier",
      description: "The unique identifier of the legal entity.",
      required: !1
    }),
    crossReference: import_pieces_framework9.Property.ShortText({
      displayName: "Reference",
      description: "The reference field default value from the transaction source.",
      required: !1
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), {
      businessUnit,
      transactionType,
      transactionSource,
      transactionNumber,
      transactionDate,
      accountingDate,
      billToCustomerNumber,
      billToCustomerName,
      billToSite,
      invoiceCurrencyCode,
      paymentTerms,
      dueDate,
      purchaseOrder,
      comments,
      invoiceStatus,
      shipToCustomerNumber,
      shipToCustomerName,
      legalEntityIdentifier,
      crossReference
    } = context.propsValue, payload = {
      BusinessUnit: businessUnit,
      TransactionType: transactionType,
      BillToCustomerNumber: billToCustomerNumber
    };
    return transactionSource && (payload.TransactionSource = transactionSource), transactionNumber && (payload.TransactionNumber = transactionNumber), transactionDate && (payload.TransactionDate = transactionDate), accountingDate && (payload.AccountingDate = accountingDate), billToCustomerName && (payload.BillToCustomerName = billToCustomerName), billToSite && (payload.BillToSite = billToSite), invoiceCurrencyCode && (payload.InvoiceCurrencyCode = invoiceCurrencyCode), paymentTerms && (payload.PaymentTerms = paymentTerms), dueDate && (payload.DueDate = dueDate), purchaseOrder && (payload.PurchaseOrder = purchaseOrder), comments && (payload.Comments = comments), invoiceStatus && (payload.InvoiceStatus = invoiceStatus), shipToCustomerNumber && (payload.ShipToCustomerNumber = shipToCustomerNumber), shipToCustomerName && (payload.ShipToCustomerName = shipToCustomerName), legalEntityIdentifier && (payload.LegalEntityIdentifier = legalEntityIdentifier), crossReference && (payload.CrossReference = crossReference), await client.createRecord("/receivablesInvoices", payload);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/receivables-invoices/get-receivables-invoice.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var getReceivablesInvoice = (0, import_pieces_framework10.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "get_receivables_invoice",
  displayName: "Get Receivables Invoice",
  description: "Retrieves details of a specific receivables invoice by ID.",
  audience: "both",
  aiMetadata: { description: "Fetch one accounts-receivable (customer) invoice by its CustomerTransactionId. Read-only and idempotent. Use when you already have the AR invoice ID; to look one up by transaction number, customer, or date range use Find Receivables Invoices first. Distinct from Get Invoice, which retrieves payables (supplier) invoices.", idempotent: !0 },
  props: {
    customerTransactionId: import_pieces_framework10.Property.ShortText({
      displayName: "Customer Transaction ID",
      description: "The unique identifier of the receivables invoice.",
      required: !0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { customerTransactionId } = context.propsValue;
    return await client.getRecord(`/receivablesInvoices/${customerTransactionId}`);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/receivables-invoices/find-receivables-invoices.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var findReceivablesInvoices = (0, import_pieces_framework11.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "find_receivables_invoices",
  displayName: "Find Receivables Invoices",
  description: "Search for receivables invoices with optional filters.",
  audience: "both",
  aiMetadata: { description: "List or search accounts-receivable (customer) invoices, optionally filtering by transaction number, bill-to customer, status, or a billing-date range, with limit/offset paging (default 25, max 500). Read-only and idempotent. Use this to discover AR invoices or resolve a CustomerTransactionId; for payables (supplier) invoices use Find Invoices instead.", idempotent: !0 },
  props: {
    transactionNumber: import_pieces_framework11.Property.ShortText({
      displayName: "Transaction Number",
      description: "Filter by transaction number.",
      required: !1
    }),
    billToCustomerNumber: import_pieces_framework11.Property.ShortText({
      displayName: "Bill-to Customer Account Number",
      description: "Filter by bill-to customer account number.",
      required: !1
    }),
    invoiceStatus: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Invoice Status",
      description: "Filter by invoice status.",
      required: !1,
      options: {
        options: [
          { label: "Complete", value: "Complete" },
          { label: "Incomplete", value: "Incomplete" },
          { label: "Frozen", value: "Frozen" }
        ]
      }
    }),
    billingDateFrom: import_pieces_framework11.Property.ShortText({
      displayName: "Billing Date From",
      description: "Filter invoices with billing date on or after this date (YYYY-MM-DD).",
      required: !1
    }),
    billingDateTo: import_pieces_framework11.Property.ShortText({
      displayName: "Billing Date To",
      description: "Filter invoices with billing date on or before this date (YYYY-MM-DD).",
      required: !1
    }),
    limit: import_pieces_framework11.Property.Number({
      displayName: "Limit",
      description: "Maximum number of records to return (default: 25, max: 500).",
      required: !1,
      defaultValue: 25
    }),
    offset: import_pieces_framework11.Property.Number({
      displayName: "Offset",
      description: "Number of records to skip for pagination.",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { transactionNumber, billToCustomerNumber, invoiceStatus, billingDateFrom, billingDateTo, limit, offset } = context.propsValue, queryParams = {
      limit: Math.min(limit || 25, 500),
      offset: offset || 0
    }, filters = [];
    return transactionNumber && filters.push(`TransactionNumber="${transactionNumber}"`), billToCustomerNumber && filters.push(`BillToCustomerNumber="${billToCustomerNumber}"`), invoiceStatus && filters.push(`InvoiceStatus="${invoiceStatus}"`), billingDateFrom && filters.push(`BillingDate>="${billingDateFrom}"`), billingDateTo && filters.push(`BillingDate<="${billingDateTo}"`), filters.length > 0 && (queryParams.q = filters.join(" AND ")), await client.searchRecords("/receivablesInvoices", queryParams);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/receivables-invoices/update-receivables-invoice.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var updateReceivablesInvoice = (0, import_pieces_framework12.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "update_receivables_invoice",
  displayName: "Update Receivables Invoice",
  description: "Updates an existing receivables invoice. Note: Only InvoiceStatus, PaymentTerms, and TransactionDate can be updated.",
  audience: "both",
  aiMetadata: { description: "Modify an existing accounts-receivable invoice identified by CustomerTransactionId; only invoice status, payment terms, and transaction date are editable. At least one field must be supplied or the call fails. Effectively idempotent since it sets fields to the values given. The invoice must already exist; use Create Receivables Invoice to add a new one.", idempotent: !0 },
  props: {
    customerTransactionId: import_pieces_framework12.Property.ShortText({
      displayName: "Customer Transaction ID",
      description: "The unique identifier of the receivables invoice to update.",
      required: !0
    }),
    invoiceStatus: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Invoice Status",
      description: "The completion status of the invoice.",
      required: !1,
      options: {
        options: [
          { label: "Complete", value: "Complete" },
          { label: "Incomplete", value: "Incomplete" },
          { label: "Frozen", value: "Frozen" }
        ]
      }
    }),
    paymentTerms: import_pieces_framework12.Property.ShortText({
      displayName: "Payment Terms",
      description: "The payment terms assigned to the invoice.",
      required: !1
    }),
    transactionDate: import_pieces_framework12.Property.ShortText({
      displayName: "Transaction Date",
      description: "The date when the invoice was created (YYYY-MM-DD).",
      required: !1
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { customerTransactionId, invoiceStatus, paymentTerms, transactionDate } = context.propsValue, payload = {};
    if (invoiceStatus && (payload.InvoiceStatus = invoiceStatus), paymentTerms && (payload.PaymentTerms = paymentTerms), transactionDate && (payload.TransactionDate = transactionDate), Object.keys(payload).length === 0)
      throw new Error("At least one field (InvoiceStatus, PaymentTerms, or TransactionDate) must be provided to update.");
    return await client.updateRecord(`/receivablesInvoices/${customerTransactionId}`, payload);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/receivables-invoices/delete-receivables-invoice.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var deleteReceivablesInvoice = (0, import_pieces_framework13.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "delete_receivables_invoice",
  displayName: "Delete Receivables Invoice",
  description: "Deletes a receivables invoice from Oracle Fusion Cloud ERP.",
  audience: "both",
  aiMetadata: { description: "Permanently delete an accounts-receivable (customer) invoice by its CustomerTransactionId. Destructive; idempotent in effect since a deleted invoice cannot be deleted again (re-running on a missing ID errors). Use only when the invoice should be fully removed rather than corrected; to change status or fields use Update Receivables Invoice.", idempotent: !0 },
  props: {
    customerTransactionId: import_pieces_framework13.Property.ShortText({
      displayName: "Customer Transaction ID",
      description: "The unique identifier of the receivables invoice to delete.",
      required: !0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { customerTransactionId } = context.propsValue;
    return await client.deleteRecord(`/receivablesInvoices/${customerTransactionId}`), { success: !0, message: `Receivables invoice ${customerTransactionId} deleted successfully.` };
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/payments/create-payment.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var createPayment = (0, import_pieces_framework14.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "create_payment",
  displayName: "Create Payment",
  description: "Creates a new manual payment in Oracle Fusion Cloud ERP.",
  audience: "both",
  aiMetadata: { description: "Record a manual payables payment (e.g. a check or EFT), requiring business unit, payment number, date, currency, and payment method. Not idempotent: each call records a new payment and re-running issues a duplicate. Use to disburse funds to a payee; to reverse a payment use Void Payment, or Stop Payment to halt one before it clears.", idempotent: !1 },
  props: {
    businessUnit: import_pieces_framework14.Property.ShortText({
      displayName: "Business Unit",
      description: "The business unit name for the payment.",
      required: !0
    }),
    paymentNumber: import_pieces_framework14.Property.Number({
      displayName: "Payment Number",
      description: "The check number printed on physical check.",
      required: !0
    }),
    paymentDate: import_pieces_framework14.Property.ShortText({
      displayName: "Payment Date",
      description: "The date when the payment is made (YYYY-MM-DD).",
      required: !0
    }),
    paymentCurrency: import_pieces_framework14.Property.ShortText({
      displayName: "Payment Currency",
      description: "The currency in which payment is made (e.g., USD).",
      required: !0,
      defaultValue: "USD"
    }),
    paymentMethodCode: import_pieces_framework14.Property.ShortText({
      displayName: "Payment Method Code",
      description: "The payment method code (e.g., CHECK, EFT).",
      required: !0
    }),
    payee: import_pieces_framework14.Property.ShortText({
      displayName: "Payee",
      description: "The party name to whom payment is made.",
      required: !1
    }),
    payeeSite: import_pieces_framework14.Property.ShortText({
      displayName: "Payee Site",
      description: "The supplier site name to whom payment is made.",
      required: !1
    }),
    paymentProcessProfile: import_pieces_framework14.Property.ShortText({
      displayName: "Payment Process Profile",
      description: "The payment process profile name.",
      required: !1
    }),
    paymentDocument: import_pieces_framework14.Property.ShortText({
      displayName: "Payment Document",
      description: "The name of the payment document.",
      required: !1
    }),
    paymentDescription: import_pieces_framework14.Property.LongText({
      displayName: "Payment Description",
      description: "The user description for the payment.",
      required: !1
    }),
    disbursementBankAccountName: import_pieces_framework14.Property.ShortText({
      displayName: "Disbursement Bank Account",
      description: "The name of the internal bank account from where the payment is made.",
      required: !1
    }),
    remitToAccountNumber: import_pieces_framework14.Property.ShortText({
      displayName: "Remit-to Account Number",
      description: "The supplier's bank account number for electronic payment.",
      required: !1
    }),
    conversionRateType: import_pieces_framework14.Property.ShortText({
      displayName: "Conversion Rate Type",
      description: "The type of conversion rate used for foreign currency payments.",
      required: !1
    }),
    conversionRate: import_pieces_framework14.Property.Number({
      displayName: "Conversion Rate",
      description: "The conversion rate for foreign currency payments.",
      required: !1
    }),
    addressLine1: import_pieces_framework14.Property.ShortText({
      displayName: "Address Line 1",
      description: "The first address line of party to whom the payment is made.",
      required: !1
    }),
    city: import_pieces_framework14.Property.ShortText({
      displayName: "City",
      description: "The city name of the party to whom the payment is made.",
      required: !1
    }),
    state: import_pieces_framework14.Property.ShortText({
      displayName: "State",
      description: "The state name of the party to whom the payment is made.",
      required: !1
    }),
    country: import_pieces_framework14.Property.ShortText({
      displayName: "Country",
      description: "The country name of the party to whom the payment is made.",
      required: !1
    }),
    zip: import_pieces_framework14.Property.ShortText({
      displayName: "Postal Code",
      description: "The postal code of the party to whom the payment is made.",
      required: !1
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), {
      businessUnit,
      paymentNumber,
      paymentDate,
      paymentCurrency,
      paymentMethodCode,
      payee,
      payeeSite,
      paymentProcessProfile,
      paymentDocument,
      paymentDescription,
      disbursementBankAccountName,
      remitToAccountNumber,
      conversionRateType,
      conversionRate,
      addressLine1,
      city,
      state,
      country,
      zip
    } = context.propsValue, payload = {
      BusinessUnit: businessUnit,
      PaymentNumber: paymentNumber,
      PaymentDate: paymentDate,
      PaymentCurrency: paymentCurrency,
      PaymentMethodCode: paymentMethodCode
    };
    return payee && (payload.Payee = payee), payeeSite && (payload.PayeeSite = payeeSite), paymentProcessProfile && (payload.PaymentProcessProfile = paymentProcessProfile), paymentDocument && (payload.PaymentDocument = paymentDocument), paymentDescription && (payload.PaymentDescription = paymentDescription), disbursementBankAccountName && (payload.DisbursementBankAccountName = disbursementBankAccountName), remitToAccountNumber && (payload.RemitToAccountNumber = remitToAccountNumber), conversionRateType && (payload.ConversionRateType = conversionRateType), conversionRate && (payload.ConversionRate = conversionRate), addressLine1 && (payload.AddressLine1 = addressLine1), city && (payload.City = city), state && (payload.State = state), country && (payload.Country = country), zip && (payload.Zip = zip), await client.createRecord("/payablesPayments", payload);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/payments/get-payment.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var getPayment = (0, import_pieces_framework15.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "get_payment",
  displayName: "Get Payment",
  description: "Retrieves details of a specific payment by Check ID.",
  audience: "both",
  aiMetadata: { description: "Fetch one payables payment by its CheckId. Read-only and idempotent. Use when you already have the payment/check identifier to inspect its status, amount, or payee before voiding or stopping it.", idempotent: !0 },
  props: {
    checkId: import_pieces_framework15.Property.ShortText({
      displayName: "Check ID",
      description: "The unique identifier of the payment.",
      required: !0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { checkId } = context.propsValue;
    return await client.getRecord(`/payablesPayments/${checkId}`);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/payments/find-payments.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var findPayments = (0, import_pieces_framework16.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "find_payments",
  displayName: "Find Payments",
  description: "Search for payments with optional filters.",
  audience: "both",
  aiMetadata: { description: "Read-only search of Oracle Fusion accounts-payable payments, optionally filtered by payment number, payee, business unit, status (Negotiable, Voided, Stopped, Cleared), or a payment-date range, with limit/offset paging. Use to look up or list payments; it never modifies data. To change a payment use Update Payment instead.", idempotent: !0 },
  props: {
    paymentNumber: import_pieces_framework16.Property.Number({
      displayName: "Payment Number",
      description: "Filter by payment number.",
      required: !1
    }),
    payee: import_pieces_framework16.Property.ShortText({
      displayName: "Payee",
      description: "Filter by payee name.",
      required: !1
    }),
    businessUnit: import_pieces_framework16.Property.ShortText({
      displayName: "Business Unit",
      description: "Filter by business unit.",
      required: !1
    }),
    paymentStatus: import_pieces_framework16.Property.StaticDropdown({
      displayName: "Payment Status",
      description: "Filter by payment status.",
      required: !1,
      options: {
        options: [
          { label: "Negotiable", value: "Negotiable" },
          { label: "Voided", value: "Voided" },
          { label: "Stopped", value: "Stopped" },
          { label: "Cleared", value: "Cleared" }
        ]
      }
    }),
    paymentDateFrom: import_pieces_framework16.Property.ShortText({
      displayName: "Payment Date From",
      description: "Filter payments on or after this date (YYYY-MM-DD).",
      required: !1
    }),
    paymentDateTo: import_pieces_framework16.Property.ShortText({
      displayName: "Payment Date To",
      description: "Filter payments on or before this date (YYYY-MM-DD).",
      required: !1
    }),
    limit: import_pieces_framework16.Property.Number({
      displayName: "Limit",
      description: "Maximum number of records to return (default: 25, max: 500).",
      required: !1,
      defaultValue: 25
    }),
    offset: import_pieces_framework16.Property.Number({
      displayName: "Offset",
      description: "Number of records to skip for pagination.",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { paymentNumber, payee, businessUnit, paymentStatus, paymentDateFrom, paymentDateTo, limit, offset } = context.propsValue, queryParams = {
      limit: Math.min(limit || 25, 500),
      offset: offset || 0
    }, filters = [];
    return paymentNumber && filters.push(`PaymentNumber=${paymentNumber}`), payee && filters.push(`Payee="${payee}"`), businessUnit && filters.push(`BusinessUnit="${businessUnit}"`), paymentStatus && filters.push(`PaymentStatus="${paymentStatus}"`), paymentDateFrom && filters.push(`PaymentDate>="${paymentDateFrom}"`), paymentDateTo && filters.push(`PaymentDate<="${paymentDateTo}"`), filters.length > 0 && (queryParams.q = filters.join(" AND ")), await client.searchRecords("/payablesPayments", queryParams);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/payments/update-payment.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var updatePayment = (0, import_pieces_framework17.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "update_payment",
  displayName: "Update Payment",
  description: "Updates descriptive and global flexfields for a payment.",
  audience: "both",
  aiMetadata: { description: "Update an existing Oracle Fusion accounts-payable payment identified by its Check ID, setting its description and/or void dates (void date and void accounting date). Requires the payment to already exist and at least one field to change; only the supplied fields are modified, so reapplying the same values leaves the payment unchanged.", idempotent: !0 },
  props: {
    checkId: import_pieces_framework17.Property.ShortText({
      displayName: "Check ID",
      description: "The unique identifier of the payment to update.",
      required: !0
    }),
    paymentDescription: import_pieces_framework17.Property.LongText({
      displayName: "Payment Description",
      description: "Updated description for the payment.",
      required: !1
    }),
    voidDate: import_pieces_framework17.Property.ShortText({
      displayName: "Void Date",
      description: "The date when the payment is voided (YYYY-MM-DD).",
      required: !1
    }),
    voidAccountingDate: import_pieces_framework17.Property.ShortText({
      displayName: "Void Accounting Date",
      description: "The date when the voided payment is accounted (YYYY-MM-DD).",
      required: !1
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { checkId, paymentDescription, voidDate, voidAccountingDate } = context.propsValue, payload = {};
    if (paymentDescription !== void 0 && paymentDescription !== "" && (payload.PaymentDescription = paymentDescription), voidDate && (payload.VoidDate = voidDate), voidAccountingDate && (payload.VoidAccountingDate = voidAccountingDate), Object.keys(payload).length === 0)
      throw new Error("At least one field must be provided to update the payment.");
    return await client.updateRecord(`/payablesPayments/${checkId}`, payload);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/payments/stop-payment.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var stopPayment = (0, import_pieces_framework18.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "stop_payment",
  displayName: "Stop Payment",
  description: "Initiates a stop payment request for a payment.",
  audience: "both",
  aiMetadata: { description: "Place a stop-payment request on an issued payables payment by CheckId, with an optional reason and reference, to prevent it from being cashed. State-changing; effectively idempotent since repeating the request leaves the payment stopped. Use when a payment has gone out but should not clear; to fully reverse one already cleared use Void Payment instead.", idempotent: !0 },
  props: {
    checkId: import_pieces_framework18.Property.ShortText({
      displayName: "Check ID",
      description: "The unique identifier of the payment to stop.",
      required: !0
    }),
    stopReason: import_pieces_framework18.Property.ShortText({
      displayName: "Stop Reason",
      description: "The reason for stopping the payment.",
      required: !1
    }),
    stopReference: import_pieces_framework18.Property.ShortText({
      displayName: "Stop Reference",
      description: "A reference for the stop payment request.",
      required: !1
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { checkId, stopReason, stopReference } = context.propsValue, payload = {
      CheckId: checkId
    };
    return stopReason && (payload.StopReason = stopReason), stopReference && (payload.StopReference = stopReference), await client.executeAction("/payablesPayments/action/initiateStopPayment", payload);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/payments/void-payment.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var voidPayment = (0, import_pieces_framework19.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "void_payment",
  displayName: "Void Payment",
  description: "Voids a payment by setting the void date.",
  audience: "both",
  aiMetadata: { description: "Void an already-issued payables payment by CheckId, setting its void date (defaults to today) and optional void accounting date. State-changing; effectively idempotent since a voided payment stays voided. Use to reverse a payment that has been issued; to prevent one from clearing before it is cashed use Stop Payment instead.", idempotent: !0 },
  props: {
    checkId: import_pieces_framework19.Property.ShortText({
      displayName: "Check ID",
      description: "The unique identifier of the payment to void.",
      required: !0
    }),
    voidDate: import_pieces_framework19.Property.ShortText({
      displayName: "Void Date",
      description: "The date when the payment is voided (YYYY-MM-DD). Defaults to today.",
      required: !1
    }),
    voidAccountingDate: import_pieces_framework19.Property.ShortText({
      displayName: "Void Accounting Date",
      description: "The date when the voided payment is accounted (YYYY-MM-DD).",
      required: !1
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { checkId, voidDate, voidAccountingDate } = context.propsValue, today = (/* @__PURE__ */ new Date()).toISOString().split("T")[0], payload = {
      VoidDate: voidDate || today
    };
    return voidAccountingDate && (payload.VoidAccountingDate = voidAccountingDate), await client.updateRecord(`/payablesPayments/${checkId}`, payload);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/journal-batches/get-journal-batch.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var getJournalBatch = (0, import_pieces_framework20.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "get_journal_batch",
  displayName: "Get Journal Batch",
  description: "Retrieves details of a specific journal batch by ID.",
  audience: "both",
  aiMetadata: { description: "Fetch one general-ledger journal batch by its JeBatchId, optionally expanding child resources such as journal headers, errors, action logs, attachments, or descriptive flexfields. Read-only and idempotent. Use when you already have the batch ID; to search by name, status, or period use Find Journal Batches first.", idempotent: !0 },
  props: {
    jeBatchId: import_pieces_framework20.Property.ShortText({
      displayName: "Journal Batch ID",
      description: "The unique identifier of the journal batch.",
      required: !0
    }),
    expand: import_pieces_framework20.Property.StaticMultiSelectDropdown({
      displayName: "Expand Child Resources",
      description: "Select child resources to include in the response.",
      required: !1,
      options: {
        options: [
          { label: "Journal Headers", value: "journalHeaders" },
          { label: "Journal Errors", value: "journalErrors" },
          { label: "Action Logs", value: "journalActionLogs" },
          { label: "Attachments", value: "batchAttachment" },
          { label: "Descriptive Flexfields", value: "journalBatchDFF" }
        ]
      }
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { jeBatchId, expand } = context.propsValue, queryParams = {};
    return expand && expand.length > 0 && (queryParams.expand = expand.join(",")), await client.searchRecords(`/journalBatches/${jeBatchId}`, queryParams);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/journal-batches/find-journal-batches.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var findJournalBatches = (0, import_pieces_framework21.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "find_journal_batches",
  displayName: "Find Journal Batches",
  description: "Search for journal batches with optional filters.",
  audience: "both",
  aiMetadata: { description: "List or search general-ledger journal batches, optionally filtering by batch name, status (unposted/posted/error), period, journal source, or creator, with limit/offset paging (default 25, max 500). Read-only and idempotent. Use to discover batches or resolve a JeBatchId before fetching or updating a specific batch.", idempotent: !0 },
  props: {
    batchName: import_pieces_framework21.Property.ShortText({
      displayName: "Batch Name",
      description: "Filter by batch name.",
      required: !1
    }),
    status: import_pieces_framework21.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter by batch status.",
      required: !1,
      options: {
        options: [
          { label: "Unposted", value: "U" },
          { label: "Posted", value: "P" },
          { label: "Error", value: "E" }
        ]
      }
    }),
    defaultPeriodName: import_pieces_framework21.Property.ShortText({
      displayName: "Period Name",
      description: "Filter by accounting period name.",
      required: !1
    }),
    userJeSourceName: import_pieces_framework21.Property.ShortText({
      displayName: "Journal Source",
      description: "Filter by journal source name.",
      required: !1
    }),
    createdBy: import_pieces_framework21.Property.ShortText({
      displayName: "Created By",
      description: "Filter by the user who created the batch.",
      required: !1
    }),
    limit: import_pieces_framework21.Property.Number({
      displayName: "Limit",
      description: "Maximum number of records to return (default: 25, max: 500).",
      required: !1,
      defaultValue: 25
    }),
    offset: import_pieces_framework21.Property.Number({
      displayName: "Offset",
      description: "Number of records to skip for pagination.",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { batchName, status, defaultPeriodName, userJeSourceName, createdBy, limit, offset } = context.propsValue, queryParams = {
      limit: Math.min(limit || 25, 500),
      offset: offset || 0
    }, filters = [];
    return batchName && filters.push(`BatchName LIKE "*${batchName}*"`), status && filters.push(`Status="${status}"`), defaultPeriodName && filters.push(`DefaultPeriodName="${defaultPeriodName}"`), userJeSourceName && filters.push(`UserJeSourceName="${userJeSourceName}"`), createdBy && filters.push(`CreatedBy="${createdBy}"`), filters.length > 0 && (queryParams.q = filters.join(" AND ")), await client.searchRecords("/journalBatches", queryParams);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/journal-batches/update-journal-batch.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var updateJournalBatch = (0, import_pieces_framework22.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "update_journal_batch",
  displayName: "Update Journal Batch",
  description: "Updates batch completion status and reversal attributes. Note: Only status and reversal attributes can be updated.",
  audience: "both",
  aiMetadata: { description: "Modify a general-ledger journal batch identified by JeBatchId; only its status and reversal attributes (reverse flag, reversal date/period/method) are editable. At least one field must be supplied or the call fails. Effectively idempotent since it sets the given fields. The batch must already exist; use Find Journal Batches to locate it.", idempotent: !0 },
  props: {
    jeBatchId: import_pieces_framework22.Property.ShortText({
      displayName: "Journal Batch ID",
      description: "The unique identifier of the journal batch to update.",
      required: !0
    }),
    status: import_pieces_framework22.Property.StaticDropdown({
      displayName: "Batch Status",
      description: "The status of the batch.",
      required: !1,
      options: {
        options: [
          { label: "Unposted", value: "U" },
          { label: "Posted", value: "P" }
        ]
      }
    }),
    reversalFlag: import_pieces_framework22.Property.Checkbox({
      displayName: "Reverse Journals",
      description: "Indicates whether to reverse all journals in the batch.",
      required: !1,
      defaultValue: !1
    }),
    reversalDate: import_pieces_framework22.Property.ShortText({
      displayName: "Reversal Date",
      description: "The reversal date for the journals in the batch (YYYY-MM-DD).",
      required: !1
    }),
    reversalPeriod: import_pieces_framework22.Property.ShortText({
      displayName: "Reversal Period",
      description: "The reversal period for the journals in the batch.",
      required: !1
    }),
    reversalMethodMeaning: import_pieces_framework22.Property.StaticDropdown({
      displayName: "Reversal Method",
      description: "The reversal method for the journals in the batch.",
      required: !1,
      options: {
        options: [
          { label: "Change Sign", value: "Change sign" },
          { label: "Switch Debit or Credit", value: "Switch debit or credit" }
        ]
      }
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { jeBatchId, status, reversalFlag, reversalDate, reversalPeriod, reversalMethodMeaning } = context.propsValue, payload = {};
    if (status && (payload.Status = status), reversalFlag !== void 0 && (payload.ReversalFlag = reversalFlag), reversalDate && (payload.ReversalDate = reversalDate), reversalPeriod && (payload.ReversalPeriod = reversalPeriod), reversalMethodMeaning && (payload.ReversalMethodMeaning = reversalMethodMeaning), Object.keys(payload).length === 0)
      throw new Error("At least one field (Status or reversal attributes) must be provided to update.");
    return await client.updateRecord(`/journalBatches/${jeBatchId}`, payload);
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/lib/actions/journal-batches/delete-journal-batch.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var deleteJournalBatch = (0, import_pieces_framework23.createAction)({
  auth: oracleFusionCloudErpAuth,
  name: "delete_journal_batch",
  displayName: "Delete Journal Batch",
  description: "Deletes a journal batch from Oracle Fusion Cloud ERP.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a general-ledger journal batch by its JeBatchId. Destructive; idempotent in effect since a removed batch cannot be deleted again. Typically only unposted batches can be removed; use when discarding a draft batch rather than correcting it (use Update Journal Batch to change status or reversal attributes).", idempotent: !0 },
  props: {
    jeBatchId: import_pieces_framework23.Property.ShortText({
      displayName: "Journal Batch ID",
      description: "The unique identifier of the journal batch to delete.",
      required: !0
    })
  },
  async run(context) {
    let client = makeClient(context.auth.props), { jeBatchId } = context.propsValue;
    return await client.deleteRecord(`/journalBatches/${jeBatchId}`), { success: !0, message: `Journal batch ${jeBatchId} deleted successfully.` };
  }
});

// ../ap/packages/pieces/community/oracle-fusion-cloud-erp/src/index.ts
var oracleFusionCloudErp = (0, import_pieces_framework24.createPiece)({
  displayName: "Oracle Fusion Cloud ERP",
  description: "Enterprise resource planning suite covering financials, procurement, project accounting, supply chain, and more.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/oracle-fusion-cloud-erp.png",
  authors: ["owuzo", "onyedikachi-david"],
  categories: [import_pieces_framework25.PieceCategory.ACCOUNTING],
  auth: oracleFusionCloudErpAuth,
  actions: [
    createInvoice,
    getInvoice,
    findInvoices,
    updateInvoice,
    deleteInvoice,
    validateInvoice,
    cancelInvoice,
    createReceivablesInvoice,
    getReceivablesInvoice,
    findReceivablesInvoices,
    updateReceivablesInvoice,
    deleteReceivablesInvoice,
    createPayment,
    getPayment,
    findPayments,
    updatePayment,
    stopPayment,
    voidPayment,
    getJournalBatch,
    findJournalBatches,
    updateJournalBatch,
    deleteJournalBatch,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: (auth) => `${auth == null ? void 0 : auth.props.serverUrl}/fscmRestApi/resources/11.13.18.05`,
      auth: oracleFusionCloudErpAuth,
      authMapping: async (auth) => {
        let { username, password } = auth.props;
        return {
          Authorization: `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`
        };
      }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  oracleFusionCloudErp
});
