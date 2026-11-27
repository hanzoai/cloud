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

// ../ap/packages/pieces/community/chatwoot/src/index.ts
var index_exports = {};
__export(index_exports, {
  chatwoot: () => chatwoot
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chatwoot/src/lib/actions/send-message.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/chatwoot/src/lib/common/constants.ts
function chatwootEndpoints(baseUrl, accountId) {
  let base = (baseUrl != null ? baseUrl : "").replace(/\/+$/, "");
  return {
    WEBHOOKS: `${base}/api/v1/accounts/${accountId}/webhooks`,
    WEBHOOK_BY_ID: (webhookId) => `${base}/api/v1/accounts/${accountId}/webhooks/${webhookId}`,
    CONVERSATION_MESSAGES: (conversationId) => `${base}/api/v1/accounts/${accountId}/conversations/${conversationId}/messages`,
    PROFILE: `${base}/auth/sign_in`
  };
}
var CHATWOOT_AUTH_HEADER = "api_access_token";

// ../ap/packages/pieces/community/chatwoot/src/lib/common/types.ts
function getChatwootAuth(auth) {
  var _a, _b, _c;
  return {
    baseUrl: (_a = auth.props.baseUrl) != null ? _a : "",
    accountId: (_b = auth.props.accountId) != null ? _b : 0,
    apiAccessToken: (_c = auth.props.apiAccessToken) != null ? _c : ""
  };
}

// ../ap/packages/pieces/community/chatwoot/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), authDesc = `
To connect your Chatwoot account:

**Chatwoot URL:**
- For Chatwoot Cloud, use: \`https://app.chatwoot.com\`
- For self-hosted instances, use your server URL (e.g. \`https://chatwoot.yourcompany.com\`)

**API Access Token:**
1. Log in to your Chatwoot dashboard.
2. Click your avatar in the bottom-left corner and go to **Profile Settings**.
3. Scroll down to the **Access Token** section.
4. Copy your access token.

**Account ID:**
1. In your Chatwoot dashboard, the Account ID is visible in the URL: \`/app/accounts/{ACCOUNT_ID}/...\`
2. It is a numeric value (e.g. 1, 2, etc.)

`, chatwootAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "Chatwoot Connection",
  required: !0,
  description: authDesc,
  props: {
    baseUrl: import_pieces_framework.Property.ShortText({
      displayName: "Chatwoot URL",
      description: "Your Chatwoot instance URL (e.g. https://app.chatwoot.com)",
      required: !0,
      defaultValue: "https://app.chatwoot.com"
    }),
    apiAccessToken: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Access Token",
      description: "Your user API access token from Profile Settings",
      required: !0
    }),
    accountId: import_pieces_framework.Property.Number({
      displayName: "Account ID",
      description: "Your numeric account ID (visible in the dashboard URL)",
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/chatwoot/src/lib/actions/send-message.ts
var MAX_RETRIES = 3, INITIAL_BACKOFF_MS = 1e3;
async function sendWithRetry(baseUrl, accountId, conversationId, token, body) {
  var _a;
  let url = chatwootEndpoints(baseUrl, accountId).CONVERSATION_MESSAGES(conversationId), lastError;
  for (let attempt = 0; attempt < MAX_RETRIES; attempt++)
    try {
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url,
        headers: {
          [CHATWOOT_AUTH_HEADER]: token,
          "Content-Type": "application/json"
        },
        body
      })).body;
    } catch (error) {
      lastError = error instanceof Error ? error : new Error(String(error));
      let statusCode = (_a = error.response) == null ? void 0 : _a.status;
      if (!(statusCode === 429 || statusCode !== void 0 && statusCode >= 500) || attempt === MAX_RETRIES - 1)
        throw lastError;
      let backoffMs = INITIAL_BACKOFF_MS * Math.pow(2, attempt);
      await new Promise((resolve) => setTimeout(resolve, backoffMs));
    }
  throw lastError != null ? lastError : new Error("Send message failed after retries");
}
var sendMessage = (0, import_pieces_framework2.createAction)({
  auth: chatwootAuth,
  name: "send_message",
  classification: "WRITE",
  displayName: "Send Message",
  description: "Send a message reply to a Chatwoot conversation",
  audience: "both",
  aiMetadata: {
    description: "Posts a new message to an existing Chatwoot conversation, identified by its numeric conversation display ID. Use to reply to a customer or, by enabling the private-note option, to leave an internal note visible only to agents rather than to the contact. Not idempotent \u2014 each call appends another message.",
    idempotent: !1
  },
  props: {
    conversationId: import_pieces_framework2.Property.Number({
      displayName: "Conversation ID",
      description: "The numeric conversation display ID. Available from the trigger output.",
      required: !0
    }),
    message: import_pieces_framework2.Property.LongText({
      displayName: "Message",
      description: "The text message to send",
      required: !0
    }),
    isPrivate: import_pieces_framework2.Property.Checkbox({
      displayName: "Private Note",
      description: "If checked, sends as a private note visible only to agents (not the contact)",
      required: !0,
      defaultValue: !1
    })
  },
  async run(context) {
    let { conversationId, message, isPrivate } = context.propsValue, authValue = getChatwootAuth(context.auth), body = {
      content: message,
      message_type: "outgoing",
      private: isPrivate,
      content_type: "text"
    }, result = await sendWithRetry(
      authValue.baseUrl,
      authValue.accountId,
      conversationId,
      authValue.apiAccessToken,
      body
    );
    return {
      success: !0,
      messageId: result.id,
      conversationId: result.conversation_id,
      status: result.status
    };
  }
});

