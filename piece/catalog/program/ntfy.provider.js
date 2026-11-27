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

// ../ap/packages/pieces/community/ntfy/src/index.ts
var index_exports = {};
__export(index_exports, {
  ntfy: () => ntfy,
  ntfyAuth: () => ntfyAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/ntfy/src/lib/actions/send-notification.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var encodeToRFC2047 = (text) => `=?UTF-8?B?${Buffer.from(text, "utf-8").toString("base64")}?=`, sendNotification = (0, import_pieces_framework.createAction)({
  auth: ntfyAuth,
  name: "send_notification",
  classification: "WRITE",
  displayName: "Send Notification",
  description: "Send a notification to ntfy",
  audience: "both",
  aiMetadata: { description: "Publishes a push notification to a ntfy topic on the configured ntfy server, optionally with a title, priority (1-5), tags, icon, click-through URL, action buttons, or scheduled delay. Choose this to alert a user or channel via ntfy. Requires a topic and message; each call sends a new notification (not idempotent).", idempotent: !1 },
  props: {
    topic: import_pieces_framework.Property.ShortText({
      displayName: "Topic",
      description: "The topic/channel to send the notification to, e.g. test1",
      required: !0
    }),
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
    priority: import_pieces_framework.Property.ShortText({
      displayName: "Priority",
      description: "The priority of the notification (1-5). 1 is lowest priority.",
      required: !1
    }),
    tags: import_pieces_framework.Property.Array({
      displayName: "Tags",
      description: "The tags for the notification.",
      required: !1
    }),
    icon: import_pieces_framework.Property.ShortText({
      displayName: "Icon",
      description: "The absolute URL to your icon, e.g. https://example.com/communityIcon_xnt6chtnr2j21.png",
      required: !1
    }),
    actions: import_pieces_framework.Property.LongText({
      displayName: "Actions",
      description: "Add Action buttons to notifications, see https://docs.ntfy.sh/publish/#action-buttons",
      required: !1
    }),
    click: import_pieces_framework.Property.ShortText({
      displayName: "Click",
      description: "You can define which URL to open when a notification is clicked, see https://docs.ntfy.sh/publish/#click-action",
      required: !1
    }),
    delay: import_pieces_framework.Property.ShortText({
      displayName: "Delay",
      description: "Let ntfy send messages at a later date, e.g. 'tomorrow, 10am', see https://docs.ntfy.sh/publish/#scheduled-delivery",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let baseUrl = auth.props.base_url.replace(/\/$/, ""), accessToken = auth.props.access_token, topic = propsValue.topic, title = propsValue.title, message = propsValue.message;
    title = encodeToRFC2047(title), message = encodeToRFC2047(message);
    let priority = propsValue.priority, tags = propsValue.tags, icon = propsValue.icon, actions = propsValue.actions, click = propsValue.click, delay = propsValue.delay;
    return await import_pieces_common.httpClient.sendRequest(__spreadProps(__spreadValues({
      method: import_pieces_common.HttpMethod.POST,
      url: `${baseUrl}/${topic}`
    }, accessToken && {
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      }
    }), {
      headers: {
        "X-Message": message,
        "X-Title": title,
        "X-Priority": priority,
        "X-Tags": tags == null ? void 0 : tags.join(","),
        "X-Icon": icon,
        "X-Actions": actions,
        "X-Click": click,
        "X-Delay": delay
      }
    }));
  }
});

// ../ap/packages/pieces/community/ntfy/src/index.ts
var ntfyAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  description: `
  To obtain a token:

  1. Log in to your Ntfy instance.
  2. Click on Account
  3. Go under, on Access tokens and click on the button icon to copy your Token or CREATE ACCESS TOKEN if you do not have
  4. Please pay attention to the expiration time when copying/creating a Token.
  4. Copy your access token & and paste them into the fields below.
  `,
  props: {
    base_url: import_pieces_framework2.Property.ShortText({
      displayName: "Server URL",
      description: "Ntfy Instance URL",
      required: !0
    }),
    access_token: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "Access Token",
      description: "Ntfy Access Token",
      required: !1
    })
  },
  required: !0
}), ntfy = (0, import_pieces_framework2.createPiece)({
  displayName: "ntfy",
  description: "Notification management made easy",
  logoUrl: "https://cdn.activepieces.com/pieces/ntfy.png",
  minimumSupportedRelease: "0.30.0",
  categories: [import_pieces_framework3.PieceCategory.COMMUNICATION],
  auth: ntfyAuth,
  authors: ["MyWay", "facferreira", "la3rence", "kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  actions: [
    sendNotification,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: (auth) => {
        var _a;
        return (_a = auth == null ? void 0 : auth.props.base_url) != null ? _a : "";
      },
      auth: ntfyAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.access_token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  ntfy,
  ntfyAuth
});
