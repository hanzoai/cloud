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

// ../ap/packages/pieces/community/copy-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  copyAi: () => copyAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/copy-ai/src/lib/actions/run-workflow.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/copy-ai/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.copy.ai/api";
async function makeRequest(auth, method, path, body) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      "x-copy-ai-api-key": `${auth}`,
      "Content-Type": "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/copy-ai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = `
To use Copy AI, you need to get an API key:
1. Login to your account at https://copy.ai.
2. Click on Workflows in the left sidebar.
3. Click on any Workflow you have. You need to create a new Workflow, if you don't have one.
4. Click on the API tab.
5. Click the Copy button below WORKSPACE API KEY.
`, copyAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0
});

// ../ap/packages/pieces/community/copy-ai/src/lib/actions/run-workflow.ts
var runWorkflowAction = (0, import_pieces_framework2.createAction)({
  auth: copyAiAuth,
  name: "run_workflow",
  displayName: "Run Workflow",
  description: "Start a Copy.ai workflow execution.",
  audience: "both",
  aiMetadata: { description: "Starts a new execution of a Copy.ai workflow by its workflow ID, passing the supplied inputs as the run's start variables. Use this to kick off content generation or any configured Copy.ai workflow; it returns a run ID you then poll with Get Workflow Run Status and fetch results from with Get Workflow Run Outputs. Not idempotent: each call launches a separate run.", idempotent: !1 },
  props: {
    workflowId: import_pieces_framework2.Property.ShortText({
      displayName: "Workflow ID",
      description: "The ID of the workflow to run.",
      required: !0
    }),
    inputs: import_pieces_framework2.Property.Object({
      displayName: "Workflow Inputs",
      description: "The input data for the workflow.",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    return {
      runId: (await makeRequest(
        auth.secret_text,
        import_pieces_common2.HttpMethod.POST,
        `/workflow/${propsValue.workflowId}/run`,
        {
          startVariables: propsValue.inputs
        }
      )).data.id
    };
  }
});

// ../ap/packages/pieces/community/copy-ai/src/lib/actions/get-workflow-run-status.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getWorkflowRunStatusAction = (0, import_pieces_framework3.createAction)({
  auth: copyAiAuth,
  name: "get_workflow_run_status",
  displayName: "Get Workflow Run Status",
  description: "Retrieves the status of a workflow execution.",
  audience: "both",
  aiMetadata: { description: "Looks up the current status of a single Copy.ai workflow run, identified by its workflow ID and run ID. Use this to poll a run started by Run Workflow until it completes before fetching its outputs. Read-only and idempotent.", idempotent: !0 },
  props: {
    workflowId: import_pieces_framework3.Property.ShortText({
      displayName: "Workflow ID",
      required: !0
    }),
    runId: import_pieces_framework3.Property.ShortText({
      displayName: "Run ID",
      description: "The ID of the workflow run to check.",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    return {
      status: (await makeRequest(
        auth.secret_text,
        import_pieces_common3.HttpMethod.GET,
        `/workflow/${propsValue.workflowId}/run/${propsValue.runId}`
      )).data.status
    };
  }
});

// ../ap/packages/pieces/community/copy-ai/src/lib/actions/get-workflow-run-outputs.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getWorkflowRunOutputsAction = (0, import_pieces_framework4.createAction)({
  auth: copyAiAuth,
  name: "get_workflow_run_outputs",
  displayName: "Get Workflow Run Outputs",
  description: "Retrieves the outputs of a completed workflow run.",
  audience: "both",
  aiMetadata: { description: "Fetches the generated outputs of a Copy.ai workflow run, identified by its workflow ID and run ID. Use this once a run has completed (confirm via Get Workflow Run Status) to retrieve the produced content. Read-only and idempotent.", idempotent: !0 },
  props: {
    workflowId: import_pieces_framework4.Property.ShortText({
      displayName: "Workflow ID",
      required: !0
    }),
    runId: import_pieces_framework4.Property.ShortText({
      displayName: "Run ID",
      description: "The ID of the workflow run to get outputs from.",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    var _a;
    let response = await makeRequest(
      auth.secret_text,
      import_pieces_common4.HttpMethod.GET,
      `/workflow/${propsValue.workflowId}/run/${propsValue.runId}`
    );
    return ((_a = response.data) == null ? void 0 : _a.output) || response;
  }
});

// ../ap/packages/pieces/community/copy-ai/src/lib/triggers/workflow-run-completed.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var workflowRunCompletedTrigger = (0, import_pieces_framework5.createTrigger)({
  auth: copyAiAuth,
  name: "workflow_run_completed",
  displayName: "Workflow Run Completed",
  description: "Triggered when a workflow run is completed.",
  aiMetadata: {
    description: "Fires via webhook when an execution of the specified Copy.ai workflow finishes, delivering the completed run including its status, inputs, and generated outputs. Scoped to the single workflow ID configured on the trigger."
  },
  props: {
    workflowId: import_pieces_framework5.Property.ShortText({
      displayName: "Workflow ID",
      required: !0
    })
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  sampleData: {
    status: "COMPLETE",
    input: {
      should_run: "Test"
    },
    toolKey: null,
    metadata: {
      webapp: !0
    },
    error: null,
    createdAt: "2025-05-07T09:31:40.545Z",
    id: "WRUN-7515d814-3890-4ba0-ae32-fa6abcf76432",
    workflowRunId: "WRUN-7515d814-3890-4ba0-ae32-fa6abcf76432",
    workflowId: "WCFG-506c46fb-6459-4e23-979b-875444170626",
    credits: 1,
    output: {
      final_output: "",
      send_api_request: "{}"
    },
    type: "workflowRun.completed"
  },
  async onEnable(context) {
    let response = await makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      "/webhook",
      {
        url: context.webhookUrl,
        eventType: "workflowRun.completed",
        workflowId: context.propsValue.workflowId
      }
    );
    await context.store.put("workflow_run_completed", { webhookId: response.data.id });
  },
  async onDisable(context) {
    let response = await context.store.get("workflow_run_completed");
    !(0, import_pieces_framework6.isNil)(response) && !(0, import_pieces_framework6.isNil)(response.webhookId) && await makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.DELETE,
      `/webhook/${response.webhookId}`,
      {}
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/copy-ai/src/index.ts
var copyAi = (0, import_pieces_framework7.createPiece)({
  displayName: "Copy.ai",
  description: "AI-powered content generation and copywriting platform",
  logoUrl: "https://cdn.activepieces.com/pieces/copy-ai.png",
  authors: ["AnkitSharmaOnGithub"],
  auth: copyAiAuth,
  actions: [runWorkflowAction, getWorkflowRunStatusAction, getWorkflowRunOutputsAction],
  triggers: [workflowRunCompletedTrigger],
  categories: [import_pieces_framework8.PieceCategory.ARTIFICIAL_INTELLIGENCE]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  copyAi
});
