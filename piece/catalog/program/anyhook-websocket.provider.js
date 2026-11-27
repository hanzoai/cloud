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

// ../ap/packages/pieces/community/anyhook-websocket/src/index.ts
var index_exports = {};
__export(index_exports, {
  anyHookWebsocket: () => anyHookWebsocket
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/anyhook-websocket/src/lib/common/common.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), websocketCommon = {
  connectionType: "websocket",
  auth: import_pieces_framework.PieceAuth.CustomAuth({
    required: !0,
    props: {
      proxyBaseUrl: import_pieces_framework.Property.ShortText({
        displayName: "AnyHook Server URL",
        description: "The URL of your AnyHook server",
        required: !0,
        defaultValue: "http://10.0.0.101:3001"
      })
    }
  }),
  apiCall: async function(url, method, data = void 0) {
    return (await fetch(url, {
      method,
      headers: {
        "Content-Type": "application/json"
      },
      body: JSON.stringify(data)
    })).json();
  }
};

// ../ap/packages/pieces/community/anyhook-websocket/src/lib/triggers/websocket-subscription-trigger.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/anyhook-websocket/src/lib/triggers/helpers.ts
async function createWebsocketSubscription(websocketUrl, headers, message, webhookUrl, proxyBaseUrl) {
  let subscriptionApiUrl = `${proxyBaseUrl}/subscribe`, data = {
    connection_type: websocketCommon.connectionType,
    webhook_url: webhookUrl,
    args: {
      endpoint_url: websocketUrl,
      headers: headers ? JSON.stringify(headers) : void 0,
      message
    }
  };
  return await websocketCommon.apiCall(subscriptionApiUrl, "POST", data);
}
async function deleteWebsocketSubscription(subscriptionId, proxyBaseUrl) {
  let deleteApiUrl = `${proxyBaseUrl}/unsubscribe`, data = {
    subscription_id: subscriptionId
  };
  return await websocketCommon.apiCall(deleteApiUrl, "POST", data);
}

// ../ap/packages/pieces/community/anyhook-websocket/src/lib/triggers/websocket-subscription-trigger.ts
var websocketSubscriptionTrigger = (0, import_pieces_framework2.createTrigger)({
  auth: websocketCommon.auth,
  name: "websocket_subscription_trigger",
  classification: "READ",
  displayName: "New Websocket Subscription Event",
  description: "Triggers on a new Websocket subscription event",
  aiMetadata: {
    description: "Fires whenever the subscribed WebSocket server pushes a new message to the connection opened by this trigger. The event represents the raw message payload streamed from the endpoint after the subscription message was sent, enabling real-time reaction to live data feeds such as blockchain, market, or collection event streams."
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  props: {
    websocketUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Endpoint URL",
      description: "Websocket endpoint URL to connect to.",
      required: !0,
      defaultValue: "wss://testnets-stream.openseabeta.com/socket/websocket?token=xxxx"
    }),
    headers: import_pieces_framework2.Property.Object({
      displayName: "Headers",
      description: "Custom headers for the Websocket connection, such as authentication tokens.",
      required: !1
    }),
    message: import_pieces_framework2.Property.Json({
      displayName: "Subscription Message",
      description: "The message to send to the Websocket server to subscribe to events.",
      required: !0,
      defaultValue: {
        topic: "collection:boredapeyachtclub",
        event: "phx_join",
        payload: {},
        ref: 0
      }
    })
  },
  async onEnable(context) {
    let subscriptionInfo = await createWebsocketSubscription(
      context.propsValue.websocketUrl,
      context.propsValue.headers,
      context.propsValue.message,
      context.webhookUrl,
      context.auth.props.proxyBaseUrl
    );
    await context.store.put("websocket_subscription_trigger", subscriptionInfo.subscriptionId);
  },
  async onDisable(context) {
    let subscriptionId = await context.store.get("websocket_subscription_trigger");
    subscriptionId && (await deleteWebsocketSubscription(subscriptionId, context.auth.props.proxyBaseUrl), await context.store.delete("websocket_subscription_trigger"));
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    return [
      {
        event: "Websocket event data",
        details: "Details of the event"
      }
    ];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/anyhook-websocket/src/index.ts
var anyHookWebsocket = (0, import_pieces_framework3.createPiece)({
  displayName: "AnyHook Websocket",
  description: "AnyHook Websocket enables real-time communication through AnyHook proxy server by allowing you to subscribe and listen to websocket events",
  auth: websocketCommon.auth,
  minimumSupportedRelease: "0.20.0",
  logoUrl: "https://cdn.activepieces.com/pieces/anyhook-websocket.png",
  authors: ["Swanblocks/Ahmad Shawar"],
  actions: [],
  triggers: [
    websocketSubscriptionTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  anyHookWebsocket
});
