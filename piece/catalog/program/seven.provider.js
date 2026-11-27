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

// ../ap/packages/pieces/community/seven/src/index.ts
var index_exports = {};
__export(index_exports, {
  seven: () => seven
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/seven/src/action/sms-send.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/seven/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), sevenAuth = import_pieces_framework.PieceAuth.SecretText({
  description: "You can find your API key in [Developer Menu](https://app.seven.io/developer).",
  displayName: "API key",
  required: !0
});

// ../ap/packages/pieces/community/seven/src/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
var callSevenApi = async (httpRequest, path, apiKey) => await import_pieces_common.httpClient.sendRequest(__spreadProps(__spreadValues({}, httpRequest), {
  headers: __spreadProps(__spreadValues({}, httpRequest.headers), {
    Accept: "application/json",
    "Content-Type": "application/json",
    SentWith: "Activepieces",
    "X-Api-Key": apiKey
  }),
  url: `https://gateway.seven.io/api/${path}`
}));

// ../ap/packages/pieces/community/seven/src/action/sms-send.ts
var sendSmsAction = (0, import_pieces_framework2.createAction)({
  auth: sevenAuth,
  name: "send-sms",
  displayName: "Send SMS",
  description: "Sends an SMS to one or more recipients.",
  audience: "both",
  aiMetadata: {
    description: "Sends an SMS text message via the seven gateway to one or more recipient phone numbers. Use to deliver a text notification or alert; supports optional scheduled dispatch (delay), flash SMS, and a custom sender. Each call dispatches a new message, so it is not idempotent \u2014 repeating sends another SMS.",
    idempotent: !1
  },
  props: {
    to: import_pieces_framework2.Property.Array({
      displayName: "To",
      description: "Recipient numbers of the SMS.",
      required: !0
    }),
    delay: import_pieces_framework2.Property.DateTime({
      displayName: "Delay",
      required: !1
    }),
    flash: import_pieces_framework2.Property.Checkbox({
      displayName: "Flash SMS ?",
      required: !1
    }),
    from: import_pieces_framework2.Property.ShortText({
      displayName: "From",
      required: !1
    }),
    text: import_pieces_framework2.Property.LongText({
      displayName: "Message Body",
      description: "The body of the message to send.",
      required: !0
    })
  },
  async run(context) {
    let { delay, flash, from, text, to } = context.propsValue;
    return (await callSevenApi({
      body: {
        delay: delay ? new Date(delay).toISOString().replace("T", " ").substring(0, 19) : void 0,
        flash,
        from,
        text,
        to
      },
      method: import_pieces_common2.HttpMethod.POST
    }, "sms", context.auth.secret_text)).body;
  }
});

// ../ap/packages/pieces/community/seven/src/action/rcs-send.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var sendRcsAction = (0, import_pieces_framework3.createAction)({
  auth: sevenAuth,
  name: "send-rcs",
  displayName: "Send RCS",
  description: "Sends a Rich Communication Services message.",
  audience: "both",
  aiMetadata: {
    description: "Sends a Rich Communication Services (RCS) message via the seven gateway to a single recipient phone number, optionally falling back to SMS or webview if RCS is unavailable. Use when sending a rich messaging payload rather than plain SMS; supports scheduled dispatch (delay), TTL, and performance tracking. Each call dispatches a new message, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    to: import_pieces_framework3.Property.ShortText({
      displayName: "To",
      description: "Recipient phone number for the RCS message (e.g., 49176123456789).",
      required: !0
    }),
    text: import_pieces_framework3.Property.LongText({
      displayName: "Message Body",
      description: "The body of the message to send.",
      required: !0
    }),
    from: import_pieces_framework3.Property.ShortText({
      displayName: "From",
      description: "The unique ID of your RCS agent. If not specified, the first RCS-capable sender will be used.",
      required: !1
    }),
    delay: import_pieces_framework3.Property.DateTime({
      displayName: "Delay",
      description: "Date/time for delayed dispatch. Format: yyyy-mm-dd hh:ii or Unix timestamp.",
      required: !1
    }),
    fallback: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Fallback",
      description: "Alternative channel if RCS is not available. If not specified, fallback is disabled.",
      required: !1,
      options: {
        options: [
          { label: "SMS", value: "sms" },
          { label: "Webview", value: "webview" }
        ]
      }
    }),
    ttl: import_pieces_framework3.Property.Number({
      displayName: "TTL (Time to Live)",
      description: "Validity period of the RCS in minutes. Default is 2880 (48 hours).",
      required: !1
    }),
    label: import_pieces_framework3.Property.ShortText({
      displayName: "Label",
      description: "Label for statistics tracking. Max 100 characters (a-z, A-Z, 0-9, .-_@).",
      required: !1
    }),
    performance_tracking: import_pieces_framework3.Property.Checkbox({
      displayName: "Performance Tracking",
      description: "Activate click and performance tracking for URLs (also enables URL shortener).",
      required: !1
    }),
    foreign_id: import_pieces_framework3.Property.ShortText({
      displayName: "Foreign ID",
      description: "Your own ID for this message (used in callbacks). Max 64 characters (a-z, A-Z, 0-9, .-_@).",
      required: !1
    })
  },
  async run(context) {
    let {
      to,
      text,
      from,
      delay,
      fallback,
      ttl,
      label,
      performance_tracking,
      foreign_id
    } = context.propsValue;
    return (await callSevenApi({
      body: {
        to,
        text,
        from,
        delay: delay ? new Date(delay).toISOString().replace("T", " ").substring(0, 19) : void 0,
        fallback,
        ttl,
        label,
        performance_tracking,
        foreign_id
      },
      method: import_pieces_common3.HttpMethod.POST
    }, "rcs/messages", context.auth.secret_text)).body;
  }
});

