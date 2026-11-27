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

// ../ap/packages/pieces/community/wafeq/src/index.ts
var index_exports = {};
__export(index_exports, {
  wafeq: () => wafeq,
  wafeqAuth: () => wafeqAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common22 = require("@activepieces/pieces-common"), import_pieces_framework22 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/wafeq/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), markdownDescription = `
**How to get your Wafeq API Key:**

1. Log in to your [Wafeq account](https://app.wafeq.com/accounts/signup/).
2. In the left-side menu, open the **Developer** section and click **API Keys**.
3. Click **Create new key** and copy the key \u2014 you won't be able to see it again.
4. Click **Enable API**. The API is available on the **Starter plan** or higher (including free trials).
5. Paste the key below.
`, wafeqAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${WAFEQ_API_BASE_URL}/organization/`,
        headers: {
          Authorization: `Api-Key ${auth}`
        }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key. Make sure the key is correct and that the API is enabled on your Wafeq plan (Starter or higher)."
      };
    }
  }
}), WAFEQ_API_BASE_URL = "https://api.wafeq.com/v1";

// ../ap/packages/pieces/community/wafeq/src/lib/actions/create-invoice.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/wafeq/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
async function wafeqApiCall({
  apiKey,
  method,
  path,
  body,
  queryParams,
  idempotencyKey
}) {
  let headers = {
    Authorization: `Api-Key ${apiKey}`
  };
  return idempotencyKey && (headers["X-Wafeq-Idempotency-Key"] = idempotencyKey), await import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${WAFEQ_API_BASE_URL}${path}`,
    headers,
    queryParams,
    body
  });
}

// ../ap/packages/pieces/community/wafeq/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var wafeqProps = {
  contactDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.Dropdown({
      auth: wafeqAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Contact",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : `Pick a contact (customer or supplier). If you don't see a contact, use the "Create Contact" action first.`,
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your Wafeq account first"
          };
        try {
          let response = await wafeqApiCall({
            apiKey: auth.secret_text,
            method: import_pieces_common3.HttpMethod.GET,
            path: "/contacts/",
            queryParams: { page_size: "200" }
          });
          return response.body.results.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No contacts found. Create one in Wafeq first."
          } : {
            disabled: !1,
            options: response.body.results.map((c) => ({
              label: c.email ? `${c.name} (${c.email})` : c.name,
              value: c.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load contacts. Check your API key."
          };
        }
      }
    });
  },
  itemDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.Dropdown({
      auth: wafeqAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Item",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : "Pick a product or service from your Wafeq item catalog. Leave empty for ad-hoc line items.",
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your Wafeq account first"
          };
        try {
          let response = await wafeqApiCall({
            apiKey: auth.secret_text,
            method: import_pieces_common3.HttpMethod.GET,
            path: "/items/",
            queryParams: { page_size: "200" }
          });
          return response.body.results.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No items found. Create one in Wafeq first."
          } : {
            disabled: !1,
            options: response.body.results.map((i) => ({
              label: i.sku ? `${i.name} (${i.sku})` : i.name,
              value: i.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load items"
          };
        }
      }
    });
  },
  invoiceDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.Dropdown({
      auth: wafeqAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Invoice",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : 'Pick an invoice. Only the 100 most recent invoices are shown \u2014 use "Create Invoice" first if you need a specific invoice.',
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your Wafeq account first"
          };
        try {
          let response = await wafeqApiCall({
            apiKey: auth.secret_text,
            method: import_pieces_common3.HttpMethod.GET,
            path: "/invoices/",
            queryParams: { page_size: "100" }
          });
          return response.body.results.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No invoices found yet"
          } : {
            disabled: !1,
            options: response.body.results.map((inv) => ({
              label: `#${inv.invoice_number} \u2014 ${inv.currency} ${inv.amount}`,
              value: inv.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load invoices"
          };
        }
      }
    });
  },
  quoteDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.Dropdown({
      auth: wafeqAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Quote",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : "Pick a quote to work with. Only the 100 most recent quotes are listed.",
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your Wafeq account first"
          };
        try {
          let response = await wafeqApiCall({
            apiKey: auth.secret_text,
            method: import_pieces_common3.HttpMethod.GET,
            path: "/quotes/",
            queryParams: { page_size: "100" }
          });
          return response.body.results.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No quotes found yet"
          } : {
            disabled: !1,
            options: response.body.results.map((q) => ({
              label: `#${q.quote_number} \u2014 ${q.currency} ${q.amount}`,
              value: q.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load quotes"
          };
        }
      }
    });
  },
  accountDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.Dropdown({
      auth: wafeqAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Account",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : "The general-ledger account this line maps to. You can see the full chart of accounts in Wafeq under Accounting > Chart of Accounts.",
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your Wafeq account first"
          };
        try {
          let queryParams = { page_size: "200" };
          return params != null && params.classification && (queryParams.classification = params.classification), params != null && params.paymentEnabledOnly && (queryParams.is_payment_enabled = "true"), {
            disabled: !1,
            options: (await wafeqApiCall({
              apiKey: auth.secret_text,
              method: import_pieces_common3.HttpMethod.GET,
              path: "/accounts/",
              queryParams
            })).body.results.map((a) => {
              var _a2, _b2;
              return {
                label: `${a.account_code ? a.account_code + " \u2014 " : ""}${(_b2 = (_a2 = a.name_en) != null ? _a2 : a.name_ar) != null ? _b2 : a.id}`,
                value: a.id
              };
            })
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load accounts"
          };
        }
      }
    });
  },
  taxRateDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.Dropdown({
      auth: wafeqAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Tax Rate",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : "Optional. Pick the tax rate to apply. Leave empty to use the item default or no tax.",
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your Wafeq account first"
          };
        try {
          return {
            disabled: !1,
            options: (await wafeqApiCall({
              apiKey: auth.secret_text,
              method: import_pieces_common3.HttpMethod.GET,
              path: "/tax-rates/",
              queryParams: { page_size: "200" }
            })).body.results.map((t) => ({
              label: t.rate != null ? `${t.name} (${t.rate}%)` : t.name,
              value: t.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load tax rates"
          };
        }
      }
    });
  },
  currency: (params) => {
    var _a;
    return import_pieces_framework2.Property.StaticDropdown({
      displayName: "Currency",
      description: "Which currency the amounts below are in. Pick AED for UAE, SAR for Saudi Arabia, or any other currency you invoice in.",
      required: (_a = params == null ? void 0 : params.required) != null ? _a : !0,
      defaultValue: params == null ? void 0 : params.defaultValue,
      options: {
        disabled: !1,
        options: COMMON_CURRENCIES.map((c) => ({
          label: `${c.code} \u2014 ${c.name}`,
          value: c.code
        }))
      }
    });
  },
  lineItemsArray: import_pieces_framework2.Property.Array({
    displayName: "Line Items",
    description: "Add one row for each product or service on the document. Each row needs at least a description, quantity, and unit price. Leave the account and tax fields blank to use the defaults you set above.",
    required: !0,
    properties: {
      description: import_pieces_framework2.Property.ShortText({
        displayName: "Description",
        description: 'What this line is for (e.g. "Consulting \u2014 April", "Office chair"). This is printed on the document.',
        required: !0
      }),
      quantity: import_pieces_framework2.Property.Number({
        displayName: "Quantity",
        description: "How many units. Use 1 if unsure.",
        required: !0,
        defaultValue: 1
      }),
      unit_amount: import_pieces_framework2.Property.Number({
        displayName: "Unit Price",
        description: "Price per single unit, in the currency you chose above (before tax).",
        required: !0
      }),
      item: import_pieces_framework2.Property.ShortText({
        displayName: "Item ID (optional)",
        description: 'Optional. Paste a Wafeq item ID here to link this line to a product in your catalog. You can get item IDs from the "List Items" action. Leave blank to just use the description and price.',
        required: !1
      }),
      account: import_pieces_framework2.Property.ShortText({
        displayName: "Account ID (optional)",
        description: "Optional. Paste an account ID to override the default account you chose above \u2014 e.g. book this one line to a different sales account. Leave blank to use the default.",
        required: !1
      }),
      tax_rate: import_pieces_framework2.Property.ShortText({
        displayName: "Tax Rate ID (optional)",
        description: "Optional. Paste a tax rate ID to override the default tax rate for this line only. Leave blank to use the default or no tax.",
        required: !1
      }),
      discount: import_pieces_framework2.Property.Number({
        displayName: "Discount %",
        description: "Optional per-line discount, as a percentage (e.g. 10 for 10% off).",
        required: !1
      })
    }
  }),
  idempotencyKey: import_pieces_framework2.Property.ShortText({
    displayName: "Duplicate Protection Key",
    description: "Advanced. If the flow retries this step, sending the same key prevents creating a duplicate. Most users can leave this blank.",
    required: !1
  }),
  externalId: (displayName = "Your Reference ID") => import_pieces_framework2.Property.ShortText({
    displayName,
    description: "Optional. Store an ID from another system here (e.g. the order number from your online store) so you can find this record in Wafeq later.",
    required: !1
  })
}, COMMON_CURRENCIES = [
  { code: "AED", name: "UAE Dirham" },
  { code: "SAR", name: "Saudi Riyal" },
  { code: "USD", name: "US Dollar" },
  { code: "EUR", name: "Euro" },
  { code: "GBP", name: "British Pound" },
  { code: "QAR", name: "Qatari Riyal" },
  { code: "KWD", name: "Kuwaiti Dinar" },
  { code: "BHD", name: "Bahraini Dinar" },
  { code: "OMR", name: "Omani Rial" },
  { code: "JOD", name: "Jordanian Dinar" },
  { code: "EGP", name: "Egyptian Pound" },
  { code: "TRY", name: "Turkish Lira" },
  { code: "INR", name: "Indian Rupee" },
  { code: "CNY", name: "Chinese Yuan" },
  { code: "JPY", name: "Japanese Yen" },
  { code: "CAD", name: "Canadian Dollar" },
  { code: "AUD", name: "Australian Dollar" }
];

