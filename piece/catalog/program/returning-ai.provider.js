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

// ../ap/packages/pieces/community/returning-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  returningAi: () => returningAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/returning-ai/src/lib/actions/reply-message.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/returning-ai/src/lib/common/index.ts
function getApiEndpoint(authToken) {
  return authToken ? authToken.includes("local:") ? "http://localhost:3333" : authToken.includes("pre-staging:") ? "https://sgtr-integration.returning.ai" : authToken.includes("staging:") ? "https://staging-integration.returning.ai" : authToken.includes("playground:") ? "https://playground-integration.returning.ai" : "https://integration.returning.ai" : "https://integration.returning.ai";
}

// ../ap/packages/pieces/community/returning-ai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), returningAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Add api key from returning.ai"
});

// ../ap/packages/pieces/community/returning-ai/src/lib/actions/reply-message.ts
var replyMessage = (0, import_pieces_framework2.createAction)({
  auth: returningAiAuth,
  name: "replyMessage",
  displayName: "Reply Message",
  description: "Reply to a specific message as a chosen user in a channel.",
  audience: "both",
  aiMetadata: { description: "Posts a threaded reply to an existing Returning.ai message on behalf of a given user (identified by username or email), so the reply appears as if that user sent it. Use to respond in-thread to a known message; requires the target message ID and the reply text. Not idempotent: each call adds a new reply.", idempotent: !1 },
  props: {
    description: import_pieces_framework2.Property.MarkDown({
      value: "This action allows you to reply to a message in any accessible channel on behalf of a specific user. The reply will appear as if the selected user posted it directly in response to the original message."
    }),
    user: import_pieces_framework2.Property.ShortText({
      displayName: "Sender (Username or email)",
      description: "The user account that will send the message",
      required: !0
    }),
    messageId: import_pieces_framework2.Property.ShortText({
      displayName: "Reply to (Message ID)",
      description: "The ID of the message to reply to. ",
      required: !0
    }),
    message: import_pieces_framework2.Property.LongText({
      displayName: "Message",
      description: "The content of the reply to be posted.",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let authToken = auth.secret_text;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${getApiEndpoint(authToken)}/apis/v1/messages/reply`,
      headers: {
        Authorization: `Bearer ${authToken.includes(":") ? authToken.split(":")[1] : authToken}`
      },
      body: {
        messageId: propsValue.messageId,
        message: propsValue.message,
        sender: propsValue.user
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/returning-ai/src/lib/actions/send-message.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var sendMessage = (0, import_pieces_framework3.createAction)({
  auth: returningAiAuth,
  name: "sendMessage",
  displayName: "Send Channel Message",
  description: "Posts a message to a specified channel as a chosen user",
  audience: "both",
  aiMetadata: { description: "Posts a new message to a Returning.ai channel on behalf of a given user (identified by username or email), so the message appears as if that user sent it. Use to broadcast or announce content in a channel; supply message text, image URLs, or both (at least one is required), and for forum-type channels a topic ID is also required. Not idempotent: each call posts a new message.", idempotent: !1 },
  props: {
    description: import_pieces_framework3.Property.MarkDown({
      value: "This action allows you to send messages to any accessible channel as a specific user. The message will appear as if the selected user posted it directly."
    }),
    user: import_pieces_framework3.Property.ShortText({
      displayName: "Sender (Username or email)",
      description: "The user account that will send the message",
      required: !0
    }),
    channel: import_pieces_framework3.Property.Dropdown({
      auth: returningAiAuth,
      displayName: "Channel",
      description: "The channel where the message will be posted",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        let authToken = auth == null ? void 0 : auth.secret_text;
        if (!authToken)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please authenticate first"
          };
        let response = await import_pieces_common2.httpClient.sendRequest({
          method: import_pieces_common2.HttpMethod.GET,
          url: `${getApiEndpoint(authToken)}/apis/v1/channels`,
          headers: {
            Authorization: `Bearer ${authToken.includes(":") ? authToken.split(":")[1] : authToken}`
          }
        });
        return response.body.status === "success" ? {
          options: response.body.data.map(
            (channel) => ({
              label: channel.topic,
              value: JSON.stringify({
                id: channel._id,
                type: channel.channelType
              })
            })
          )
        } : {
          disabled: !0,
          options: [],
          placeholder: response.body.message
        };
      }
    }),
    dynamicFields: import_pieces_framework3.Property.DynamicProperties({
      auth: returningAiAuth,
      displayName: "Channel Fields",
      description: "Additional fields based on channel type",
      required: !0,
      refreshers: ["channel"],
      props: async ({ channel }) => {
        let properties = {};
        return channel && JSON.parse(channel).type === "forum" && (properties.topicId = import_pieces_framework3.Property.ShortText({
          displayName: "Topic ID",
          description: "The topic ID of the forum channel where the message will be posted",
          required: !0
        })), properties;
      }
    }),
    message: import_pieces_framework3.Property.LongText({
      displayName: "Message",
      description: "The message to be posted (optional if images are provided)",
      required: !1
    }),
    images: import_pieces_framework3.Property.Array({
      displayName: "Images",
      description: "Add one image URL per entry (e.g., https://example.com/image1.png)",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let authToken = auth.secret_text, channelData = JSON.parse(propsValue.channel), imagesInput = propsValue.images, dynamicFields = propsValue.dynamicFields, imageUrls = imagesInput ? imagesInput.map((url) => url.trim()).filter((url) => url.length > 0) : [], hasMessage = propsValue.message && propsValue.message.trim().length > 0, hasImages = imageUrls.length > 0;
    return !hasMessage && !hasImages ? {
      status: "error",
      message: 'Either "message" (text content) or "images" (comma-separated URLs) must be provided.',
      detail: "Both fields are empty or missing."
    } : (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${getApiEndpoint(authToken)}/apis/v1/messages/send`,
      headers: {
        Authorization: `Bearer ${authToken.includes(":") ? authToken.split(":")[1] : authToken}`
      },
      body: __spreadValues(__spreadValues({
        channelId: channelData.id,
        message: propsValue.message,
        sender: propsValue.user
      }, channelData.type === "forum" && {
        forumTopicId: dynamicFields.topicId
      }), hasImages && {
        images: imageUrls
      })
    })).body;
  }
});

