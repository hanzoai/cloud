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

// ../ap/packages/pieces/community/logsnag/src/index.ts
var index_exports = {};
__export(index_exports, {
  logsnag: () => logsnag,
  logsnagAuth: () => logsnagAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/logsnag/src/lib/actions/create-event.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var createEvent = (0, import_pieces_framework.createAction)({
  auth: logsnagAuth,
  name: "createEvent",
  displayName: "Create Event",
  description: "Creates a new event in LogSnag with the specified channel and details.",
  audience: "both",
  aiMetadata: { description: "Logs a new event to a LogSnag project channel (LogSnag is an event/activity tracking service). Use it to record that something happened \u2014 e.g. a signup, payment, or job completion \u2014 for monitoring or notification feeds. Requires the project name, channel, and an event title; an optional description adds detail. Not idempotent: each call appends a new event entry, so repeating the same input creates duplicate log entries.", idempotent: !1 },
  props: {
    project: import_pieces_framework.Property.ShortText({ displayName: "Project", required: !0 }),
    channel: import_pieces_framework.Property.ShortText({ displayName: "Channel", required: !0 }),
    event: import_pieces_framework.Property.ShortText({ displayName: "Event", required: !0 }),
    description: import_pieces_framework.Property.ShortText({ displayName: "Description", required: !1 })
  },
  async run(context) {
    let { project, channel, event, description } = context.propsValue;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.logsnag.com/v1/log",
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: {
        project,
        channel,
        event,
        description
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/logsnag/src/lib/triggers/new-event-created.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), newEventCreated = (0, import_pieces_framework2.createTrigger)({
  auth: logsnagAuth,
  name: "newEventCreated",
  displayName: "New event created",
  description: "triggers when a new event ic created and logged",
  aiMetadata: {
    description: "Fires when a new event is created and logged in LogSnag for the configured project and channel, delivering the logged event payload. Use it to react in real time to tracked activity such as signups, payments, or other recorded events."
  },
  props: {
    project: import_pieces_framework2.Property.ShortText({ displayName: "Project", required: !0 }),
    channel: import_pieces_framework2.Property.ShortText({ displayName: "Channel", required: !0 }),
    event: import_pieces_framework2.Property.ShortText({ displayName: "Event", required: !0 })
  },
  sampleData: {
    project: "MyProject",
    channel: "MyChannel",
    event: "TestEvent"
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.logsnag.com/v1/webhooks",
      // <-- fictional LogSnag API endpoint,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: {
        event: "event.created",
        target: context.webhookUrl,
        project: context.propsValue.project,
        channel: context.propsValue.channel
      }
    });
    if (response.status === 200 || response.status === 201) {
      let webhook = response.body;
      await context.store.put("_logsnag_webhook", webhook.uuid);
    }
  },
  async onDisable(context) {
    let webhookId = await context.store.get("_logsnag_webhook");
    webhookId && (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.DELETE,
      url: `https://api.logsnag.com/v1/webhooks/${webhookId}/`,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      }
    }), await context.store.delete("_logsnag_webhook"));
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/logsnag/src/index.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), logsnagAuth = import_pieces_framework3.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Your LogSnag API key specific to the project"
}), logsnag = (0, import_pieces_framework3.createPiece)({
  displayName: "LogSnag",
  auth: logsnagAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/logsnag.png",
  authors: [],
  actions: [
    createEvent,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: logsnagAuth,
      baseUrl: () => "https://api.logsnag.com/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newEventCreated]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  logsnag,
  logsnagAuth
});
