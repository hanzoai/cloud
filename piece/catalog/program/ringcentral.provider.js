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

// ../ap/packages/pieces/community/ringcentral/src/index.ts
var index_exports = {};
__export(index_exports, {
  ringcentral: () => ringcentral
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/ringcentral/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), ringcentralAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: "In the [RingCentral Developer Console](https://developers.ringcentral.com/), create a REST API app using **OAuth 2.0 (Authorization Code Flow)** for a server/web app. Add the redirect URI shown here to the app, then enable the app scopes you need (for example: SMS, RingOut, Read Messages, Read Call Log, Read Accounts, TeamMessaging, Webhook Subscriptions). Copy the app's Client ID and Client Secret below, and pick the Environment that matches your app.",
  required: !0,
  props: {
    environment: import_pieces_framework.Property.StaticDropdown({
      displayName: "Environment",
      description: "The RingCentral server your app is registered on.",
      required: !0,
      defaultValue: "platform.ringcentral.com",
      options: {
        disabled: !1,
        options: [
          { label: "Production", value: "platform.ringcentral.com" },
          { label: "Sandbox", value: "platform.devtest.ringcentral.com" }
        ]
      }
    })
  },
  // RingCentral's token endpoint answers client creds in the request body with OAU-123 "Client
  // authentication is required" before it looks at the grant, and the framework defaults to
  // OAuth2AuthorizationMethod.BODY. Refresh reads the same stored method, so BODY breaks reconnects too.
  authorizationMethod: import_pieces_framework.OAuth2AuthorizationMethod.HEADER,
  authUrl: "https://{environment}/restapi/oauth/authorize",
  tokenUrl: "https://{environment}/restapi/oauth/token",
  scope: [],
  // The platform's default prompt=consent sends RingCentral's login to SSO-only (API_ERROR_208).
  prompt: "omit"
});

