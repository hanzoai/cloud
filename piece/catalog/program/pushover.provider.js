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

// ../ap/packages/pieces/community/pushover/src/index.ts
var index_exports = {};
__export(index_exports, {
  pushover: () => pushover,
  pushoverAuth: () => pushoverAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pushover/src/lib/actions/send-notification.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var sendNotification = (0, import_pieces_framework.createAction)({
  auth: pushoverAuth,
  name: "send_notification",
  classification: "WRITE",
  displayName: "Send Notification",
  description: "Send a notification to Pushover",
  audience: "both",
  aiMetadata: {
    description: "Sends a push notification through Pushover to the device(s) tied to the configured user/group key. Use to alert a person or group on their phone/desktop. The message body is required; optional priority (-2 to 2) controls urgency, and emergency priority (2) additionally requires retry and expire values. Not idempotent: each call delivers a new notification.",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework.Property.ShortText({
      displayName: "Title",
      description: "The title of the notification",
      required: !1
    }),
    message: import_pieces_framework.Property.LongText({
      displayName: "Message",
      description: "The message to send",
      required: !0
    }),
    html: import_pieces_framework.Property.Checkbox({
      displayName: "Enable HTML",
      description: "To enable HTML parsing",
      required: !1
    }),
    priority: import_pieces_framework.Property.Number({
      displayName: "Priority",
      description: "The priority of the notification (-2 to 2). -2 is lowest priority. If set to 2, you should also specify Retry and Expire.",
      required: !1
    }),
    retry: import_pieces_framework.Property.Number({
      displayName: "Retry",
      description: "Works only if priority is set to 2. Specifies how often (in seconds) the Pushover servers will send the same notification to the user.",
      required: !1
    }),
    expire: import_pieces_framework.Property.Number({
      displayName: "Expire",
      description: "Works only if priority is set to 2. Specifies how many seconds your notification will continue to be retried for (every retry seconds).",
      required: !1
    }),
    url: import_pieces_framework.Property.ShortText({
      displayName: "URL",
      description: "A supplementary URL to show with your message.",
      required: !1
    }),
    url_title: import_pieces_framework.Property.ShortText({
      displayName: "URL Title",
      description: "A title for the URL specified as the url input parameter, otherwise just the URL is shown.",
      required: !1
    }),
    timestamp: import_pieces_framework.Property.ShortText({
      displayName: "Timestamp",
      description: "a Unix timestamp of a time to display instead of when our API received it.",
      required: !1
    }),
    device: import_pieces_framework.Property.ShortText({
      displayName: "Device",
      description: "The name of one of your devices to send just to that device instead of all devices.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let baseUrl = "https://api.pushover.net/1/messages.json", apiToken = auth.props.api_token, userKey = auth.props.user_key, title = propsValue.title, message = propsValue.message, html = propsValue.html, priority = propsValue.priority, url = propsValue.url, url_title = propsValue.url_title, timestamp = propsValue.timestamp, device = propsValue.device, retry = propsValue.retry, expire = propsValue.expire;
    return await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: baseUrl,
      body: __spreadValues(__spreadValues(__spreadValues({
        token: apiToken,
        user: userKey,
        title,
        message,
        html: html ? 1 : 0,
        url,
        url_title,
        timestamp,
        device
      }, priority && { priority: +priority }), retry && { retry: +retry }), expire && { expire: +expire })
    });
  }
});

// ../ap/packages/pieces/community/pushover/src/index.ts
var pushoverAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  description: `
    To obtain the api token:

    1. Log in to Pushover.
    2. Click on your Application or on Create an Application/API Token
    3. Copy the API Token/Key.

    To obtain the user key:
    1. Log in to Pushover
    2. Copy your Your User Key

    Note if you want to send the message to your group, you should specify a group key instead of the user key
    `,
  props: {
    api_token: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "Api Token",
      description: "Pushover Api Token",
      required: !0
    }),
    user_key: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "User Key",
      description: "Pushover User Key",
      required: !0
    })
  },
  required: !0
}), pushover = (0, import_pieces_framework2.createPiece)({
  displayName: "Pushover",
  description: "Simple push notification service",
  logoUrl: "https://cdn.activepieces.com/pieces/pushover.png",
  categories: [import_pieces_framework3.PieceCategory.COMMUNICATION],
  minimumSupportedRelease: "0.30.0",
  authors: ["MyWay", "Vitalini", "kishanprmr", "khaledmashaly", "abuaboud"],
  auth: pushoverAuth,
  actions: [sendNotification],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pushover,
  pushoverAuth
});
