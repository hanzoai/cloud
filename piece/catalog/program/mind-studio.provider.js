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

// ../ap/packages/pieces/community/mind-studio/src/index.ts
var index_exports = {};
__export(index_exports, {
  mindStudio: () => mindStudio,
  mindStudioAuth: () => mindStudioAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mind-studio/src/lib/actions/run-workflow.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var runWorkflowAction = (0, import_pieces_framework.createAction)({
  auth: mindStudioAuth,
  name: "run_workflow",
  displayName: "Run Workflow",
  description: "Run a workflow from your MindStudio app and start a thread.",
  audience: "both",
  aiMetadata: {
    description: "Executes a MindStudio app workflow to get an AI result, identified by App ID (and optionally a specific workflow name within the app). Pass input as key-value variables; optionally supply a callback URL to receive the result asynchronously instead of inline. Use when an agent needs to invoke a prebuilt MindStudio AI app. Not idempotent: each call starts a new thread/execution and may incur billing cost.",
    idempotent: !1
  },
  props: {
    appId: import_pieces_framework.Property.ShortText({
      displayName: "App ID",
      description: "MindStudio app ID to run.",
      required: !0
    }),
    workflow: import_pieces_framework.Property.ShortText({
      displayName: "Workflow",
      description: "Workflow name to run (without the .flow extension).",
      required: !1
    }),
    variables: import_pieces_framework.Property.Json({
      displayName: "Variables",
      description: "Key-value variables passed to the app.",
      required: !1,
      defaultValue: {}
    }),
    callbackUrl: import_pieces_framework.Property.ShortText({
      displayName: "Callback URL",
      description: "URL to receive the execution result asynchronously.",
      required: !1
    }),
    includeBillingCost: import_pieces_framework.Property.Checkbox({
      displayName: "Include Billing Cost",
      description: "Return the billing cost in the response.",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      appId: propsValue.appId
    };
    propsValue.workflow && (body.workflow = propsValue.workflow), propsValue.variables && (body.variables = propsValue.variables), propsValue.callbackUrl && (body.callbackUrl = propsValue.callbackUrl), propsValue.includeBillingCost !== void 0 && (body.includeBillingCost = propsValue.includeBillingCost);
    let request = {
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.mindstudio.ai/developer/v2/apps/run",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/mind-studio/src/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), mindStudioAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your MindStudio API key (Bearer token).",
  required: !0
}), mindStudio = (0, import_pieces_framework2.createPiece)({
  displayName: "MindStudio",
  description: "Run MindStudio workflows and get AI results.",
  auth: mindStudioAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/mind-studio.png",
  authors: ["onyedikachi-david"],
  categories: [import_pieces_framework3.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  actions: [runWorkflowAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mindStudio,
  mindStudioAuth
});