// ../ap/packages/pieces/community/chatwoot/src/lib/triggers/new-message.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var WEBHOOK_STORE_KEY = "_chatwoot_webhook_id";
function normalizeUrl(u) {
  return u == null || typeof u != "string" ? "" : u.trim().replace(/\/+$/, "");
}
function toWebhookList(raw) {
  if (Array.isArray(raw)) return raw;
  if (raw && typeof raw == "object") {
    let o = raw;
    for (let key of ["payload", "data", "webhooks"]) {
      let v = o[key];
      if (Array.isArray(v)) return v;
    }
    let payload = o.payload;
    if (payload && typeof payload == "object") {
      let w = payload.webhooks;
      if (Array.isArray(w)) return w;
    }
    let data = o.data;
    if (data && typeof data == "object") {
      let w = data.webhooks;
      if (Array.isArray(w)) return w;
    }
  }
  return [];
}
var newMessage = (0, import_pieces_framework3.createTrigger)({
  auth: chatwootAuth,
  name: "new_message",
  classification: "READ",
  displayName: "New Incoming Message",
  description: "Triggers when a contact sends a new message in a Chatwoot conversation",
  aiMetadata: {
    description: "Fires when a contact sends a new inbound message in a Chatwoot conversation. Only incoming, non-empty messages are emitted (outgoing agent replies and private notes are filtered out), representing a fresh customer message awaiting a response."
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    messageId: 42,
    content: "Hello, I need help with my order",
    messageType: "incoming",
    conversationId: 7,
    contactId: 15,
    contactName: "Jane Doe",
    accountId: 1,
    createdAt: "2024-06-15 10:30:00 UTC"
  },
  async onEnable(context) {
    var _a, _b, _c;
    let authValue = getChatwootAuth(context.auth), endpoints = chatwootEndpoints(
      authValue.baseUrl,
      authValue.accountId
    ), webhookUrl = context.webhookUrl, webhookId;
    try {
      let listRes = await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: endpoints.WEBHOOKS,
        headers: {
          [CHATWOOT_AUTH_HEADER]: authValue.apiAccessToken
        }
      }), list = toWebhookList(listRes.body), targetUrl = normalizeUrl(webhookUrl), existing = list.find((w) => normalizeUrl(w.url) === targetUrl);
      existing ? webhookId = existing.id : webhookId = (await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.POST,
        url: endpoints.WEBHOOKS,
        headers: {
          [CHATWOOT_AUTH_HEADER]: authValue.apiAccessToken,
          "Content-Type": "application/json"
        },
        body: {
          url: webhookUrl,
          subscriptions: ["message_created"]
        }
      })).body.payload.webhook.id;
    } catch (err) {
      let status = (_a = err == null ? void 0 : err.response) == null ? void 0 : _a.status, body = (_b = err == null ? void 0 : err.response) == null ? void 0 : _b.body;
      if (status === 422 && body && typeof body == "object" && "message" in body && ((_c = body.message) != null && _c.includes("already been taken"))) {
        let listRes = await import_pieces_common2.httpClient.sendRequest({
          method: import_pieces_common2.HttpMethod.GET,
          url: endpoints.WEBHOOKS,
          headers: { [CHATWOOT_AUTH_HEADER]: authValue.apiAccessToken }
        }), list = toWebhookList(listRes.body), targetUrl = normalizeUrl(webhookUrl), existing = list.find((w) => normalizeUrl(w.url) === targetUrl);
        if (existing != null && existing.id)
          webhookId = existing.id;
        else
          throw err;
      } else
        throw err;
    }
    await context.store.put(WEBHOOK_STORE_KEY, { webhookId });
  },
  async onDisable(context) {
    let authValue = getChatwootAuth(context.auth), endpoints = chatwootEndpoints(
      authValue.baseUrl,
      authValue.accountId
    ), stored = await context.store.get(
      WEBHOOK_STORE_KEY
    );
    if (stored != null && stored.webhookId) {
      try {
        await import_pieces_common2.httpClient.sendRequest({
          method: import_pieces_common2.HttpMethod.DELETE,
          url: endpoints.WEBHOOK_BY_ID(stored.webhookId),
          headers: {
            [CHATWOOT_AUTH_HEADER]: authValue.apiAccessToken
          }
        });
      } catch (error) {
        console.error("[Chatwoot] Error deleting webhook:", error);
      }
      await context.store.delete(WEBHOOK_STORE_KEY);
    }
  },
  async run(context) {
    var _a;
    let rawBody = (_a = context.payload) == null ? void 0 : _a.body, payload = rawBody && typeof rawBody == "object" && "payload" in rawBody ? rawBody.payload : rawBody;
    if (!payload || typeof payload != "object")
      return [];
    if (payload.event !== "message_created")
      return [];
    if (payload.message_type !== "incoming")
      return [];
    let content = payload.content;
    if (content == null || String(content).trim().length === 0)
      return [];
    let conversation = payload.conversation, contact = payload.contact, sender = payload.sender, account = payload.account, conversationId = 0;
    if (conversation != null && typeof conversation == "object") {
      if ("display_id" in conversation) {
        let displayId = conversation.display_id;
        conversationId = displayId != null && Number(displayId) || 0;
      } else if ("id" in conversation) {
        let id = conversation.id;
        conversationId = id != null && Number(id) || 0;
      }
    }
    let contactObj = contact || sender, contactId = contactObj != null && typeof contactObj == "object" && "id" in contactObj && Number(contactObj.id) || 0, contactName = contactObj != null && typeof contactObj == "object" && "name" in contactObj ? String(contactObj.name) : "Unknown", accountId = account != null && typeof account == "object" && "id" in account && Number(account.id) || 0, messageId = typeof payload.id == "number" ? payload.id : Number(payload.id) || 0, createdAt = payload.created_at != null ? String(payload.created_at) : "";
    return [{
      messageId,
      content: String(content).trim(),
      messageType: "incoming",
      conversationId,
      contactId,
      contactName,
      accountId,
      createdAt
    }];
  }
});

// ../ap/packages/pieces/community/chatwoot/src/index.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
var chatwoot = (0, import_pieces_framework4.createPiece)({
  displayName: "Chatwoot",
  description: "Receive and reply to customer messages with Chatwoot",
  auth: chatwootAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/chatwoot.png",
  authors: ["AhmadTash"],
  actions: [
    sendMessage,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: chatwootAuth,
      baseUrl: (auth) => getChatwootAuth(auth).baseUrl,
      authMapping: async (auth) => {
        let authValue = getChatwootAuth(auth);
        return {
          [CHATWOOT_AUTH_HEADER]: authValue.apiAccessToken
        };
      }
    })
  ],
  triggers: [newMessage]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  chatwoot
});