// ../ap/packages/pieces/community/wafeq/src/lib/common/helpers.ts
var wafeqHelpers = {
  toDate(value) {
    if (value)
      return value.slice(0, 10);
  },
  stripEmpty(obj) {
    let out = {};
    for (let [k, v] of Object.entries(obj))
      v == null || v === "" || (out[k] = v);
    return out;
  },
  mapLineItems({
    items,
    defaultAccount,
    defaultTaxRate
  }) {
    return !items || items.length === 0 ? [] : items.map((line) => {
      let account = line.account || defaultAccount;
      if (!account)
        throw new Error(
          'Every line needs an account. Choose a "Default Account" at the top of the action, or fill in the Account ID on each line.'
        );
      return wafeqHelpers.stripEmpty({
        item: line.item,
        description: line.description,
        quantity: line.quantity,
        unit_amount: line.unit_amount,
        account,
        tax_rate: line.tax_rate || defaultTaxRate,
        discount: line.discount
      });
    });
  }
};

// ../ap/packages/pieces/community/wafeq/src/lib/actions/create-invoice.ts
var createInvoice = (0, import_pieces_framework3.createAction)({
  auth: wafeqAuth,
  name: "create_invoice",
  displayName: "Create Invoice",
  description: "Create a new sales invoice for a customer.",
  audience: "both",
  aiMetadata: {
    description: "Creates a sales (B2B) invoice in Wafeq for a customer contact, with line items, currency, tax handling, and a status (Draft/Sent/Finalized). Choose this for standard customer billing; use Create Simplified Invoice for retail/walk-in sales and Create Quote for estimates. Requires an existing contact ID and a unique invoice_number (reusing one is rejected). Not idempotent \u2014 each call posts a new invoice.",
    idempotent: !1
  },
  props: {
    contact: wafeqProps.contactDropdown({
      displayName: "Customer",
      description: "Who are you billing? Pick from your Wafeq contacts."
    }),
    invoice_number: import_pieces_framework3.Property.ShortText({
      displayName: "Invoice Number",
      description: "A unique number for this invoice (e.g. INV-2024-001). If you reuse a number, Wafeq will reject the invoice.",
      required: !0
    }),
    invoice_date: import_pieces_framework3.Property.DateTime({
      displayName: "Invoice Date",
      description: "The date shown on the invoice. Usually today.",
      required: !0
    }),
    invoice_due_date: import_pieces_framework3.Property.DateTime({
      displayName: "Due Date",
      description: "The date you expect to be paid by.",
      required: !0
    }),
    currency: wafeqProps.currency(),
    default_account: wafeqProps.accountDropdown({
      displayName: "Default Sales Account",
      description: 'Where the money from this invoice is booked (e.g. "Sales Revenue"). This is applied to every line item unless you override it on a specific line.',
      classification: "REVENUE"
    }),
    default_tax_rate: wafeqProps.taxRateDropdown({
      displayName: "Default Tax Rate",
      description: 'Tax applied to every line by default (e.g. "VAT 15%"). Leave blank if this invoice has no tax.',
      required: !1
    }),
    line_items: wafeqProps.lineItemsArray,
    prices_include_tax: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Do the prices above include tax?",
      description: 'If you already baked tax into the unit prices, pick "Yes". If tax should be added on top of the prices, pick "No". Most B2B invoices use "No".',
      required: !1,
      defaultValue: "TAX_EXCLUSIVE",
      options: {
        options: [
          { label: "No \u2014 add tax on top of the prices", value: "TAX_EXCLUSIVE" },
          { label: "Yes \u2014 prices already include tax", value: "TAX_INCLUSIVE" }
        ]
      }
    }),
    status: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Status",
      description: "Draft = still editable, not posted to your books yet. Sent = emailed to the customer. Finalized = locked and posted to your books. Start with Draft if unsure.",
      required: !1,
      defaultValue: "DRAFT",
      options: {
        options: [
          { label: "Draft (recommended \u2014 you can still edit)", value: "DRAFT" },
          { label: "Sent", value: "SENT" },
          { label: "Finalized (locked, posts to books)", value: "FINALIZED" }
        ]
      }
    }),
    language: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Printed Language",
      description: "Language used on the PDF invoice sent to the customer.",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English", value: "en" },
          { label: "Arabic", value: "ar" }
        ]
      }
    }),
    reference: import_pieces_framework3.Property.ShortText({
      displayName: "Reference",
      description: "Optional text printed on the invoice (e.g. a project name or the customer's PO number).",
      required: !1
    }),
    notes: import_pieces_framework3.Property.LongText({
      displayName: "Notes / Message to Customer",
      description: "Optional notes printed at the bottom of the invoice (e.g. payment instructions, thank-you note).",
      required: !1
    }),
    external_id: wafeqProps.externalId("Your Reference ID"),
    idempotency_key: wafeqProps.idempotencyKey
  },
  async run(context) {
    let p = context.propsValue, body = wafeqHelpers.stripEmpty({
      contact: p.contact,
      invoice_number: p.invoice_number,
      invoice_date: wafeqHelpers.toDate(p.invoice_date),
      invoice_due_date: wafeqHelpers.toDate(p.invoice_due_date),
      currency: p.currency,
      line_items: wafeqHelpers.mapLineItems({
        items: p.line_items,
        defaultAccount: p.default_account,
        defaultTaxRate: p.default_tax_rate
      }),
      status: p.status,
      language: p.language,
      tax_amount_type: p.prices_include_tax,
      notes: p.notes,
      reference: p.reference,
      external_id: p.external_id
    }), response = await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/invoices/",
      body,
      idempotencyKey: p.idempotency_key
    });
    return flattenInvoice(response.body);
  }
});
function flattenInvoice(inv) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
  return {
    id: inv.id,
    invoice_number: inv.invoice_number,
    contact_id: (_a = inv.contact) != null ? _a : null,
    status: (_b = inv.status) != null ? _b : null,
    currency: inv.currency,
    amount: inv.amount,
    balance: (_c = inv.balance) != null ? _c : null,
    tax_amount: (_d = inv.tax_amount) != null ? _d : null,
    invoice_date: inv.invoice_date,
    invoice_due_date: inv.invoice_due_date,
    language: (_e = inv.language) != null ? _e : null,
    tax_amount_type: (_f = inv.tax_amount_type) != null ? _f : null,
    reference: (_g = inv.reference) != null ? _g : null,
    notes: (_h = inv.notes) != null ? _h : null,
    external_id: (_i = inv.external_id) != null ? _i : null,
    line_items_count: Array.isArray(inv.line_items) ? inv.line_items.length : 0,
    created_ts: (_j = inv.created_ts) != null ? _j : null,
    modified_ts: (_k = inv.modified_ts) != null ? _k : null
  };
}

// ../ap/packages/pieces/community/wafeq/src/lib/actions/report-invoice-to-tax-authority.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var reportInvoiceToTaxAuthority = (0, import_pieces_framework4.createAction)({
  auth: wafeqAuth,
  name: "report_invoice_to_tax_authority",
  displayName: "Report Invoice to Tax Authority",
  description: "Submit an invoice to the tax authority (ZATCA in Saudi Arabia, FTA in UAE). The invoice must already be finalized in Wafeq before you can report it.",
  audience: "both",
  aiMetadata: {
    description: "Submits an already-finalized Wafeq invoice to the government tax authority (ZATCA in Saudi Arabia, FTA in UAE) for e-invoicing compliance. Choose this after an invoice has been created and moved to Finalized status; it will fail on draft invoices. Requires the invoice ID. Not idempotent \u2014 it is a reporting submission against the external authority.",
    idempotent: !1
  },
  props: {
    invoice: wafeqProps.invoiceDropdown({
      description: 'Pick which invoice to submit. \u26A0\uFE0F The invoice must be in "Finalized" status first \u2014 if you just created it as a Draft, change its status to Finalized in Wafeq before running this action.'
    }),
    idempotency_key: wafeqProps.idempotencyKey
  },
  async run(context) {
    let { invoice, idempotency_key } = context.propsValue, response = await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: `/invoices/${invoice}/tax-authority/report/`,
      idempotencyKey: idempotency_key
    });
    return {
      invoice_id: invoice,
      status: "submitted",
      response: response.body
    };
  }
});

// ../ap/packages/pieces/community/wafeq/src/lib/actions/create-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createContact = (0, import_pieces_framework5.createAction)({
  auth: wafeqAuth,
  name: "create_contact",
  displayName: "Create Contact",
  description: "Add a new customer or supplier to your Wafeq address book so you can invoice them or record bills from them.",
  audience: "both",
  aiMetadata: {
    description: "Creates a contact (customer, supplier, or both) in Wafeq. Choose this when an invoice/bill needs a contact that does not exist yet; run Find Contact first to avoid duplicates, since this does not de-duplicate. Only name and relationship are required; tax_registration_number is needed for tax-compliant B2B documents. Not idempotent \u2014 each call creates a new contact.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Name",
      description: "The business name (for companies) or full name (for individuals). This is what appears on invoices and bills.",
      required: !0
    }),
    relationship: import_pieces_framework5.Property.StaticMultiSelectDropdown({
      displayName: "Type of Contact",
      description: `Pick "Customer" if you sell to them, "Supplier" if you buy from them, or both if they're on both sides.`,
      required: !0,
      defaultValue: ["CUSTOMER"],
      options: {
        options: [
          { label: "Customer (I sell to them)", value: "CUSTOMER" },
          { label: "Supplier (I buy from them)", value: "SUPPLIER" }
        ]
      }
    }),
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      description: "Where to send invoices and other documents. Optional but recommended \u2014 Wafeq can email invoices directly.",
      required: !1
    }),
    phone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone",
      description: "Main phone number.",
      required: !1
    }),
    tax_registration_number: import_pieces_framework5.Property.ShortText({
      displayName: "VAT / Tax Number",
      description: "The contact's VAT registration number (also called TRN in UAE, TIN in some countries). Required for tax-compliant B2B invoices \u2014 leave blank for individuals.",
      required: !1
    }),
    country: import_pieces_framework5.Property.ShortText({
      displayName: "Country Code",
      description: "Two-letter ISO country code: AE = UAE, SA = Saudi Arabia, US = United States, GB = UK, etc.",
      required: !1
    }),
    city: import_pieces_framework5.Property.ShortText({
      displayName: "City",
      description: "The city name.",
      required: !1
    }),
    address: import_pieces_framework5.Property.LongText({
      displayName: "Street Address",
      description: "Street / building address. This appears on printed invoices.",
      required: !1
    }),
    postal_code: import_pieces_framework5.Property.ShortText({
      displayName: "Postal / ZIP Code",
      description: "The postal code. Must be exactly 5 digits for Saudi Arabia addresses.",
      required: !1
    }),
    external_id: wafeqProps.externalId("Your Customer ID"),
    idempotency_key: wafeqProps.idempotencyKey
  },
  async run(context) {
    let p = context.propsValue, body = wafeqHelpers.stripEmpty({
      name: p.name,
      relationship: p.relationship,
      email: p.email,
      phone: p.phone,
      tax_registration_number: p.tax_registration_number,
      country: p.country,
      city: p.city,
      address: p.address,
      postal_code: p.postal_code,
      external_id: p.external_id
    }), response = await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      path: "/contacts/",
      body,
      idempotencyKey: p.idempotency_key
    });
    return flattenContact(response.body);
  }
});
function flattenContact(c) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
  return {
    id: c.id,
    name: c.name,
    email: (_a = c.email) != null ? _a : null,
    phone: (_b = c.phone) != null ? _b : null,
    country: (_c = c.country) != null ? _c : null,
    city: (_d = c.city) != null ? _d : null,
    address: (_e = c.address) != null ? _e : null,
    postal_code: (_f = c.postal_code) != null ? _f : null,
    tax_registration_number: (_g = c.tax_registration_number) != null ? _g : null,
    relationship: Array.isArray(c.relationship) ? c.relationship.join(", ") : null,
    external_id: (_h = c.external_id) != null ? _h : null,
    created_ts: (_i = c.created_ts) != null ? _i : null,
    modified_ts: (_j = c.modified_ts) != null ? _j : null
  };
}

