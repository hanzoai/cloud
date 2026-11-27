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

// ../ap/packages/pieces/community/insightly/src/index.ts
var index_exports = {};
__export(index_exports, {
  insightly: () => insightly
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/insightly/src/lib/common/common.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), insightlyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Enter your Insightly API key. You can find this in your Insightly account under User Settings > API Keys.",
  required: !0
});
async function makeInsightlyRequest(auth, endpoint, pod = "na1", method = import_pieces_common.HttpMethod.GET, body) {
  let url = `${`https://api.${pod}.insightly.com/v3.1`}${endpoint}`, requestConfig = {
    method,
    url,
    authentication: {
      type: import_pieces_common.AuthenticationType.BASIC,
      username: auth.secret_text,
      password: ""
    }
  };
  return body && (method === import_pieces_common.HttpMethod.POST || method === import_pieces_common.HttpMethod.PUT) && (requestConfig.headers = {
    "Content-Type": "application/json"
  }, requestConfig.body = body), await import_pieces_common.httpClient.sendRequest(requestConfig);
}

// ../ap/packages/pieces/community/insightly/src/lib/actions/create-record.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/insightly/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var NameFields = {
  SALUTATION: import_pieces_framework2.Property.ShortText({
    displayName: "Salutation",
    required: !1
  }),
  FIRST_NAME: import_pieces_framework2.Property.ShortText({
    displayName: "First Name",
    required: !1
  }),
  LAST_NAME: import_pieces_framework2.Property.ShortText({
    displayName: "Last Name",
    required: !1
  }),
  TITLE: import_pieces_framework2.Property.ShortText({
    displayName: "Title",
    required: !1
  })
}, ContactInfoFields = {
  PHONE: import_pieces_framework2.Property.ShortText({
    displayName: "Phone",
    required: !1
  }),
  PHONE_HOME: import_pieces_framework2.Property.ShortText({
    displayName: "Home Phone",
    required: !1
  }),
  PHONE_MOBILE: import_pieces_framework2.Property.ShortText({
    displayName: "Mobile Phone",
    required: !1
  }),
  PHONE_OTHER: import_pieces_framework2.Property.ShortText({
    displayName: "Other Phone",
    required: !1
  }),
  PHONE_ASSISTANT: import_pieces_framework2.Property.ShortText({
    displayName: "Assistant Phone",
    required: !1
  }),
  PHONE_FAX: import_pieces_framework2.Property.ShortText({
    displayName: "Fax",
    required: !1
  }),
  EMAIL_ADDRESS: import_pieces_framework2.Property.ShortText({
    displayName: "Email Address",
    required: !1
  })
}, AddressFields = (type) => ({
  [`ADDRESS_${type.toUpperCase()}_STREET`]: import_pieces_framework2.Property.ShortText({
    displayName: `${type} Street`,
    required: !1
  }),
  [`ADDRESS_${type.toUpperCase()}_CITY`]: import_pieces_framework2.Property.ShortText({
    displayName: `${type} City`,
    required: !1
  }),
  [`ADDRESS_${type.toUpperCase()}_STATE`]: import_pieces_framework2.Property.ShortText({
    displayName: `${type} State`,
    required: !1
  }),
  [`ADDRESS_${type.toUpperCase()}_POSTCODE`]: import_pieces_framework2.Property.ShortText({
    displayName: `${type} Postcode`,
    required: !1
  }),
  [`ADDRESS_${type.toUpperCase()}_COUNTRY`]: import_pieces_framework2.Property.ShortText({
    displayName: `${type} Country`,
    required: !1
  })
}), contactFields = __spreadProps(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, NameFields), ContactInfoFields), AddressFields("Mail")), AddressFields("Other")), {
  BACKGROUND: import_pieces_framework2.Property.LongText({
    displayName: "Background",
    required: !1
  }),
  OWNER_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Owner User ID",
    required: !1
  }),
  SOCIAL_LINKEDIN: import_pieces_framework2.Property.ShortText({
    displayName: "LinkedIn",
    required: !1
  }),
  SOCIAL_FACEBOOK: import_pieces_framework2.Property.ShortText({
    displayName: "Facebook",
    required: !1
  }),
  SOCIAL_TWITTER: import_pieces_framework2.Property.ShortText({
    displayName: "Twitter",
    required: !1
  }),
  DATE_OF_BIRTH: import_pieces_framework2.Property.ShortText({
    displayName: "Date of Birth",
    description: "Format: YYYY-MM-DD",
    required: !1
  }),
  ASSISTANT_NAME: import_pieces_framework2.Property.ShortText({
    displayName: "Assistant Name",
    required: !1
  }),
  ORGANISATION_ID: import_pieces_framework2.Property.Number({
    displayName: "Organization ID",
    required: !1
  }),
  EMAIL_OPTED_OUT: import_pieces_framework2.Property.Checkbox({
    displayName: "Email Opted Out",
    required: !1
  })
}), leadFields = __spreadProps(__spreadValues({}, NameFields), {
  LEAD_SOURCE_ID: import_pieces_framework2.Property.Number({
    displayName: "Lead Source ID",
    required: !0
  }),
  LEAD_STATUS_ID: import_pieces_framework2.Property.Number({
    displayName: "Lead Status ID",
    required: !0
  }),
  EMAIL: import_pieces_framework2.Property.ShortText({
    displayName: "Email",
    required: !1
  }),
  EMPLOYEE_COUNT: import_pieces_framework2.Property.Number({
    displayName: "Employee Count",
    required: !1
  }),
  FAX: import_pieces_framework2.Property.ShortText({
    displayName: "Fax",
    required: !1
  }),
  INDUSTRY: import_pieces_framework2.Property.ShortText({
    displayName: "Industry",
    required: !1
  }),
  LEAD_DESCRIPTION: import_pieces_framework2.Property.LongText({
    displayName: "Lead Description",
    required: !1
  }),
  LEAD_RATING: import_pieces_framework2.Property.Number({
    displayName: "Lead Rating",
    required: !1
  }),
  MOBILE: import_pieces_framework2.Property.ShortText({
    displayName: "Mobile",
    required: !1
  }),
  OWNER_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Owner User ID",
    required: !1
  }),
  PHONE: import_pieces_framework2.Property.ShortText({
    displayName: "Phone",
    required: !1
  }),
  RESPONSIBLE_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Responsible User ID",
    required: !1
  }),
  WEBSITE: import_pieces_framework2.Property.ShortText({
    displayName: "Website",
    required: !1
  }),
  ADDRESS_STREET: import_pieces_framework2.Property.ShortText({
    displayName: "Street",
    required: !1
  }),
  ADDRESS_CITY: import_pieces_framework2.Property.ShortText({
    displayName: "City",
    required: !1
  }),
  ADDRESS_STATE: import_pieces_framework2.Property.ShortText({
    displayName: "State",
    required: !1
  }),
  ADDRESS_POSTCODE: import_pieces_framework2.Property.ShortText({
    displayName: "Postcode",
    required: !1
  }),
  ADDRESS_COUNTRY: import_pieces_framework2.Property.ShortText({
    displayName: "Country",
    required: !1
  }),
  ORGANISATION_NAME: import_pieces_framework2.Property.ShortText({
    displayName: "Organization Name",
    required: !1
  }),
  EMAIL_OPTED_OUT: import_pieces_framework2.Property.Checkbox({
    displayName: "Email Opted Out",
    required: !1
  })
}), opportunityFields = {
  OPPORTUNITY_NAME: import_pieces_framework2.Property.ShortText({
    displayName: "Opportunity Name",
    required: !0
  }),
  OPPORTUNITY_DETAILS: import_pieces_framework2.Property.LongText({
    displayName: "Opportunity Details",
    required: !1
  }),
  OPPORTUNITY_STATE: import_pieces_framework2.Property.StaticDropdown({
    displayName: "Opportunity State",
    required: !1,
    options: {
      options: [
        { label: "Open", value: "Open" },
        { label: "Suspended", value: "Suspended" },
        { label: "Abandoned", value: "Abandoned" },
        { label: "Lost", value: "Lost" },
        { label: "Won", value: "Won" }
      ]
    }
  }),
  RESPONSIBLE_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Responsible User ID",
    required: !1
  }),
  CATEGORY_ID: import_pieces_framework2.Property.Number({
    displayName: "Category ID",
    required: !1
  }),
  BID_CURRENCY: import_pieces_framework2.Property.ShortText({
    displayName: "Bid Currency",
    description: "3-letter currency code (e.g., USD)",
    required: !1
  }),
  BID_AMOUNT: import_pieces_framework2.Property.Number({
    displayName: "Bid Amount",
    required: !1
  }),
  BID_TYPE: import_pieces_framework2.Property.ShortText({
    displayName: "Bid Type",
    description: 'e.g., "Fixed Bid", "Per Hour"',
    required: !1
  }),
  BID_DURATION: import_pieces_framework2.Property.Number({
    displayName: "Bid Duration",
    required: !1
  }),
  PROBABILITY: import_pieces_framework2.Property.Number({
    displayName: "Probability",
    description: "Value from 0 to 100",
    required: !1
  }),
  FORECAST_CLOSE_DATE: import_pieces_framework2.Property.ShortText({
    displayName: "Forecast Close Date",
    description: "Format: YYYY-MM-DD",
    required: !1
  }),
  OWNER_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Owner User ID",
    required: !1
  }),
  ORGANISATION_ID: import_pieces_framework2.Property.Number({
    displayName: "Organization ID",
    required: !1
  }),
  PIPELINE_ID: import_pieces_framework2.Property.Number({
    displayName: "Pipeline ID",
    required: !1
  }),
  STAGE_ID: import_pieces_framework2.Property.Number({
    displayName: "Stage ID",
    required: !1
  }),
  PRICEBOOK_ID: import_pieces_framework2.Property.Number({
    displayName: "Pricebook ID",
    required: !1
  })
}, organisationFields = {
  ORGANISATION_NAME: import_pieces_framework2.Property.ShortText({
    displayName: "Organisation Name",
    required: !0
  }),
  BACKGROUND: import_pieces_framework2.Property.LongText({
    displayName: "Background",
    required: !1
  }),
  PHONE: import_pieces_framework2.Property.ShortText({
    displayName: "Phone",
    required: !1
  }),
  PHONE_FAX: import_pieces_framework2.Property.ShortText({
    displayName: "Fax",
    required: !1
  }),
  WEBSITE: import_pieces_framework2.Property.ShortText({
    displayName: "Website",
    required: !1
  }),
  ADDRESS_BILLING_STREET: import_pieces_framework2.Property.ShortText({
    displayName: "Billing Street",
    required: !1
  }),
  ADDRESS_BILLING_CITY: import_pieces_framework2.Property.ShortText({
    displayName: "Billing City",
    required: !1
  }),
  ADDRESS_BILLING_STATE: import_pieces_framework2.Property.ShortText({
    displayName: "Billing State",
    required: !1
  }),
  ADDRESS_BILLING_POSTCODE: import_pieces_framework2.Property.ShortText({
    displayName: "Billing Postcode",
    required: !1
  }),
  ADDRESS_BILLING_COUNTRY: import_pieces_framework2.Property.ShortText({
    displayName: "Billing Country",
    required: !1
  }),
  ADDRESS_SHIP_STREET: import_pieces_framework2.Property.ShortText({
    displayName: "Shipping Street",
    required: !1
  }),
  ADDRESS_SHIP_CITY: import_pieces_framework2.Property.ShortText({
    displayName: "Shipping City",
    required: !1
  }),
  ADDRESS_SHIP_STATE: import_pieces_framework2.Property.ShortText({
    displayName: "Shipping State",
    required: !1
  }),
  ADDRESS_SHIP_POSTCODE: import_pieces_framework2.Property.ShortText({
    displayName: "Shipping Postcode",
    required: !1
  }),
  ADDRESS_SHIP_COUNTRY: import_pieces_framework2.Property.ShortText({
    displayName: "Shipping Country",
    required: !1
  }),
  SOCIAL_LINKEDIN: import_pieces_framework2.Property.ShortText({
    displayName: "LinkedIn",
    required: !1
  }),
  SOCIAL_FACEBOOK: import_pieces_framework2.Property.ShortText({
    displayName: "Facebook",
    required: !1
  }),
  SOCIAL_TWITTER: import_pieces_framework2.Property.ShortText({
    displayName: "Twitter",
    required: !1
  }),
  OWNER_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Owner User ID",
    required: !1
  })
}, projectFields = {
  PROJECT_NAME: import_pieces_framework2.Property.ShortText({
    displayName: "Project Name",
    required: !0
  }),
  STATUS: import_pieces_framework2.Property.StaticDropdown({
    displayName: "Status",
    required: !0,
    options: {
      options: [
        { label: "Not Started", value: "NOT STARTED" },
        { label: "In Progress", value: "IN PROGRESS" },
        { label: "On Hold", value: "ON HOLD" },
        { label: "Completed", value: "COMPLETED" },
        { label: "Cancelled", value: "CANCELLED" }
      ]
    }
  }),
  PROJECT_DETAILS: import_pieces_framework2.Property.LongText({
    displayName: "Project Details",
    required: !1
  }),
  STARTED_DATE: import_pieces_framework2.Property.ShortText({
    displayName: "Started Date",
    description: "Format: YYYY-MM-DD",
    required: !1
  }),
  COMPLETED_DATE: import_pieces_framework2.Property.ShortText({
    displayName: "Completed Date",
    description: "Format: YYYY-MM-DD",
    required: !1
  }),
  OPPORTUNITY_ID: import_pieces_framework2.Property.Number({
    displayName: "Opportunity ID",
    required: !1
  }),
  CATEGORY_ID: import_pieces_framework2.Property.Number({
    displayName: "Category ID",
    required: !1
  }),
  PIPELINE_ID: import_pieces_framework2.Property.Number({
    displayName: "Pipeline ID",
    required: !1
  }),
  STAGE_ID: import_pieces_framework2.Property.Number({
    displayName: "Stage ID",
    required: !1
  }),
  OWNER_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Owner User ID",
    required: !1
  }),
  RESPONSIBLE_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Responsible User ID",
    required: !1
  })
}, taskFields = {
  TITLE: import_pieces_framework2.Property.ShortText({
    displayName: "Title",
    required: !0
  }),
  STATUS: import_pieces_framework2.Property.StaticDropdown({
    displayName: "Status",
    required: !1,
    options: {
      options: [
        { label: "Not Started", value: "NOT STARTED" },
        { label: "In Progress", value: "IN PROGRESS" },
        { label: "Waiting", value: "WAITING" },
        { label: "Completed", value: "COMPLETED" },
        { label: "Deferred", value: "DEFERRED" }
      ]
    }
  }),
  DETAILS: import_pieces_framework2.Property.LongText({
    displayName: "Details",
    required: !1
  }),
  START_DATE: import_pieces_framework2.Property.ShortText({
    displayName: "Start Date",
    description: "Format: YYYY-MM-DD",
    required: !1
  }),
  DUE_DATE: import_pieces_framework2.Property.ShortText({
    displayName: "Due Date",
    description: "Format: YYYY-MM-DD",
    required: !1
  }),
  PERCENT_COMPLETE: import_pieces_framework2.Property.Number({
    displayName: "Percent Complete",
    required: !1
  }),
  PRIORITY: import_pieces_framework2.Property.Number({
    displayName: "Priority",
    description: "1 (Low), 2 (Normal), 3 (High)",
    required: !1
  }),
  COMPLETED: import_pieces_framework2.Property.Checkbox({
    displayName: "Completed",
    required: !1
  }),
  OWNER_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Owner User ID",
    required: !0
  }),
  RESPONSIBLE_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Responsible User ID",
    required: !1
  }),
  PROJECT_ID: import_pieces_framework2.Property.Number({
    displayName: "Project ID",
    required: !1
  }),
  OPPORTUNITY_ID: import_pieces_framework2.Property.Number({
    displayName: "Opportunity ID",
    required: !1
  }),
  CATEGORY_ID: import_pieces_framework2.Property.Number({
    displayName: "Category ID",
    required: !1
  }),
  MILESTONE_ID: import_pieces_framework2.Property.Number({
    displayName: "Milestone ID",
    required: !1
  })
}, eventFields = {
  TITLE: import_pieces_framework2.Property.ShortText({
    displayName: "Title",
    required: !0
  }),
  LOCATION: import_pieces_framework2.Property.ShortText({
    displayName: "Location",
    required: !1
  }),
  START_DATE_UTC: import_pieces_framework2.Property.ShortText({
    displayName: "Start Date (UTC)",
    description: "Format: YYYY-MM-DDTHH:MM:SSZ",
    required: !0
  }),
  END_DATE_UTC: import_pieces_framework2.Property.ShortText({
    displayName: "End Date (UTC)",
    description: "Format: YYYY-MM-DDTHH:MM:SSZ",
    required: !0
  }),
  ALL_DAY: import_pieces_framework2.Property.Checkbox({
    displayName: "All Day",
    required: !1
  }),
  DETAILS: import_pieces_framework2.Property.LongText({
    displayName: "Details",
    required: !1
  }),
  OWNER_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Owner User ID",
    required: !1
  })
}, noteFields = {
  TITLE: import_pieces_framework2.Property.ShortText({
    displayName: "Title",
    required: !0
  }),
  BODY: import_pieces_framework2.Property.LongText({
    displayName: "Body",
    required: !1
  })
}, productFields = {
  PRODUCT_NAME: import_pieces_framework2.Property.ShortText({
    displayName: "Product Name",
    required: !0
  }),
  PRODUCT_CODE: import_pieces_framework2.Property.ShortText({
    displayName: "Product Code",
    required: !1
  }),
  PRODUCT_SKU: import_pieces_framework2.Property.ShortText({
    displayName: "Product SKU",
    required: !1
  }),
  DESCRIPTION: import_pieces_framework2.Property.LongText({
    displayName: "Description",
    required: !1
  }),
  PRODUCT_FAMILY: import_pieces_framework2.Property.ShortText({
    displayName: "Product Family",
    required: !1
  }),
  PRODUCT_IMAGE_URL: import_pieces_framework2.Property.ShortText({
    displayName: "Product Image URL",
    required: !1
  }),
  DEFAULT_PRICE: import_pieces_framework2.Property.Number({
    displayName: "Default Price",
    required: !1
  }),
  ACTIVE: import_pieces_framework2.Property.Checkbox({
    displayName: "Active",
    required: !1
  }),
  OWNER_USER_ID: import_pieces_framework2.Property.Number({
    displayName: "Owner User ID",
    required: !1
  }),
  CURRENCY_CODE: import_pieces_framework2.Property.ShortText({
    displayName: "Currency Code",
    description: "3-letter currency code (e.g., USD)",
    required: !1
  })
}, quotationFields = {
  QUOTATION_NAME: import_pieces_framework2.Property.ShortText({
    displayName: "Quotation Name",
    required: !0
  }),
  OPPORTUNITY_ID: import_pieces_framework2.Property.Number({
    displayName: "Opportunity ID",
    required: !0
  }),
  QUOTE_STATUS: import_pieces_framework2.Property.StaticDropdown({
    displayName: "Quote Status",
    required: !1,
    options: {
      options: [
        { label: "Draft", value: "Draft" },
        { label: "Awaiting Approval", value: "Awaiting Approval" },
        { label: "Approved", value: "Approved" },
        { label: "Sent", value: "Sent" },
        { label: "Accepted", value: "Accepted" },
        { label: "Declined", value: "Declined" }
      ]
    }
  }),
  QUOTATION_DESCRIPTION: import_pieces_framework2.Property.LongText({
    displayName: "Description",
    required: !1
  }),
  QUOTATION_EXPIRATION_DATE: import_pieces_framework2.Property.ShortText({
    displayName: "Expiration Date",
    description: "Format: YYYY-MM-DD",
    required: !1
  }),
  CONTACT_ID: import_pieces_framework2.Property.Number({
    displayName: "Contact ID",
    required: !1
  }),
  ORGANISATION_ID: import_pieces_framework2.Property.Number({
    displayName: "Organisation ID",
    required: !1
  }),
  PRICEBOOK_ID: import_pieces_framework2.Property.Number({
    displayName: "Pricebook ID",
    required: !1
  }),
  QUOTATION_PHONE: import_pieces_framework2.Property.ShortText({
    displayName: "Phone",
    required: !1
  }),
  QUOTATION_EMAIL: import_pieces_framework2.Property.ShortText({
    displayName: "Email",
    required: !1
  }),
  QUOTATION_FAX: import_pieces_framework2.Property.ShortText({
    displayName: "Fax",
    required: !1
  }),
  SHIPPING_HANDLING: import_pieces_framework2.Property.Number({
    displayName: "Shipping & Handling",
    required: !1
  }),
  TAX: import_pieces_framework2.Property.Number({
    displayName: "Tax",
    required: !1
  }),
  ADDRESS_BILLING_NAME: import_pieces_framework2.Property.ShortText({
    displayName: "Billing Name",
    required: !1
  }),
  ADDRESS_BILLING_STREET: import_pieces_framework2.Property.ShortText({
    displayName: "Billing Street",
    required: !1
  }),
  ADDRESS_BILLING_CITY: import_pieces_framework2.Property.ShortText({
    displayName: "Billing City",
    required: !1
  }),
  ADDRESS_BILLING_STATE: import_pieces_framework2.Property.ShortText({
    displayName: "Billing State",
    required: !1
  }),
  ADDRESS_BILLING_POSTCODE: import_pieces_framework2.Property.ShortText({
    displayName: "Billing Postcode",
    required: !1
  }),
  ADDRESS_BILLING_COUNTRY: import_pieces_framework2.Property.ShortText({
    displayName: "Billing Country",
    required: !1
  }),
  ADDRESS_SHIPPING_NAME: import_pieces_framework2.Property.ShortText({
    displayName: "Shipping Name",
    required: !1
  }),
  ADDRESS_SHIPPING_STREET: import_pieces_framework2.Property.ShortText({
    displayName: "Shipping Street",
    required: !1
  }),
  ADDRESS_SHIPPING_CITY: import_pieces_framework2.Property.ShortText({
    displayName: "Shipping City",
    required: !1
  }),
  ADDRESS_SHIPPING_STATE: import_pieces_framework2.Property.ShortText({
    displayName: "Shipping State",
    required: !1
  }),
  ADDRESS_SHIPPING_POSTCODE: import_pieces_framework2.Property.ShortText({
    displayName: "Shipping Postcode",
    required: !1
  }),
  ADDRESS_SHIPPING_COUNTRY: import_pieces_framework2.Property.ShortText({
    displayName: "Shipping Country",
    required: !1
  })
};