// ../ap/packages/pieces/community/ringcentral/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), ringcentralCommon = {
  getServerUrl,
  sendRequest,
  createSubscription,
  deleteSubscription
};
function describeRingCentralError(err, method, resourcePath) {
  let call = `${method} ${resourcePath}`;
  if (!(err instanceof import_pieces_common.HttpError)) {
    let detail2 = err instanceof Error && err.message ? ` (${err.message})` : "";
    return `RingCentral request ${call} failed before a response arrived${detail2}.`;
  }
  let { status, body } = err.errorMessage().response, detail = ringcentralErrorDetail(body);
  switch (status) {
    case 401:
      return `RingCentral rejected the connection's token (401 on ${call}). Reconnect the RingCentral connection.${detail}`;
    case 403:
      return `RingCentral refused ${call} (403). Usually the app in the RingCentral Developer Console is missing the permission this needs (for example SMS, RingOut or TeamMessaging).${detail}`;
    case 429:
      return `RingCentral rate-limited ${call} (429). Wait for the window to reset or slow the flow down.${detail}`;
    default:
      return `RingCentral answered ${status} for ${call}.${detail}`;
  }
}
var PRODUCTION_SERVER = "platform.ringcentral.com", SUBSCRIPTION_EXPIRES_IN_SECONDS = 63072e4, REQUEST_TIMEOUT_MS = 3e4, READ_RETRIES = 3;
function getServerUrl(auth) {
  var _a, _b;
  return `https://${(_b = (_a = auth.props) == null ? void 0 : _a.environment) != null ? _b : PRODUCTION_SERVER}`;
}
async function sendRequest({
  auth,
  method,
  resourcePath,
  body,
  queryParams,
  responseType
}) {
  let request = {
    method,
    url: `${getServerUrl(auth)}${resourcePath}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: auth.access_token
    },
    body,
    queryParams,
    responseType,
    timeout: REQUEST_TIMEOUT_MS,
    retries: method === import_pieces_common.HttpMethod.GET ? READ_RETRIES : 0
  };
  try {
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  } catch (err) {
    throw new Error(describeRingCentralError(err, method, resourcePath));
  }
}
async function createSubscription({
  auth,
  webhookUrl,
  eventFilters
}) {
  return (await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.POST,
    resourcePath: "/restapi/v1.0/subscription",
    body: {
      eventFilters,
      deliveryMode: {
        transportType: "WebHook",
        address: webhookUrl
      },
      expiresIn: SUBSCRIPTION_EXPIRES_IN_SECONDS
    }
  })).id;
}
async function deleteSubscription({
  auth,
  subscriptionId
}) {
  await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.DELETE,
    resourcePath: `/restapi/v1.0/subscription/${encodeURIComponent(subscriptionId)}`
  });
}
function ringcentralErrorDetail(body) {
  var _a;
  if (!body) return "";
  let messages = ((_a = body.errors) != null ? _a : []).filter((e) => e !== null).map((e) => [e.errorCode, e.message].filter(Boolean).join(" "));
  return messages.length === 0 && body.message && messages.push([body.errorCode, body.message].filter(Boolean).join(" ")), messages.length > 0 ? ` RingCentral says: ${messages.join("; ")}` : "";
}

// ../ap/packages/pieces/community/ringcentral/src/lib/actions/send-sms.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/ringcentral/src/lib/common/props.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var smsFromNumberDropdown = import_pieces_framework2.Property.Dropdown({
  auth: ringcentralAuth,
  displayName: "From",
  description: "The SMS-enabled RingCentral number to send from.",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return notConnected();
    let senders = ((_a = (await ringcentralCommon.sendRequest({
      auth,
      method: import_pieces_common2.HttpMethod.GET,
      resourcePath: "/restapi/v1.0/account/~/extension/~/phone-number",
      // The documented maximum. An extension with more direct numbers than this is not a shape worth
      // paging a dropdown for, and the SMS senders among them are a small subset anyway.
      queryParams: { perPage: "1000" }
    })).records) != null ? _a : []).filter(
      (record) => {
        var _a2;
        return ((_a2 = record.features) != null ? _a2 : []).includes("SmsSender");
      }
    );
    return senders.length === 0 ? {
      disabled: !0,
      // Distinguishes "nothing is SMS-enabled" from "the lookup failed", which look identical in an
      // empty dropdown and send the reader to the wrong place.
      placeholder: "No SMS-enabled numbers on this extension. Enable SMS on a number in the RingCentral admin portal.",
      options: []
    } : {
      disabled: !1,
      options: senders.map((record) => ({
        label: record.usageType ? `${record.phoneNumber} (${record.usageType})` : record.phoneNumber,
        value: record.phoneNumber
      }))
    };
  }
}), chatDropdown = import_pieces_framework2.Property.Dropdown({
  auth: ringcentralAuth,
  displayName: "Chat",
  description: "The direct message, group or team to post to.",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await listAllChats(auth)).map((chat) => {
      var _a, _b;
      return {
        label: (_b = chat.name) != null ? _b : `${(_a = chat.type) != null ? _a : "Chat"} (${chat.id})`,
        value: chat.id
      };
    })
  } : notConnected()
});
function notConnected() {
  return {
    disabled: !0,
    placeholder: "Connect your account first",
    options: []
  };
}
async function listAllChats(auth) {
  var _a, _b;
  let collected = [], pageToken;
  for (let page = 0; page < 10; page++) {
    let queryParams = { recordCount: "250" };
    pageToken && (queryParams.pageToken = pageToken);
    let response = await ringcentralCommon.sendRequest({
      auth,
      method: import_pieces_common2.HttpMethod.GET,
      resourcePath: "/team-messaging/v1/chats",
      queryParams
    });
    if (collected.push(...(_a = response.records) != null ? _a : []), pageToken = (_b = response.navigation) == null ? void 0 : _b.nextPageToken, !pageToken) break;
  }
  return collected;
}

// ../ap/packages/pieces/community/ringcentral/src/lib/actions/send-sms.ts
var sendSms = (0, import_pieces_framework3.createAction)({
  auth: ringcentralAuth,
  name: "send_sms",
  displayName: "Send SMS",
  description: "Send an SMS text message from one of your RingCentral numbers.",
  props: {
    from: smsFromNumberDropdown,
    to: import_pieces_framework3.Property.Array({
      displayName: "To",
      description: "Recipient phone number(s) in E.164 format (e.g. +14155550123).",
      required: !0
    }),
    text: import_pieces_framework3.Property.LongText({
      displayName: "Message",
      description: "The text content of the SMS message.",
      required: !0
    })
  },
  async run(context) {
    let { from, to, text } = context.propsValue;
    return await ringcentralCommon.sendRequest({
      auth: context.auth,
      method: import_pieces_common3.HttpMethod.POST,
      resourcePath: "/restapi/v1.0/account/~/extension/~/sms",
      body: {
        from: { phoneNumber: from },
        to: to.map((phoneNumber) => ({ phoneNumber: String(phoneNumber) })),
        text
      }
    });
  }
});

// ../ap/packages/pieces/community/ringcentral/src/lib/actions/make-call.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var makeCall = (0, import_pieces_framework4.createAction)({
  auth: ringcentralAuth,
  name: "make_call",
  displayName: "Make Call (RingOut)",
  description: 'Start a two-legged RingOut call: RingCentral calls the "from" number first, then connects it to the "to" number.',
  props: {
    from: import_pieces_framework4.Property.ShortText({
      displayName: "From",
      description: "The number RingCentral calls first, in E.164 format (e.g. +14155550100). Usually one of your RingCentral numbers.",
      required: !0
    }),
    to: import_pieces_framework4.Property.ShortText({
      displayName: "To",
      description: "The number to connect the call to, in E.164 format (e.g. +14155550123).",
      required: !0
    }),
    callerId: import_pieces_framework4.Property.ShortText({
      displayName: "Caller ID",
      description: "Optional number shown to the callee, in E.164 format. Must be one of your RingCentral numbers.",
      required: !1
    }),
    playPrompt: import_pieces_framework4.Property.Checkbox({
      displayName: "Play Prompt",
      description: 'Play a "please hold" prompt to the "from" party before connecting the call.',
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { from, to, callerId, playPrompt } = context.propsValue;
    return await ringcentralCommon.sendRequest({
      auth: context.auth,
      method: import_pieces_common4.HttpMethod.POST,
      resourcePath: "/restapi/v1.0/account/~/extension/~/ring-out",
      body: __spreadProps(__spreadValues({
        from: { phoneNumber: from },
        to: { phoneNumber: to }
      }, callerId ? { callerId: { phoneNumber: callerId } } : {}), {
        playPrompt: playPrompt != null ? playPrompt : !1
      })
    });
  }
});

// ../ap/packages/pieces/community/ringcentral/src/lib/actions/send-team-message.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var sendTeamMessage = (0, import_pieces_framework5.createAction)({
  auth: ringcentralAuth,
  name: "send_team_message",
  displayName: "Send Team Messaging Post",
  description: "Post a message to a RingCentral Team Messaging chat, group, or team.",
  props: {
    chatId: chatDropdown,
    text: import_pieces_framework5.Property.LongText({
      displayName: "Message",
      description: "The text content of the post. Supports Markdown.",
      required: !0
    })
  },
  async run(context) {
    let { chatId, text } = context.propsValue;
    return await ringcentralCommon.sendRequest({
      auth: context.auth,
      method: import_pieces_common5.HttpMethod.POST,
      resourcePath: `/team-messaging/v1/chats/${encodeURIComponent(chatId)}/posts`,
      body: { text }
    });
  }
});

// ../ap/packages/pieces/community/ringcentral/src/lib/actions/get-call-log.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getCallLog = (0, import_pieces_framework6.createAction)({
  auth: ringcentralAuth,
  name: "get_call_log",
  displayName: "Get Call Log",
  description: "Retrieve call log records for the authenticated user's extension.",
  props: {
    direction: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Direction",
      description: "Filter records by call direction.",
      required: !1,
      options: {
        options: [
          { label: "Inbound", value: "Inbound" },
          { label: "Outbound", value: "Outbound" }
        ]
      }
    }),
    type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Type",
      description: "Filter records by call type.",
      required: !1,
      options: {
        options: [
          { label: "Voice", value: "Voice" },
          { label: "Fax", value: "Fax" }
        ]
      }
    }),
    dateFrom: import_pieces_framework6.Property.ShortText({
      displayName: "Date From",
      description: "The start of the time range in ISO 8601 format (e.g. 2024-01-01T00:00:00Z).",
      required: !1
    }),
    dateTo: import_pieces_framework6.Property.ShortText({
      displayName: "Date To",
      description: "The end of the time range in ISO 8601 format (e.g. 2024-01-31T23:59:59Z).",
      required: !1
    }),
    perPage: import_pieces_framework6.Property.Number({
      displayName: "Records Per Page",
      description: "Maximum number of records to return (1-1000).",
      required: !1
    })
  },
  async run(context) {
    let { direction, type, dateFrom, dateTo, perPage } = context.propsValue, queryParams = {};
    return direction && (queryParams.direction = direction), type && (queryParams.type = type), dateFrom && (queryParams.dateFrom = dateFrom), dateTo && (queryParams.dateTo = dateTo), perPage && (queryParams.perPage = String(perPage)), await ringcentralCommon.sendRequest({
      auth: context.auth,
      method: import_pieces_common6.HttpMethod.GET,
      resourcePath: "/restapi/v1.0/account/~/extension/~/call-log",
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/ringcentral/src/lib/actions/get-extension-info.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var getExtensionInfo = (0, import_pieces_framework7.createAction)({
  auth: ringcentralAuth,
  name: "get_extension_info",
  displayName: "Get Extension Info",
  description: "Get profile information for the authenticated user's extension.",
  props: {},
  async run(context) {
    return await ringcentralCommon.sendRequest({
      auth: context.auth,
      method: import_pieces_common7.HttpMethod.GET,
      resourcePath: "/restapi/v1.0/account/~/extension/~"
    });
  }
});

// ../ap/packages/pieces/community/ringcentral/src/lib/actions/get-message.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var getMessage = (0, import_pieces_framework8.createAction)({
  auth: ringcentralAuth,
  name: "get_message",
  displayName: "Get Message",
  description: "Retrieve a single SMS, MMS or voicemail message, including the list of attachments it carries.",
  props: {
    messageId: import_pieces_framework8.Property.ShortText({
      displayName: "Message ID",
      description: "The message to read. The New Inbound SMS or MMS trigger emits this as `id`.",
      required: !0
    })
  },
  async run(context) {
    let { messageId } = context.propsValue;
    return await ringcentralCommon.sendRequest({
      auth: context.auth,
      method: import_pieces_common8.HttpMethod.GET,
      resourcePath: `/restapi/v1.0/account/~/extension/~/message-store/${encodeURIComponent(
        messageId
      )}`
    });
  }
});

// ../ap/packages/pieces/community/ringcentral/src/lib/actions/download-message-attachment.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var downloadMessageAttachment = (0, import_pieces_framework9.createAction)({
  auth: ringcentralAuth,
  name: "download_message_attachment",
  displayName: "Download Message Attachment",
  description: "Download the content of a message attachment, such as an MMS photo or a voicemail recording, and return it as a file.",
  props: {
    messageId: import_pieces_framework9.Property.ShortText({
      displayName: "Message ID",
      description: "The message the attachment belongs to.",
      required: !0
    }),
    attachmentId: import_pieces_framework9.Property.ShortText({
      displayName: "Attachment ID",
      description: "Which attachment to download. Leave blank to take the first one that is not the message text, which is the usual case for an MMS carrying a single photo.",
      required: !1
    }),
    fileName: import_pieces_framework9.Property.ShortText({
      displayName: "File Name",
      description: "Overrides the name RingCentral reports. Worth setting when the name matters downstream, e.g. naming a POD after the load number.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { messageId, attachmentId, fileName } = context.propsValue, attachments = (_a = (await ringcentralCommon.sendRequest({
      auth: context.auth,
      method: import_pieces_common9.HttpMethod.GET,
      resourcePath: `/restapi/v1.0/account/~/extension/~/message-store/${encodeURIComponent(
        messageId
      )}`
    })).attachments) != null ? _a : [];
    if (attachments.length === 0)
      throw new Error(
        `Message ${messageId} carries no attachments. An SMS with no media has none, so check the message id or use Get Message to inspect it.`
      );
    let attachment = attachmentId ? attachments.find((a) => String(a.id) === String(attachmentId)) : (
      // 'Text' is the SMS body itself, which is already on the trigger payload and is not the file
      // anyone means by "the attachment", so it is skipped when picking a default.
      (_b = attachments.find((a) => a.type !== "Text")) != null ? _b : attachments[0]
    );
    if (!attachment)
      throw new Error(
        `Message ${messageId} has no attachment ${attachmentId}. It carries: ${attachments.map((a) => {
          var _a2;
          return `${a.id} (${(_a2 = a.type) != null ? _a2 : "unknown type"})`;
        }).join(", ")}.`
      );
    let content = await ringcentralCommon.sendRequest({
      auth: context.auth,
      method: import_pieces_common9.HttpMethod.GET,
      resourcePath: `/restapi/v1.0/account/~/extension/~/message-store/${encodeURIComponent(
        messageId
      )}/content/${encodeURIComponent(String(attachment.id))}`,
      responseType: "arraybuffer"
    }), reportedName = (_c = attachment.fileName) != null ? _c : attachment.filename;
    return { file: await context.files.write({
      fileName: (_d = fileName != null ? fileName : reportedName) != null ? _d : `ringcentral-${messageId}-${attachment.id}`,
      data: Buffer.from(content)
    }), attachment };
  }
});

// ../ap/packages/pieces/community/ringcentral/src/lib/common/subscription-trigger.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
function createSubscriptionTrigger({
  name,
  displayName,
  description,
  eventFilters,
  accept,
  sampleData
}) {
  let subscriptionIdKey = `ringcentral_${name}_subscription_id`;
  return (0, import_pieces_framework10.createTrigger)({
    auth: ringcentralAuth,
    name,
    displayName,
    description,
    type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
    props: {},
    handshakeConfiguration: {
      strategy: import_pieces_framework10.WebhookHandshakeStrategy.HEADER_PRESENT,
      paramName: "validation-token"
    },
    async onHandshake(context) {
      let validationToken = context.payload.headers["validation-token"];
      return validationToken ? {
        status: 200,
        headers: { "Validation-Token": validationToken }
      } : { status: 400, body: { message: "Missing Validation-Token header." } };
    },
    async onEnable(context) {
      let subscriptionId = await ringcentralCommon.createSubscription({
        auth: context.auth,
        webhookUrl: context.webhookUrl,
        eventFilters
      });
      await context.store.put(subscriptionIdKey, subscriptionId);
    },
    async onDisable(context) {
      let subscriptionId = await context.store.get(subscriptionIdKey);
      if (subscriptionId)
        try {
          await ringcentralCommon.deleteSubscription({ auth: context.auth, subscriptionId });
        } catch (e) {
        }
      await context.store.delete(subscriptionIdKey);
    },
    async run(context) {
      var _a, _b;
      let event = (_a = context.payload.body) != null ? _a : {}, expectedSubscriptionId = await context.store.get(subscriptionIdKey);
      if (!expectedSubscriptionId || event.subscriptionId !== expectedSubscriptionId)
        return [];
      let body = event.body;
      if (!body || accept && !accept(body))
        return [];
      let dedupeKey = (_b = body.id) != null ? _b : event.uuid;
      return dedupeKey == null || dedupeKey === "" ? [body] : [__spreadProps(__spreadValues({}, body), { [import_pieces_framework10.DEDUPE_KEY_PROPERTY]: String(dedupeKey) })];
    },
    sampleData
  });
}

// ../ap/packages/pieces/community/ringcentral/src/lib/triggers/new-inbound-sms.ts
var newInboundSms = createSubscriptionTrigger({
  name: "new_inbound_sms",
  displayName: "New Inbound SMS or MMS",
  description: "Triggers when a new inbound text message is received, with or without media attached.",
  // ONE filter, and `type=SMS` is correct for picture messages too. Do not add `type=MMS`: there is
  // no MMS message type. RingCentral delivers an inbound MMS through this same filter with
  // `type: 'SMS'` and an `MmsAttachment` part, and an unrecognised type value can fail the whole
  // createSubscription call, which would break the trigger rather than widen it.
  // https://developers.ringcentral.com/guide/messaging/sms/receiving-sms-mms
  eventFilters: ["/restapi/v1.0/account/~/extension/~/message-store/instant?type=SMS"],
  // The instant message-store filter also fires for what this extension sends.
  accept: (message) => message.direction === "Inbound",
  sampleData: {
    uuid: "3c9d3d10-1f1a-4f0e-9b0e-2f2a9a1a4b6c",
    id: 1234567890,
    to: [{ phoneNumber: "+14155550100", name: "RingCentral User" }],
    from: { phoneNumber: "+14155550123" },
    type: "SMS",
    creationTime: "2024-01-15T18:30:00.000Z",
    readStatus: "Unread",
    priority: "Normal",
    // A text-only SMS carries just the Text part. An inbound MMS looks identical except for an
    // extra MmsAttachment part, which is what Download Message Attachment fetches.
    attachments: [
      { id: 111, type: "Text", contentType: "text/plain" },
      { id: 222, type: "MmsAttachment", contentType: "image/jpeg" }
    ],
    direction: "Inbound",
    availability: "Alive",
    subject: "Hello from a customer!",
    messageStatus: "Received",
    conversationId: 9876543210,
    conversation: { id: "9876543210" },
    lastModifiedTime: "2024-01-15T18:30:00.000Z"
  }
});

// ../ap/packages/pieces/community/ringcentral/src/lib/triggers/new-voicemail.ts
var newVoicemail = createSubscriptionTrigger({
  name: "new_voicemail",
  displayName: "New Voicemail",
  description: "Triggers when a new voicemail message is received.",
  // The dedicated voicemail filter, NOT message-store/instant. The instant filter is documented for
  // inbound SMS only, so `?type=VoiceMail` either fails validation on enable or enables and never
  // delivers. https://developers.ringcentral.com/guide/notifications/event-filters/voicemail-message
  eventFilters: ["/restapi/v1.0/account/~/extension/~/voicemail"],
  sampleData: {
    uuid: "5a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d",
    id: 2233445566,
    to: [{ phoneNumber: "+14155550100", name: "RingCentral User" }],
    from: { phoneNumber: "+14155550123", name: "Jane Caller" },
    type: "VoiceMail",
    creationTime: "2024-01-15T19:05:00.000Z",
    readStatus: "Unread",
    priority: "Normal",
    attachments: [
      { id: 222, type: "AudioRecording", contentType: "audio/mpeg", vmDuration: 12 }
    ],
    direction: "Inbound",
    availability: "Alive",
    messageStatus: "Received",
    vmTranscriptionStatus: "NotAvailable",
    lastModifiedTime: "2024-01-15T19:05:00.000Z"
  }
});

// ../ap/packages/pieces/community/ringcentral/src/lib/triggers/new-team-message.ts
var newTeamMessage = createSubscriptionTrigger({
  name: "new_team_message",
  displayName: "New Team Messaging Post",
  description: "Triggers when a new post is added in RingCentral Team Messaging.",
  eventFilters: ["/restapi/v1.0/glip/posts"],
  // The posts filter also fires for edits and removals; only additions are this trigger.
  accept: (post) => post.eventType === "PostAdded",
  sampleData: {
    id: "5544332211",
    groupId: "112233445566",
    type: "TextMessage",
    text: "Hey team, the deploy is done!",
    creatorId: "778899",
    addedPersonIds: ["778899"],
    creationTime: "2024-01-15T20:10:00.000Z",
    lastModifiedTime: "2024-01-15T20:10:00.000Z",
    eventType: "PostAdded"
  }
});

// ../ap/packages/pieces/community/ringcentral/src/index.ts
var ringcentral = (0, import_pieces_framework11.createPiece)({
  displayName: "RingCentral",
  description: "Cloud business communications: send SMS, place calls, manage messages, and post to Team Messaging.",
  auth: ringcentralAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/ringcentral.png",
  authors: ["alexandronic"],
  categories: [import_pieces_framework11.PieceCategory.COMMUNICATION],
  actions: [
    sendSms,
    makeCall,
    sendTeamMessage,
    getCallLog,
    getExtensionInfo,
    getMessage,
    downloadMessageAttachment,
    (0, import_pieces_common10.createCustomApiCallAction)({
      auth: ringcentralAuth,
      baseUrl: (auth) => auth ? ringcentralCommon.getServerUrl(auth) : "",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [newInboundSms, newVoicemail, newTeamMessage]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  ringcentral
});