// ../ap/packages/pieces/community/wafeq/src/lib/actions/find-contact.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var findContact = (0, import_pieces_framework6.createAction)({
  auth: wafeqAuth,
  name: "find_contact",
  displayName: "Find Contact",
  description: "Search your Wafeq contacts by name, email, phone, or tax number. Useful for checking if a customer already exists before creating a new one.",
  audience: "both",
  aiMetadata: {
    description: "Searches your Wafeq contacts by name, email, phone, or tax number. Choose this to look up a contact ID before creating an invoice/bill, or to check whether a customer already exists. Leaving the search keyword blank lists all contacts (a fetch-all mode); supply a keyword, relationship filter, or external_id to narrow results. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    keyword: import_pieces_framework6.Property.ShortText({
      displayName: "Search For",
      description: "Type what you're looking for \u2014 part of a name, email, phone number, or tax number works. Leave blank to list all contacts.",
      required: !1
    }),
    relationship: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Type of Contact",
      description: "Only show customers, only suppliers, or both.",
      required: !1,
      options: {
        options: [
          { label: "Any", value: "" },
          { label: "Customer", value: "CUSTOMER" },
          { label: "Supplier", value: "SUPPLIER" }
        ]
      }
    }),
    external_id: import_pieces_framework6.Property.ShortText({
      displayName: "Your Reference ID (optional)",
      description: "If you stored a reference ID when creating the contact (e.g. your CRM's customer ID), enter it here for an exact match.",
      required: !1
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Max Results",
      description: "How many contacts to return at most. Default is 50, maximum is 200.",
      required: !1,
      defaultValue: 50
    })
  },
  async run(context) {
    var _a;
    let p = context.propsValue, queryParams = wafeqHelpers.stripEmpty({
      keyword: p.keyword,
      relationship: p.relationship,
      external_id: p.external_id,
      page_size: String(Math.min(Math.max((_a = p.limit) != null ? _a : 50, 1), 200))
    }), response = await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      path: "/contacts/",
      queryParams
    });
    return {
      total: response.body.count,
      returned: response.body.results.length,
      results: response.body.results.map(flattenContact2)
    };
  }
});
function flattenContact2(c) {
  var _a, _b, _c, _d, _e, _f, _g, _h;
  return {
    id: c.id,
    name: c.name,
    email: (_a = c.email) != null ? _a : null,
    phone: (_b = c.phone) != null ? _b : null,
    country: (_c = c.country) != null ? _c : null,
    city: (_d = c.city) != null ? _d : null,
    tax_registration_number: (_e = c.tax_registration_number) != null ? _e : null,
    relationship: Array.isArray(c.relationship) ? c.relationship.join(", ") : null,
    external_id: (_f = c.external_id) != null ? _f : null,
    created_ts: (_g = c.created_ts) != null ? _g : null,
    modified_ts: (_h = c.modified_ts) != null ? _h : null
  };
}

// ../ap/packages/pieces/community/wafeq/src/lib/actions/create-bill.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var createBill = (0, import_pieces_framework7.createAction)({
  auth: wafeqAuth,
  name: "create_bill",
  displayName: "Create Bill",
  description: "Record a bill you received from a supplier (e.g. a vendor invoice you need to pay).",
  audience: "both",
  aiMetadata: {
    description: "Records a supplier bill (accounts payable) in Wafeq with line items, an expense account, currency, and tax handling, in Draft or Authorized status. Choose this for vendor invoices you owe, as opposed to Create Invoice for money customers owe you. Requires an existing supplier contact ID and a bill_number unique per supplier. Not idempotent \u2014 each call creates a new bill.",
    idempotent: !1
  },
  props: {
    contact: wafeqProps.contactDropdown({
      displayName: "Supplier",
      description: "Who sent you this bill? Pick from your Wafeq contacts."
    }),
    bill_number: import_pieces_framework7.Property.ShortText({
      displayName: "Bill Number",
      description: "The invoice number printed on the supplier's bill (so you can match it later). Must be unique per supplier in your Wafeq org.",
      required: !0
    }),
    bill_date: import_pieces_framework7.Property.DateTime({
      displayName: "Bill Date",
      description: "The date printed on the supplier's bill.",
      required: !0
    }),
    bill_due_date: import_pieces_framework7.Property.DateTime({
      displayName: "Due Date",
      description: "The date you need to pay this bill by.",
      required: !0
    }),
    currency: wafeqProps.currency(),
    default_account: wafeqProps.accountDropdown({
      displayName: "Default Expense Account",
      description: 'Where this cost is booked in your books (e.g. "Office Supplies", "Software Subscriptions"). Applied to every line item unless you override it on a specific line.',
      classification: "EXPENSE"
    }),
    default_tax_rate: wafeqProps.taxRateDropdown({
      displayName: "Default Tax Rate",
      description: 'Tax applied to every line by default (e.g. "VAT 15% on purchases"). Leave blank if no tax.',
      required: !1
    }),
    line_items: wafeqProps.lineItemsArray,
    prices_include_tax: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Do the prices above include tax?",
      description: `If the supplier's prices already include tax, pick "Yes". If tax should be added on top, pick "No".`,
      required: !1,
      defaultValue: "TAX_EXCLUSIVE",
      options: {
        options: [
          { label: "No \u2014 add tax on top of the prices", value: "TAX_EXCLUSIVE" },
          { label: "Yes \u2014 prices already include tax", value: "TAX_INCLUSIVE" }
        ]
      }
    }),
    status: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Status",
      description: "Draft = still editable, not in your books yet. Authorized = approved and posted to your books. Start with Draft if unsure.",
      required: !1,
      defaultValue: "DRAFT",
      options: {
        options: [
          { label: "Draft (recommended)", value: "DRAFT" },
          { label: "Authorized (posts to your books)", value: "AUTHORIZED" }
        ]
      }
    }),
    reference: import_pieces_framework7.Property.ShortText({
      displayName: "Reference",
      description: "Optional free text (e.g. project name, department).",
      required: !1
    }),
    order_number: import_pieces_framework7.Property.ShortText({
      displayName: "Your PO Number",
      description: "Optional. The purchase order number from your own system, if you issued one.",
      required: !1
    }),
    notes: import_pieces_framework7.Property.LongText({
      displayName: "Internal Notes",
      description: "Optional notes visible in Wafeq. Not printed on anything and not shared with the supplier.",
      required: !1
    }),
    external_id: wafeqProps.externalId(),
    idempotency_key: wafeqProps.idempotencyKey
  },
  async run(context) {
    let p = context.propsValue, body = wafeqHelpers.stripEmpty({
      contact: p.contact,
      bill_number: p.bill_number,
      bill_date: wafeqHelpers.toDate(p.bill_date),
      bill_due_date: wafeqHelpers.toDate(p.bill_due_date),
      currency: p.currency,
      line_items: wafeqHelpers.mapLineItems({
        items: p.line_items,
        defaultAccount: p.default_account,
        defaultTaxRate: p.default_tax_rate
      }),
      status: p.status,
      tax_amount_type: p.prices_include_tax,
      reference: p.reference,
      order_number: p.order_number,
      notes: p.notes,
      external_id: p.external_id
    }), response = await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.POST,
      path: "/bills/",
      body,
      idempotencyKey: p.idempotency_key
    });
    return flattenBill(response.body);
  }
});
function flattenBill(b) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
  return {
    id: b.id,
    bill_number: b.bill_number,
    contact_id: (_a = b.contact) != null ? _a : null,
    status: (_b = b.status) != null ? _b : null,
    currency: b.currency,
    amount: b.amount,
    balance: (_c = b.balance) != null ? _c : null,
    tax_amount: (_d = b.tax_amount) != null ? _d : null,
    bill_date: b.bill_date,
    bill_due_date: b.bill_due_date,
    reference: (_e = b.reference) != null ? _e : null,
    order_number: (_f = b.order_number) != null ? _f : null,
    notes: (_g = b.notes) != null ? _g : null,
    external_id: (_h = b.external_id) != null ? _h : null,
    line_items_count: Array.isArray(b.line_items) ? b.line_items.length : 0,
    created_ts: (_i = b.created_ts) != null ? _i : null,
    modified_ts: (_j = b.modified_ts) != null ? _j : null
  };
}

