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

// ../ap/packages/pieces/community/linkupapi/src/index.ts
var index_exports = {};
__export(index_exports, {
  linkupapi: () => linkupapi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_framework16 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/linkupapi/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), LINKUPAPI_BASE_URL = "https://api.linkupapi.com/v2", linkupAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Your LinkupAPI key, sent as the `x-api-key` header. Get it from https://app.linkupapi.com",
  validate: async ({ auth }) => {
    var _a;
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${LINKUPAPI_BASE_URL}/accounts`,
        headers: { "x-api-key": auth },
        queryParams: { limit: "1" }
      }), { valid: !0 };
    } catch (e) {
      let status = (_a = e == null ? void 0 : e.response) == null ? void 0 : _a.status;
      return status === 401 || status === 403 ? {
        valid: !1,
        error: "Invalid API key. Check it at https://app.linkupapi.com"
      } : {
        valid: !1,
        error: `Could not reach LinkupAPI to validate the key (status ${status != null ? status : "network error"}). Please try again.`
      };
    }
  }
}), accountIdProp = import_pieces_framework.Property.ShortText({
  displayName: "Account ID",
  description: 'The connected LinkedIn account to act as. Get it from the "List Accounts" action.',
  required: !0
});
async function linkupAction(apiKey, category, action, accountId, params = {}) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${LINKUPAPI_BASE_URL}/${category}`,
    headers: { "x-api-key": apiKey, "Content-Type": "application/json" },
    body: { account_id: accountId, action, params: cleanParams(params) }
  })).body;
}
async function linkupPost(apiKey, path, body) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${LINKUPAPI_BASE_URL}${path}`,
    headers: { "x-api-key": apiKey, "Content-Type": "application/json" },
    body
  })).body;
}
async function linkupGet(apiKey, path, queryParams = {}) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${LINKUPAPI_BASE_URL}${path}`,
    headers: { "x-api-key": apiKey },
    queryParams
  })).body;
}
async function linkupDelete(apiKey, path) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.DELETE,
    url: `${LINKUPAPI_BASE_URL}${path}`,
    headers: { "x-api-key": apiKey }
  })).body;
}
function cleanParams(params) {
  let out = {};
  for (let [k, v] of Object.entries(params))
    v != null && v !== "" && (out[k] = v);
  return out;
}

