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

// ../ap/packages/pieces/community/gravityforms/src/index.ts
var index_exports = {};
__export(index_exports, {
  gravityforms: () => gravityforms
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/gravityforms/src/lib/triggers/new-submission.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdown = `
- Go to the "Plugins" section.
- Find and click on the "Webhook" plugin to activate it.
- Now, locate the form where you want the trigger to occur.
- Add a webhook to that form.
- In the webhook settings, paste this URL: 
  \`\`\`text
  {{webhookUrl}}
  \`\`\`

- Keep the other settings unchanged (default).
`, gravityFormsNewSubmission = (0, import_pieces_framework.createTrigger)({
  name: "new-submission",
  displayName: "New Submission",
  auth: import_pieces_framework.PieceAuth.None(),
  description: "Triggers when form receives a new submission",
  aiMetadata: {
    description: "Fires when a Gravity Forms form on the connected WordPress site receives a new submission, delivered via the form's configured webhook. Each event represents one completed form entry and carries the submitted field values as its payload."
  },
  props: {
    md: import_pieces_framework.Property.MarkDown({
      value: markdown
    })
  },
  type: import_pieces_framework.TriggerStrategy.WEBHOOK,
  sampleData: {},
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload];
  }
});

// ../ap/packages/pieces/community/gravityforms/src/index.ts
var gravityforms = (0, import_pieces_framework2.createPiece)({
  displayName: "Gravity Forms",
  description: "Build and publish your WordPress forms",
  auth: import_pieces_framework2.PieceAuth.None(),
  minimumSupportedRelease: "0.27.1",
  logoUrl: "https://cdn.activepieces.com/pieces/gravityforms.svg",
  authors: ["Abdallah-Alwarawreh", "kishanprmr", "MoShizzle", "abuaboud"],
  categories: [import_pieces_framework3.PieceCategory.FORMS_AND_SURVEYS],
  actions: [],
  triggers: [gravityFormsNewSubmission]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  gravityforms
});
