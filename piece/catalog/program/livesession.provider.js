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

// ../ap/packages/pieces/community/livesession/src/index.ts
var index_exports = {};
__export(index_exports, {
  livesession: () => livesession
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/livesession/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), livesessionAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `r LiveSession API authentication.

**How to get your API Key:**
1. Go to [LiveSession settings](https://app.livesession.io/app/settings/api?tab=api_tokens)
2. Click **API Tokens** from the left menu
3. Click the creation button in the upper-right corner
4. Give your token a descriptive name
5. Select the website you want to access through the REST API
6. Select the scopes you'd like to grant this token e.g webhooks:read, webhooks:write, sessions:read, recordings:read etc.
7. Click confirm `,
  required: !0
});

// ../ap/packages/pieces/community/livesession/src/lib/triggers/session-event.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), sessionEvent = (0, import_pieces_framework2.createTrigger)({
  auth: livesessionAuth,
  name: "sessionEvent",
  displayName: "Session Event",
  description: "Triggered when a session event occurs in LiveSession",
  aiMetadata: {
    description: "Fires when LiveSession reports a session event for the specified website via webhook. Each event represents activity in a visitor recording session (identified by session_id and website_id, with a Unix timestamp), letting agents react in real time to user sessions captured by LiveSession."
  },
  props: {
    website_id: import_pieces_framework2.Property.ShortText({
      displayName: "Website ID",
      description: "The website ID to listen for session events",
      required: !0
    }),
    api_version: import_pieces_framework2.Property.ShortText({
      displayName: "API Version",
      description: "API version (e.g., v1.0)",
      required: !1,
      defaultValue: "v1.0"
    })
  },
  sampleData: {
    event: "session_event",
    session_id: "example-session-id",
    website_id: "example-website-id",
    timestamp: 1234567890
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let { website_id, api_version } = context.propsValue, apiKey = context.auth.secret_text, response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.livesession.io/v1/webhooks",
      headers: {
        Authorization: `Bearer ${apiKey}`,
        "Content-Type": "application/json",
        Accept: "application/json"
      },
      body: {
        url: context.webhookUrl,
        website_id,
        version: api_version
      }
    });
    if (response.body.webhook_id === void 0)
      throw new Error(`Failed to create webhook: ${response.body}`);
    await ((_a = context.store) == null ? void 0 : _a.put("webhook_id", response.body.webhook_id));
  },
  async onDisable(context) {
    var _a, _b;
    let apiKey = context.auth.secret_text, webhook_id = await ((_a = context.store) == null ? void 0 : _a.get("webhook_id"));
    webhook_id && (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.DELETE,
      url: `https://api.livesession.io/v1/webhooks/${webhook_id}`,
      headers: {
        Authorization: `Bearer ${apiKey}`,
        Accept: "application/json"
      }
    }), await ((_b = context.store) == null ? void 0 : _b.delete("webhook_id")));
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/livesession/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework"), livesession = (0, import_pieces_framework3.createPiece)({
  displayName: "LiveSession",
  auth: livesessionAuth,
  minimumSupportedRelease: "0.36.1",
  description: "LiveSession is the analytics platform that helps businesses scale up based on data.",
  categories: [import_pieces_framework4.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  logoUrl: "https://cdn.activepieces.com/pieces/livesession.png",
  authors: ["sanket-a11y"],
  actions: [
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: livesessionAuth,
      baseUrl: () => "https://api.livesession.io/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [sessionEvent]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  livesession
});
