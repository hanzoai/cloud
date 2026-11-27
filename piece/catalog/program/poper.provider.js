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

// ../ap/packages/pieces/community/poper/src/index.ts
var index_exports = {};
__export(index_exports, {
  poper: () => poper
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/poper/src/lib/triggers/new-lead.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), message = `
1. Log in to your [Poper Account](https://app.poper.ai/).
2. Click on the popup for which you want to set up a trigger.
3. On the left-side menu, click on Integrations and search for **Webhook**.
4. Enter an appropriate webhook name and paste the following URL:
  \`\`\`text
  {{webhookUrl}}
  \`\`\`
`, newLead = (0, import_pieces_framework.createTrigger)({
  name: "newLead",
  displayName: "New Lead",
  description: "Triggers when a new lead is obtained from popup.",
  aiMetadata: {
    description: "Fires when a Poper popup captures a new lead, such as a visitor submitting their email or contact details through a popup form. The event represents a single newly collected lead and delivers the submitted form data from the popup."
  },
  props: {
    markdown: import_pieces_framework.Property.MarkDown({
      value: message
    })
  },
  sampleData: {},
  type: import_pieces_framework.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/poper/src/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), poper = (0, import_pieces_framework2.createPiece)({
  displayName: "Poper",
  auth: import_pieces_framework2.PieceAuth.None(),
  minimumSupportedRelease: "0.30.0",
  categories: [import_pieces_framework3.PieceCategory.MARKETING],
  description: "AI Driven Pop-up Builder that can convert visitors into customers,increase subscriber count, and skyrocket sales.",
  logoUrl: "https://cdn.activepieces.com/pieces/poper.png",
  authors: ["thirstycode"],
  actions: [],
  triggers: [newLead]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  poper
});
