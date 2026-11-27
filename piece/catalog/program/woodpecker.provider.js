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

// ../ap/packages/pieces/community/woodpecker/src/index.ts
var index_exports = {};
__export(index_exports, {
  woodpecker: () => woodpecker,
  woodpeckerAuth: () => woodpeckerAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework27 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework28 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/woodpecker/src/lib/actions/add-prospect-to-campaign.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/woodpecker/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var API_BASE_URL = "https://api.woodpecker.co/rest", WEBHOOK_EVENTS = {
  PROSPECT_REPLIED: "prospect_replied",
  PROSPECT_BLACKLISTED: "prospect_blacklisted",
  PROSPECT_OPTOUT: "prospect_opt_out",
  PROSPECT_BOUNCED: "prospect_bounced",
  PROSPECT_INVALID: "prospect_invalid",
  PROSPECT_AUTOREPLIED: "prospect_autoreplied",
  PROSPECT_SAVED: "prospect_saved",
  PROSPECT_NONRESPONSIVE: "prospect_non_responsive",
  LINK_CLICKED: "link_clicked",
  EMAIL_OPENED: "email_opened",
  PROSPECT_INTERESTED: "prospect_interested",
  PROSPECT_MAYBE_LATER: "prospect_maybe_later",
  PROSPECT_NOT_INTERESTED: "prospect_not_interested",
  EMAIL_SENT: "campaign_sent",
  FOLLOWUP_AFTER_AUTOREPLY: "followup_after_autoreply",
  SECONDARY_REPLIED: "secondary_replied",
  CAMPAIGN_COMPLETED: "campaign_completed",
  TASK_CREATED: "task_created",
  TASK_DONE: "task_done",
  TASK_IGNORED: "task_ignored"
};
async function subscribeWebhook(auth, webhookUrl, event) {
  await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${API_BASE_URL}/v1/webhooks/subscribe`,
    headers: {
      "x-api-key": auth,
      "Content-Type": "application/json"
    },
    body: {
      target_url: webhookUrl,
      event
    }
  });
}
async function unsubscribeWebhook(auth, webhookUrl, event) {
  await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${API_BASE_URL}/v1/webhooks/unsubscribe`,
    headers: {
      "x-api-key": auth,
      "Content-Type": "application/json"
    },
    body: {
      target_url: webhookUrl,
      event
    }
  });
}
var campaignsDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Campaign",
  description: "Select the campaign to add prospects to",
  required: !0,
  refreshers: [],
  auth: woodpeckerAuth,
  options: async ({ auth }) => {
    var _a;
    return auth ? {
      disabled: !1,
      options: ((_a = (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${API_BASE_URL}/v1/campaign_list`,
        headers: {
          "x-api-key": auth.secret_text
        }
      })).body) != null ? _a : []).map((campaign) => ({
        label: `${campaign.name} (${campaign.status})`,
        value: campaign.id
      }))
    } : {
      disabled: !0,
      options: [],
      placeholder: "Please authenticate first"
    };
  }
}), woodpeckerClient = {
  async makeRequest(auth, method, endpoint, body, queryParams) {
    let request = {
      method,
      url: `${API_BASE_URL}${endpoint}`,
      headers: {
        "x-api-key": auth
      },
      body,
      queryParams
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  }
};

// ../ap/packages/pieces/community/woodpecker/src/lib/actions/add-prospect-to-campaign.ts
var addProspectToCampaign = (0, import_pieces_framework2.createAction)({
  auth: woodpeckerAuth,
  name: "add_prospect_to_campaign",
  displayName: "Create/Update Prospect in Campaign",
  description: "Adds a new prospect or updates existing prospect data in a campaign",
  audience: "both",
  aiMetadata: { description: "Enroll a prospect (keyed by email) into a specific Woodpecker campaign, creating the prospect or updating its fields if it already exists. Use to start outreach to someone or push updated prospect details into a campaign; requires the campaign and the prospect email. Re-running with the same email upserts the same record rather than duplicating it.", idempotent: !0 },
  props: {
    campaign_id: campaignsDropdown,
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "Prospect email address",
      required: !0
    }),
    first_name: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    company: import_pieces_framework2.Property.ShortText({
      displayName: "Company",
      required: !1
    }),
    website: import_pieces_framework2.Property.ShortText({
      displayName: "Website",
      required: !1
    }),
    linkedin_url: import_pieces_framework2.Property.ShortText({
      displayName: "LinkedIn URL",
      required: !1
    }),
    tags: import_pieces_framework2.Property.ShortText({
      displayName: "Tags",
      description: "Tags starting with # separated by spaces (e.g. #VC #Startup)",
      required: !1
    }),
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Job Title",
      required: !1
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    address: import_pieces_framework2.Property.ShortText({
      displayName: "Address",
      required: !1
    }),
    city: import_pieces_framework2.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    state: import_pieces_framework2.Property.ShortText({
      displayName: "State",
      required: !1
    }),
    country: import_pieces_framework2.Property.ShortText({
      displayName: "Country",
      required: !1
    }),
    industry: import_pieces_framework2.Property.ShortText({
      displayName: "Industry",
      required: !1
    }),
    status: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Status",
      description: "Prospect status in the campaign",
      required: !1,
      options: {
        options: [
          { label: "Active", value: "ACTIVE" },
          { label: "Paused", value: "PAUSED" },
          { label: "To Review", value: "TO-REVIEW" },
          { label: "To Check", value: "TO-CHECK" }
        ]
      }
    }),
    snippets: import_pieces_framework2.Property.Array({
      displayName: "Snippets",
      description: "Custom snippets for personalization (supports HTML). Max 15 snippets.",
      required: !1
    }),
    send_after: import_pieces_framework2.Property.DateTime({
      displayName: "Send After",
      description: "Earliest date and time the prospect can be contacted",
      required: !1
    }),
    force: import_pieces_framework2.Property.Checkbox({
      displayName: "Force Add",
      description: "Add prospect even if their global status is not ACTIVE (use with caution)",
      required: !1,
      defaultValue: !1
    }),
    file_name: import_pieces_framework2.Property.ShortText({
      displayName: "Import Batch Name",
      description: "Name of the import batch (visible in the imported column)",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let prospect = {
      email: propsValue.email
    }, optionalFields = [
      "first_name",
      "last_name",
      "company",
      "website",
      "linkedin_url",
      "tags",
      "title",
      "phone",
      "address",
      "city",
      "state",
      "country",
      "industry",
      "status"
    ];
    for (let field of optionalFields)
      propsValue[field] && (prospect[field] = propsValue[field]);
    propsValue.snippets && Array.isArray(propsValue.snippets) && propsValue.snippets.slice(0, 15).forEach((value, index) => {
      value && (prospect[`snippet${index + 1}`] = value);
    });
    let campaign = {
      campaign_id: propsValue.campaign_id
    };
    propsValue.send_after && (campaign.send_after = propsValue.send_after);
    let body = {
      campaign,
      prospects: [prospect]
    };
    return propsValue.force && (body.force = !0), propsValue.file_name && (body.file_name = propsValue.file_name), await woodpeckerClient.makeRequest(
      auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/v1/add_prospects_campaign",
      body
    );
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/actions/add-prospect-to-list.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var addProspectToList = (0, import_pieces_framework3.createAction)({
  auth: woodpeckerAuth,
  name: "add_prospect_to_list",
  displayName: "Create/Update Prospect",
  description: "Adds a new prospect or updates existing prospect in the global prospect list",
  audience: "both",
  aiMetadata: { description: "Add or update a prospect (keyed by email) in the global Woodpecker prospect list, independent of any campaign. Use to maintain contact records or seed the prospect database; existing prospects are only modified when the update flag is enabled (otherwise they are left untouched). Re-running with the same email upserts the same record rather than duplicating it.", idempotent: !0 },
  props: {
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "Prospect email address",
      required: !0
    }),
    update: import_pieces_framework3.Property.Checkbox({
      displayName: "Update Existing",
      description: "If enabled, updates existing prospect data. If disabled, existing prospects remain unchanged.",
      required: !1,
      defaultValue: !1
    }),
    first_name: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    company: import_pieces_framework3.Property.ShortText({
      displayName: "Company",
      required: !1
    }),
    website: import_pieces_framework3.Property.ShortText({
      displayName: "Website",
      required: !1
    }),
    linkedin_url: import_pieces_framework3.Property.ShortText({
      displayName: "LinkedIn URL",
      required: !1
    }),
    tags: import_pieces_framework3.Property.ShortText({
      displayName: "Tags",
      description: "Tags starting with # separated by spaces (e.g. #VC #Startup). Appends to existing tags when updating.",
      required: !1
    }),
    set_tags: import_pieces_framework3.Property.ShortText({
      displayName: "Replace Tags",
      description: "Replaces all existing tags with these (only when updating). Use empty string to clear all tags.",
      required: !1
    }),
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Job Title",
      required: !1
    }),
    phone: import_pieces_framework3.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    address: import_pieces_framework3.Property.ShortText({
      displayName: "Address",
      required: !1
    }),
    city: import_pieces_framework3.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    state: import_pieces_framework3.Property.ShortText({
      displayName: "State",
      required: !1
    }),
    country: import_pieces_framework3.Property.ShortText({
      displayName: "Country",
      required: !1
    }),
    industry: import_pieces_framework3.Property.ShortText({
      displayName: "Industry",
      required: !1
    }),
    status: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Status",
      description: "Prospect status",
      required: !1,
      options: {
        options: [
          { label: "Active", value: "ACTIVE" },
          { label: "Blacklist", value: "BLACKLIST" },
          { label: "Bounced", value: "BOUNCED" },
          { label: "Invalid", value: "INVALID" },
          { label: "Replied", value: "REPLIED" }
        ]
      }
    }),
    snippets: import_pieces_framework3.Property.Array({
      displayName: "Snippets",
      description: "Custom snippets for personalization (supports HTML). Max 15 snippets.",
      required: !1
    }),
    file_name: import_pieces_framework3.Property.ShortText({
      displayName: "Import Batch Name",
      description: "Name of the import batch (visible in the imported column)",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let prospect = {
      email: propsValue.email
    }, optionalFields = [
      "first_name",
      "last_name",
      "company",
      "website",
      "linkedin_url",
      "tags",
      "title",
      "phone",
      "address",
      "city",
      "state",
      "country",
      "industry",
      "status"
    ];
    for (let field of optionalFields)
      propsValue[field] && (prospect[field] = propsValue[field]);
    propsValue.update && propsValue.set_tags !== void 0 && (prospect.set_tags = propsValue.set_tags), propsValue.snippets && Array.isArray(propsValue.snippets) && propsValue.snippets.slice(0, 15).forEach((value, index) => {
      value && (prospect[`snippet${index + 1}`] = value);
    });
    let body = {
      prospects: [prospect]
    };
    return propsValue.update && (body.update = !0), propsValue.file_name && (body.file_name = propsValue.file_name), await woodpeckerClient.makeRequest(
      auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/v1/add_prospects_list",
      body
    );
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/actions/blacklist-domain.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var blacklistDomain = (0, import_pieces_framework4.createAction)({
  auth: woodpeckerAuth,
  name: "blacklist_domain",
  displayName: "Blacklist Domain",
  description: "Add a domain to the blacklist to block sending to all prospects within that domain",
  audience: "both",
  aiMetadata: { description: "Add a domain (e.g. example.com) to the account-wide blacklist so Woodpecker stops emailing every prospect at that domain. Use to suppress an entire company or to honor an unsubscribe-by-domain request. Blacklisting an already-blacklisted domain leaves it blacklisted with no additional effect.", idempotent: !0 },
  props: {
    domain: import_pieces_framework4.Property.ShortText({
      displayName: "Domain",
      description: "Domain to blacklist (e.g. example.com)",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return await woodpeckerClient.makeRequest(
      auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/v2/blacklist/domains",
      {
        domains: [propsValue.domain]
      }
    );
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/actions/get-prospect-responses.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getProspectResponses = (0, import_pieces_framework5.createAction)({
  auth: woodpeckerAuth,
  name: "get_prospect_responses",
  displayName: "Get Prospect Responses",
  description: "Fetch all responses from a specified prospect",
  audience: "both",
  aiMetadata: { description: "Retrieve the reply messages a given prospect has sent back, looked up by the prospect numeric ID and optionally narrowed to a single campaign. Use to read a prospect's replies for triage, sentiment, or logging. Read-only; safe to call repeatedly.", idempotent: !0 },
  props: {
    prospect_id: import_pieces_framework5.Property.Number({
      displayName: "Prospect ID",
      description: "The unique ID of the prospect",
      required: !0
    }),
    campaign_id: import_pieces_framework5.Property.Dropdown({
      displayName: "Campaign",
      description: "Filter responses by campaign (optional)",
      required: !1,
      auth: woodpeckerAuth,
      refreshers: [],
      options: async ({ auth }) => {
        var _a;
        return auth ? {
          disabled: !1,
          options: ((_a = (await import_pieces_common5.httpClient.sendRequest({
            method: import_pieces_common5.HttpMethod.GET,
            url: "https://api.woodpecker.co/rest/v1/campaign_list",
            headers: {
              "x-api-key": auth.secret_text
            }
          })).body) != null ? _a : []).map((campaign) => ({
            label: `${campaign.name} (${campaign.status})`,
            value: campaign.id
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
      }
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = {};
    return propsValue.campaign_id && (queryParams.campaign_id = String(propsValue.campaign_id)), await woodpeckerClient.makeRequest(
      auth.secret_text,
      import_pieces_common5.HttpMethod.GET,
      `/v2/prospects/${propsValue.prospect_id}/responses`,
      void 0,
      Object.keys(queryParams).length > 0 ? queryParams : void 0
    );
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/actions/find-prospect-by-email.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var findProspectByEmail = (0, import_pieces_framework6.createAction)({
  auth: woodpeckerAuth,
  name: "find_prospect_by_email",
  displayName: "Find Prospect by Email",
  description: "Search for a prospect by their email address",
  audience: "both",
  aiMetadata: { description: "Look up prospects in Woodpecker by email address, optionally including the campaigns each prospect is enrolled in. Use to check whether a contact already exists or to fetch their stored details and IDs before another action. The email is matched as a search filter (may return more than one match); read-only and safe to call repeatedly.", idempotent: !0 },
  props: {
    email: import_pieces_framework6.Property.ShortText({
      displayName: "Email",
      description: "Email address to search for (exact match or partial)",
      required: !0
    }),
    include_campaign_details: import_pieces_framework6.Property.Checkbox({
      displayName: "Include Campaign Details",
      description: "Include information about campaigns the prospect is enrolled in",
      required: !1,
      defaultValue: !0
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = {
      search: `email=${propsValue.email}`
    };
    return propsValue.include_campaign_details && (queryParams.campaigns_details = "true"), await woodpeckerClient.makeRequest(
      auth.secret_text,
      import_pieces_common6.HttpMethod.GET,
      "/v1/prospects",
      void 0,
      queryParams
    );
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/prospect-replied.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var prospectReplied = (0, import_pieces_framework7.createTrigger)({
  auth: woodpeckerAuth,
  name: "prospect_replied",
  displayName: "Prospect Replied",
  description: "Triggers when a prospect replies to an email or their status is manually set to RESPONDED",
  aiMetadata: {
    description: "Fires when a prospect replies to a campaign email, or when their status is manually changed to RESPONDED in Woodpecker. Represents an inbound human response and includes the prospect plus the reply email content."
  },
  props: {},
  sampleData: {
    method: "prospect_replied",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity",
      status: "REPLIED",
      campaign_id: 123456,
      campaign_name: "SaaS in America"
    },
    email: {
      id: 191867492,
      subject: "Reply message subject",
      message: "reply content"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_REPLIED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_REPLIED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/prospect-blacklisted.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var prospectBlacklisted = (0, import_pieces_framework8.createTrigger)({
  auth: woodpeckerAuth,
  name: "prospect_blacklisted",
  displayName: "Prospect Blacklisted",
  description: "Triggers when a prospect is added to the blacklist",
  aiMetadata: {
    description: "Fires when a prospect is added to the Woodpecker blacklist, meaning Woodpecker will no longer email that contact. Represents a suppression event for a single prospect."
  },
  props: {},
  sampleData: {
    method: "prospect_blacklisted",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity",
      status: "BLACKLIST"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_BLACKLISTED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_BLACKLISTED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/prospect-optout.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var prospectOptout = (0, import_pieces_framework9.createTrigger)({
  auth: woodpeckerAuth,
  name: "prospect_optout",
  displayName: "Prospect Opt-out",
  description: "Triggers when a prospect opts out from receiving emails",
  aiMetadata: {
    description: "Fires when a prospect opts out of receiving further emails (e.g. clicks unsubscribe). Represents an explicit unsubscribe by the prospect and includes their details."
  },
  props: {},
  sampleData: {
    method: "prospect_optout",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity",
      status: "OPTOUT"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_OPTOUT
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_OPTOUT
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/prospect-bounced.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var prospectBounced = (0, import_pieces_framework10.createTrigger)({
  auth: woodpeckerAuth,
  name: "prospect_bounced",
  displayName: "Prospect Bounced",
  description: "Triggers when an email to a prospect bounces",
  aiMetadata: {
    description: "Fires when an email sent to a prospect bounces and the prospect is marked BOUNCED. Represents an undeliverable-address event for a single prospect."
  },
  props: {},
  sampleData: {
    method: "prospect_bounced",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity",
      status: "BOUNCED"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_BOUNCED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_BOUNCED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/prospect-invalid.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var prospectInvalid = (0, import_pieces_framework11.createTrigger)({
  auth: woodpeckerAuth,
  name: "prospect_invalid",
  displayName: "Prospect Invalid",
  description: "Triggers when a prospect email is marked as invalid",
  aiMetadata: {
    description: "Fires when a prospect email address is marked INVALID (failed verification or rejected as undeliverable). Represents a bad-address event for a single prospect."
  },
  props: {},
  sampleData: {
    method: "prospect_invalid",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity",
      status: "INVALID"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_INVALID
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_INVALID
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/prospect-autoreplied.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var prospectAutoreplied = (0, import_pieces_framework12.createTrigger)({
  auth: woodpeckerAuth,
  name: "prospect_autoreplied",
  displayName: "Prospect Autoreplied",
  description: "Triggers when an autoreply is detected from a prospect",
  aiMetadata: {
    description: "Fires when Woodpecker detects an automated reply (e.g. out-of-office) from a prospect rather than a genuine human response. Represents an autoreply event for a single prospect."
  },
  props: {},
  sampleData: {
    method: "prospect_autoreplied",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity",
      status: "AUTOREPLIED"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_AUTOREPLIED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_AUTOREPLIED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/prospect-saved.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var prospectSaved = (0, import_pieces_framework13.createTrigger)({
  auth: woodpeckerAuth,
  name: "prospect_saved",
  displayName: "Prospect Saved",
  description: "Triggers when a prospect is saved",
  aiMetadata: {
    description: "Fires when a prospect is created or saved in Woodpecker. Represents a new or updated prospect record and includes its details."
  },
  props: {},
  sampleData: {
    method: "prospect_saved",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_SAVED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_SAVED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/prospect-nonresponsive.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var prospectNonresponsive = (0, import_pieces_framework14.createTrigger)({
  auth: woodpeckerAuth,
  name: "prospect_nonresponsive",
  displayName: "Prospect Nonresponsive",
  description: "Triggers when a prospect is marked as nonresponsive",
  aiMetadata: {
    description: "Fires when a prospect is marked nonresponsive, meaning they completed the campaign sequence without replying. Represents an end-of-sequence no-reply outcome for a single prospect."
  },
  props: {},
  sampleData: {
    method: "prospect_nonresponsive",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_NONRESPONSIVE
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_NONRESPONSIVE
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/link-clicked.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var linkClicked = (0, import_pieces_framework15.createTrigger)({
  auth: woodpeckerAuth,
  name: "link_clicked",
  displayName: "Prospect Clicked a Link",
  description: "Triggers when a prospect clicks a link in an email",
  aiMetadata: {
    description: "Fires when a prospect clicks a tracked link inside a campaign email. Represents a link-click engagement event for a single prospect."
  },
  props: {},
  sampleData: {
    method: "link_clicked",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.LINK_CLICKED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.LINK_CLICKED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/email-opened.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var emailOpened = (0, import_pieces_framework16.createTrigger)({
  auth: woodpeckerAuth,
  name: "email_opened",
  displayName: "Prospect Opened an Email",
  description: "Triggers when a prospect opens an email",
  aiMetadata: {
    description: "Fires when a prospect opens a campaign email (tracked open). Represents an email-open engagement event for a single prospect."
  },
  props: {},
  sampleData: {
    method: "email_opened",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework16.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.EMAIL_OPENED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.EMAIL_OPENED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/prospect-interested.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var prospectInterested = (0, import_pieces_framework17.createTrigger)({
  auth: woodpeckerAuth,
  name: "prospect_interested",
  displayName: "Prospect Interested",
  description: "Triggers when a prospect is marked as interested",
  aiMetadata: {
    description: "Fires when a prospect is categorized as INTERESTED in Woodpecker. Represents a positive-intent signal for a single prospect, useful for routing hot leads."
  },
  props: {},
  sampleData: {
    method: "prospect_interested",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity",
      interested: "INTERESTED"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_INTERESTED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_INTERESTED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/prospect-maybe-later.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var prospectMaybeLater = (0, import_pieces_framework18.createTrigger)({
  auth: woodpeckerAuth,
  name: "prospect_maybe_later",
  displayName: "Prospect Maybe Later",
  description: "Triggers when a prospect is marked as maybe later",
  aiMetadata: {
    description: "Fires when a prospect is categorized as MAYBE_LATER in Woodpecker, signaling deferred interest. Represents a follow-up-later intent for a single prospect."
  },
  props: {},
  sampleData: {
    method: "prospect_maybe_later",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity",
      interested: "MAYBE_LATER"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework18.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_MAYBE_LATER
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_MAYBE_LATER
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/prospect-not-interested.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var prospectNotInterested = (0, import_pieces_framework19.createTrigger)({
  auth: woodpeckerAuth,
  name: "prospect_not_interested",
  displayName: "Prospect Not Interested",
  description: "Triggers when a prospect is marked as not interested",
  aiMetadata: {
    description: "Fires when a prospect is categorized as NOT_INTERESTED in Woodpecker. Represents a negative-intent signal for a single prospect, useful for disqualifying or suppressing leads."
  },
  props: {},
  sampleData: {
    method: "prospect_not_interested",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity",
      interested: "NOT_INTERESTED"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework19.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_NOT_INTERESTED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.PROSPECT_NOT_INTERESTED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/email-sent.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var emailSent = (0, import_pieces_framework20.createTrigger)({
  auth: woodpeckerAuth,
  name: "email_sent",
  displayName: "Campaign Email Sent",
  description: "Triggers when a campaign email is sent to a prospect",
  aiMetadata: {
    description: "Fires when Woodpecker sends a campaign email to a prospect. Represents an outbound send event and includes the prospect plus the campaign it was sent from."
  },
  props: {},
  sampleData: {
    method: "email_sent",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity",
      campaign_id: 123456,
      campaign_name: "SaaS in America"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework20.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.EMAIL_SENT
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.EMAIL_SENT
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/followup-after-autoreply.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var followupAfterAutoreply = (0, import_pieces_framework21.createTrigger)({
  auth: woodpeckerAuth,
  name: "followup_after_autoreply",
  displayName: "Follow-up After Autoreply",
  description: "Triggers when a follow-up is scheduled after an autoreply",
  aiMetadata: {
    description: "Fires when Woodpecker schedules a follow-up to a prospect after detecting an earlier autoreply (e.g. resuming outreach once an out-of-office period ends). Represents a follow-up-resumption event for a single prospect."
  },
  props: {},
  sampleData: {
    method: "followup_after_autoreply",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework21.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.FOLLOWUP_AFTER_AUTOREPLY
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.FOLLOWUP_AFTER_AUTOREPLY
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/secondary-replied.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var secondaryReplied = (0, import_pieces_framework22.createTrigger)({
  auth: woodpeckerAuth,
  name: "secondary_replied",
  displayName: "Secondary Replied",
  description: "Triggers when a secondary email address replies",
  aiMetadata: {
    description: "Fires when a reply comes from a prospect's secondary email address rather than their primary one. Represents an inbound response from an alternate address for a single prospect."
  },
  props: {},
  sampleData: {
    method: "secondary_replied",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework22.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.SECONDARY_REPLIED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.SECONDARY_REPLIED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/campaign-completed.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var campaignCompleted = (0, import_pieces_framework23.createTrigger)({
  auth: woodpeckerAuth,
  name: "campaign_completed",
  displayName: "Campaign Completed",
  description: "Triggers when a campaign is completed",
  aiMetadata: {
    description: "Fires when a campaign finishes (all steps have been delivered to its prospects). Represents a campaign-level completion event and includes the campaign id and name."
  },
  props: {},
  sampleData: {
    method: "campaign_completed",
    campaign: {
      id: 123456,
      name: "SaaS in America"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework23.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.CAMPAIGN_COMPLETED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.CAMPAIGN_COMPLETED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/task-created.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework");
var taskCreated = (0, import_pieces_framework24.createTrigger)({
  auth: woodpeckerAuth,
  name: "task_created",
  displayName: "Task Created",
  description: "Triggers when a task is created",
  aiMetadata: {
    description: "Fires when a manual task is created in Woodpecker for a prospect (e.g. a call or LinkedIn step). Represents a new follow-up task and includes the associated prospect."
  },
  props: {},
  sampleData: {
    method: "task_created",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework24.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.TASK_CREATED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.TASK_CREATED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/task-done.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework");
var taskDone = (0, import_pieces_framework25.createTrigger)({
  auth: woodpeckerAuth,
  name: "task_done",
  displayName: "Task Done",
  description: "Triggers when a task is marked as done",
  aiMetadata: {
    description: "Fires when a manual task in Woodpecker is marked done. Represents completion of a follow-up task for a prospect and includes the associated prospect."
  },
  props: {},
  sampleData: {
    method: "task_done",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework25.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.TASK_DONE
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.TASK_DONE
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/lib/triggers/task-ignored.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework");
var taskIgnored = (0, import_pieces_framework26.createTrigger)({
  auth: woodpeckerAuth,
  name: "task_ignored",
  displayName: "Task Ignored",
  description: "Triggers when a task is ignored",
  aiMetadata: {
    description: "Fires when a manual task in Woodpecker is dismissed or ignored rather than completed. Represents a skipped follow-up task for a prospect and includes the associated prospect."
  },
  props: {},
  sampleData: {
    method: "task_ignored",
    prospect: {
      id: 1234567890,
      email: "erlich@bachmanity.com",
      first_name: "Erlich",
      last_name: "Bachman",
      company: "Bachmanity"
    },
    timestamp: "2025-03-21T20:47:47+0100"
  },
  type: import_pieces_framework26.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await subscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.TASK_IGNORED
    );
  },
  async onDisable(context) {
    await unsubscribeWebhook(
      context.auth.secret_text,
      context.webhookUrl,
      WEBHOOK_EVENTS.TASK_IGNORED
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/woodpecker/src/index.ts
var woodpeckerAuth = import_pieces_framework27.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To obtain your API key:
1. Log into your Woodpecker account
2. Go to Marketplace (top-right) \u2192 Integrations \u2192 API keys
3. Click **Create a key**
4. Copy the key and paste it here
`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common7.httpClient.sendRequest({
        method: import_pieces_common7.HttpMethod.GET,
        url: "https://api.woodpecker.co/rest/v2/users",
        headers: {
          "x-api-key": auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key."
      };
    }
  }
}), woodpecker = (0, import_pieces_framework27.createPiece)({
  displayName: "Woodpecker",
  description: "Cold email automation tool for sales teams to send personalized outreach campaigns",
  auth: woodpeckerAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/woodpecker.png",
  authors: ["onyedikachi-david"],
  categories: [import_pieces_framework28.PieceCategory.SALES_AND_CRM],
  actions: [
    addProspectToCampaign,
    addProspectToList,
    blacklistDomain,
    getProspectResponses,
    findProspectByEmail,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: woodpeckerAuth,
      baseUrl: () => "https://api.woodpecker.co/rest",
      authMapping: async (auth) => ({
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: [
    prospectReplied,
    prospectBlacklisted,
    prospectOptout,
    prospectBounced,
    prospectInvalid,
    prospectAutoreplied,
    prospectSaved,
    prospectNonresponsive,
    linkClicked,
    emailOpened,
    prospectInterested,
    prospectMaybeLater,
    prospectNotInterested,
    emailSent,
    followupAfterAutoreply,
    secondaryReplied,
    campaignCompleted,
    taskCreated,
    taskDone,
    taskIgnored
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  woodpecker,
  woodpeckerAuth
});
