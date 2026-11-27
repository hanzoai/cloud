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

// ../ap/packages/pieces/community/hystruct/src/index.ts
var index_exports = {};
__export(index_exports, {
  hystruct: () => hystruct
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/hystruct/src/lib/actions/create-job.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/hystruct/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), hystructAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To get your API key:
1. Log in to your Hystruct dashboard
2. Click on your profile picture (top right)
3. Click **Settings**
4. Click **API Keys**
5. Copy and paste your API key here
`,
  required: !0
});

// ../ap/packages/pieces/community/hystruct/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), HYSTRUCT_BASE_URL = "https://api.hystruct.com";
async function hystructApiCall({
  apiKey,
  method,
  endpoint,
  query,
  body
}) {
  let qs = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (qs[key] = String(value));
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${HYSTRUCT_BASE_URL}${endpoint}`,
    headers: {
      "x-api-key": apiKey,
      "Content-Type": "application/json"
    },
    queryParams: qs,
    body
  })).body;
}

// ../ap/packages/pieces/community/hystruct/src/lib/actions/create-job.ts
var createJob = (0, import_pieces_framework2.createAction)({
  auth: hystructAuth,
  name: "create_job",
  displayName: "Create Job",
  description: "Create and queue a new job to run for a workflow",
  audience: "both",
  aiMetadata: { description: "Queues a new job to execute a Hystruct document-structuring/data-extraction workflow, identified by its workflow ID. Use this to kick off a workflow run on demand. Not idempotent: each call enqueues a separate job run.", idempotent: !1 },
  props: {
    workflowId: import_pieces_framework2.Property.ShortText({
      displayName: "Workflow ID",
      description: "The ID of the workflow to run",
      required: !0
    })
  },
  async run(context) {
    let { workflowId } = context.propsValue;
    return await hystructApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common2.HttpMethod.POST,
      endpoint: `/v1/workflows/${workflowId}/queue`
    });
  }
});

// ../ap/packages/pieces/community/hystruct/src/lib/triggers/workflow-event.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var workflowEvent = (0, import_pieces_framework3.createTrigger)({
  auth: hystructAuth,
  name: "workflow_event",
  displayName: "Workflow Event",
  description: "Triggers when a workflow event occurs (e.g., job completed, data updated)",
  aiMetadata: {
    description: "Fires when Hystruct sends a workflow event to the registered webhook, such as a job completing or extracted data being updated; the specific event types are configured in the Hystruct dashboard and can be filtered to a single workflow ID."
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  props: {
    markdown: import_pieces_framework3.Property.MarkDown({
      value: `## Webhook Setup

To receive workflow events, set up a webhook in your Hystruct dashboard:

1. Log in to Hystruct and go to **Integrations**
2. Click **Webhooks** \u2192 **Create webhook**
3. Paste this URL as the Webhook URL:
\`\`\`text
{{webhookUrl}}
\`\`\`
4. Select the events you want to subscribe to
5. Optionally enter a Workflow ID to filter events
6. Click **Save**
`
    })
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  },
  async test() {
    return [
      {
        workflowId: "wf_abc123",
        event: "job.completed",
        data: {
          jobId: "job_xyz789",
          status: "completed",
          completedAt: (/* @__PURE__ */ new Date()).toISOString()
        }
      }
    ];
  },
  sampleData: {
    workflowId: "wf_abc123",
    event: "job.completed",
    data: {
      jobId: "job_xyz789",
      status: "completed",
      completedAt: "2024-01-15T10:30:00Z"
    }
  }
});

// ../ap/packages/pieces/community/hystruct/src/index.ts
var hystruct = (0, import_pieces_framework4.createPiece)({
  displayName: "Hystruct",
  description: "AI-powered document structuring and data extraction",
  auth: hystructAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/hystruct.png",
  categories: [import_pieces_framework5.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["onyedikachi-david"],
  actions: [createJob],
  triggers: [workflowEvent]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  hystruct
});
