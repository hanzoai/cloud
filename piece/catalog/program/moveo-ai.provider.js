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

// ../ap/packages/pieces/community/moveo-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  moveoAi: () => moveoAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/moveo-ai/src/lib/triggers/dialog-expired.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/moveo-ai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), moveoAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Generate an API key in Deploy \u2192 Developer Tools \u2192 API Keys.",
  required: !0
});

// ../ap/packages/pieces/community/moveo-ai/src/lib/triggers/dialog-expired.ts
var dialogExpired = (0, import_pieces_framework2.createTrigger)({
  name: "dialog_expired",
  displayName: "Dialog Expired",
  description: "Triggers when the dialog expires.",
  aiMetadata: {
    description: "Fires when a Moveo conversation (dialog) expires due to inactivity or reaching its session timeout. The event payload represents the expired dialog, including its identifiers and session context, signaling that the conversation has ended without explicit closure."
  },
  auth: moveoAuth,
  props: {
    setupInstructions: import_pieces_framework2.Property.MarkDown({
      value: `
**Quick Setup:**

1. In Moveo: **Deploy \u2192 Developer Tools \u2192 Event notifications**
2. Click **Create event**
3. Select the event **Dialog expired**
4. **URL:** \`{{webhookUrl}}\`
5. (Optional) Set a **Secret** and keep it saved for webhook verification
6. Click **Create**
      `
    })
  },
  sampleData: null,
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    let body = context.payload.body;
    return [typeof body == "string" ? JSON.parse(body) : body];
  }
});

// ../ap/packages/pieces/community/moveo-ai/src/lib/triggers/dialog-insights.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var dialogInsights = (0, import_pieces_framework3.createTrigger)({
  name: "dialog_insights",
  displayName: "Dialog Insights",
  description: "Triggers when the dialog insights are available.",
  aiMetadata: {
    description: "Fires when Moveo finishes computing analytics and insights for a completed conversation (dialog). The event payload represents the dialog insights, such as summary, sentiment, resolution, and other derived metrics for that conversation."
  },
  auth: moveoAuth,
  props: {
    setupInstructions: import_pieces_framework3.Property.MarkDown({
      value: `
**Quick Setup:**

1. In Moveo: **Deploy \u2192 Developer Tools \u2192 Event notifications**
2. Click **Create event**
3. Select the event **Dialog insights**
4. **URL:** \`{{webhookUrl}}\`
5. (Optional) Set a **Secret** and keep it saved for webhook verification
6. Click **Create**
      `
    })
  },
  sampleData: null,
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    let body = context.payload.body;
    return [typeof body == "string" ? JSON.parse(body) : body];
  }
});

// ../ap/packages/pieces/community/moveo-ai/src/lib/triggers/index.ts
var moveoTriggers = [dialogExpired, dialogInsights];

// ../ap/packages/pieces/community/moveo-ai/src/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
var moveoAi = (0, import_pieces_framework4.createPiece)({
  displayName: "Moveo",
  description: "Moveo.AI is a conversational AI platform that powers the toughest conversations through GenAI agents, driving efficiency and financial outcomes.",
  auth: moveoAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/moveo-ai.png",
  categories: [import_pieces_framework5.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["onyedikachi-david"],
  actions: [
    (0, import_pieces_common.createCustomApiCallAction)({
      auth: moveoAuth,
      baseUrl: () => "https://api.moveo.ai/api/v1",
      authMapping: async (auth) => ({
        Authorization: `apikey ${auth.secret_text}`
      }),
      props: {
        queryParams: {
          description: "Most Moveo endpoints require account_slug as a query parameter.",
          defaultValue: {
            account_slug: ""
          }
        }
      }
    })
  ],
  triggers: moveoTriggers
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  moveoAi
});
