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

// ../ap/packages/pieces/community/simplepdf/src/index.ts
var index_exports = {};
__export(index_exports, {
  simplepdf: () => simplepdf
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/simplepdf/src/lib/triggers/new-submission.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdown = `
- Paste this URL in the webhook integration endpoint:
\`\`\`text
{{webhookUrl}}
\`\`\`
- Click update (keep other settings unchanged)
<br>
<br>

_[Read more about configuring webhooks](https://simplepdf.eu/help/how-to/configure-webhooks-pdf-form-submissions)_
`, simplePDFNewSubmission = (0, import_pieces_framework.createTrigger)({
  name: "new-submission",
  displayName: "New Submission",
  auth: import_pieces_framework.PieceAuth.None(),
  description: "Triggers when a form receives a new submission",
  aiMetadata: {
    description: "Fires when an end user submits a SimplePDF form, delivering the completed PDF submission via webhook. The event represents a single form submission and includes the source document (id and name), the submission (id, submitted_at timestamp, and a URL to the filled PDF), and any context such as environment and customer_id."
  },
  props: {
    md: import_pieces_framework.Property.MarkDown({
      value: markdown
    })
  },
  type: import_pieces_framework.TriggerStrategy.WEBHOOK,
  sampleData: {
    document: {
      id: "b7615a68-9e1f-4eac-bd20-5e80632a4d9e",
      name: "your_document.pdf"
    },
    submission: {
      id: "80146d5b-a068-490f-8eb9-fe393ba11396",
      submitted_at: "2023-06-04T11:54:58.995Z",
      url: "https://cdn.simplepdf.eu/simple-pdf/assets/webhooks-playground.pdf"
    },
    context: {
      environment: "production",
      customer_id: "123"
    }
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    var _a;
    let payloadBody = context.payload.body;
    return [(_a = payloadBody == null ? void 0 : payloadBody.data) != null ? _a : {}];
  }
});

// ../ap/packages/pieces/community/simplepdf/src/index.ts
var simplepdf = (0, import_pieces_framework2.createPiece)({
  displayName: "SimplePDF",
  description: "PDF editing and generation tool",
  auth: import_pieces_framework2.PieceAuth.None(),
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/simplepdf.png",
  authors: ["bendersej", "kishanprmr", "khaledmashaly", "abuaboud"],
  categories: [import_pieces_framework3.PieceCategory.CONTENT_AND_FILES],
  actions: [],
  triggers: [simplePDFNewSubmission]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  simplepdf
});