// ../ap/packages/pieces/community/returning-ai/src/lib/actions/react-message.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var reactMessage = (0, import_pieces_framework4.createAction)({
  auth: returningAiAuth,
  name: "reactMessage",
  displayName: "React to Message",
  description: "Add an emoji reaction to a specific message as a chosen user.",
  audience: "both",
  aiMetadata: { description: "Adds an emoji reaction to an existing Returning.ai message on behalf of a given user (identified by username or email), so the reaction appears as if that user added it. Use to acknowledge or react to a known message; requires the target message ID and an emoji shortcode (e.g. :sparkling_heart:). Not idempotent: the API is called on each invocation.", idempotent: !1 },
  props: {
    description: import_pieces_framework4.Property.MarkDown({
      value: "This action allows you to add an emoji reaction to a message in any accessible channel on behalf of a specific user. The reaction will appear as if the selected user added it directly to the message."
    }),
    user: import_pieces_framework4.Property.ShortText({
      displayName: "Sender (Username or email)",
      description: "The user account that will add the reaction",
      required: !0
    }),
    messageId: import_pieces_framework4.Property.ShortText({
      displayName: "React to (Message ID)",
      description: "The ID of the message to react to",
      required: !0
    }),
    emoji: import_pieces_framework4.Property.ShortText({
      displayName: "Reaction emoji",
      description: "The emoji to react with (e.g. :sparkling_heart:)",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let authToken = auth.secret_text;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${getApiEndpoint(authToken)}/apis/v1/messages/react`,
      headers: {
        Authorization: `Bearer ${authToken.includes(":") ? authToken.split(":")[1] : authToken}`
      },
      body: {
        messageId: propsValue.messageId,
        emoji: propsValue.emoji,
        sender: propsValue.user
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/returning-ai/src/index.ts
var returningAi = (0, import_pieces_framework5.createPiece)({
  displayName: "Returning AI",
  auth: returningAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/returning-ai.png",
  authors: ["mg-wunna"],
  actions: [sendMessage, replyMessage, reactMessage],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  returningAi
});
