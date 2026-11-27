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

// ../ap/packages/pieces/community/smartlead/src/index.ts
var index_exports = {};
__export(index_exports, {
  smartlead: () => smartlead
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/smartlead/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/smartlead/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://server.smartlead.ai/api/v1";
async function smartleadRequest({
  endpoint,
  method,
  apiKey,
  body,
  queryParams = {}
}) {
  let allParams = {
    api_key: apiKey
  };
  for (let [k, value] of Object.entries(queryParams))
    value != null && (allParams[k] = String(value));
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}/${endpoint}`,
    headers: {
      "Content-Type": "application/json"
    },
    queryParams: allParams,
    body
  })).body;
}

// ../ap/packages/pieces/community/smartlead/src/lib/auth.ts
var markdownDescription = `
To obtain your API key:
1. Log in to your SmartLead account
2. Go to **Profile -> Settings \u2192 API Key **
3. Copy your API key

The API key is passed as a query parameter to all API requests.
`, smartleadAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0,
  validate: async ({ auth }) => {
    var _a;
    try {
      return await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `${BASE_URL}/campaigns`,
        queryParams: {
          api_key: auth
        }
      }), { valid: !0 };
    } catch (error) {
      let status = (_a = error == null ? void 0 : error.response) == null ? void 0 : _a.status;
      if (status === 401 || status === 403)
        return {
          valid: !1,
          error: "Invalid API key. Please check your Smartlead API credentials."
        };
      throw error;
    }
  }
});

// ../ap/packages/pieces/community/smartlead/src/lib/actions/create-campaign.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createCampaignAction = (0, import_pieces_framework2.createAction)({
  auth: smartleadAuth,
  name: "create_campaign",
  displayName: "Create Campaign",
  description: "Create a new cold email campaign in SmartLead",
  audience: "both",
  aiMetadata: {
    description: "Creates a new, empty cold email campaign in SmartLead, identified by name; on agency/white-label accounts an optional client ID associates it with a specific client. Use this as the first step before adding leads or configuring sequences. Not idempotent: each call creates a separate campaign even with the same name.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Campaign Name",
      description: 'Name of the campaign. Defaults to "Untitled Campaign" if not provided.',
      required: !0
    }),
    client_id: import_pieces_framework2.Property.Number({
      displayName: "Client ID",
      description: "Associate the campaign with a specific client (for agency/white-label accounts). Leave empty to use your default.",
      required: !1
    })
  },
  async run(context) {
    let { name, client_id } = context.propsValue, apiKey = context.auth.secret_text, body = { name };
    return client_id != null && (body.client_id = client_id), await smartleadRequest({
      endpoint: "campaigns/create",
      method: import_pieces_common3.HttpMethod.POST,
      apiKey,
      body
    });
  }
});

// ../ap/packages/pieces/community/smartlead/src/lib/actions/add-leads-to-campaign.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var addLeadsToCampaignAction = (0, import_pieces_framework3.createAction)({
  auth: smartleadAuth,
  name: "add_leads_to_campaign",
  displayName: "Add Leads to Campaign",
  description: "Add one or more leads to a campaign with validation and deduplication. Maximum 400 leads per request.",
  audience: "both",
  aiMetadata: {
    description: "Adds leads to an existing SmartLead campaign by campaign ID, passing a JSON array where each lead requires an email field. Optional flags let you bypass the global block list, unsubscribe list, cross-campaign duplicate check, and community bounce list. Use to populate or grow a campaign recipient list; capped at 400 leads per call. Not idempotent: it appends leads on each call (though SmartLead deduplicates by email within the campaign).",
    idempotent: !1
  },
  props: {
    campaign_id: import_pieces_framework3.Property.Number({
      displayName: "Campaign ID",
      description: "The ID of the campaign to add leads to",
      required: !0
    }),
    lead_list: import_pieces_framework3.Property.Json({
      displayName: "Lead List",
      description: `Array of lead objects. Each lead must have an "email" field. Optional fields: first_name, last_name, phone_number, company_name, website, location, linkedin_profile, company_url, custom_fields (object).

Example:
\`\`\`json
[
  {
    "email": "john@example.com",
    "first_name": "John",
    "last_name": "Doe",
    "company_name": "Acme Corp",
    "custom_fields": { "job_title": "CEO" }
  }
]
\`\`\``,
      required: !0
    }),
    ignore_global_block_list: import_pieces_framework3.Property.Checkbox({
      displayName: "Ignore Global Block List",
      description: "Skip global block list validation",
      required: !1,
      defaultValue: !1
    }),
    ignore_unsubscribe_list: import_pieces_framework3.Property.Checkbox({
      displayName: "Ignore Unsubscribe List",
      description: "Include previously unsubscribed leads",
      required: !1,
      defaultValue: !1
    }),
    ignore_duplicate_leads_in_other_campaign: import_pieces_framework3.Property.Checkbox({
      displayName: "Ignore Duplicates in Other Campaigns",
      description: "Allow the same lead in multiple campaigns",
      required: !1,
      defaultValue: !1
    }),
    ignore_community_bounce_list: import_pieces_framework3.Property.Checkbox({
      displayName: "Ignore Community Bounce List",
      description: "Skip community bounce list check",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      campaign_id,
      lead_list,
      ignore_global_block_list,
      ignore_unsubscribe_list,
      ignore_duplicate_leads_in_other_campaign,
      ignore_community_bounce_list
    } = context.propsValue, apiKey = context.auth.secret_text, leads = typeof lead_list == "string" ? JSON.parse(lead_list) : lead_list;
    if (!Array.isArray(leads))
      throw new Error(
        'lead_list must be a JSON array of lead objects with at least an "email" field'
      );
    if (leads.length === 0)
      throw new Error(
        "lead_list must contain at least one lead."
      );
    if (leads.length > 400)
      throw new Error(
        `Maximum 400 leads per request. Got ${leads.length}. Split into multiple requests.`
      );
    for (let i = 0; i < leads.length; i++)
      if (!leads[i].email || typeof leads[i].email != "string")
        throw new Error(
          `Lead at index ${i} is missing a required "email" field.`
        );
    let body = {
      lead_list: leads
    }, settings = {};
    return ignore_global_block_list && (settings.ignore_global_block_list = !0), ignore_unsubscribe_list && (settings.ignore_unsubscribe_list = !0), ignore_duplicate_leads_in_other_campaign && (settings.ignore_duplicate_leads_in_other_campaign = !0), ignore_community_bounce_list && (settings.ignore_community_bounce_list = !0), Object.keys(settings).length > 0 && (body.settings = settings), await smartleadRequest({
      endpoint: `campaigns/${campaign_id}/leads`,
      method: import_pieces_common4.HttpMethod.POST,
      apiKey,
      body
    });
  }
});

