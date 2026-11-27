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

// ../ap/packages/pieces/community/leap-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  leapAi: () => leapAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/leap-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), leapAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Leap AI API Key",
  description: `Provide your Leap AI API key. 

**How to get your API key:**
1. Go to [app.tryleap.ai](https://app.tryleap.ai)
2. Click on **Settings** in the left sidebar (bottom)
3. Navigate to the **API** section
4. Click **Create API Key** to generate a new key
5. Copy and paste the key here`,
  required: !0
});

// ../ap/packages/pieces/community/leap-ai/src/lib/actions/get-a-workflow-run.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/leap-ai/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.workflows.tryleap.ai/v2";
async function makeRequest(auth, method, path, body) {
  let url = `${BASE_URL}${path}`;
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url,
    headers: {
      "X-Api-Key": auth,
      "Content-Type": "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/leap-ai/src/lib/actions/get-a-workflow-run.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), getAWorkflowRun = (0, import_pieces_framework2.createAction)({
  auth: leapAiAuth,
  name: "getAWorkflowRun",
  displayName: "Get a Workflow Run",
  description: "Retrieve the status and results of a workflow run",
  audience: "both",
  aiMetadata: {
    description: 'Fetches the current status and results of a single Leap AI workflow run by its run ID. Use to poll a run started by "Run a Workflow" until it completes. Requires the workflow run ID. Read-only and safe to call repeatedly.',
    idempotent: !0
  },
  props: {
    workflow_run_id: import_pieces_framework2.Property.ShortText({
      displayName: "Workflow Run ID",
      description: "The ID of the workflow run to retrieve",
      required: !0
    })
  },
  async run(context) {
    let { workflow_run_id } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.GET,
      `/runs/${workflow_run_id}`
    );
  }
});

// ../ap/packages/pieces/community/leap-ai/src/lib/actions/run-a-workflow.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), runAWorkflow = (0, import_pieces_framework3.createAction)({
  auth: leapAiAuth,
  name: "runAWorkflow",
  displayName: "Run a Workflow",
  description: "Execute a Leap AI workflow and return the run ID",
  audience: "both",
  aiMetadata: {
    description: 'Starts execution of a Leap AI workflow identified by its workflow ID, optionally passing input variables and a webhook URL to receive results on completion. Returns a run ID to track progress; pair with "Get a Workflow Run" to poll for status and results. Each call launches a new run, so it is not idempotent.',
    idempotent: !1
  },
  props: {
    workflow_id: import_pieces_framework3.Property.ShortText({
      displayName: "Workflow ID",
      description: "The ID of the workflow to run",
      required: !0
    }),
    webhook_url: import_pieces_framework3.Property.ShortText({
      displayName: "Webhook URL",
      description: "The URL to which workflow results should be sent on completion (optional)",
      required: !1
    }),
    input: import_pieces_framework3.Property.Object({
      displayName: "Input Variables",
      description: "Variables that the workflow can use globally and their values (optional)",
      required: !1
    })
  },
  async run(context) {
    let { workflow_id, webhook_url, input } = context.propsValue, body = {
      workflow_id
    };
    return webhook_url && (body.webhook_url = webhook_url), input && (body.input = input), await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/runs",
      body
    );
  }
});

// ../ap/packages/pieces/community/leap-ai/src/index.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), leapAi = (0, import_pieces_framework4.createPiece)({
  displayName: "Leap AI",
  auth: leapAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/leap-ai.png",
  categories: [import_pieces_framework5.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  description: "Automate any workflow with AI. Build custom AI automations to scale your marketing, sales, and operations.",
  authors: ["sanket-a11y"],
  actions: [getAWorkflowRun, runAWorkflow],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  leapAi
});
