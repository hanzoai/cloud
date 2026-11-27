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

// ../ap/packages/pieces/community/what-converts/src/index.ts
var index_exports = {};
__export(index_exports, {
  whatConverts: () => whatConverts
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/what-converts/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), WHATCONVERTS_API_URL = "https://app.whatconverts.com/api/v1", whatConvertsAuthProps = {
  api_token: import_pieces_framework.Property.ShortText({
    displayName: "API Token",
    description: "Your WhatConverts API Token.",
    required: !0
  }),
  api_secret: import_pieces_framework.PieceAuth.SecretText({
    displayName: "API Secret",
    description: "Your WhatConverts API Secret.",
    required: !0
  })
}, whatConvertsAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `
  To get your API credentials:
  1. Log in to your WhatConverts dashboard.
  2. Navigate to an account and select a profile.
  3. Select the **Tracking** dropdown menu.
  4. Click on **Integrations**, then **API Keys**.
  5. Click **Generate API Key** to get your Token and Secret.
  `,
  required: !0,
  props: whatConvertsAuthProps,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${WHATCONVERTS_API_URL}/leads`,
        authentication: {
          type: import_pieces_common.AuthenticationType.BASIC,
          username: auth.api_token,
          password: auth.api_secret
        },
        queryParams: {
          per_page: "1"
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Token or Secret. Please check your credentials and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/what-converts/src/lib/actions/create-lead.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/what-converts/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/what-converts/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), WHATCONVERTS_API_URL2 = "https://app.whatconverts.com/api/v1", whatConvertsClient = {
  async getAccounts(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: `${WHATCONVERTS_API_URL2}/accounts`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BASIC,
        username: auth.props.api_token,
        password: auth.props.api_secret
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.accounts;
  },
  async getProfiles(auth, accountId) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: `${WHATCONVERTS_API_URL2}/accounts/${accountId}/profiles`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BASIC,
        username: auth.props.api_token,
        password: auth.props.api_secret
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.profiles;
  },
  async createLead(auth, accountId, params) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: `${WHATCONVERTS_API_URL2}/leads`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BASIC,
        username: auth.props.api_token,
        password: auth.props.api_secret
      },
      body: params,
      headers: {
        "Content-Type": "application/x-www-form-urlencoded"
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body;
  },
  async findLeads(auth, params) {
    let queryParams = {};
    Object.keys(params).forEach((key) => {
      let value = params[key];
      value != null && value !== "" && (queryParams[key] = value.toString());
    });
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: `${WHATCONVERTS_API_URL2}/leads`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BASIC,
        username: auth.props.api_token,
        password: auth.props.api_secret
      },
      queryParams
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body;
  },
  async updateLead(auth, leadId, params) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: `${WHATCONVERTS_API_URL2}/leads/${leadId}`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BASIC,
        username: auth.props.api_token,
        password: auth.props.api_secret
      },
      body: params
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body;
  }
};

// ../ap/packages/pieces/community/what-converts/src/lib/common/props.ts
var whatConvertsProps = {
  account_id: () => import_pieces_framework2.Property.Dropdown({
    auth: whatConvertsAuth,
    displayName: "Account",
    description: "The account to select a profile from.",
    required: !0,
    refreshers: ["auth"],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await whatConvertsClient.getAccounts(
        auth
      )).map((account) => ({
        label: account.account_name,
        value: account.account_id
      }))
    } : {
      disabled: !0,
      placeholder: "Please authenticate first.",
      options: []
    }
  }),
  profile_id: (required = !0) => import_pieces_framework2.Property.Dropdown({
    auth: whatConvertsAuth,
    displayName: "Profile",
    description: "The profile to search for leads in.",
    required,
    refreshers: ["auth", "account_id"],
    options: async ({ auth, account_id }) => !auth || !account_id ? {
      disabled: !0,
      placeholder: "Please select an account first.",
      options: []
    } : {
      disabled: !1,
      options: (await whatConvertsClient.getProfiles(
        auth,
        account_id
      )).map((profile) => ({
        label: profile.profile_name,
        value: profile.profile_id
      }))
    }
  }),
  lead_id: () => import_pieces_framework2.Property.Dropdown({
    auth: whatConvertsAuth,
    displayName: "Lead",
    description: "The lead to update.",
    required: !0,
    refreshers: ["auth", "profile_id"],
    options: async ({ auth, profile_id }) => !auth || !profile_id ? {
      disabled: !0,
      placeholder: "Please select a profile first.",
      options: []
    } : {
      disabled: !1,
      options: ((await whatConvertsClient.findLeads(
        auth,
        {
          profile_id,
          per_page: 50
        }
      )).leads || []).map((lead) => {
        var _a, _b;
        let emailDetail = (_a = lead.lead_details) == null ? void 0 : _a.find(
          (d) => d.label === "Email"
        ), nameDetail = (_b = lead.lead_details) == null ? void 0 : _b.find(
          (d) => d.label === "First Name"
        );
        return {
          label: `${(nameDetail == null ? void 0 : nameDetail.value) || (emailDetail == null ? void 0 : emailDetail.value) || lead.lead_type} (ID: ${lead.lead_id})`,
          value: lead.lead_id
        };
      })
    }
  })
};

// ../ap/packages/pieces/community/what-converts/src/lib/actions/create-lead.ts
var createLeadAction = (0, import_pieces_framework3.createAction)({
  auth: whatConvertsAuth,
  name: "create_lead",
  displayName: "Create Lead",
  description: "Create a new lead in WhatConverts.",
  audience: "both",
  aiMetadata: { description: "Creates a new lead in a WhatConverts account, capturing contact details, lead type, attribution (source/medium), and optional quote/sales values. Use when an agent needs to record an inbound lead or conversion in WhatConverts. Requires the target account and profile; the lead type is mandatory. Not idempotent \u2014 each call creates a separate lead and can optionally send a notification email.", idempotent: !1 },
  props: {
    account_id: whatConvertsProps.account_id(),
    profile_id: whatConvertsProps.profile_id(),
    lead_type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Lead Type",
      description: "The type of the lead.",
      required: !0,
      options: {
        options: [
          { label: "Phone Call", value: "phone_call" },
          { label: "Web Form", value: "web_form" },
          { label: "Chat", value: "chat" },
          { label: "Transaction", value: "transaction" },
          { label: "Event", value: "event" },
          { label: "Appointment", value: "appointment" },
          { label: "Text Message", value: "text_message" }
        ]
      }
    }),
    first_name: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    phone_number: import_pieces_framework3.Property.ShortText({
      displayName: "Phone Number",
      required: !1
    }),
    company_name: import_pieces_framework3.Property.ShortText({
      displayName: "Company Name",
      required: !1
    }),
    notes: import_pieces_framework3.Property.LongText({
      displayName: "Notes",
      required: !1
    }),
    referring_source: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Referring Source",
      description: 'The source of the lead (e.g., "google").',
      required: !1,
      options: {
        options: [
          { label: "Google", value: "google" },
          { label: "Bing", value: "bing" },
          { label: "Facebook", value: "facebook" },
          { label: "LinkedIn", value: "linkedin" },
          { label: "Twitter", value: "twitter" },
          { label: "Instagram", value: "instagram" },
          { label: "Direct Traffic", value: "(direct)" }
        ]
      }
    }),
    referring_medium: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Referring Medium",
      description: 'The medium of the lead (e.g., "cpc").',
      required: !1,
      options: {
        options: [
          { label: "Organic Search", value: "organic" },
          { label: "Paid Search (CPC)", value: "cpc" },
          { label: "Social Media", value: "social" },
          { label: "Email", value: "email" },
          { label: "Referral", value: "referral" },
          { label: "Display Ad", value: "display" },
          { label: "None (for Direct Traffic)", value: "(none)" }
        ]
      }
    }),
    send_notification: import_pieces_framework3.Property.Checkbox({
      displayName: "Send Notification",
      description: "Set to true to send a new lead notification email.",
      required: !0,
      defaultValue: !1
    }),
    date_created: import_pieces_framework3.Property.DateTime({
      displayName: "Creation Date",
      description: "The date and time when the lead was created.",
      required: !1
    }),
    quotable: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Quotable",
      description: "Indicates if the lead is quotable.",
      required: !1,
      options: {
        options: [
          { label: "Yes", value: "yes" },
          { label: "No", value: "no" },
          { label: "Pending", value: "pending" },
          { label: "Not Set", value: "not_set" }
        ]
      }
    }),
    quote_value: import_pieces_framework3.Property.Number({
      displayName: "Quote Value",
      description: "The monetary value of the quote associated with the lead.",
      required: !1
    }),
    sales_value: import_pieces_framework3.Property.Number({
      displayName: "Sales Value",
      description: "The monetary value of the sale associated with the lead.",
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue } = context;
    if (propsValue.account_id === void 0)
      throw new Error("Account ID is required.");
    if (propsValue.profile_id === void 0)
      throw new Error("Profile ID is required.");
    let contactName = [propsValue.first_name, propsValue.last_name].filter(Boolean).join(" ").trim();
    return await whatConvertsClient.createLead(auth, propsValue.account_id, {
      profile_id: propsValue.profile_id,
      lead_type: propsValue.lead_type,
      send_notification: propsValue.send_notification,
      referring_source: propsValue.referring_source,
      referring_medium: propsValue.referring_medium,
      contact_name: contactName || void 0,
      email_address: propsValue.email,
      phone_number: propsValue.phone_number,
      company_name: propsValue.company_name,
      notes: propsValue.notes,
      date_created: propsValue.date_created,
      quotable: propsValue.quotable,
      quote_value: propsValue.quote_value,
      sales_value: propsValue.sales_value
    });
  }
});

// ../ap/packages/pieces/community/what-converts/src/lib/actions/create-export.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var exportLeadsAction = (0, import_pieces_framework4.createAction)({
  auth: whatConvertsAuth,
  name: "export_leads",
  displayName: "Export Leads",
  description: "Retrieves all leads from an account, handling pagination automatically.",
  audience: "both",
  aiMetadata: { description: "Retrieves leads for a WhatConverts profile in bulk, automatically paging through results up to the configured page count. Use when an agent needs to export or analyze many leads at once, optionally narrowed by date range and lead type. Requires a profile; an empty date/type filter returns all leads. Read-only and idempotent.", idempotent: !0 },
  props: {
    account_id: whatConvertsProps.account_id(),
    profile_id: whatConvertsProps.profile_id(),
    from_date: import_pieces_framework4.Property.ShortText({
      displayName: "Start Date",
      description: "The start date for the export range in YYYY-MM-DD format.",
      required: !1
    }),
    to_date: import_pieces_framework4.Property.ShortText({
      displayName: "End Date",
      description: "The end date for the export range in YYYY-MM-DD format.",
      required: !1
    }),
    lead_type: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Lead Type (Optional)",
      description: "Filter leads by their type.",
      required: !1,
      options: {
        options: [
          { label: "Appointment", value: "appointment" },
          { label: "Chat", value: "chat" },
          { label: "Email", value: "email" },
          { label: "Event", value: "event" },
          { label: "Other", value: "other" },
          { label: "Phone Call", value: "phone_call" },
          { label: "Text Message", value: "text_message" },
          { label: "Transaction", value: "transaction" },
          { label: "Web Form", value: "web_form" }
        ]
      }
    }),
    per_page: import_pieces_framework4.Property.Number({
      displayName: "Leads Per Page",
      description: "Number of leads to fetch per page (max 1000).",
      required: !1,
      defaultValue: 25
    }),
    no_of_pages: import_pieces_framework4.Property.Number({
      displayName: "Number of Pages",
      description: "The number of pages to retrieve (max 100).",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    let { auth, propsValue } = context, { profile_id, from_date, to_date, lead_type, per_page } = propsValue;
    if (profile_id === void 0)
      throw new Error("Profile ID is required for exporting leads.");
    let initialResponse = await whatConvertsClient.findLeads(auth, {
      profile_id,
      from_date,
      to_date,
      lead_type,
      per_page,
      page_number: 1
    }), allLeads = initialResponse.leads || [], totalPages = Math.min(
      initialResponse.total_pages || 1,
      propsValue.no_of_pages || 1,
      100
    );
    for (let page = 2; page <= totalPages; page++) {
      let response = await whatConvertsClient.findLeads(auth, {
        profile_id,
        from_date,
        to_date,
        lead_type,
        per_page,
        page_number: page
      });
      allLeads = allLeads.concat(response.leads || []);
    }
    return allLeads;
  }
});

// ../ap/packages/pieces/community/what-converts/src/lib/actions/update-lead.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var updateLeadAction = (0, import_pieces_framework5.createAction)({
  auth: whatConvertsAuth,
  name: "update_lead",
  displayName: "Update Lead",
  description: "Update an existing lead's attributes by its ID.",
  audience: "both",
  aiMetadata: { description: "Updates an existing WhatConverts lead identified by its lead ID, changing fields such as lead type, quotable status, sales value, contact details, and attribution. Use when an agent needs to enrich or correct a known lead. Only provided fields are changed; requires the lead ID. Idempotent \u2014 re-sending the same field values leaves the lead in the same state.", idempotent: !0 },
  props: {
    account_id: whatConvertsProps.account_id(),
    profile_id: whatConvertsProps.profile_id(!0),
    lead_id: whatConvertsProps.lead_id(),
    lead_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Lead Type",
      description: "The type of the lead.",
      required: !1,
      options: {
        options: [
          { label: "Phone Call", value: "Phone Call" },
          { label: "Web Form", value: "Web Form" },
          { label: "Chat", value: "Chat" },
          { label: "Transaction", value: "Transaction" },
          { label: "Event", value: "Event" }
        ]
      }
    }),
    quotable: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Quotable",
      description: "Mark the lead as quotable.",
      required: !1,
      options: {
        options: [
          { label: "Yes", value: "Yes" },
          { label: "No", value: "No" }
        ]
      }
    }),
    sales_value: import_pieces_framework5.Property.ShortText({
      displayName: "Sales Value",
      description: 'The monetary value of the lead (e.g., "150.00").',
      required: !1
    }),
    first_name: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    phone_number: import_pieces_framework5.Property.ShortText({
      displayName: "Phone Number",
      required: !1
    }),
    notes: import_pieces_framework5.Property.LongText({
      displayName: "Notes",
      required: !1
    }),
    referring_source: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Referring Source",
      description: 'The source of the lead (e.g., "google").',
      required: !1,
      options: {
        options: [
          { label: "Google", value: "google" },
          { label: "Bing", value: "bing" },
          { label: "Facebook", value: "facebook" },
          { label: "LinkedIn", value: "linkedin" },
          { label: "Twitter", value: "twitter" },
          { label: "Instagram", value: "instagram" },
          { label: "Direct Traffic", value: "(direct)" }
        ]
      }
    }),
    referring_medium: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Referring Medium",
      description: 'The medium of the lead (e.g., "cpc").',
      required: !1,
      options: {
        options: [
          { label: "Organic Search", value: "organic" },
          { label: "Paid Search (CPC)", value: "cpc" },
          { label: "Social Media", value: "social" },
          { label: "Email", value: "email" },
          { label: "Referral", value: "referral" },
          { label: "Display Ad", value: "display" },
          { label: "None (for Direct Traffic)", value: "(none)" }
        ]
      }
    }),
    referring_campaign: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Referring Campaign",
      description: "The campaign that generated the lead.",
      required: !1,
      options: {
        options: [
          { label: "Summer Sale", value: "summer-sale" },
          { label: "Q4 Promotion", value: "q4-promo" },
          { label: "New Product Launch", value: "new-product-launch" },
          { label: "Brand Awareness", value: "brand-awareness" }
        ]
      }
    })
  },
  async run(context) {
    let { auth, propsValue } = context;
    if (propsValue.lead_id === void 0)
      throw new Error("Lead ID is required to update a lead.");
    let body = {}, leadDetails = {}, addIfExists = (obj, key, value) => {
      value != null && value !== "" && (obj[key] = value);
    };
    return addIfExists(body, "lead_type", propsValue.lead_type), addIfExists(body, "quotable", propsValue.quotable), addIfExists(body, "sales_value", propsValue.sales_value), addIfExists(body, "referring_source", propsValue.referring_source), addIfExists(body, "referring_medium", propsValue.referring_medium), addIfExists(body, "referring_campaign", propsValue.referring_campaign), addIfExists(leadDetails, "first_name", propsValue.first_name), addIfExists(leadDetails, "last_name", propsValue.last_name), addIfExists(leadDetails, "email", propsValue.email), addIfExists(leadDetails, "phone_number", propsValue.phone_number), addIfExists(leadDetails, "notes", propsValue.notes), Object.keys(leadDetails).length > 0 && (body.lead_details = leadDetails), await whatConvertsClient.updateLead(auth, propsValue.lead_id, body);
  }
});

// ../ap/packages/pieces/community/what-converts/src/lib/actions/find-lead.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var findLeadAction = (0, import_pieces_framework6.createAction)({
  auth: whatConvertsAuth,
  name: "find_lead",
  displayName: "Find Lead",
  description: "Find a lead by various criteria.",
  audience: "both",
  aiMetadata: { description: "Searches WhatConverts leads filtered by email address, phone number, and/or lead type, returning matching leads. Use when an agent needs to look up leads matching specific contact or type criteria. Requires at least one of email, phone, or lead type. Read-only and idempotent.", idempotent: !0 },
  props: {
    account_id: whatConvertsProps.account_id(),
    profile_id: whatConvertsProps.profile_id(),
    email_address: import_pieces_framework6.Property.ShortText({
      displayName: "Email Address",
      description: "Find a lead by their email address.",
      required: !1
    }),
    phone_number: import_pieces_framework6.Property.ShortText({
      displayName: "Phone Number",
      description: "Find a lead by their E.164 formatted phone number.",
      required: !1
    }),
    lead_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Lead Type",
      description: "Filter leads by their type.",
      required: !1,
      options: {
        options: [
          { label: "Appointment", value: "appointment" },
          { label: "Chat", value: "chat" },
          { label: "Email", value: "email" },
          { label: "Event", value: "event" },
          { label: "Other", value: "other" },
          { label: "Phone Call", value: "phone_call" },
          { label: "Text Message", value: "text_message" },
          { label: "Transaction", value: "transaction" },
          { label: "Web Form", value: "web_form" }
        ]
      }
    }),
    per_page: import_pieces_framework6.Property.Number({
      displayName: "Leads Per Page",
      description: "The number of leads to return (default 25, max 2500).",
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue } = context;
    if ([
      propsValue.email_address,
      propsValue.phone_number,
      propsValue.lead_type
    ].every((criterion) => !criterion))
      throw new Error(
        "Please provide at least one search criteria (Email, Phone, or Lead Type)."
      );
    return await whatConvertsClient.findLeads(auth, {
      profile_id: propsValue.profile_id,
      email_address: propsValue.email_address,
      phone_number: propsValue.phone_number,
      lead_type: propsValue.lead_type,
      per_page: propsValue.per_page
    });
  }
});

// ../ap/packages/pieces/community/what-converts/src/lib/triggers/new-lead.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var newLeadTrigger = (0, import_pieces_framework7.createTrigger)({
  auth: whatConvertsAuth,
  name: "new_lead",
  displayName: "New Lead",
  description: "Fires when a new lead is received.",
  aiMetadata: {
    description: 'Fires when WhatConverts receives a new lead (a phone call, web form, chat, transaction, or other tracked conversion). Delivers the full lead record including contact details, attribution, and analysis. Requires the WhatConverts webhook to be configured to send "new" lead events.'
  },
  props: {},
  sampleData: {
    trigger: "new",
    lead_id: 153928,
    user_id: "51497-af17340d-62b8-3044-423f-3dc754e621c2",
    lead_type: "Phone Call",
    lead_status: "Unique",
    lead_analysis: {
      "Keyword Detection": "buy,quote,test",
      "Lead Summary": "The customer was looking to purchase call tracking services. The agent assisted them with setting up an account.",
      "Intent Detection": "The customer wanted to purchase services.",
      "Sentiment Detection": "Positive",
      "Topic Detection": "call tracking,purchase,sign up"
    },
    last_updated: "2016-01-25T17:18:20Z",
    date_created: "2016-02-01T14:09:01Z",
    quotable: "Not Set",
    quote_value: null,
    sales_value: null,
    spotted_keywords: "buy,quote",
    lead_score: 50,
    lead_state: "Completed",
    profile: "My Profile",
    profile_id: 51497,
    account: "My Account",
    account_id: 27313,
    lead_url: "https://www.whatconverts.com/contact",
    landing_url: "https://www.whatconverts.com/",
    operating_system: "iOS 12",
    browser: "Chrome Mobile 71",
    device_type: "Smartphone",
    device_make: "Apple iPhone",
    lead_source: "google",
    lead_medium: "cpc",
    lead_campaign: "lead generation",
    lead_content: "new ad",
    lead_keyword: "generating leads",
    ip_address: "107.210.21.188",
    notes: "This is a new lead!",
    contact_name: "Jeremy Helms",
    contact_company_name: "Call Tracking Company",
    contact_email_address: "hello@whatconverts.com",
    contact_phone_number: "+18887203034",
    email_address: "hello@whatconverts.com",
    phone_number: "+18887203034",
    city: "Charlotte",
    state: "NC",
    zip: "28226",
    country: "US",
    field_mappings: {
      "Company Name": "Call Tracking Company",
      "Contact Person": "Jeremy Helms",
      Email: "hello@whatconverts.com",
      "Phone Number": "(888) 720-3034"
    },
    gclid: "CLibmtmqpNICFcSfGwodQbUAvg",
    msclkid: "25d83debf85f146b8f1d66a754c6a56c",
    unbounce_page_id: "f8e32bbc-e1a1-2c5b-b4d3-c6289c8e33b",
    unbounce_variant_id: "b",
    unbounce_visitor_id: "107.210.21.1881565955056416071",
    salesforce_user_id: "15228103840",
    roistat_visit_id: "wqeOuWjfeIUQdd122casdLK",
    hubspot_visitor_id: "5a72d290d2d21865a693f14bcf710fed",
    facebook_browser_id: "fb.1.1621529626600.927228660",
    facebook_click_id: "fb.1.1621529626598.IwAR1yDeb6_5RRDGD3fTthh9cPoaqhLYYyDFLN-LolvQwaMAPMhGRljDj-POc",
    google_analytics_client_id: "1017418163.1594157791",
    vwo_account_id: "512835",
    vwo_experiment_id: "2",
    vwo_variant_id: "2",
    vwo_user_id: "D4A14C3EFD6DB2A51749E1A931BE96B44",
    duplicate: !1,
    spam: !1
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(_context) {
  },
  async onDisable(_context) {
  },
  async run(context) {
    return context.payload.body.trigger !== "new" ? [] : [context.payload.body];
  }
});

// ../ap/packages/pieces/community/what-converts/src/lib/triggers/update-lead.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var updatedLeadTrigger = (0, import_pieces_framework8.createTrigger)({
  auth: whatConvertsAuth,
  name: "updated_lead",
  displayName: "Updated Lead",
  description: "Fires when an existing lead is updated in WhatConverts.",
  aiMetadata: {
    description: 'Fires when an existing WhatConverts lead is updated \u2014 for example when its quotable status, sales value, contact details, or attribution change. Delivers the updated lead record. Requires the WhatConverts webhook to be configured to send "update" lead events.'
  },
  props: {},
  sampleData: {
    trigger: "update",
    lead_id: 153928,
    user_id: "51497-af17340d-62b8-3044-423f-3dc754e621c2",
    lead_type: "Phone Call",
    lead_status: "Unique",
    last_updated: "2016-01-25T17:18:20Z",
    date_created: "2016-02-01T14:09:01Z",
    quotable: "Not Set",
    quote_value: null,
    sales_value: null,
    spotted_keywords: "buy,quote",
    lead_score: 50,
    lead_state: "Completed",
    profile: "My Profile",
    profile_id: 51497,
    account: "My Account",
    account_id: 27313,
    lead_url: "https://www.whatconverts.com/contact",
    landing_url: "https://www.whatconverts.com/",
    lead_source: "google",
    lead_medium: "cpc",
    lead_campaign: "lead generation",
    lead_content: "new ad",
    lead_keyword: "generating leads",
    ip_address: "107.210.21.188",
    notes: "This is a new lead!",
    contact_name: "Jeremy Helms",
    contact_company_name: "Call Tracking Company",
    contact_email_address: "hello@whatconverts.com",
    contact_phone_number: "+18887203034",
    email_address: "hello@whatconverts.com",
    phone_number: "+18887203034",
    city: "Charlotte",
    state: "NC",
    zip: "28226",
    country: "US",
    field_mappings: {
      "Company Name": "Call Tracking Company",
      "Contact Person": "Jeremy Helms",
      Email: "hello@whatconverts.com",
      "Phone Number": "(888) 720-3034"
    },
    gclid: "CLibmtmqpNICFcSfGwodQbUAvg",
    msclkid: "25d83debf85f146b8f1d66a754c6a56c",
    unbounce_page_id: "f8e32bbc-e1a1-2c5b-b4d3-c6289c8e33b",
    unbounce_variant_id: "b",
    unbounce_visitor_id: "107.210.21.1881565955056416071",
    salesforce_user_id: "15228103840",
    roistat_visit_id: "wqeOuWjfeIUQdd122casdLK",
    hubspot_visitor_id: "5a72d290d2d21865a693f14bcf710fed",
    facebook_browser_id: "fb.1.1621529626600.927228660",
    facebook_click_id: "fb.1.1621529626598.IwAR1yDeb6_5RRDGD3fTthh9cPoaqhLYYyDFLN-LolvQwaMAPMhGRljDj-POc",
    google_analytics_client_id: "1017418163.1594157791",
    vwo_account_id: "512835",
    vwo_experiment_id: "2",
    vwo_variant_id: "2",
    vwo_user_id: "D4A14C3EFD6DB2A51749E1A931BE96B44",
    duplicate: !1,
    spam: !1
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(_context) {
  },
  async onDisable(_context) {
  },
  async run(context) {
    return context.payload.body.trigger !== "update" ? [] : [context.payload.body];
  }
});

// ../ap/packages/pieces/community/what-converts/src/index.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), whatConverts = (0, import_pieces_framework9.createPiece)({
  displayName: "WhatConverts",
  auth: whatConvertsAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/what-converts.png",
  authors: ["Prabhukiran161", "sanket-a11y"],
  categories: [import_pieces_framework10.PieceCategory.SALES_AND_CRM, import_pieces_framework10.PieceCategory.MARKETING],
  actions: [
    createLeadAction,
    exportLeadsAction,
    updateLeadAction,
    findLeadAction
  ],
  triggers: [newLeadTrigger, updatedLeadTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  whatConverts
});