// ../ap/packages/pieces/community/wafeq/src/lib/actions/record-payment.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var recordPayment = (0, import_pieces_framework8.createAction)({
  auth: wafeqAuth,
  name: "record_payment",
  displayName: "Record Payment",
  description: "Record money received from a customer (paying an invoice) or money sent to a supplier (paying a bill).",
  audience: "both",
  aiMetadata: {
    description: 'Records a payment in Wafeq and applies it against a document. A "payment for" selector switches the target between a single invoice, bill, or credit note picked from a list, or an Advanced mode that splits one payment across multiple documents by ID. Choose this to settle or partially settle amounts owed; requires the amount, currency, and the bank/cash/card account the money moved through. Not idempotent \u2014 each call posts a new payment.',
    idempotent: !1
  },
  props: {
    payment_for: import_pieces_framework8.Property.StaticDropdown({
      displayName: "What is this payment for?",
      description: 'Pick what the money was used to pay. "Advanced" lets you split a single payment across multiple invoices or bills.',
      required: !0,
      defaultValue: "invoice",
      options: {
        options: [
          { label: "An invoice (customer paid me)", value: "invoice" },
          { label: "A bill (I paid a supplier)", value: "bill" },
          { label: "A credit note", value: "credit_note" },
          {
            label: "Advanced \u2014 split across multiple documents",
            value: "advanced"
          }
        ]
      }
    }),
    target: import_pieces_framework8.Property.DynamicProperties({
      auth: wafeqAuth,
      displayName: "Paying",
      required: !0,
      refreshers: ["payment_for"],
      props: async ({ auth, payment_for }) => {
        let pick = payment_for;
        if (!auth) return {};
        let authKey = auth.secret_text;
        return pick === "invoice" ? { invoice: await buildInvoiceDropdown(authKey) } : pick === "bill" ? { bill: await buildBillDropdown(authKey) } : pick === "credit_note" ? { credit_note: await buildCreditNoteDropdown(authKey) } : pick === "advanced" ? {
          allocations: import_pieces_framework8.Property.Array({
            displayName: "Allocations",
            description: "Add one row per document you're paying. The amounts should add up to the total Amount below.",
            required: !0,
            properties: {
              type: import_pieces_framework8.Property.StaticDropdown({
                displayName: "Type",
                required: !0,
                defaultValue: "invoice",
                options: {
                  options: [
                    { label: "Invoice", value: "invoice" },
                    { label: "Bill", value: "bill" },
                    { label: "Credit note", value: "credit_note" }
                  ]
                }
              }),
              target_id: import_pieces_framework8.Property.ShortText({
                displayName: "Document ID",
                description: "Paste the invoice / bill / credit note ID (from a previous Create action).",
                required: !0
              }),
              amount: import_pieces_framework8.Property.Number({
                displayName: "Amount Applied",
                description: "How much of the payment goes to this document, in the document's own currency.",
                required: !0
              })
            }
          })
        } : {};
      }
    }),
    amount: import_pieces_framework8.Property.Number({
      displayName: "Total Amount Paid",
      description: "The full amount of the payment, including any fees. For a simple single-document payment, this should match the unpaid balance.",
      required: !0
    }),
    amount_to_pcy: import_pieces_framework8.Property.Number({
      displayName: "Amount in Company Currency",
      description: "The amount in the payment currency. Must be greater than zero.",
      required: !0
    }),
    currency: wafeqProps.currency(),
    date: import_pieces_framework8.Property.DateTime({
      displayName: "Payment Date",
      description: "When the payment was made or received. Use the date on your bank statement.",
      required: !0
    }),
    paid_through_account: wafeqProps.accountDropdown({
      displayName: "Paid Through Account",
      description: "Which bank, cash, or card account the money moved through. Only payment-enabled accounts are shown.",
      paymentEnabledOnly: !0
    }),
    contact: wafeqProps.contactDropdown({
      displayName: "Contact (optional)",
      description: "Optional. The customer or supplier this payment is with. Usually auto-detected from the document.",
      required: !1
    }),
    payment_fees: import_pieces_framework8.Property.Number({
      displayName: "Payment Fees (optional)",
      description: "Optional. Any bank or card processing fees deducted from the payment (e.g. Stripe fee). Leave blank if none.",
      required: !1
    }),
    payment_fees_account: wafeqProps.accountDropdown({
      displayName: "Fees Account",
      description: 'Only needed if you entered Payment Fees above. Pick the expense account the fees are booked to (e.g. "Bank Charges").',
      required: !1,
      classification: "EXPENSE"
    }),
    reference: import_pieces_framework8.Property.ShortText({
      displayName: "Reference",
      description: "Optional. A bank transaction ID or any note you want to attach.",
      required: !1
    }),
    external_id: wafeqProps.externalId(),
    idempotency_key: wafeqProps.idempotencyKey
  },
  async run(context) {
    var _a, _b;
    let p = context.propsValue, target = (_a = p.target) != null ? _a : {}, paymentFor = p.payment_for, invoicePayments, billPayments, creditNotePayments;
    if (paymentFor === "invoice" && target.invoice)
      invoicePayments = [
        {
          invoice: target.invoice,
          amount: p.amount,
          amount_to_pcy: p.amount_to_pcy
        }
      ];
    else if (paymentFor === "bill" && target.bill)
      billPayments = [
        {
          bill: target.bill,
          amount: p.amount,
          amount_to_pcy: p.amount_to_pcy
        }
      ];
    else if (paymentFor === "credit_note" && target.credit_note)
      creditNotePayments = [
        {
          credit_note: target.credit_note,
          amount: p.amount,
          amount_to_pcy: p.amount_to_pcy
        }
      ];
    else if (paymentFor === "advanced") {
      let allocations = (_b = target.allocations) != null ? _b : [];
      invoicePayments = buildAllocationList(allocations, "invoice"), billPayments = buildAllocationList(allocations, "bill"), creditNotePayments = buildAllocationList(allocations, "credit_note");
    }
    let body = wafeqHelpers.stripEmpty({
      amount: p.amount,
      currency: p.currency,
      date: wafeqHelpers.toDate(p.date),
      paid_through_account: p.paid_through_account,
      contact: p.contact,
      invoice_payments: invoicePayments,
      bill_payments: billPayments,
      credit_note_payments: creditNotePayments,
      payment_fees: p.payment_fees,
      payment_fees_account: p.payment_fees_account,
      reference: p.reference,
      external_id: p.external_id
    }), response = await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.POST,
      path: "/payments/",
      body,
      idempotencyKey: p.idempotency_key
    });
    return flattenPayment(response.body);
  }
});
async function buildInvoiceDropdown(authKey) {
  let response = await wafeqApiCall({
    apiKey: authKey,
    method: import_pieces_common9.HttpMethod.GET,
    path: "/invoices/",
    queryParams: { page_size: "100" }
  });
  return import_pieces_framework8.Property.StaticDropdown({
    displayName: "Invoice Being Paid",
    description: "Pick the invoice this payment settles.",
    required: !0,
    options: {
      options: response.body.results.map((i) => ({
        label: `#${i.invoice_number} \u2014 ${i.currency} ${i.amount}${i.balance != null ? ` (${i.currency} ${i.balance} unpaid)` : ""}`,
        value: i.id
      }))
    }
  });
}
async function buildBillDropdown(authKey) {
  let response = await wafeqApiCall({
    apiKey: authKey,
    method: import_pieces_common9.HttpMethod.GET,
    path: "/bills/",
    queryParams: { page_size: "100" }
  });
  return import_pieces_framework8.Property.StaticDropdown({
    displayName: "Bill Being Paid",
    description: "Pick the bill this payment settles.",
    required: !0,
    options: {
      options: response.body.results.map((b) => ({
        label: `#${b.bill_number} \u2014 ${b.currency} ${b.amount}${b.balance != null ? ` (${b.currency} ${b.balance} unpaid)` : ""}`,
        value: b.id
      }))
    }
  });
}
async function buildCreditNoteDropdown(authKey) {
  let response = await wafeqApiCall({
    apiKey: authKey,
    method: import_pieces_common9.HttpMethod.GET,
    path: "/credit-notes/",
    queryParams: { page_size: "100" }
  });
  return import_pieces_framework8.Property.StaticDropdown({
    displayName: "Credit Note",
    description: "Pick the credit note to apply.",
    required: !0,
    options: {
      options: response.body.results.map((c) => ({
        label: `#${c.credit_note_number} \u2014 ${c.currency} ${c.amount}`,
        value: c.id
      }))
    }
  });
}
function buildAllocationList(allocations, type) {
  let matches = allocations.filter((a) => a.type === type && a.target_id && a.amount != null).map((a) => {
    var _a;
    return {
      [type]: a.target_id,
      amount: a.amount,
      amount_to_pcy: (_a = a.amount_to_pcy) != null ? _a : a.amount
    };
  });
  return matches.length > 0 ? matches : void 0;
}
function flattenPayment(p) {
  var _a, _b, _c, _d, _e, _f;
  return {
    id: p.id,
    amount: p.amount,
    currency: p.currency,
    date: p.date,
    contact_id: (_a = p.contact) != null ? _a : null,
    paid_through_account_id: p.paid_through_account,
    payment_fees: (_b = p.payment_fees) != null ? _b : null,
    reference: (_c = p.reference) != null ? _c : null,
    external_id: (_d = p.external_id) != null ? _d : null,
    invoice_payments_count: Array.isArray(p.invoice_payments) ? p.invoice_payments.length : 0,
    bill_payments_count: Array.isArray(p.bill_payments) ? p.bill_payments.length : 0,
    credit_note_payments_count: Array.isArray(p.credit_note_payments) ? p.credit_note_payments.length : 0,
    created_ts: (_e = p.created_ts) != null ? _e : null,
    modified_ts: (_f = p.modified_ts) != null ? _f : null
  };
}

