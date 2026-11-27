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

// ../ap/packages/pieces/community/bokio/src/index.ts
var index_exports = {};
__export(index_exports, {
  bokio: () => bokio
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bokio/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bokio/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.bokio.se/v1";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/bokio/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), bokioAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "Bokio API Key",
  description: "Bokio API Key is required to authenticate requests to Bokio",
  required: !0,
  props: {
    companyId: import_pieces_framework.Property.ShortText({
      displayName: "Company ID",
      description: "Enter your Bokio Company ID ",
      required: !0
    }),
    api_key: import_pieces_framework.Property.ShortText({
      displayName: "API Key",
      description: "Enter your Bokio API Key",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    if (auth)
      try {
        return await makeRequest(
          auth.api_key,
          import_pieces_common2.HttpMethod.GET,
          `/companies/${auth.companyId}/journal-entries`
        ), {
          valid: !0
        };
      } catch (e) {
        return {
          valid: !1,
          error: "Invalid Api Key"
        };
      }
    return {
      valid: !1,
      error: "Invalid Api Key"
    };
  }
});

// ../ap/packages/pieces/community/bokio/src/lib/actions/create-customer.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), createCustomer = (0, import_pieces_framework2.createAction)({
  auth: bokioAuth,
  name: "createCustomer",
  classification: "WRITE",
  displayName: "Create Customer",
  description: "Creates a new customer in Bokio",
  audience: "both",
  aiMetadata: { description: "Creates a new customer record in a Bokio accounting company. Use to register a person or company before invoicing them; only customer name and type (individual/company) are required, with optional VAT/org number, contact, and address details. Not idempotent \u2014 each call creates a separate customer.", idempotent: !1 },
  props: {
    customerName: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Name",
      description: "Name of the customer",
      required: !0
    }),
    type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Customer Type",
      description: "Type of the customer",
      required: !0,
      options: {
        options: [
          { label: "Individual", value: "private" },
          { label: "Company", value: "company" }
        ]
      }
    }),
    vatNumber: import_pieces_framework2.Property.ShortText({
      displayName: "VAT Number",
      description: "Customer VAT number",
      required: !1
    }),
    orgNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Organization Number",
      description: "Customer organization number",
      required: !1
    }),
    paymentTerms: import_pieces_framework2.Property.ShortText({
      displayName: "Payment Terms",
      description: "Payment terms for the customer. Payment terms for the invoices. Payment terms can both take free text or days, in digits, until the payment is due.",
      required: !1
    }),
    customerNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Number",
      description: "Customer number (optional)",
      required: !1
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "Customer email address",
      required: !1
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone",
      description: "Customer phone number",
      required: !1
    }),
    lin1: import_pieces_framework2.Property.ShortText({
      displayName: "Address line 1",
      description: "Customer address line 1",
      required: !1
    }),
    line2: import_pieces_framework2.Property.ShortText({
      displayName: "Address line 2",
      description: "Customer address line 2",
      required: !1
    }),
    zipCode: import_pieces_framework2.Property.ShortText({
      displayName: "Zip Code",
      description: "Customer zip code",
      required: !1
    }),
    city: import_pieces_framework2.Property.ShortText({
      displayName: "City",
      description: "Customer city",
      required: !1
    }),
    country: import_pieces_framework2.Property.ShortText({
      displayName: "Country",
      description: "Customer country (ISO 3166-1 alpha-2 country code)",
      required: !1
    })
  },
  async run(context) {
    let {
      customerName,
      type,
      vatNumber,
      orgNumber,
      paymentTerms,
      customerNumber,
      email,
      phone,
      lin1,
      line2,
      zipCode,
      city,
      country
    } = context.propsValue, { api_key, companyId } = context.auth.props, body = {
      name: customerName,
      type
    };
    vatNumber && (body.vat_number = vatNumber), orgNumber && (body.org_number = orgNumber), customerNumber && (body.customer_number = customerNumber), paymentTerms && (body.payment_terms = paymentTerms);
    let contactsDetails = {};
    email && (contactsDetails.email = email), phone && (contactsDetails.phone = phone), Object.keys(contactsDetails).length > 0 && (body.contactsDetails = contactsDetails);
    let addressDetails = {};
    return lin1 && (addressDetails.line_1 = lin1), line2 && (addressDetails.line_2 = line2), zipCode && (addressDetails.zip_code = zipCode), city && (addressDetails.city = city), country && (addressDetails.country = country), Object.keys(addressDetails).length > 0 && (body.address = addressDetails), await makeRequest(
      api_key,
      import_pieces_common3.HttpMethod.POST,
      `/companies/${companyId}/customers`,
      body
    );
  }
});

