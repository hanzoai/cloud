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

// ../ap/packages/pieces/community/hunter/src/index.ts
var index_exports = {};
__export(index_exports, {
  hunter: () => hunter
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/hunter/src/lib/actions/add-recipients.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/hunter/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function hunterApiCall({
  apiKey,
  endpoint,
  method,
  qparams,
  body
}) {
  let queryParams = __spreadProps(__spreadValues({}, qparams != null ? qparams : {}), {
    api_key: apiKey.secret_text
  }), request = {
    url: `https://api.hunter.io/v2${endpoint}`,
    method,
    queryParams,
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}

// ../ap/packages/pieces/community/hunter/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), hunterAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "You can obtain your API key from [Account Settings](https://hunter.io/dashboard)"
});

// ../ap/packages/pieces/community/hunter/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var campaignIdProp = import_pieces_framework2.Property.Dropdown({
  displayName: "Campaign",
  required: !0,
  refreshers: [],
  auth: hunterAuth,
  options: async ({ auth }) => {
    var _a;
    return auth ? {
      disabled: !1,
      options: ((_a = (await hunterApiCall({
        apiKey: auth,
        endpoint: "/campaigns",
        method: import_pieces_common2.HttpMethod.GET
      })).data) == null ? void 0 : _a.campaigns).map((c) => ({
        label: c.name,
        value: c.id
      }))
    } : {
      disabled: !0,
      placeholder: "Connect your Hunter account first.",
      options: []
    };
  }
}), emailsProp = import_pieces_framework2.Property.Array({
  displayName: "Emails",
  required: !1,
  description: "Email addresses to add as recipients. At least one email or lead ID is required.",
  defaultValue: []
}), leadIdsProp = import_pieces_framework2.Property.Array({
  displayName: "Lead IDs",
  required: !1,
  description: "Existing lead IDs from your Hunter account to add as recipients.",
  defaultValue: []
}), leadIdsDropdownProp = import_pieces_framework2.Property.MultiSelectDropdown({
  displayName: "Leads",
  required: !1,
  auth: hunterAuth,
  description: "Select leads from your Hunter account to add as recipients.",
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    return auth ? {
      disabled: !1,
      options: (((_a = (await hunterApiCall({
        apiKey: auth,
        endpoint: "/leads",
        method: import_pieces_common2.HttpMethod.GET,
        qparams: { limit: "100" }
      })).data) == null ? void 0 : _a.leads) || []).map((lead) => ({
        label: `${lead.first_name || ""} ${lead.last_name || ""} (${lead.email})`.trim(),
        value: lead.id
      }))
    } : {
      disabled: !0,
      placeholder: "Connect your Hunter account first.",
      options: []
    };
  }
}), domainProp = import_pieces_framework2.Property.ShortText({
  displayName: "Domain",
  required: !1,
  description: 'Domain name to count addresses for (e.g., "stripe.com"). At least one of domain or company is required.'
}), companyProp = import_pieces_framework2.Property.ShortText({
  displayName: "Company",
  required: !1,
  description: 'Company name to count addresses for (e.g., "Stripe"). At least one of domain or company is required.'
}), companyLeadProp = import_pieces_framework2.Property.ShortText({
  displayName: "Company",
  required: !1,
  description: "Name of the company the lead is working in."
}), emailTypeProp = import_pieces_framework2.Property.Dropdown({
  auth: hunterAuth,
  displayName: "Type",
  required: !1,
  description: 'Count only "personal" or "generic" email addresses. Leave blank for both.',
  options: async () => ({
    disabled: !1,
    options: [
      { label: "Personal", value: "personal" },
      { label: "Generic", value: "generic" }
    ]
  }),
  refreshers: []
}), emailProp = import_pieces_framework2.Property.ShortText({
  displayName: "Email",
  required: !0,
  description: "The email address of the lead."
}), firstNameProp = import_pieces_framework2.Property.ShortText({
  displayName: "First Name",
  required: !1
}), lastNameProp = import_pieces_framework2.Property.ShortText({
  displayName: "Last Name",
  required: !1
}), positionProp = import_pieces_framework2.Property.ShortText({
  displayName: "Position",
  required: !1
}), companyIndustryProp = import_pieces_framework2.Property.ShortText({
  displayName: "Company Industry",
  required: !1,
  description: "Sector of the company (e.g. Finance, Technology, Education, etc.)."
}), companySizeProp = import_pieces_framework2.Property.ShortText({
  displayName: "Company Size",
  required: !1,
  description: "Size of the company (e.g. 1-10, 201-500 employees)."
}), confidenceScoreProp = import_pieces_framework2.Property.Number({
  displayName: "Confidence Score",
  required: !1,
  description: "Probability the email is correct (0\u2013100)."
}), websiteProp = import_pieces_framework2.Property.ShortText({
  displayName: "Website",
  required: !1,
  description: "Domain name of the company."
}), countryCodeProp = import_pieces_framework2.Property.ShortText({
  displayName: "Country Code",
  required: !1,
  description: "ISO 3166-1 alpha-2 country code."
}), linkedinUrlProp = import_pieces_framework2.Property.ShortText({
  displayName: "LinkedIn URL",
  required: !1
}), phoneNumberProp = import_pieces_framework2.Property.ShortText({
  displayName: "Phone Number",
  required: !1
}), twitterProp = import_pieces_framework2.Property.ShortText({
  displayName: "Twitter Handle",
  required: !1
}), notesProp = import_pieces_framework2.Property.ShortText({
  displayName: "Notes",
  required: !1,
  description: "Personal notes about the lead."
}), sourceProp = import_pieces_framework2.Property.ShortText({
  displayName: "Source",
  required: !1,
  description: "Origin where the lead was found."
}), leadsListIdProp = import_pieces_framework2.Property.Number({
  displayName: "Leads List ID",
  required: !1,
  description: "ID of the list to add the lead to; defaults to your most recent list."
}), leadsListIdsProp = import_pieces_framework2.Property.Array({
  displayName: "Leads List IDs",
  required: !1,
  description: "Array of list IDs to add the lead to; defaults to your most recent list.",
  defaultValue: []
}), leadsListDropdownProp = import_pieces_framework2.Property.Dropdown({
  auth: hunterAuth,
  displayName: "Leads List",
  required: !1,
  description: "Select which list to add the lead to; defaults to your most recent list.",
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Hunter account first.",
        options: []
      };
    let leads = ((_a = (await hunterApiCall({
      apiKey: auth,
      endpoint: "/leads",
      method: import_pieces_common2.HttpMethod.GET,
      qparams: { limit: "100" }
    })).data) == null ? void 0 : _a.leads) || [], uniqueLists = /* @__PURE__ */ new Map();
    return leads.forEach((lead) => {
      lead.leads_list && uniqueLists.set(lead.leads_list.id, lead.leads_list);
    }), {
      disabled: !1,
      options: Array.from(uniqueLists.values()).map((list) => ({
        label: `${list.name} (${list.leads_count} leads)`,
        value: list.id
      }))
    };
  }
}), customAttributesProp = import_pieces_framework2.Property.Json({
  displayName: "Custom Attributes",
  required: !1,
  description: 'Key/value map of any custom attributes, e.g. { "customer_id": "cus-1234abcd" }.'
}), leadIdProp = import_pieces_framework2.Property.Number({
  displayName: "Lead ID",
  required: !0,
  description: "Identifier of the lead to delete."
}), leadSelectDropdownProp = import_pieces_framework2.Property.Dropdown({
  auth: hunterAuth,
  displayName: "Lead",
  required: !0,
  description: "Select a lead from your Hunter account.",
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    return auth ? {
      disabled: !1,
      options: (((_a = (await hunterApiCall({
        apiKey: auth,
        endpoint: "/leads",
        method: import_pieces_common2.HttpMethod.GET,
        qparams: { limit: "100" }
      })).data) == null ? void 0 : _a.leads) || []).map((lead) => ({
        label: `${lead.first_name || ""} ${lead.last_name || ""} (${lead.email})`.trim(),
        value: lead.id
      }))
    } : {
      disabled: !0,
      placeholder: "Connect your Hunter account first.",
      options: []
    };
  }
}), leadDeleteDropdownProp = import_pieces_framework2.Property.Dropdown({
  auth: hunterAuth,
  displayName: "Lead to Delete",
  required: !0,
  description: "Select the lead to delete from your Hunter account.",
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    return auth ? {
      disabled: !1,
      options: (((_a = (await hunterApiCall({
        apiKey: auth,
        endpoint: "/leads",
        method: import_pieces_common2.HttpMethod.GET,
        qparams: { limit: "100" }
      })).data) == null ? void 0 : _a.leads) || []).map((lead) => ({
        label: `${lead.first_name || ""} ${lead.last_name || ""} (${lead.email})`.trim(),
        value: lead.id
      }))
    } : {
      disabled: !0,
      placeholder: "Connect your Hunter account first.",
      options: []
    };
  }
}), fullNameProp = import_pieces_framework2.Property.ShortText({
  displayName: "Full Name",
  required: !1,
  description: "The person's full name (if you can't supply both first and last name)."
}), maxDurationProp = import_pieces_framework2.Property.Number({
  displayName: "Max Duration (seconds)",
  required: !1,
  description: "How long Hunter should spend refining results (3-20, default 10)."
}), filterProp = (name, description) => import_pieces_framework2.Property.ShortText({ displayName: name, required: !1, description }), emailFilterProp = filterProp(
  "Email",
  'Filter by email (use "*" for any, "~" for empty, or substring).'
), firstNameFilterProp = filterProp(
  "First Name",
  "Filter by first name."
), lastNameFilterProp = filterProp(
  "Last Name",
  "Filter by last name."
), positionFilterProp = filterProp(
  "Position",
  "Filter by position."
), companyFilterProp = filterProp(
  "Company",
  "Filter by company."
), industryFilterProp = filterProp(
  "Industry",
  "Filter by industry."
), websiteFilterProp = filterProp(
  "Website",
  "Filter by website."
), countryCodeFilterProp = filterProp(
  "Country Code",
  "Filter by ISO\u202F3166\u20111 alpha\u20112 country code."
), companySizeFilterProp = filterProp(
  "Company Size",
  "Filter by company size."
), sourceFilterProp = filterProp(
  "Source",
  "Filter by source."
), twitterFilterProp = filterProp(
  "Twitter Handle",
  "Filter by Twitter handle."
), linkedinUrlFilterProp = filterProp(
  "LinkedIn URL",
  "Filter by LinkedIn URL."
), phoneNumberFilterProp = filterProp(
  "Phone Number",
  "Filter by phone number."
), syncStatusProp = import_pieces_framework2.Property.Dropdown({
  displayName: "Sync Status",
  required: !1,
  description: "Filter by synchronization status.",
  auth: hunterAuth,
  options: async () => ({
    disabled: !1,
    options: [
      { label: "Pending", value: "pending" },
      { label: "Error", value: "error" },
      { label: "Success", value: "success" }
    ]
  }),
  refreshers: []
}), arrayStringProp = (name, description) => import_pieces_framework2.Property.Array({
  displayName: name,
  required: !1,
  description,
  defaultValue: []
}), sendingStatusProp = arrayStringProp(
  "Sending Status(es)",
  'Filter by sending status(es): clicked, opened, sent, pending, error, bounced, unsubscribed, replied, or "~" (unset).'
), verificationStatusProp = arrayStringProp(
  "Verification Status(es)",
  'Filter by verification status(es): accept_all, disposable, invalid, unknown, valid, webmail, or "pending".'
), dateFilterProp = filterProp(
  "Activity/Contact Date",
  'Use "*" for any value or "~" for unset.'
), customAttributesFilterProp = import_pieces_framework2.Property.Json({
  displayName: "Custom Attributes Filter",
  required: !1,
  description: 'JSON object of slug\u2192filterValue (use "*", "~", or substrings).'
}), queryProp = import_pieces_framework2.Property.ShortText({
  displayName: "Query",
  required: !1,
  description: "Search first_name, last_name or email containing this substring."
}), limitProp = import_pieces_framework2.Property.Number({
  displayName: "Limit",
  required: !1,
  description: "Max leads to return (1-1000). Defaults to 20."
}), offsetProp = import_pieces_framework2.Property.Number({
  displayName: "Offset",
  required: !1,
  description: "Number of leads to skip (0-100000). Defaults to 0."
});

