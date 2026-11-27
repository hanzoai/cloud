"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/digital-pilot/src/index.ts
var index_exports = {};
__export(index_exports, {
  digitalPilot: () => digitalPilot
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/digital-pilot/src/lib/actions/add-target-account.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/digital-pilot/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), digitalPilotAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Enter your DigitalPilot API key",
  required: !0
});

// ../ap/packages/pieces/community/digital-pilot/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var BASE_URL = "https://api.digitalpilot.app/v1", DigitalPilotClientImpl = class {
  constructor(apiKey) {
    __publicField(this, "apiKey", apiKey);
  }
  async addTargetAccount(tagId, listId, domain) {
    let request = {
      method: import_pieces_common.HttpMethod.POST,
      url: `${BASE_URL}/tags/${tagId}/target-accounts/${listId}`,
      headers: {
        "Content-Type": "application/json"
      },
      body: {
        domain
      },
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.apiKey
      }
    }, response = await import_pieces_common.httpClient.sendRequest(request);
    if (response.status === 200)
      return;
    let errorMessage = "Unknown error occurred";
    switch (response.status) {
      case 400:
        errorMessage = "Invalid input. Please check your parameters.";
        break;
      case 401:
        errorMessage = "Authentication failed. Please check your API key.";
        break;
      case 404:
        errorMessage = "Tag or list not found. Please verify your selection.";
        break;
      case 429:
        errorMessage = "Rate limit exceeded. Please try again later.";
        break;
      case 500:
        errorMessage = "DigitalPilot server error. Please try again later.";
        break;
    }
    throw new Error(errorMessage);
  }
  async removeTargetAccount(tagId, listId, domain) {
    let request = {
      method: import_pieces_common.HttpMethod.DELETE,
      url: `${BASE_URL}/tags/${tagId}/target-accounts/${listId}`,
      headers: {
        "Content-Type": "application/json"
      },
      body: {
        domain
      },
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.apiKey
      }
    }, response = await import_pieces_common.httpClient.sendRequest(request);
    if (response.status === 200)
      return;
    let errorMessage = "Unknown error occurred";
    switch (response.status) {
      case 400:
        errorMessage = "Invalid input. Please check your parameters.";
        break;
      case 401:
        errorMessage = "Authentication failed. Please check your API key.";
        break;
      case 404:
        errorMessage = "Tag or list not found. Please verify your selection.";
        break;
      case 429:
        errorMessage = "Rate limit exceeded. Please try again later.";
        break;
      case 500:
        errorMessage = "DigitalPilot server error. Please try again later.";
        break;
    }
    throw new Error(errorMessage);
  }
  async createWebhook(tagId, webhookURL, subscriptions) {
    let request = {
      method: import_pieces_common.HttpMethod.POST,
      url: `${BASE_URL}/tags/${tagId}/webhooks`,
      headers: {
        "Content-Type": "application/json"
      },
      body: {
        webhookURL,
        subscriptions
      },
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.apiKey
      }
    }, response = await import_pieces_common.httpClient.sendRequest(request);
    if (response.status === 200)
      return response.body;
    let errorMessage = "Failed to register webhook";
    switch (response.status) {
      case 400:
        errorMessage = "Invalid webhook parameters. Please check your inputs.";
        break;
      case 401:
        errorMessage = "Authentication failed. Please check your API key.";
        break;
      case 404:
        errorMessage = "Tag not found. Please verify your selection.";
        break;
      case 429:
        errorMessage = "Rate limit exceeded. Please try again later.";
        break;
      case 500:
        errorMessage = "DigitalPilot server error. Please try again later.";
        break;
    }
    throw new Error(errorMessage);
  }
  async deleteWebhook(tagId, webhookURL, webhookID) {
    let body = {};
    webhookURL && (body.webhookURL = webhookURL), webhookID && (body.webhookID = webhookID);
    let request = {
      method: import_pieces_common.HttpMethod.DELETE,
      url: `${BASE_URL}/tags/${tagId}/webhooks`,
      headers: {
        "Content-Type": "application/json"
      },
      body,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.apiKey
      }
    }, response = await import_pieces_common.httpClient.sendRequest(request);
    if (response.status === 200)
      return;
    let errorMessage = "Failed to delete webhook";
    switch (response.status) {
      case 400:
        errorMessage = "Invalid webhook parameters.";
        break;
      case 401:
        errorMessage = "Authentication failed. Please check your API key.";
        break;
      case 404:
        errorMessage = "Webhook not found.";
        break;
      case 429:
        errorMessage = "Rate limit exceeded.";
        break;
      case 500:
        errorMessage = "DigitalPilot server error.";
        break;
    }
    console.warn(`Webhook deletion warning: ${errorMessage}`);
  }
};
function makeClient(auth) {
  return new DigitalPilotClientImpl(auth);
}

// ../ap/packages/pieces/community/digital-pilot/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), tagIdProp = import_pieces_framework2.Property.ShortText({
  displayName: "Tag ID",
  description: "The ID of the tag",
  required: !0
}), listIdProp = import_pieces_framework2.Property.ShortText({
  displayName: "List ID",
  description: "The ID of the list",
  required: !0
});

