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

// ../ap/packages/pieces/community/quaderno/src/index.ts
var index_exports = {};
__export(index_exports, {
  quaderno: () => quaderno
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quaderno/src/lib/actions/create-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quaderno/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function makeRequest(account_name, api_key, method, path, body) {
  try {
    let credentials = Buffer.from(`${api_key}:x`).toString("base64");
    return console.log(`https://${account_name}.quadernoapp.com/api${path}`), (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `https://${account_name}.quadernoapp.com/api${path}`,
      headers: {
        Authorization: `Basic ${credentials}`,
        "Content-Type": "application/json",
        api_version: "20241028"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/quaderno/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), quadernoAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "API Key",
  description: "Quaderno API Key",
  required: !0,
  props: {
    account_name: import_pieces_framework.Property.ShortText({
      displayName: "Account Name",
      description: "Your Quaderno account name https://<your_account_name>.quadernoapp.com/ (the subdomain part of your Quaderno URL) https://<your_account_name>.sandbox-quadernoapp.com/",
      required: !0
    }),
    api_key: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your Quaderno Private API Key",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await makeRequest(
        auth.account_name,
        auth.api_key,
        import_pieces_common2.HttpMethod.GET,
        "/tax_rates/calculate?to_country=US&to_postal_code=10128"
      ), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid Api Key"
      };
    }
  }
});

// ../ap/packages/pieces/community/quaderno/src/lib/actions/create-contact.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), createContact = (0, import_pieces_framework2.createAction)({
  auth: quadernoAuth,
  name: "createContact",
  displayName: "Create Contact",
  description: "Create a new contact in Quaderno (customer or vendor)",
  audience: "both",
  aiMetadata: {
    description: "Creates a new contact (customer or vendor) in Quaderno via POST, capturing name, address, tax ID/status, and contact details. Use when an agent needs to register a party before billing or recording expenses against them. Requires first name and a 2-letter ISO country code; the Kind prop selects person vs. company. Not idempotent \u2014 each call creates another contact even with identical input.",
    idempotent: !1
  },
  props: {
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "The contact's first name (required for person contacts)",
      required: !0
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "The contact's last name (optional)",
      required: !1
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "The contact's email address",
      required: !1
    }),
    fullName: import_pieces_framework2.Property.ShortText({
      displayName: "Full Name",
      description: "The contact's full name",
      required: !1
    }),
    kind: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Kind",
      description: "The type of contact",
      options: {
        options: [
          { label: "Person", value: "person" },
          { label: "Company", value: "company" }
        ]
      },
      required: !1
    }),
    country: import_pieces_framework2.Property.ShortText({
      displayName: "Country",
      description: "2-letter ISO country code (e.g., US, GB)",
      required: !0
    }),
    city: import_pieces_framework2.Property.ShortText({
      displayName: "City",
      description: "City/District/Suburb/Town/Village",
      required: !1
    }),
    postalCode: import_pieces_framework2.Property.ShortText({
      displayName: "Postal Code",
      description: "ZIP or postal code",
      required: !1
    }),
    region: import_pieces_framework2.Property.ShortText({
      displayName: "Region/State",
      description: "State/Province/Region",
      required: !1
    }),
    streetLine1: import_pieces_framework2.Property.ShortText({
      displayName: "Street Address Line 1",
      description: "Street address or PO Box",
      required: !1
    }),
    streetLine2: import_pieces_framework2.Property.ShortText({
      displayName: "Street Address Line 2",
      description: "Apartment/Suite/Unit/Building",
      required: !1
    }),
    taxId: import_pieces_framework2.Property.ShortText({
      displayName: "Tax ID",
      description: "Tax identification number (VAT, ABN, etc.)",
      required: !1
    }),
    taxStatus: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Tax Status",
      description: "Tax status of the contact",
      options: {
        options: [
          { label: "Taxable", value: "taxable" },
          { label: "Exempt", value: "exempt" },
          { label: "Reverse", value: "reverse" }
        ]
      },
      required: !1
    }),
    language: import_pieces_framework2.Property.ShortText({
      displayName: "Language",
      description: "2-letter ISO language code (e.g., EN, FR, ES)",
      required: !1
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone",
      description: "The contact's phone number",
      required: !1
    }),
    website: import_pieces_framework2.Property.ShortText({
      displayName: "Website",
      description: "The contact's website URL",
      required: !1
    }),
    discount: import_pieces_framework2.Property.Number({
      displayName: "Discount",
      description: "Default discount for this contact (as a percentage)",
      required: !1
    }),
    notes: import_pieces_framework2.Property.LongText({
      displayName: "Notes",
      description: "Internal notes about the contact",
      required: !1
    }),
    processorId: import_pieces_framework2.Property.ShortText({
      displayName: "Processor ID",
      description: "The ID assigned by an external payment processor (e.g., Stripe customer ID)",
      required: !1
    }),
    processor: import_pieces_framework2.Property.ShortText({
      displayName: "Processor",
      description: "The external platform where the contact was imported from (e.g., stripe)",
      required: !1
    })
  },
  async run(context) {
    let contactData = {
      first_name: context.propsValue.firstName,
      kind: context.propsValue.kind || "person",
      country: context.propsValue.country
    };
    return context.propsValue.lastName && (contactData.last_name = context.propsValue.lastName), context.propsValue.email && (contactData.email = context.propsValue.email), context.propsValue.fullName && (contactData.full_name = context.propsValue.fullName), context.propsValue.city && (contactData.city = context.propsValue.city), context.propsValue.postalCode && (contactData.postal_code = context.propsValue.postalCode), context.propsValue.region && (contactData.region = context.propsValue.region), context.propsValue.streetLine1 && (contactData.street_line_1 = context.propsValue.streetLine1), context.propsValue.streetLine2 && (contactData.street_line_2 = context.propsValue.streetLine2), context.propsValue.taxId && (contactData.tax_id = context.propsValue.taxId), context.propsValue.taxStatus && (contactData.tax_status = context.propsValue.taxStatus), context.propsValue.language && (contactData.language = context.propsValue.language), context.propsValue.phone && (contactData.phone_1 = context.propsValue.phone), context.propsValue.website && (contactData.web = context.propsValue.website), context.propsValue.discount !== void 0 && context.propsValue.discount !== null && (contactData.discount = context.propsValue.discount), context.propsValue.notes && (contactData.notes = context.propsValue.notes), context.propsValue.processorId && (contactData.processor_id = context.propsValue.processorId), context.propsValue.processor && (contactData.processor = context.propsValue.processor), await makeRequest(
      context.auth.props.account_name,
      context.auth.props.api_key,
      import_pieces_common3.HttpMethod.POST,
      "/contacts",
      contactData
    );
  }
});