// ../ap/packages/pieces/community/hunter/src/lib/actions/add-recipients.ts
var MAX_EMAILS = 50, MAX_LEAD_IDS = 50, addRecipientsAction = (0, import_pieces_framework3.createAction)({
  auth: hunterAuth,
  name: "add-recipients",
  displayName: "Add Recipients",
  description: "Add one or multiple recipients to a campaign.",
  audience: "both",
  aiMetadata: { description: "Adds one or more recipients to a Hunter campaign, identified either by raw email addresses, by existing lead IDs, or both. Use to enroll contacts into an outreach campaign once you have the campaign ID. Requires at least one valid email or lead ID (max 50 of each per call); not idempotent \u2014 each call appends recipients, though Hunter skips duplicates already in the campaign.", idempotent: !1 },
  props: {
    campaign_id: campaignIdProp,
    emails: emailsProp,
    lead_ids: leadIdsDropdownProp
  },
  async run(context) {
    let {
      campaign_id,
      emails: rawEmails = [],
      lead_ids: rawLeadIds = []
    } = context.propsValue, stringEmails = rawEmails.filter(
      (e) => typeof e == "string"
    ), numberLeadIds = rawLeadIds.filter(
      (id) => typeof id == "number"
    ), filteredEmails = stringEmails.map((e) => e.trim()).filter((e) => e.length > 0);
    if (filteredEmails.length === 0 && numberLeadIds.length === 0)
      throw new Error(
        "You must provide at least one valid email or one lead ID."
      );
    if (filteredEmails.length > MAX_EMAILS)
      throw new Error(`You can add at most ${MAX_EMAILS} emails at once.`);
    if (numberLeadIds.length > MAX_LEAD_IDS)
      throw new Error(`You can add at most ${MAX_LEAD_IDS} lead IDs at once.`);
    let body = {};
    filteredEmails.length && (body.emails = filteredEmails), numberLeadIds.length && (body.lead_ids = numberLeadIds);
    let resp = await hunterApiCall({
      apiKey: context.auth,
      endpoint: `/campaigns/${campaign_id}/recipients`,
      method: import_pieces_common3.HttpMethod.POST,
      body
    });
    return {
      recipients_added: resp.data.recipients_added,
      skipped_recipients: resp.data.skipped_recipients
    };
  }
});