// ../ap/packages/pieces/community/bokio/src/lib/actions/create-invoice.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bokio/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), invoiceIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: bokioAuth,
  displayName: "Invoice",
  description: "Select the invoice",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(
          auth.props.api_key,
          import_pieces_common4.HttpMethod.GET,
          `/companies/${auth.props.companyId}/invoices?limit=100`
        )).items.map((invoice) => ({
          label: `#${invoice.invoiceNumber} - ${invoice.customerRef.customerName}`,
          value: invoice.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: []
      };
    }
  }
}), unitTypeDropdown = import_pieces_framework3.Property.StaticDropdown({
  displayName: "Unit Type",
  description: "Unit type for the item",
  required: !1,
  options: {
    options: [
      { label: "Unspecified", value: "unspecified" },
      { label: "Piece", value: "piece" },
      { label: "Hour", value: "hour" },
      { label: "Day", value: "day" },
      { label: "Month", value: "month" },
      { label: "Kilogram", value: "kilogram" },
      { label: "Gram", value: "gram" },
      { label: "Liter", value: "liter" },
      { label: "Meter", value: "meter" },
      { label: "Centimeter", value: "centimeter" },
      { label: "Millimeter", value: "millimeter" },
      { label: "Meter Squared", value: "meterSquared" },
      { label: "Meter Cubic", value: "meterCubic" },
      { label: "Mile", value: "mile" },
      { label: "Kilometer", value: "kilometer" },
      { label: "Gigabyte", value: "gigabyte" },
      { label: "Hectar", value: "hectar" },
      { label: "Words", value: "words" },
      { label: "Year", value: "year" },
      { label: "Week", value: "week" },
      { label: "Minute", value: "minute" },
      { label: "Megabyte", value: "megabyte" },
      { label: "Ton", value: "ton" }
    ]
  }
}), lineItemsProps = import_pieces_framework3.Property.DynamicProperties({
  auth: bokioAuth,
  displayName: "Item Details",
  description: "Configure item details based on item type",
  required: !1,
  refreshers: ["itemType"],
  props: async ({ itemType }) => {
    let properties = {
      description: import_pieces_framework3.Property.ShortText({
        displayName: "Description",
        description: "Description of the line item",
        required: !0
      })
    };
    return itemType === "salesItem" && (properties.itemRefId = import_pieces_framework3.Property.ShortText({
      displayName: "Item Reference ID",
      description: "UUID of the item reference",
      required: !1
    }), properties.productType = import_pieces_framework3.Property.StaticDropdown({
      displayName: "Product Type",
      description: "Type of product",
      required: !0,
      options: {
        options: [
          { label: "Goods", value: "goods" },
          { label: "Service", value: "service" }
        ]
      }
    }), properties.unitType = unitTypeDropdown, properties.quantity = import_pieces_framework3.Property.Number({
      displayName: "Quantity",
      description: "Quantity of the item",
      required: !0,
      defaultValue: 1
    }), properties.unitPrice = import_pieces_framework3.Property.Number({
      displayName: "Unit Price",
      description: "Price per unit",
      required: !0
    }), properties.taxRate = import_pieces_framework3.Property.Number({
      displayName: "Tax Rate",
      description: "Tax rate in percentage (e.g., 25 for 25%)",
      required: !1
    })), properties;
  }
});

