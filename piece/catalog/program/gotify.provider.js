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

// ../ap/packages/pieces/community/gotify/src/index.ts
var index_exports = {};
__export(index_exports, {
  gotify: () => gotify
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/gotify/src/lib/actions/send-notification.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/gotify/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), gotifyAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `
    To obtain a token:

    1. Log in to your Gotify instance.
    2. Click on Apps
    3. Select the Eye icon in the same row as your App to copy your token, or CREATE APPLICATION if you do not have one app yet.
    4. Copy your access token & and paste them into the fields below.
    `,
  props: {
    base_url: import_pieces_framework.Property.ShortText({
      displayName: "Server URL",
      description: "Gotify Instance URL",
      required: !0
    }),
    app_token: import_pieces_framework.PieceAuth.SecretText({
      displayName: "App Token",
      description: "Gotify App Token",
      required: !0
    })
  },
  required: !0
});

// ../ap/packages/pieces/community/gotify/src/lib/actions/send-notification.ts
var sendNotification = (0, import_pieces_framework2.createAction)({
  auth: gotifyAuth,
  name: "send_notification",
  displayName: "Send Notification",
  description: "Send a notification to gotify",
  audience: "both",
  aiMetadata: { description: "Sends a push notification (title, message, optional 0-10 priority) to a self-hosted Gotify server via its message API. Choose this to alert a user or channel through their Gotify instance configured in the connection. Not idempotent: each call posts a new message, so repeating it delivers duplicate notifications.", idempotent: !1 },
  props: {
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Title",
      description: "The title of the notification",
      required: !0
    }),
    message: import_pieces_framework2.Property.LongText({
      displayName: "Message",
      description: "The message to send",
      required: !0
    }),
    priority: import_pieces_framework2.Property.Number({
      displayName: "Priority",
      description: "The priority of the notification (0-10). 0 is lowest priority.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let baseUrl = auth.props.base_url.replace(/\/$/, ""), appToken = auth.props.app_token, title = propsValue.title, message = propsValue.message, priority = propsValue.priority;
    return await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${baseUrl}/message?token=${appToken}`,
      body: __spreadValues({
        title,
        message
      }, priority && { priority: +priority })
    });
  }
});

// ../ap/packages/pieces/community/gotify/src/index.ts
var gotify = (0, import_pieces_framework3.createPiece)({
  displayName: "Gotify",
  description: "Self-hosted push notification service",
  logoUrl: "https://cdn.activepieces.com/pieces/gotify.png",
  minimumSupportedRelease: "0.30.0",
  categories: [import_pieces_framework4.PieceCategory.DEVELOPER_TOOLS],
  authors: ["MyWay", "kishanprmr", "khaledmashaly", "abuaboud"],
  auth: gotifyAuth,
  actions: [sendNotification],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  gotify
});