// ../ap/packages/pieces/community/hunter/src/lib/actions/count-emails.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var countEmailsAction = (0, import_pieces_framework4.createAction)({
  auth: hunterAuth,
  name: "count-emails",
  displayName: "Count Emails",
  description: "Returns the number of email addresses found for a domain or company.",
  audience: "both",
  aiMetadata: { description: "Returns how many email addresses Hunter knows for a given domain or company, broken down by department and seniority, and optionally filtered to personal or generic addresses. Use to gauge coverage before running an email search or to estimate prospecting volume. Provide either a domain or a company name; this is a read-only lookup that consumes no credits and is idempotent.", idempotent: !0 },
  props: {
    domain: domainProp,
    company: companyProp,
    type: emailTypeProp
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let { domain, company, type } = context.propsValue;
    if (!domain && !company)
      throw new Error(
        "You must provide at least a domain or a company name to count emails."
      );
    let qparams = {};
    domain && (qparams.domain = domain), !domain && company && (qparams.company = company), type && (qparams.type = type);
    let respBody;
    try {
      respBody = await hunterApiCall({
        apiKey: context.auth,
        endpoint: "/email-count",
        method: import_pieces_common4.HttpMethod.GET,
        qparams
      });
    } catch (err) {
      let httpErr = err, status = (_a = httpErr.response) == null ? void 0 : _a.status, details = ((_e = (_d = (_c = (_b = httpErr.response) == null ? void 0 : _b.body) == null ? void 0 : _c.errors) == null ? void 0 : _d[0]) == null ? void 0 : _e.details) || "Unknown error";
      throw status === 400 && details.includes("wrong_params") ? new Error("Missing domain or company parameter.") : status === 400 && details.includes("invalid_type") ? new Error(
        'Invalid type. Must be "personal" or "generic", exactly as shown.'
      ) : status === 429 ? new Error(
        "Rate limit exceeded (15 req/s). Please wait a moment and try again."
      ) : new Error(`Hunter Email Count API error: ${details}`);
    }
    let data = respBody.data;
    return {
      total: data.total,
      personal_emails: data.personal_emails,
      generic_emails: data.generic_emails,
      department: data.department,
      seniority: data.seniority
    };
  }
});

