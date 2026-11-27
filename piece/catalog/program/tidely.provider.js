"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
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

// ../ap/packages/pieces/community/tidely/src/index.ts
var index_exports = {};
__export(index_exports, {
  tidely: () => tidely
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tidely/src/lib/actions/import-invoice.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tidely/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.tidely.com/api/v1";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "X-Authorization": api_key,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/tidely/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var tidelyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Tidely API Key",
  description: "Your Tidely API Key. Get it from your Tidely account settings.",
  required: !0,
  validate: async (auth) => {
    try {
      return await makeRequest(
        auth.auth,
        import_pieces_common2.HttpMethod.GET,
        "/open-api/authentication/verifyAuth",
        {}
      ), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key"
      };
    }
  }
});

// ../ap/packages/pieces/community/tidely/src/lib/actions/import-invoice.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), importInvoice = (0, import_pieces_framework2.createAction)({
  auth: tidelyAuth,
  name: "importInvoice",
  displayName: "Import Invoice",
  description: "Create, update, or delete an invoice in Tidely",
  audience: "both",
  aiMetadata: {
    description: "Imports an invoice into Tidely via its open-api import endpoint. Provide an Invoice ID to upsert/match an existing invoice, or omit it to create a new one on each call. Use to push invoice data (number, contact, dates, amounts, type) from an external source into Tidely. Requires invoice number, contact name, invoice date, type, net and gross amounts, and currency. Not idempotent without a stable Invoice ID \u2014 repeating without one creates duplicates.",
    idempotent: !1
  },
  props: {
    invoiceId: import_pieces_framework2.Property.ShortText({
      displayName: "Invoice ID",
      description: "Unique identifier for the invoice",
      required: !1
    }),
    invoiceNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Invoice Number",
      description: "Invoice number or reference",
      required: !0
    }),
    contactName: import_pieces_framework2.Property.ShortText({
      displayName: "Contact Name",
      description: "Name of the contact/customer",
      required: !0
    }),
    invoiceDate: import_pieces_framework2.Property.ShortText({
      displayName: "Invoice Date",
      description: "Date the invoice was issued (format: dd/MM/yy, M/dd/yy, or yyyy-MM-dd)",
      required: !0
    }),
    dueDate: import_pieces_framework2.Property.ShortText({
      displayName: "Due Date",
      description: "Date the invoice is due (format: dd/MM/yy, M/dd/yy, or yyyy-MM-dd)",
      required: !1
    }),
    paidDate: import_pieces_framework2.Property.ShortText({
      displayName: "Paid Date",
      description: "Date the invoice was paid (format: yyyy-MM-dd HH:mm:ss or yyyy-MM-dd'T'HH:mm:ss)",
      required: !1
    }),
    createdDate: import_pieces_framework2.Property.ShortText({
      displayName: "Created Date",
      description: "Date the invoice was created (format: dd/MM/yy, M/dd/yy, or yyyy-MM-dd)",
      required: !1
    }),
    updatedDate: import_pieces_framework2.Property.ShortText({
      displayName: "Updated Date",
      description: "Date the invoice was last updated (format: dd/MM/yy, M/dd/yy, or yyyy-MM-dd)",
      required: !1
    }),
    invoiceType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Invoice Type",
      description: "Type of invoice",
      required: !0,
      options: {
        options: [
          { label: "Sales Invoice", value: "SALES_INVOICE" },
          { label: "Purchase Invoice", value: "PURCHASE_INVOICE" },
          { label: "Credit Note", value: "CREDIT_NOTE" },
          { label: "Debit Note", value: "DEBIT_NOTE" }
        ]
      }
    }),
    invoiceStatus: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Invoice Status",
      description: "Current status of the invoice",
      required: !1,
      options: {
        options: [
          { label: "Draft", value: "DRAFT" },
          { label: "Sent", value: "SENT" },
          { label: "Viewed", value: "VIEWED" },
          { label: "Overdue", value: "OVERDUE" },
          { label: "Paid", value: "PAID" },
          { label: "Cancelled", value: "CANCELLED" }
        ]
      }
    }),
    totalNetAmount: import_pieces_framework2.Property.Number({
      displayName: "Total Net Amount",
      description: "Total amount before tax",
      required: !0
    }),
    totalTaxAmount: import_pieces_framework2.Property.Number({
      displayName: "Total Tax Amount",
      description: "Total tax amount",
      required: !1
    }),
    totalGrossAmount: import_pieces_framework2.Property.Number({
      displayName: "Total Gross Amount",
      description: "Total amount including tax",
      required: !0
    }),
    openAmount: import_pieces_framework2.Property.Number({
      displayName: "Open Amount",
      description: "Amount still outstanding",
      required: !1
    }),
    currency: import_pieces_framework2.Property.ShortText({
      displayName: "Currency",
      description: "Currency code (e.g., USD, EUR, GBP)",
      required: !0
    }),
    description: import_pieces_framework2.Property.LongText({
      displayName: "Description",
      description: "Invoice description or notes",
      required: !1
    }),
    categoryId: import_pieces_framework2.Property.Number({
      displayName: "Category ID",
      description: "ID of the invoice category",
      required: !1
    })
  },
  async run(context) {
    let body = {
      invoiceNumber: context.propsValue.invoiceNumber,
      contactName: context.propsValue.contactName,
      invoiceDate: context.propsValue.invoiceDate,
      invoiceType: context.propsValue.invoiceType,
      totalNetAmount: context.propsValue.totalNetAmount,
      totalGrossAmount: context.propsValue.totalGrossAmount,
      currency: context.propsValue.currency
    };
    return context.propsValue.invoiceId && (body.invoiceId = context.propsValue.invoiceId), context.propsValue.dueDate && (body.dueDate = context.propsValue.dueDate), context.propsValue.paidDate && (body.paidDate = context.propsValue.paidDate), context.propsValue.createdDate && (body.createdDate = context.propsValue.createdDate), context.propsValue.updatedDate && (body.updatedDate = context.propsValue.updatedDate), context.propsValue.invoiceStatus && (body.invoiceStatus = context.propsValue.invoiceStatus), context.propsValue.totalTaxAmount !== void 0 && context.propsValue.totalTaxAmount !== null && (body.totalTaxAmount = context.propsValue.totalTaxAmount), context.propsValue.openAmount !== void 0 && context.propsValue.openAmount !== null && (body.openAmount = context.propsValue.openAmount), context.propsValue.description && (body.description = context.propsValue.description), context.propsValue.categoryId !== void 0 && context.propsValue.categoryId !== null && (body.categoryId = context.propsValue.categoryId), await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/open-api/invoices",
      body
    );
  }
});