// ../ap/packages/pieces/community/wafeq/src/lib/actions/create-simplified-invoice.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var createSimplifiedInvoice = (0, import_pieces_framework9.createAction)({
  auth: wafeqAuth,
  name: "create_simplified_invoice",
  displayName: "Create Simplified Invoice",
  description: "Create a simplified (retail) tax invoice \u2014 used for walk-in / over-the-counter sales, especially in Saudi Arabia under ZATCA. Use a regular invoice for B2B sales instead.",
  audience: "both",
  aiMetadata: {
    description: "Creates a simplified (retail/B2C) tax invoice in Wafeq for walk-in or over-the-counter sales, especially under Saudi ZATCA rules. Choose this for point-of-sale receipts where the customer is optional; use Create Invoice for B2B sales to a named contact. Requires line items and the account the money was received into; invoice_number can be auto-generated. Not idempotent \u2014 each call creates a new invoice.",
    idempotent: !1
  },
  props: {
    invoice_date: import_pieces_framework9.Property.DateTime({
      displayName: "Sale Date",
      description: "The date the sale happened. Usually today.",
      required: !0
    }),
    currency: wafeqProps.currency(),
    paid_through_account: wafeqProps.accountDropdown({
      displayName: "Where was the money received?",
      description: "Pick the bank, cash drawer, or POS account the customer paid into. Only payment-enabled accounts are shown.",
      paymentEnabledOnly: !0
    }),
    default_account: wafeqProps.accountDropdown({
      displayName: "Default Sales Account",
      description: 'Where the sale is booked (e.g. "Sales Revenue"). Applied to every line unless overridden.',
      classification: "REVENUE"
    }),
    default_tax_rate: wafeqProps.taxRateDropdown({
      displayName: "Default Tax Rate",
      description: 'Tax applied to every line by default (e.g. "VAT 15%").',
      required: !1
    }),
    line_items: wafeqProps.lineItemsArray,
    prices_include_tax: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Do the prices above include tax?",
      description: 'Retail prices almost always include tax, so "Yes" is recommended.',
      required: !1,
      defaultValue: "TAX_INCLUSIVE",
      options: {
        options: [
          {
            label: "Yes \u2014 prices already include tax (recommended for retail)",
            value: "TAX_INCLUSIVE"
          },
          { label: "No \u2014 add tax on top of the prices", value: "TAX_EXCLUSIVE" }
        ]
      }
    }),
    contact: wafeqProps.contactDropdown({
      displayName: "Customer (optional)",
      description: "Optional. Most simplified invoices are for walk-in customers and leave this blank.",
      required: !1
    }),
    invoice_number: import_pieces_framework9.Property.ShortText({
      displayName: "Invoice Number (optional)",
      description: "Leave blank to let Wafeq auto-generate one.",
      required: !1
    }),
    status: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Status",
      description: "Paid = the sale is complete. Draft = not posted to books yet.",
      required: !1,
      defaultValue: "PAID",
      options: {
        options: [
          { label: "Paid (recommended)", value: "PAID" },
          { label: "Draft", value: "DRAFT" }
        ]
      }
    }),
    language: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Printed Language",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English", value: "en" },
          { label: "Arabic", value: "ar" }
        ]
      }
    }),
    reference: import_pieces_framework9.Property.ShortText({
      displayName: "Reference",
      description: "Optional free text shown on the invoice.",
      required: !1
    }),
    notes: import_pieces_framework9.Property.LongText({
      displayName: "Notes",
      description: "Optional notes printed on the invoice.",
      required: !1
    }),
    external_id: wafeqProps.externalId(),
    idempotency_key: wafeqProps.idempotencyKey
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
    let p = context.propsValue, body = wafeqHelpers.stripEmpty({
      invoice_date: wafeqHelpers.toDate(p.invoice_date),
      currency: p.currency,
      paid_through_account: p.paid_through_account,
      line_items: wafeqHelpers.mapLineItems({
        items: p.line_items,
        defaultAccount: p.default_account,
        defaultTaxRate: p.default_tax_rate
      }),
      contact: p.contact,
      invoice_number: p.invoice_number,
      status: p.status,
      tax_amount_type: p.prices_include_tax,
      language: p.language,
      notes: p.notes,
      reference: p.reference,
      external_id: p.external_id
    }), inv = (await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.POST,
      path: "/simplified-invoices/",
      body,
      idempotencyKey: p.idempotency_key
    })).body;
    return {
      id: inv.id,
      invoice_number: (_a = inv.invoice_number) != null ? _a : null,
      contact_id: (_b = inv.contact) != null ? _b : null,
      status: (_c = inv.status) != null ? _c : null,
      currency: inv.currency,
      amount: inv.amount,
      tax_amount: (_d = inv.tax_amount) != null ? _d : null,
      invoice_date: inv.invoice_date,
      paid_through_account_id: inv.paid_through_account,
      language: (_e = inv.language) != null ? _e : null,
      tax_amount_type: (_f = inv.tax_amount_type) != null ? _f : null,
      reference: (_g = inv.reference) != null ? _g : null,
      notes: (_h = inv.notes) != null ? _h : null,
      external_id: (_i = inv.external_id) != null ? _i : null,
      line_items_count: Array.isArray(inv.line_items) ? inv.line_items.length : 0,
      created_ts: (_j = inv.created_ts) != null ? _j : null,
      modified_ts: (_k = inv.modified_ts) != null ? _k : null
    };
  }
});

// ../ap/packages/pieces/community/wafeq/src/lib/actions/create-credit-note.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var createCreditNote = (0, import_pieces_framework10.createAction)({
  auth: wafeqAuth,
  name: "create_credit_note",
  displayName: "Create Credit Note",
  description: "Issue a credit note to a customer \u2014 used when you refund them, correct an overcharge, or take back goods. A credit note reduces how much the customer owes you.",
  audience: "both",
  aiMetadata: {
    description: "Issues a customer credit note in Wafeq to refund, correct an overcharge, or reverse goods, reducing what the customer owes. Choose this to credit back against an earlier sale rather than create a new charge. Requires an existing customer contact ID, line items, and a unique credit_note_number. Not idempotent \u2014 each call creates a new credit note.",
    idempotent: !1
  },
  props: {
    contact: wafeqProps.contactDropdown({
      displayName: "Customer",
      description: "Which customer is the credit note for?"
    }),
    credit_note_number: import_pieces_framework10.Property.ShortText({
      displayName: "Credit Note Number",
      description: "A unique number for this credit note (e.g. CN-2024-001).",
      required: !0
    }),
    credit_note_date: import_pieces_framework10.Property.DateTime({
      displayName: "Credit Note Date",
      description: "The date shown on the credit note. Usually today.",
      required: !0
    }),
    currency: wafeqProps.currency(),
    default_account: wafeqProps.accountDropdown({
      displayName: "Default Sales Account",
      description: "The sales account the credit reverses against (usually the same account the original invoice posted to). Applied to every line unless overridden per line.",
      classification: "REVENUE"
    }),
    default_tax_rate: wafeqProps.taxRateDropdown({
      displayName: "Default Tax Rate",
      description: "Tax applied to every line by default. Leave blank for no tax.",
      required: !1
    }),
    line_items: wafeqProps.lineItemsArray,
    prices_include_tax: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Do the prices above include tax?",
      required: !1,
      defaultValue: "TAX_EXCLUSIVE",
      options: {
        options: [
          { label: "No \u2014 add tax on top of the prices", value: "TAX_EXCLUSIVE" },
          { label: "Yes \u2014 prices already include tax", value: "TAX_INCLUSIVE" }
        ]
      }
    }),
    status: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Status",
      description: "Draft = still editable. Sent = marked as sent to the customer. Finalized = locked and posted to your books.",
      required: !1,
      defaultValue: "DRAFT",
      options: {
        options: [
          { label: "Draft (recommended)", value: "DRAFT" },
          { label: "Sent", value: "SENT" },
          { label: "Finalized (locked)", value: "FINALIZED" }
        ]
      }
    }),
    language: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Printed Language",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English", value: "en" },
          { label: "Arabic", value: "ar" }
        ]
      }
    }),
    reference: import_pieces_framework10.Property.ShortText({
      displayName: "Reference",
      description: "Optional text shown on the credit note (e.g. the original invoice number this refunds).",
      required: !1
    }),
    notes: import_pieces_framework10.Property.LongText({
      displayName: "Notes / Message to Customer",
      description: "Optional notes printed on the credit note (e.g. reason for the credit).",
      required: !1
    }),
    external_id: wafeqProps.externalId(),
    idempotency_key: wafeqProps.idempotencyKey
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
    let p = context.propsValue, body = wafeqHelpers.stripEmpty({
      contact: p.contact,
      credit_note_number: p.credit_note_number,
      credit_note_date: wafeqHelpers.toDate(p.credit_note_date),
      currency: p.currency,
      line_items: wafeqHelpers.mapLineItems({
        items: p.line_items,
        defaultAccount: p.default_account,
        defaultTaxRate: p.default_tax_rate
      }),
      status: p.status,
      tax_amount_type: p.prices_include_tax,
      language: p.language,
      reference: p.reference,
      notes: p.notes,
      external_id: p.external_id
    }), cn = (await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.POST,
      path: "/credit-notes/",
      body,
      idempotencyKey: p.idempotency_key
    })).body;
    return {
      id: cn.id,
      credit_note_number: cn.credit_note_number,
      contact_id: (_a = cn.contact) != null ? _a : null,
      status: (_b = cn.status) != null ? _b : null,
      currency: cn.currency,
      amount: cn.amount,
      balance: (_c = cn.balance) != null ? _c : null,
      tax_amount: (_d = cn.tax_amount) != null ? _d : null,
      credit_note_date: cn.credit_note_date,
      language: (_e = cn.language) != null ? _e : null,
      tax_amount_type: (_f = cn.tax_amount_type) != null ? _f : null,
      reference: (_g = cn.reference) != null ? _g : null,
      notes: (_h = cn.notes) != null ? _h : null,
      external_id: (_i = cn.external_id) != null ? _i : null,
      line_items_count: Array.isArray(cn.line_items) ? cn.line_items.length : 0,
      created_ts: (_j = cn.created_ts) != null ? _j : null,
      modified_ts: (_k = cn.modified_ts) != null ? _k : null
    };
  }
});