// ../ap/packages/pieces/community/hunter/src/lib/actions/create-lead.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createLeadAction = (0, import_pieces_framework5.createAction)({
  auth: hunterAuth,
  name: "create-lead",
  displayName: "Create Lead",
  description: "Create and store a lead record.",
  audience: "both",
  aiMetadata: { description: "Creates a new lead record in the Hunter account with contact and company details, optionally assigning it to a leads list and setting custom attributes. Use to persist a prospect for later outreach or tracking. Not idempotent \u2014 each call creates a separate lead even with identical input.", idempotent: !1 },
  props: {
    email: emailProp,
    first_name: firstNameProp,
    last_name: lastNameProp,
    position: positionProp,
    company: companyLeadProp,
    company_industry: companyIndustryProp,
    company_size: companySizeProp,
    confidence_score: confidenceScoreProp,
    website: websiteProp,
    country_code: countryCodeProp,
    linkedin_url: linkedinUrlProp,
    phone_number: phoneNumberProp,
    twitter: twitterProp,
    notes: notesProp,
    source: sourceProp,
    leads_list_id: leadsListDropdownProp,
    leads_list_ids: leadsListIdsProp,
    custom_attributes: customAttributesProp
  },
  async run(context) {
    let props = context.propsValue, body = {};
    for (let [key, val] of Object.entries(props))
      val != null && !(Array.isArray(val) && val.length === 0 || typeof val == "string" && val === "") && (body[key] = val);
    return { lead: (await hunterApiCall({
      apiKey: context.auth,
      endpoint: "/leads",
      method: import_pieces_common5.HttpMethod.POST,
      body
    })).data };
  }
});

