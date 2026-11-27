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

// ../ap/packages/pieces/community/anyhook-graphql/src/index.ts
var index_exports = {};
__export(index_exports, {
  anyHookGraphql: () => anyHookGraphql
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/anyhook-graphql/src/lib/common/common.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), graphqlCommon = {
  connectionType: "graphql",
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

// ../ap/packages/pieces/community/anyhook-graphql/src/lib/triggers/graphql-subscription-trigger.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/anyhook-graphql/src/lib/triggers/helpers.ts
async function createGraphQLSubscription(websocketUrl, headers, query, webhookUrl, proxyBaseUrl) {
  let subscriptionApiUrl = `${proxyBaseUrl}/subscribe`, data = {
    connection_type: graphqlCommon.connectionType,
    webhook_url: webhookUrl,
    args: {
      endpoint_url: websocketUrl,
      headers: headers ? JSON.stringify(headers) : void 0,
      query
    }
  };
  return await graphqlCommon.apiCall(subscriptionApiUrl, "POST", data);
}
async function deleteGraphQLSubscription(subscriptionId, proxyBaseUrl) {
  let deleteApiUrl = `${proxyBaseUrl}/unsubscribe`, data = {
    subscription_id: subscriptionId
  };
  return await graphqlCommon.apiCall(deleteApiUrl, "POST", data);
}

// ../ap/packages/pieces/community/anyhook-graphql/src/lib/triggers/graphql-subscription-trigger.ts
var graphqlSubscriptionTrigger = (0, import_pieces_framework2.createTrigger)({
  auth: graphqlCommon.auth,
  name: "graphql_subscription_trigger",
  classification: "READ",
  displayName: "New GraphQL Subscription Event",
  description: "Triggers on a new GraphQL subscription event",
  aiMetadata: {
    description: "Fires whenever the subscribed GraphQL subscription pushes a new event over its websocket connection. Each fire delivers one event payload matching the configured subscription query, representing a real-time update streamed from the GraphQL server (for example a new blockchain block or transaction). Use this to react to live data the moment the GraphQL endpoint emits it."
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  props: {
    websocketUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Endpoint URL",
      description: "The GraphQL websocket to connect to.",
      required: !0,
      defaultValue: "wss://streaming.bitquery.io/graphql?token=xxxx"
    }),
    headers: import_pieces_framework2.Property.Object({
      displayName: "Headers",
      description: "Custom headers for the GraphQL connection, such as authentication tokens.",
      required: !1
    }),
    query: import_pieces_framework2.Property.LongText({
      displayName: "GraphQL Query",
      description: "GraphQL subscription query to listen to events",
      required: !0,
      defaultValue: "subscription { EVM(network: eth, trigger_on: head) { Blocks { Block { BaseFee BaseFeeInUSD Bloom Coinbase } } } }"
    })
  },
  async onEnable(context) {
    let subscriptionInfo = await createGraphQLSubscription(
      context.propsValue.websocketUrl,
      context.propsValue.headers,
      context.propsValue.query,
      context.webhookUrl,
      context.auth.props.proxyBaseUrl
    );
    await context.store.put("graphql_subscription_trigger", subscriptionInfo.subscriptionId);
  },
  async onDisable(context) {
    let subscriptionId = await context.store.get("graphql_subscription_trigger");
    subscriptionId && (await deleteGraphQLSubscription(subscriptionId, context.auth.props.proxyBaseUrl), await context.store.delete("graphql_subscription_trigger"));
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    return [
      {
        event: "GraphQL event data",
        details: "Details of the event"
      }
    ];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/anyhook-graphql/src/index.ts
var anyHookGraphql = (0, import_pieces_framework3.createPiece)({
  displayName: "AnyHook GraphQL",
  description: "AnyHook GraphQL enables real-time communication through AnyHook proxy server by allowing you to subscribe and listen to GraphQL subscription events",
  auth: graphqlCommon.auth,
  minimumSupportedRelease: "0.20.0",
  logoUrl: "https://cdn.activepieces.com/pieces/anyhook-graphql.png",
  authors: ["ahmad-swanblocks"],
  actions: [],
  triggers: [
    graphqlSubscriptionTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  anyHookGraphql
});