// ../ap/packages/pieces/community/wafeq/src/lib/actions/create-quote.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var createQuote = (0, import_pieces_framework11.createAction)({
  auth: wafeqAuth,
  name: "create_quote",
  displayName: "Create Quote",
  description: 'Send a customer a quote (also called an estimate). Quotes can later be converted into an invoice with the "Convert Quote to Invoice" action.',
  audience: "both",
  aiMetadata: {
    description: "Creates a customer quote/estimate in Wafeq with line items, currency, and tax handling, in Draft or Sent status. Choose this to propose pricing before billing; once accepted, turn it into an invoice with Convert Quote to Invoice rather than re-keying. Requires an existing customer contact ID and a unique quote_number. Not idempotent \u2014 each call creates a new quote.",
    idempotent: !1
  },
  props: {
    contact: wafeqProps.contactDropdown({
      displayName: "Customer",
      description: "Who is the quote for?"
    }),
    quote_number: import_pieces_framework11.Property.ShortText({
      displayName: "Quote Number",
      description: "A unique number for this quote (e.g. QT-2024-001). Must not reuse an existing quote number.",
      required: !0
    }),
    quote_date: import_pieces_framework11.Property.DateTime({
      displayName: "Quote Date",
      description: "The date shown on the quote. Usually today.",
      required: !0
    }),
    currency: wafeqProps.currency(),
    default_account: wafeqProps.accountDropdown({
      displayName: "Default Sales Account",
      description: "Where the revenue is booked when this quote becomes an invoice. Applied to every line unless overridden per line.",
      classification: "REVENUE"
    }),
    default_tax_rate: wafeqProps.taxRateDropdown({
      displayName: "Default Tax Rate",
      description: "Tax applied to every line by default. Leave blank for no tax.",
      required: !1
    }),
    line_items: wafeqProps.lineItemsArray,
    prices_include_tax: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Do the prices above include tax?",
      description: 'Pick "Yes" if the prices already include tax, "No" if tax should be added on top.',
      required: !1,
      defaultValue: "TAX_EXCLUSIVE",
      options: {
        options: [
          { label: "No \u2014 add tax on top of the prices", value: "TAX_EXCLUSIVE" },
          { label: "Yes \u2014 prices already include tax", value: "TAX_INCLUSIVE" }
        ]
      }
    }),
    status: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Status",
      description: "Draft = still editable. Sent = marked as sent to the customer. Start with Draft if unsure.",
      required: !1,
      defaultValue: "DRAFT",
      options: {
        options: [
          { label: "Draft (recommended)", value: "DRAFT" },
          { label: "Sent", value: "SENT" }
        ]
      }
    }),
    language: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Printed Language",
      description: "Language used on the quote PDF.",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English", value: "en" },
          { label: "Arabic", value: "ar" }
        ]
      }
    }),
    purchase_order: import_pieces_framework11.Property.ShortText({
      displayName: "Customer's PO Number",
      description: "Optional. If the customer sent you a purchase order number, enter it here so it appears on the quote.",
      required: !1
    }),
    reference: import_pieces_framework11.Property.ShortText({
      displayName: "Reference",
      description: "Optional free text shown on the quote (e.g. project name).",
      required: !1
    }),
    notes: import_pieces_framework11.Property.LongText({
      displayName: "Notes / Message to Customer",
      description: "Optional notes printed at the bottom of the quote.",
      required: !1
    }),
    external_id: wafeqProps.externalId(),
    idempotency_key: wafeqProps.idempotencyKey
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
    let p = context.propsValue, body = wafeqHelpers.stripEmpty({
      contact: p.contact,
      quote_number: p.quote_number,
      quote_date: wafeqHelpers.toDate(p.quote_date),
      currency: p.currency,
      line_items: wafeqHelpers.mapLineItems({
        items: p.line_items,
        defaultAccount: p.default_account,
        defaultTaxRate: p.default_tax_rate
      }),
      status: p.status,
      tax_amount_type: p.prices_include_tax,
      language: p.language,
      purchase_order: p.purchase_order,
      notes: p.notes,
      reference: p.reference,
      external_id: p.external_id
    }), q = (await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.POST,
      path: "/quotes/",
      body,
      idempotencyKey: p.idempotency_key
    })).body;
    return {
      id: q.id,
      quote_number: q.quote_number,
      contact_id: (_a = q.contact) != null ? _a : null,
      status: (_b = q.status) != null ? _b : null,
      currency: q.currency,
      amount: q.amount,
      tax_amount: (_c = q.tax_amount) != null ? _c : null,
      quote_date: q.quote_date,
      language: (_d = q.language) != null ? _d : null,
      tax_amount_type: (_e = q.tax_amount_type) != null ? _e : null,
      purchase_order: (_f = q.purchase_order) != null ? _f : null,
      reference: (_g = q.reference) != null ? _g : null,
      notes: (_h = q.notes) != null ? _h : null,
      external_id: (_i = q.external_id) != null ? _i : null,
      line_items_count: Array.isArray(q.line_items) ? q.line_items.length : 0,
      created_ts: (_j = q.created_ts) != null ? _j : null,
      modified_ts: (_k = q.modified_ts) != null ? _k : null
    };
  }
});

// ../ap/packages/pieces/community/wafeq/src/lib/actions/convert-quote-to-invoice.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var convertQuoteToInvoice = (0, import_pieces_framework12.createAction)({
  auth: wafeqAuth,
  name: "convert_quote_to_invoice",
  displayName: "Convert Quote to Invoice",
  description: "Turn a customer-accepted quote into a real invoice. The new invoice copies the customer, line items, and totals from the quote.",
  audience: "both",
  aiMetadata: {
    description: "Converts an existing accepted quote into a new invoice in Wafeq, copying the customer, line items, and totals. Choose this after a quote created via Create Quote is approved, instead of re-entering the data as a fresh invoice. Requires the quote ID. Not idempotent \u2014 each call generates a new invoice from the quote.",
    idempotent: !1
  },
  props: {
    quote: wafeqProps.quoteDropdown({
      description: "Pick the quote you want to convert into an invoice."
    }),
    idempotency_key: wafeqProps.idempotencyKey
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h;
    let { quote, idempotency_key } = context.propsValue, inv = (await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.POST,
      path: `/quotes/${quote}/invoice/`,
      idempotencyKey: idempotency_key
    })).body;
    return {
      id: inv.id,
      invoice_number: (_a = inv.invoice_number) != null ? _a : null,
      contact_id: (_b = inv.contact) != null ? _b : null,
      status: (_c = inv.status) != null ? _c : null,
      currency: (_d = inv.currency) != null ? _d : null,
      amount: (_e = inv.amount) != null ? _e : null,
      invoice_date: (_f = inv.invoice_date) != null ? _f : null,
      invoice_due_date: (_g = inv.invoice_due_date) != null ? _g : null,
      source_quote_id: quote,
      created_ts: (_h = inv.created_ts) != null ? _h : null
    };
  }
});

// ../ap/packages/pieces/community/wafeq/src/lib/actions/download-invoice-pdf.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var downloadInvoicePdf = (0, import_pieces_framework13.createAction)({
  auth: wafeqAuth,
  name: "download_invoice_pdf",
  displayName: "Download Invoice PDF",
  description: "Download an invoice as a PDF. You can attach the file to a Gmail / Outlook email, save it to Google Drive or Dropbox, or upload it anywhere else in the flow.",
  audience: "both",
  aiMetadata: {
    description: "Fetches an existing Wafeq invoice as a PDF file for downstream use (email attachment, cloud storage, upload). Choose this when a flow needs the rendered invoice document rather than its data fields. Requires the invoice ID; the file name is optional. Read-only and idempotent \u2014 re-running downloads the invoice document again without changing anything in Wafeq (the PDF reflects the invoice as it stands at call time).",
    idempotent: !0
  },
  props: {
    invoice: wafeqProps.invoiceDropdown({
      description: "Pick which invoice to download."
    }),
    file_name: import_pieces_framework13.Property.ShortText({
      displayName: "File Name (optional)",
      description: `What to call the downloaded PDF. Leave blank to use "invoice-<id>.pdf". The ".pdf" extension is added automatically if you don't include it.`,
      required: !1
    })
  },
  async run(context) {
    let { invoice, file_name } = context.propsValue, response = await import_pieces_common14.httpClient.sendRequest({
      method: import_pieces_common14.HttpMethod.GET,
      url: `${WAFEQ_API_BASE_URL}/invoices/${invoice}/download/`,
      headers: {
        Authorization: `Api-Key ${context.auth.secret_text}`
      },
      responseType: "arraybuffer"
    }), pdfFileName = file_name && file_name.trim().length > 0 ? file_name.endsWith(".pdf") ? file_name : `${file_name}.pdf` : `invoice-${invoice}.pdf`, fileRef = await context.files.write({
      fileName: pdfFileName,
      data: Buffer.from(response.body)
    });
    return {
      invoice_id: invoice,
      file_name: pdfFileName,
      file: fileRef,
      size_bytes: response.body.byteLength
    };
  }
});

// ../ap/packages/pieces/community/wafeq/src/lib/actions/create-item.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var createItem = (0, import_pieces_framework14.createAction)({
  auth: wafeqAuth,
  name: "create_item",
  displayName: "Create Item",
  description: "Add a product or service to your Wafeq catalog. Catalog items can be quickly added to invoices, bills, and quotes without retyping descriptions or prices.",
  audience: "both",
  aiMetadata: {
    description: "Creates a catalog item (product or service) in Wafeq with optional pricing, SKU, accounts, and inventory tracking, so it can be reused on invoices, bills, and quotes. Choose this to register a reusable item; only name is required. Not idempotent \u2014 each call adds a new catalog item.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework14.Property.ShortText({
      displayName: "Name",
      description: 'What the product or service is called (e.g. "Pro subscription", "Consulting hour"). This shows up in item pickers.',
      required: !0
    }),
    description: import_pieces_framework14.Property.LongText({
      displayName: "Description",
      description: "Optional longer description used as the default line text when this item is added to an invoice.",
      required: !1
    }),
    sku: import_pieces_framework14.Property.ShortText({
      displayName: "SKU / Product Code",
      description: 'Optional short code (e.g. "SUB-PRO-YR"). Useful if you also track the item in another system.',
      required: !1
    }),
    unit_price: import_pieces_framework14.Property.Number({
      displayName: "Sale Price",
      description: "How much you charge customers per unit. Used as the default on invoices and quotes.",
      required: !1
    }),
    unit_cost: import_pieces_framework14.Property.Number({
      displayName: "Purchase Cost",
      description: "How much you pay suppliers per unit. Used as the default on bills.",
      required: !1
    }),
    revenue_account: import_pieces_framework14.Property.ShortText({
      displayName: "Sales Account ID (advanced)",
      description: 'Advanced. The account ID sales of this item post to (e.g. "Sales Revenue"). Leave blank to use your org default.',
      required: !1
    }),
    expense_account: import_pieces_framework14.Property.ShortText({
      displayName: "Cost Account ID (advanced)",
      description: "Advanced. The account ID purchases of this item post to.",
      required: !1
    }),
    revenue_tax_rate: import_pieces_framework14.Property.ShortText({
      displayName: "Sales Tax Rate ID (advanced)",
      description: "Advanced. Tax rate ID applied when selling this item. Leave blank for no default tax.",
      required: !1
    }),
    purchase_tax_rate: import_pieces_framework14.Property.ShortText({
      displayName: "Purchase Tax Rate ID (advanced)",
      description: "Advanced. Tax rate ID applied when buying this item.",
      required: !1
    }),
    is_tracked_inventory: import_pieces_framework14.Property.Checkbox({
      displayName: "Track Stock Levels?",
      description: "Turn on for physical products where you want Wafeq to track inventory counts. Leave off for services and digital products.",
      required: !1,
      defaultValue: !1
    }),
    is_active: import_pieces_framework14.Property.Checkbox({
      displayName: "Active",
      description: "Active items can be added to new invoices. Turn off to hide the item from pickers without deleting it.",
      required: !1,
      defaultValue: !0
    }),
    external_id: wafeqProps.externalId("Your Product ID"),
    idempotency_key: wafeqProps.idempotencyKey
  },
  async run(context) {
    let p = context.propsValue, body = wafeqHelpers.stripEmpty({
      name: p.name,
      description: p.description,
      sku: p.sku,
      unit_price: p.unit_price,
      unit_cost: p.unit_cost,
      revenue_account: p.revenue_account,
      expense_account: p.expense_account,
      revenue_tax_rate: p.revenue_tax_rate,
      purchase_tax_rate: p.purchase_tax_rate,
      is_tracked_inventory: p.is_tracked_inventory,
      is_active: p.is_active,
      external_id: p.external_id
    }), response = await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common15.HttpMethod.POST,
      path: "/items/",
      body,
      idempotencyKey: p.idempotency_key
    });
    return flattenItem(response.body);
  }
});
function flattenItem(i) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m;
  return {
    id: i.id,
    name: i.name,
    description: (_a = i.description) != null ? _a : null,
    sku: (_b = i.sku) != null ? _b : null,
    unit_price: (_c = i.unit_price) != null ? _c : null,
    unit_cost: (_d = i.unit_cost) != null ? _d : null,
    revenue_account_id: (_e = i.revenue_account) != null ? _e : null,
    expense_account_id: (_f = i.expense_account) != null ? _f : null,
    revenue_tax_rate_id: (_g = i.revenue_tax_rate) != null ? _g : null,
    purchase_tax_rate_id: (_h = i.purchase_tax_rate) != null ? _h : null,
    is_tracked_inventory: (_i = i.is_tracked_inventory) != null ? _i : null,
    is_active: (_j = i.is_active) != null ? _j : null,
    external_id: (_k = i.external_id) != null ? _k : null,
    created_ts: (_l = i.created_ts) != null ? _l : null,
    modified_ts: (_m = i.modified_ts) != null ? _m : null
  };
}