// ../ap/packages/pieces/community/hunter/src/lib/actions/delete-lead.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var deleteLeadAction = (0, import_pieces_framework6.createAction)({
  auth: hunterAuth,
  name: "delete-lead",
  displayName: "Delete Lead",
  description: "Delete a specific lead record by ID.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a single lead from the Hunter account by its lead ID. Use to remove a prospect that is no longer relevant. Requires the lead ID; idempotent in effect since re-deleting an already-removed lead leaves the account in the same state.", idempotent: !0 },
  props: {
    lead_id: leadDeleteDropdownProp
  },
  async run(context) {
    let { lead_id } = context.propsValue;
    return await hunterApiCall({
      apiKey: context.auth,
      endpoint: `/leads/${lead_id}`,
      method: import_pieces_common6.HttpMethod.DELETE
    }), { success: !0 };
  }
});

// ../ap/packages/pieces/community/hunter/src/lib/actions/find-email.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var findEmailAction = (0, import_pieces_framework7.createAction)({
  auth: hunterAuth,
  name: "find-email",
  displayName: "Find Email",
  description: "Retrieve/propose the most likely email for a person at a domain.",
  audience: "both",
  aiMetadata: { description: "Finds the most likely professional email address for a named person at a given company, returning a confidence score and supporting sources. Use to discover an unknown contact email from a name plus an organization. Requires either a domain or a company name, plus either both first and last name or a full name; this is a read-only lookup and is idempotent for the same input.", idempotent: !0 },
  props: {
    domain: domainProp,
    company: companyProp,
    first_name: firstNameProp,
    last_name: lastNameProp,
    full_name: fullNameProp,
    max_duration: maxDurationProp
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
    let {
      domain,
      company,
      first_name,
      last_name,
      full_name,
      max_duration
    } = context.propsValue;
    if (!domain && !company)
      throw new Error("You must provide at least a domain or a company name.");
    if (!(first_name && last_name || full_name))
      throw new Error(
        "You must provide both first_name and last_name, or a full_name."
      );
    if (max_duration !== void 0 && (max_duration < 3 || max_duration > 20))
      throw new Error("max_duration must be between 3 and 20 seconds.");
    let qparams = {};
    domain ? qparams.domain = domain : company && (qparams.company = company), first_name && (qparams.first_name = first_name), last_name && (qparams.last_name = last_name), full_name && (qparams.full_name = full_name), max_duration !== void 0 && (qparams.max_duration = String(max_duration));
    let responseBody;
    try {
      responseBody = await hunterApiCall({
        apiKey: context.auth,
        endpoint: "/email-finder",
        method: import_pieces_common7.HttpMethod.GET,
        qparams
      });
    } catch (err) {
      let httpErr = err, status = (_a = httpErr.response) == null ? void 0 : _a.status, errId = (_e = (_d = (_c = (_b = httpErr.response) == null ? void 0 : _b.body) == null ? void 0 : _c.errors) == null ? void 0 : _d[0]) == null ? void 0 : _e.id, details = (_j = (_i = (_h = (_g = (_f = httpErr.response) == null ? void 0 : _f.body) == null ? void 0 : _g.errors) == null ? void 0 : _h[0]) == null ? void 0 : _i.details) != null ? _j : "";
      if (status === 400)
        switch (errId) {
          case "wrong_params":
            throw new Error(
              "Missing required parameter\u2014please supply domain or company, and first_name+last_name or full_name."
            );
          case "invalid_first_name":
            throw new Error("The supplied first_name is invalid.");
          case "invalid_last_name":
            throw new Error("The supplied last_name is invalid.");
          case "invalid_full_name":
            throw new Error("The supplied full_name is invalid.");
          case "invalid_domain":
            throw new Error(
              "The supplied domain is invalid or lacks MX records."
            );
          case "invalid_max_duration":
            throw new Error(
              "The supplied max_duration is invalid (must be 3\u201320 seconds)."
            );
        }
      throw status === 429 ? new Error(
        "Rate limit exceeded (15 requests/sec, 500 req/min). Please retry shortly."
      ) : status === 451 && errId === "claimed_email" ? new Error(
        "This email address cannot be processed because its owner requested to stop processing."
      ) : new Error(
        `Hunter Email Finder error: ${details} (status ${status}).`
      );
    }
    let d = responseBody.data;
    return {
      first_name: d.first_name,
      last_name: d.last_name,
      email: d.email,
      score: d.score,
      domain: d.domain,
      accept_all: d.accept_all,
      position: d.position,
      twitter: d.twitter,
      linkedin_url: d.linkedin_url,
      phone_number: d.phone_number,
      company: d.company,
      sources: d.sources,
      verification: d.verification
    };
  }
});

