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

// ../ap/packages/pieces/community/lofty/src/index.ts
var index_exports = {};
__export(index_exports, {
  lofty: () => lofty
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lofty/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), loftyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "API Key for Lofty",
  required: !0
});

// ../ap/packages/pieces/community/lofty/src/lib/actions/create-lead.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lofty/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.lofty.com/v1.0";
async function makeRequest(apiKey, method, path, body) {
  var _a;
  try {
    let headers = {
      Authorization: `token ${apiKey}`,
      "Content-Type": "application/json"
    };
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers,
      body
    })).body;
  } catch (error) {
    throw new Error(
      `MagicalAPI request failed: ${JSON.stringify(
        ((_a = error.response) == null ? void 0 : _a.body) || error.message || error
      )}`
    );
  }
}

// ../ap/packages/pieces/community/lofty/src/lib/actions/create-lead.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), createLead = (0, import_pieces_framework2.createAction)({
  auth: loftyAuth,
  name: "createLead",
  displayName: "Create Lead",
  description: "Create a lead in Lofty",
  audience: "both",
  aiMetadata: {
    description: "Creates a new lead (contact) in the Lofty real-estate CRM with the given name, email(s), and optional details such as phone, address, lead type, source, buyer/seller property criteria, and communication opt-ins. Use when adding a person to Lofty. First name and at least one email are required. Not idempotent: each call creates a new lead, so repeated calls produce duplicates.",
    idempotent: !1
  },
  props: {
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First name",
      required: !0
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last name",
      required: !1
    }),
    emails: import_pieces_framework2.Property.Array({
      displayName: "Email",
      description: "Lead email address(s)",
      required: !0
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    birthday: import_pieces_framework2.Property.ShortText({
      displayName: "Birthday",
      description: "YYYY-MM-DD",
      required: !1
    }),
    mailingAddress: import_pieces_framework2.Property.ShortText({
      displayName: "Street or Complete Address (Mailing Address)",
      required: !1
    }),
    mailingCity: import_pieces_framework2.Property.ShortText({
      displayName: "City (Mailing Address)",
      required: !1
    }),
    mailingState: import_pieces_framework2.Property.ShortText({
      displayName: "State (Mailing Address)",
      required: !1
    }),
    mailingZipcode: import_pieces_framework2.Property.ShortText({
      displayName: "Zip Code (Mailing Address)",
      required: !1
    }),
    leadType: import_pieces_framework2.Property.ShortText({
      displayName: "Lead Type",
      required: !1
    }),
    leadOwnershipLevel: import_pieces_framework2.Property.ShortText({
      displayName: "Lead Ownership Level",
      required: !1
    }),
    source: import_pieces_framework2.Property.ShortText({
      displayName: "Source",
      required: !1
    }),
    segment: import_pieces_framework2.Property.ShortText({
      displayName: "Segment",
      required: !1
    }),
    tags: import_pieces_framework2.Property.Array({
      displayName: "Tag(s)",
      required: !1
    }),
    note: import_pieces_framework2.Property.LongText({
      displayName: "Note",
      required: !1
    }),
    subscriptionSmartPlans: import_pieces_framework2.Property.Checkbox({
      displayName: "Subscription - Smart Plans",
      required: !1
    }),
    subscriptionPropertyAlert: import_pieces_framework2.Property.Checkbox({
      displayName: "Subscription - Property Alert",
      required: !1
    }),
    subscriptionMarketReport: import_pieces_framework2.Property.Checkbox({
      displayName: "Subscription - Market Report",
      required: !1
    }),
    minPriceBuyer: import_pieces_framework2.Property.Number({
      displayName: "Min Price (Buyer)",
      required: !1
    }),
    maxPriceBuyer: import_pieces_framework2.Property.Number({
      displayName: "Max Price (Buyer)",
      required: !1
    }),
    locationDeprecated: import_pieces_framework2.Property.ShortText({
      displayName: "Location (Deprecated)",
      required: !1
    }),
    buyerCity: import_pieces_framework2.Property.ShortText({
      displayName: "City (Buyer)",
      required: !1
    }),
    buyerState: import_pieces_framework2.Property.ShortText({
      displayName: "State (Buyer)",
      required: !1
    }),
    buyerZipcode: import_pieces_framework2.Property.ShortText({
      displayName: "Zipcode (Buyer)",
      required: !1
    }),
    buyerAddress: import_pieces_framework2.Property.ShortText({
      displayName: "Address (Buyer)",
      required: !1
    }),
    minBedsBuyer: import_pieces_framework2.Property.Number({
      displayName: "Min Beds (Buyer)",
      required: !1
    }),
    minBathsBuyer: import_pieces_framework2.Property.Number({
      displayName: "Min Baths (Buyer)",
      required: !1
    }),
    bedsSeller: import_pieces_framework2.Property.Number({
      displayName: "Beds (Seller)",
      required: !1
    }),
    bathsSeller: import_pieces_framework2.Property.Number({
      displayName: "Baths (Seller)",
      required: !1
    }),
    priceSeller: import_pieces_framework2.Property.Number({
      displayName: "Price (Seller)",
      required: !1
    }),
    addressSeller: import_pieces_framework2.Property.ShortText({
      displayName: "Address (Seller)",
      required: !1
    }),
    sqftSeller: import_pieces_framework2.Property.Number({
      displayName: "SqFt (Seller)",
      required: !1
    }),
    citySeller: import_pieces_framework2.Property.ShortText({
      displayName: "City (Seller)",
      required: !1
    }),
    stateSeller: import_pieces_framework2.Property.ShortText({
      displayName: "State (Seller)",
      required: !1
    }),
    zipcodeSeller: import_pieces_framework2.Property.ShortText({
      displayName: "Zipcode (Seller)",
      required: !1
    }),
    sendWelcomeEmail: import_pieces_framework2.Property.Checkbox({
      displayName: "Send Welcome Email",
      required: !1
    }),
    callOptIn: import_pieces_framework2.Property.Checkbox({
      displayName: "Call Opt In",
      required: !1
    }),
    textOptIn: import_pieces_framework2.Property.Checkbox({
      displayName: "Text Opt In",
      required: !1
    }),
    emailOptIn: import_pieces_framework2.Property.Checkbox({
      displayName: "Email Opt In",
      required: !1
    }),
    numberConsent: import_pieces_framework2.Property.Checkbox({
      displayName: "Number Consent",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let payload = {
      emails: propsValue.emails
    };
    return propsValue.firstName && (payload.first_name = propsValue.firstName), propsValue.lastName && (payload.last_name = propsValue.lastName), propsValue.phone && (payload.phone = propsValue.phone), propsValue.source && (payload.source = propsValue.source), propsValue.birthday && (payload.birthday = propsValue.birthday), propsValue.mailingAddress && (payload.mailing_address = propsValue.mailingAddress), propsValue.mailingCity && (payload.mailing_city = propsValue.mailingCity), propsValue.mailingState && (payload.mailing_state = propsValue.mailingState), propsValue.mailingZipcode && (payload.mailing_zipcode = propsValue.mailingZipcode), propsValue.leadType && (payload.lead_type = propsValue.leadType), propsValue.leadOwnershipLevel && (payload.lead_ownership_level = propsValue.leadOwnershipLevel), propsValue.segment && (payload.segment = propsValue.segment), propsValue.tags && (payload.tags = propsValue.tags), propsValue.note && (payload.note = propsValue.note), propsValue.subscriptionSmartPlans !== void 0 && (payload.subscription_smart_plans = propsValue.subscriptionSmartPlans), propsValue.subscriptionPropertyAlert !== void 0 && (payload.subscription_property_alert = propsValue.subscriptionPropertyAlert), propsValue.subscriptionMarketReport !== void 0 && (payload.subscription_market_report = propsValue.subscriptionMarketReport), propsValue.minPriceBuyer !== void 0 && (payload.min_price_buyer = propsValue.minPriceBuyer), propsValue.maxPriceBuyer !== void 0 && (payload.max_price_buyer = propsValue.maxPriceBuyer), propsValue.locationDeprecated && (payload.location_deprecated = propsValue.locationDeprecated), propsValue.buyerCity && (payload.buyer_city = propsValue.buyerCity), propsValue.buyerState && (payload.buyer_state = propsValue.buyerState), propsValue.buyerZipcode && (payload.buyer_zipcode = propsValue.buyerZipcode), propsValue.buyerAddress && (payload.buyer_address = propsValue.buyerAddress), propsValue.minBedsBuyer !== void 0 && (payload.min_beds_buyer = propsValue.minBedsBuyer), propsValue.minBathsBuyer !== void 0 && (payload.min_baths_buyer = propsValue.minBathsBuyer), propsValue.bedsSeller !== void 0 && (payload.beds_seller = propsValue.bedsSeller), propsValue.bathsSeller !== void 0 && (payload.baths_seller = propsValue.bathsSeller), propsValue.priceSeller !== void 0 && (payload.price_seller = propsValue.priceSeller), propsValue.addressSeller && (payload.address_seller = propsValue.addressSeller), propsValue.sqftSeller !== void 0 && (payload.sqft_seller = propsValue.sqftSeller), propsValue.citySeller && (payload.city_seller = propsValue.citySeller), propsValue.stateSeller && (payload.state_seller = propsValue.stateSeller), propsValue.zipcodeSeller && (payload.zipcode_seller = propsValue.zipcodeSeller), propsValue.sendWelcomeEmail !== void 0 && (payload.send_welcome_email = propsValue.sendWelcomeEmail), propsValue.callOptIn !== void 0 && (payload.call_opt_in = propsValue.callOptIn), propsValue.textOptIn !== void 0 && (payload.text_opt_in = propsValue.textOptIn), propsValue.emailOptIn !== void 0 && (payload.email_opt_in = propsValue.emailOptIn), propsValue.numberConsent !== void 0 && (payload.number_consent = propsValue.numberConsent), await makeRequest(
      auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/leads",
      payload
    );
  }
});

// ../ap/packages/pieces/community/lofty/src/index.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lofty/src/lib/actions/create-transaction.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/lofty/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), leadIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: loftyAuth,
  displayName: "Lead",
  description: "Select a lead",
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
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/leads"
        )).leads.map((lead) => ({
          label: `${lead.firstName} ${lead.lastName}`,
          value: lead.leadId
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: []
      };
    }
  }
}), transactionIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: loftyAuth,
  displayName: "Transaction",
  description: "Select a transaction",
  required: !0,
  refreshers: ["leadId"],
  options: async ({ auth, leadId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: []
      };
    if (!leadId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Select a lead first"
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          `/leads/${leadId}/transactions`
        )).transactions.map((transaction) => ({
          label: `${transaction.leadName} - ${transaction.transactionName}`,
          value: transaction.transactionId
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

// ../ap/packages/pieces/community/lofty/src/lib/actions/create-transaction.ts
var createTransaction = (0, import_pieces_framework4.createAction)({
  auth: loftyAuth,
  name: "createTransaction",
  displayName: "Create Transaction",
  description: "Create a transaction for a Lofty lead",
  audience: "both",
  aiMetadata: {
    description: "Records a new transaction (e.g. a sale or rental deal with amount, property details, dates, and agent) against an existing Lofty lead. Use when logging a deal for a known lead; requires the lead id (selectable from the account or passed directly). Not idempotent: each call appends another transaction, so repeated calls create duplicates.",
    idempotent: !1
  },
  props: {
    leadId: leadIdDropdown,
    transactionDate: import_pieces_framework4.Property.ShortText({
      displayName: "Transaction Date",
      description: "YYYY-MM-DD",
      required: !1
    }),
    amount: import_pieces_framework4.Property.Number({
      displayName: "Amount",
      required: !1
    }),
    transactionType: import_pieces_framework4.Property.ShortText({
      displayName: "Transaction Type",
      description: "e.g., sale, rent",
      required: !1
    }),
    propertyAddress: import_pieces_framework4.Property.ShortText({
      displayName: "Property Address",
      required: !1
    }),
    propertyCity: import_pieces_framework4.Property.ShortText({
      displayName: "Property City",
      required: !1
    }),
    propertyState: import_pieces_framework4.Property.ShortText({
      displayName: "Property State",
      required: !1
    }),
    propertyZipcode: import_pieces_framework4.Property.ShortText({
      displayName: "Property Zipcode",
      required: !1
    }),
    beds: import_pieces_framework4.Property.Number({
      displayName: "Beds",
      required: !1
    }),
    baths: import_pieces_framework4.Property.Number({
      displayName: "Baths",
      required: !1
    }),
    sqft: import_pieces_framework4.Property.Number({
      displayName: "SqFt",
      required: !1
    }),
    mlsNumber: import_pieces_framework4.Property.ShortText({
      displayName: "MLS Number",
      required: !1
    }),
    closingDate: import_pieces_framework4.Property.ShortText({
      displayName: "Closing Date",
      description: "YYYY-MM-DD",
      required: !1
    }),
    agentId: import_pieces_framework4.Property.ShortText({
      displayName: "Agent ID",
      required: !1
    }),
    agentName: import_pieces_framework4.Property.ShortText({
      displayName: "Agent Name",
      required: !1
    }),
    agentEmail: import_pieces_framework4.Property.ShortText({
      displayName: "Agent Email",
      required: !1
    }),
    source: import_pieces_framework4.Property.ShortText({
      displayName: "Source",
      required: !1
    }),
    note: import_pieces_framework4.Property.LongText({
      displayName: "Note",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { leadId } = propsValue, payload = {};
    return propsValue.transactionDate && (payload.transaction_date = propsValue.transactionDate), propsValue.amount !== void 0 && (payload.amount = propsValue.amount), propsValue.transactionType && (payload.transaction_type = propsValue.transactionType), propsValue.propertyAddress && (payload.property_address = propsValue.propertyAddress), propsValue.propertyCity && (payload.property_city = propsValue.propertyCity), propsValue.propertyState && (payload.property_state = propsValue.propertyState), propsValue.propertyZipcode && (payload.property_zipcode = propsValue.propertyZipcode), propsValue.beds !== void 0 && (payload.beds = propsValue.beds), propsValue.baths !== void 0 && (payload.baths = propsValue.baths), propsValue.sqft !== void 0 && (payload.sqft = propsValue.sqft), propsValue.mlsNumber && (payload.mls_number = propsValue.mlsNumber), propsValue.closingDate && (payload.closing_date = propsValue.closingDate), propsValue.agentId && (payload.agent_id = propsValue.agentId), propsValue.agentName && (payload.agent_name = propsValue.agentName), propsValue.agentEmail && (payload.agent_email = propsValue.agentEmail), propsValue.source && (payload.source = propsValue.source), propsValue.note && (payload.note = propsValue.note), await makeRequest(
      auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      `/leads/${leadId}/transaction`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/lofty/src/lib/actions/update-lead.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");
var updateLead = (0, import_pieces_framework5.createAction)({
  auth: loftyAuth,
  name: "updateLead",
  displayName: "Update Lead",
  description: "Update a lead in Lofty",
  audience: "both",
  aiMetadata: {
    description: "Updates fields on an existing Lofty lead identified by lead id, sending only the fields you provide (name, emails, phone, address, lead type, source, buyer/seller criteria, opt-ins, etc.). Use to modify a known lead rather than create one. Idempotent: applying the same field values repeatedly leaves the lead in the same state.",
    idempotent: !0
  },
  props: {
    leadId: leadIdDropdown,
    firstName: import_pieces_framework5.Property.ShortText({
      displayName: "First name",
      required: !1
    }),
    lastName: import_pieces_framework5.Property.ShortText({
      displayName: "Last name",
      required: !1
    }),
    emails: import_pieces_framework5.Property.Array({
      displayName: "Email",
      description: "Lead email address(s)",
      required: !1
    }),
    phone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    birthday: import_pieces_framework5.Property.ShortText({
      displayName: "Birthday",
      description: "YYYY-MM-DD",
      required: !1
    }),
    mailingAddress: import_pieces_framework5.Property.ShortText({
      displayName: "Street or Complete Address (Mailing Address)",
      required: !1
    }),
    mailingCity: import_pieces_framework5.Property.ShortText({
      displayName: "City (Mailing Address)",
      required: !1
    }),
    mailingState: import_pieces_framework5.Property.ShortText({
      displayName: "State (Mailing Address)",
      required: !1
    }),
    mailingZipcode: import_pieces_framework5.Property.ShortText({
      displayName: "Zip Code (Mailing Address)",
      required: !1
    }),
    leadType: import_pieces_framework5.Property.ShortText({
      displayName: "Lead Type",
      required: !1
    }),
    leadOwnershipLevel: import_pieces_framework5.Property.ShortText({
      displayName: "Lead Ownership Level",
      required: !1
    }),
    source: import_pieces_framework5.Property.ShortText({
      displayName: "Source",
      required: !1
    }),
    segment: import_pieces_framework5.Property.ShortText({
      displayName: "Segment",
      required: !1
    }),
    tags: import_pieces_framework5.Property.Array({
      displayName: "Tag(s)",
      required: !1
    }),
    note: import_pieces_framework5.Property.LongText({
      displayName: "Note",
      required: !1
    }),
    subscriptionSmartPlans: import_pieces_framework5.Property.Checkbox({
      displayName: "Subscription - Smart Plans",
      required: !1
    }),
    subscriptionPropertyAlert: import_pieces_framework5.Property.Checkbox({
      displayName: "Subscription - Property Alert",
      required: !1
    }),
    subscriptionMarketReport: import_pieces_framework5.Property.Checkbox({
      displayName: "Subscription - Market Report",
      required: !1
    }),
    minPriceBuyer: import_pieces_framework5.Property.Number({
      displayName: "Min Price (Buyer)",
      required: !1
    }),
    maxPriceBuyer: import_pieces_framework5.Property.Number({
      displayName: "Max Price (Buyer)",
      required: !1
    }),
    locationDeprecated: import_pieces_framework5.Property.ShortText({
      displayName: "Location (Deprecated)",
      required: !1
    }),
    buyerCity: import_pieces_framework5.Property.ShortText({
      displayName: "City (Buyer)",
      required: !1
    }),
    buyerState: import_pieces_framework5.Property.ShortText({
      displayName: "State (Buyer)",
      required: !1
    }),
    buyerZipcode: import_pieces_framework5.Property.ShortText({
      displayName: "Zipcode (Buyer)",
      required: !1
    }),
    buyerAddress: import_pieces_framework5.Property.ShortText({
      displayName: "Address (Buyer)",
      required: !1
    }),
    minBedsBuyer: import_pieces_framework5.Property.Number({
      displayName: "Min Beds (Buyer)",
      required: !1
    }),
    minBathsBuyer: import_pieces_framework5.Property.Number({
      displayName: "Min Baths (Buyer)",
      required: !1
    }),
    bedsSeller: import_pieces_framework5.Property.Number({
      displayName: "Beds (Seller)",
      required: !1
    }),
    bathsSeller: import_pieces_framework5.Property.Number({
      displayName: "Baths (Seller)",
      required: !1
    }),
    priceSeller: import_pieces_framework5.Property.Number({
      displayName: "Price (Seller)",
      required: !1
    }),
    addressSeller: import_pieces_framework5.Property.ShortText({
      displayName: "Address (Seller)",
      required: !1
    }),
    sqftSeller: import_pieces_framework5.Property.Number({
      displayName: "SqFt (Seller)",
      required: !1
    }),
    citySeller: import_pieces_framework5.Property.ShortText({
      displayName: "City (Seller)",
      required: !1
    }),
    stateSeller: import_pieces_framework5.Property.ShortText({
      displayName: "State (Seller)",
      required: !1
    }),
    zipcodeSeller: import_pieces_framework5.Property.ShortText({
      displayName: "Zipcode (Seller)",
      required: !1
    }),
    sendWelcomeEmail: import_pieces_framework5.Property.Checkbox({
      displayName: "Send Welcome Email",
      required: !1
    }),
    callOptIn: import_pieces_framework5.Property.Checkbox({
      displayName: "Call Opt In",
      required: !1
    }),
    textOptIn: import_pieces_framework5.Property.Checkbox({
      displayName: "Text Opt In",
      required: !1
    }),
    emailOptIn: import_pieces_framework5.Property.Checkbox({
      displayName: "Email Opt In",
      required: !1
    }),
    numberConsent: import_pieces_framework5.Property.Checkbox({
      displayName: "Number Consent",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { leadId } = propsValue, payload = {};
    return propsValue.firstName && (payload.first_name = propsValue.firstName), propsValue.lastName && (payload.last_name = propsValue.lastName), propsValue.emails && (payload.emails = propsValue.emails), propsValue.phone && (payload.phone = propsValue.phone), propsValue.source && (payload.source = propsValue.source), propsValue.birthday && (payload.birthday = propsValue.birthday), propsValue.mailingAddress && (payload.mailing_address = propsValue.mailingAddress), propsValue.mailingCity && (payload.mailing_city = propsValue.mailingCity), propsValue.mailingState && (payload.mailing_state = propsValue.mailingState), propsValue.mailingZipcode && (payload.mailing_zipcode = propsValue.mailingZipcode), propsValue.leadType && (payload.lead_type = propsValue.leadType), propsValue.leadOwnershipLevel && (payload.lead_ownership_level = propsValue.leadOwnershipLevel), propsValue.segment && (payload.segment = propsValue.segment), propsValue.tags && (payload.tags = propsValue.tags), propsValue.note && (payload.note = propsValue.note), propsValue.subscriptionSmartPlans !== void 0 && (payload.subscription_smart_plans = propsValue.subscriptionSmartPlans), propsValue.subscriptionPropertyAlert !== void 0 && (payload.subscription_property_alert = propsValue.subscriptionPropertyAlert), propsValue.subscriptionMarketReport !== void 0 && (payload.subscription_market_report = propsValue.subscriptionMarketReport), propsValue.minPriceBuyer !== void 0 && (payload.min_price_buyer = propsValue.minPriceBuyer), propsValue.maxPriceBuyer !== void 0 && (payload.max_price_buyer = propsValue.maxPriceBuyer), propsValue.locationDeprecated && (payload.location_deprecated = propsValue.locationDeprecated), propsValue.buyerCity && (payload.buyer_city = propsValue.buyerCity), propsValue.buyerState && (payload.buyer_state = propsValue.buyerState), propsValue.buyerZipcode && (payload.buyer_zipcode = propsValue.buyerZipcode), propsValue.buyerAddress && (payload.buyer_address = propsValue.buyerAddress), propsValue.minBedsBuyer !== void 0 && (payload.min_beds_buyer = propsValue.minBedsBuyer), propsValue.minBathsBuyer !== void 0 && (payload.min_baths_buyer = propsValue.minBathsBuyer), propsValue.bedsSeller !== void 0 && (payload.beds_seller = propsValue.bedsSeller), propsValue.bathsSeller !== void 0 && (payload.baths_seller = propsValue.bathsSeller), propsValue.priceSeller !== void 0 && (payload.price_seller = propsValue.priceSeller), propsValue.addressSeller && (payload.address_seller = propsValue.addressSeller), propsValue.sqftSeller !== void 0 && (payload.sqft_seller = propsValue.sqftSeller), propsValue.citySeller && (payload.city_seller = propsValue.citySeller), propsValue.stateSeller && (payload.state_seller = propsValue.stateSeller), propsValue.zipcodeSeller && (payload.zipcode_seller = propsValue.zipcodeSeller), propsValue.sendWelcomeEmail !== void 0 && (payload.send_welcome_email = propsValue.sendWelcomeEmail), propsValue.callOptIn !== void 0 && (payload.call_opt_in = propsValue.callOptIn), propsValue.textOptIn !== void 0 && (payload.text_opt_in = propsValue.textOptIn), propsValue.emailOptIn !== void 0 && (payload.email_opt_in = propsValue.emailOptIn), propsValue.numberConsent !== void 0 && (payload.number_consent = propsValue.numberConsent), await makeRequest(
      auth.secret_text,
      import_pieces_common5.HttpMethod.PUT,
      `/leads/${leadId}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/lofty/src/lib/actions/update-transaction.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var updateTransaction = (0, import_pieces_framework6.createAction)({
  auth: loftyAuth,
  name: "updateTransaction",
  displayName: "Update Transaction",
  description: "Update a transaction for a Lofty lead",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing transaction on a Lofty lead, identified by both the lead id and transaction id, sending only the provided fields (amount, property details, dates, agent, source, note). Use to amend a known deal rather than create one. Idempotent: applying the same field values repeatedly leaves the transaction in the same state.",
    idempotent: !0
  },
  props: {
    leadId: leadIdDropdown,
    transactionId: transactionIdDropdown,
    transactionDate: import_pieces_framework6.Property.ShortText({
      displayName: "Transaction Date",
      description: "YYYY-MM-DD",
      required: !1
    }),
    amount: import_pieces_framework6.Property.Number({
      displayName: "Amount",
      required: !1
    }),
    transactionType: import_pieces_framework6.Property.ShortText({
      displayName: "Transaction Type",
      description: "e.g., sale, rent",
      required: !1
    }),
    propertyAddress: import_pieces_framework6.Property.ShortText({
      displayName: "Property Address",
      required: !1
    }),
    propertyCity: import_pieces_framework6.Property.ShortText({
      displayName: "Property City",
      required: !1
    }),
    propertyState: import_pieces_framework6.Property.ShortText({
      displayName: "Property State",
      required: !1
    }),
    propertyZipcode: import_pieces_framework6.Property.ShortText({
      displayName: "Property Zipcode",
      required: !1
    }),
    beds: import_pieces_framework6.Property.Number({
      displayName: "Beds",
      required: !1
    }),
    baths: import_pieces_framework6.Property.Number({
      displayName: "Baths",
      required: !1
    }),
    sqft: import_pieces_framework6.Property.Number({
      displayName: "SqFt",
      required: !1
    }),
    mlsNumber: import_pieces_framework6.Property.ShortText({
      displayName: "MLS Number",
      required: !1
    }),
    closingDate: import_pieces_framework6.Property.ShortText({
      displayName: "Closing Date",
      description: "YYYY-MM-DD",
      required: !1
    }),
    agentId: import_pieces_framework6.Property.ShortText({
      displayName: "Agent ID",
      required: !1
    }),
    agentName: import_pieces_framework6.Property.ShortText({
      displayName: "Agent Name",
      required: !1
    }),
    agentEmail: import_pieces_framework6.Property.ShortText({
      displayName: "Agent Email",
      required: !1
    }),
    source: import_pieces_framework6.Property.ShortText({
      displayName: "Source",
      required: !1
    }),
    note: import_pieces_framework6.Property.LongText({
      displayName: "Note",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { leadId, transactionId } = propsValue, payload = {};
    return propsValue.transactionDate && (payload.transaction_date = propsValue.transactionDate), propsValue.amount !== void 0 && (payload.amount = propsValue.amount), propsValue.transactionType && (payload.transaction_type = propsValue.transactionType), propsValue.propertyAddress && (payload.property_address = propsValue.propertyAddress), propsValue.propertyCity && (payload.property_city = propsValue.propertyCity), propsValue.propertyState && (payload.property_state = propsValue.propertyState), propsValue.propertyZipcode && (payload.property_zipcode = propsValue.propertyZipcode), propsValue.beds !== void 0 && (payload.beds = propsValue.beds), propsValue.baths !== void 0 && (payload.baths = propsValue.baths), propsValue.sqft !== void 0 && (payload.sqft = propsValue.sqft), propsValue.mlsNumber && (payload.mls_number = propsValue.mlsNumber), propsValue.closingDate && (payload.closing_date = propsValue.closingDate), propsValue.agentId && (payload.agent_id = propsValue.agentId), propsValue.agentName && (payload.agent_name = propsValue.agentName), propsValue.agentEmail && (payload.agent_email = propsValue.agentEmail), propsValue.source && (payload.source = propsValue.source), propsValue.note && (payload.note = propsValue.note), await makeRequest(
      auth.secret_text,
      import_pieces_common6.HttpMethod.PUT,
      `/leads/${leadId}/transaction/${transactionId}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/lofty/src/index.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), lofty = (0, import_pieces_framework7.createPiece)({
  displayName: "Lofty",
  auth: loftyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/lofty.png",
  categories: [import_pieces_framework8.PieceCategory.SALES_AND_CRM],
  description: "Lofty is a product of the technology company Lofty Inc, which offers a complete tech solution for real estate agents.",
  authors: ["sanket-a11y"],
  actions: [
    createLead,
    createTransaction,
    updateLead,
    updateTransaction,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: loftyAuth,
      baseUrl: () => "https://api.lofty.ai/v1",
      authMapping: async (auth) => ({
        Authorization: `token ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  lofty
});