// ../ap/packages/pieces/community/quaderno/src/lib/actions/create-expence.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), createExpense = (0, import_pieces_framework3.createAction)({
  auth: quadernoAuth,
  name: "createExpense",
  displayName: "Create Expense",
  description: "Create a new expense in Quaderno",
  audience: "both",
  aiMetadata: {
    description: "Records a new expense in Quaderno via POST, embedding the vendor contact and a single line item (description, quantity, unit cost) along with currency, issue date, and payment method. Use when an agent needs to log a purchase or bill. Requires issue date, currency, vendor first name/country, item details, payment method, and expense country. Not idempotent \u2014 each call creates another expense.",
    idempotent: !1
  },
  props: {
    issueDate: import_pieces_framework3.Property.ShortText({
      displayName: "Issue Date",
      description: "Date when the expense was issued (YYYY-MM-DD)",
      required: !0
    }),
    currency: import_pieces_framework3.Property.ShortText({
      displayName: "Currency",
      description: "3-letter ISO currency code (USD, EUR, GBP, etc.)",
      required: !0
    }),
    contactFirstName: import_pieces_framework3.Property.ShortText({
      displayName: "Vendor First Name",
      description: "The vendor's first name",
      required: !0
    }),
    contactLastName: import_pieces_framework3.Property.ShortText({
      displayName: "Vendor Last Name",
      description: "The vendor's last name (optional for person contacts)",
      required: !1
    }),
    contactEmail: import_pieces_framework3.Property.ShortText({
      displayName: "Vendor Email",
      description: "The vendor's email address",
      required: !1
    }),
    contactCountry: import_pieces_framework3.Property.ShortText({
      displayName: "Vendor Country",
      description: "2-letter ISO country code (e.g., US, GB)",
      required: !0
    }),
    itemDescription: import_pieces_framework3.Property.ShortText({
      displayName: "Item Description",
      description: "Description of the expense item",
      required: !0
    }),
    itemQuantity: import_pieces_framework3.Property.Number({
      displayName: "Item Quantity",
      description: "Quantity of the expense item",
      required: !0
    }),
    itemUnitCost: import_pieces_framework3.Property.ShortText({
      displayName: "Item Unit Cost",
      description: "Unit cost of the expense item",
      required: !0
    }),
    paymentMethod: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Payment Method",
      description: "How the expense was paid",
      options: {
        options: [
          { label: "Credit Card", value: "credit_card" },
          { label: "Cash", value: "cash" },
          { label: "Wire Transfer", value: "wire_transfer" },
          { label: "Direct Debit", value: "direct_debit" },
          { label: "Check", value: "check" },
          { label: "IOU", value: "iou" },
          { label: "PayPal", value: "paypal" },
          { label: "Other", value: "other" }
        ]
      },
      required: !0
    }),
    country: import_pieces_framework3.Property.ShortText({
      displayName: "Expense Country",
      description: "2-letter ISO country code for the expense location",
      required: !0
    }),
    poNumber: import_pieces_framework3.Property.ShortText({
      displayName: "Purchase Order Number",
      description: "Purchase order number (optional)",
      required: !1
    }),
    tagList: import_pieces_framework3.Property.ShortText({
      displayName: "Tags",
      description: "Tags for the expense, separated by commas",
      required: !1
    }),
    notes: import_pieces_framework3.Property.LongText({
      displayName: "Notes",
      description: "Additional notes about the expense",
      required: !1
    })
  },
  async run(context) {
    let contactData = {
      first_name: context.propsValue.contactFirstName,
      country: context.propsValue.contactCountry,
      kind: "person"
    };
    context.propsValue.contactLastName && (contactData.last_name = context.propsValue.contactLastName), context.propsValue.contactEmail && (contactData.email = context.propsValue.contactEmail);
    let itemsData = [
      {
        description: context.propsValue.itemDescription,
        quantity: context.propsValue.itemQuantity,
        unit_cost: context.propsValue.itemUnitCost
      }
    ], expenseData = {
      issue_date: context.propsValue.issueDate,
      currency: context.propsValue.currency,
      contact: contactData,
      items: itemsData,
      payment_method: context.propsValue.paymentMethod,
      country: context.propsValue.country
    };
    return context.propsValue.poNumber && (expenseData.po_number = context.propsValue.poNumber), context.propsValue.tagList && (expenseData.tag_list = context.propsValue.tagList.split(",").map((tag) => tag.trim())), context.propsValue.notes && (expenseData.notes = context.propsValue.notes), await makeRequest(
      context.auth.props.account_name,
      context.auth.props.api_key,
      import_pieces_common4.HttpMethod.POST,
      "/expenses",
      expenseData
    );
  }
});

