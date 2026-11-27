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

// ../ap/packages/pieces/community/just-invoice/src/index.ts
var index_exports = {};
__export(index_exports, {
  justInvoice: () => justInvoice
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/just-invoice/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), justInvoiceAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Enter your JustInvoice API key. You can find this in your JustInvoice account settings.",
  required: !0
});

// ../ap/packages/pieces/community/just-invoice/src/lib/actions/create-invoice.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/just-invoice/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), JUSTINVOICE_API_URL = "https://api.justinvoice.io";
async function justInvoiceApiCall({
  apiKey,
  method,
  endpoint,
  query,
  body
}) {
  let qs = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (qs[key] = String(value));
  let headers = {
    Authorization: `ApiKey ${apiKey}`,
    "Content-Type": "application/json"
  };
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${JUSTINVOICE_API_URL}${endpoint}`,
    headers,
    queryParams: qs,
    body
  })).body;
}

// ../ap/packages/pieces/community/just-invoice/src/lib/actions/create-invoice.ts
var createInvoice = (0, import_pieces_framework2.createAction)({
  auth: justInvoiceAuth,
  name: "create_invoice",
  displayName: "Create Invoice",
  description: "Creates a new invoice in JustInvoice",
  audience: "both",
  aiMetadata: { description: "Creates a new invoice in JustInvoice for a customer, with at least one line item, optionally setting it to Draft or Final status on creation. Use to bill a customer or generate an invoice record. Not idempotent: each call mints a new invoice even with identical input.", idempotent: !1 },
  props: {
    customerEmail: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Email",
      description: "Email address of the customer",
      required: !0
    }),
    customerFirstName: import_pieces_framework2.Property.ShortText({
      displayName: "Customer First Name",
      description: "First name of the customer",
      required: !1
    }),
    customerLastName: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Last Name",
      description: "Last name of the customer",
      required: !1
    }),
    customerCompanyName: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Company Name",
      description: "Company name of the customer",
      required: !1
    }),
    customerAddress: import_pieces_framework2.Property.LongText({
      displayName: "Customer Address",
      description: "Street address of the customer",
      required: !1
    }),
    customerCity: import_pieces_framework2.Property.ShortText({
      displayName: "Customer City",
      description: "City of the customer",
      required: !1
    }),
    customerProvinceState: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Province/State",
      description: "Province or state of the customer",
      required: !1
    }),
    customerPostalCode: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Postal Code",
      description: "Postal code of the customer",
      required: !1
    }),
    customerCountry: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Country",
      description: "Country code of the customer (e.g., US, CA)",
      required: !1
    }),
    invoiceDate: import_pieces_framework2.Property.DateTime({
      displayName: "Invoice Date",
      description: "Date of the invoice",
      required: !1
    }),
    invoiceStatus: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Invoice Status",
      description: "Initial status of the invoice",
      required: !1,
      options: {
        options: [
          { label: "Final", value: 0 },
          { label: "Draft", value: 5 }
        ]
      },
      defaultValue: 0
    }),
    currencyCode: import_pieces_framework2.Property.ShortText({
      displayName: "Currency Code",
      description: "Currency code (e.g., USD, EUR)",
      required: !1,
      defaultValue: "USD"
    }),
    noteToCustomer: import_pieces_framework2.Property.LongText({
      displayName: "Note to Customer",
      description: "Additional notes for the customer",
      required: !1
    }),
    lineItems: import_pieces_framework2.Property.Array({
      displayName: "Line Items",
      description: "Products or services being billed",
      required: !0,
      properties: {
        description: import_pieces_framework2.Property.ShortText({
          displayName: "Description",
          description: "Description of the item",
          required: !0
        }),
        quantity: import_pieces_framework2.Property.Number({
          displayName: "Quantity",
          description: "Number of items",
          required: !0,
          defaultValue: 1
        }),
        unitPrice: import_pieces_framework2.Property.Number({
          displayName: "Unit Price",
          description: "Price per item",
          required: !0
        })
      }
    })
  },
  async run({ auth, propsValue }) {
    let {
      customerEmail,
      customerFirstName,
      customerLastName,
      customerCompanyName,
      customerAddress,
      customerCity,
      customerProvinceState,
      customerPostalCode,
      customerCountry,
      invoiceDate,
      invoiceStatus,
      currencyCode,
      noteToCustomer,
      lineItems
    } = propsValue, body = {
      customerEmail,
      lineItems
    };
    return customerFirstName && (body.customerFirstName = customerFirstName), customerLastName && (body.customerLastName = customerLastName), customerCompanyName && (body.customerCompanyName = customerCompanyName), customerAddress && (body.customerAddress = customerAddress), customerCity && (body.customerCity = customerCity), customerProvinceState && (body.customerProvinceState = customerProvinceState), customerPostalCode && (body.customerPostalCode = customerPostalCode), customerCountry && (body.customerCountry = customerCountry), invoiceDate && (body.invoiceDate = invoiceDate), invoiceStatus !== void 0 && (body.invoiceStatus = invoiceStatus), currencyCode && (body.currencyCode = currencyCode), noteToCustomer && (body.noteToCustomer = noteToCustomer), await justInvoiceApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common2.HttpMethod.POST,
      endpoint: "/api/invoices",
      body
    });
  }
});

// ../ap/packages/pieces/community/just-invoice/src/lib/actions/get-invoice.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getInvoice = (0, import_pieces_framework3.createAction)({
  auth: justInvoiceAuth,
  name: "get_invoice",
  displayName: "Get Invoice",
  description: "Gets detailed information about a specific invoice",
  audience: "both",
  aiMetadata: { description: "Retrieves full details of a single JustInvoice invoice by its ID or invoice number. Use to look up an invoice before acting on it or to read its current status. Read-only and idempotent.", idempotent: !0 },
  props: {
    invoiceId: import_pieces_framework3.Property.ShortText({
      displayName: "Invoice ID",
      description: "The ID or invoice number of the invoice to retrieve",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { invoiceId } = propsValue;
    return await justInvoiceApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      endpoint: `/api/invoices/${invoiceId}`
    });
  }
});

// ../ap/packages/pieces/community/just-invoice/src/lib/actions/mark-invoice-final.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var markInvoiceFinal = (0, import_pieces_framework4.createAction)({
  auth: justInvoiceAuth,
  name: "mark_invoice_final",
  displayName: "Mark Invoice Final",
  description: "Marks an invoice currently in draft as final and ready to accept payment",
  audience: "both",
  aiMetadata: { description: "Transitions a draft JustInvoice invoice (by ID or invoice number) to final status so it can accept payment. Use after a draft invoice is ready to issue. Idempotent: an invoice already final stays final.", idempotent: !0 },
  props: {
    invoiceId: import_pieces_framework4.Property.ShortText({
      displayName: "Invoice ID",
      description: "The ID or invoice number of the invoice to mark as final",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { invoiceId } = propsValue;
    return await justInvoiceApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      endpoint: `/api/invoices/${invoiceId}/final`
    });
  }
});

// ../ap/packages/pieces/community/just-invoice/src/lib/actions/mark-invoice-cancelled.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var markInvoiceCancelled = (0, import_pieces_framework5.createAction)({
  auth: justInvoiceAuth,
  name: "mark_invoice_cancelled",
  displayName: "Mark Invoice Cancelled",
  description: "Marks an invoice currently in final as cancelled",
  audience: "both",
  aiMetadata: { description: "Transitions a final JustInvoice invoice (by ID or invoice number) to cancelled status. Use to void an issued invoice instead of deleting it. Idempotent: an invoice already cancelled stays cancelled.", idempotent: !0 },
  props: {
    invoiceId: import_pieces_framework5.Property.ShortText({
      displayName: "Invoice ID",
      description: "The ID or invoice number of the invoice to mark as cancelled",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { invoiceId } = propsValue;
    return await justInvoiceApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      endpoint: `/api/invoices/${invoiceId}/cancelled`
    });
  }
});

// ../ap/packages/pieces/community/just-invoice/src/lib/actions/mark-invoice-paid.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var markInvoicePaid = (0, import_pieces_framework6.createAction)({
  auth: justInvoiceAuth,
  name: "mark_invoice_paid",
  displayName: "Mark Invoice Paid",
  description: "Marks an existing final invoice as paid",
  audience: "both",
  aiMetadata: { description: "Marks a final JustInvoice invoice (by ID or invoice number) as paid. Use to record that payment for an issued invoice has been received. Idempotent: an invoice already marked paid stays paid.", idempotent: !0 },
  props: {
    invoiceId: import_pieces_framework6.Property.ShortText({
      displayName: "Invoice ID",
      description: "The ID or invoice number of the invoice to mark as paid",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { invoiceId } = propsValue;
    return await justInvoiceApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      endpoint: `/api/invoices/${invoiceId}/paid`
    });
  }
});

// ../ap/packages/pieces/community/just-invoice/src/lib/actions/delete-invoice.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var deleteInvoice = (0, import_pieces_framework7.createAction)({
  auth: justInvoiceAuth,
  name: "delete_invoice",
  displayName: "Delete Invoice",
  description: "Permanently deletes an invoice",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a JustInvoice invoice by its ID or invoice number. Use to remove an invoice entirely; prefer cancelling instead of deleting an issued invoice. Idempotent in effect: once removed, the invoice stays gone.", idempotent: !0 },
  props: {
    invoiceId: import_pieces_framework7.Property.ShortText({
      displayName: "Invoice ID",
      description: "The ID or invoice number of the invoice to delete",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { invoiceId } = propsValue;
    return await justInvoiceApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common7.HttpMethod.DELETE,
      endpoint: `/api/invoices/${invoiceId}`
    });
  }
});

// ../ap/packages/pieces/community/just-invoice/src/index.ts
var justInvoice = (0, import_pieces_framework8.createPiece)({
  displayName: "JustInvoice",
  description: "Create and manage invoices with JustInvoice API",
  auth: justInvoiceAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/just-invoice.png",
  authors: ["onyedikachi-david"],
  actions: [createInvoice, getInvoice, markInvoiceFinal, markInvoiceCancelled, markInvoicePaid, deleteInvoice],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  justInvoice
});