// ../ap/packages/pieces/community/digital-pilot/src/lib/actions/add-target-account.ts
var addTargetAccountAction = (0, import_pieces_framework3.createAction)({
  auth: digitalPilotAuth,
  name: "add_target_account",
  displayName: "Add Target Account",
  description: "Add a target account to a list",
  audience: "both",
  aiMetadata: { description: "Adds a company domain as a target account to a specific DigitalPilot list (identified by tag and list IDs) so its website visits are tracked as high-priority. Use to build or grow an account-based marketing target list. Requires the tag ID, list ID, and the account domain (e.g. example.com); idempotent, since re-adding the same domain leaves the list membership unchanged.", idempotent: !0 },
  props: {
    tagId: tagIdProp,
    listId: listIdProp,
    domain: import_pieces_framework3.Property.ShortText({
      displayName: "Domain",
      description: "Account domain name (e.g., example.com)",
      required: !0
    })
  },
  async run(context) {
    return await makeClient(context.auth.secret_text).addTargetAccount(
      context.propsValue.tagId,
      context.propsValue.listId,
      context.propsValue.domain
    ), {
      success: !0,
      message: "Target account added successfully"
    };
  }
});

// ../ap/packages/pieces/community/digital-pilot/src/lib/actions/remove-target-account.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var removeTargetAccountAction = (0, import_pieces_framework4.createAction)({
  auth: digitalPilotAuth,
  name: "remove_target_account",
  displayName: "Remove Target Account",
  description: "Remove a target account from a list",
  audience: "both",
  aiMetadata: { description: "Removes a company domain from a specific DigitalPilot target-account list (identified by tag and list IDs), stopping it from being tracked as a high-priority account. Use to prune an account-based marketing target list. Requires the tag ID, list ID, and the account domain; idempotent, since removing an already-absent domain leaves the list membership unchanged.", idempotent: !0 },
  props: {
    tagId: tagIdProp,
    listId: listIdProp,
    domain: import_pieces_framework4.Property.ShortText({
      displayName: "Domain",
      description: "Account domain name (e.g., example.com)",
      required: !0
    })
  },
  async run(context) {
    return await makeClient(context.auth.secret_text).removeTargetAccount(
      context.propsValue.tagId,
      context.propsValue.listId,
      context.propsValue.domain
    ), {
      success: !0,
      message: "Target account removed successfully"
    };
  }
});

// ../ap/packages/pieces/community/digital-pilot/src/lib/triggers/new-high-intent-visit.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var newHighIntentVisitTrigger = (0, import_pieces_framework5.createTrigger)({
  auth: digitalPilotAuth,
  name: "new_high_intent_visit",
  displayName: "New High Intent Visit",
  description: "Triggers when a business exhibits high intent behavior on your website",
  aiMetadata: {
    description: "Fires when DigitalPilot detects high-intent browsing behavior (e.g. elevated intent score, repeated page views, extended time on site) from an identified business visitor for the configured tag. Each event represents one such visit and carries the visiting account domain and engagement signals."
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  props: {
    tagId: tagIdProp
  },
  sampleData: {
    domain: "example.com",
    timestamp: "2024-01-15T10:30:00Z",
    intent_score: 85,
    page_views: 12,
    time_on_site: 450
  },
  async onEnable(context) {
    let client = makeClient(context.auth.secret_text), webhookUrl = context.webhookUrl, webhook = await client.createWebhook(
      context.propsValue.tagId,
      webhookUrl,
      "high_intent"
    );
    await context.store.put("_webhook_id", webhook.id);
  },
  async onDisable(context) {
    let client = makeClient(context.auth.secret_text), webhookId = await context.store.get("_webhook_id");
    webhookId && await client.deleteWebhook(
      context.propsValue.tagId,
      void 0,
      webhookId
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/digital-pilot/src/lib/triggers/new-target-account-visit.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var newTargetAccountVisitTrigger = (0, import_pieces_framework6.createTrigger)({
  auth: digitalPilotAuth,
  name: "new_target_account_visit",
  displayName: "New Target Account Visit",
  description: "Triggers when a business you've identified as a high-priority target account browses your site",
  aiMetadata: {
    description: "Fires when a business already on one of your DigitalPilot target-account lists (for the configured tag) visits your website. Each event represents one such visit and carries the account domain and engagement signals (page views, time on site, pages visited). Use to react to activity from accounts you are specifically targeting, as opposed to any high-intent visitor."
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  props: {
    tagId: tagIdProp
  },
  sampleData: {
    domain: "acme-corp.com",
    timestamp: "2024-01-15T14:22:00Z",
    page_views: 8,
    time_on_site: 320,
    pages_visited: [
      "/products",
      "/pricing",
      "/contact"
    ]
  },
  async onEnable(context) {
    let client = makeClient(context.auth.secret_text), webhookUrl = context.webhookUrl, webhook = await client.createWebhook(
      context.propsValue.tagId,
      webhookUrl,
      "target_accounts"
    );
    await context.store.put("_webhook_id", webhook.id);
  },
  async onDisable(context) {
    let client = makeClient(context.auth.secret_text), webhookId = await context.store.get("_webhook_id");
    webhookId && await client.deleteWebhook(
      context.propsValue.tagId,
      void 0,
      webhookId
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/digital-pilot/src/index.ts
var digitalPilot = (0, import_pieces_framework7.createPiece)({
  displayName: "DigitalPilot",
  auth: digitalPilotAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/digital-pilot.png",
  authors: ["onyedikachi-david"],
  actions: [addTargetAccountAction, removeTargetAccountAction],
  triggers: [newHighIntentVisitTrigger, newTargetAccountVisitTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  digitalPilot
});