// ../ap/packages/pieces/community/hunter/src/lib/actions/get-lead.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var getLeadAction = (0, import_pieces_framework8.createAction)({
  auth: hunterAuth,
  name: "get-lead",
  displayName: "Get Lead",
  description: "Retrieve details of a specific lead.",
  audience: "both",
  aiMetadata: { description: "Retrieves the full record for a single lead by its lead ID, including contact, company, verification, and list details. Use to inspect a known lead before updating or acting on it. Requires the lead ID; read-only and idempotent.", idempotent: !0 },
  props: {
    lead_id: leadSelectDropdownProp
  },
  async run(context) {
    let { lead_id } = context.propsValue;
    return { lead: (await hunterApiCall({
      apiKey: context.auth,
      endpoint: `/leads/${lead_id}`,
      method: import_pieces_common8.HttpMethod.GET
    })).data };
  }
});

// ../ap/packages/pieces/community/hunter/src/lib/actions/search-leads.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var searchLeadsAction = (0, import_pieces_framework9.createAction)({
  auth: hunterAuth,
  name: "search-leads",
  displayName: "Search Leads",
  description: "List and filter leads in the account.",
  audience: "both",
  aiMetadata: { description: "Lists leads in the Hunter account, optionally narrowed by a wide set of filters (email, name, company, list, verification/sending status, activity dates, custom attributes, free-text query) with limit/offset paging; with no filters it returns all leads. Use to find leads matching criteria or to page through the account. Read-only and idempotent.", idempotent: !0 },
  props: {
    leads_list_id: leadsListDropdownProp,
    email: emailFilterProp,
    first_name: firstNameFilterProp,
    last_name: lastNameFilterProp,
    position: positionFilterProp,
    company: companyFilterProp,
    industry: industryFilterProp,
    website: websiteFilterProp,
    country_code: countryCodeFilterProp,
    company_size: companySizeFilterProp,
    source: sourceFilterProp,
    twitter: twitterFilterProp,
    linkedin_url: linkedinUrlFilterProp,
    phone_number: phoneNumberFilterProp,
    sync_status: syncStatusProp,
    sending_status: sendingStatusProp,
    verification_status: verificationStatusProp,
    last_activity_at: dateFilterProp,
    last_contacted_at: dateFilterProp,
    custom_attributes: customAttributesFilterProp,
    query: queryProp,
    limit: limitProp,
    offset: offsetProp
  },
  async run(context) {
    var _a, _b;
    let pv = context.propsValue, q = {};
    if (pv.limit !== void 0 && (q.limit = String(pv.limit)), pv.offset !== void 0 && (q.offset = String(pv.offset)), [
      "email",
      "first_name",
      "last_name",
      "position",
      "company",
      "industry",
      "website",
      "country_code",
      "company_size",
      "source",
      "twitter",
      "linkedin_url",
      "phone_number",
      "sync_status",
      "query"
    ].forEach((key) => {
      pv[key] && (q[key] = pv[key]);
    }), (_a = pv.sending_status) != null && _a.length && (q["sending_status[]"] = pv.sending_status.join(",")), (_b = pv.verification_status) != null && _b.length && (q["verification_status[]"] = pv.verification_status.join(",")), pv.last_activity_at && (q.last_activity_at = pv.last_activity_at), pv.last_contacted_at && (q.last_contacted_at = pv.last_contacted_at), pv.leads_list_id !== void 0 && (q.leads_list_id = String(pv.leads_list_id)), pv.custom_attributes) {
      let ca = pv.custom_attributes;
      Object.entries(ca).forEach(([slug, val]) => {
        q[`custom_attributes[${slug}]`] = val;
      });
    }
    let resp = await hunterApiCall({
      apiKey: context.auth,
      endpoint: "/leads",
      method: import_pieces_common9.HttpMethod.GET,
      qparams: q
    });
    return {
      leads: resp.data.leads,
      count: resp.meta.count,
      total: resp.meta.total,
      limit: resp.meta.params.limit,
      offset: resp.meta.params.offset
    };
  }
});