// ../ap/packages/pieces/community/wafeq/src/lib/actions/list-items.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var listItems = (0, import_pieces_framework15.createAction)({
  auth: wafeqAuth,
  name: "list_items",
  displayName: "List Items",
  description: "List products and services from your Wafeq catalog. Useful for syncing your catalog to a spreadsheet or another system.",
  audience: "both",
  aiMetadata: {
    description: "Lists catalog items (products and services) from Wafeq, with optional filters for external_id and a created-date range. Choose this to export the catalog or look up item IDs; all filters are optional, so an unfiltered call returns the full catalog up to the result limit. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    external_id: import_pieces_framework15.Property.ShortText({
      displayName: "Your Reference ID (optional)",
      description: "Find a single item by the reference ID you stored when creating it (e.g. your Shopify product ID).",
      required: !1
    }),
    created_after: import_pieces_framework15.Property.DateTime({
      displayName: "Created On or After",
      description: "Only return items created on or after this date.",
      required: !1
    }),
    created_before: import_pieces_framework15.Property.DateTime({
      displayName: "Created On or Before",
      description: "Only return items created on or before this date.",
      required: !1
    }),
    limit: import_pieces_framework15.Property.Number({
      displayName: "Max Results",
      description: "How many items to return at most. Default is 50, maximum is 200.",
      required: !1,
      defaultValue: 50
    })
  },
  async run(context) {
    var _a;
    let p = context.propsValue, queryParams = wafeqHelpers.stripEmpty({
      external_id: p.external_id,
      created_ts_after: p.created_after,
      created_ts_before: p.created_before,
      page_size: String(Math.min(Math.max((_a = p.limit) != null ? _a : 50, 1), 200))
    }), response = await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common16.HttpMethod.GET,
      path: "/items/",
      queryParams
    });
    return {
      total: response.body.count,
      returned: response.body.results.length,
      results: response.body.results.map((i) => {
        var _a2, _b, _c, _d, _e, _f, _g, _h, _i;
        return {
          id: i.id,
          name: i.name,
          description: (_a2 = i.description) != null ? _a2 : null,
          sku: (_b = i.sku) != null ? _b : null,
          unit_price: (_c = i.unit_price) != null ? _c : null,
          unit_cost: (_d = i.unit_cost) != null ? _d : null,
          is_tracked_inventory: (_e = i.is_tracked_inventory) != null ? _e : null,
          is_active: (_f = i.is_active) != null ? _f : null,
          external_id: (_g = i.external_id) != null ? _g : null,
          created_ts: (_h = i.created_ts) != null ? _h : null,
          modified_ts: (_i = i.modified_ts) != null ? _i : null
        };
      })
    };
  }
});

// ../ap/packages/pieces/community/wafeq/src/lib/actions/list-accounts.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var listAccounts = (0, import_pieces_framework16.createAction)({
  auth: wafeqAuth,
  name: "list_accounts",
  displayName: "List Accounts",
  description: "List accounts from your Wafeq chart of accounts. Useful for exporting your chart of accounts or finding account IDs you need elsewhere.",
  audience: "both",
  aiMetadata: {
    description: "Lists accounts from the Wafeq chart of accounts, optionally filtered by classification (Revenue/Expense/Asset/Liability/Equity), payment-enabled status, or external_id. Choose this to resolve the account IDs other actions require (e.g. a sales, expense, or bank account) or to export the chart of accounts. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    classification: import_pieces_framework16.Property.StaticDropdown({
      displayName: "Account Type",
      description: "Filter to one kind of account. Revenue = sales accounts, Expense = cost accounts, Asset = bank / inventory, Liability = loans / payables, Equity = owner's capital.",
      required: !1,
      options: {
        options: [
          { label: "Any", value: "" },
          { label: "Revenue (sales)", value: "REVENUE" },
          { label: "Expense (costs)", value: "EXPENSE" },
          { label: "Asset (bank, cash, inventory)", value: "ASSET" },
          { label: "Liability (loans, payables)", value: "LIABILITY" },
          { label: "Equity (owner capital)", value: "EQUITY" }
        ]
      }
    }),
    is_payment_enabled: import_pieces_framework16.Property.StaticDropdown({
      displayName: "Payment-Enabled Only?",
      description: "Payment-enabled accounts are the bank, cash, or card accounts you can receive or send money from.",
      required: !1,
      options: {
        options: [
          { label: "Any", value: "" },
          { label: "Only payment-enabled (bank, cash, card)", value: "true" },
          { label: "Only non-payment accounts", value: "false" }
        ]
      }
    }),
    external_id: import_pieces_framework16.Property.ShortText({
      displayName: "Your Reference ID (optional)",
      description: "Return the single account matching this reference ID (if you've stored one).",
      required: !1
    }),
    limit: import_pieces_framework16.Property.Number({
      displayName: "Max Results",
      description: "How many accounts to return at most. Default is 100, maximum is 500.",
      required: !1,
      defaultValue: 100
    })
  },
  async run(context) {
    var _a;
    let p = context.propsValue, queryParams = wafeqHelpers.stripEmpty({
      classification: p.classification,
      is_payment_enabled: p.is_payment_enabled,
      external_id: p.external_id,
      page_size: String(Math.min(Math.max((_a = p.limit) != null ? _a : 100, 1), 500))
    }), response = await wafeqApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common17.HttpMethod.GET,
      path: "/accounts/",
      queryParams
    });
    return {
      total: response.body.count,
      returned: response.body.results.length,
      results: response.body.results.map((a) => {
        var _a2, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m;
        return {
          id: a.id,
          account_code: (_a2 = a.account_code) != null ? _a2 : null,
          name_en: (_b = a.name_en) != null ? _b : null,
          name_ar: (_c = a.name_ar) != null ? _c : null,
          classification: (_d = a.classification) != null ? _d : null,
          account_type: (_e = a.account_type) != null ? _e : null,
          is_payment_enabled: (_f = a.is_payment_enabled) != null ? _f : null,
          is_posting: (_g = a.is_posting) != null ? _g : null,
          is_system: (_h = a.is_system) != null ? _h : null,
          is_locked: (_i = a.is_locked) != null ? _i : null,
          parent_id: (_j = a.parent) != null ? _j : null,
          external_id: (_k = a.external_id) != null ? _k : null,
          created_ts: (_l = a.created_ts) != null ? _l : null,
          modified_ts: (_m = a.modified_ts) != null ? _m : null
        };
      })
    };
  }
});

// ../ap/packages/pieces/community/wafeq/src/lib/triggers/new-invoice.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common18.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let queryParams = {
      page_size: "100"
    };
    return lastFetchEpochMS > 0 && (queryParams.created_ts_after = new Date(lastFetchEpochMS).toISOString()), (await wafeqApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common18.HttpMethod.GET,
      path: "/invoices/",
      queryParams
    })).body.results.map((inv) => ({
      epochMilliSeconds: new Date(inv.created_ts).getTime(),
      data: flattenInvoice2(inv)
    }));
  }
}, newInvoice = (0, import_pieces_framework17.createTrigger)({
  auth: wafeqAuth,
  name: "new_invoice",
  displayName: "New Invoice",
  description: "Fires every time a new invoice is created in Wafeq. Useful for logging sales to a spreadsheet, posting to Slack, or syncing to another system.",
  aiMetadata: {
    description: "Fires when a new sales invoice is created in Wafeq (polling by creation time), representing a newly issued customer invoice regardless of its status. Use to react to new sales \u2014 log them, notify a channel, or sync them onward."
  },
  props: {},
  sampleData: {
    id: "inv_abc123",
    invoice_number: "INV-2024-001",
    contact_id: "con_xyz789",
    status: "DRAFT",
    currency: "AED",
    amount: 1050,
    balance: 1050,
    tax_amount: 50,
    invoice_date: "2024-04-24",
    invoice_due_date: "2024-05-24",
    reference: null,
    external_id: null,
    created_ts: "2024-04-24T10:15:00Z",
    modified_ts: "2024-04-24T10:15:00Z"
  },
  type: import_pieces_framework17.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common18.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common18.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common18.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common18.pollingHelper.poll(polling, context);
  }
});
function flattenInvoice2(inv) {
  var _a, _b, _c, _d, _e, _f, _g;
  return {
    id: inv.id,
    invoice_number: inv.invoice_number,
    contact_id: (_a = inv.contact) != null ? _a : null,
    status: (_b = inv.status) != null ? _b : null,
    currency: inv.currency,
    amount: inv.amount,
    balance: (_c = inv.balance) != null ? _c : null,
    tax_amount: (_d = inv.tax_amount) != null ? _d : null,
    invoice_date: inv.invoice_date,
    invoice_due_date: inv.invoice_due_date,
    reference: (_e = inv.reference) != null ? _e : null,
    external_id: (_f = inv.external_id) != null ? _f : null,
    created_ts: inv.created_ts,
    modified_ts: (_g = inv.modified_ts) != null ? _g : null
  };
}