// ../ap/packages/pieces/community/bokio/src/lib/actions/create-invoice.ts
var createInvoice = (0, import_pieces_framework4.createAction)({
  auth: bokioAuth,
  name: "createInvoice",
  classification: "WRITE",
  displayName: "Create Invoice",
  description: "Creates a new draft invoice in Bokio",
  audience: "both",
  aiMetadata: { description: "Creates a new draft invoice in a Bokio company with a single line item. Use to start a new customer invoice; invoice date, due date, and item type are required, and the customer is linked by passing the customer UUID. Not idempotent \u2014 each call creates a separate draft invoice.", idempotent: !1 },
  props: {
    invoiceType: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Invoice Type",
      description: "Type of invoice",
      required: !1,
      options: {
        options: [
          { label: "Invoice", value: "invoice" },
          { label: "Cash Invoice", value: "cashInvoice" }
        ]
      }
    }),
    customerRef: import_pieces_framework4.Property.ShortText({
      displayName: "Customer ID",
      description: "UUID of the customer",
      required: !1
    }),
    invoiceDate: import_pieces_framework4.Property.ShortText({
      displayName: "Invoice Date",
      description: "Invoice date (YYYY-MM-DD)",
      required: !0
    }),
    dueDate: import_pieces_framework4.Property.ShortText({
      displayName: "Due Date",
      description: "Due date (YYYY-MM-DD)",
      required: !0
    }),
    orderNumberReference: import_pieces_framework4.Property.ShortText({
      displayName: "Order Number Reference",
      description: "The order number associated with the invoice",
      required: !1
    }),
    currency: import_pieces_framework4.Property.ShortText({
      displayName: "Currency",
      description: "ISO 4217 currency code (e.g., SEK, EUR, USD)",
      required: !1
    }),
    currencyRate: import_pieces_framework4.Property.Number({
      displayName: "Currency Rate",
      description: "Currency rate (defaults to 1)",
      required: !1
    }),
    itemType: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Item Type",
      description: "Type of the line item",
      required: !0,
      options: {
        options: [
          { label: "Sales Item", value: "salesItem" },
          { label: "Description Only Item", value: "descriptionOnlyItem" }
        ]
      }
    }),
    dynamicProps: lineItemsProps
  },
  async run(context) {
    let {
      invoiceType,
      customerRef,
      invoiceDate,
      dueDate,
      orderNumberReference,
      currency,
      currencyRate,
      itemType,
      dynamicProps
    } = context.propsValue, { api_key, companyId } = context.auth.props, body = {
      type: invoiceType || "invoice",
      invoiceDate,
      dueDate
    };
    customerRef && (body.customerRef = {
      id: customerRef
    }), orderNumberReference && (body.orderNumberReference = orderNumberReference), currency && (body.currency = currency), currencyRate != null && (body.currencyRate = currencyRate);
    let lineItems = {};
    return itemType === "salesItem" && dynamicProps && (lineItems.itemType = itemType, body.description = dynamicProps == null ? void 0 : dynamicProps.description, lineItems.quantity = dynamicProps.quantity, lineItems.unitPrice = dynamicProps.unitPrice, dynamicProps.itemRefId && (lineItems.itemRef = {
      id: dynamicProps.itemRefId
    }), dynamicProps.productType && (lineItems.productType = dynamicProps.productType), dynamicProps.unitType && (lineItems.unitType = dynamicProps.unitType), dynamicProps.taxRate !== void 0 && dynamicProps.taxRate !== null && (lineItems.taxRate = dynamicProps.taxRate)), body.lineItems = [lineItems], await makeRequest(
      api_key,
      import_pieces_common5.HttpMethod.POST,
      `/companies/${companyId}/invoices`,
      body
    );
  }
});

