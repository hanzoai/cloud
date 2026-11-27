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

// ../ap/packages/pieces/community/formspark/src/index.ts
var index_exports = {};
__export(index_exports, {
  formspark: () => formspark
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/formspark/src/lib/triggers/new-submission.trigger.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), liveMarkdown = "**Live URL:**\n```text\n{{webhookUrl}}\n```\ngenerate sample data & triggers published flow.\n\n", markdown = `
To set up the trigger for new form submissions, follow these steps:

1. Go to the "Dashboard" section.
2. Select the form where you want the trigger to occur.
3. Click on the "Settings" section.
4. Find the "Webhook URL" field and paste above URL there.  
6. Click on "Save".
`, newSubmissionTrigger = (0, import_pieces_framework.createTrigger)({
  name: "new-submission",
  displayName: "New Submission",
  auth: import_pieces_framework.PieceAuth.None(),
  description: "Triggers when form receives a new submission.",
  aiMetadata: {
    description: "Fires when a Formspark form receives a new submission, delivered via webhook. The event represents one completed form submission and carries the submitted field values as its payload."
  },
  props: {
    liveMarkdown: import_pieces_framework.Property.MarkDown({
      value: liveMarkdown,
      variant: import_pieces_framework2.MarkdownVariant.BORDERLESS
    }),
    md: import_pieces_framework.Property.MarkDown({
      value: markdown
    })
  },
  type: import_pieces_framework.TriggerStrategy.WEBHOOK,
  sampleData: void 0,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/formspark/src/index.ts
var formspark = (0, import_pieces_framework3.createPiece)({
  displayName: "Formspark",
  auth: import_pieces_framework3.PieceAuth.None(),
  categories: [import_pieces_framework4.PieceCategory.FORMS_AND_SURVEYS],
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/formspark.png",
  authors: ["kishanprmr"],
  actions: [],
  triggers: [newSubmissionTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  formspark
});
