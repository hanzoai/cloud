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

// ../ap/packages/pieces/community/dittofeed/src/index.ts
var index_exports = {};
__export(index_exports, {
  dittofeed: () => dittofeed,
  dittofeedAuth: () => dittofeedAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/dittofeed/src/lib/actions/identify.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var identifyAction = (0, import_pieces_framework.createAction)({
  name: "identify",
  auth: dittofeedAuth,
  displayName: "Identify User",
  description: "Identify a user in Dittofeed.",
  audience: "both",
  aiMetadata: { description: "Sends an identify event to Dittofeed that links a userId to a set of traits (e.g. email, name, attributes), creating or updating that user profile in the customer data platform. Use to register a new user or attach/refresh trait data before tracking activity. Requires a userId; each call posts a new event with a unique messageId, so it is not idempotent.", idempotent: !1 },
  props: {
    userId: import_pieces_framework.Property.ShortText({
      displayName: "User ID",
      required: !0
    }),
    traits: import_pieces_framework.Property.Object({
      displayName: "User Traits",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let { userId, traits } = context.propsValue, { apiKey, baseUrl } = context.auth.props;
    try {
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: `${baseUrl}/api/public/apps/identify`,
        headers: {
          Authorization: apiKey
        },
        body: {
          type: "identify",
          messageId: `identify-${userId}-${Date.now()}`,
          userId,
          traits
        }
      })).body;
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error("Authentication failed. Please check your API key.") : ((_b = error.response) == null ? void 0 : _b.status) === 404 ? new Error(`Dittofeed API endpoint not found. Please check your base URL: ${baseUrl}`) : ((_c = error.response) == null ? void 0 : _c.status) === 429 ? new Error("Rate limit exceeded. Please try again later.") : ((_d = error.response) == null ? void 0 : _d.status) >= 500 ? new Error(`Dittofeed server error: ${((_f = (_e = error.response) == null ? void 0 : _e.body) == null ? void 0 : _f.message) || error.message}`) : new Error(`Failed to identify user in Dittofeed: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/dittofeed/src/lib/actions/track.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var trackAction = (0, import_pieces_framework2.createAction)({
  name: "track",
  auth: dittofeedAuth,
  displayName: "Track Event",
  description: "Track an event for a user.",
  audience: "both",
  aiMetadata: { description: 'Sends a track event to Dittofeed recording a named action a user performed (e.g. "Purchase Completed", "Signup"), optionally with event properties. Use to log user behavior that can drive segmentation and messaging campaigns. Requires a userId and event name; each call appends a new event with a unique messageId, so it is not idempotent.', idempotent: !1 },
  props: {
    userId: import_pieces_framework2.Property.ShortText({
      displayName: "User ID",
      required: !0
    }),
    event: import_pieces_framework2.Property.ShortText({
      displayName: "Event Name",
      required: !0
    }),
    properties: import_pieces_framework2.Property.Object({
      displayName: "Event Properties",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let { userId, event, properties } = context.propsValue, { apiKey, baseUrl } = context.auth.props;
    try {
      return (await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.POST,
        url: `${baseUrl}/api/public/apps/track`,
        headers: {
          Authorization: apiKey
        },
        body: {
          type: "track",
          messageId: `track-${userId}-${event}-${Date.now()}`,
          userId,
          event,
          properties
        }
      })).body;
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error("Authentication failed. Please check your API key.") : ((_b = error.response) == null ? void 0 : _b.status) === 404 ? new Error(`Dittofeed API endpoint not found. Please check your base URL: ${baseUrl}`) : ((_c = error.response) == null ? void 0 : _c.status) === 429 ? new Error("Rate limit exceeded. Please try again later.") : ((_d = error.response) == null ? void 0 : _d.status) >= 500 ? new Error(`Dittofeed server error: ${((_f = (_e = error.response) == null ? void 0 : _e.body) == null ? void 0 : _f.message) || error.message}`) : new Error(`Failed to track event in Dittofeed: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/dittofeed/src/lib/actions/screen.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var screenAction = (0, import_pieces_framework3.createAction)({
  name: "screen",
  auth: dittofeedAuth,
  displayName: "Screen Event",
  description: "Track a screen view event.",
  audience: "both",
  aiMetadata: { description: "Sends a screen event to Dittofeed recording that a user viewed a named screen or page, optionally with properties. Use to capture in-app screen-view activity for a user (the screen-specific counterpart to a track event). Requires a userId and screen name; each call appends a new event with a unique messageId, so it is not idempotent.", idempotent: !1 },
  props: {
    userId: import_pieces_framework3.Property.ShortText({
      displayName: "User ID",
      required: !0
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Screen Name",
      required: !0
    }),
    properties: import_pieces_framework3.Property.Object({
      displayName: "Screen Properties",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let { userId, name, properties } = context.propsValue, { apiKey, baseUrl } = context.auth.props;
    try {
      return (await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.POST,
        url: `${baseUrl}/api/public/apps/screen`,
        headers: {
          Authorization: apiKey
        },
        body: {
          type: "screen",
          messageId: `screen-${userId}-${name}-${Date.now()}`,
          userId,
          name,
          properties
        }
      })).body;
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error("Authentication failed. Please check your API key.") : ((_b = error.response) == null ? void 0 : _b.status) === 404 ? new Error(`Dittofeed API endpoint not found. Please check your base URL: ${baseUrl}`) : ((_c = error.response) == null ? void 0 : _c.status) === 429 ? new Error("Rate limit exceeded. Please try again later.") : ((_d = error.response) == null ? void 0 : _d.status) >= 500 ? new Error(`Dittofeed server error: ${((_f = (_e = error.response) == null ? void 0 : _e.body) == null ? void 0 : _f.message) || error.message}`) : new Error(`Failed to track screen view in Dittofeed: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/dittofeed/src/index.ts
var dittofeedAuth = import_pieces_framework4.PieceAuth.CustomAuth({
  props: {
    apiKey: import_pieces_framework4.PieceAuth.SecretText({
      displayName: "API Key",
      required: !0,
      description: "Your API key of Dittofeed."
    }),
    baseUrl: import_pieces_framework4.Property.ShortText({
      displayName: "Base URL",
      required: !0,
      description: "The base URL of your Dittofeed instance.",
      defaultValue: "http://localhost:3200"
    })
  },
  required: !0
}), dittofeed = (0, import_pieces_framework4.createPiece)({
  displayName: "Dittofeed",
  auth: dittofeedAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/dittofeed.png",
  authors: [
    "SmarterService"
  ],
  categories: [
    import_pieces_framework5.PieceCategory.MARKETING,
    import_pieces_framework5.PieceCategory.BUSINESS_INTELLIGENCE
  ],
  description: "Customer data platform for user analytics and tracking",
  actions: [identifyAction, trackAction, screenAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  dittofeed,
  dittofeedAuth
});
