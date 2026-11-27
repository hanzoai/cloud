"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __objRest = (source, exclude) => {
  var target = {};
  for (var prop in source)
    __hasOwnProp.call(source, prop) && exclude.indexOf(prop) < 0 && (target[prop] = source[prop]);
  if (source != null && __getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(source))
      exclude.indexOf(prop) < 0 && __propIsEnum.call(source, prop) && (target[prop] = source[prop]);
  return target;
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

// ../ap/packages/pieces/community/lemlist/src/index.ts
var index_exports = {};
__export(index_exports, {
  lemlist: () => lemlist
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework19 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lemlist/src/lib/common/constants.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lemlist/src/lib/common/requests.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function fireHttpRequest({
  method,
  path,
  auth,
  body
}) {
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json"
    },
    body,
    authentication: {
      type: import_pieces_common.AuthenticationType.BASIC,
      username: "",
      password: auth
    }
  }).then((res) => res.body);
}
var lemlistApiService = {
  async fetchTeams(auth) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.TEAM}`,
      method: import_pieces_common.HttpMethod.GET,
      auth: auth.secret_text
    });
  },
  async getLeadByEmail(auth, email) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.LEADS}/${email}?version=v2`,
      method: import_pieces_common.HttpMethod.GET,
      auth: auth.secret_text
    });
  },
  async fetchCampaigns(auth) {
    return (await fireHttpRequest({
      path: `${API_ENDPOINTS.CAMPAIGNS}?version=v2&sortBy=createdAt&sortOrder=desc`,
      method: import_pieces_common.HttpMethod.GET,
      auth
    })).campaigns;
  },
  async removeLeadFromACampaign(auth, { campaignId, leadEmail }) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.CAMPAIGNS}/${campaignId}${API_ENDPOINTS.LEADS}/${leadEmail}?action=remove`,
      method: import_pieces_common.HttpMethod.DELETE,
      auth: auth.secret_text
    });
  },
  async addLeadToACampaign(auth, {
    campaignId,
    leadEmail,
    leadData,
    deduplicate,
    linkedinEnrichment,
    findEmail,
    verifyEmail,
    findPhone
  }) {
    let queryParams = new URLSearchParams();
    deduplicate && queryParams.append("deduplicate", "true"), linkedinEnrichment && queryParams.append("linkedinEnrichment", "true"), findEmail && queryParams.append("findEmail", "true"), verifyEmail && queryParams.append("verifyEmail", "true"), findPhone && queryParams.append("findPhone", "true");
    let query = queryParams.toString() ? `?${queryParams.toString()}` : "";
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.CAMPAIGNS}/${campaignId}${API_ENDPOINTS.LEADS}/${leadEmail}${query}`,
      method: import_pieces_common.HttpMethod.POST,
      auth: auth.secret_text,
      body: leadData
    });
  },
  async addLeadVariables(auth, { leadId, variables }) {
    let queryParams = new URLSearchParams();
    for (let [key, value] of Object.entries(variables))
      value != null && queryParams.append(key, String(value));
    let query = queryParams.toString() ? `?${queryParams.toString()}` : "";
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.LEADS}/${leadId}/variables${query}`,
      method: import_pieces_common.HttpMethod.POST,
      auth: auth.secret_text
    });
  },
  async updateLeadFromCampaign(auth, {
    campaignId,
    leadEmail,
    leadData
  }) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.CAMPAIGNS}/${campaignId}${API_ENDPOINTS.LEADS}/${leadEmail}`,
      method: import_pieces_common.HttpMethod.PATCH,
      auth: auth.secret_text,
      body: leadData
    });
  },
  async unsubscribeLeadFromACampaign(auth, { campaignId, leadEmail }) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.CAMPAIGNS}/${campaignId}${API_ENDPOINTS.LEADS}/${leadEmail}`,
      method: import_pieces_common.HttpMethod.DELETE,
      auth: auth.secret_text
    });
  },
  async markLeadAsInterestedInCampaign(auth, { campaignId, leadEmail }) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.CAMPAIGNS}/${campaignId}${API_ENDPOINTS.LEADS}/${leadEmail}/interested`,
      method: import_pieces_common.HttpMethod.POST,
      auth: auth.secret_text
    });
  },
  async markLeadAsInterestedInAllCampaigns(auth, { leadEmail }) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.LEADS}/interested/${leadEmail}`,
      method: import_pieces_common.HttpMethod.POST,
      auth: auth.secret_text
    });
  },
  async markLeadAsNotInterestedInAllCampaigns(auth, { leadEmail }) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.LEADS}/notinterested/${leadEmail}`,
      method: import_pieces_common.HttpMethod.POST,
      auth: auth.secret_text
    });
  },
  async markLeadAsNotInterestedInCampaign(auth, { campaignId, leadEmail }) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.CAMPAIGNS}/${campaignId}${API_ENDPOINTS.LEADS}/${leadEmail}/notinterested`,
      method: import_pieces_common.HttpMethod.POST,
      auth: auth.secret_text
    });
  },
  async pauseLeadInSpecificOrAllCampaigns(auth, { campaignId, leadEmail }) {
    let query = campaignId ? `?campaignId=${campaignId}` : "";
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.LEADS}/pause/${leadEmail}${query}`,
      method: import_pieces_common.HttpMethod.POST,
      auth: auth.secret_text
    });
  },
  async resumeLeadInSpecificOrAllCampaigns(auth, { campaignId, leadEmail }) {
    let query = campaignId ? `?campaignId=${campaignId}` : "";
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.LEADS}/start/${leadEmail}${query}`,
      method: import_pieces_common.HttpMethod.POST,
      auth: auth.secret_text
    });
  },
  async removeLeadFromUnsubscribeList(auth, { leadEmail }) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.UNSUBSCRIBES}/${leadEmail}`,
      method: import_pieces_common.HttpMethod.DELETE,
      auth: auth.secret_text
    });
  },
  async createWebhook(auth, payload) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.HOOKS}`,
      method: import_pieces_common.HttpMethod.POST,
      auth: auth.secret_text,
      body: payload
    });
  },
  async deleteWebhook(auth, webhookId) {
    return await fireHttpRequest({
      path: `${API_ENDPOINTS.HOOKS}/${webhookId}`,
      method: import_pieces_common.HttpMethod.DELETE,
      auth: auth.secret_text
    });
  }
};

// ../ap/packages/pieces/community/lemlist/src/lib/common/constants.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), BASE_URL = "https://api.lemlist.com/api", lemlistAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Lemlist API Key",
  description: `
  
  1.Click on your profile picture in the bottom left-hand corner and click on Settings.
  2.Go to the Integrations tab.
  3.Click on Generate a new API key.
  4.Store it securely because you won\u2019t be able to see it again`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await lemlistApiService.fetchTeams({
        secret_text: auth,
        type: import_pieces_framework2.AppConnectionType.SECRET_TEXT
      }).catch((err) => {
        throw err;
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: `Auth validation error: ${e.message}`
      };
    }
  }
}), API_ENDPOINTS = {
  TEAM: "/team",
  HOOKS: "/hooks",
  CAMPAIGNS: "/campaigns",
  LEADS: "/leads",
  UNSUBSCRIBES: "/unsubscribes"
};

// ../ap/packages/pieces/community/lemlist/src/lib/triggers/new-activity.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var CACHE_KEY = "lemlist_new_activity_trigger_key", newActivity = (0, import_pieces_framework3.createTrigger)({
  auth: lemlistAuth,
  name: "newActivity",
  classification: "READ",
  displayName: "New Activity",
  description: "Triggers for each new activity.",
  aiMetadata: {
    description: "Fires on each new Lemlist activity event received via webhook, covering campaign engagement signals such as emails sent, opened, clicked, replied, bounced, or unsubscribed. Represents a single activity record as it occurs."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await lemlistApiService.createWebhook(context.auth, {
      targetUrl: context.webhookUrl
    });
    await context.store.put(CACHE_KEY, {
      webhookId: response._id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY);
    cachedWebhookData && await lemlistApiService.deleteWebhook(context.auth, cachedWebhookData.webhookId).then(async () => {
      await context.store.delete(CACHE_KEY);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/triggers/unsubscribed-recipient.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var CACHE_KEY2 = "lemlist_unsubscribed_recipient_trigger_key", unsubscribedRecipient = (0, import_pieces_framework4.createTrigger)({
  auth: lemlistAuth,
  name: "unsubscribedRecipient",
  classification: "READ",
  displayName: "Unsubscribed Recipient",
  description: "Triggers when a recipient unsubscribes.",
  aiMetadata: {
    description: "Fires via webhook when a recipient unsubscribes from outreach (Lemlist emailsUnsubscribed event). Represents an opt-out signal for a single lead."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await lemlistApiService.createWebhook(context.auth, {
      targetUrl: context.webhookUrl,
      type: "emailsUnsubscribed"
    });
    await context.store.put(CACHE_KEY2, {
      webhookId: response._id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY2);
    cachedWebhookData && await lemlistApiService.deleteWebhook(context.auth, cachedWebhookData.webhookId).then(async () => {
      await context.store.delete(CACHE_KEY2);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/mark-lead-from-one-campaign-as-interested.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lemlist/src/lib/common/props.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var campaignsDropdown = ({
  refreshers,
  required = !0
}) => import_pieces_framework5.Property.Dropdown({
  auth: lemlistAuth,
  displayName: "Campaign",
  description: required ? "Select a campaign" : "Select a campaign (optional)",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Lemlist account first",
        options: []
      };
    try {
      return {
        options: (await lemlistApiService.fetchCampaigns(auth.secret_text)).map((c) => ({
          label: c.name,
          value: c._id
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch campaigns", e), {
        options: [],
        placeholder: "Unable to load campaigns"
      };
    }
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/mark-lead-from-one-campaign-as-interested.ts
var markLeadFromOneCampaignAsInterested = (0, import_pieces_framework6.createAction)({
  auth: lemlistAuth,
  name: "markLeadFromOneCampaignAsInterested",
  classification: "WRITE",
  displayName: "Mark Lead From One Campaign as Interested",
  description: "Mark a lead in a campaign as interested.",
  audience: "both",
  aiMetadata: { description: `Sets the "interested" status on a lead within one specific Lemlist campaign, identified by the campaign and the lead's email. Use when you want to flag interest scoped to a single campaign rather than across all of them. Idempotent: re-running leaves the lead in the same interested state.`, idempotent: !0 },
  props: {
    campaignId: campaignsDropdown({ refreshers: ["auth"] }),
    email: import_pieces_framework6.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to mark as interested.",
      required: !0
    })
  },
  async run({ auth, propsValue: { campaignId, email } }) {
    return await lemlistApiService.markLeadAsInterestedInCampaign(
      auth,
      {
        campaignId,
        leadEmail: email
      }
    );
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/mark-lead-from-one-campaign-as-not-interested.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var markLeadFromOneCampaignAsNotInterested = (0, import_pieces_framework7.createAction)({
  auth: lemlistAuth,
  name: "markLeadFromOneCampaignAsNotInterested",
  classification: "WRITE",
  displayName: "Mark Lead From One Campaign as Not Interested",
  description: "Mark a lead in a campaign as not interested.",
  audience: "both",
  aiMetadata: { description: `Sets the "not interested" status on a lead within one specific Lemlist campaign, identified by the campaign and the lead's email. Use when disqualifying interest scoped to a single campaign rather than across all of them. Idempotent: re-running leaves the lead in the same not-interested state.`, idempotent: !0 },
  props: {
    campaignId: campaignsDropdown({ refreshers: ["auth"] }),
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to mark as not interested.",
      required: !0
    })
  },
  async run({ auth, propsValue: { campaignId, email } }) {
    return await lemlistApiService.markLeadAsNotInterestedInCampaign(auth, {
      campaignId,
      leadEmail: email
    });
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/mark-lead-from-all-campaign-as-interested.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var markLeadFromAllCampaignAsInterested = (0, import_pieces_framework8.createAction)({
  auth: lemlistAuth,
  name: "markLeadFromAllCampaignAsInterested",
  classification: "WRITE",
  displayName: "Mark Lead From All Campaign as Interested",
  description: "Mark a lead as \u201Cinterested\u201D across all campaigns.",
  audience: "both",
  aiMetadata: { description: 'Sets the "interested" status on a lead, identified by email, across every Lemlist campaign the lead belongs to. Use when interest is not campaign-specific; for a single campaign use the per-campaign variant instead. Idempotent: re-running leaves the lead in the same interested state.', idempotent: !0 },
  props: {
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to mark as interested.",
      required: !0
    })
  },
  async run({ auth, propsValue: { email } }) {
    return await lemlistApiService.markLeadAsInterestedInAllCampaigns(auth, {
      leadEmail: email
    });
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/mark-lead-from-all-campaigns-as-not-interested.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var markLeadFromAllCampaignsAsNotInterested = (0, import_pieces_framework9.createAction)({
  auth: lemlistAuth,
  name: "markLeadFromAllCampaignsAsNotInterested",
  classification: "WRITE",
  displayName: "Mark Lead From All Campaigns as Not Interested",
  description: "Mark a lead as \u201Cnot interested\u201D across all campaigns.",
  audience: "both",
  aiMetadata: { description: 'Sets the "not interested" status on a lead, identified by email, across every Lemlist campaign the lead belongs to. Use when disqualifying interest globally; for a single campaign use the per-campaign variant instead. Idempotent: re-running leaves the lead in the same not-interested state.', idempotent: !0 },
  props: {
    email: import_pieces_framework9.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to mark as not interested.",
      required: !0
    })
  },
  async run({ auth, propsValue: { email } }) {
    return await lemlistApiService.markLeadAsNotInterestedInAllCampaigns(auth, {
      leadEmail: email
    });
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/pause-lead-from-all-or-specific-campaigns.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var pauseLeadFromAllOrSpecificCampaigns = (0, import_pieces_framework10.createAction)({
  auth: lemlistAuth,
  name: "pauseLeadFromAllOrSpecificCampaigns",
  classification: "DESTRUCTIVE",
  displayName: "Pause Lead From All or Specific Campaigns",
  description: "Pause a lead\u2019s outreach across all or specific campaigns.",
  audience: "both",
  aiMetadata: { description: "Pauses outreach sequencing for a lead, identified by email. Operates in two modes: supply a campaign to pause the lead only in that campaign, or leave the campaign empty to pause the lead across all campaigns. Idempotent: re-running leaves the lead paused.", idempotent: !0 },
  props: {
    campaignId: campaignsDropdown({ refreshers: ["auth"], required: !1 }),
    email: import_pieces_framework10.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to pause.",
      required: !0
    })
  },
  async run({ auth, propsValue: { campaignId, email } }) {
    return await lemlistApiService.pauseLeadInSpecificOrAllCampaigns(auth, {
      campaignId,
      leadEmail: email
    });
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/resume-lead-from-all-or-specific-campaigns.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var resumeLeadFromAllOrSpecificCampaigns = (0, import_pieces_framework11.createAction)({
  auth: lemlistAuth,
  name: "resumeLeadFromAllOrSpecificCampaigns",
  classification: "WRITE",
  displayName: "Resume Lead From All or Specific Campaigns",
  description: "Resume a lead\u2019s outreach across all or specific campaigns.",
  audience: "both",
  aiMetadata: { description: "Resumes (restarts) outreach sequencing for a previously paused lead, identified by email. Operates in two modes: supply a campaign to resume the lead only in that campaign, or leave the campaign empty to resume across all campaigns. Idempotent: re-running leaves the lead active.", idempotent: !0 },
  props: {
    campaignId: campaignsDropdown({ refreshers: ["auth"], required: !1 }),
    email: import_pieces_framework11.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to resume.",
      required: !0
    })
  },
  async run({ auth, propsValue: { campaignId, email } }) {
    return await lemlistApiService.resumeLeadInSpecificOrAllCampaigns(auth, {
      campaignId,
      leadEmail: email
    });
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/remove-lead-from-unsubscribe-list.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var removeLeadFromUnsubscribeList = (0, import_pieces_framework12.createAction)({
  auth: lemlistAuth,
  name: "removeLeadFromUnsubscribeList",
  classification: "WRITE",
  displayName: "Remove Lead From Unsubscribe List",
  description: "Remove a lead from \u201Cunsubscribe\u201D list.",
  audience: "both",
  aiMetadata: { description: "Removes a lead, identified by email, from the team-wide unsubscribe (do-not-contact) list so they become eligible for outreach again. Use to re-enable contacting a previously unsubscribed lead. Idempotent: re-running leaves the lead off the unsubscribe list.", idempotent: !0 },
  props: {
    email: import_pieces_framework12.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to remove from the unsubscribe list.",
      required: !0
    })
  },
  async run({ auth, propsValue: { email } }) {
    return await lemlistApiService.removeLeadFromUnsubscribeList(auth, {
      leadEmail: email
    });
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/remove-lead-from-a-campaign.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var removeLeadFromACampaign = (0, import_pieces_framework13.createAction)({
  auth: lemlistAuth,
  name: "removeLeadFromACampaign",
  classification: "WRITE",
  displayName: "Remove Lead from A Campaign",
  description: "removes a lead from a specific campaign.",
  audience: "both",
  aiMetadata: { description: "Removes a lead, identified by email, from one specific Lemlist campaign, stopping their sequence in it. Requires both the campaign and the lead email. Idempotent: re-running on an already-removed lead leaves them absent from the campaign.", idempotent: !0 },
  props: {
    campaignId: campaignsDropdown({ refreshers: ["auth"] }),
    email: import_pieces_framework13.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to remove.",
      required: !0
    })
  },
  async run({ auth, propsValue: { campaignId, email } }) {
    return await lemlistApiService.removeLeadFromACampaign(auth, {
      campaignId,
      leadEmail: email
    });
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/unsubscribe-a-lead.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var unsubscribeALead = (0, import_pieces_framework14.createAction)({
  auth: lemlistAuth,
  name: "unsubscribeALead",
  classification: "DESTRUCTIVE",
  displayName: "Unsubscribe a Lead",
  description: "Unsubscribe a lead from a campaign.",
  audience: "both",
  aiMetadata: { description: "Unsubscribes a lead, identified by email, from a specific Lemlist campaign, stopping further emails to them in it. Use to honor an opt-out or stop contacting a lead in one campaign; requires both the campaign and the lead email. Idempotent: re-running leaves the lead unsubscribed.", idempotent: !0 },
  props: {
    campaignId: campaignsDropdown({ refreshers: ["auth"] }),
    email: import_pieces_framework14.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to unsubscribe.",
      required: !0
    })
  },
  async run({ auth, propsValue: { campaignId, email } }) {
    return await lemlistApiService.unsubscribeLeadFromACampaign(auth, {
      campaignId,
      leadEmail: email
    });
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/add-lead-to-a-campaign.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var addLeadToACampaign = (0, import_pieces_framework15.createAction)({
  auth: lemlistAuth,
  name: "addLeadToACampaign",
  classification: "WRITE",
  displayName: "Add Lead To A Campaign",
  description: "Adds a lead to a specific campaign with optional enrichment and lead details.",
  audience: "both",
  aiMetadata: { description: "Adds a lead (by email, with optional name, company, phone, LinkedIn and other details) to a specific Lemlist campaign, optionally running enrichment such as email/phone finding, email verification, or LinkedIn lookup. Use to enroll a new contact into outreach. Not idempotent: each call adds the lead and re-runs requested enrichment unless the deduplicate option is enabled to skip leads already present in any campaign.", idempotent: !1 },
  props: {
    campaignId: campaignsDropdown({ refreshers: ["auth"] }),
    email: import_pieces_framework15.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to add",
      required: !0
    }),
    firstName: import_pieces_framework15.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework15.Property.ShortText({ displayName: "Last Name", required: !1 }),
    picture: import_pieces_framework15.Property.ShortText({
      displayName: "Picture URL",
      required: !1
    }),
    phone: import_pieces_framework15.Property.ShortText({ displayName: "Phone", required: !1 }),
    linkedinUrl: import_pieces_framework15.Property.ShortText({
      displayName: "LinkedIn URL",
      required: !1
    }),
    companyName: import_pieces_framework15.Property.ShortText({
      displayName: "Company Name",
      required: !1
    }),
    companyDomain: import_pieces_framework15.Property.ShortText({
      displayName: "Company Domain",
      required: !1
    }),
    icebreaker: import_pieces_framework15.Property.LongText({
      displayName: "Icebreaker",
      required: !1
    }),
    jobTitle: import_pieces_framework15.Property.ShortText({ displayName: "Job Title", required: !1 }),
    deduplicate: import_pieces_framework15.Property.Checkbox({
      displayName: "Deduplicate",
      description: "Avoid adding the lead if it already exists in any campaign",
      required: !1,
      defaultValue: !1
    }),
    linkedinEnrichment: import_pieces_framework15.Property.Checkbox({
      displayName: "LinkedIn Enrichment",
      description: "Run LinkedIn enrichment for this lead",
      required: !1,
      defaultValue: !1
    }),
    findEmail: import_pieces_framework15.Property.Checkbox({
      displayName: "Find Email",
      description: "Try to find a verified email for this lead",
      required: !1,
      defaultValue: !1
    }),
    verifyEmail: import_pieces_framework15.Property.Checkbox({
      displayName: "Verify Email",
      description: "Verify the lead\u2019s email using debounce",
      required: !1,
      defaultValue: !1
    }),
    findPhone: import_pieces_framework15.Property.Checkbox({
      displayName: "Find Phone",
      description: "Try to find a phone number for this lead",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    let _a = propsValue, {
      campaignId,
      email,
      deduplicate,
      linkedinEnrichment,
      findEmail,
      verifyEmail,
      findPhone
    } = _a, leadData = __objRest(_a, [
      "campaignId",
      "email",
      "deduplicate",
      "linkedinEnrichment",
      "findEmail",
      "verifyEmail",
      "findPhone"
    ]);
    return await lemlistApiService.addLeadToACampaign(auth, {
      campaignId,
      leadEmail: email,
      leadData,
      deduplicate,
      linkedinEnrichment,
      findEmail,
      verifyEmail,
      findPhone
    });
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/add-custom-variables-on-lead.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var addCustomVariablesOnLead = (0, import_pieces_framework16.createAction)({
  auth: lemlistAuth,
  name: "addCustomVariablesOnLead",
  classification: "WRITE",
  displayName: "Add Custom Variables on Lead",
  description: "Add new custom variables to a lead.",
  audience: "both",
  aiMetadata: {
    description: "Creates new custom variables on a specific lead (identified by its Lemlist lead id) and sets each variable\u2019s value for that lead. Variable names must be new \u2014 the call fails if a name matches a default field (email, firstName, lastName, picture, phone, linkedinUrl, companyName, companyDomain, icebreaker) or a previously created custom variable. Use to add extra mail-merge fields beyond the standard ones. Not idempotent: re-running with the same variable names returns an error.",
    idempotent: !1
  },
  props: {
    leadId: import_pieces_framework16.Property.ShortText({
      displayName: "Lead ID",
      description: "The unique identifier of the lead (e.g. lea_8xJSc7sV7ggpiVnXe). Available as the _id field returned by the Add Lead To A Campaign and Search Lead actions.",
      required: !0
    }),
    customVariables: import_pieces_framework16.Property.Object({
      displayName: "Custom Variables",
      description: "New custom variables to create on the lead. Each key is the variable name and each value is its content. Names must not match an existing default or custom variable.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { leadId, customVariables } = propsValue;
    return await lemlistApiService.addLeadVariables(auth, {
      leadId,
      variables: customVariables != null ? customVariables : {}
    });
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/update-lead-from-campaign.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var updateLeadFromCampaign = (0, import_pieces_framework17.createAction)({
  auth: lemlistAuth,
  name: "updateLeadFromCampaign",
  classification: "WRITE",
  displayName: "Update Lead From Campaign",
  description: "Updates a lead in a specific campaign.",
  audience: "both",
  aiMetadata: { description: "Updates an existing lead's details (name, company, phone, LinkedIn, icebreaker, job title, etc.) within a specific Lemlist campaign, identified by the campaign and the lead's email. Use to edit profile fields on a lead already enrolled in a campaign. Idempotent: re-applying the same field values yields the same result.", idempotent: !0 },
  props: {
    campaignId: campaignsDropdown({ refreshers: ["auth"] }),
    email: import_pieces_framework17.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to update.",
      required: !0
    }),
    firstName: import_pieces_framework17.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework17.Property.ShortText({ displayName: "Last Name", required: !1 }),
    picture: import_pieces_framework17.Property.ShortText({
      displayName: "Picture URL",
      required: !1
    }),
    phone: import_pieces_framework17.Property.ShortText({ displayName: "Phone", required: !1 }),
    linkedinUrl: import_pieces_framework17.Property.ShortText({
      displayName: "LinkedIn URL",
      required: !1
    }),
    companyName: import_pieces_framework17.Property.ShortText({
      displayName: "Company Name",
      required: !1
    }),
    companyDomain: import_pieces_framework17.Property.ShortText({
      displayName: "Company Domain",
      required: !1
    }),
    icebreaker: import_pieces_framework17.Property.LongText({
      displayName: "Icebreaker",
      required: !1
    }),
    jobTitle: import_pieces_framework17.Property.ShortText({ displayName: "Job Title", required: !1 })
  },
  async run({ auth, propsValue }) {
    let _a = propsValue, {
      campaignId,
      email
    } = _a, leadData = __objRest(_a, [
      "campaignId",
      "email"
    ]);
    return await lemlistApiService.updateLeadFromCampaign(auth, {
      campaignId,
      leadEmail: email,
      leadData
    });
  }
});

// ../ap/packages/pieces/community/lemlist/src/lib/actions/search-lead.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var searchLead = (0, import_pieces_framework18.createAction)({
  auth: lemlistAuth,
  name: "searchLead",
  classification: "READ",
  displayName: "Search Lead",
  description: "Look up a lead by email and campaign.",
  audience: "both",
  aiMetadata: { description: "Looks up a lead by email across the account and returns matching lead records. Operates in two modes: supply a campaign to filter results to that campaign, or leave it empty to return all campaigns the lead belongs to; returns a not-found result when no match exists. Idempotent read-only lookup.", idempotent: !0 },
  props: {
    campaignId: campaignsDropdown({ refreshers: ["auth"], required: !1 }),
    email: import_pieces_framework18.Property.ShortText({
      displayName: "Lead Email",
      description: "The email of the lead to look up",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { email, campaignId } = propsValue, leads = await lemlistApiService.getLeadByEmail(auth, email);
    if (!Array.isArray(leads) || leads.length === 0)
      return { found: !1, message: `No leads found for ${email}` };
    let matching = campaignId ? leads.filter((lead) => lead.campaign.id === campaignId) : leads;
    return matching.length === 0 ? {
      found: !1,
      message: `Lead with email ${email} not found in campaign ${campaignId}`
    } : { found: !0, leads: matching };
  }
});

// ../ap/packages/pieces/community/lemlist/src/index.ts
var lemlist = (0, import_pieces_framework19.createPiece)({
  displayName: "Lemlist",
  auth: lemlistAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/lemlist.png",
  authors: ["gs03-dev"],
  actions: [
    markLeadFromOneCampaignAsInterested,
    markLeadFromOneCampaignAsNotInterested,
    markLeadFromAllCampaignAsInterested,
    markLeadFromAllCampaignsAsNotInterested,
    pauseLeadFromAllOrSpecificCampaigns,
    resumeLeadFromAllOrSpecificCampaigns,
    removeLeadFromUnsubscribeList,
    removeLeadFromACampaign,
    unsubscribeALead,
    addLeadToACampaign,
    addCustomVariablesOnLead,
    updateLeadFromCampaign,
    searchLead
  ],
  triggers: [newActivity, unsubscribedRecipient]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  lemlist
});
