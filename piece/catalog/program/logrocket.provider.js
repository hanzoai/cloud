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

// ../ap/packages/pieces/community/logrocket/src/index.ts
var index_exports = {};
__export(index_exports, {
  logrocket: () => logrocket
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/logrocket/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), logrocketAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To get your LogRocket API key:

1. **Login to your LogRocket Dashboard**
2. **Go to Settings > General Settings**
3. **Copy your API key** from the API Key section
4. **Paste it here**

Your API key is used to authenticate requests to the LogRocket API.
  `,
  required: !0
});

// ../ap/packages/pieces/community/logrocket/src/lib/actions/request-highlights.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), requestHighlights = (0, import_pieces_framework2.createAction)({
  auth: logrocketAuth,
  name: "requestHighlights",
  displayName: "Request Highlights",
  description: "Request session highlights for a user. Results will be sent to the webhook URL when ready.",
  audience: "both",
  aiMetadata: { description: `Kicks off an asynchronous LogRocket job that generates AI summaries of a user's recorded sessions; the results are not returned inline but POSTed to the given webhook URL once ready (pair with the "Highlights Ready" trigger). Identify the user by either email or user ID (one is required), and optionally scope to a time range (both start and end must be set together). Not idempotent: each call enqueues a new highlights request.`, idempotent: !1 },
  props: {
    orgId: import_pieces_framework2.Property.ShortText({
      displayName: "Organization ID",
      description: "Your LogRocket organization ID (found in Settings > General Settings, or in your App ID before the slash)",
      required: !0
    }),
    projectId: import_pieces_framework2.Property.ShortText({
      displayName: "Project ID",
      description: "Your LogRocket project ID (found in Settings > General Settings, or in your App ID after the slash)",
      required: !0
    }),
    userEmail: import_pieces_framework2.Property.ShortText({
      displayName: "User Email",
      description: "The email of the user to get highlights for",
      required: !1
    }),
    userId: import_pieces_framework2.Property.ShortText({
      displayName: "User ID",
      description: "The ID of the user from LogRocket.identify() calls",
      required: !1
    }),
    timeRangeStart: import_pieces_framework2.Property.DateTime({
      displayName: "Start Time",
      description: "Start of the time range for sessions (optional)",
      required: !1
    }),
    timeRangeEnd: import_pieces_framework2.Property.DateTime({
      displayName: "End Time",
      description: "End of the time range for sessions (optional)",
      required: !1
    }),
    webhookUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Webhook URL",
      description: 'URL where highlights results will be posted when ready. You can get this from the "Highlights Ready" trigger.',
      required: !0
    })
  },
  async run(context) {
    let { orgId, projectId, userEmail, userId, timeRangeStart, timeRangeEnd, webhookUrl } = context.propsValue, apiKey = context.auth;
    if (!userEmail && !userId)
      throw new Error("Either userEmail or userId must be provided");
    let body = {
      webhookURL: webhookUrl
    };
    if (userEmail && (body.userEmail = userEmail), userId && (body.userID = userId), timeRangeStart && timeRangeEnd) {
      let startMs = new Date(timeRangeStart).getTime(), endMs = new Date(timeRangeEnd).getTime();
      if (startMs >= endMs)
        throw new Error("Start time must be before end time");
      body.timeRange = {
        startMs,
        endMs
      };
    } else if (timeRangeStart || timeRangeEnd)
      throw new Error("Both start time and end time must be provided if using time range");
    try {
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: `https://api.logrocket.com/v1/orgs/${orgId}/apps/${projectId}/highlights/`,
        headers: {
          Authorization: `token ${apiKey}`,
          "Content-Type": "application/json"
        },
        body
      })).body;
    } catch (error) {
      throw new Error(
        `Failed to request highlights: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/logrocket/src/lib/actions/identify-user.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), identifyUser = (0, import_pieces_framework3.createAction)({
  auth: logrocketAuth,
  name: "identifyUser",
  displayName: "Identify User",
  description: "Create or update user information and traits in LogRocket to contextualize user behavior.",
  audience: "both",
  aiMetadata: { description: "Upserts a LogRocket user record keyed by user ID, attaching identity fields (name, email) and arbitrary custom traits so sessions can be attributed and filtered. Use when enriching who a user is before or alongside session tracking. At least one of name, email, timestamp, or traits must be provided. Idempotent: it overwrites the same user record via PUT, so repeating with identical input yields the same state.", idempotent: !0 },
  props: {
    orgId: import_pieces_framework3.Property.ShortText({
      displayName: "Organization ID",
      description: "Your LogRocket organization ID (found in Settings > General Settings, or in your App ID before the slash)",
      required: !0
    }),
    appId: import_pieces_framework3.Property.ShortText({
      displayName: "App ID",
      description: "Your LogRocket app/project ID (found in Settings > General Settings, or in your App ID after the slash)",
      required: !0
    }),
    userId: import_pieces_framework3.Property.ShortText({
      displayName: "User ID",
      description: "The unique identifier for the user",
      required: !0
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Name",
      description: "The user's name (max 1024 characters)",
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "The user's email address (max 1024 characters)",
      required: !1
    }),
    timestamp: import_pieces_framework3.Property.DateTime({
      displayName: "Timestamp",
      description: "Unix timestamp in milliseconds indicating when this data was collected (optional)",
      required: !1
    }),
    traits: import_pieces_framework3.Property.Object({
      displayName: "Traits",
      description: 'User traits as key-value pairs (e.g., {"plan": "free", "age": 43}). Values will be converted to strings.',
      required: !1
    })
  },
  async run(context) {
    let { orgId, appId, userId, name, email, timestamp, traits } = context.propsValue, apiKey = context.auth, body = {};
    if (name && (body.name = name), email && (body.email = email), timestamp && (body.timestamp = new Date(timestamp).getTime()), traits && (body.traits = traits), Object.keys(body).length === 0)
      throw new Error("At least one of name, email, timestamp, or traits must be provided");
    try {
      return (await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.PUT,
        url: `https://api.logrocket.com/v1/orgs/${orgId}/apps/${appId}/users/${userId}`,
        headers: {
          Authorization: `token ${apiKey}`,
          "Content-Type": "application/json"
        },
        body
      })).body;
    } catch (error) {
      throw new Error(
        `Failed to identify user: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/logrocket/src/lib/triggers/highlights-ready.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var highlightsReady = (0, import_pieces_framework4.createTrigger)({
  auth: logrocketAuth,
  name: "highlightsReady",
  displayName: "Highlights Ready",
  description: "Trigger when session highlights are ready. Use this webhook URL when requesting highlights.",
  aiMetadata: {
    description: 'Fires when LogRocket finishes generating AI session highlights and POSTs the results back to this webhook. Its URL must be supplied as the Webhook URL of a preceding "Request Highlights" action; the event carries the completed highlights and the matching sessions.'
  },
  props: {
    markdown: import_pieces_framework4.Property.MarkDown({
      value: `
## Webhook Configuration

This trigger receives highlights results from LogRocket when they're ready.

**To use this trigger:**

1. Copy the webhook URL below
2. Use it as the **Webhook URL** field when using the "Request Highlights" action
3. When highlights are ready, this trigger will fire with the results

**Webhook URL:**
\`\`\`text
{{webhookUrl}}
\`\`\`
      `
    })
  },
  sampleData: {
    result: {
      highlights: "The user [checks out their cart](https://app.logrocket.com/orgID/appID/s/5-14de95d6-d5b8-1a62-0a3a-4858caf874b0/0?t=1715564734801) and [encounters an error loading settings](https://app.logrocket.com/orgID/appID/s/5-3cde95d6-a5b8-4a62-9b3a-6aa834f8747c/0?t=1715564554783).",
      sessions: [
        {
          recordingID: "5-3cde95d6-a5b8-4a62-9b3a-6aa834f8747c",
          sessionID: 0,
          highlights: "The user [encounters an error loading settings](https://app.logrocket.com/orgID/appID/s/5-3cde95d6-a5b8-4a62-9b3a-6aa834f8747c/0?t=1715564554783) and [the issue persists](https://app.logrocket.com/orgID/appID/s/5-3cde95d6-a5b8-4a62-9b3a-6aa834f8747c/0?t=1715564734801)."
        },
        {
          recordingID: "5-14de95d6-d5b8-1a62-0a3a-4858caf874b0",
          sessionID: 0,
          highlights: "The user [adds an item to their cart](https://app.logrocket.com/orgID/appID/s/5-14de95d6-d5b8-1a62-0a3a-4858caf874b0/0?t=1715564554783) and [proceeds to checkout](https://app.logrocket.com/orgID/appID/s/5-14de95d6-d5b8-1a62-0a3a-4858caf874b0/0?t=1715564734801)"
        }
      ]
    },
    status: "READY",
    appID: "orgID/appID",
    requestID: "0cc12cad4b5b8b93760edf7fb5731ac66fbc8a766f9431df6c1e72b214ed6a65"
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/logrocket/src/index.ts
var logrocket = (0, import_pieces_framework5.createPiece)({
  displayName: "LogRocket",
  description: "Get AI-generated summaries of user sessions to understand customer behavior and troubleshoot issues faster.",
  auth: logrocketAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/logrocket.png",
  categories: [import_pieces_framework6.PieceCategory.DEVELOPER_TOOLS],
  authors: ["onyedikachi-david"],
  actions: [requestHighlights, identifyUser],
  triggers: [highlightsReady]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  logrocket
});