// ../ap/packages/pieces/community/bokio/src/lib/actions/add-line-item-to-invoice.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var addLineItemToInvoice = (0, import_pieces_framework5.createAction)({
  auth: bokioAuth,
  name: "addLineItemToInvoice",
  classification: "WRITE",
  displayName: "Add line item to invoice",
  description: "Adds a line item to an existing draft invoice",
  audience: "both",
  aiMetadata: { description: "Appends a single line item to an existing draft invoice in Bokio, identified by invoice ID. Use to add a product, service, or description-only row to an invoice that already exists; pricing fields apply only when item type is a sales item. Not idempotent \u2014 each call appends another line item.", idempotent: !1 },
  props: {
    invoiceId: invoiceIdDropdown,
    itemType: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Item Type",
      description: "Type of the item",
      required: !0,
      options: {
        options: [
          { label: "Sales Item", value: "salesItem" },
          { label: "Description Only Item", value: "descriptionOnlyItem" }
        ]
      }
    }),
    dynamicProps: lineItemsProps
  },
  async run(context) {
    let { invoiceId, itemType, dynamicProps } = context.propsValue, { api_key, companyId } = context.auth.props, body = {
      itemType,
      description: dynamicProps == null ? void 0 : dynamicProps.description
    };
    return itemType === "salesItem" && dynamicProps && (body.quantity = dynamicProps.quantity, body.unitPrice = dynamicProps.unitPrice, dynamicProps.itemRefId && (body.itemRef = {
      id: dynamicProps.itemRefId
    }), dynamicProps.productType && (body.productType = dynamicProps.productType), dynamicProps.unitType && (body.unitType = dynamicProps.unitType), dynamicProps.taxRate !== void 0 && dynamicProps.taxRate !== null && (body.taxRate = dynamicProps.taxRate)), await makeRequest(
      api_key,
      import_pieces_common6.HttpMethod.POST,
      `/companies/${companyId}/invoices/${invoiceId}/line-items`,
      body
    );
  }
});

// ../ap/packages/pieces/community/bokio/src/lib/actions/create-an-item.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common");
var createAnItem = (0, import_pieces_framework6.createAction)({
  auth: bokioAuth,
  name: "createAnItem",
  classification: "WRITE",
  displayName: "Create an item",
  description: "Creates a new item in Bokio",
  audience: "both",
  aiMetadata: { description: "Creates a new reusable item (a sales item with pricing, or a text item) in a Bokio company catalog. Use to define a product or service that can later be referenced on invoices; item type is required. Not idempotent \u2014 each call creates a separate item.", idempotent: !1 },
  props: {
    itemType: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Item Type",
      description: "Type of the item",
      required: !0,
      options: {
        options: [
          { label: "Sales Item", value: "salesItem" },
          { label: "Text", value: "text" }
        ]
      }
    }),
    dynamicProps: lineItemsProps
  },
  async run(context) {
    let { itemType, dynamicProps } = context.propsValue, { api_key, companyId } = context.auth.props, body = {
      itemType,
      description: dynamicProps == null ? void 0 : dynamicProps.description
    };
    return dynamicProps != null && dynamicProps.productType && (body.productType = dynamicProps.productType), dynamicProps != null && dynamicProps.unitType && (body.unitType = dynamicProps.unitType), (dynamicProps == null ? void 0 : dynamicProps.unitPrice) !== void 0 && (dynamicProps == null ? void 0 : dynamicProps.unitPrice) !== null && (body.unitPrice = dynamicProps.unitPrice), (dynamicProps == null ? void 0 : dynamicProps.taxRate) !== void 0 && (dynamicProps == null ? void 0 : dynamicProps.taxRate) !== null && (body.taxRate = dynamicProps.taxRate), await makeRequest(
      api_key,
      import_pieces_common7.HttpMethod.POST,
      `/companies/${companyId}/items`,
      body
    );
  }
});

