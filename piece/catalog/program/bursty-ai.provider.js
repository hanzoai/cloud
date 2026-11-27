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

// ../ap/packages/pieces/community/bursty-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  burstyAi: () => burstyAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bursty-ai/src/lib/actions/run-workflow.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bursty-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), burstyAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "API Key for Bursty-ai",
  required: !0
});

// ../ap/packages/pieces/community/bursty-ai/src/lib/actions/run-workflow.ts
var runWorkflow = (0, import_pieces_framework2.createAction)({
  auth: burstyAiAuth,
  name: "runWorkflow",
  classification: "WRITE",
  displayName: "Run Workflow",
  description: "Run a Bursty AI workflow and optionally wait for and return the results",
  audience: "both",
  aiMetadata: {
    description: 'Starts (triggers) a BurstyAI no-code AI workflow by its workflow ID, launching a new asynchronous execution. Use to kick off a BurstyAI content/SEO/outreach automation; enable "Wait for Result" to block and poll until the job completes (returns the result, times out after ~10 minutes), or leave it off to fire-and-return the job handle immediately. Not idempotent: each call starts a separate workflow run.',
    idempotent: !1
  },
  props: {
    workflow_id: import_pieces_framework2.Property.ShortText({
      displayName: "Workflow ID",
      description: "The ID of the Bursty AI workflow to run",
      required: !0
    }),
    get_result: import_pieces_framework2.Property.Checkbox({
      displayName: "Wait for Result",
      description: "Wait for the workflow to complete and return the results (default: false)",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let jobData = (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `https://app.burstyai.com/burstyai/aiflows/${context.propsValue.workflow_id}/async_run`,
      headers: {
        Authorization: `Bearer ${context.auth}`,
        "content-type": "application/json"
      },
      body: {}
    })).body;
    if (!context.propsValue.get_result)
      return jobData;
    let jobId = jobData.jobId || jobData.id;
    if (!jobId)
      throw new Error("No job ID returned from workflow execution");
    let maxAttempts = 30, attempts = 0;
    for (; attempts < maxAttempts; ) {
      let result = (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `https://app.burstyai.com/burstyai/aiflowjobs/${jobId}/result`,
        headers: {
          Authorization: `Bearer ${context.auth}`
        }
      })).body;
      if (result.status === "END" || result.status === "ERROR" || result.status === "DONE")
        return result;
      await new Promise((resolve) => setTimeout(resolve, 5e3)), attempts++;
    }
    throw new Error("Workflow execution timed out after 10 minutes");
  }
});

// ../ap/packages/pieces/community/bursty-ai/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), burstyAi = (0, import_pieces_framework3.createPiece)({
  displayName: "BurstyAI",
  auth: burstyAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/bursty-ai.png",
  categories: [import_pieces_framework4.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  description: "Automate content creation, SEO optimization, email outreach, and influencer partnerships with BurstyAI's no-code AI workflows.",
  authors: ["sanket-a11y"],
  actions: [runWorkflow],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  burstyAi
});
