"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/pendo/src/index.ts
var index_exports = {};
__export(index_exports, {
  pendo: () => pendo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/pendo/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), pendoAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Integration Key",
  description: "Your Pendo integration key. Found in **Settings \u2192 Integrations \u2192 Integration Keys**.",
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b;
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://app.pendo.io/api/v1/feature",
        headers: {
          "x-pendo-integration-key": auth,
          "Content-Type": "application/json"
        },
        queryParams: {
          length: "1"
        }
      }), { valid: !0 };
    } catch (e) {
      let err = e;
      return ((_a = err.response) == null ? void 0 : _a.status) === 401 || ((_b = err.response) == null ? void 0 : _b.status) === 403 ? {
        valid: !1,
        error: "Invalid integration key. Please check your Pendo settings."
      } : {
        valid: !1,
        error: `Connection failed: ${String(e).slice(0, 100)}`
      };
    }
  }
});

// ../ap/packages/pieces/community/pendo/src/lib/actions/track-event.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var trackEvent = (0, import_pieces_framework2.createAction)({
  auth: pendoAuth,
  name: "track_event",
  displayName: "Track Event",
  description: "Send a track event to Pendo.",
  audience: "both",
  aiMetadata: { description: "Sends a custom track event to Pendo for a given visitor (and optionally an account), recording product-usage activity for analytics. Choose this to log a behavioral/usage event; requires an event type/name and a visitor ID, with optional account ID, millisecond timestamp (defaults to now), and arbitrary event properties. Not idempotent \u2014 each call appends a new event.", idempotent: !1 },
  props: {
    type: import_pieces_framework2.Property.ShortText({
      displayName: "Event Type",
      description: "The type/name of the track event.",
      required: !0
    }),
    visitorId: import_pieces_framework2.Property.ShortText({
      displayName: "Visitor ID",
      description: "The unique identifier for the visitor.",
      required: !0
    }),
    accountId: import_pieces_framework2.Property.ShortText({
      displayName: "Account ID",
      description: "The account identifier the visitor belongs to.",
      required: !1
    }),
    timestamp: import_pieces_framework2.Property.Number({
      displayName: "Timestamp (ms)",
      description: "Unix timestamp in milliseconds. Defaults to the current time if not provided.",
      required: !1
    }),
    properties: import_pieces_framework2.Property.Object({
      displayName: "Event Properties",
      description: "Additional key-value properties for the event.",
      required: !1
    })
  },
  async run(context) {
    let { type, visitorId, accountId, timestamp, properties } = context.propsValue, body = __spreadProps(__spreadValues({
      type: "track",
      event: type,
      visitorId
    }, accountId ? { accountId } : {}), {
      timestamp: timestamp != null ? timestamp : Date.now(),
      properties: properties != null ? properties : {}
    });
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://app.pendo.io/data/track",
      headers: {
        "x-pendo-integration-key": context.auth.secret_text,
        "Content-Type": "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/pendo/src/lib/actions/get-visitor.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getVisitor = (0, import_pieces_framework3.createAction)({
  auth: pendoAuth,
  name: "get_visitor",
  displayName: "Get Visitor Details",
  description: "Retrieve details of a visitor by their ID.",
  audience: "both",
  aiMetadata: { description: "Looks up a single Pendo visitor by their unique visitor ID and returns that visitor record. Use when you have a specific visitor identifier and need their metadata/attributes. Read-only and idempotent.", idempotent: !0 },
  props: {
    visitorId: import_pieces_framework3.Property.ShortText({
      displayName: "Visitor ID",
      description: "The unique identifier for the visitor.",
      required: !0
    })
  },
  async run(context) {
    let { visitorId } = context.propsValue;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `https://app.pendo.io/api/v1/visitor/${encodeURIComponent(
        visitorId
      )}`,
      headers: {
        "x-pendo-integration-key": context.auth.secret_text,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/pendo/src/lib/actions/get-account.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getAccount = (0, import_pieces_framework4.createAction)({
  auth: pendoAuth,
  name: "get_account",
  displayName: "Get Account Details",
  description: "Retrieve details of an account by its ID.",
  audience: "both",
  aiMetadata: { description: "Looks up a single Pendo account by its unique account ID and returns that account record. Use when you have a specific account identifier and need its metadata/attributes. Read-only and idempotent.", idempotent: !0 },
  props: {
    accountId: import_pieces_framework4.Property.ShortText({
      displayName: "Account ID",
      description: "The unique identifier for the account.",
      required: !0
    })
  },
  async run(context) {
    let { accountId } = context.propsValue;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `https://app.pendo.io/api/v1/account/${encodeURIComponent(
        accountId
      )}`,
      headers: {
        "x-pendo-integration-key": context.auth.secret_text,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/pendo/src/lib/actions/list-guides.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var listGuides = (0, import_pieces_framework5.createAction)({
  auth: pendoAuth,
  name: "list_guides",
  displayName: "List Guides",
  description: "Retrieve all guides from your Pendo account.",
  audience: "both",
  aiMetadata: { description: "Lists all in-app guides configured in the authenticated Pendo account. Use to enumerate guides or to discover a guide ID for use in other steps. Takes no input. Read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: "https://app.pendo.io/api/v1/guide",
      headers: {
        "x-pendo-integration-key": context.auth.secret_text,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/pendo/src/index.ts
var pendo = (0, import_pieces_framework6.createPiece)({
  displayName: "Pendo",
  description: "Product analytics and digital adoption platform",
  auth: pendoAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/pendo.png",
  categories: [import_pieces_framework7.PieceCategory.BUSINESS_INTELLIGENCE],
  authors: ["Harmatta"],
  actions: [
    trackEvent,
    getVisitor,
    getAccount,
    listGuides,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: () => "https://app.pendo.io/api/v1",
      auth: pendoAuth,
      authMapping: async (auth) => ({
        "x-pendo-integration-key": String(auth)
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pendo
});