// ../ap/packages/pieces/community/bokio/src/lib/actions/create-a-journal-entry.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), createAJournalEntry = (0, import_pieces_framework7.createAction)({
  auth: bokioAuth,
  name: "createAJournalEntry",
  classification: "WRITE",
  displayName: "Create a journal entry",
  description: "Creates a new journal entry in Bokio",
  audience: "both",
  aiMetadata: { description: "Records a new bookkeeping journal entry in a Bokio company, posting debit/credit amounts against account numbers. Use for manual double-entry transactions; the entry date is required and items carry the per-account debit/credit lines. Not idempotent \u2014 each call posts a separate journal entry.", idempotent: !1 },
  props: {
    title: import_pieces_framework7.Property.ShortText({
      displayName: "Title",
      description: "Title of the journal entry",
      required: !1
    }),
    date: import_pieces_framework7.Property.ShortText({
      displayName: "Date",
      description: "Journal entry date (YYYY-MM-DD)",
      required: !0
    }),
    items: import_pieces_framework7.Property.Array({
      displayName: "Items (JSON)",
      description: "Journal entry items as JSON array. Each item should have: account (account number), debit (optional), credit (optional)",
      required: !1,
      properties: {
        account: import_pieces_framework7.Property.Number({
          displayName: "Account Number",
          description: "Account number for the journal entry item",
          required: !1
        }),
        debit: import_pieces_framework7.Property.Number({
          displayName: "Debit Amount",
          description: "Debit amount for the journal entry item",
          required: !1
        }),
        credit: import_pieces_framework7.Property.Number({
          displayName: "Credit Amount",
          description: "Credit amount for the journal entry item",
          required: !1
        })
      }
    })
  },
  async run(context) {
    let { title, date, items } = context.propsValue, { api_key, companyId } = context.auth.props, body = {
      date
    };
    return title && (body.title = title), items && (body.items = items), await makeRequest(
      api_key,
      import_pieces_common8.HttpMethod.POST,
      `/companies/${companyId}/journal-entries`,
      body
    );
  }
});

// ../ap/packages/pieces/community/bokio/src/lib/actions/update-an-invoice.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common");
var updateAnInvoice = (0, import_pieces_framework8.createAction)({
  auth: bokioAuth,
  name: "updateAnInvoice",
  classification: "WRITE",
  displayName: "Update an invoice",
  description: "Updates an existing draft invoice in Bokio",
  audience: "both",
  aiMetadata: { description: "Updates an existing draft invoice in Bokio, identified by invoice ID, replacing fields such as type, customer, dates, currency, and the full line-items list (passed as a JSON array). Use to modify an invoice that already exists rather than create one. Idempotent \u2014 repeating the same update yields the same invoice state.", idempotent: !0 },
  props: {
    invoiceId: invoiceIdDropdown,
    invoiceType: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Invoice Type",
      description: "Type of invoice",
      required: !1,
      options: {
        options: [
          { label: "Invoice", value: "invoice" },
          { label: "Cash Invoice", value: "cashInvoice" }
        ]
      }
    }),
    customerRef: import_pieces_framework8.Property.ShortText({
      displayName: "Customer ID",
      description: "UUID of the customer",
      required: !1
    }),
    invoiceDate: import_pieces_framework8.Property.ShortText({
      displayName: "Invoice Date",
      description: "Invoice date (YYYY-MM-DD)",
      required: !0
    }),
    dueDate: import_pieces_framework8.Property.ShortText({
      displayName: "Due Date",
      description: "Due date (YYYY-MM-DD)",
      required: !0
    }),
    orderNumberReference: import_pieces_framework8.Property.ShortText({
      displayName: "Order Number Reference",
      description: "The order number associated with the invoice",
      required: !1
    }),
    currency: import_pieces_framework8.Property.ShortText({
      displayName: "Currency",
      description: "ISO 4217 currency code (e.g., SEK, EUR, USD)",
      required: !1
    }),
    currencyRate: import_pieces_framework8.Property.Number({
      displayName: "Currency Rate",
      description: "Currency rate",
      required: !1
    }),
    lineItemsJson: import_pieces_framework8.Property.LongText({
      displayName: "Line Items (JSON)",
      description: "Line items as JSON array. Each item should have: itemRef (object with id), description, quantity, unitPrice, unit, itemType (PRODUCT or SERVICE), vat",
      required: !1
    })
  },
  async run(context) {
    let {
      invoiceId,
      invoiceType,
      customerRef,
      invoiceDate,
      dueDate,
      orderNumberReference,
      currency,
      currencyRate,
      lineItemsJson
    } = context.propsValue, { api_key, companyId } = context.auth.props, body = {};
    if (invoiceType && (body.type = invoiceType), customerRef && (body.customerRef = {
      id: customerRef
    }), invoiceDate && (body.invoiceDate = invoiceDate), dueDate && (body.dueDate = dueDate), orderNumberReference && (body.orderNumberReference = orderNumberReference), currency && (body.currency = currency), currencyRate != null && (body.currencyRate = currencyRate), lineItemsJson)
      try {
        body.lineItems = JSON.parse(lineItemsJson);
      } catch (e) {
        throw new Error("Invalid JSON format for line items");
      }
    return await makeRequest(
      api_key,
      import_pieces_common9.HttpMethod.PUT,
      `/companies/${companyId}/invoices/${invoiceId}`,
      body
    );
  }
});

