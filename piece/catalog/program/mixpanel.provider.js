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

// ../ap/packages/pieces/community/mixpanel/src/index.ts
var index_exports = {};
__export(index_exports, {
  mixpanel: () => mixpanel
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mixpanel/src/lib/actions/track-event.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/mixpanel/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), mixpanelAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Mixpanel token",
  required: !0,
  description: `
      The Mixpanel token associated with your project. You can find your Mixpanel token in the project settings dialog in the Mixpanel app.
    `
});

// ../ap/packages/pieces/community/mixpanel/src/lib/actions/track-event.ts
var API_URL = "https://api.mixpanel.com", trackEvent = (0, import_pieces_framework2.createAction)({
  name: "track_event",
  auth: mixpanelAuth,
  displayName: "Track Event",
  description: "Send an Event to Mixpanel.",
  audience: "both",
  aiMetadata: {
    description: 'Record a product-analytics event in Mixpanel by name (e.g. "Payment Made"), optionally tying it to a user via a distinct ID and attaching arbitrary event properties as key-value pairs. Use to log user actions or behavior for analytics. Not idempotent: each call appends a new event stamped with the current time, so repeating it records duplicates.',
    idempotent: !1
  },
  props: {
    event: import_pieces_framework2.Property.ShortText({
      displayName: "Event",
      description: "A name for this Event. For example, 'Brand Mentioned in Tweet' or 'Payment Made'.",
      required: !0
    }),
    distinct_id: import_pieces_framework2.Property.ShortText({
      displayName: "Distinct ID (Profile ID)",
      description: "A way to uniquely identify your users (or more generally, profiles). If you are sending Profiles to Mixpanel in addition to events, this property value should be identical to the Distinct ID property attached to the Profile so that you can connect events to people records.",
      required: !1
    }),
    event_properties: import_pieces_framework2.Property.Object({
      displayName: "Event Properties",
      description: "Event Properties are bits of extra information that you send along with your Events describing the details of that action. They are usually specific to the Event they\u2019re describing and don\u2019t apply universally to other Events. Leveraging Event Properties allows you to conduct deeper analysis to better understand user behavior for a specific action. For example, a 'Song Added to Playlist' event could have 'Artist' and 'Playlist' as the properties. Properties are sent as key-value pairs where the key is the property name and the value is the property value.",
      required: !1
    })
  },
  async run(context) {
    let projectToken = context.auth, { event, distinct_id, event_properties } = context.propsValue, eventPayload = [
      {
        event,
        properties: __spreadValues({
          time: Math.floor(Date.now() / 1e3),
          distinct_id
        }, event_properties)
      }
    ];
    return await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${API_URL}/import`,
      body: eventPayload,
      headers: {
        "Content-Type": "application/json"
      },
      authentication: {
        type: import_pieces_common.AuthenticationType.BASIC,
        username: projectToken.secret_text,
        password: ""
      }
    });
  }
});

// ../ap/packages/pieces/community/mixpanel/src/index.ts
var mixpanel = (0, import_pieces_framework3.createPiece)({
  displayName: "Mixpanel",
  description: "Simple and powerful product analytics that helps everyone make better decisions",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/mixpanel.png",
  authors: ["yann120", "kishanprmr", "MoShizzle", "abuaboud"],
  auth: mixpanelAuth,
  categories: [import_pieces_framework4.PieceCategory.BUSINESS_INTELLIGENCE],
  actions: [
    trackEvent,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://api.mixpanel.com",
      auth: mixpanelAuth,
      authMapping: async (auth) => ({
        Authorization: `Basic ${Buffer.from(auth.secret_text).toString(
          "base64"
        )}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mixpanel
});