// ../ap/packages/pieces/community/quaderno/src/lib/actions/create-invoice.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), createInvoice = (0, import_pieces_framework4.createAction)({
  auth: quadernoAuth,
  name: "createInvoice",
  displayName: "Create Invoice",
  description: "Create a new invoice in Quaderno",
  audience: "both",
  aiMetadata: {
    description: "Issues a new invoice in Quaderno via POST, embedding the customer contact and a single line item (description, quantity, unit cost) with currency and issue date, plus optional invoice number, due date, and tags. Use when an agent needs to bill a customer. Requires issue date, currency, customer first name, and item details. Not idempotent \u2014 each call creates another invoice even if the invoice number repeats.",
    idempotent: !1
  },
  props: {
    issueDate: import_pieces_framework4.Property.ShortText({
      displayName: "Issue Date",
      description: "Date when the invoice was issued (YYYY-MM-DD)",
      required: !0
    }),
    currency: import_pieces_framework4.Property.ShortText({
      displayName: "Currency",
      description: "3-letter ISO currency code (USD, EUR, GBP, etc.)",
      required: !0
    }),
    invoiceNumber: import_pieces_framework4.Property.ShortText({
      displayName: "Invoice Number",
      description: "A unique, sequential code that identifies the invoice",
      required: !1
    }),
    dueDate: import_pieces_framework4.Property.ShortText({
      displayName: "Due Date",
      description: "Date on which payment for this invoice is due (YYYY-MM-DD)",
      required: !1
    }),
    customerFirstName: import_pieces_framework4.Property.ShortText({
      displayName: "Customer First Name",
      description: "The customer's first name",
      required: !0
    }),
    customerLastName: import_pieces_framework4.Property.ShortText({
      displayName: "Customer Last Name",
      description: "The customer's last name",
      required: !1
    }),
    customerEmail: import_pieces_framework4.Property.ShortText({
      displayName: "Customer Email",
      description: "The customer's email address",
      required: !1
    }),
    customerCountry: import_pieces_framework4.Property.ShortText({
      displayName: "Customer Country",
      description: "2-letter ISO country code (e.g., US, GB)",
      required: !1
    }),
    itemDescription: import_pieces_framework4.Property.ShortText({
      displayName: "Item Description",
      description: "Description of the invoice line item",
      required: !0
    }),
    itemQuantity: import_pieces_framework4.Property.Number({
      displayName: "Item Quantity",
      description: "Quantity of the invoice item",
      required: !0
    }),
    itemUnitCost: import_pieces_framework4.Property.ShortText({
      displayName: "Item Unit Cost",
      description: "Unit cost of the invoice item",
      required: !0
    }),
    poNumber: import_pieces_framework4.Property.ShortText({
      displayName: "PO Number",
      description: "Purchase order number",
      required: !1
    }),
    tagList: import_pieces_framework4.Property.ShortText({
      displayName: "Tags",
      description: "Tags attached to the invoice (comma-separated)",
      required: !1
    }),
    notes: import_pieces_framework4.Property.ShortText({
      displayName: "Notes",
      description: "Extra notes about the invoice",
      required: !1
    })
  },
  async run(context) {
    let customerData = {
      first_name: context.propsValue.customerFirstName,
      kind: "person"
    };
    context.propsValue.customerLastName && (customerData.last_name = context.propsValue.customerLastName), context.propsValue.customerEmail && (customerData.email = context.propsValue.customerEmail), context.propsValue.customerCountry && (customerData.country = context.propsValue.customerCountry);
    let itemsData = [
      {
        description: context.propsValue.itemDescription,
        quantity: context.propsValue.itemQuantity,
        unit_cost: context.propsValue.itemUnitCost
      }
    ], invoiceData = {
      issue_date: context.propsValue.issueDate,
      currency: context.propsValue.currency,
      contact: customerData,
      items: itemsData
    };
    return context.propsValue.invoiceNumber && (invoiceData.number = context.propsValue.invoiceNumber), context.propsValue.dueDate && (invoiceData.due_date = context.propsValue.dueDate), context.propsValue.poNumber && (invoiceData.po_number = context.propsValue.poNumber), context.propsValue.tagList && (invoiceData.tag_list = context.propsValue.tagList.split(",").map((tag) => tag.trim())), context.propsValue.notes && (invoiceData.notes = context.propsValue.notes), await makeRequest(
      context.auth.props.account_name,
      context.auth.props.api_key,
      import_pieces_common5.HttpMethod.POST,
      "/invoices",
      invoiceData
    );
  }
});

