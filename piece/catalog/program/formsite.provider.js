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

// ../ap/packages/pieces/community/formsite/src/index.ts
var index_exports = {};
__export(index_exports, {
  formsite: () => formsite,
  formsiteAuth: () => formsiteAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/formsite/src/lib/triggers/new-form-result.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), newFormResult = (0, import_pieces_framework.createTrigger)({
  auth: formsiteAuth,
  name: "newFormResult",
  displayName: "New Form Result",
  description: "Trigger when a new form result is submitted",
  aiMetadata: {
    description: "Fires when a form submission is received via the configured Formsite Server Post webhook. Represents a single completed form result, including submission metadata (status, start/finish timestamps, user IP, browser, OS, device) and the answered form items."
  },
  props: {
    instruction: import_pieces_framework.Property.MarkDown({
      value: `
  **Enable Basic Authentication:**
  1. Login to your Formbricks account
  2. Open the form you want to connect
  3. Change tab to *From Settings* -> *Integrations* -> *Server Post*
  4. Enable *Server Post* and set the URL to the webhook URL generated after you save the trigger.
  \`\`\`text
			{{webhookUrl}}
			\`\`\`
5. Select Message format as *JSON*
6. Save the settings
`
    })
  },
  sampleData: {
    user_ip: "::1",
    date_start: "2025-12-15T12:59:48.416Z",
    user_referrer: "N/A",
    user_os: "Windows (deprecated)",
    result_status: "Complete",
    date_finish: "2025-12-15T12:59:51.695Z",
    date_update: "2025-12-15T12:59:51.701Z",
    user_browser: "Chrome",
    id: "19086671",
    items: [
      {
        id: "0",
        position: 0,
        value: "dasas"
      },
      {
        values: [
          {
            position: 0,
            value: "Choice A"
          }
        ],
        id: "1",
        position: 1
      }
    ],
    user_device: "Desktop"
  },
  type: import_pieces_framework.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/formsite/src/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), formsiteAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Formsite API Key",
  required: !0
}), formsite = (0, import_pieces_framework2.createPiece)({
  displayName: "Formsite",
  auth: import_pieces_framework2.PieceAuth.None(),
  description: "Formsite is an online form builder that allows you to create forms and surveys easily.",
  categories: [import_pieces_framework3.PieceCategory.SALES_AND_CRM],
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/formsite.png",
  authors: ["sanket-a11y"],
  actions: [],
  triggers: [newFormResult]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  formsite,
  formsiteAuth
});