// ../ap/packages/pieces/community/hunter/src/lib/actions/update-lead.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var updateLeadAction = (0, import_pieces_framework10.createAction)({
  auth: hunterAuth,
  name: "update-lead",
  displayName: "Update Lead",
  description: "Modify existing lead data.",
  audience: "both",
  aiMetadata: { description: "Updates fields on an existing lead identified by its lead ID, overwriting only the provided contact, company, list, or custom-attribute values. Use to correct or enrich a known lead. Requires the lead ID and at least one field to change; idempotent since re-sending the same values yields the same final state.", idempotent: !0 },
  props: {
    lead_id: leadSelectDropdownProp,
    email: emailProp,
    first_name: firstNameProp,
    last_name: lastNameProp,
    position: positionProp,
    company: companyLeadProp,
    company_industry: companyIndustryProp,
    company_size: companySizeProp,
    confidence_score: confidenceScoreProp,
    website: websiteProp,
    country_code: countryCodeProp,
    linkedin_url: linkedinUrlProp,
    phone_number: phoneNumberProp,
    twitter: twitterProp,
    notes: notesProp,
    source: sourceProp,
    leads_list_id: leadsListDropdownProp,
    leads_list_ids: leadsListIdsProp,
    custom_attributes: customAttributesProp
  },
  async run(context) {
    let props = context.propsValue, leadId = props.lead_id, body = {};
    for (let [key, value] of Object.entries(props))
      key !== "lead_id" && value != null && !(Array.isArray(value) && value.length === 0 || typeof value == "string" && value === "") && (body[key] = value);
    if (Object.keys(body).length === 0)
      throw new Error("You must specify at least one field to update.");
    return await hunterApiCall({
      apiKey: context.auth,
      endpoint: `/leads/${leadId}`,
      method: import_pieces_common10.HttpMethod.PUT,
      body
    }), { success: !0 };
  }
});