// ../ap/packages/pieces/community/quaderno/src/lib/actions/find-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), findContact = (0, import_pieces_framework5.createAction)({
  auth: quadernoAuth,
  name: "findContact",
  displayName: "Find Contact",
  description: "Search and list contacts from Quaderno",
  audience: "both",
  aiMetadata: {
    description: "Looks up contacts in Quaderno via GET. With a search query it filters contacts by name, email, or tax ID; left empty it lists all contacts (capped by the results limit). Use to resolve a contact before referencing it or to check whether one already exists. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    searchQuery: import_pieces_framework5.Property.ShortText({
      displayName: "Search Query",
      description: "Search for contacts by name, email, or tax ID (optional)",
      required: !1
    }),
    processorId: import_pieces_framework5.Property.ShortText({
      displayName: "Processor ID",
      description: "Filter by processor ID (optional)",
      required: !1
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Results Limit",
      description: "Maximum number of results to return (default: 25, max: 100)",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let params = new URLSearchParams();
    context.propsValue.searchQuery && params.append("q", context.propsValue.searchQuery), context.propsValue.processorId && params.append("processor_id", context.propsValue.processorId), context.propsValue.limit && params.append("limit", context.propsValue.limit.toString());
    let queryString = params.toString(), endpoint = queryString ? `/contacts?${queryString}` : "/contacts";
    return await makeRequest(
      context.auth.props.account_name,
      context.auth.props.api_key,
      import_pieces_common6.HttpMethod.GET,
      endpoint
    );
  }
});

// ../ap/packages/pieces/community/quaderno/src/index.ts
var quaderno = (0, import_pieces_framework6.createPiece)({
  displayName: "Quaderno",
  auth: quadernoAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/quaderno.png",
  categories: [import_pieces_framework7.PieceCategory.COMMERCE],
  description: "Quaderno is a tax software that handles sales tax, VAT, and GST for your online business. Automatically calculates tax, sends receipts and invoices, and provides instant multi-channel tax reports for your sales around the world.",
  authors: ["sanket-a11y"],
  actions: [
    createContact,
    findContact,
    createExpense,
    createInvoice,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: quadernoAuth,
      baseUrl: (auth) => `https://${auth == null ? void 0 : auth.props.account_name}.quadernoapp.com/api`,
      authMapping: async (auth) => ({
        Authorization: `Basic ${Buffer.from(`${auth.props.api_key}:x`).toString(
          "base64"
        )}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  quaderno
});