// ../ap/packages/pieces/community/insightly/src/lib/actions/create-record.ts
var createRecord = (0, import_pieces_framework3.createAction)({
  auth: insightlyAuth,
  name: "create_record",
  displayName: "Create Record",
  description: "Create a new record in a specified Insightly object (Contact, Lead, Opportunity, etc.)",
  audience: "both",
  aiMetadata: { description: 'Creates a new record in a chosen Insightly CRM object (Contact, Lead, Opportunity, Organization, Project, Task, Event, Product, or Quote), with field values supplied per the selected object type. Use to add a new entity to Insightly. Not idempotent: each call creates another record. Requires the Insightly pod (e.g. "na1") that matches your account region.', idempotent: !1 },
  props: {
    pod: import_pieces_framework3.Property.ShortText({
      displayName: "Pod",
      description: 'Your Insightly pod (e.g., "na1", "eu1"). Find this in your API URL: https://api.{pod}.insightly.com',
      required: !0,
      defaultValue: "na1"
    }),
    objectName: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Object Type",
      description: "Select the type of record to create",
      required: !0,
      options: {
        options: [
          { label: "Contact", value: "Contacts" },
          { label: "Lead", value: "Leads" },
          { label: "Opportunity", value: "Opportunities" },
          { label: "Organization", value: "Organisations" },
          { label: "Project", value: "Projects" },
          { label: "Task", value: "Tasks" },
          { label: "Event", value: "Events" },
          { label: "Product", value: "Products" },
          { label: "Quote", value: "Quotations" }
        ]
      }
    }),
    fields: import_pieces_framework3.Property.DynamicProperties({
      auth: insightlyAuth,
      displayName: "Fields",
      required: !0,
      refreshers: ["objectName"],
      props: async ({ auth, objectName }) => {
        if (!objectName) return {};
        let objName = objectName, fields = {};
        switch (objName) {
          case "Contacts":
            Object.assign(fields, contactFields);
            break;
          case "Leads":
            Object.assign(fields, leadFields);
            break;
          case "Opportunities":
            Object.assign(fields, opportunityFields);
            break;
          case "Organisations":
            Object.assign(fields, organisationFields);
            break;
          case "Projects":
            Object.assign(fields, projectFields);
            break;
          case "Tasks":
            Object.assign(fields, taskFields);
            break;
          case "Events":
            Object.assign(fields, eventFields);
            break;
          case "Products":
            Object.assign(fields, productFields);
            break;
          case "Quotations":
            Object.assign(fields, quotationFields);
            break;
        }
        return fields;
      }
    })
  },
  async run(context) {
    let { pod, objectName, fields } = context.propsValue, recordData = __spreadValues({}, fields);
    if (!recordData || Object.keys(recordData).length === 0)
      throw new Error("Field values must be provided to create the record");
    try {
      let response = await makeInsightlyRequest(
        context.auth,
        `/${objectName}`,
        pod,
        import_pieces_common2.HttpMethod.POST,
        recordData
      ), recordIdKey = Object.keys(response.body).find((key) => key.endsWith("_ID")), recordId = recordIdKey ? response.body[recordIdKey] : void 0;
      return __spreadValues({
        success: !0,
        recordId
      }, response.body);
    } catch (error) {
      throw new Error(`Failed to create record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/insightly/src/lib/actions/update-record.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var updateRecord = (0, import_pieces_framework4.createAction)({
  auth: insightlyAuth,
  name: "update_record",
  displayName: "Update Record",
  description: "Update an existing record's fields in a specified Insightly object",
  audience: "both",
  aiMetadata: { description: 'Updates field values on an existing record in a chosen Insightly CRM object (Contact, Lead, Opportunity, etc.), identified by its record ID. Use to modify an entity that already exists. Sending the same field values repeatedly leaves the record in the same state, but the call always overwrites the targeted fields. Requires the matching Insightly pod (e.g. "na1") and the record ID.', idempotent: !1 },
  props: {
    pod: import_pieces_framework4.Property.ShortText({
      displayName: "Pod",
      description: 'Your Insightly pod (e.g., "na1", "eu1"). Find this in your API URL: https://api.{pod}.insightly.com',
      required: !0,
      defaultValue: "na1"
    }),
    objectName: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Object Type",
      description: "Select the type of object that contains the record you want to update",
      required: !0,
      options: {
        options: [
          { label: "Contact", value: "Contacts" },
          { label: "Lead", value: "Leads" },
          { label: "Opportunity", value: "Opportunities" },
          { label: "Organization", value: "Organisations" },
          { label: "Project", value: "Projects" },
          { label: "Task", value: "Tasks" },
          { label: "Event", value: "Events" },
          { label: "Product", value: "Products" },
          { label: "Quote", value: "Quotations" }
        ]
      }
    }),
    recordId: import_pieces_framework4.Property.Dropdown({
      auth: insightlyAuth,
      displayName: "Record ID",
      description: "Select the record to update",
      required: !0,
      refreshers: ["objectName", "pod"],
      options: async ({ auth, objectName, pod }) => {
        if (!objectName || !pod || !auth)
          return {
            disabled: !0,
            placeholder: "Please select an object type and pod first",
            options: []
          };
        let response = await makeInsightlyRequest(
          auth,
          `/${objectName}?top=100&brief=true`,
          pod
        );
        return {
          options: (Array.isArray(response.body) ? response.body : []).map((record) => {
            let idKey = Object.keys(record).find((key) => key.endsWith("_ID")), recordId = idKey ? record[idKey] : "Unknown ID", nameKey = Object.keys(record).find((key) => key.endsWith("_NAME") || key === "TITLE" || key === "FIRST_NAME"), recordName = nameKey ? record[nameKey] : `Record ${recordId}`;
            return (objectName === "Contacts" || objectName === "Leads") && (recordName = `${record.FIRST_NAME || ""} ${record.LAST_NAME || ""}`.trim()), {
              label: `${recordName} (ID: ${recordId})`,
              value: recordId
            };
          })
        };
      }
    }),
    fields: import_pieces_framework4.Property.DynamicProperties({
      auth: insightlyAuth,
      displayName: "Fields",
      description: "The new field values to update.",
      required: !0,
      refreshers: ["objectName", "recordId", "pod"],
      props: async ({ auth, objectName, recordId, pod }) => {
        if (!objectName || !recordId || !pod || !auth)
          return {};
        let objName = objectName, podName = pod, record = await makeInsightlyRequest(
          auth,
          `/${objName}/${recordId}`,
          podName
        ), fieldDefinitions = {};
        switch (objName) {
          case "Contacts":
            fieldDefinitions = contactFields;
            break;
          case "Leads":
            fieldDefinitions = leadFields;
            break;
          case "Opportunities":
            fieldDefinitions = opportunityFields;
            break;
          case "Organisations":
            fieldDefinitions = organisationFields;
            break;
          case "Projects":
            fieldDefinitions = projectFields;
            break;
          case "Tasks":
            fieldDefinitions = taskFields;
            break;
          case "Events":
            fieldDefinitions = eventFields;
            break;
          case "Products":
            fieldDefinitions = productFields;
            break;
          case "Quotations":
            fieldDefinitions = quotationFields;
            break;
        }
        let fields = {};
        for (let key in fieldDefinitions) {
          let fieldDef = fieldDefinitions[key];
          fields[key] = __spreadProps(__spreadValues({}, fieldDef), {
            defaultValue: record.body[key]
          });
        }
        return fields;
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let { pod, objectName, recordId, fields } = context.propsValue, recordData = __spreadValues({}, fields), idKey = `${objectName.slice(0, -1).toUpperCase()}_ID`;
    recordData[idKey] = recordId;
    try {
      let response = await makeInsightlyRequest(
        context.auth,
        `/${objectName}/${recordId}`,
        pod,
        import_pieces_common3.HttpMethod.PUT,
        recordData
      );
      return __spreadValues({
        success: !0
      }, response.body);
    } catch (error) {
      if (((_a = error.response) == null ? void 0 : _a.status) === 400) {
        let errorMessage = ((_b = error.response.body) == null ? void 0 : _b.message) || ((_c = error.response.body) == null ? void 0 : _c.error) || "Invalid data provided or record not found", errorDetails = error.response.body ? JSON.stringify(error.response.body, null, 2) : "No details available";
        throw new Error(
          `Data validation error for ${objectName} ID ${recordId}: ${errorMessage}. Sent data: ${JSON.stringify(recordData, null, 2)}. API response: ${errorDetails}`
        );
      } else throw ((_d = error.response) == null ? void 0 : _d.status) === 401 ? new Error(
        "Authentication failed. Please check your API key and pod."
      ) : ((_e = error.response) == null ? void 0 : _e.status) === 402 ? new Error("Record limit reached for your Insightly plan.") : ((_f = error.response) == null ? void 0 : _f.status) === 404 ? new Error(
        `Record with ID ${recordId} not found in ${objectName}`
      ) : new Error(`Failed to update record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/insightly/src/lib/actions/get-record.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getRecord = (0, import_pieces_framework5.createAction)({
  auth: insightlyAuth,
  name: "get_record",
  displayName: "Get Record",
  description: "Get a record by ID from a specified Insightly object",
  audience: "both",
  aiMetadata: { description: 'Retrieves a single record by its ID from a chosen Insightly CRM object (Contact, Lead, Opportunity, etc.). Use to look up the full details of one known entity. Read-only and idempotent. Requires the matching Insightly pod (e.g. "na1") and the record ID.', idempotent: !0 },
  props: {
    pod: import_pieces_framework5.Property.ShortText({
      displayName: "Pod",
      description: 'Your Insightly pod (e.g., "na1", "eu1"). Find this in your API URL: https://api.{pod}.insightly.com',
      required: !0,
      defaultValue: "na1"
    }),
    objectName: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Object Type",
      description: "Select the type of record to retrieve",
      required: !0,
      options: {
        options: [
          { label: "Contact", value: "Contacts" },
          { label: "Lead", value: "Leads" },
          { label: "Opportunity", value: "Opportunities" },
          { label: "Organization", value: "Organisations" },
          { label: "Project", value: "Projects" },
          { label: "Task", value: "Tasks" },
          { label: "Event", value: "Events" },
          { label: "Product", value: "Products" },
          { label: "Quote", value: "Quotations" }
        ]
      }
    }),
    recordId: import_pieces_framework5.Property.Dropdown({
      auth: insightlyAuth,
      displayName: "Record ID",
      description: "Select the record to retrieve",
      required: !0,
      refreshers: ["objectName", "pod"],
      options: async ({ auth, objectName, pod }) => {
        if (!objectName || !pod || !auth)
          return {
            disabled: !0,
            placeholder: "Please select an object type and pod first",
            options: []
          };
        let response = await makeInsightlyRequest(
          auth,
          `/${objectName}?top=100&brief=true`,
          pod
        );
        return {
          options: (Array.isArray(response.body) ? response.body : []).map((record) => {
            let idKey = Object.keys(record).find((key) => key.endsWith("_ID")), recordId = idKey ? record[idKey] : "Unknown ID", nameKey = Object.keys(record).find((key) => key.endsWith("_NAME") || key === "TITLE" || key === "FIRST_NAME"), recordName = nameKey ? record[nameKey] : `Record ${recordId}`;
            return (objectName === "Contacts" || objectName === "Leads") && (recordName = `${record.FIRST_NAME || ""} ${record.LAST_NAME || ""}`.trim()), {
              label: `${recordName} (ID: ${recordId})`,
              value: recordId
            };
          })
        };
      }
    })
  },
  async run(context) {
    var _a;
    let { pod, objectName, recordId } = context.propsValue;
    try {
      return (await makeInsightlyRequest(
        context.auth,
        `/${objectName}/${recordId}`,
        pod,
        import_pieces_common4.HttpMethod.GET
      )).body;
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 404 ? new Error(`Record with ID ${recordId} not found in ${objectName}`) : new Error(`Failed to retrieve record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/insightly/src/lib/actions/delete-record.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var deleteRecord = (0, import_pieces_framework6.createAction)({
  auth: insightlyAuth,
  name: "delete_record",
  displayName: "Delete Record",
  description: "Delete a record by ID from a specified Insightly object",
  audience: "both",
  aiMetadata: { description: 'Permanently deletes a single record by its ID from a chosen Insightly CRM object (Contact, Lead, Opportunity, etc.). Use to remove an entity. Destructive: repeating the call on the same ID after deletion errors with a not-found, so treat as not idempotent. Requires the matching Insightly pod (e.g. "na1") and the record ID.', idempotent: !1 },
  props: {
    pod: import_pieces_framework6.Property.ShortText({
      displayName: "Pod",
      description: 'Your Insightly pod (e.g., "na1", "eu1"). Find this in your API URL: https://api.{pod}.insightly.com',
      required: !0,
      defaultValue: "na1"
    }),
    objectName: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Object Type",
      description: "Select the type of record to delete",
      required: !0,
      options: {
        options: [
          { label: "Contact", value: "Contacts" },
          { label: "Lead", value: "Leads" },
          { label: "Opportunity", value: "Opportunities" },
          { label: "Organization", value: "Organisations" },
          { label: "Project", value: "Projects" },
          { label: "Task", value: "Tasks" },
          { label: "Event", value: "Events" },
          { label: "Product", value: "Products" },
          { label: "Quote", value: "Quotations" }
        ]
      }
    }),
    recordId: import_pieces_framework6.Property.Dropdown({
      auth: insightlyAuth,
      displayName: "Record ID",
      description: "Select the record to delete",
      required: !0,
      refreshers: ["objectName", "pod"],
      options: async ({ auth, objectName, pod }) => {
        if (!objectName || !pod || !auth)
          return {
            disabled: !0,
            placeholder: "Please select an object type and pod first",
            options: []
          };
        let response = await makeInsightlyRequest(
          auth,
          `/${objectName}?top=100&brief=true`,
          pod
        );
        return {
          options: (Array.isArray(response.body) ? response.body : []).map((record) => {
            let idKey = Object.keys(record).find((key) => key.endsWith("_ID")), recordId = idKey ? record[idKey] : "Unknown ID", nameKey = Object.keys(record).find((key) => key.endsWith("_NAME") || key === "TITLE" || key === "FIRST_NAME"), recordName = nameKey ? record[nameKey] : `Record ${recordId}`;
            return (objectName === "Contacts" || objectName === "Leads") && (recordName = `${record.FIRST_NAME || ""} ${record.LAST_NAME || ""}`.trim()), {
              label: `${recordName} (ID: ${recordId})`,
              value: recordId
            };
          })
        };
      }
    })
  },
  async run(context) {
    var _a;
    let { pod, objectName, recordId } = context.propsValue;
    try {
      return await makeInsightlyRequest(
        context.auth,
        `/${objectName}/${recordId}`,
        pod,
        import_pieces_common5.HttpMethod.DELETE
      ), {
        success: !0,
        message: `Record ${recordId} successfully deleted from ${objectName}`
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 404 ? new Error(`Record with ID ${recordId} not found in ${objectName}`) : new Error(`Failed to delete record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/insightly/src/lib/actions/find-records.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var findRecords = (0, import_pieces_framework7.createAction)({
  auth: insightlyAuth,
  name: "find_records",
  displayName: "Find Records",
  description: "Find records in a specified Insightly object",
  audience: "both",
  aiMetadata: { description: 'Searches a chosen Insightly CRM object (Contact, Lead, Opportunity, etc.) for records where a given field matches a value, optionally capped by a limit. Use to locate records by a known field value (e.g. an email or name) rather than by ID. Read-only and idempotent. Requires the matching Insightly pod (e.g. "na1"), the field to search, and the value to match.', idempotent: !0 },
  props: {
    pod: import_pieces_framework7.Property.ShortText({
      displayName: "Pod",
      description: 'Your Insightly pod (e.g., "na1", "eu1").',
      required: !0,
      defaultValue: "na1"
    }),
    objectName: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Object Type",
      description: "Select the type of records to find",
      required: !0,
      options: {
        options: [
          { label: "Contact", value: "Contacts" },
          { label: "Lead", value: "Leads" },
          { label: "Opportunity", value: "Opportunities" },
          { label: "Organization", value: "Organisations" },
          { label: "Project", value: "Projects" },
          { label: "Task", value: "Tasks" },
          { label: "Event", value: "Events" },
          { label: "Product", value: "Products" },
          { label: "Quote", value: "Quotations" }
        ]
      }
    }),
    fieldName: import_pieces_framework7.Property.Dropdown({
      auth: insightlyAuth,
      displayName: "Field Name",
      description: "Select the field to search by",
      required: !0,
      refreshers: ["objectName"],
      options: async ({ objectName }) => {
        if (!objectName)
          return {
            disabled: !0,
            placeholder: "Select an object type first",
            options: []
          };
        let fields = {};
        switch (objectName) {
          case "Contacts":
            fields = contactFields;
            break;
          case "Leads":
            fields = leadFields;
            break;
          case "Opportunities":
            fields = opportunityFields;
            break;
          case "Organisations":
            fields = organisationFields;
            break;
          case "Projects":
            fields = projectFields;
            break;
          case "Tasks":
            fields = taskFields;
            break;
          case "Events":
            fields = eventFields;
            break;
          case "Products":
            fields = productFields;
            break;
          case "Quotations":
            fields = quotationFields;
            break;
        }
        return {
          options: Object.keys(fields).map((key) => ({
            label: fields[key].displayName,
            value: key
          }))
        };
      }
    }),
    fieldValue: import_pieces_framework7.Property.ShortText({
      displayName: "Field Value",
      description: "The value to search for",
      required: !0
    }),
    top: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      description: "Maximum number of records to return",
      required: !1
    })
  },
  async run(context) {
    let { pod, objectName, fieldName, fieldValue, top } = context.propsValue, queryParams = new URLSearchParams({
      field_name: fieldName,
      field_value: fieldValue
    });
    top && queryParams.append("top", top.toString());
    try {
      return (await makeInsightlyRequest(
        context.auth,
        `/${objectName}/Search?${queryParams.toString()}`,
        pod,
        import_pieces_common6.HttpMethod.GET
      )).body;
    } catch (error) {
      throw new Error(`Failed to find records: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/insightly/src/lib/triggers/new-record.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var newRecord = (0, import_pieces_framework8.createTrigger)({
  auth: insightlyAuth,
  name: "new_record",
  displayName: "New Record",
  description: "Fires when a new record is created in Insightly (requires webhook setup)",
  aiMetadata: {
    description: "Fires when a new record is created in the selected Insightly object type (Contact, Lead, Opportunity, Organization, Project, Task, or Event). Each event carries the newly created record. Requires a webhook to be configured manually in Insightly Workflow Automation for the chosen object type."
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  props: {
    objectType: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Object Type",
      description: "The type of Insightly object to monitor for new records",
      required: !0,
      options: {
        options: [
          { label: "Contact", value: "Contacts" },
          { label: "Lead", value: "Leads" },
          { label: "Opportunity", value: "Opportunities" },
          { label: "Organization", value: "Organisations" },
          { label: "Project", value: "Projects" },
          { label: "Task", value: "Tasks" },
          { label: "Event", value: "Events" }
        ]
      }
    }),
    webhookUrl: import_pieces_framework8.Property.MarkDown({
      value: `
**Webhook URL:**
\`\`\`text
{{webhookUrl}}
\`\`\`
**Instructions:**
1.  Go to **System Settings > Workflow Automation** in your Insightly account.
2.  Create a new Workflow Process for the desired **Object Type**.
3.  Set the trigger to **"When a record is created"**.
4.  Under "Immediate Actions," add a new action and select **"Add New Webhook"**.
5.  Paste the webhook URL above into the **URI** field and save the workflow.
            `
    })
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload.body;
    return payload && payload.entity ? [payload.entity] : [];
  },
  async test(context) {
    return [
      {
        CONTACT_ID: 123456,
        FIRST_NAME: "John",
        LAST_NAME: "Doe",
        EMAIL_ADDRESS: "john.doe@example.com",
        DATE_CREATED_UTC: (/* @__PURE__ */ new Date()).toISOString()
      }
    ];
  },
  sampleData: {}
  // Webhook triggers don't need sample data in the same way
});

// ../ap/packages/pieces/community/insightly/src/lib/triggers/updated-record.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var updatedRecord = (0, import_pieces_framework9.createTrigger)({
  auth: insightlyAuth,
  name: "updated_record",
  displayName: "Updated Record",
  description: "Fires when an existing record is updated in Insightly (requires webhook setup)",
  aiMetadata: {
    description: "Fires when a record is created or edited in the selected Insightly object type (Contact, Lead, Opportunity, Organization, Project, Task, or Event). Each event carries the affected record. Requires a webhook to be configured manually in Insightly Workflow Automation for the chosen object type."
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  props: {
    objectType: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Object Type",
      description: "The type of Insightly object to monitor for updates",
      required: !0,
      options: {
        options: [
          { label: "Contact", value: "Contacts" },
          { label: "Lead", value: "Leads" },
          { label: "Opportunity", value: "Opportunities" },
          { label: "Organization", value: "Organisations" },
          { label: "Project", value: "Projects" },
          { label: "Task", value: "Tasks" },
          { label: "Event", value: "Events" }
        ]
      }
    }),
    webhookUrl: import_pieces_framework9.Property.MarkDown({
      value: `
**Webhook URL:**
\`\`\`text
{{webhookUrl}}
\`\`\`
**Instructions:**
1.  Go to **System Settings > Workflow Automation** in your Insightly account.
2.  Create a new Workflow Process for the desired **Object Type**.
3.  Set the trigger to **"When a record is created and when a record is edited"**.
4.  Under "Immediate Actions," add a new action and select **"Add New Webhook"**.
5.  Paste the webhook URL above into the **URI** field and save the workflow.
            `
    })
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload.body;
    return payload && payload.entity ? [payload.entity] : [];
  },
  async test(context) {
    return [
      {
        CONTACT_ID: 123456,
        FIRST_NAME: "John",
        LAST_NAME: "Doe",
        EMAIL_ADDRESS: "john.doe.updated@example.com",
        DATE_CREATED_UTC: "2025-10-03T23:29:42.815Z",
        DATE_UPDATED_UTC: (/* @__PURE__ */ new Date()).toISOString()
      }
    ];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/insightly/src/lib/triggers/deleted-record.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var deletedRecord = (0, import_pieces_framework10.createTrigger)({
  auth: insightlyAuth,
  name: "deleted_record",
  displayName: "Deleted Record",
  description: "Fires when a record is deleted from Insightly",
  aiMetadata: {
    description: 'Fires when a record disappears from the selected Insightly object type (Contact, Lead, Opportunity, Organization, Project, Task, Event, Product, or Quote), inferring deletion by polling and comparing against previously seen records. A deletion is only reported after the record has been absent for at least 15 minutes, so events are delayed and approximate rather than real-time. Requires the Insightly pod (e.g. "na1") matching your account region.'
  },
  type: import_pieces_framework10.TriggerStrategy.POLLING,
  props: {
    pod: import_pieces_framework10.Property.ShortText({
      displayName: "Pod",
      description: 'Your Insightly pod (e.g., "na1", "eu1").',
      required: !0,
      defaultValue: "na1"
    }),
    objectType: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Object Type",
      description: "The type of Insightly object to monitor for deletions",
      required: !0,
      options: {
        options: [
          { label: "Contact", value: "Contacts" },
          { label: "Lead", value: "Leads" },
          { label: "Opportunity", value: "Opportunities" },
          { label: "Organization", value: "Organisations" },
          { label: "Project", value: "Projects" },
          { label: "Task", value: "Tasks" },
          { label: "Event", value: "Events" },
          { label: "Product", value: "Products" },
          { label: "Quote", value: "Quotations" }
        ]
      }
    })
  },
  async onEnable(context) {
    await context.store.put(`known_records_${context.propsValue.objectType}`, {});
  },
  async onDisable(context) {
    await context.store.delete(`known_records_${context.propsValue.objectType}`);
  },
  async run(context) {
    let { pod, objectType } = context.propsValue, response = await makeInsightlyRequest(
      context.auth,
      `/${objectType}?brief=true&top=500`,
      pod,
      import_pieces_common7.HttpMethod.GET
    ), records = Array.isArray(response.body) ? response.body : [], currentTimeMs = Date.now(), currentTime = new Date(currentTimeMs).toISOString(), getRecordId = (record) => {
      let idKey = Object.keys(record).find((key) => key.endsWith("_ID"));
      return String(idKey ? record[idKey] : record.ID || "");
    }, getRecordName = (record) => {
      if (record.FIRST_NAME || record.LAST_NAME)
        return `${record.FIRST_NAME || ""} ${record.LAST_NAME || ""}`.trim();
      let nameKey = Object.keys(record).find(
        (key) => key.endsWith("_NAME") || key === "TITLE"
      );
      return nameKey ? String(record[nameKey]) : void 0;
    }, currentRecordIds = new Set(records.map(getRecordId)), storeKey = `known_records_${objectType}`, storedRecords = await context.store.get(storeKey) || {}, storedRecordIds = new Set(Object.keys(storedRecords)), deletedRecords = [], DELETION_THRESHOLD_MS = 900 * 1e3;
    for (let storedId of storedRecordIds)
      if (!currentRecordIds.has(storedId))
        currentTimeMs - storedRecords[storedId].lastSeenMs > DELETION_THRESHOLD_MS && (deletedRecords.push({
          recordId: storedId,
          recordName: storedRecords[storedId].name || `Record ${storedId}`,
          deletedAt: currentTime,
          objectType,
          lastSeenAt: new Date(storedRecords[storedId].lastSeenMs).toISOString()
        }), delete storedRecords[storedId]);
      else {
        let recordId = storedId, record = records.find((r) => getRecordId(r) === recordId);
        record && (storedRecords[recordId] = {
          name: getRecordName(record) || storedRecords[recordId].name,
          lastSeenMs: currentTimeMs
        });
      }
    for (let record of records) {
      let recordId = getRecordId(record);
      storedRecords[recordId] || (storedRecords[recordId] = {
        name: getRecordName(record) || `Record ${recordId}`,
        lastSeenMs: currentTimeMs
      });
    }
    let THIRTY_DAYS_MS = 720 * 60 * 60 * 1e3;
    return Object.keys(storedRecords).forEach((id) => {
      currentTimeMs - storedRecords[id].lastSeenMs > THIRTY_DAYS_MS && delete storedRecords[id];
    }), await context.store.put(storeKey, storedRecords), deletedRecords;
  },
  test: async (context) => [
    {
      recordId: 123456,
      recordName: "Test Deleted Record",
      deletedAt: (/* @__PURE__ */ new Date()).toISOString(),
      objectType: context.propsValue.objectType
    }
  ],
  sampleData: {}
});

// ../ap/packages/pieces/community/insightly/src/index.ts
var insightly = (0, import_pieces_framework11.createPiece)({
  displayName: "Insightly",
  auth: insightlyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/insightly.png",
  authors: ["fortunamide", "onyedikachi-david"],
  actions: [createRecord, updateRecord, getRecord, deleteRecord, findRecords],
  triggers: [newRecord, updatedRecord, deletedRecord]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  insightly
});