// ../ap/packages/pieces/community/smartlead/src/lib/actions/get-campaign-statistics.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getCampaignStatisticsAction = (0, import_pieces_framework4.createAction)({
  auth: smartleadAuth,
  name: "get_campaign_statistics",
  displayName: "Get Campaign Statistics",
  description: "Retrieve comprehensive analytics for a campaign including open rates, click rates, reply rates, and engagement statistics.",
  audience: "both",
  aiMetadata: {
    description: "Fetches aggregate performance analytics for a single SmartLead campaign by campaign ID, covering open, click, and reply rates and related engagement metrics. Use to report on or evaluate how a campaign is performing. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    campaign_id: import_pieces_framework4.Property.Number({
      displayName: "Campaign ID",
      description: "The ID of the campaign to get statistics for",
      required: !0
    })
  },
  async run(context) {
    let { campaign_id } = context.propsValue, apiKey = context.auth.secret_text;
    return await smartleadRequest({
      endpoint: `campaigns/${campaign_id}/analytics`,
      method: import_pieces_common5.HttpMethod.GET,
      apiKey
    });
  }
});

// ../ap/packages/pieces/community/smartlead/src/lib/actions/update-campaign-settings.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var updateCampaignSettingsAction = (0, import_pieces_framework5.createAction)({
  auth: smartleadAuth,
  name: "update_campaign_settings",
  displayName: "Update Campaign Settings",
  description: "Update campaign settings including tracking, stop conditions, and deliverability options.",
  audience: "both",
  aiMetadata: {
    description: "Updates configuration on an existing SmartLead campaign by campaign ID, such as open/click tracking, the stop-lead condition, unsubscribe text, AI ESP matching, plain-text sending, and follow-up percentage. Use to adjust how a campaign sends and tracks; at least one setting must be supplied and follow-up percentage must be 0-100. Idempotent: it overwrites the targeted settings, so repeating the same input leaves the campaign in the same state.",
    idempotent: !0
  },
  props: {
    campaign_id: import_pieces_framework5.Property.Number({
      displayName: "Campaign ID",
      description: "The ID of the campaign to update",
      required: !0
    }),
    track_open: import_pieces_framework5.Property.Checkbox({
      displayName: "Track Opens",
      description: "Enable email open tracking",
      required: !1
    }),
    track_click: import_pieces_framework5.Property.Checkbox({
      displayName: "Track Clicks",
      description: "Enable link click tracking",
      required: !1
    }),
    stop_lead_settings: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Stop Lead Condition",
      description: "When to stop emailing a lead",
      required: !1,
      options: {
        options: [
          { label: "Reply to an email", value: "REPLY_TO_AN_EMAIL" },
          { label: "Opened email", value: "OPENED_EMAIL" },
          { label: "Clicked link", value: "CLICKED_LINK" }
        ]
      }
    }),
    unsubscribe_text: import_pieces_framework5.Property.LongText({
      displayName: "Unsubscribe Text",
      description: "Custom unsubscribe text added to campaign emails",
      required: !1
    }),
    enable_ai_esp_matching: import_pieces_framework5.Property.Checkbox({
      displayName: "Enable AI ESP Matching",
      description: "Use AI to intelligently pair leads with optimal email accounts",
      required: !1
    }),
    send_as_plain_text: import_pieces_framework5.Property.Checkbox({
      displayName: "Send as Plain Text",
      description: "Send emails as plain text for better deliverability with technical audiences",
      required: !1
    }),
    follow_up_percentage: import_pieces_framework5.Property.Number({
      displayName: "Follow Up Percentage",
      description: "Percentage of leads to follow up with (0-100)",
      required: !1
    })
  },
  async run(context) {
    let {
      campaign_id,
      track_open,
      track_click,
      stop_lead_settings,
      unsubscribe_text,
      enable_ai_esp_matching,
      send_as_plain_text,
      follow_up_percentage
    } = context.propsValue, apiKey = context.auth.secret_text, body = {};
    if ((track_open !== void 0 || track_click !== void 0) && (body.track_settings = __spreadValues(__spreadValues({}, track_open !== void 0 && { track_open }), track_click !== void 0 && { track_click })), stop_lead_settings !== void 0 && (body.stop_lead_settings = stop_lead_settings), unsubscribe_text !== void 0 && unsubscribe_text !== "" && (body.unsubscribe_text = unsubscribe_text), enable_ai_esp_matching !== void 0 && (body.enable_ai_esp_matching = enable_ai_esp_matching), send_as_plain_text !== void 0 && (body.send_as_plain_text = send_as_plain_text), follow_up_percentage !== void 0) {
      if (follow_up_percentage < 0 || follow_up_percentage > 100)
        throw new Error(
          `follow_up_percentage must be between 0 and 100. Got ${follow_up_percentage}.`
        );
      body.follow_up_percentage = follow_up_percentage;
    }
    if (Object.keys(body).length === 0)
      throw new Error(
        "At least one setting must be provided to update."
      );
    return await smartleadRequest({
      endpoint: `campaigns/${campaign_id}/settings`,
      method: import_pieces_common6.HttpMethod.POST,
      apiKey,
      body
    });
  }
});

// ../ap/packages/pieces/community/smartlead/src/index.ts
var smartlead = (0, import_pieces_framework6.createPiece)({
  displayName: "SmartLead",
  description: "Powerful cold email outreach platform with multi-account rotation, email warmup, unified inbox, and advanced analytics.",
  auth: smartleadAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/smartlead.png",
  categories: [import_pieces_framework7.PieceCategory.MARKETING, import_pieces_framework7.PieceCategory.SALES_AND_CRM],
  authors: ["Harmatta"],
  actions: [
    createCampaignAction,
    addLeadsToCampaignAction,
    getCampaignStatisticsAction,
    updateCampaignSettingsAction,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: smartleadAuth,
      baseUrl: () => BASE_URL,
      authLocation: "queryParams",
      authMapping: async (auth) => ({
        api_key: auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  smartlead
});
