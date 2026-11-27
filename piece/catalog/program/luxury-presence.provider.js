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

// ../ap/packages/pieces/community/luxury-presence/src/index.ts
var index_exports = {};
__export(index_exports, {
  luxuryPresence: () => luxuryPresence
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/luxury-presence/src/lib/triggers/new-lead.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/luxury-presence/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), luxuryPresenceAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `Generating and Managing API Keys

API Keys can be generated via the dashboard located here, or by following these steps:

1. Login to your Luxury Presence account: app.luxurypresence.com.
2. Click on your profile icon in the bottom left-hand corner.
3. Navigate to Settings > API Keys > Generate Key.
4. Type in a Key Description for your API key to help easily recognize it later.
5. Click Generate Key.
6. Copy your API Key, and store it in a safe place.

You must have the role of admin in your account to be able to manage API Keys.
`,
  required: !0
});

// ../ap/packages/pieces/community/luxury-presence/src/lib/triggers/new-lead.ts
var import_pieces_common = require("@activepieces/pieces-common"), LUXURY_PRESENCE_API_BASE = "https://api.luxurypresence.com/crm/v1", newLead = (0, import_pieces_framework2.createTrigger)({
  auth: luxuryPresenceAuth,
  name: "newLead",
  displayName: "New Lead",
  description: "",
  aiMetadata: {
    description: "Fires when a new lead is captured in Luxury Presence, representing a prospective client (buyer or seller) who submitted an inquiry or contact form on the agent or brokerage website. The payload contains the newly created lead record from the Luxury Presence CRM."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let apiKey = context.auth.secret_text, url = `${LUXURY_PRESENCE_API_BASE}/webhooks`, body = await (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url,
      headers: {
        Authorization: `Bearer ${apiKey}`,
        "Content-Type": "application/json"
      },
      body: {
        name: "ActivePieces New Lead Webhook",
        url: context.webhookUrl,
        events: ["leads"]
      }
    })).body;
    await context.store.put("webhook_id", body.id);
  },
  async onDisable(context) {
    let apiKey = context.auth.secret_text, webhookId = await context.store.get("webhook_id");
    if (webhookId) {
      let deleteUrl = `${LUXURY_PRESENCE_API_BASE}/webhooks/${encodeURIComponent(
        webhookId
      )}`;
      await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.DELETE,
        url: deleteUrl,
        headers: {
          Authorization: `Bearer ${apiKey}`,
          "Content-Type": "application/json"
        }
      });
    }
    await context.store.delete("webhook_id");
  },
  async run(context) {
    return context.payload.body.eventName !== "leads" ? [] : [context.payload.body];
  }
});

// ../ap/packages/pieces/community/luxury-presence/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), luxuryPresence = (0, import_pieces_framework3.createPiece)({
  displayName: "Luxury Presence",
  auth: luxuryPresenceAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/luxury-presence.png",
  description: "Luxury Presence is a software company designed for real estate agents. Their all-in-one platform combines a CRM, website builder, marketing tools, and more to help agents grow their business and close more deals.",
  categories: [import_pieces_framework4.PieceCategory.SALES_AND_CRM],
  authors: ["sanket-a11y"],
  actions: [],
  triggers: [newLead]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  luxuryPresence
});