// ../ap/packages/pieces/community/tidely/src/index.ts
var import_pieces_common5 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/tidely/src/lib/actions/create-a-plan.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), createAPlan = (0, import_pieces_framework3.createAction)({
  auth: tidelyAuth,
  name: "createAPlan",
  displayName: "Create a Plan",
  description: "Create a new financial plan in Tidely",
  audience: "both",
  aiMetadata: {
    description: "Creates a new financial plan entry in Tidely for cash-flow planning, with a name, amount, period (daily/weekly/monthly), type (one-time/recurring), and date; optionally tied to a category and scenario. Use to add planned inflows or outflows to a Tidely forecast. Not idempotent \u2014 each call creates a new plan.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Plan Name",
      description: "Name of the plan",
      required: !0
    }),
    amount: import_pieces_framework3.Property.Number({
      displayName: "Amount",
      description: "Plan amount",
      required: !0
    }),
    period: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Period",
      description: "Frequency of the plan",
      required: !0,
      options: {
        options: [
          { label: "Daily", value: "DAILY" },
          { label: "Weekly", value: "WEEKLY" },
          { label: "Monthly", value: "MONTHLY" }
        ]
      }
    }),
    type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Plan Type",
      description: "Type of plan",
      required: !0,
      options: {
        options: [
          { label: "One Time", value: "ONE_TIME" },
          { label: "Recurring", value: "RECURRING" }
        ]
      }
    }),
    date: import_pieces_framework3.Property.ShortText({
      displayName: "Date",
      description: "Plan date (format: dd/MM/yy, M/dd/yy, or yyyy-MM-dd)",
      required: !0
    }),
    categoryId: import_pieces_framework3.Property.Number({
      displayName: "Category ID",
      description: "ID of the plan category",
      required: !1
    }),
    categoryName: import_pieces_framework3.Property.ShortText({
      displayName: "Category Name",
      description: "Name of the plan category",
      required: !1
    }),
    scenarioId: import_pieces_framework3.Property.Number({
      displayName: "Scenario ID",
      description: "ID of the scenario",
      required: !1
    }),
    scenarioName: import_pieces_framework3.Property.ShortText({
      displayName: "Scenario Name",
      description: "Name of the scenario",
      required: !1
    }),
    replaceCurrentValuesForThePeriod: import_pieces_framework3.Property.Checkbox({
      displayName: "Replace Current Values for the Period",
      description: "Whether to replace current values for the period",
      required: !1
    })
  },
  async run(context) {
    let body = {
      name: context.propsValue.name,
      amount: context.propsValue.amount,
      period: context.propsValue.period,
      type: context.propsValue.type,
      date: context.propsValue.date
    };
    return context.propsValue.categoryId !== void 0 && context.propsValue.categoryId !== null && (body.categoryId = context.propsValue.categoryId), context.propsValue.categoryName && (body.categoryName = context.propsValue.categoryName), context.propsValue.scenarioId !== void 0 && context.propsValue.scenarioId !== null && (body.scenarioId = context.propsValue.scenarioId), context.propsValue.scenarioName && (body.scenarioName = context.propsValue.scenarioName), context.propsValue.replaceCurrentValuesForThePeriod !== void 0 && (body.replaceCurrentValuesForThePeriod = context.propsValue.replaceCurrentValuesForThePeriod), await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/open-api/plans",
      body
    );
  }
});

// ../ap/packages/pieces/community/tidely/src/index.ts
var tidely = (0, import_pieces_framework4.createPiece)({
  displayName: "Tidely",
  auth: tidelyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/tidely.png",
  authors: ["sanket-a11y"],
  actions: [
    importInvoice,
    createAPlan,
    (0, import_pieces_common5.createCustomApiCallAction)({
      auth: tidelyAuth,
      baseUrl: () => "https://api.tidely.com/api/v1",
      authMapping: async (auth) => ({
        "X-Authorization": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  tidely
});