// ../ap/packages/pieces/community/hunter/src/lib/actions/verify-email.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var verifyEmailAction = (0, import_pieces_framework11.createAction)({
  auth: hunterAuth,
  name: "verify-email",
  displayName: "Verify Email",
  description: "Check email deliverability and validation status.",
  audience: "both",
  aiMetadata: { description: "Verifies a single email address against Hunter, returning deliverability status, a confidence score, and signals such as MX records, SMTP check, disposable/webmail flags, and accept-all. Use to validate an address before sending. Requires the email; read-only and idempotent, though verification may occasionally be reported as still in progress.", idempotent: !0 },
  props: {
    email: emailProp
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
    let { email } = context.propsValue, qparams = { email }, respBody;
    try {
      respBody = await hunterApiCall({
        apiKey: context.auth,
        endpoint: "/email-verifier",
        method: import_pieces_common11.HttpMethod.GET,
        qparams
      });
    } catch (err) {
      let httpErr = err, status = (_a = httpErr.response) == null ? void 0 : _a.status, errId = (_e = (_d = (_c = (_b = httpErr.response) == null ? void 0 : _b.body) == null ? void 0 : _c.errors) == null ? void 0 : _d[0]) == null ? void 0 : _e.id;
      if (status === 202)
        return { status: "in_progress" };
      if (status === 222)
        throw new Error(
          "Verification failed due to an unexpected SMTP server error. Please try again later."
        );
      if (status === 400)
        switch (errId) {
          case "wrong_params":
            throw new Error("Missing required parameter: email.");
          case "invalid_email":
            throw new Error("The supplied email is invalid.");
        }
      if (status === 429)
        throw new Error(
          "Rate limit exceeded (10 requests/sec, 300 req/min). Please wait and retry shortly."
        );
      if (status === 451 && errId === "claimed_email")
        throw new Error(
          "Cannot verify: the email owner requested no processing of their data."
        );
      let detail = (_j = (_i = (_h = (_g = (_f = httpErr.response) == null ? void 0 : _f.body) == null ? void 0 : _g.errors) == null ? void 0 : _h[0]) == null ? void 0 : _i.details) != null ? _j : "";
      throw new Error(`Hunter Email Verifier error: ${detail} (status ${status}).`);
    }
    let data = respBody.data;
    return data ? {
      status: data.status,
      result: data.result,
      score: data.score,
      email: data.email,
      regexp: data.regexp,
      gibberish: data.gibberish,
      disposable: data.disposable,
      webmail: data.webmail,
      mx_records: data.mx_records,
      smtp_server: data.smtp_server,
      smtp_check: data.smtp_check,
      accept_all: data.accept_all,
      block: data.block,
      sources: data.sources
    } : { status: "in_progress" };
  }
});

// ../ap/packages/pieces/community/hunter/src/lib/triggers/new-lead.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var newLeadTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: hunterAuth,
  name: "new-lead",
  displayName: "New Lead",
  description: "Fires when a new lead is created.",
  aiMetadata: {
    description: "Fires when a new lead is created in the connected Hunter account, emitting that lead record. Polls the account leads list and detects entries by creation time."
  },
  type: import_pieces_framework12.TriggerStrategy.POLLING,
  props: {},
  async onEnable(context) {
    await import_pieces_common12.pollingHelper.onEnable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common12.pollingHelper.onDisable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async test(context) {
    return await import_pieces_common12.pollingHelper.test(polling, context);
  },
  async run(context) {
    return await import_pieces_common12.pollingHelper.poll(polling, context);
  },
  sampleData: {
    id: 123,
    email: "john.doe@example.com",
    first_name: "John",
    last_name: "Doe",
    company: "Example Inc.",
    position: "CEO",
    created_at: "2025-07-24T12:00:00Z"
  }
}), polling = {
  strategy: import_pieces_common12.DedupeStrategy.TIMEBASED,
  async items({ auth }) {
    var _a;
    return ((_a = (await hunterApiCall({
      apiKey: auth,
      endpoint: "/leads",
      method: import_pieces_common12.HttpMethod.GET,
      qparams: { limit: "100" }
    })).data) == null ? void 0 : _a.leads).map((lead) => ({
      epochMilliSeconds: new Date(lead.created_at).valueOf(),
      data: lead
    }));
  }
};

// ../ap/packages/pieces/community/hunter/src/index.ts
var hunter = (0, import_pieces_framework13.createPiece)({
  displayName: "Hunter",
  description: "Find, verify and manage professional email addresses at scale. Automate email discovery, validation, lead tracking, and campaign outreach with Hunter.io.",
  auth: hunterAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/hunter.png",
  authors: ["varshith257"],
  categories: [import_pieces_framework14.PieceCategory.SALES_AND_CRM],
  actions: [
    addRecipientsAction,
    countEmailsAction,
    createLeadAction,
    deleteLeadAction,
    findEmailAction,
    getLeadAction,
    searchLeadsAction,
    updateLeadAction,
    verifyEmailAction
  ],
  triggers: [newLeadTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  hunter
});