// ../ap/packages/pieces/community/linkupapi/src/lib/actions/list-accounts.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var listAccounts = (0, import_pieces_framework2.createAction)({
  auth: linkupAuth,
  name: "list_accounts",
  displayName: "List Accounts",
  description: "List all connected LinkedIn accounts linked to your API key. Free (0 credits). Use the returned account_id in other actions.",
  audience: "both",
  aiMetadata: { description: "Lists the LinkedIn accounts connected to this LinkupAPI key, paged by limit and offset. Call this first to obtain the account ID that every other LinkupAPI action and trigger requires; prefer Get Account Details when the ID is already known. Read-only and idempotent.", idempotent: !0 },
  props: {
    limit: import_pieces_framework2.Property.Number({
      displayName: "Limit",
      description: "Max accounts to return per page (1-500)",
      required: !1,
      defaultValue: 50
    }),
    offset: import_pieces_framework2.Property.Number({
      displayName: "Offset",
      description: "Number of accounts to skip (for pagination)",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let { limit, offset } = context.propsValue;
    return linkupGet(context.auth.secret_text, "/accounts", {
      limit: String(limit != null ? limit : 50),
      offset: String(offset != null ? offset : 0)
    });
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/actions/get-account.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var getAccount = (0, import_pieces_framework3.createAction)({
  auth: linkupAuth,
  name: "get_account",
  displayName: "Get Account Details",
  description: "Get detailed information and status for a specific connected account. Free (0 credits).",
  audience: "both",
  aiMetadata: { description: "Fetches the integration details and connection status of one connected LinkedIn account by its account ID. Use List Accounts instead when the ID is unknown, and Get My Profile when the LinkedIn profile behind the account is wanted rather than its connection state. Requires a valid account ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    accountId: import_pieces_framework3.Property.ShortText({
      displayName: "Account ID",
      description: 'The account to retrieve (from "List Accounts")',
      required: !0
    })
  },
  async run(context) {
    let { accountId } = context.propsValue;
    return linkupGet(
      context.auth.secret_text,
      `/accounts/${encodeURIComponent(accountId)}`
    );
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/actions/get-my-profile.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var getMyProfile = (0, import_pieces_framework4.createAction)({
  auth: linkupAuth,
  name: "get_my_profile",
  displayName: "Get My Profile",
  description: "Retrieve the profile of the connected LinkedIn account.",
  audience: "both",
  aiMetadata: { description: "Retrieves the LinkedIn profile of the connected account itself, resolving who the automation is acting as. Use Get Profile Info for anyone else. Requires the account ID from List Accounts. Read-only and idempotent.", idempotent: !0 },
  props: {
    accountId: accountIdProp
  },
  async run(context) {
    return linkupAction(
      context.auth.secret_text,
      "profiles",
      "get_me",
      context.propsValue.accountId
    );
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/actions/get-profile.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var getProfile = (0, import_pieces_framework5.createAction)({
  auth: linkupAuth,
  name: "get_profile",
  displayName: "Get Profile Info",
  description: "Retrieve detailed information about a LinkedIn profile by URL, public identifier, or URN.",
  audience: "both",
  aiMetadata: { description: "Looks up a LinkedIn person profile addressed in one of three interchangeable ways: full profile URL, public identifier, or profile URN. Use it when a specific person is already identified; use Search People to discover profiles from attributes, and Get My Profile for the connected account. Requires the account ID plus exactly one of the three address inputs. Read-only and idempotent.", idempotent: !0 },
  props: {
    accountId: accountIdProp,
    profileUrl: import_pieces_framework5.Property.ShortText({
      displayName: "Profile URL",
      description: "Full LinkedIn profile URL (provide this OR Identifier OR URN)",
      required: !1
    }),
    identifier: import_pieces_framework5.Property.ShortText({
      displayName: "Identifier",
      description: 'Public identifier (e.g. "john-doe")',
      required: !1
    }),
    profileUrn: import_pieces_framework5.Property.ShortText({
      displayName: "Profile URN",
      description: "LinkedIn profile URN",
      required: !1
    })
  },
  async run(context) {
    let { accountId, profileUrl, identifier, profileUrn } = context.propsValue;
    return linkupAction(context.auth.secret_text, "profiles", "get", accountId, {
      profile_url: profileUrl,
      identifier,
      profile_urn: profileUrn
    });
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/actions/search-people.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var searchPeople = (0, import_pieces_framework6.createAction)({
  auth: linkupAuth,
  name: "search_people",
  displayName: "Search People",
  description: "Search LinkedIn profiles by keyword, company, title, location, school, industry and more. Returns the LinkedIn URL of each match.",
  audience: "both",
  aiMetadata: { description: "Searches LinkedIn people by any combination of keyword, name, title, company, location, school, industry, or connection degree; alternatively it can enumerate the connections or followers of one given profile instead of running an attribute search. Use it to discover prospects when no profile URL is known, then switch to Get Profile Info once one is. Requires the account ID from List Accounts, and the result count is capped by Total Results. Read-only and idempotent.", idempotent: !0 },
  props: {
    accountId: accountIdProp,
    keyword: import_pieces_framework6.Property.ShortText({ displayName: "Keyword", required: !1 }),
    firstName: import_pieces_framework6.Property.ShortText({ displayName: "First Name", required: !1 }),
    lastName: import_pieces_framework6.Property.ShortText({ displayName: "Last Name", required: !1 }),
    title: import_pieces_framework6.Property.ShortText({ displayName: "Current Job Title", required: !1 }),
    companyName: import_pieces_framework6.Property.ShortText({ displayName: "Current Company Name", required: !1 }),
    companyUrl: import_pieces_framework6.Property.ShortText({
      displayName: "Company URL",
      description: "LinkedIn company page URL",
      required: !1
    }),
    pastCompany: import_pieces_framework6.Property.ShortText({ displayName: "Past Company", required: !1 }),
    location: import_pieces_framework6.Property.ShortText({
      displayName: "Location",
      description: 'e.g. "San Francisco"',
      required: !1
    }),
    schoolUrl: import_pieces_framework6.Property.ShortText({ displayName: "School URL", required: !1 }),
    industry: import_pieces_framework6.Property.ShortText({ displayName: "Industry", required: !1 }),
    network: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Network",
      description: "Connection degree relative to the account",
      required: !1,
      options: {
        options: [
          { label: "1st degree (F)", value: "F" },
          { label: "2nd degree (S)", value: "S" },
          { label: "3rd+ degree (O)", value: "O" }
        ]
      }
    }),
    connectionOf: import_pieces_framework6.Property.ShortText({
      displayName: "Connection Of",
      description: "Show connections of a specific profile (URL or identifier)",
      required: !1
    }),
    followerOf: import_pieces_framework6.Property.ShortText({
      displayName: "Follower Of",
      description: "Show followers of a specific company or profile",
      required: !1
    }),
    fetchInvitationState: import_pieces_framework6.Property.Checkbox({
      displayName: "Fetch Invitation State",
      description: "Include the connection invitation state for each result",
      required: !1,
      defaultValue: !0
    }),
    totalResults: import_pieces_framework6.Property.Number({
      displayName: "Total Results",
      description: "Number of results to return (1 credit per 10 results)",
      required: !1,
      defaultValue: 10
    }),
    startPage: import_pieces_framework6.Property.Number({ displayName: "Start Page", required: !1 }),
    endPage: import_pieces_framework6.Property.Number({ displayName: "End Page", required: !1 })
  },
  async run(context) {
    let p = context.propsValue;
    return linkupAction(context.auth.secret_text, "profiles", "search_people", p.accountId, {
      keyword: p.keyword,
      first_name: p.firstName,
      last_name: p.lastName,
      title: p.title,
      company_name: p.companyName,
      company_url: p.companyUrl,
      past_company: p.pastCompany,
      location: p.location,
      school_url: p.schoolUrl,
      industry: p.industry,
      network: p.network,
      connection_of: p.connectionOf,
      follower_of: p.followerOf,
      fetch_invitation_state: p.fetchInvitationState,
      total_results: p.totalResults,
      start_page: p.startPage,
      end_page: p.endPage
    });
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/actions/search-companies.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var searchCompanies = (0, import_pieces_framework7.createAction)({
  auth: linkupAuth,
  name: "search_companies",
  displayName: "Search Companies",
  description: "Search LinkedIn companies by keyword, location, sector and company size.",
  audience: "both",
  aiMetadata: { description: "Searches LinkedIn company pages by keyword, location, sector, or company size, over a configurable page range. Use it to locate a company page when only a name or profile of the firm is known; call Get Company Info afterwards for the full record of a known company URL, and Search People to find individuals. Requires the account ID from List Accounts. Read-only and idempotent.", idempotent: !0 },
  props: {
    accountId: accountIdProp,
    keyword: import_pieces_framework7.Property.ShortText({ displayName: "Keyword", required: !1 }),
    location: import_pieces_framework7.Property.ShortText({ displayName: "Location", required: !1 }),
    sector: import_pieces_framework7.Property.ShortText({ displayName: "Sector", required: !1 }),
    companySize: import_pieces_framework7.Property.ShortText({
      displayName: "Company Size",
      description: 'e.g. "11-50", "201-500"',
      required: !1
    }),
    totalResults: import_pieces_framework7.Property.Number({
      displayName: "Total Results",
      required: !1,
      defaultValue: 10
    }),
    resultsPerPage: import_pieces_framework7.Property.Number({ displayName: "Results Per Page", required: !1 }),
    startPage: import_pieces_framework7.Property.Number({ displayName: "Start Page", required: !1 }),
    endPage: import_pieces_framework7.Property.Number({ displayName: "End Page", required: !1 })
  },
  async run(context) {
    let p = context.propsValue;
    return linkupAction(context.auth.secret_text, "profiles", "search_companies", p.accountId, {
      keyword: p.keyword,
      location: p.location,
      sector: p.sector,
      company_size: p.companySize,
      total_results: p.totalResults,
      results_per_page: p.resultsPerPage,
      start_page: p.startPage,
      end_page: p.endPage
    });
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/actions/get-company.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var getCompany = (0, import_pieces_framework8.createAction)({
  auth: linkupAuth,
  name: "get_company",
  displayName: "Get Company Info",
  description: "Retrieve detailed information about a LinkedIn company page.",
  audience: "both",
  aiMetadata: { description: "Looks up one LinkedIn company page from its URL. Use Search Companies first when only a company name is known, and Get Profile Info for a person rather than an organisation. Requires both the account ID and an exact company page URL. Read-only and idempotent.", idempotent: !0 },
  props: {
    accountId: accountIdProp,
    companyUrl: import_pieces_framework8.Property.ShortText({
      displayName: "Company URL",
      description: "LinkedIn company page URL",
      required: !0
    })
  },
  async run(context) {
    let { accountId, companyUrl } = context.propsValue;
    return linkupAction(context.auth.secret_text, "profiles", "get_company", accountId, {
      company_url: companyUrl
    });
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/actions/send-message.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var sendMessage = (0, import_pieces_framework9.createAction)({
  auth: linkupAuth,
  name: "send_message",
  displayName: "Send Message",
  description: "Send a LinkedIn message to a profile, optionally with a media attachment.",
  audience: "both",
  aiMetadata: { description: "Sends a LinkedIn direct message from the connected account to a recipient profile URL, optionally attaching a media link. Use it for outreach or replies to an existing thread; Send Connection Request is the right action when the recipient cannot be messaged yet, and Get Conversation loads the thread context first. Requires the account ID, the recipient profile URL, and message text. Not idempotent: every call delivers another message, so retries duplicate it.", idempotent: !1 },
  props: {
    accountId: accountIdProp,
    profileUrl: import_pieces_framework9.Property.ShortText({
      displayName: "Profile URL",
      description: "LinkedIn profile URL of the recipient",
      required: !0
    }),
    messageText: import_pieces_framework9.Property.LongText({
      displayName: "Message",
      description: "The text of the message to send",
      required: !0
    }),
    mediaLink: import_pieces_framework9.Property.ShortText({
      displayName: "Media Link",
      description: "Optional URL of a media file to attach",
      required: !1
    })
  },
  async run(context) {
    let { accountId, profileUrl, messageText, mediaLink } = context.propsValue;
    return linkupAction(context.auth.secret_text, "messages", "send", accountId, {
      profile_url: profileUrl,
      message_text: messageText,
      media_link: mediaLink
    });
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/actions/get-conversation.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var getConversation = (0, import_pieces_framework10.createAction)({
  auth: linkupAuth,
  name: "get_conversation",
  displayName: "Get Conversation",
  description: "Retrieve the message history of a LinkedIn conversation, by conversation ID or profile URL.",
  audience: "both",
  aiMetadata: { description: "Reads the message history of one LinkedIn thread, addressed either by conversation ID or by the other participant's profile URL, with count and cursor paging for longer threads. Use it to gather context before replying with Send Message. Requires the account ID plus one of the two addressing inputs. Read-only and idempotent.", idempotent: !0 },
  props: {
    accountId: accountIdProp,
    conversationId: import_pieces_framework10.Property.ShortText({
      displayName: "Conversation ID",
      description: "Provide this OR a Profile URL",
      required: !1
    }),
    profileUrl: import_pieces_framework10.Property.ShortText({
      displayName: "Profile URL",
      description: "Profile URL of the other participant",
      required: !1
    }),
    count: import_pieces_framework10.Property.Number({
      displayName: "Count",
      description: "Number of messages to retrieve",
      required: !1
    }),
    cursor: import_pieces_framework10.Property.ShortText({
      displayName: "Cursor",
      description: "Pagination cursor from a previous response",
      required: !1
    })
  },
  async run(context) {
    let { accountId, conversationId, profileUrl, count, cursor } = context.propsValue;
    return linkupAction(context.auth.secret_text, "messages", "get_conversation", accountId, {
      conversation_id: conversationId,
      profile_url: profileUrl,
      count,
      cursor
    });
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/actions/send-connection-request.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var sendConnectionRequest = (0, import_pieces_framework11.createAction)({
  auth: linkupAuth,
  name: "send_connection_request",
  displayName: "Send Connection Request",
  description: "Send a LinkedIn connection invitation to a profile, with an optional note.",
  audience: "both",
  aiMetadata: { description: "Sends a LinkedIn connection invitation from the connected account to a person addressed by either profile URL or public identifier, with an optional note. Use it to open contact with someone outside the account network; Send Message is for people already reachable. Requires the account ID and one of the two address inputs. Not idempotent: each call issues a fresh invitation and consumes LinkedIn invite quota, so check Check Invitation Status before retrying.", idempotent: !1 },
  props: {
    accountId: accountIdProp,
    profileUrl: import_pieces_framework11.Property.ShortText({
      displayName: "Profile URL",
      description: "LinkedIn profile URL (provide this OR Identifier)",
      required: !1
    }),
    identifier: import_pieces_framework11.Property.ShortText({
      displayName: "Identifier",
      description: 'Public identifier (e.g. "john-doe")',
      required: !1
    }),
    message: import_pieces_framework11.Property.LongText({
      displayName: "Note",
      description: "Optional note to include with the invitation",
      required: !1
    })
  },
  async run(context) {
    let { accountId, profileUrl, identifier, message } = context.propsValue;
    return linkupAction(context.auth.secret_text, "network", "invite", accountId, {
      profile_url: profileUrl,
      identifier,
      message
    });
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/actions/check-invitation-status.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var checkInvitationStatus = (0, import_pieces_framework12.createAction)({
  auth: linkupAuth,
  name: "check_invitation_status",
  displayName: "Check Invitation Status",
  description: "Check the status of a connection invitation sent to a profile.",
  audience: "both",
  aiMetadata: { description: "Checks the current state of a connection invitation sent from the connected account to a given profile URL. Use it to poll the outcome of Send Connection Request rather than re-sending; the Invitation Accepted trigger is the push alternative for flows that can wait. Requires the account ID and the target profile URL. Read-only and idempotent.", idempotent: !0 },
  props: {
    accountId: accountIdProp,
    profileUrl: import_pieces_framework12.Property.ShortText({
      displayName: "Profile URL",
      description: "LinkedIn profile URL to check",
      required: !0
    })
  },
  async run(context) {
    let { accountId, profileUrl } = context.propsValue;
    return linkupAction(context.auth.secret_text, "network", "check_invitation", accountId, {
      profile_url: profileUrl
    });
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/triggers/new-message-received.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var STORE_KEY = "_linkup_new_message_webhook_id", newMessageReceived = (0, import_pieces_framework13.createTrigger)({
  auth: linkupAuth,
  name: "new_message_received",
  displayName: "New Message Received",
  description: "Fires in real time when the connected LinkedIn account receives a new message. Registers a LinkupAPI webhook (\u224810 credits/day per monitored account while active).",
  aiMetadata: {
    description: "Fires once for every inbound direct message received by one connected LinkedIn account, in real time via a LinkupAPI webhook. Use it to start reply, triage, or CRM-logging automations on incoming LinkedIn conversations; it covers only messages received, not messages the account sends, and not invitation activity (use Invitation Accepted for that). Each enabled instance registers a webhook against a single account ID and bills credits daily while the flow stays on."
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  props: {
    accountId: accountIdProp
  },
  sampleData: {
    timestamp: "2026-06-17T12:00:00Z",
    event: {
      type: "message",
      account_id: "your-account-id",
      data: {
        conversation_id: "2-abc123",
        sender_profile_url: "https://www.linkedin.com/in/janedoe",
        message_text: "Hi, thanks for connecting!"
      },
      timestamp: "2026-06-17T11:59:58Z"
    }
  },
  async onEnable(context) {
    var _a;
    let res = await linkupPost(
      context.auth.secret_text,
      "/webhooks",
      {
        account_id: context.propsValue.accountId,
        url: context.webhookUrl,
        events: ["message_received"]
      }
    );
    (_a = res == null ? void 0 : res.data) != null && _a.webhook_id && await context.store.put(STORE_KEY, res.data.webhook_id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(STORE_KEY);
    webhookId && await linkupDelete(context.auth.secret_text, `/webhooks/${webhookId}`);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/linkupapi/src/lib/triggers/invitation-accepted.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var STORE_KEY2 = "_linkup_invitation_accepted_webhook_id", invitationAccepted = (0, import_pieces_framework14.createTrigger)({
  auth: linkupAuth,
  name: "invitation_accepted",
  displayName: "Invitation Accepted",
  description: "Fires in real time when a connection request sent from the connected LinkedIn account is accepted. Registers a LinkupAPI webhook (\u224810 credits/day per monitored account while active).",
  aiMetadata: {
    description: "Fires once each time someone accepts a connection invitation previously sent from the connected LinkedIn account, in real time via a LinkupAPI webhook. Use it to continue outreach the moment a prospect connects instead of polling Check Invitation Status; it does not fire for invitations received by the account or for new messages (use New Message Received for those). Each enabled instance registers a webhook against a single account ID and bills credits daily while the flow stays on."
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  props: {
    accountId: accountIdProp
  },
  sampleData: {
    timestamp: "2026-06-17T12:00:00Z",
    event: {
      type: "accepted_invitation",
      account_id: "your-account-id",
      data: {
        profile_url: "https://www.linkedin.com/in/janedoe",
        first_name: "Jane",
        last_name: "Doe"
      },
      timestamp: "2026-06-17T11:59:58Z"
    }
  },
  async onEnable(context) {
    var _a;
    let res = await linkupPost(
      context.auth.secret_text,
      "/webhooks",
      {
        account_id: context.propsValue.accountId,
        url: context.webhookUrl,
        events: ["accepted_invitation"]
      }
    );
    (_a = res == null ? void 0 : res.data) != null && _a.webhook_id && await context.store.put(STORE_KEY2, res.data.webhook_id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(STORE_KEY2);
    webhookId && await linkupDelete(context.auth.secret_text, `/webhooks/${webhookId}`);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/linkupapi/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), linkupapi = (0, import_pieces_framework15.createPiece)({
  displayName: "LinkupAPI",
  description: "LinkupAPI lets agents send messages, manage conversations, and automate outreach across every channel, for sales and recruitment.",
  auth: linkupAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/linkupapi.png",
  categories: [import_pieces_framework16.PieceCategory.SALES_AND_CRM, import_pieces_framework16.PieceCategory.MARKETING],
  authors: ["titouanprx", "sanket-a11y"],
  actions: [
    listAccounts,
    getAccount,
    getMyProfile,
    getProfile,
    searchPeople,
    searchCompanies,
    getCompany,
    sendMessage,
    getConversation,
    sendConnectionRequest,
    checkInvitationStatus,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => LINKUPAPI_BASE_URL,
      auth: linkupAuth,
      authMapping: async (auth) => ({
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: [newMessageReceived, invitationAccepted]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  linkupapi
});