// ../ap/packages/pieces/community/bokio/src/lib/actions/find-customer.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common"), findCustomer = (0, import_pieces_framework9.createAction)({
  auth: bokioAuth,
  name: "findCustomer",
  classification: "SEARCH",
  displayName: "Find Customer",
  description: "Find customers in Bokio by filtering and pagination",
  audience: "both",
  aiMetadata: { description: "Lists customers in a Bokio company with pagination, optionally filtered by name, type, VAT number, or organization number. Leaving the filter empty returns all customers; supplying a filter type and value narrows to matching records. Use to look up a customer or their ID. Idempotent read-only lookup.", idempotent: !0 },
  props: {
    filterType: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Filter Type",
      description: "Type of filter to use",
      required: !1,
      options: {
        options: [
          { label: "Name", value: "name" },
          { label: "Type", value: "type" },
          { label: "VAT Number", value: "vatNumber" },
          { label: "Organization Number", value: "orgNumber" }
        ]
      }
    }),
    filterValue: import_pieces_framework9.Property.ShortText({
      displayName: "Filter Value",
      description: "Value to filter by",
      required: !1
    }),
    page: import_pieces_framework9.Property.Number({
      displayName: "Page",
      description: "Page number (defaults to 1)",
      required: !1
    }),
    pageSize: import_pieces_framework9.Property.Number({
      displayName: "Page Size",
      description: "Number of items per page (max 100, defaults to 25)",
      required: !1
    })
  },
  async run(context) {
    let { filterType, filterValue, page, pageSize } = context.propsValue, { api_key, companyId } = context.auth.props, query = "";
    filterType && filterValue && (query = `${filterType}=='${filterValue}'`);
    let params = new URLSearchParams();
    page != null ? params.append("page", String(page)) : params.append("page", "1"), pageSize != null ? params.append("pageSize", String(pageSize)) : params.append("pageSize", "25"), query && params.append("query", query);
    let url = `/companies/${companyId}/customers?${params.toString()}`;
    return await makeRequest(api_key, import_pieces_common10.HttpMethod.GET, url);
  }
});

// ../ap/packages/pieces/community/bokio/src/lib/actions/get-draft-invoice-by-customer-name.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common");
var getDraftInvoiceByCustomerName = (0, import_pieces_framework10.createAction)({
  auth: bokioAuth,
  name: "getDraftInvoiceByCustomerName",
  classification: "SEARCH",
  displayName: "Get Draft Invoice by Customer Name",
  description: "Get all draft invoices for a customer by customer name",
  audience: "both",
  aiMetadata: { description: "Looks up a Bokio customer by exact name and returns their draft invoices with pagination. Use when you have a customer name (not an ID) and want their pending draft invoices; returns an empty result if no customer matches. Idempotent read-only lookup.", idempotent: !0 },
  props: {
    customerName: import_pieces_framework10.Property.ShortText({
      displayName: "Customer Name",
      description: "Name of the customer to find draft invoices for",
      required: !0
    }),
    page: import_pieces_framework10.Property.Number({
      displayName: "Page",
      description: "Page number (defaults to 1)",
      required: !1
    }),
    pageSize: import_pieces_framework10.Property.Number({
      displayName: "Page Size",
      description: "Number of items per page (max 100, defaults to 25)",
      required: !1
    })
  },
  async run(context) {
    let { customerName, page, pageSize } = context.propsValue, { api_key, companyId } = context.auth.props, customerSearchParams = new URLSearchParams();
    customerSearchParams.append("page", "1"), customerSearchParams.append("pageSize", "100"), customerSearchParams.append("query", `name=='${customerName}'`);
    let customerUrl = `/companies/${companyId}/customers?${customerSearchParams.toString()}`, customerResponse = await makeRequest(api_key, import_pieces_common11.HttpMethod.GET, customerUrl);
    if (!customerResponse.items || customerResponse.items.length === 0)
      return {
        message: `No customer found with name: ${customerName}`,
        items: []
      };
    let customerId = customerResponse.items[0].id, invoiceParams = new URLSearchParams();
    page != null ? invoiceParams.append("page", String(page)) : invoiceParams.append("page", "1"), pageSize != null ? invoiceParams.append("pageSize", String(pageSize)) : invoiceParams.append("pageSize", "25"), invoiceParams.append("query", `status==draft&&customerRef==${customerId}`);
    let invoiceUrl = `/companies/${companyId}/invoices?${invoiceParams.toString()}`, invoiceResponse = await makeRequest(api_key, import_pieces_common11.HttpMethod.GET, invoiceUrl);
    return __spreadValues({
      customerName: customerResponse.items[0].name,
      customerId
    }, invoiceResponse);
  }
});