// ../ap/packages/pieces/community/seven/src/action/send-voice-call.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var sendVoiceCallAction = (0, import_pieces_framework4.createAction)({
  auth: sevenAuth,
  name: "send-voice-call",
  displayName: "Send Voice Call",
  description: "Creates a new Text-To-Speech call to a number.",
  audience: "both",
  aiMetadata: {
    description: "Places an automated Text-To-Speech voice call via the seven gateway to one or more recipient numbers, reading out the provided message text. Use to deliver a spoken notification rather than a text message; an optional custom sender can be set. Each call initiates a new outbound call, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    to: import_pieces_framework4.Property.ShortText({
      description: "Recipient number(s) of the voice calls.",
      displayName: "To",
      required: !0
    }),
    from: import_pieces_framework4.Property.ShortText({
      displayName: "From",
      required: !1
    }),
    text: import_pieces_framework4.Property.LongText({
      displayName: "Message Body",
      description: "Text message to be read out.",
      required: !0
    })
  },
  async run(context) {
    let { from, text, to } = context.propsValue;
    return (await callSevenApi({
      body: {
        from,
        text,
        to
      },
      method: import_pieces_common4.HttpMethod.POST
    }, "voice", context.auth.secret_text)).body;
  }
});

// ../ap/packages/pieces/community/seven/src/action/lookup.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var lookup = (0, import_pieces_framework5.createAction)({
  auth: sevenAuth,
  name: "lookup",
  displayName: "Lookup Phone Numbers",
  description: "Get information about CNAM, HLR, MNP, RCS capabilities and Number formats.",
  audience: "both",
  aiMetadata: {
    description: "Looks up information about one or more phone numbers via the seven gateway. The lookup type selects what is returned \u2014 CNAM (caller name), HLR (network/status), MNP (number portability), RCS capability, or number format validation. Use to validate or enrich phone numbers before messaging. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    type: import_pieces_framework5.Property.StaticDropdown({
      options: {
        options: [
          { label: "CNAM", value: "cnam" },
          { label: "HLR", value: "hlr" },
          { label: "Format", value: "format" },
          { label: "MNP", value: "mnp" },
          { label: "RCS capabilities", value: "rcs" }
        ]
      },
      displayName: "Type",
      required: !0
    }),
    numbers: import_pieces_framework5.Property.Array({
      description: "The phone numbers to look up.",
      displayName: "Numbers",
      required: !0
    })
  },
  async run(context) {
    let { numbers, type } = context.propsValue;
    return (await callSevenApi({
      queryParams: {
        number: numbers.join(",")
      },
      method: import_pieces_common5.HttpMethod.GET
    }, `lookup/${type}`, context.auth.secret_text)).body;
  }
});

// ../ap/packages/pieces/community/seven/src/trigger/sms-inbound.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), triggerNameInStore = "seven_new_sms_trigger", smsInbound = (0, import_pieces_framework6.createTrigger)({
  auth: sevenAuth,
  description: "Triggers when a new SMS message is received",
  aiMetadata: {
    description: "Fires when an inbound SMS is received at the seven gateway (a mobile-originated sms_mo event), delivering the incoming message. Can optionally be limited to messages from a single sender phone number."
  },
  displayName: "New Incoming SMS",
  name: "new_incoming_sms",
  props: {
    from: import_pieces_framework6.Property.ShortText({
      displayName: "Phone Number",
      description: "Optionally limit inbound SMS to this particular phone number.",
      required: !1
    })
  },
  sampleData: {
    data: {
      id: "681590",
      sender: "SMS",
      system: "491771783130",
      text: "Hello. I am an example for demonstrating a webhook payload.",
      time: "1605878104"
    },
    webhook_event: "sms_mo",
    webhook_timestamp: "2020-12-02 11:55:44"
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let { from = "" } = context.propsValue, { body } = await callSevenApi({
      body: {
        event_filter: from,
        event_type: "sms_mo",
        target_url: context.webhookUrl
      },
      method: import_pieces_common6.HttpMethod.POST
    }, "hooks", context.auth.secret_text);
    body.success && await ((_a = context.store) == null ? void 0 : _a.put(triggerNameInStore, {
      webhookId: body.id
    }));
  },
  async onDisable(context) {
    var _a;
    let info = await ((_a = context.store) == null ? void 0 : _a.get(triggerNameInStore));
    if (!info) return;
    let { body } = await callSevenApi({
      body: {
        action: "unsubscribe",
        id: info.webhookId
      },
      method: import_pieces_common6.HttpMethod.POST
    }, "hooks", context.auth.secret_text);
    body.success && await context.store.put(triggerNameInStore, null);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/seven/src/index.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var seven = (0, import_pieces_framework7.createPiece)({
  displayName: "seven",
  description: "Business Messaging Gateway",
  auth: sevenAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/seven.jpg",
  categories: [import_pieces_framework8.PieceCategory.MARKETING],
  authors: ["seven-io"],
  actions: [sendSmsAction, sendVoiceCallAction, lookup, sendRcsAction],
  triggers: [smsInbound]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  seven
});