// ../ap/packages/pieces/community/wafeq/src/lib/triggers/new-bill.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common19.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let queryParams = { page_size: "100" };
    return lastFetchEpochMS > 0 && (queryParams.created_ts_after = new Date(
      lastFetchEpochMS
    ).toISOString()), (await wafeqApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common19.HttpMethod.GET,
      path: "/bills/",
      queryParams
    })).body.results.map((b) => ({
      epochMilliSeconds: new Date(b.created_ts).getTime(),
      data: flattenBill2(b)
    }));
  }
}, newBill = (0, import_pieces_framework18.createTrigger)({
  auth: wafeqAuth,
  name: "new_bill",
  displayName: "New Bill",
  description: "Fires when a new bill from a supplier is created. Useful for approval workflows \u2014 e.g. post the new bill to a Slack channel for a manager to authorize.",
  aiMetadata: {
    description: "Fires when a new supplier bill (accounts payable) is created in Wafeq (polling by creation time), representing a newly recorded amount owed to a vendor. Use to drive bill-approval or payment workflows."
  },
  props: {},
  sampleData: {
    id: "bill_abc123",
    bill_number: "SUP-2024-5001",
    contact_id: "con_supplier",
    status: "DRAFT",
    currency: "AED",
    amount: 525,
    balance: 525,
    tax_amount: 25,
    bill_date: "2024-04-24",
    bill_due_date: "2024-05-24",
    reference: null,
    order_number: null,
    external_id: null,
    created_ts: "2024-04-24T10:15:00Z",
    modified_ts: "2024-04-24T10:15:00Z"
  },
  type: import_pieces_framework18.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common19.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common19.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common19.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common19.pollingHelper.poll(polling2, context);
  }
});
function flattenBill2(b) {
  var _a, _b, _c, _d, _e, _f, _g, _h;
  return {
    id: b.id,
    bill_number: b.bill_number,
    contact_id: (_a = b.contact) != null ? _a : null,
    status: (_b = b.status) != null ? _b : null,
    currency: b.currency,
    amount: b.amount,
    balance: (_c = b.balance) != null ? _c : null,
    tax_amount: (_d = b.tax_amount) != null ? _d : null,
    bill_date: b.bill_date,
    bill_due_date: b.bill_due_date,
    reference: (_e = b.reference) != null ? _e : null,
    order_number: (_f = b.order_number) != null ? _f : null,
    external_id: (_g = b.external_id) != null ? _g : null,
    created_ts: b.created_ts,
    modified_ts: (_h = b.modified_ts) != null ? _h : null
  };
}

// ../ap/packages/pieces/community/wafeq/src/lib/triggers/new-contact.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common20 = require("@activepieces/pieces-common");
var props = {
  relationship: import_pieces_framework19.Property.StaticDropdown({
    displayName: "Type of Contact",
    description: "Only fire on new customers, only suppliers, or both.",
    required: !1,
    options: {
      options: [
        { label: "Any", value: "" },
        { label: "Customer only", value: "CUSTOMER" },
        { label: "Supplier only", value: "SUPPLIER" }
      ]
    }
  })
}, polling3 = {
  strategy: import_pieces_common20.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let queryParams = { page_size: "100" };
    return lastFetchEpochMS > 0 && (queryParams.created_ts_after = new Date(
      lastFetchEpochMS
    ).toISOString()), propsValue.relationship && (queryParams.relationship = propsValue.relationship), (await wafeqApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common20.HttpMethod.GET,
      path: "/contacts/",
      queryParams
    })).body.results.map((c) => ({
      epochMilliSeconds: new Date(c.created_ts).getTime(),
      data: flattenContact3(c)
    }));
  }
}, newContact = (0, import_pieces_framework19.createTrigger)({
  auth: wafeqAuth,
  name: "new_contact",
  displayName: "New Contact",
  description: "Fires every time a new customer or supplier is added to your Wafeq address book. Useful for syncing new customers to HubSpot, Mailchimp, or your CRM.",
  aiMetadata: {
    description: "Fires when a new contact is added to Wafeq (polling by creation time), representing a newly created customer or supplier. An optional relationship filter can restrict it to only new customers or only new suppliers. Use to sync new contacts into a CRM or mailing list."
  },
  props,
  sampleData: {
    id: "con_xyz789",
    name: "Acme Ltd.",
    email: "billing@acme.com",
    phone: "+971501234567",
    country: "AE",
    city: "Dubai",
    tax_registration_number: "100123456700003",
    relationship: "CUSTOMER",
    external_id: null,
    created_ts: "2024-04-24T10:15:00Z",
    modified_ts: "2024-04-24T10:15:00Z"
  },
  type: import_pieces_framework19.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common20.pollingHelper.test(polling3, context);
  },
  async onEnable(context) {
    await import_pieces_common20.pollingHelper.onEnable(polling3, context);
  },
  async onDisable(context) {
    await import_pieces_common20.pollingHelper.onDisable(polling3, context);
  },
  async run(context) {
    return await import_pieces_common20.pollingHelper.poll(polling3, context);
  }
});
function flattenContact3(c) {
  var _a, _b, _c, _d, _e, _f, _g;
  return {
    id: c.id,
    name: c.name,
    email: (_a = c.email) != null ? _a : null,
    phone: (_b = c.phone) != null ? _b : null,
    country: (_c = c.country) != null ? _c : null,
    city: (_d = c.city) != null ? _d : null,
    tax_registration_number: (_e = c.tax_registration_number) != null ? _e : null,
    relationship: Array.isArray(c.relationship) ? c.relationship.join(", ") : null,
    external_id: (_f = c.external_id) != null ? _f : null,
    created_ts: c.created_ts,
    modified_ts: (_g = c.modified_ts) != null ? _g : null
  };
}

// ../ap/packages/pieces/community/wafeq/src/lib/triggers/new-payment-received.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_common21 = require("@activepieces/pieces-common");
var polling4 = {
  strategy: import_pieces_common21.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let queryParams = { page_size: "100" };
    return lastFetchEpochMS > 0 && (queryParams.created_ts_after = new Date(
      lastFetchEpochMS
    ).toISOString()), (await wafeqApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common21.HttpMethod.GET,
      path: "/payments/",
      queryParams
    })).body.results.map((p) => ({
      epochMilliSeconds: new Date(p.created_ts).getTime(),
      data: flattenPayment2(p)
    }));
  }
}, newPaymentReceived = (0, import_pieces_framework20.createTrigger)({
  auth: wafeqAuth,
  name: "new_payment_received",
  displayName: "New Payment Received",
  description: "Fires every time a payment is recorded in Wafeq. Great for celebrating revenue in Slack, triggering commission calculations, or sending the customer a receipt.",
  aiMetadata: {
    description: "Fires when a new payment is recorded in Wafeq (polling by creation time), representing money received from a customer or paid to a supplier and applied to invoices, bills, or credit notes. Use to react to settlements \u2014 send receipts, notify a channel, or trigger commission logic."
  },
  props: {},
  sampleData: {
    id: "pay_abc123",
    amount: 1050,
    currency: "AED",
    date: "2024-04-24",
    contact_id: "con_xyz789",
    paid_through_account_id: "acc_bank_main",
    payment_fees: null,
    reference: "Stripe ch_xxx",
    external_id: null,
    invoice_payments_count: 1,
    bill_payments_count: 0,
    credit_note_payments_count: 0,
    created_ts: "2024-04-24T10:15:00Z",
    modified_ts: "2024-04-24T10:15:00Z"
  },
  type: import_pieces_framework20.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common21.pollingHelper.test(polling4, context);
  },
  async onEnable(context) {
    await import_pieces_common21.pollingHelper.onEnable(polling4, context);
  },
  async onDisable(context) {
    await import_pieces_common21.pollingHelper.onDisable(polling4, context);
  },
  async run(context) {
    return await import_pieces_common21.pollingHelper.poll(polling4, context);
  }
});
function flattenPayment2(p) {
  var _a, _b, _c, _d, _e;
  return {
    id: p.id,
    amount: p.amount,
    currency: p.currency,
    date: p.date,
    contact_id: (_a = p.contact) != null ? _a : null,
    paid_through_account_id: p.paid_through_account,
    payment_fees: (_b = p.payment_fees) != null ? _b : null,
    reference: (_c = p.reference) != null ? _c : null,
    external_id: (_d = p.external_id) != null ? _d : null,
    invoice_payments_count: Array.isArray(p.invoice_payments) ? p.invoice_payments.length : 0,
    bill_payments_count: Array.isArray(p.bill_payments) ? p.bill_payments.length : 0,
    credit_note_payments_count: Array.isArray(p.credit_note_payments) ? p.credit_note_payments.length : 0,
    created_ts: p.created_ts,
    modified_ts: (_e = p.modified_ts) != null ? _e : null
  };
}

// ../ap/packages/pieces/community/wafeq/src/index.ts
var wafeq = (0, import_pieces_framework21.createPiece)({
  displayName: "Wafeq",
  description: "Cloud accounting software for invoicing, bills, expenses, and tax reporting (ZATCA, UAE FTA).",
  auth: wafeqAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/wafeq.png",
  categories: [import_pieces_framework22.PieceCategory.ACCOUNTING],
  authors: ["sanket-a11y"],
  actions: [
    createInvoice,
    reportInvoiceToTaxAuthority,
    createContact,
    findContact,
    createBill,
    recordPayment,
    createSimplifiedInvoice,
    createCreditNote,
    createQuote,
    convertQuoteToInvoice,
    downloadInvoicePdf,
    createItem,
    listItems,
    listAccounts,
    (0, import_pieces_common22.createCustomApiCallAction)({
      baseUrl: () => WAFEQ_API_BASE_URL,
      auth: wafeqAuth,
      authMapping: async (auth) => ({
        Authorization: `Api-Key ${auth.secret_text}`
      })
    })
  ],
  triggers: [
    newInvoice,
    newBill,
    newContact,
    newPaymentReceived
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  wafeq,
  wafeqAuth
});