// ../ap/packages/pieces/community/bokio/src/lib/actions/get-an-invoice.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common"), getAnInvoice = (0, import_pieces_framework11.createAction)({
  auth: bokioAuth,
  name: "getAnInvoice",
  classification: "READ",
  displayName: "Get an invoice",
  description: "Retrieve a specific invoice by its ID",
  audience: "both",
  aiMetadata: { description: "Retrieves a single invoice from a Bokio company by its invoice ID. Use when you already have the invoice ID and need its full details. Idempotent read-only lookup.", idempotent: !0 },
  props: {
    invoiceId: import_pieces_framework11.Property.ShortText({
      displayName: "Invoice ID",
      description: "The ID of the invoice to retrieve",
      required: !0
    })
  },
  async run(context) {
    let { invoiceId } = context.propsValue, { api_key, companyId } = context.auth.props;
    return await makeRequest(
      api_key,
      import_pieces_common12.HttpMethod.GET,
      `/companies/${companyId}/invoices/${invoiceId}`
    );
  }
});

// ../ap/packages/pieces/community/bokio/src/lib/actions/get-a-journal-entry.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common13 = require("@activepieces/pieces-common"), getAJournalEntry = (0, import_pieces_framework12.createAction)({
  auth: bokioAuth,
  name: "getAJournalEntry",
  classification: "READ",
  displayName: "Get a journal entry",
  description: "Retrieve a specific journal entry by its ID",
  audience: "both",
  aiMetadata: { description: "Retrieves a single journal entry from a Bokio company by its UUID. Use when you already have the journal entry ID and need its full bookkeeping details. Idempotent read-only lookup.", idempotent: !0 },
  props: {
    journalEntryId: import_pieces_framework12.Property.ShortText({
      displayName: "Journal Entry ID",
      description: "UUID of the journal entry",
      required: !0
    })
  },
  async run(context) {
    let { journalEntryId } = context.propsValue, { api_key, companyId } = context.auth.props;
    return await makeRequest(
      api_key,
      import_pieces_common13.HttpMethod.GET,
      `/companies/${companyId}/journal-entries/${journalEntryId}`
    );
  }
});

// ../ap/packages/pieces/community/bokio/src/index.ts
var import_pieces_common14 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework"), bokio = (0, import_pieces_framework13.createPiece)({
  displayName: "Bokio",
  auth: bokioAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/bokio.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework14.PieceCategory.ACCOUNTING],
  description: "Bokio is an accounting software platform which offers digital bookkeeping, invoicing, and financial management tools to help simplify accounting processes.",
  actions: [
    createCustomer,
    createInvoice,
    addLineItemToInvoice,
    createAnItem,
    createAJournalEntry,
    updateAnInvoice,
    findCustomer,
    getDraftInvoiceByCustomerName,
    getAnInvoice,
    getAJournalEntry,
    (0, import_pieces_common14.createCustomApiCallAction)({
      auth: bokioAuth,
      baseUrl: (auth) => `https://api.bokio.se/v1/companies/${auth == null ? void 0 : auth.props.companyId}`,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth == null ? void 0 : auth.props.api_key}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  bokio
});
