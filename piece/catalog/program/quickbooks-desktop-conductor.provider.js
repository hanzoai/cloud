"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/index.ts
var index_exports = {};
__export(index_exports, {
  quickbooksDesktopConductor: () => quickbooksDesktopConductor
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common15 = require("@activepieces/pieces-common"), import_pieces_framework23 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/common/errors.ts
var TRANSIENT_CONDUCTOR_ERROR_CODES = /* @__PURE__ */ new Set([
  "QBD_CONNECTION_ERROR",
  "QBD_REQUEST_TIMEOUT"
]), NOT_FOUND_MESSAGE_PATTERN = /could not be found in QuickBooks/i, STALE_REVISION_MESSAGE_PATTERN = /revision number \(edit sequence\).*is out-of-date/i, RECORD_LOCKED_MESSAGE_PATTERN = /already in use/i;
function safeJsonParse(value) {
  try {
    return JSON.parse(value);
  } catch (e) {
    return;
  }
}
function parseConductorError(error) {
  var _a, _b, _c, _d, _e;
  let responseBody = (_a = error == null ? void 0 : error.response) == null ? void 0 : _a.body, parsedBody = typeof responseBody == "string" ? safeJsonParse(responseBody) : responseBody, conductorError = parsedBody == null ? void 0 : parsedBody.error;
  return conductorError ? {
    // userFacingMessage is accurate and worth surfacing verbatim for real QuickBooks errors —
    // except INVALID_REQUEST_ERROR (Conductor's own request-shape validation, before it ever
    // reaches QuickBooks), which ships a useless generic userFacingMessage while `message`
    // already has the real, specific reason. Prefer `message` for that one type only.
    message: conductorError.type === "INVALID_REQUEST_ERROR" ? (_c = (_b = conductorError.message) != null ? _b : conductorError.userFacingMessage) != null ? _c : "Unknown Conductor error" : (_e = (_d = conductorError.userFacingMessage) != null ? _d : conductorError.message) != null ? _e : "Unknown Conductor error",
    code: conductorError.code,
    type: conductorError.type,
    httpStatusCode: conductorError.httpStatusCode
  } : { message: error instanceof Error ? error.message : "Unknown Conductor error" };
}
var ConductorApiError = class extends Error {
  constructor(rawError) {
    let { message, code, type, httpStatusCode } = parseConductorError(rawError);
    super(message);
    __publicField(this, "code");
    __publicField(this, "type");
    __publicField(this, "httpStatusCode");
    __publicField(this, "isTransient");
    /**
     * True when this error is QuickBooks Desktop's "no record matched an exact-name filter"
     * signal disguised as a request error, rather than a genuine failure. Callers doing a
     * lookup-then-upsert should treat this as "no existing record," not rethrow it.
     */
    __publicField(this, "isNotFound");
    /**
     * True when an update was rejected because its `revisionNumber` is stale — the record changed
     * since the caller last fetched it. Recoverable by re-fetching and retrying once; see
     * `withStaleRevisionRetry` in `client.ts`.
     */
    __publicField(this, "isStaleRevision");
    /**
     * True when QuickBooks Desktop itself rejected the request because the record is currently
     * being processed by another in-flight request against the same company file. Recoverable by
     * waiting briefly and retrying the identical request; see `withRecordLockRetry` in `client.ts`.
     */
    __publicField(this, "isRecordLocked");
    this.name = "ConductorApiError", this.code = code, this.type = type, this.httpStatusCode = httpStatusCode, this.isTransient = code !== void 0 && TRANSIENT_CONDUCTOR_ERROR_CODES.has(code), this.isNotFound = code === "QBD_REQUEST_ERROR" && NOT_FOUND_MESSAGE_PATTERN.test(message), this.isStaleRevision = code === "QBD_REQUEST_ERROR" && STALE_REVISION_MESSAGE_PATTERN.test(message), this.isRecordLocked = code === "QBD_REQUEST_ERROR" && RECORD_LOCKED_MESSAGE_PATTERN.test(message);
  }
};

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/common/client.ts
var CONDUCTOR_API_BASE_URL = "https://api.conductor.is/v1", TRANSIENT_HTTP_RETRIES = 2, conductorClient = {
  /**
   * `safeToRetry` (default `true`) controls whether a transient failure (timeout, 5xx) gets
   * `httpClient`'s automatic retry. Conductor has no idempotency-key mechanism — its
   * `Conductor-Request-Id` is a response-only tracking id for support, not something you can send
   * to dedupe a resent request — so retrying a create blindly can duplicate a real accounting
   * record if the original request actually succeeded server-side but its response was lost.
   * Reads are always safe to retry (the default). Updates-by-id are also safe by default: a
   * resent update either lands on the same `revisionNumber` result or gets rejected as stale by
   * `withStaleRevisionRetry`, neither of which duplicates anything. Every call that **creates** a
   * new record (no id in the URL) must explicitly pass `safeToRetry: false`.
   */
  async request({
    auth,
    method,
    resourceUri,
    body,
    queryParams,
    safeToRetry = !0
  }) {
    let request = {
      method,
      url: `${CONDUCTOR_API_BASE_URL}${resourceUri}`,
      headers: {
        Authorization: `Bearer ${auth.secretKey}`,
        "Conductor-End-User-Id": auth.endUserId
      },
      body,
      queryParams,
      retries: safeToRetry ? TRANSIENT_HTTP_RETRIES : 0
    };
    try {
      return (await import_pieces_common.httpClient.sendRequest(request)).body;
    } catch (error) {
      throw new ConductorApiError(error);
    }
  },
  async healthCheck(auth) {
    return conductorClient.request({
      auth,
      method: import_pieces_common.HttpMethod.GET,
      resourceUri: "/quickbooks-desktop/health-check"
    });
  }
};
async function withStaleRevisionRetry({
  attempt,
  refetchRevisionNumber,
  revisionNumber
}) {
  try {
    return await attempt(revisionNumber);
  } catch (error) {
    if (error instanceof ConductorApiError && error.isStaleRevision) {
      let freshRevisionNumber = await refetchRevisionNumber();
      return attempt(freshRevisionNumber);
    }
    throw error;
  }
}
var RECORD_LOCK_RETRY_DELAY_MS = 1500;
function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
async function withRecordLockRetry(attempt) {
  try {
    return await attempt();
  } catch (error) {
    if (error instanceof ConductorApiError && error.isRecordLocked)
      return await delay(RECORD_LOCK_RETRY_DELAY_MS), attempt();
    throw error;
  }
}

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/auth.ts
var authDescription = `
This piece connects to QuickBooks Desktop through [Conductor](https://conductor.is) \u2014 Activepieces
cannot talk to QuickBooks Desktop directly, since it has no cloud API of its own.

1. In your Conductor dashboard, go to **Settings \u2192 API Keys** and copy your **Secret Key**.
2. Under **End-Users**, select (or create) the end-user connected to this QuickBooks Desktop
   company file and copy their **End-User ID** (starts with \`end_usr_\`).
3. The end-user must have already completed Conductor's auth flow and installed the QuickBooks
   Web Connector \u2014 Conductor's dashboard shows the connection as "Connected" once QuickBooks
   Desktop is reachable. If it shows "Not connected," fix that in Conductor before connecting here.
4. **Top setup mistake \u2014 read before finishing step 3:** when QuickBooks Desktop first asks to
   authorize the Web Connector, **choose the option that keeps access even when QuickBooks Desktop
   isn't running** (not "ask every time"). Picking the wrong option doesn't error \u2014 it just makes
   every sync silently do nothing until someone is sitting at the QuickBooks Desktop machine to
   approve it, which usually isn't noticed until a customer asks why nothing has synced in days.
   See this piece's README for the full walkthrough.
`, quickbooksDesktopConductorAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "Connection",
  description: authDescription,
  required: !0,
  props: {
    secretKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Conductor Secret Key",
      description: "From your Conductor dashboard, Settings \u2192 API Keys.",
      required: !0
    }),
    endUserId: import_pieces_framework.Property.ShortText({
      displayName: "Conductor End-User ID",
      description: "The end-user ID for this QuickBooks Desktop company file (starts with end_usr_).",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await conductorClient.healthCheck({
        secretKey: auth.secretKey,
        endUserId: auth.endUserId
      }), { valid: !0 };
    } catch (error) {
      return {
        valid: !1,
        error: error instanceof Error ? error.message : "Could not connect to QuickBooks Desktop through Conductor."
      };
    }
  }
});

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/actions/upsert-customer.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/common/address.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var billingAddressProps = {
  billingLine1: import_pieces_framework2.Property.ShortText({ displayName: "Address Line 1", required: !1 }),
  billingCity: import_pieces_framework2.Property.ShortText({ displayName: "City", required: !1 }),
  billingState: import_pieces_framework2.Property.ShortText({ displayName: "State", required: !1 }),
  billingPostalCode: import_pieces_framework2.Property.ShortText({ displayName: "Postal Code", required: !1 }),
  billingCountry: import_pieces_framework2.Property.ShortText({ displayName: "Country", required: !1 })
}, billingAddressPropertyGroup = {
  key: "billing",
  display: "section",
  label: "Billing Address",
  props: ["billingLine1", "billingCity", "billingState", "billingPostalCode", "billingCountry"]
};
function buildBillingAddress(propsValue) {
  if (propsValue.billingLine1 || propsValue.billingCity || propsValue.billingState || propsValue.billingPostalCode || propsValue.billingCountry)
    return __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, (0, import_pieces_framework3.spreadIfDefined)("line1", propsValue.billingLine1)), (0, import_pieces_framework3.spreadIfDefined)("city", propsValue.billingCity)), (0, import_pieces_framework3.spreadIfDefined)("state", propsValue.billingState)), (0, import_pieces_framework3.spreadIfDefined)("postalCode", propsValue.billingPostalCode)), (0, import_pieces_framework3.spreadIfDefined)("country", propsValue.billingCountry));
}

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/output-schemas.ts
var upsertCustomerActionOutputSchema = {
  fields: [
    { key: "id", label: "Customer ID" },
    { key: "name", label: "Name" },
    { key: "full_name", label: "Full Name" },
    { key: "company_name", label: "Company Name" },
    { key: "is_active", label: "Is Active", format: "boolean" },
    { key: "email", label: "Email", format: "email" },
    { key: "phone", label: "Phone" },
    { key: "note", label: "Note" },
    { key: "billing_address_line1", label: "Billing Address Line 1" },
    { key: "billing_address_city", label: "Billing Address City" },
    { key: "billing_address_state", label: "Billing Address State" },
    { key: "billing_address_postal_code", label: "Billing Address Postal Code" },
    { key: "billing_address_country", label: "Billing Address Country" },
    { key: "revision_number", label: "Revision Number" },
    { key: "created_at", label: "Created At", format: "datetime" },
    { key: "updated_at", label: "Updated At", format: "datetime" }
  ]
}, createInvoiceActionOutputSchema = {
  fields: [
    { key: "id", label: "Invoice ID" },
    { key: "transaction_date", label: "Invoice Date", format: "date" },
    { key: "due_date", label: "Due Date", format: "date" },
    { key: "ref_number", label: "Invoice Number" },
    { key: "memo", label: "Memo" },
    { key: "customer_id", label: "Customer ID" },
    { key: "customer_name", label: "Customer Name" },
    { key: "subtotal_amount", label: "Subtotal", format: "currency", currency: "USD" },
    { key: "sales_tax_amount", label: "Sales Tax", format: "currency", currency: "USD" },
    { key: "total_amount", label: "Total", format: "currency", currency: "USD" },
    { key: "balance_remaining", label: "Balance Remaining", format: "currency", currency: "USD" },
    { key: "is_paid", label: "Is Paid", format: "boolean" },
    { key: "line_count", label: "Line Item Count", format: "number" },
    { key: "revision_number", label: "Revision Number" },
    { key: "created_at", label: "Created At", format: "datetime" },
    { key: "updated_at", label: "Updated At", format: "datetime" }
  ]
}, createBillActionOutputSchema = {
  fields: [
    { key: "id", label: "Bill ID" },
    { key: "transaction_date", label: "Bill Date", format: "date" },
    { key: "due_date", label: "Due Date", format: "date" },
    { key: "ref_number", label: "Bill Number" },
    { key: "memo", label: "Memo" },
    { key: "vendor_id", label: "Vendor ID" },
    { key: "vendor_name", label: "Vendor Name" },
    { key: "amount_due", label: "Amount Due", format: "currency", currency: "USD" },
    { key: "balance_remaining", label: "Balance Remaining", format: "currency", currency: "USD" },
    { key: "is_paid", label: "Is Paid", format: "boolean" },
    { key: "line_count", label: "Expense Line Count", format: "number" },
    { key: "revision_number", label: "Revision Number" },
    { key: "created_at", label: "Created At", format: "datetime" },
    { key: "updated_at", label: "Updated At", format: "datetime" }
  ]
}, recordPaymentActionOutputSchema = {
  fields: [
    { key: "id", label: "Payment ID" },
    { key: "payment_type", label: "Payment Type" },
    { key: "transaction_date", label: "Payment Date", format: "date" },
    { key: "ref_number", label: "Reference / Check Number" },
    { key: "memo", label: "Memo" },
    { key: "amount", label: "Amount", format: "currency", currency: "USD" },
    { key: "customer_id", label: "Customer ID" },
    { key: "customer_name", label: "Customer Name" },
    { key: "vendor_id", label: "Vendor ID" },
    { key: "vendor_name", label: "Vendor Name" },
    { key: "revision_number", label: "Revision Number" },
    { key: "created_at", label: "Created At", format: "datetime" },
    { key: "updated_at", label: "Updated At", format: "datetime" }
  ]
}, queryTransactionsActionOutputSchema = {
  fields: [
    {
      key: "transactions",
      label: "Transactions",
      labelKey: "ref_number",
      listItems: [
        { key: "transaction_type", label: "Type" },
        { key: "transaction_id", label: "Transaction ID" },
        { key: "transaction_date", label: "Date", format: "date" },
        { key: "ref_number", label: "Reference Number" },
        { key: "amount", label: "Amount", format: "currency", currency: "USD" },
        { key: "memo", label: "Memo" },
        { key: "entity_id", label: "Entity ID" },
        { key: "entity_name", label: "Entity Name" },
        { key: "account_id", label: "Account ID" },
        { key: "account_name", label: "Account Name" },
        { key: "created_at", label: "Created At", format: "datetime" },
        { key: "updated_at", label: "Updated At", format: "datetime" }
      ]
    },
    { key: "count", label: "Result Count", format: "number" },
    { key: "next_cursor", label: "Next Cursor" },
    { key: "has_more", label: "Has More", format: "boolean" }
  ]
}, newOrUpdatedInvoiceTriggerOutputSchema = createInvoiceActionOutputSchema, newPaymentTriggerOutputSchema = recordPaymentActionOutputSchema, upsertVendorActionOutputSchema = {
  fields: [
    { key: "id", label: "Vendor ID" },
    { key: "name", label: "Name" },
    { key: "company_name", label: "Company Name" },
    { key: "is_active", label: "Is Active", format: "boolean" },
    { key: "email", label: "Email", format: "email" },
    { key: "phone", label: "Phone" },
    { key: "note", label: "Note" },
    { key: "billing_address_line1", label: "Billing Address Line 1" },
    { key: "billing_address_city", label: "Billing Address City" },
    { key: "billing_address_state", label: "Billing Address State" },
    { key: "billing_address_postal_code", label: "Billing Address Postal Code" },
    { key: "billing_address_country", label: "Billing Address Country" },
    { key: "revision_number", label: "Revision Number" },
    { key: "created_at", label: "Created At", format: "datetime" },
    { key: "updated_at", label: "Updated At", format: "datetime" }
  ]
}, listItemsActionOutputSchema = {
  fields: [
    {
      key: "items",
      label: "Items",
      labelKey: "name",
      listItems: [
        { key: "id", label: "Item ID" },
        { key: "name", label: "Name" },
        { key: "full_name", label: "Full Name" },
        { key: "item_type", label: "Item Type" },
        { key: "is_active", label: "Is Active", format: "boolean" }
      ]
    },
    { key: "count", label: "Result Count", format: "number" },
    { key: "has_more", label: "Has More", format: "boolean" }
  ]
};

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/actions/upsert-customer.ts
var MAX_NAME_LENGTH = 41;
async function lookupCustomerByName({
  auth,
  name
}) {
  let { data, error } = await (0, import_pieces_framework5.tryCatch)(
    () => conductorClient.request({
      auth,
      method: import_pieces_common2.HttpMethod.GET,
      resourceUri: "/quickbooks-desktop/customers",
      queryParams: { fullNames: name }
    })
  );
  if (error) {
    if (error instanceof ConductorApiError && error.isNotFound)
      return;
    throw error;
  }
  return data.data[0];
}
function flattenCustomer(customer) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
  return {
    id: customer.id,
    name: customer.name,
    full_name: customer.fullName,
    company_name: customer.companyName,
    is_active: customer.isActive,
    email: customer.email,
    phone: customer.phone,
    note: customer.note,
    billing_address_line1: (_b = (_a = customer.billingAddress) == null ? void 0 : _a.line1) != null ? _b : null,
    billing_address_city: (_d = (_c = customer.billingAddress) == null ? void 0 : _c.city) != null ? _d : null,
    billing_address_state: (_f = (_e = customer.billingAddress) == null ? void 0 : _e.state) != null ? _f : null,
    billing_address_postal_code: (_h = (_g = customer.billingAddress) == null ? void 0 : _g.postalCode) != null ? _h : null,
    billing_address_country: (_j = (_i = customer.billingAddress) == null ? void 0 : _i.country) != null ? _j : null,
    revision_number: customer.revisionNumber,
    created_at: customer.createdAt,
    updated_at: customer.updatedAt
  };
}
var upsertCustomerAction = (0, import_pieces_framework4.createAction)({
  auth: quickbooksDesktopConductorAuth,
  name: "upsert_customer",
  classification: "WRITE",
  displayName: "Upsert Customer",
  description: "Creates a customer in QuickBooks Desktop, or updates it if one with the same name already exists.",
  audience: "both",
  aiMetadata: {
    description: "Create or update a QuickBooks Desktop customer, matched by exact name. Use this before creating an invoice or receiving a payment for a customer that may not exist yet \u2014 it looks the customer up first and updates it instead of creating a duplicate. Safe to retry: calling it again with the same name always resolves to the same customer record.",
    idempotent: !0
  },
  outputSchema: upsertCustomerActionOutputSchema,
  props: __spreadValues({
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Customer Name",
      description: `The exact name of the customer as it should appear in QuickBooks Desktop (max ${MAX_NAME_LENGTH} characters). Used to find an existing customer \u2014 if one already exists with this exact name, it is updated instead of duplicated.`,
      required: !0
    }),
    companyName: import_pieces_framework4.Property.ShortText({
      displayName: "Company Name",
      required: !1
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    phone: import_pieces_framework4.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    note: import_pieces_framework4.Property.LongText({
      displayName: "Note",
      required: !1
    })
  }, billingAddressProps),
  propertyGroups: [billingAddressPropertyGroup],
  async run(context) {
    let { propsValue } = context, auth = {
      secretKey: context.auth.props.secretKey,
      endUserId: context.auth.props.endUserId
    };
    if (propsValue.name.length > MAX_NAME_LENGTH)
      throw new Error(
        `Customer name must be ${MAX_NAME_LENGTH} characters or fewer (QuickBooks Desktop's limit) \u2014 "${propsValue.name}" is ${propsValue.name.length}.`
      );
    let billingAddress = buildBillingAddress(propsValue), body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      name: propsValue.name
    }, (0, import_pieces_framework5.spreadIfDefined)("companyName", propsValue.companyName)), (0, import_pieces_framework5.spreadIfDefined)("email", propsValue.email)), (0, import_pieces_framework5.spreadIfDefined)("phone", propsValue.phone)), (0, import_pieces_framework5.spreadIfDefined)("note", propsValue.note)), (0, import_pieces_framework5.spreadIfDefined)("billingAddress", billingAddress)), existingCustomer = await lookupCustomerByName({ auth, name: propsValue.name });
    if (existingCustomer) {
      let updatedCustomer = await withRecordLockRetry(
        () => withStaleRevisionRetry({
          revisionNumber: existingCustomer.revisionNumber,
          attempt: (revisionNumber) => conductorClient.request({
            auth,
            method: import_pieces_common2.HttpMethod.POST,
            resourceUri: `/quickbooks-desktop/customers/${existingCustomer.id}`,
            body: __spreadProps(__spreadValues({}, body), { revisionNumber })
          }),
          refetchRevisionNumber: async () => {
            let fresh = await lookupCustomerByName({ auth, name: propsValue.name });
            if (!fresh)
              throw new Error(`Customer "${propsValue.name}" no longer exists in QuickBooks Desktop.`);
            return fresh.revisionNumber;
          }
        })
      );
      return flattenCustomer(updatedCustomer);
    }
    let createdCustomer = await withRecordLockRetry(
      () => conductorClient.request({
        auth,
        method: import_pieces_common2.HttpMethod.POST,
        resourceUri: "/quickbooks-desktop/customers",
        body,
        // This is the create branch (no id in the URL) — see client.ts's `request` doc on why
        // creates opt out of retry. The update branch above keeps the default: a resent update
        // either matches the same revisionNumber or gets rejected as stale, never duplicated.
        safeToRetry: !1
      })
    );
    return flattenCustomer(createdCustomer);
  }
});

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/actions/upsert-vendor.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var MAX_NAME_LENGTH2 = 41;
async function lookupVendorByName({
  auth,
  name
}) {
  let { data, error } = await (0, import_pieces_framework7.tryCatch)(
    () => conductorClient.request({
      auth,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: "/quickbooks-desktop/vendors",
      // Vendors have no parent/child hierarchy, unlike customers, so the exact-match filter is
      // `names`, not `fullNames`.
      queryParams: { names: name }
    })
  );
  if (error) {
    if (error instanceof ConductorApiError && error.isNotFound)
      return;
    throw error;
  }
  return data.data[0];
}
function flattenVendor(vendor) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
  return {
    id: vendor.id,
    name: vendor.name,
    company_name: vendor.companyName,
    is_active: vendor.isActive,
    email: vendor.email,
    phone: vendor.phone,
    note: vendor.note,
    billing_address_line1: (_b = (_a = vendor.billingAddress) == null ? void 0 : _a.line1) != null ? _b : null,
    billing_address_city: (_d = (_c = vendor.billingAddress) == null ? void 0 : _c.city) != null ? _d : null,
    billing_address_state: (_f = (_e = vendor.billingAddress) == null ? void 0 : _e.state) != null ? _f : null,
    billing_address_postal_code: (_h = (_g = vendor.billingAddress) == null ? void 0 : _g.postalCode) != null ? _h : null,
    billing_address_country: (_j = (_i = vendor.billingAddress) == null ? void 0 : _i.country) != null ? _j : null,
    revision_number: vendor.revisionNumber,
    created_at: vendor.createdAt,
    updated_at: vendor.updatedAt
  };
}
var upsertVendorAction = (0, import_pieces_framework6.createAction)({
  auth: quickbooksDesktopConductorAuth,
  name: "upsert_vendor",
  classification: "WRITE",
  displayName: "Upsert Vendor",
  description: "Creates a vendor in QuickBooks Desktop, or updates it if one with the same name already exists.",
  audience: "both",
  aiMetadata: {
    description: "Create or update a QuickBooks Desktop vendor, matched by exact name. Use this before creating a bill or recording a bill payment for a vendor that may not exist yet \u2014 it looks the vendor up first and updates it instead of creating a duplicate. Safe to retry: calling it again with the same name always resolves to the same vendor record.",
    idempotent: !0
  },
  outputSchema: upsertVendorActionOutputSchema,
  props: __spreadValues({
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Vendor Name",
      description: `The exact name of the vendor as it should appear in QuickBooks Desktop (max ${MAX_NAME_LENGTH2} characters, unique across all vendors). Used to find an existing vendor \u2014 if one already exists with this exact name, it is updated instead of duplicated.`,
      required: !0
    }),
    companyName: import_pieces_framework6.Property.ShortText({
      displayName: "Company Name",
      required: !1
    }),
    email: import_pieces_framework6.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    phone: import_pieces_framework6.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    note: import_pieces_framework6.Property.LongText({
      displayName: "Note",
      required: !1
    })
  }, billingAddressProps),
  propertyGroups: [billingAddressPropertyGroup],
  async run(context) {
    let { propsValue } = context, auth = {
      secretKey: context.auth.props.secretKey,
      endUserId: context.auth.props.endUserId
    };
    if (propsValue.name.length > MAX_NAME_LENGTH2)
      throw new Error(
        `Vendor name must be ${MAX_NAME_LENGTH2} characters or fewer (QuickBooks Desktop's limit) \u2014 "${propsValue.name}" is ${propsValue.name.length}.`
      );
    let billingAddress = buildBillingAddress(propsValue), body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      name: propsValue.name
    }, (0, import_pieces_framework7.spreadIfDefined)("companyName", propsValue.companyName)), (0, import_pieces_framework7.spreadIfDefined)("email", propsValue.email)), (0, import_pieces_framework7.spreadIfDefined)("phone", propsValue.phone)), (0, import_pieces_framework7.spreadIfDefined)("note", propsValue.note)), (0, import_pieces_framework7.spreadIfDefined)("billingAddress", billingAddress)), existingVendor = await lookupVendorByName({ auth, name: propsValue.name });
    if (existingVendor) {
      let updatedVendor = await withRecordLockRetry(
        () => withStaleRevisionRetry({
          revisionNumber: existingVendor.revisionNumber,
          attempt: (revisionNumber) => conductorClient.request({
            auth,
            method: import_pieces_common3.HttpMethod.POST,
            resourceUri: `/quickbooks-desktop/vendors/${existingVendor.id}`,
            body: __spreadProps(__spreadValues({}, body), { revisionNumber })
          }),
          refetchRevisionNumber: async () => {
            let fresh = await lookupVendorByName({ auth, name: propsValue.name });
            if (!fresh)
              throw new Error(`Vendor "${propsValue.name}" no longer exists in QuickBooks Desktop.`);
            return fresh.revisionNumber;
          }
        })
      );
      return flattenVendor(updatedVendor);
    }
    let createdVendor = await withRecordLockRetry(
      () => conductorClient.request({
        auth,
        method: import_pieces_common3.HttpMethod.POST,
        resourceUri: "/quickbooks-desktop/vendors",
        body,
        // Create branch (no id in the URL) — see the matching comment in upsert-customer.ts.
        safeToRetry: !1
      })
    );
    return flattenVendor(createdVendor);
  }
});

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/actions/create-invoice.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/common/items.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
async function lookupItemByFullName({
  auth,
  resourceUri,
  name
}) {
  let { data, error } = await (0, import_pieces_framework8.tryCatch)(
    () => conductorClient.request({
      auth,
      method: import_pieces_common4.HttpMethod.GET,
      resourceUri,
      queryParams: { fullNames: name }
    })
  );
  if (error) {
    if (error instanceof ConductorApiError && error.isNotFound)
      return;
    throw error;
  }
  return data.data[0];
}
async function resolveItemIdByName({
  auth,
  name
}) {
  let serviceItem = await lookupItemByFullName({ auth, resourceUri: "/quickbooks-desktop/service-items", name });
  if (serviceItem)
    return serviceItem.id;
  let nonInventoryItem = await lookupItemByFullName({ auth, resourceUri: "/quickbooks-desktop/non-inventory-items", name });
  if (nonInventoryItem)
    return nonInventoryItem.id;
  throw new Error(
    `Item "${name}" was not found among Service Items or Non-Inventory Items in QuickBooks Desktop. Check the exact name in QuickBooks Desktop's Item List (Lists > Item List).`
  );
}

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/common/dropdowns.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
function customerIdDropdown({ required, description }) {
  return import_pieces_framework9.Property.Dropdown({
    displayName: "Customer",
    description,
    auth: quickbooksDesktopConductorAuth,
    required,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return { disabled: !0, placeholder: "Connect your account first", options: [] };
      let conductorAuth = { secretKey: auth.props.secretKey, endUserId: auth.props.endUserId };
      return { disabled: !1, options: (await conductorClient.request({
        auth: conductorAuth,
        method: import_pieces_common5.HttpMethod.GET,
        resourceUri: "/quickbooks-desktop/customers"
      })).data.map((customer) => ({ label: customer.fullName, value: customer.id })) };
    }
  });
}
function vendorIdDropdown({ required, description }) {
  return import_pieces_framework9.Property.Dropdown({
    displayName: "Vendor",
    description,
    auth: quickbooksDesktopConductorAuth,
    required,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return { disabled: !0, placeholder: "Connect your account first", options: [] };
      let conductorAuth = { secretKey: auth.props.secretKey, endUserId: auth.props.endUserId };
      return { disabled: !1, options: (await conductorClient.request({
        auth: conductorAuth,
        method: import_pieces_common5.HttpMethod.GET,
        resourceUri: "/quickbooks-desktop/vendors"
      })).data.map((vendor) => ({ label: vendor.name, value: vendor.id })) };
    }
  });
}

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/common/invoices.ts
function addDecimalStrings(a, b) {
  return (Number(a) + Number(b)).toFixed(2);
}
function flattenInvoice(invoice) {
  var _a, _b, _c, _d, _e, _f, _g;
  return {
    id: invoice.id,
    transaction_date: invoice.transactionDate,
    due_date: invoice.dueDate,
    ref_number: invoice.refNumber,
    memo: invoice.memo,
    customer_id: (_b = (_a = invoice.customer) == null ? void 0 : _a.id) != null ? _b : null,
    customer_name: (_d = (_c = invoice.customer) == null ? void 0 : _c.fullName) != null ? _d : null,
    subtotal_amount: invoice.subtotal,
    sales_tax_amount: invoice.salesTaxTotal,
    total_amount: addDecimalStrings(invoice.subtotal, (_e = invoice.salesTaxTotal) != null ? _e : "0"),
    balance_remaining: invoice.balanceRemaining,
    is_paid: invoice.isPaid,
    line_count: (_g = (_f = invoice.lines) == null ? void 0 : _f.length) != null ? _g : 0,
    revision_number: invoice.revisionNumber,
    created_at: invoice.createdAt,
    updated_at: invoice.updatedAt
  };
}

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/actions/create-invoice.ts
var MAX_REF_NUMBER_LENGTH = 11;
function toDateOnly(isoDateTime) {
  return isoDateTime.split("T")[0];
}
var createInvoiceAction = (0, import_pieces_framework10.createAction)({
  auth: quickbooksDesktopConductorAuth,
  name: "create_invoice",
  classification: "WRITE",
  displayName: "Create Invoice",
  description: "Creates an invoice for a customer in QuickBooks Desktop.",
  audience: "both",
  aiMetadata: {
    description: "Create a new invoice for an existing QuickBooks Desktop customer, with one or more line items. Each line item references a Service or Non-Inventory item by its exact name. Not idempotent \u2014 each call creates a new invoice, so retries duplicate it; use Query Transactions first to check whether an equivalent invoice already exists.",
    idempotent: !1
  },
  outputSchema: createInvoiceActionOutputSchema,
  props: {
    customerId: customerIdDropdown({
      required: !0,
      description: "The customer this invoice is for. Only customers that already exist in QuickBooks Desktop appear here \u2014 use Upsert Customer first if the customer might not exist yet."
    }),
    transactionDate: import_pieces_framework10.Property.DateTime({
      displayName: "Invoice Date",
      required: !0
    }),
    dueDate: import_pieces_framework10.Property.DateTime({
      displayName: "Due Date",
      description: "When left blank, QuickBooks Desktop derives it from the customer's default payment terms.",
      required: !1
    }),
    refNumber: import_pieces_framework10.Property.ShortText({
      displayName: "Invoice Number",
      description: `Optional reference number (max ${MAX_REF_NUMBER_LENGTH} characters). QuickBooks Desktop does not auto-generate one if left blank.`,
      required: !1
    }),
    memo: import_pieces_framework10.Property.LongText({
      displayName: "Memo",
      description: "Internal note. Appears in reports, not on the invoice sent to the customer.",
      required: !1
    }),
    lineItems: import_pieces_framework10.Property.Array({
      displayName: "Line Items",
      description: "At least one line item is required.",
      required: !0,
      properties: {
        itemName: import_pieces_framework10.Property.ShortText({
          displayName: "Item Name",
          description: "The exact name of a Service or Non-Inventory item in QuickBooks Desktop (Lists > Item List).",
          required: !0
        }),
        description: import_pieces_framework10.Property.ShortText({
          displayName: "Description",
          required: !1
        }),
        quantity: import_pieces_framework10.Property.Number({
          displayName: "Quantity",
          required: !1
        }),
        rate: import_pieces_framework10.Property.ShortText({
          displayName: "Rate",
          description: 'Price per unit, e.g. "125.00". Ignored if Amount is set.',
          required: !1
        }),
        amount: import_pieces_framework10.Property.ShortText({
          displayName: "Amount",
          description: 'Total for this line, e.g. "500.00". Calculated from Quantity \xD7 Rate if left blank.',
          required: !1
        })
      }
    })
  },
  async run(context) {
    let { propsValue } = context, auth = {
      secretKey: context.auth.props.secretKey,
      endUserId: context.auth.props.endUserId
    }, lineItemInputs = propsValue.lineItems;
    if (lineItemInputs.length === 0)
      throw new Error("At least one line item is required to create an invoice.");
    if (propsValue.refNumber && propsValue.refNumber.length > MAX_REF_NUMBER_LENGTH)
      throw new Error(
        `Invoice Number must be ${MAX_REF_NUMBER_LENGTH} characters or fewer (QuickBooks Desktop's limit) \u2014 "${propsValue.refNumber}" is ${propsValue.refNumber.length}.`
      );
    let lines = await Promise.all(
      lineItemInputs.map(async (line) => {
        let itemId = await resolveItemIdByName({ auth, name: line.itemName });
        return __spreadValues(__spreadValues(__spreadValues(__spreadValues({
          itemId
        }, (0, import_pieces_framework11.spreadIfDefined)("description", line.description)), (0, import_pieces_framework11.spreadIfDefined)("quantity", line.quantity)), (0, import_pieces_framework11.spreadIfDefined)("rate", line.rate)), (0, import_pieces_framework11.spreadIfDefined)("amount", line.amount));
      })
    ), body = __spreadProps(__spreadValues(__spreadValues(__spreadValues({
      customerId: propsValue.customerId,
      transactionDate: toDateOnly(propsValue.transactionDate)
    }, (0, import_pieces_framework11.spreadIfDefined)("dueDate", propsValue.dueDate ? toDateOnly(propsValue.dueDate) : void 0)), (0, import_pieces_framework11.spreadIfDefined)("refNumber", propsValue.refNumber)), (0, import_pieces_framework11.spreadIfDefined)("memo", propsValue.memo)), {
      lines
    }), createdInvoice = await withRecordLockRetry(
      () => conductorClient.request({
        auth,
        method: import_pieces_common6.HttpMethod.POST,
        resourceUri: "/quickbooks-desktop/invoices",
        body,
        // This creates a new invoice — Conductor has no idempotency key, so a blind transport
        // retry on a lost response would create a duplicate. See client.ts's `request` doc.
        safeToRetry: !1
      })
    );
    return flattenInvoice(createdInvoice);
  }
});

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/actions/create-bill.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/common/accounts.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework");
async function resolveAccountIdByName({
  auth,
  name
}) {
  let { data, error } = await (0, import_pieces_framework12.tryCatch)(
    () => conductorClient.request({
      auth,
      method: import_pieces_common7.HttpMethod.GET,
      resourceUri: "/quickbooks-desktop/accounts",
      queryParams: { fullNames: name }
    })
  );
  if (error)
    throw error instanceof ConductorApiError && error.isNotFound ? new Error(
      `Account "${name}" was not found in QuickBooks Desktop's Chart of Accounts. Check the exact name (Lists > Chart of Accounts) \u2014 sub-accounts use "Parent:Child" form, e.g. "Expenses:Fuel".`
    ) : error;
  return data.data[0].id;
}

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/actions/create-bill.ts
var MAX_REF_NUMBER_LENGTH2 = 20;
function toDateOnly2(isoDateTime) {
  return isoDateTime.split("T")[0];
}
function flattenBill(bill) {
  var _a, _b, _c, _d, _e, _f;
  return {
    id: bill.id,
    transaction_date: bill.transactionDate,
    due_date: bill.dueDate,
    ref_number: bill.refNumber,
    memo: bill.memo,
    vendor_id: (_b = (_a = bill.vendor) == null ? void 0 : _a.id) != null ? _b : null,
    vendor_name: (_d = (_c = bill.vendor) == null ? void 0 : _c.fullName) != null ? _d : null,
    amount_due: bill.amountDue,
    balance_remaining: bill.openAmount,
    is_paid: bill.isPaid,
    line_count: (_f = (_e = bill.expenseLines) == null ? void 0 : _e.length) != null ? _f : 0,
    revision_number: bill.revisionNumber,
    created_at: bill.createdAt,
    updated_at: bill.updatedAt
  };
}
var createBillAction = (0, import_pieces_framework13.createAction)({
  auth: quickbooksDesktopConductorAuth,
  name: "create_bill",
  classification: "WRITE",
  displayName: "Create Bill",
  description: "Creates a vendor bill (accounts payable) in QuickBooks Desktop, expensed against one or more accounts.",
  audience: "both",
  aiMetadata: {
    description: "Record a new bill from an existing vendor, expensed against one or more Chart of Accounts entries (fuel, maintenance, insurance, etc.) \u2014 this is the accounts-payable expense-line shape, not an item/inventory purchase. Not idempotent \u2014 each call creates a new bill, so retries duplicate it; use Query Transactions first to check whether an equivalent bill already exists.",
    idempotent: !1
  },
  outputSchema: createBillActionOutputSchema,
  props: {
    vendorId: vendorIdDropdown({
      required: !0,
      description: "The vendor this bill is from. Only vendors that already exist in QuickBooks Desktop appear here \u2014 use Upsert Vendor first if the vendor might not exist yet."
    }),
    transactionDate: import_pieces_framework13.Property.DateTime({
      displayName: "Bill Date",
      required: !0
    }),
    dueDate: import_pieces_framework13.Property.DateTime({
      displayName: "Due Date",
      description: "When left blank, QuickBooks Desktop derives it from the vendor's default payment terms.",
      required: !1
    }),
    refNumber: import_pieces_framework13.Property.ShortText({
      displayName: "Bill Number",
      description: `Optional reference number (max ${MAX_REF_NUMBER_LENGTH2} characters). QuickBooks Desktop does not auto-generate one if left blank.`,
      required: !1
    }),
    memo: import_pieces_framework13.Property.LongText({
      displayName: "Memo",
      description: "Internal note. Appears in the A/P register and reports, not sent to the vendor.",
      required: !1
    }),
    expenseLines: import_pieces_framework13.Property.Array({
      displayName: "Expense Lines",
      description: "At least one expense line is required. Each line expenses this bill against one Chart of Accounts entry (e.g. fuel, maintenance, insurance) \u2014 not a product/service item.",
      required: !0,
      properties: {
        accountName: import_pieces_framework13.Property.ShortText({
          displayName: "Account Name",
          description: 'The exact Chart of Accounts name in QuickBooks Desktop, e.g. "Automobile Expense" or "Expenses:Fuel" for a sub-account.',
          required: !0
        }),
        amount: import_pieces_framework13.Property.ShortText({
          displayName: "Amount",
          description: 'The amount to expense to this account, e.g. "450.00".',
          required: !0
        }),
        memo: import_pieces_framework13.Property.ShortText({
          displayName: "Line Memo",
          required: !1
        })
      }
    })
  },
  async run(context) {
    let { propsValue } = context, auth = {
      secretKey: context.auth.props.secretKey,
      endUserId: context.auth.props.endUserId
    }, expenseLineInputs = propsValue.expenseLines;
    if (expenseLineInputs.length === 0)
      throw new Error("At least one expense line is required to create a bill.");
    if (propsValue.refNumber && propsValue.refNumber.length > MAX_REF_NUMBER_LENGTH2)
      throw new Error(
        `Bill Number must be ${MAX_REF_NUMBER_LENGTH2} characters or fewer (QuickBooks Desktop's limit) \u2014 "${propsValue.refNumber}" is ${propsValue.refNumber.length}.`
      );
    let expenseLines = await Promise.all(
      expenseLineInputs.map(async (line) => {
        let accountId = await resolveAccountIdByName({ auth, name: line.accountName });
        return __spreadValues({
          accountId,
          amount: line.amount
        }, (0, import_pieces_framework14.spreadIfDefined)("memo", line.memo));
      })
    ), body = __spreadProps(__spreadValues(__spreadValues(__spreadValues({
      vendorId: propsValue.vendorId,
      transactionDate: toDateOnly2(propsValue.transactionDate)
    }, (0, import_pieces_framework14.spreadIfDefined)("dueDate", propsValue.dueDate ? toDateOnly2(propsValue.dueDate) : void 0)), (0, import_pieces_framework14.spreadIfDefined)("refNumber", propsValue.refNumber)), (0, import_pieces_framework14.spreadIfDefined)("memo", propsValue.memo)), {
      expenseLines
    }), createdBill = await withRecordLockRetry(
      () => conductorClient.request({
        auth,
        method: import_pieces_common8.HttpMethod.POST,
        resourceUri: "/quickbooks-desktop/bills",
        body,
        // This creates a new bill — see client.ts's `request` doc on why creates opt out of retry.
        safeToRetry: !1
      })
    );
    return flattenBill(createdBill);
  }
});

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/actions/record-payment.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework16 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/common/payments.ts
function isPaymentType(value) {
  return value === "customer_payment" || value === "bill_payment_check" || value === "bill_payment_credit_card";
}
function flattenPayment({ payment, paymentType }) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i;
  return {
    id: payment.id,
    payment_type: paymentType,
    transaction_date: payment.transactionDate,
    ref_number: payment.refNumber,
    memo: payment.memo,
    amount: (_a = payment.totalAmount) != null ? _a : payment.amount,
    customer_id: (_c = (_b = payment.customer) == null ? void 0 : _b.id) != null ? _c : null,
    customer_name: (_e = (_d = payment.customer) == null ? void 0 : _d.fullName) != null ? _e : null,
    vendor_id: (_g = (_f = payment.vendor) == null ? void 0 : _f.id) != null ? _g : null,
    vendor_name: (_i = (_h = payment.vendor) == null ? void 0 : _h.fullName) != null ? _i : null,
    revision_number: payment.revisionNumber,
    created_at: payment.createdAt,
    updated_at: payment.updatedAt
  };
}

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/actions/record-payment.ts
var CUSTOMER_PAYMENT_MAX_REF_LENGTH = 20, BILL_PAYMENT_MAX_REF_LENGTH = 11;
function toDateOnly3(isoDateTime) {
  return isoDateTime.split("T")[0];
}
var recordPaymentAction = (0, import_pieces_framework15.createAction)({
  auth: quickbooksDesktopConductorAuth,
  name: "record_payment",
  classification: "WRITE",
  displayName: "Record Payment",
  description: "Records a customer payment against open invoices, or a vendor bill payment by check or credit card, in QuickBooks Desktop.",
  audience: "both",
  aiMetadata: {
    description: "Record a payment in QuickBooks Desktop \u2014 either a customer payment against open invoices (Accounts Receivable) or a vendor bill payment by check or credit card (Accounts Payable), chosen via Payment Type. Not idempotent \u2014 each call records a new payment, so retries duplicate it.",
    idempotent: !1
  },
  outputSchema: recordPaymentActionOutputSchema,
  props: {
    paymentType: import_pieces_framework15.Property.StaticDropdown({
      displayName: "Payment Type",
      required: !0,
      options: {
        options: [
          { label: "Customer Payment (Accounts Receivable)", value: "customer_payment" },
          { label: "Vendor Bill Payment \u2014 Check (Accounts Payable)", value: "bill_payment_check" },
          { label: "Vendor Bill Payment \u2014 Credit Card (Accounts Payable)", value: "bill_payment_credit_card" }
        ]
      }
    }),
    transactionDate: import_pieces_framework15.Property.DateTime({
      displayName: "Payment Date",
      required: !0
    }),
    amount: import_pieces_framework15.Property.ShortText({
      displayName: "Amount",
      description: 'Total payment amount, e.g. "500.00".',
      required: !0
    }),
    refNumber: import_pieces_framework15.Property.ShortText({
      displayName: "Reference / Check Number",
      description: `Optional. Max ${CUSTOMER_PAYMENT_MAX_REF_LENGTH} characters for a customer payment, max ${BILL_PAYMENT_MAX_REF_LENGTH} for a bill payment (it's the check number for a check payment).`,
      required: !1
    }),
    memo: import_pieces_framework15.Property.LongText({
      displayName: "Memo",
      required: !1
    }),
    customerId: customerIdDropdown({
      required: !1,
      description: "Required for Customer Payment only. The customer who made this payment."
    }),
    applyToSpecificInvoice: import_pieces_framework15.Property.Checkbox({
      displayName: "Apply to a specific invoice",
      description: "Customer Payment only. When off, QuickBooks Desktop automatically applies the payment to an exact-amount match or the oldest open invoice.",
      required: !1,
      defaultValue: !1,
      reveals: ["invoiceId"]
    }),
    invoiceId: import_pieces_framework15.Property.Dropdown({
      displayName: "Invoice",
      description: "The specific open invoice this payment applies to.",
      auth: quickbooksDesktopConductorAuth,
      required: !1,
      refreshers: ["customerId"],
      options: async ({ auth, customerId }) => {
        if (!auth || typeof customerId != "string" || customerId.length === 0)
          return { disabled: !0, placeholder: "Select a customer first", options: [] };
        let conductorAuth = { secretKey: auth.props.secretKey, endUserId: auth.props.endUserId };
        return {
          disabled: !1,
          options: (await conductorClient.request({
            auth: conductorAuth,
            method: import_pieces_common9.HttpMethod.GET,
            resourceUri: "/quickbooks-desktop/invoices",
            queryParams: { customerIds: customerId, paymentStatus: "not_paid" }
          })).data.map((inv) => {
            var _a;
            return {
              label: `${(_a = inv.refNumber) != null ? _a : inv.id} \u2014 $${inv.balanceRemaining} due (${inv.transactionDate})`,
              value: inv.id
            };
          })
        };
      }
    }),
    vendorId: vendorIdDropdown({
      required: !1,
      description: "Required for both Bill Payment types. The vendor being paid."
    }),
    billId: import_pieces_framework15.Property.Dropdown({
      displayName: "Bill",
      description: "Required for both Bill Payment types. QuickBooks Desktop has no auto-apply for bill payments, so the specific open bill must be picked.",
      auth: quickbooksDesktopConductorAuth,
      required: !1,
      refreshers: ["vendorId"],
      options: async ({ auth, vendorId }) => {
        if (!auth || typeof vendorId != "string" || vendorId.length === 0)
          return { disabled: !0, placeholder: "Select a vendor first", options: [] };
        let conductorAuth = { secretKey: auth.props.secretKey, endUserId: auth.props.endUserId };
        return {
          disabled: !1,
          options: (await conductorClient.request({
            auth: conductorAuth,
            method: import_pieces_common9.HttpMethod.GET,
            resourceUri: "/quickbooks-desktop/bills",
            queryParams: { vendorIds: vendorId, paymentStatus: "not_paid" }
          })).data.map((bill) => {
            var _a;
            return {
              label: `${(_a = bill.refNumber) != null ? _a : bill.id} \u2014 $${bill.amountDue} due (${bill.transactionDate})`,
              value: bill.id
            };
          })
        };
      }
    }),
    bankAccountId: import_pieces_framework15.Property.Dropdown({
      displayName: "Bank Account",
      description: "Required for Bill Payment \u2014 Check only. The account funds are drawn from.",
      auth: quickbooksDesktopConductorAuth,
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return { disabled: !0, placeholder: "Connect your account first", options: [] };
        let conductorAuth = { secretKey: auth.props.secretKey, endUserId: auth.props.endUserId };
        return { disabled: !1, options: (await conductorClient.request({
          auth: conductorAuth,
          method: import_pieces_common9.HttpMethod.GET,
          resourceUri: "/quickbooks-desktop/accounts",
          queryParams: { accountType: "bank" }
        })).data.map((a) => ({ label: a.fullName, value: a.id })) };
      }
    }),
    creditCardAccountId: import_pieces_framework15.Property.Dropdown({
      displayName: "Credit Card Account",
      description: "Required for Bill Payment \u2014 Credit Card only. The account charged.",
      auth: quickbooksDesktopConductorAuth,
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return { disabled: !0, placeholder: "Connect your account first", options: [] };
        let conductorAuth = { secretKey: auth.props.secretKey, endUserId: auth.props.endUserId };
        return { disabled: !1, options: (await conductorClient.request({
          auth: conductorAuth,
          method: import_pieces_common9.HttpMethod.GET,
          resourceUri: "/quickbooks-desktop/accounts",
          queryParams: { accountType: "credit_card" }
        })).data.map((a) => ({ label: a.fullName, value: a.id })) };
      }
    })
  },
  propertyGroups: [
    // Declared first on purpose: ungrouped props render after every declared section, so
    // paymentType — the field that decides which sections below actually matter — would
    // otherwise land dead last in the form.
    { key: "type", display: "section", label: "Payment Type", props: ["paymentType"] },
    { key: "details", display: "section", label: "Payment Details", props: ["transactionDate", "amount", "refNumber", "memo"] },
    { key: "ar", display: "section", label: "Customer Payment (Accounts Receivable)", props: ["customerId", "applyToSpecificInvoice", "invoiceId"] },
    { key: "ap", display: "section", label: "Vendor Bill Payment (Accounts Payable)", props: ["vendorId", "billId", "bankAccountId", "creditCardAccountId"] }
  ],
  async run(context) {
    let { propsValue } = context;
    if (!isPaymentType(propsValue.paymentType))
      throw new Error(`Unexpected Payment Type: "${propsValue.paymentType}".`);
    let paymentType = propsValue.paymentType, auth = {
      secretKey: context.auth.props.secretKey,
      endUserId: context.auth.props.endUserId
    }, maxRefLength = paymentType === "customer_payment" ? CUSTOMER_PAYMENT_MAX_REF_LENGTH : BILL_PAYMENT_MAX_REF_LENGTH;
    if (propsValue.refNumber && propsValue.refNumber.length > maxRefLength)
      throw new Error(
        `Reference/Check Number must be ${maxRefLength} characters or fewer for this payment type \u2014 "${propsValue.refNumber}" is ${propsValue.refNumber.length}.`
      );
    let commonBody = __spreadValues(__spreadValues({
      transactionDate: toDateOnly3(propsValue.transactionDate)
    }, (0, import_pieces_framework16.spreadIfDefined)("refNumber", propsValue.refNumber)), (0, import_pieces_framework16.spreadIfDefined)("memo", propsValue.memo));
    if (paymentType === "customer_payment") {
      if (!propsValue.customerId)
        throw new Error("Customer is required for a Customer Payment.");
      if (propsValue.applyToSpecificInvoice && !propsValue.invoiceId)
        throw new Error('Invoice is required when "Apply to a specific invoice" is on.');
      let body2 = __spreadValues(__spreadValues({
        customerId: propsValue.customerId,
        totalAmount: propsValue.amount
      }, commonBody), propsValue.applyToSpecificInvoice ? { applyToTransactions: [{ transactionId: propsValue.invoiceId, paymentAmount: propsValue.amount }] } : { isAutoApply: !0 }), result2 = await withRecordLockRetry(
        () => conductorClient.request({
          auth,
          method: import_pieces_common9.HttpMethod.POST,
          resourceUri: "/quickbooks-desktop/receive-payments",
          body: body2,
          // This creates a new payment — see client.ts's `request` doc on why creates opt out of retry.
          safeToRetry: !1
        })
      );
      return flattenPayment({ payment: result2, paymentType });
    }
    if (!propsValue.vendorId)
      throw new Error("Vendor is required for a Bill Payment.");
    if (!propsValue.billId)
      throw new Error("Bill is required for a Bill Payment \u2014 QuickBooks Desktop has no auto-apply for bill payments.");
    let applyToTransactions = [{ transactionId: propsValue.billId, paymentAmount: propsValue.amount }];
    if (paymentType === "bill_payment_check") {
      if (!propsValue.bankAccountId)
        throw new Error("Bank Account is required for a Bill Payment \u2014 Check.");
      let body2 = __spreadProps(__spreadValues({
        vendorId: propsValue.vendorId,
        bankAccountId: propsValue.bankAccountId
      }, commonBody), {
        applyToTransactions
      }), result2 = await withRecordLockRetry(
        () => conductorClient.request({
          auth,
          method: import_pieces_common9.HttpMethod.POST,
          resourceUri: "/quickbooks-desktop/bill-check-payments",
          body: body2,
          safeToRetry: !1
        })
      );
      return flattenPayment({ payment: result2, paymentType });
    }
    if (!propsValue.creditCardAccountId)
      throw new Error("Credit Card Account is required for a Bill Payment \u2014 Credit Card.");
    let body = __spreadProps(__spreadValues({
      vendorId: propsValue.vendorId,
      creditCardAccountId: propsValue.creditCardAccountId
    }, commonBody), {
      applyToTransactions
    }), result = await withRecordLockRetry(
      () => conductorClient.request({
        auth,
        method: import_pieces_common9.HttpMethod.POST,
        resourceUri: "/quickbooks-desktop/bill-credit-card-payments",
        body,
        safeToRetry: !1
      })
    );
    return flattenPayment({ payment: result, paymentType });
  }
});

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/actions/query-transactions.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework18 = require("@activepieces/pieces-framework");
var MAX_PAGE_SIZE = 150;
function toDateOnly4(isoDateTime) {
  return isoDateTime.split("T")[0];
}
function flattenTransaction(transaction) {
  var _a, _b, _c, _d, _e, _f, _g, _h;
  return {
    transaction_type: transaction.transactionType,
    transaction_id: transaction.transactionId,
    transaction_date: transaction.transactionDate,
    ref_number: transaction.refNumber,
    amount: transaction.amount,
    memo: transaction.memo,
    entity_id: (_b = (_a = transaction.entity) == null ? void 0 : _a.id) != null ? _b : null,
    entity_name: (_d = (_c = transaction.entity) == null ? void 0 : _c.fullName) != null ? _d : null,
    account_id: (_f = (_e = transaction.account) == null ? void 0 : _e.id) != null ? _f : null,
    account_name: (_h = (_g = transaction.account) == null ? void 0 : _g.fullName) != null ? _h : null,
    created_at: transaction.createdAt,
    updated_at: transaction.updatedAt
  };
}
var queryTransactionsAction = (0, import_pieces_framework17.createAction)({
  auth: quickbooksDesktopConductorAuth,
  name: "query_transactions",
  classification: "SEARCH",
  displayName: "Query Transactions",
  description: "Searches across all transaction types in QuickBooks Desktop (invoices, bills, payments, and more) with date and type filters.",
  audience: "both",
  aiMetadata: {
    description: "Search QuickBooks Desktop transactions across all types (invoice, bill, receive_payment, bill_payment_check, bill_payment_credit_card, and more) with date-range and payment-status filters. Only common fields are returned (id, type, date, amount, entity, account) \u2014 use the type-specific action or Custom API Call for full line-item detail. Read-only and safe to retry.",
    idempotent: !0
  },
  outputSchema: queryTransactionsActionOutputSchema,
  props: {
    transactionDateFrom: import_pieces_framework17.Property.DateTime({
      displayName: "Transaction Date From",
      required: !1
    }),
    transactionDateTo: import_pieces_framework17.Property.DateTime({
      displayName: "Transaction Date To",
      required: !1
    }),
    transactionTypes: import_pieces_framework17.Property.StaticMultiSelectDropdown({
      displayName: "Transaction Types",
      description: "Leave empty to search all types.",
      required: !1,
      options: {
        options: [
          { label: "Invoice", value: "invoice" },
          { label: "Bill", value: "bill" },
          { label: "Customer Payment", value: "receive_payment" },
          { label: "Bill Payment (Check)", value: "bill_payment_check" },
          { label: "Bill Payment (Credit Card)", value: "bill_payment_credit_card" },
          { label: "Purchase Order", value: "purchase_order" },
          { label: "Credit Memo", value: "credit_memo" },
          { label: "Sales Receipt", value: "sales_receipt" },
          { label: "Estimate", value: "estimate" },
          { label: "Check", value: "check" },
          { label: "Transfer", value: "transfer" }
        ]
      }
    }),
    paymentStatus: import_pieces_framework17.Property.StaticDropdown({
      displayName: "Payment Status",
      description: "Leave empty to search regardless of payment status.",
      required: !1,
      options: {
        options: [
          { label: "Open (unpaid)", value: "open" },
          { label: "Closed (paid)", value: "closed" },
          { label: "Either", value: "either" }
        ]
      }
    }),
    entityId: import_pieces_framework17.Property.ShortText({
      displayName: "Entity ID",
      description: "Optional. A customer, vendor, or employee id to filter by \u2014 typically chained from another step's output (e.g. Upsert Customer's Customer ID), not typed by hand.",
      required: !1
    }),
    limit: import_pieces_framework17.Property.Number({
      displayName: "Max Results",
      description: `Results per page (1\u2013${MAX_PAGE_SIZE}).`,
      required: !1,
      defaultValue: MAX_PAGE_SIZE,
      display: "stepper",
      min: 1,
      max: MAX_PAGE_SIZE
    }),
    cursor: import_pieces_framework17.Property.ShortText({
      displayName: "Page Cursor",
      description: "Optional. Pass the Next Cursor from a previous call's output to fetch the next page.",
      required: !1,
      advanced: !0
    })
  },
  async run(context) {
    var _a, _b;
    let { propsValue } = context, auth = {
      secretKey: context.auth.props.secretKey,
      endUserId: context.auth.props.endUserId
    };
    if (propsValue.limit && (propsValue.limit < 1 || propsValue.limit > MAX_PAGE_SIZE))
      throw new Error(`Max Results must be between 1 and ${MAX_PAGE_SIZE} \u2014 got ${propsValue.limit}.`);
    let queryParams = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      limit: String((_a = propsValue.limit) != null ? _a : MAX_PAGE_SIZE)
    }, (0, import_pieces_framework18.spreadIfDefined)("transactionDateFrom", propsValue.transactionDateFrom ? toDateOnly4(propsValue.transactionDateFrom) : void 0)), (0, import_pieces_framework18.spreadIfDefined)("transactionDateTo", propsValue.transactionDateTo ? toDateOnly4(propsValue.transactionDateTo) : void 0)), (0, import_pieces_framework18.spreadIfDefined)("paymentStatus", propsValue.paymentStatus)), (0, import_pieces_framework18.spreadIfDefined)("entityIds", propsValue.entityId)), (0, import_pieces_framework18.spreadIfDefined)("cursor", propsValue.cursor)), transactionTypesQuery = ((_b = propsValue.transactionTypes) != null ? _b : []).map((type) => `transactionTypes=${encodeURIComponent(type)}`).join("&"), resourceUri = transactionTypesQuery ? `/quickbooks-desktop/transactions?${transactionTypesQuery}` : "/quickbooks-desktop/transactions", response = await conductorClient.request({
      auth,
      method: import_pieces_common10.HttpMethod.GET,
      resourceUri,
      queryParams
    });
    return {
      transactions: response.data.map(flattenTransaction),
      count: response.data.length,
      next_cursor: response.nextCursor,
      has_more: response.hasMore
    };
  }
});

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/actions/list-items.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework20 = require("@activepieces/pieces-framework");
var MAX_PAGE_SIZE2 = 150;
function flattenItem({ item, itemType }) {
  return {
    id: item.id,
    name: item.name,
    full_name: item.fullName,
    item_type: itemType,
    is_active: item.isActive
  };
}
var listItemsAction = (0, import_pieces_framework19.createAction)({
  auth: quickbooksDesktopConductorAuth,
  name: "list_items",
  classification: "SEARCH",
  displayName: "List Items",
  description: "Lists Service and Non-Inventory items from QuickBooks Desktop, for finding the exact item name to use in Create Invoice's line items.",
  audience: "both",
  aiMetadata: {
    description: "Search QuickBooks Desktop's Service and Non-Inventory items by name \u2014 the two item types Create Invoice's line items accept. Use this to find the exact item name before calling Create Invoice, instead of guessing; typing an item name Create Invoice can't find fails with a clear error naming the bad value, but this avoids that round-trip. Read-only and safe to retry.",
    idempotent: !0
  },
  outputSchema: listItemsActionOutputSchema,
  props: {
    nameContains: import_pieces_framework19.Property.ShortText({
      displayName: "Name Contains",
      description: "Search by a substring of the item name. Leave empty to list all items.",
      required: !1
    }),
    status: import_pieces_framework19.Property.StaticDropdown({
      displayName: "Status",
      required: !1,
      defaultValue: "active",
      options: {
        options: [
          { label: "Active", value: "active" },
          { label: "Inactive", value: "inactive" },
          { label: "Either", value: "all" }
        ]
      }
    }),
    limit: import_pieces_framework19.Property.Number({
      displayName: "Max Results (per item type)",
      description: `Results per item type \u2014 up to twice this many total, since Service and Non-Inventory items are searched together (1\u2013${MAX_PAGE_SIZE2}).`,
      required: !1,
      defaultValue: MAX_PAGE_SIZE2,
      display: "stepper",
      min: 1,
      max: MAX_PAGE_SIZE2
    })
  },
  async run(context) {
    var _a;
    let { propsValue } = context, auth = {
      secretKey: context.auth.props.secretKey,
      endUserId: context.auth.props.endUserId
    };
    if (propsValue.limit && (propsValue.limit < 1 || propsValue.limit > MAX_PAGE_SIZE2))
      throw new Error(`Max Results must be between 1 and ${MAX_PAGE_SIZE2} \u2014 got ${propsValue.limit}.`);
    let queryParams = __spreadValues(__spreadValues({
      limit: String((_a = propsValue.limit) != null ? _a : MAX_PAGE_SIZE2)
    }, (0, import_pieces_framework20.spreadIfDefined)("nameContains", propsValue.nameContains)), (0, import_pieces_framework20.spreadIfDefined)("status", propsValue.status)), [serviceItems, nonInventoryItems] = await Promise.all([
      conductorClient.request({
        auth,
        method: import_pieces_common11.HttpMethod.GET,
        resourceUri: "/quickbooks-desktop/service-items",
        queryParams
      }),
      conductorClient.request({
        auth,
        method: import_pieces_common11.HttpMethod.GET,
        resourceUri: "/quickbooks-desktop/non-inventory-items",
        queryParams
      })
    ]), items = [
      ...serviceItems.data.map((item) => flattenItem({ item, itemType: "service" })),
      ...nonInventoryItems.data.map((item) => flattenItem({ item, itemType: "non_inventory" }))
    ];
    return {
      items,
      count: items.length,
      // True if either item type has more results beyond this call's limit — this action
      // doesn't paginate across the two merged types, so narrow the search instead (e.g. via
      // Name Contains) rather than expecting more pages.
      has_more: serviceItems.hasMore || nonInventoryItems.hasMore
    };
  }
});

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/triggers/new-or-updated-invoice.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/common/polling.ts
var import_pieces_common12 = require("@activepieces/pieces-common");
var MIN_SAFE_UPDATED_AFTER_MS = Date.parse("1980-01-01T00:00:00.000Z");
async function fetchAllUpdatedSince({
  auth,
  resourceUri,
  updatedAfterEpochMS
}) {
  let safeEpochMS = Math.max(updatedAfterEpochMS, MIN_SAFE_UPDATED_AFTER_MS), updatedAfter = new Date(safeEpochMS).toISOString();
  return fetchPage({ auth, resourceUri, updatedAfter, cursor: void 0, itemsSoFar: [] });
}
async function fetchPage({
  auth,
  resourceUri,
  updatedAfter,
  cursor,
  itemsSoFar
}) {
  let response = await conductorClient.request({
    auth,
    method: import_pieces_common12.HttpMethod.GET,
    resourceUri,
    queryParams: __spreadValues({
      updatedAfter,
      limit: "150"
    }, cursor ? { cursor } : {})
  }), items = [...itemsSoFar, ...response.data];
  return !response.hasMore || !response.nextCursor ? items : fetchPage({ auth, resourceUri, updatedAfter, cursor: response.nextCursor, itemsSoFar: items });
}

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/triggers/new-or-updated-invoice.ts
var polling = {
  strategy: import_pieces_common13.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let conductorAuth = { secretKey: auth.props.secretKey, endUserId: auth.props.endUserId };
    return (await fetchAllUpdatedSince({
      auth: conductorAuth,
      resourceUri: "/quickbooks-desktop/invoices",
      updatedAfterEpochMS: lastFetchEpochMS
    })).map((invoice) => ({
      epochMilliSeconds: new Date(invoice.updatedAt).getTime(),
      data: flattenInvoice(invoice)
    }));
  }
}, newOrUpdatedInvoiceTrigger = (0, import_pieces_framework21.createTrigger)({
  auth: quickbooksDesktopConductorAuth,
  name: "new_or_updated_invoice",
  displayName: "New or Updated Invoice",
  description: "Fires when an invoice is created or updated (e.g. line items, balance, or payment status changed) in QuickBooks Desktop. One event per invoice change, not create-only.",
  aiMetadata: {
    description: "Fires when an invoice is created or changed in QuickBooks Desktop, emitting the current invoice record. Fires on every update to a matching invoice, not just its creation \u2014 a flow reacting only to brand-new invoices should check whether created_at and updated_at are close together. Polls every ~5 minutes; if the QuickBooks Desktop machine is off or asleep at poll time, the poll fails visibly (Conductor returns a connection error) rather than silently returning zero results."
  },
  props: {},
  type: import_pieces_framework21.TriggerStrategy.POLLING,
  outputSchema: newOrUpdatedInvoiceTriggerOutputSchema,
  async onEnable(context) {
    await import_pieces_common13.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common13.pollingHelper.onDisable(polling, context);
  },
  async test(context) {
    return await import_pieces_common13.pollingHelper.test(polling, context);
  },
  async run(context) {
    return await import_pieces_common13.pollingHelper.poll(polling, context);
  },
  sampleData: {
    id: "2FC27-1797340210",
    transaction_date: "2026-08-20",
    due_date: "2026-08-20",
    ref_number: "SUITE1",
    memo: "full suite test",
    customer_id: "800000EA-1797339907",
    customer_name: "Suite Test Customer",
    subtotal_amount: "270.00",
    sales_tax_amount: "0.00",
    total_amount: "270.00",
    // A partial payment after creation, so this sample shows the "or updated" half too, not
    // just a fresh invoice.
    balance_remaining: "120.00",
    is_paid: !1,
    line_count: 1,
    revision_number: "1797340229",
    created_at: "2026-12-15T16:10:10+03:00",
    updated_at: "2026-12-15T16:10:29+03:00"
  }
});

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/lib/triggers/new-payment.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common14.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let conductorAuth = { secretKey: auth.props.secretKey, endUserId: auth.props.endUserId };
    return (await fetchAllUpdatedSince({
      auth: conductorAuth,
      resourceUri: "/quickbooks-desktop/receive-payments",
      updatedAfterEpochMS: lastFetchEpochMS
    })).map((payment) => ({
      epochMilliSeconds: new Date(payment.createdAt).getTime(),
      data: flattenPayment({ payment, paymentType: "customer_payment" })
    }));
  }
}, newPaymentTrigger = (0, import_pieces_framework22.createTrigger)({
  auth: quickbooksDesktopConductorAuth,
  name: "new_payment",
  displayName: "New Payment",
  description: `Fires once when a new customer payment is recorded (Accounts Receivable) in QuickBooks Desktop \u2014 a "Receive Payment" transaction, e.g. from the Record Payment action's Customer Payment mode. Create-only: editing an existing payment does not re-fire it. Vendor bill payments (Accounts Payable) do not fire this trigger.`,
  aiMetadata: {
    description: 'Fires when a new customer payment is recorded in QuickBooks Desktop (Accounts Receivable \u2014 "Receive Payment"), emitting the payment record. Create-only \u2014 later edits to that same payment do not fire it again. Scoped to customer payments only \u2014 vendor bill payments (Accounts Payable) are not covered. Polls every ~5 minutes; if the QuickBooks Desktop machine is off or asleep at poll time, the poll fails visibly (Conductor returns a connection error) rather than silently returning zero results.'
  },
  props: {},
  type: import_pieces_framework22.TriggerStrategy.POLLING,
  outputSchema: newPaymentTriggerOutputSchema,
  async onEnable(context) {
    await import_pieces_common14.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common14.pollingHelper.onDisable(polling2, context);
  },
  async test(context) {
    return await import_pieces_common14.pollingHelper.test(polling2, context);
  },
  async run(context) {
    return await import_pieces_common14.pollingHelper.poll(polling2, context);
  },
  sampleData: {
    id: "2FC2E-1797340229",
    payment_type: "customer_payment",
    transaction_date: "2026-08-20",
    ref_number: "SUITEPMT1",
    memo: "full suite test",
    amount: "270.00",
    customer_id: "800000EA-1797339907",
    customer_name: "Suite Test Customer",
    vendor_id: null,
    vendor_name: null,
    revision_number: "1797340229",
    created_at: "2026-12-15T16:10:29+03:00",
    updated_at: "2026-12-15T16:10:29+03:00"
  }
});

// ../ap/packages/pieces/community/quickbooks-desktop-conductor/src/index.ts
var quickbooksDesktopConductor = (0, import_pieces_framework23.createPiece)({
  displayName: "QuickBooks Desktop (via Conductor)",
  description: "Sync invoices, bills, customers, vendors and payments with QuickBooks Desktop through the Conductor API bridge.",
  auth: quickbooksDesktopConductorAuth,
  minimumSupportedRelease: "0.87.0",
  logoUrl: "https://cdn.activepieces.com/pieces/quickbooks.png",
  categories: [import_pieces_framework23.PieceCategory.ACCOUNTING],
  authors: ["OdaiAhmed99"],
  actions: [
    upsertCustomerAction,
    upsertVendorAction,
    createInvoiceAction,
    createBillAction,
    recordPaymentAction,
    queryTransactionsAction,
    listItemsAction,
    (0, import_pieces_common15.createCustomApiCallAction)({
      auth: quickbooksDesktopConductorAuth,
      baseUrl: () => "https://api.conductor.is/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.secretKey}`,
        "Conductor-End-User-Id": auth.props.endUserId
      })
    })
  ],
  triggers: [newOrUpdatedInvoiceTrigger, newPaymentTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  quickbooksDesktopConductor
});
