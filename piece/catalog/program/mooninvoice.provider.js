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

// ../ap/packages/pieces/community/mooninvoice/src/index.ts
var index_exports = {};
__export(index_exports, {
  mooninvoice: () => mooninvoice
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mooninvoice/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), mooninvoiceAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "MoonInvoice API Key",
  description: `
# MoonInvoice Authentication

To authenticate with MoonInvoice API, you need:

1. **Email**: Your MoonInvoice account email address
2. **Secret Key**: Your MoonInvoice secret key for API access

## Getting Your Credentials

If you don't have your API credentials yet, please contact us at **support@mooninvoice.com** to get your API secret key or if you have any queries.

## API Documentation

For more information about MoonInvoice API, visit: https://www.mooninvoice.com/docs/api
  `,
  required: !0,
  props: {
    email: import_pieces_framework.Property.ShortText({
      displayName: "Email",
      description: "Your MoonInvoice account email address",
      required: !0
    }),
    secret_text: import_pieces_framework.Property.ShortText({
      displayName: "Secret Key",
      description: "Your MoonInvoice API secret key (contact support@mooninvoice.com if you need one)",
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/mooninvoice/src/lib/actions/create-credit-note.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mooninvoice/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://www.mooninvoice.com/api_mi/public";
async function getAccessToken(email, secretKey) {
  try {
    let response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${BASE_URL}/get_auth_token`,
      headers: {
        "Content-Type": "application/json"
      },
      body: {
        email,
        secret_key: secretKey
      }
    });
    if (response.body && response.body.data && response.body.data.token)
      return response.body.data.token;
    throw new Error("Failed to obtain access token: " + JSON.stringify(response.body));
  } catch (error) {
    throw new Error(`Failed to get access token: ${error.message || String(error)}`);
  }
}
async function makeRequest(accessToken, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: accessToken,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/mooninvoice/src/lib/actions/create-credit-note.ts
var import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/mooninvoice/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), companyIdProp = import_pieces_framework2.Property.Dropdown({
  auth: mooninvoiceAuth,
  displayName: "Company",
  description: "Select the company",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (auth === void 0)
      return {
        disabled: !0,
        options: []
      };
    try {
      let accessToken = await getAccessToken(
        auth == null ? void 0 : auth.props.email,
        auth == null ? void 0 : auth.props.secret_text
      );
      return {
        disabled: !1,
        options: (await makeRequest(
          accessToken,
          import_pieces_common2.HttpMethod.GET,
          "/company_list",
          { CompanySort: "asc" }
        )).data.map((company) => ({
          label: company.CompanyName,
          value: company.CompanyID
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: []
      };
    }
  }
}), contactIdProp = import_pieces_framework2.Property.Dropdown({
  auth: mooninvoiceAuth,
  displayName: "Contact",
  description: "Select the contact",
  required: !0,
  refreshers: ["companyId"],
  options: async ({ auth, companyId }) => {
    if (auth === void 0 || companyId === void 0)
      return {
        disabled: !0,
        options: []
      };
    try {
      let accessToken = await getAccessToken(
        auth == null ? void 0 : auth.props.email,
        auth == null ? void 0 : auth.props.secret_text
      );
      return {
        disabled: !1,
        options: (await makeRequest(
          accessToken,
          import_pieces_common2.HttpMethod.GET,
          "/contact_list",
          { CompanyId: companyId, PageSize: 25, page: 1, ContactSort: "asc" }
        )).data.contacts.map((contact) => ({
          label: contact.FirstName + " " + contact.LastName,
          value: contact.ContactID
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: []
      };
    }
  }
}), projectIdProp = import_pieces_framework2.Property.Dropdown({
  auth: mooninvoiceAuth,
  displayName: "Project",
  description: "Select the project",
  required: !0,
  refreshers: ["companyId"],
  options: async ({ auth, companyId }) => {
    if (auth === void 0 || companyId === void 0)
      return {
        disabled: !0,
        options: []
      };
    try {
      let accessToken = await getAccessToken(
        auth == null ? void 0 : auth.props.email,
        auth == null ? void 0 : auth.props.secret_text
      );
      return {
        disabled: !1,
        options: (await makeRequest(
          accessToken,
          import_pieces_common2.HttpMethod.GET,
          "/project_list",
          { CompanyID: companyId, PageSize: 25, page: 1, ProjectSort: "asc" }
        )).data.projects.map((project) => ({
          label: project.ProjectName,
          value: project.ProjectID
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/mooninvoice/src/lib/actions/create-credit-note.ts
var createCreditNote = (0, import_pieces_framework3.createAction)({
  auth: mooninvoiceAuth,
  name: "createCreditNote",
  displayName: "Create Credit Note",
  description: "Create a new credit note in MoonInvoice",
  audience: "both",
  aiMetadata: {
    description: "Creates a new credit note for a contact under a MoonInvoice company. Use when issuing a refund or credit against prior billing. Requires the company, contact, a credit note number, and an entry date; line items (products and tasks) are supplied as JSON arrays. Not idempotent \u2014 each call creates a new credit note regardless of the credit note number.",
    idempotent: !1
  },
  props: {
    companyId: companyIdProp,
    contactId: contactIdProp,
    creditNotesNumber: import_pieces_framework3.Property.ShortText({
      displayName: "Credit Note Number",
      description: "Credit Note Number",
      required: !0
    }),
    entryDate: import_pieces_framework3.Property.ShortText({
      displayName: "Entry Date",
      description: 'Entry Date of Credit Note (e.g., "2025-02-20")',
      required: !0
    }),
    creditNotesHeader: import_pieces_framework3.Property.ShortText({
      displayName: "Credit Note Header",
      description: "Title or Header of Credit Note",
      required: !1
    }),
    creditNotesTerms: import_pieces_framework3.Property.LongText({
      displayName: "Credit Note Terms",
      description: "Terms of Credit Note",
      required: !1
    }),
    notes: import_pieces_framework3.Property.LongText({
      displayName: "Notes",
      description: "Notes/Description of Credit Note",
      required: !1
    }),
    selectedCurrency: import_pieces_framework3.Property.ShortText({
      displayName: "Selected Currency",
      description: "Currency code like en_IN, en_US etc.",
      required: !1
    }),
    discountBeforeTax: import_pieces_framework3.Property.Checkbox({
      displayName: "Discount Before Tax",
      description: "Discount applied before tax",
      required: !1
    }),
    isRoundOff: import_pieces_framework3.Property.Checkbox({
      displayName: "Round Off",
      description: "Round off the final amount",
      required: !1
    }),
    roundedAmount: import_pieces_framework3.Property.Number({
      displayName: "Rounded Amount",
      description: "Rounded amount value",
      required: !1
    }),
    percentage: import_pieces_framework3.Property.Number({
      displayName: "Discount Percentage",
      description: "Discount percentage from total amount",
      required: !1
    }),
    billingStreet1: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address Street 1",
      description: "Billing Address Street1",
      required: !1
    }),
    billingStreet2: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address Street 2",
      description: "Billing Address Street2",
      required: !1
    }),
    billingCity: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address City",
      description: "Billing Address City",
      required: !1
    }),
    billingState: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address State",
      description: "Billing Address State",
      required: !1
    }),
    billingCountry: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address Country",
      description: "Billing Address Country",
      required: !1
    }),
    billingZip: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address Zip",
      description: "Billing Address Zip",
      required: !1
    }),
    shippingStreet1: import_pieces_framework3.Property.ShortText({
      displayName: "Shipping Address Street 1",
      description: "Shipping Address Street1",
      required: !1
    }),
    shippingStreet2: import_pieces_framework3.Property.ShortText({
      displayName: "Shipping Address Street 2",
      description: "Shipping Address Street2",
      required: !1
    }),
    shippingCity: import_pieces_framework3.Property.ShortText({
      displayName: "Shipping Address City",
      description: "Shipping Address City",
      required: !1
    }),
    shippingState: import_pieces_framework3.Property.ShortText({
      displayName: "Shipping Address State",
      description: "Shipping Address State",
      required: !1
    }),
    shippingCountry: import_pieces_framework3.Property.ShortText({
      displayName: "Shipping Address Country",
      description: "Shipping Address Country",
      required: !1
    }),
    shippingZip: import_pieces_framework3.Property.ShortText({
      displayName: "Shipping Address Zip",
      description: "Shipping Address Zip",
      required: !1
    }),
    productDataJson: import_pieces_framework3.Property.LongText({
      displayName: "Product Data (JSON)",
      description: "Product Data as JSON array. Each item should contain: ProductName, HSNCode, Quantity, UnitCost, ProductNotes, ProductID, Total, SubAmount, DiscountIsPercentage, ProductType, DiscountValue, TaxID (array), TaxType",
      required: !1
    }),
    taskDataJson: import_pieces_framework3.Property.LongText({
      displayName: "Task Data (JSON)",
      description: "Task Data as JSON array. Each item should contain: TaskName, Hours, TaskRate, TimeNotes, TaskID, Total, DiscountIsPercentage, TaskType, DiscountValue, TaxID (array), TaxType",
      required: !1
    })
  },
  async run(context) {
    let {
      companyId,
      contactId,
      creditNotesNumber,
      entryDate,
      creditNotesHeader,
      creditNotesTerms,
      notes,
      selectedCurrency,
      discountBeforeTax,
      isRoundOff,
      roundedAmount,
      percentage,
      billingStreet1,
      billingStreet2,
      billingCity,
      billingState,
      billingCountry,
      billingZip,
      shippingStreet1,
      shippingStreet2,
      shippingCity,
      shippingState,
      shippingCountry,
      shippingZip,
      productDataJson,
      taskDataJson
    } = context.propsValue, body = {
      CompanyID: companyId,
      ContactID: contactId,
      CreditNotesNumber: creditNotesNumber,
      EntryDate: entryDate
    };
    creditNotesHeader && (body.CreditNotesHeader = creditNotesHeader), creditNotesTerms && (body.CreditNotesTerms = creditNotesTerms), notes && (body.Notes = notes), selectedCurrency && (body.SelectedCurrency = selectedCurrency), discountBeforeTax !== void 0 && (body.DiscountBeforeTax = discountBeforeTax);
    let creditNotesFolder = {
      isroundoff: isRoundOff ? 1 : 0
    };
    if (roundedAmount != null && (creditNotesFolder.rounded_amount = roundedAmount), percentage != null && (creditNotesFolder.Percentage = percentage), (billingStreet1 || billingStreet2 || billingCity || billingState || billingCountry || billingZip) && (creditNotesFolder.Billing_Add = {
      Street1: billingStreet1 || "",
      Street2: billingStreet2 || "",
      City: billingCity || "",
      State: billingState || "",
      Country: billingCountry || "",
      Zip: billingZip || ""
    }), (shippingStreet1 || shippingStreet2 || shippingCity || shippingState || shippingCountry || shippingZip) && (creditNotesFolder.Secondary_Add = {
      Street1: shippingStreet1 || "",
      Street2: shippingStreet2 || "",
      City: shippingCity || "",
      State: shippingState || "",
      Country: shippingCountry || "",
      Zip: shippingZip || ""
    }), body.CreditNotesFolder = JSON.stringify(creditNotesFolder), productDataJson)
      try {
        body.ProductData = JSON.parse(productDataJson);
      } catch (error) {
        throw new Error("Invalid ProductData JSON format");
      }
    if (taskDataJson)
      try {
        body.TaskData = JSON.parse(taskDataJson);
      } catch (error) {
        throw new Error("Invalid TaskData JSON format");
      }
    let accessToken = await getAccessToken(
      context.auth.props.email,
      context.auth.props.secret_text
    );
    return await makeRequest(
      accessToken,
      import_pieces_common3.HttpMethod.POST,
      "/create_credit_notes",
      body
    );
  }
});

// ../ap/packages/pieces/community/mooninvoice/src/lib/actions/create-invoice.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var createInvoice = (0, import_pieces_framework4.createAction)({
  auth: mooninvoiceAuth,
  name: "createInvoice",
  displayName: "Create Invoice",
  description: "Create a new invoice in MoonInvoice",
  audience: "both",
  aiMetadata: {
    description: "Creates a new invoice for a contact under a MoonInvoice company, optionally as a recurring invoice on a chosen interval. Use when billing a customer. Requires the company, contact, an invoice number, an entry date, and a due date; payment methods and line items (products and tasks) are supplied as JSON arrays. Not idempotent \u2014 each call creates a new invoice regardless of the invoice number.",
    idempotent: !1
  },
  props: {
    companyId: companyIdProp,
    contactId: contactIdProp,
    invoiceNumber: import_pieces_framework4.Property.ShortText({
      displayName: "Invoice Number",
      description: "Invoice Number",
      required: !0
    }),
    entryDate: import_pieces_framework4.Property.ShortText({
      displayName: "Entry Date",
      description: 'Entry Date of Invoice (e.g., "2025-02-20")',
      required: !0
    }),
    dueDate: import_pieces_framework4.Property.ShortText({
      displayName: "Due Date",
      description: 'Payment Due Date of Invoice (e.g., "2025-03-20")',
      required: !0
    }),
    invoiceHeader: import_pieces_framework4.Property.ShortText({
      displayName: "Invoice Header",
      description: "Title or Header of Invoice",
      required: !1
    }),
    terms: import_pieces_framework4.Property.LongText({
      displayName: "Terms",
      description: "Terms of Invoice",
      required: !1
    }),
    notes: import_pieces_framework4.Property.LongText({
      displayName: "Notes",
      description: "Notes/Description of Invoice",
      required: !1
    }),
    selectedCurrency: import_pieces_framework4.Property.ShortText({
      displayName: "Selected Currency",
      description: "Currency code like en_IN, en_US etc.",
      required: !1
    }),
    shippingCost: import_pieces_framework4.Property.Number({
      displayName: "Shipping Cost",
      description: "Shipping Cost of Invoice",
      required: !1
    }),
    shippingMethod: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping Method",
      description: "Shipping Method of Invoice",
      required: !1
    }),
    poNo: import_pieces_framework4.Property.ShortText({
      displayName: "PO Number",
      description: "PO No of Invoice",
      required: !1
    }),
    discountAmount: import_pieces_framework4.Property.Number({
      displayName: "Discount Amount",
      description: "Discount Amount on invoice total",
      required: !1
    }),
    discountBeforeTax: import_pieces_framework4.Property.Checkbox({
      displayName: "Discount Before Tax",
      description: "Discount applied before tax",
      required: !1
    }),
    depositeAmount: import_pieces_framework4.Property.Number({
      displayName: "Deposite Amount",
      description: "Deposite Amount of Invoice",
      required: !1
    }),
    amountDue: import_pieces_framework4.Property.Number({
      displayName: "Amount Due",
      description: "Remaining Payment Amount of Invoice",
      required: !1
    }),
    amountPaid: import_pieces_framework4.Property.Number({
      displayName: "Amount Paid",
      description: "Total Paid Amount of Invoice",
      required: !1
    }),
    totalAmount: import_pieces_framework4.Property.Number({
      displayName: "Total Amount",
      description: "Total Amount of Invoice",
      required: !1
    }),
    subTotal: import_pieces_framework4.Property.Number({
      displayName: "Sub Total",
      description: "Sub Total Amount of Invoice",
      required: !1
    }),
    isRecurring: import_pieces_framework4.Property.Checkbox({
      displayName: "Is Recurring",
      description: "If Invoice is recurring",
      required: !1
    }),
    recurringEndDate: import_pieces_framework4.Property.ShortText({
      displayName: "Recurring End Date",
      description: 'End Date of Invoice recurring (e.g., "2025-12-31")',
      required: !1
    }),
    recurringIntervalInString: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Recurring Period",
      description: "Invoice recurring period",
      required: !1,
      options: {
        options: [
          { label: "Daily", value: "Daily" },
          { label: "Weekly", value: "Weekly" },
          { label: "Fortnightly", value: "Fortnightly" },
          { label: "Monthly", value: "Monthly" },
          { label: "Bi-Monthly", value: "BiMonthly" },
          { label: "Quarterly", value: "Quarterly" },
          { label: "6 Months", value: "6-Months" },
          { label: "Annually", value: "Annually" },
          { label: "2 Years", value: "2-Years" }
        ]
      }
    }),
    billingStreet1: import_pieces_framework4.Property.ShortText({
      displayName: "Billing Address Street 1",
      description: "Billing Address Street1",
      required: !1
    }),
    billingStreet2: import_pieces_framework4.Property.ShortText({
      displayName: "Billing Address Street 2",
      description: "Billing Address Street2",
      required: !1
    }),
    billingCity: import_pieces_framework4.Property.ShortText({
      displayName: "Billing Address City",
      description: "Billing Address City",
      required: !1
    }),
    billingState: import_pieces_framework4.Property.ShortText({
      displayName: "Billing Address State",
      description: "Billing Address State",
      required: !1
    }),
    billingCountry: import_pieces_framework4.Property.ShortText({
      displayName: "Billing Address Country",
      description: "Billing Address Country",
      required: !1
    }),
    billingZip: import_pieces_framework4.Property.ShortText({
      displayName: "Billing Address Zip",
      description: "Billing Address Zip",
      required: !1
    }),
    shippingStreet1: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping Address Street 1",
      description: "Shipping Address Street1",
      required: !1
    }),
    shippingStreet2: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping Address Street 2",
      description: "Shipping Address Street2",
      required: !1
    }),
    shippingCity: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping Address City",
      description: "Shipping Address City",
      required: !1
    }),
    shippingState: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping Address State",
      description: "Shipping Address State",
      required: !1
    }),
    shippingCountry: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping Address Country",
      description: "Shipping Address Country",
      required: !1
    }),
    shippingZip: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping Address Zip",
      description: "Shipping Address Zip",
      required: !1
    }),
    paymentMethodsJson: import_pieces_framework4.Property.LongText({
      displayName: "Payment Methods (JSON)",
      description: 'PaymentMethods is array and contains Payment Method IDs. Example: ["3A72C3BF-1B42-4A62-9E8E-5C4178E07D2F", "D89F53D9-7E01-4438-88BD-74D9C4101D7C"]',
      required: !1
    }),
    productDataJson: import_pieces_framework4.Property.LongText({
      displayName: "Product Data (JSON)",
      description: "Product Data as JSON array. Each item: ProductName, Quantity, UnitCost, ProductNotes, ProductID, Total, SubAmount, DiscountIsPercentage, ProductType, DiscountValue, TaxID (array), TaxType",
      required: !1
    }),
    taskDataJson: import_pieces_framework4.Property.LongText({
      displayName: "Task Data (JSON)",
      description: "Task Data as JSON array. Each item: TaskName, Hours, TaskRate, TimeNotes, TaskID, Total, DiscountIsPercentage, TaskType, DiscountValue, TaxID (array), TaxType",
      required: !1
    })
  },
  async run(context) {
    let {
      companyId,
      contactId,
      invoiceNumber,
      entryDate,
      dueDate,
      invoiceHeader,
      terms,
      notes,
      selectedCurrency,
      shippingCost,
      shippingMethod,
      poNo,
      discountAmount,
      discountBeforeTax,
      depositeAmount,
      amountDue,
      amountPaid,
      totalAmount,
      subTotal,
      isRecurring,
      recurringEndDate,
      recurringIntervalInString,
      billingStreet1,
      billingStreet2,
      billingCity,
      billingState,
      billingCountry,
      billingZip,
      shippingStreet1,
      shippingStreet2,
      shippingCity,
      shippingState,
      shippingCountry,
      shippingZip,
      paymentMethodsJson,
      productDataJson,
      taskDataJson
    } = context.propsValue, body = {
      CompanyID: companyId,
      ContactID: contactId,
      InvoiceNumber: invoiceNumber,
      EntryDate: entryDate,
      DueDate: dueDate
    };
    if (invoiceHeader && (body.InvoiceHeader = invoiceHeader), terms && (body.Terms = terms), notes && (body.Notes = notes), selectedCurrency && (body.SelectedCurrency = selectedCurrency), shippingCost != null && (body.ShippingCost = shippingCost), shippingMethod && (body.ShippingMethod = shippingMethod), poNo && (body.PoNo = poNo), discountAmount != null && (body.DiscountAmount = discountAmount), discountBeforeTax !== void 0 && (body.DiscountBeforeTax = discountBeforeTax), depositeAmount != null && (body.DepositeAmount = depositeAmount), amountDue != null && (body.AmountDue = amountDue), amountPaid != null && (body.AmountPaid = amountPaid), totalAmount != null && (body.TotalAmount = totalAmount), subTotal != null && (body.SubTotal = subTotal), isRecurring !== void 0 && (body.IsRecurring = isRecurring ? 1 : 0), recurringEndDate && (body.RecurringEndDate = recurringEndDate), recurringIntervalInString && (body.RecurringIntervalInString = recurringIntervalInString), (billingStreet1 || billingStreet2 || billingCity || billingState || billingCountry || billingZip) && (body.Billing_Add = {
      Street1: billingStreet1 || "",
      Street2: billingStreet2 || "",
      City: billingCity || "",
      State: billingState || "",
      Country: billingCountry || "",
      Zip: billingZip || ""
    }), (shippingStreet1 || shippingStreet2 || shippingCity || shippingState || shippingCountry || shippingZip) && (body.Secondary_Add = {
      Street1: shippingStreet1 || "",
      Street2: shippingStreet2 || "",
      City: shippingCity || "",
      State: shippingState || "",
      Country: shippingCountry || "",
      Zip: shippingZip || ""
    }), paymentMethodsJson)
      try {
        body.PaymentMethods = JSON.parse(paymentMethodsJson);
      } catch (error) {
        throw new Error("Invalid PaymentMethods JSON format");
      }
    if (productDataJson)
      try {
        body.ProductData = JSON.parse(productDataJson);
      } catch (error) {
        throw new Error("Invalid ProductData JSON format");
      }
    if (taskDataJson)
      try {
        body.TaskData = JSON.parse(taskDataJson);
      } catch (error) {
        throw new Error("Invalid TaskData JSON format");
      }
    let accessToken = await getAccessToken(
      context.auth.props.email,
      context.auth.props.secret_text
    );
    return await makeRequest(
      accessToken,
      import_pieces_common4.HttpMethod.POST,
      "/create_invoice",
      body
    );
  }
});

// ../ap/packages/pieces/community/mooninvoice/src/lib/actions/create-expense.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");
var createExpense = (0, import_pieces_framework5.createAction)({
  auth: mooninvoiceAuth,
  name: "createExpense",
  displayName: "Create Expense",
  description: "Create a new expense in MoonInvoice",
  audience: "both",
  aiMetadata: {
    description: "Records a new expense against a contact under a MoonInvoice company, optionally as a recurring expense on a chosen interval. Use when logging a cost or vendor payment. Requires the company, contact, an expense number, and a date; tax IDs are supplied as a JSON array. Not idempotent \u2014 each call creates a new expense regardless of the expense number.",
    idempotent: !1
  },
  props: {
    companyId: companyIdProp,
    date: import_pieces_framework5.Property.ShortText({
      displayName: "Date",
      description: 'Date of Expense (e.g., "2025-02-20")',
      required: !0
    }),
    expenseNumber: import_pieces_framework5.Property.ShortText({
      displayName: "Expense Number",
      description: "Expense Number",
      required: !0
    }),
    contactId: contactIdProp,
    expenseCost: import_pieces_framework5.Property.Number({
      displayName: "Expense Cost",
      description: "Cost of Expense",
      required: !1
    }),
    expenseNotes: import_pieces_framework5.Property.LongText({
      displayName: "Expense Notes",
      description: "Expense Notes",
      required: !1
    }),
    totalAmount: import_pieces_framework5.Property.Number({
      displayName: "Total Amount",
      description: "Total Amount of Expense",
      required: !1
    }),
    categoryName: import_pieces_framework5.Property.ShortText({
      displayName: "Category Name",
      description: "Category Name of Expense (e.g., Wages, Supplies, etc.)",
      required: !1
    }),
    paymentType: import_pieces_framework5.Property.ShortText({
      displayName: "Payment Type",
      description: "Type of Payment (e.g., Visa, Cash, UPI, etc.)",
      required: !1
    }),
    selectedCurrency: import_pieces_framework5.Property.ShortText({
      displayName: "Selected Currency",
      description: "Currency code like en_IN, en_US etc.",
      required: !1
    }),
    isRecurring: import_pieces_framework5.Property.Checkbox({
      displayName: "Is Recurring",
      description: "If Expense is recurring",
      required: !1
    }),
    recurringEndDate: import_pieces_framework5.Property.ShortText({
      displayName: "Recurring End Date",
      description: 'End Date of Expense recurring (e.g., "2025-12-31")',
      required: !1
    }),
    recurringIntervalInDay: import_pieces_framework5.Property.Number({
      displayName: "Recurring Interval (Days)",
      description: "No of Days for Expense recurring",
      required: !1
    }),
    recurringIntervalInString: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Recurring Period",
      description: "Expense recurring period",
      required: !1,
      options: {
        options: [
          { label: "Daily", value: "Daily" },
          { label: "Weekly", value: "Weekly" },
          { label: "Fortnightly", value: "Fortnightly" },
          { label: "Monthly", value: "Monthly" },
          { label: "Bi-Monthly", value: "BiMonthly" },
          { label: "Quarterly", value: "Quarterly" },
          { label: "6 Months", value: "6-Months" },
          { label: "Annually", value: "Annually" },
          { label: "2 Years", value: "2-Years" }
        ]
      }
    }),
    isRoundOff: import_pieces_framework5.Property.Checkbox({
      displayName: "Round Off",
      description: "Total Amount Round off or not",
      required: !1
    }),
    roundedAmount: import_pieces_framework5.Property.Number({
      displayName: "Rounded Amount",
      description: "Total Amount Rounded",
      required: !1
    }),
    taxIdJson: import_pieces_framework5.Property.LongText({
      displayName: "Tax IDs (JSON)",
      description: 'TaxID is an array of Tax IDs. Example: ["1C34BE89-2FC6-48A4-9808-99490F40D3E1", "95A1956E-E60A-4FAC-8EC4-C88EE7431280"]',
      required: !1
    }),
    taxType: import_pieces_framework5.Property.Checkbox({
      displayName: "Tax is Inclusive",
      description: "If Tax is Inclusive then true otherwise false",
      required: !1
    })
  },
  async run(context) {
    let {
      companyId,
      date,
      expenseNumber,
      contactId,
      expenseCost,
      expenseNotes,
      totalAmount,
      categoryName,
      paymentType,
      selectedCurrency,
      isRecurring,
      recurringEndDate,
      recurringIntervalInDay,
      recurringIntervalInString,
      isRoundOff,
      roundedAmount,
      taxIdJson,
      taxType
    } = context.propsValue, body = {
      CompanyID: companyId,
      Date: date,
      ExpenseNumber: expenseNumber,
      ContactID: contactId
    };
    if (expenseCost != null && (body.ExpenseCost = expenseCost), expenseNotes && (body.ExpenseNotes = expenseNotes), totalAmount != null && (body.TotalAmount = totalAmount), categoryName && (body.CategoryName = categoryName), paymentType && (body.PaymentType = paymentType), selectedCurrency && (body.SelectedCurrency = selectedCurrency), isRecurring !== void 0 && (body.IsRecurring = isRecurring ? 1 : 0), recurringEndDate && (body.RecurringEndDate = recurringEndDate), recurringIntervalInDay != null && (body.RecurringIntervalInDay = recurringIntervalInDay), recurringIntervalInString && (body.RecurringIntervalInString = recurringIntervalInString), isRoundOff !== void 0 && (body.IsRoundOff = isRoundOff ? 1 : 0), roundedAmount != null && (body.RoundedAmount = roundedAmount), taxType !== void 0 && (body.TaxType = taxType ? 1 : 0), taxIdJson)
      try {
        body.TaxID = JSON.parse(taxIdJson);
      } catch (error) {
        throw new Error("Invalid TaxID JSON format");
      }
    let accessToken = await getAccessToken(
      context.auth.props.email,
      context.auth.props.secret_text
    );
    return await makeRequest(
      accessToken,
      import_pieces_common5.HttpMethod.POST,
      "/create_expense",
      body
    );
  }
});

// ../ap/packages/pieces/community/mooninvoice/src/lib/actions/create-estimate.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var createEstimate = (0, import_pieces_framework6.createAction)({
  auth: mooninvoiceAuth,
  name: "createEstimate",
  displayName: "Create Estimate",
  description: "Create a new estimate in MoonInvoice",
  audience: "both",
  aiMetadata: {
    description: "Creates a new estimate (quote) for a contact under a MoonInvoice company. Use when generating a price quote before invoicing. Requires the company, contact, an estimate number, and an entry date; line items (products and tasks) are supplied as JSON arrays. Not idempotent \u2014 each call creates a new estimate regardless of the estimate number.",
    idempotent: !1
  },
  props: {
    companyId: companyIdProp,
    contactId: contactIdProp,
    estimateNumber: import_pieces_framework6.Property.ShortText({
      displayName: "Estimate Number",
      description: "Estimate Number",
      required: !0
    }),
    entryDate: import_pieces_framework6.Property.ShortText({
      displayName: "Entry Date",
      description: 'Entry Date of Estimate (e.g., "2025-02-20")',
      required: !0
    }),
    estimateHeader: import_pieces_framework6.Property.ShortText({
      displayName: "Estimate Header",
      description: "Title or Header of Estimate",
      required: !1
    }),
    estimateTerms: import_pieces_framework6.Property.LongText({
      displayName: "Estimate Terms",
      description: "Terms of Estimate",
      required: !1
    }),
    estimateNotes: import_pieces_framework6.Property.LongText({
      displayName: "Estimate Notes",
      description: "Notes/Description of Estimate",
      required: !1
    }),
    selectedCurrency: import_pieces_framework6.Property.ShortText({
      displayName: "Selected Currency",
      description: "Currency code like en_IN, en_US etc.",
      required: !1
    }),
    shippingCost: import_pieces_framework6.Property.Number({
      displayName: "Shipping Cost",
      description: "Shipping Cost of Estimate",
      required: !1
    }),
    shippingMethod: import_pieces_framework6.Property.ShortText({
      displayName: "Shipping Method",
      description: "Shipping Method of Estimate",
      required: !1
    }),
    discountAmount: import_pieces_framework6.Property.Number({
      displayName: "Discount Amount",
      description: "Discount Amount on Estimate total",
      required: !1
    }),
    discountBeforeTax: import_pieces_framework6.Property.Checkbox({
      displayName: "Discount Before Tax",
      description: "Discount applied before tax",
      required: !1
    }),
    isRoundOff: import_pieces_framework6.Property.Checkbox({
      displayName: "Round Off",
      description: "Round off the final amount",
      required: !1
    }),
    roundedAmount: import_pieces_framework6.Property.Number({
      displayName: "Rounded Amount",
      description: "Rounded amount value",
      required: !1
    }),
    percentage: import_pieces_framework6.Property.Number({
      displayName: "Discount Percentage",
      description: "Discount percentage from total amount",
      required: !1
    }),
    billingStreet1: import_pieces_framework6.Property.ShortText({
      displayName: "Billing Address Street 1",
      description: "Billing Address Street1",
      required: !1
    }),
    billingStreet2: import_pieces_framework6.Property.ShortText({
      displayName: "Billing Address Street 2",
      description: "Billing Address Street2",
      required: !1
    }),
    billingCity: import_pieces_framework6.Property.ShortText({
      displayName: "Billing Address City",
      description: "Billing Address City",
      required: !1
    }),
    billingState: import_pieces_framework6.Property.ShortText({
      displayName: "Billing Address State",
      description: "Billing Address State",
      required: !1
    }),
    billingCountry: import_pieces_framework6.Property.ShortText({
      displayName: "Billing Address Country",
      description: "Billing Address Country",
      required: !1
    }),
    billingZip: import_pieces_framework6.Property.ShortText({
      displayName: "Billing Address Zip",
      description: "Billing Address Zip",
      required: !1
    }),
    shippingStreet1: import_pieces_framework6.Property.ShortText({
      displayName: "Shipping Address Street 1",
      description: "Shipping Address Street1",
      required: !1
    }),
    shippingStreet2: import_pieces_framework6.Property.ShortText({
      displayName: "Shipping Address Street 2",
      description: "Shipping Address Street2",
      required: !1
    }),
    shippingCity: import_pieces_framework6.Property.ShortText({
      displayName: "Shipping Address City",
      description: "Shipping Address City",
      required: !1
    }),
    shippingState: import_pieces_framework6.Property.ShortText({
      displayName: "Shipping Address State",
      description: "Shipping Address State",
      required: !1
    }),
    shippingCountry: import_pieces_framework6.Property.ShortText({
      displayName: "Shipping Address Country",
      description: "Shipping Address Country",
      required: !1
    }),
    shippingZip: import_pieces_framework6.Property.ShortText({
      displayName: "Shipping Address Zip",
      description: "Shipping Address Zip",
      required: !1
    }),
    productDataJson: import_pieces_framework6.Property.LongText({
      displayName: "Product Data (JSON)",
      description: "Product Data as JSON array. Each item: ProductName, Quantity, UnitCost, ProductNotes, ProductID, Total, SubAmount, DiscountIsPercentage, ProductType, DiscountValue, TaxID (array), TaxType",
      required: !1
    }),
    taskDataJson: import_pieces_framework6.Property.LongText({
      displayName: "Task Data (JSON)",
      description: "Task Data as JSON array. Each item: TaskName, Hours, TaskRate, TimeNotes, TaskID, Total, DiscountIsPercentage, TaskType, DiscountValue, TaxID (array), TaxType",
      required: !1
    })
  },
  async run(context) {
    let {
      companyId,
      contactId,
      estimateNumber,
      entryDate,
      estimateHeader,
      estimateTerms,
      estimateNotes,
      selectedCurrency,
      shippingCost,
      shippingMethod,
      discountAmount,
      discountBeforeTax,
      isRoundOff,
      roundedAmount,
      percentage,
      billingStreet1,
      billingStreet2,
      billingCity,
      billingState,
      billingCountry,
      billingZip,
      shippingStreet1,
      shippingStreet2,
      shippingCity,
      shippingState,
      shippingCountry,
      shippingZip,
      productDataJson,
      taskDataJson
    } = context.propsValue, body = {
      CompanyID: companyId,
      ContactID: contactId,
      EstimateNumber: estimateNumber,
      EntryDate: entryDate
    };
    estimateHeader && (body.EstimateHeader = estimateHeader), estimateTerms && (body.EstimateTerms = estimateTerms), estimateNotes && (body.EstimateNotes = estimateNotes), selectedCurrency && (body.SelectedCurrency = selectedCurrency), shippingCost != null && (body.ShippingCost = shippingCost), shippingMethod && (body.ShippingMethod = shippingMethod), discountAmount != null && (body.DiscountAmount = discountAmount), discountBeforeTax !== void 0 && (body.DiscountBeforeTax = discountBeforeTax);
    let estimateFolder = {
      isroundoff: isRoundOff ? 1 : 0
    };
    if (roundedAmount != null && (estimateFolder.rounded_amount = roundedAmount), percentage != null && (estimateFolder.Percentage = percentage), (billingStreet1 || billingStreet2 || billingCity || billingState || billingCountry || billingZip) && (estimateFolder.Billing_Add = {
      Street1: billingStreet1 || "",
      Street2: billingStreet2 || "",
      City: billingCity || "",
      State: billingState || "",
      Country: billingCountry || "",
      Zip: billingZip || ""
    }), (shippingStreet1 || shippingStreet2 || shippingCity || shippingState || shippingCountry || shippingZip) && (estimateFolder.Secondary_Add = {
      Street1: shippingStreet1 || "",
      Street2: shippingStreet2 || "",
      City: shippingCity || "",
      State: shippingState || "",
      Country: shippingCountry || "",
      Zip: shippingZip || ""
    }), body.EstimateFolder = JSON.stringify(estimateFolder), productDataJson)
      try {
        body.ProductData = JSON.parse(productDataJson);
      } catch (error) {
        throw new Error("Invalid ProductData JSON format");
      }
    if (taskDataJson)
      try {
        body.TaskData = JSON.parse(taskDataJson);
      } catch (error) {
        throw new Error("Invalid TaskData JSON format");
      }
    let accessToken = await getAccessToken(
      context.auth.props.email,
      context.auth.props.secret_text
    );
    return await makeRequest(
      accessToken,
      import_pieces_common6.HttpMethod.POST,
      "/create_estimate",
      body
    );
  }
});

// ../ap/packages/pieces/community/mooninvoice/src/lib/actions/add-new-contact.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common");
var addNewContact = (0, import_pieces_framework7.createAction)({
  auth: mooninvoiceAuth,
  name: "addNewContact",
  displayName: "Add New Contact",
  description: "Add a new contact to a company in MoonInvoice",
  audience: "both",
  aiMetadata: {
    description: "Creates a new contact (customer and/or vendor) under a given MoonInvoice company. Use when adding a person or organization that will later be billed, invoiced, or paid. Requires the target company; all contact details (name, email, addresses, tax/registration numbers, opening balance) are optional. Not idempotent \u2014 each call creates a separate contact, even with identical details.",
    idempotent: !1
  },
  props: {
    companyId: companyIdProp,
    organization: import_pieces_framework7.Property.ShortText({
      displayName: "Organization",
      description: "Name of Organization/Company",
      required: !1
    }),
    firstName: import_pieces_framework7.Property.ShortText({
      displayName: "First Name",
      description: "First Name of Contact",
      required: !1
    }),
    lastName: import_pieces_framework7.Property.ShortText({
      displayName: "Last Name",
      description: "Last Name of Contact",
      required: !1
    }),
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Email",
      description: "Email of Contact person",
      required: !1
    }),
    mobileNo: import_pieces_framework7.Property.ShortText({
      displayName: "Mobile Number",
      description: "Mobile No. of Contact person",
      required: !1
    }),
    businessNo: import_pieces_framework7.Property.ShortText({
      displayName: "Business Number",
      description: "Business Ph No. of Contact person",
      required: !1
    }),
    homeNo: import_pieces_framework7.Property.ShortText({
      displayName: "Home Number",
      description: "Home Ph No. of Contact person",
      required: !1
    }),
    vatNo: import_pieces_framework7.Property.ShortText({
      displayName: "VAT Number",
      description: "VAT No. of Contact",
      required: !1
    }),
    regNo: import_pieces_framework7.Property.ShortText({
      displayName: "Registration Number",
      description: "Registration No.",
      required: !1
    }),
    faxNo: import_pieces_framework7.Property.ShortText({
      displayName: "Fax Number",
      description: "Fax No.",
      required: !1
    }),
    internalNotes: import_pieces_framework7.Property.LongText({
      displayName: "Internal Notes",
      description: "Internal Notes of Contact",
      required: !1
    }),
    hourlyRate: import_pieces_framework7.Property.Number({
      displayName: "Hourly Rate",
      description: "Hourly Rate of Contact person",
      required: !1
    }),
    currencyCode: import_pieces_framework7.Property.ShortText({
      displayName: "Currency Code",
      description: "Currency code like INR, USD, CAD etc.",
      required: !1
    }),
    statusCustomer: import_pieces_framework7.Property.Checkbox({
      displayName: "Is Customer",
      description: "If Contact is customer",
      required: !1
    }),
    statusVendor: import_pieces_framework7.Property.Checkbox({
      displayName: "Is Vendor",
      description: "If Contact is vendor",
      required: !1
    }),
    paymentReminder: import_pieces_framework7.Property.Checkbox({
      displayName: "Payment Reminder",
      description: "Enable payment reminder",
      required: !1
    }),
    billingAddressStreet1: import_pieces_framework7.Property.ShortText({
      displayName: "Billing Address Street 1",
      description: "Billing Address Street1 of Contact person",
      required: !1
    }),
    billingAddressStreet2: import_pieces_framework7.Property.ShortText({
      displayName: "Billing Address Street 2",
      description: "Billing Address Street2 of Contact person",
      required: !1
    }),
    billingAddressCity: import_pieces_framework7.Property.ShortText({
      displayName: "Billing Address City",
      description: "Billing Address City of Contact person",
      required: !1
    }),
    billingAddressState: import_pieces_framework7.Property.ShortText({
      displayName: "Billing Address State",
      description: "Billing Address State of Contact person",
      required: !1
    }),
    billingAddressCountry: import_pieces_framework7.Property.ShortText({
      displayName: "Billing Address Country",
      description: "Billing Address Country of Contact person",
      required: !1
    }),
    billingAddressZip: import_pieces_framework7.Property.ShortText({
      displayName: "Billing Address Zip",
      description: "Billing Address Zip of Contact person",
      required: !1
    }),
    shippingAddressStreet1: import_pieces_framework7.Property.ShortText({
      displayName: "Shipping Address Street 1",
      description: "Shipping Address Street1 of Contact person",
      required: !1
    }),
    shippingAddressStreet2: import_pieces_framework7.Property.ShortText({
      displayName: "Shipping Address Street 2",
      description: "Shipping Address Street2 of Contact person",
      required: !1
    }),
    shippingAddressCity: import_pieces_framework7.Property.ShortText({
      displayName: "Shipping Address City",
      description: "Shipping Address City of Contact person",
      required: !1
    }),
    shippingAddressState: import_pieces_framework7.Property.ShortText({
      displayName: "Shipping Address State",
      description: "Shipping Address State of Contact person",
      required: !1
    }),
    shippingAddressCountry: import_pieces_framework7.Property.ShortText({
      displayName: "Shipping Address Country",
      description: "Shipping Address Country of Contact person",
      required: !1
    }),
    shippingAddressZip: import_pieces_framework7.Property.ShortText({
      displayName: "Shipping Address Zip",
      description: "Shipping Address Zip of Contact person",
      required: !1
    }),
    paymentTerms: import_pieces_framework7.Property.Number({
      displayName: "Payment Terms (Days)",
      description: "Payment terms must be 7, 10, 15, 30, 60, 90, or 180 days only",
      required: !1
    }),
    openingBalance: import_pieces_framework7.Property.Number({
      displayName: "Opening Balance",
      description: "Opening Balance of Contact",
      required: !1
    }),
    openingBalanceDate: import_pieces_framework7.Property.ShortText({
      displayName: "Opening Balance Date",
      description: 'Opening Balance Date in format: "2025-1-8 15:9:40"',
      required: !1
    }),
    bankDetails: import_pieces_framework7.Property.LongText({
      displayName: "Bank Details",
      description: 'Bank details in format: "Account No: 78452356\\nAccount Holder Name: Abby Hamill\\nBranch Name: ABC"',
      required: !1
    })
  },
  async run(context) {
    let {
      companyId,
      organization,
      firstName,
      lastName,
      email,
      mobileNo,
      businessNo,
      homeNo,
      vatNo,
      regNo,
      faxNo,
      internalNotes,
      hourlyRate,
      currencyCode,
      statusCustomer,
      statusVendor,
      paymentReminder,
      billingAddressStreet1,
      billingAddressStreet2,
      billingAddressCity,
      billingAddressState,
      billingAddressCountry,
      billingAddressZip,
      shippingAddressStreet1,
      shippingAddressStreet2,
      shippingAddressCity,
      shippingAddressState,
      shippingAddressCountry,
      shippingAddressZip,
      paymentTerms,
      openingBalance,
      openingBalanceDate,
      bankDetails
    } = context.propsValue, body = {
      CompanyID: companyId
    };
    organization && (body.Organization = organization), firstName && (body.FirstName = firstName), lastName && (body.LastName = lastName), email && (body.Email = email), mobileNo && (body.MobileNo = mobileNo), businessNo && (body.BusinessNo = businessNo), homeNo && (body.HomeNo = homeNo), vatNo && (body.VatNo = vatNo), regNo && (body.RegNo = regNo), faxNo && (body.FaxNo = faxNo), internalNotes && (body.InternalNotes = internalNotes), hourlyRate != null && (body.HourlyRate = hourlyRate), currencyCode && (body.CurrencyCode = currencyCode), statusCustomer !== void 0 && (body.StatusCustomer = statusCustomer ? 1 : 0), statusVendor !== void 0 && (body.StatusVendor = statusVendor ? 1 : 0), paymentReminder !== void 0 && (body.PaymentReminder = paymentReminder ? 1 : 0), billingAddressStreet1 && (body.BillingAddressStreet1 = billingAddressStreet1), billingAddressStreet2 && (body.BillingAddressStreet2 = billingAddressStreet2), billingAddressCity && (body.BillingAddressCity = billingAddressCity), billingAddressState && (body.BillingAddressState = billingAddressState), billingAddressCountry && (body.BillingAddressCountry = billingAddressCountry), billingAddressZip && (body.BillingAddressZip = billingAddressZip), shippingAddressStreet1 && (body.ShippingAddressStreet1 = shippingAddressStreet1), shippingAddressStreet2 && (body.ShippingAddressStreet2 = shippingAddressStreet2), shippingAddressCity && (body.ShippingAddressCity = shippingAddressCity), shippingAddressState && (body.ShippingAddressState = shippingAddressState), shippingAddressCountry && (body.ShippingAddressCountry = shippingAddressCountry), shippingAddressZip && (body.ShippingAddressZip = shippingAddressZip), paymentTerms != null && (body.PaymentTerms = paymentTerms), openingBalance != null && (body.OpeningBalance = openingBalance), openingBalanceDate && (body.OpeningBalanceDate = openingBalanceDate), bankDetails && (body.BankDetails = bankDetails);
    let accessToken = await getAccessToken(
      context.auth.props.email,
      context.auth.props.secret_text
    );
    return await makeRequest(
      accessToken,
      import_pieces_common7.HttpMethod.POST,
      "/add_contact",
      body
    );
  }
});

// ../ap/packages/pieces/community/mooninvoice/src/lib/actions/create-product.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common");
var createProduct = (0, import_pieces_framework8.createAction)({
  auth: mooninvoiceAuth,
  name: "createProduct",
  displayName: "Create Product",
  description: "Create a new product in MoonInvoice",
  audience: "both",
  aiMetadata: {
    description: "Creates a new product (catalog item) under a MoonInvoice company that can later be added to invoices, estimates, or credit notes. Use when adding a sellable item with its costs, codes, and stock/serial/batch options. Requires the company and a product name; buy/sales tax IDs are supplied as JSON arrays. Not idempotent \u2014 each call creates a new product even with the same name.",
    idempotent: !1
  },
  props: {
    companyId: companyIdProp,
    productName: import_pieces_framework8.Property.ShortText({
      displayName: "Product Name",
      description: "Name of product",
      required: !0
    }),
    productCode: import_pieces_framework8.Property.ShortText({
      displayName: "Product Code",
      description: "SKU code of product",
      required: !1
    }),
    productBuyCost: import_pieces_framework8.Property.Number({
      displayName: "Product Buy Cost",
      description: "Buy cost of product",
      required: !1
    }),
    productUnitCost: import_pieces_framework8.Property.Number({
      displayName: "Product Unit Cost",
      description: "Sell or Unit cost of product",
      required: !1
    }),
    productType: import_pieces_framework8.Property.ShortText({
      displayName: "Product Type",
      description: "Type of product like kg, piece, liter etc.",
      required: !1
    }),
    productQuantity: import_pieces_framework8.Property.Number({
      displayName: "Product Quantity",
      description: "Quantity of product",
      required: !1
    }),
    productNotes: import_pieces_framework8.Property.LongText({
      displayName: "Product Notes",
      description: "Note or Description about product",
      required: !1
    }),
    productCategory: import_pieces_framework8.Property.ShortText({
      displayName: "Product Category",
      description: "Category of product",
      required: !1
    }),
    productSelectedCurrency: import_pieces_framework8.Property.ShortText({
      displayName: "Product Currency",
      description: "Currency code like INR, USD, CAD etc.",
      required: !1
    }),
    hsnCode: import_pieces_framework8.Property.ShortText({
      displayName: "HSN Code",
      description: "HSNCode of product",
      required: !1
    }),
    productStockManage: import_pieces_framework8.Property.Checkbox({
      displayName: "Stock Manage",
      description: "Enable stock management for this product",
      required: !1
    }),
    showInMenu: import_pieces_framework8.Property.Checkbox({
      displayName: "Show In Menu",
      description: "Show product in POS menu",
      required: !1
    }),
    productSerialized: import_pieces_framework8.Property.Checkbox({
      displayName: "Product Serialized",
      description: "Enable serial number tracking for each unit",
      required: !1
    }),
    productBatched: import_pieces_framework8.Property.Checkbox({
      displayName: "Product Batched",
      description: "Enable batch management for this product",
      required: !1
    }),
    productDietType: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Product Diet Type",
      description: "Dietary classification of the product",
      required: !1,
      options: {
        options: [
          { label: "Veg", value: "1" },
          { label: "Non Veg", value: "2" },
          { label: "None", value: "3" },
          { label: "Egg", value: "4" }
        ]
      }
    }),
    productBuyTaxJson: import_pieces_framework8.Property.LongText({
      displayName: "Product Buy Tax (JSON)",
      description: 'Array of tax IDs applied on product buy. Example: ["95A1956E-E60A-4FAC-8EC4-C88EE7431280"]',
      required: !1
    }),
    productSalesTaxJson: import_pieces_framework8.Property.LongText({
      displayName: "Product Sales Tax (JSON)",
      description: 'Array of tax IDs applied on product sales. Example: ["95A1956E-E60A-4FAC-8EC4-C88EE7431280"]',
      required: !1
    })
  },
  async run(context) {
    let {
      companyId,
      productName,
      productCode,
      productBuyCost,
      productUnitCost,
      productType,
      productQuantity,
      productNotes,
      productCategory,
      productSelectedCurrency,
      hsnCode,
      productStockManage,
      showInMenu,
      productSerialized,
      productBatched,
      productDietType,
      productBuyTaxJson,
      productSalesTaxJson
    } = context.propsValue, body = {
      CompanyID: companyId,
      ProductName: productName
    };
    if (productCode && (body.ProductCode = productCode), productBuyCost != null && (body.ProductBuyCost = productBuyCost), productUnitCost != null && (body.ProductUnitCost = productUnitCost), productType && (body.ProductType = productType), productQuantity != null && (body.ProductQuantity = productQuantity), productNotes && (body.ProductNotes = productNotes), productCategory && (body.ProductCategory = productCategory), productSelectedCurrency && (body.ProductSelectedCurrency = productSelectedCurrency), hsnCode && (body.HSNCode = hsnCode), productStockManage !== void 0 && (body.ProductStockManage = productStockManage ? 1 : 0), showInMenu !== void 0 && (body.ShowInMenu = showInMenu ? 1 : 0), productSerialized !== void 0 && (body.ProductSerialized = productSerialized ? 1 : 0), productBatched !== void 0 && (body.ProductBatched = productBatched ? 1 : 0), productDietType && (body.ProductDietType = parseInt(productDietType)), productBuyTaxJson)
      try {
        body.ProductBuyTax = JSON.parse(productBuyTaxJson);
      } catch (error) {
        throw new Error("Invalid ProductBuyTax JSON format");
      }
    if (productSalesTaxJson)
      try {
        body.ProductSalesTax = JSON.parse(productSalesTaxJson);
      } catch (error) {
        throw new Error("Invalid ProductSalesTax JSON format");
      }
    let accessToken = await getAccessToken(
      context.auth.props.email,
      context.auth.props.secret_text
    );
    return await makeRequest(
      accessToken,
      import_pieces_common8.HttpMethod.POST,
      "/add_product",
      body
    );
  }
});

// ../ap/packages/pieces/community/mooninvoice/src/lib/actions/create-task.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common");
var createTask = (0, import_pieces_framework9.createAction)({
  auth: mooninvoiceAuth,
  name: "createTask",
  displayName: "Create Task",
  description: "Create a new task in MoonInvoice",
  audience: "both",
  aiMetadata: {
    description: "Creates a new billable task (time/service line item) under a project within a MoonInvoice company that can later be added to invoices or estimates. Use when defining work to bill by hours and rate. Requires the company, a project, and a task name; tax IDs are supplied as a JSON array. Not idempotent \u2014 each call creates a new task even with the same name.",
    idempotent: !1
  },
  props: {
    companyId: companyIdProp,
    taskName: import_pieces_framework9.Property.ShortText({
      displayName: "Task Name",
      description: "Name of Task",
      required: !0
    }),
    projectId: projectIdProp,
    hours: import_pieces_framework9.Property.Number({
      displayName: "Hours",
      description: "Task hours",
      required: !1
    }),
    notes: import_pieces_framework9.Property.LongText({
      displayName: "Notes",
      description: "Task notes",
      required: !1
    }),
    rate: import_pieces_framework9.Property.Number({
      displayName: "Rate",
      description: "Rate of task",
      required: !1
    }),
    taskType: import_pieces_framework9.Property.ShortText({
      displayName: "Task Type",
      description: "Type of task like Hours, Minutes etc.",
      required: !1
    }),
    sacCode: import_pieces_framework9.Property.ShortText({
      displayName: "SAC Code",
      description: "SACCode of task",
      required: !1
    }),
    taskTaxJson: import_pieces_framework9.Property.LongText({
      displayName: "Task Tax (JSON)",
      description: 'Array of TaxID for task. Example: ["95A1956E-E60A-4FAC-8EC4-C88EE7431280", "CBC0A8DD-EDF5-47B2-B8DB-E7BA953E2C86"]',
      required: !1
    })
  },
  async run(context) {
    let {
      companyId,
      taskName,
      projectId,
      hours,
      notes,
      rate,
      taskType,
      sacCode,
      taskTaxJson
    } = context.propsValue, body = {
      CompanyID: companyId,
      TaskName: taskName,
      ProjectID: projectId
    };
    if (hours != null && (body.Hours = hours), notes && (body.Notes = notes), rate != null && (body.Rate = rate), taskType && (body.TaskType = taskType), sacCode && (body.SACCode = sacCode), taskTaxJson)
      try {
        body.TaskTax = JSON.parse(taskTaxJson);
      } catch (error) {
        throw new Error("Invalid TaskTax JSON format");
      }
    let accessToken = await getAccessToken(
      context.auth.props.email,
      context.auth.props.secret_text
    );
    return await makeRequest(
      accessToken,
      import_pieces_common9.HttpMethod.POST,
      "/add_task",
      body
    );
  }
});

// ../ap/packages/pieces/community/mooninvoice/src/index.ts
var mooninvoice = (0, import_pieces_framework10.createPiece)({
  displayName: "Moon Invoice",
  auth: mooninvoiceAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/mooninvoice.png",
  authors: ["sanket-a11y"],
  actions: [
    addNewContact,
    createCreditNote,
    createEstimate,
    createExpense,
    createInvoice,
    createProduct,
    createTask
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mooninvoice
});
