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

// ../ap/packages/pieces/community/skyvern/src/index.ts
var index_exports = {};
__export(index_exports, {
  skyvern: () => skyvern
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/skyvern/src/lib/actions/cancel-run.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/skyvern/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/skyvern/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.skyvern.com/v1";
async function skyvernApiCall({
  apiKey,
  method,
  resourceUri,
  query,
  body
}) {
  let qs = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (qs[key] = String(value));
  let request = {
    method,
    url: BASE_URL + resourceUri,
    headers: {
      "x-api-key": apiKey
    },
    queryParams: qs,
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}

// ../ap/packages/pieces/community/skyvern/src/lib/common/auth.ts
var skyvernAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can obtain your API key by navigating to [Settings](https://app.skyvern.com/settings).",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await skyvernApiCall({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: "/workflows"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key."
      };
    }
  }
});

// ../ap/packages/pieces/community/skyvern/src/lib/actions/cancel-run.ts
var cancelRunAction = (0, import_pieces_framework2.createAction)({
  auth: skyvernAuth,
  name: "cancel-run",
  displayName: "Cancel Run",
  description: "Cancels a workflow or or task run by ID.",
  audience: "both",
  aiMetadata: { description: "Cancels an in-progress Skyvern workflow or task run identified by its run ID. Use to stop a run an agent previously started (via Run Agent Task or Run Workflow). Idempotent: cancelling an already-cancelled or finished run converges to the same cancelled state with no additional effect.", idempotent: !0 },
  props: {
    runId: import_pieces_framework2.Property.ShortText({
      displayName: "Workflow/Task Run ID",
      required: !0
    })
  },
  async run(context) {
    let { runId } = context.propsValue;
    return await skyvernApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      resourceUri: `/runs/${runId}/cancel`
    });
  }
});

// ../ap/packages/pieces/community/skyvern/src/lib/actions/find-workflow.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var findWorkflowAction = (0, import_pieces_framework3.createAction)({
  auth: skyvernAuth,
  name: "find-workflow",
  displayName: "Find Workflow",
  description: "Finds workflow based on title.",
  audience: "both",
  aiMetadata: { description: "Searches Skyvern workflows by title, paging through all results and returning every match. Use to resolve a workflow title into its workflow ID before calling Run Workflow. Read-only and idempotent.", idempotent: !0 },
  props: {
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Workflow Title",
      required: !0
    })
  },
  async run(context) {
    let { title } = context.propsValue, hasMore = !0, page = 1, workflows = [];
    do {
      let response = await skyvernApiCall({
        apiKey: context.auth.secret_text,
        method: import_pieces_common4.HttpMethod.GET,
        resourceUri: "/workflows",
        query: {
          page_size: 100,
          page,
          title
        }
      });
      if ((0, import_pieces_framework4.isNil)(response) || !Array.isArray(response)) break;
      workflows.push(...response), hasMore = response.length > 0, page++;
    } while (hasMore);
    return {
      found: workflows.length > 0,
      result: workflows
    };
  }
});

// ../ap/packages/pieces/community/skyvern/src/lib/actions/get-run.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var getRunAction = (0, import_pieces_framework5.createAction)({
  auth: skyvernAuth,
  name: "get-run",
  displayName: "Get Workflow/Task Run",
  description: "Retrieves a workflow or task run by ID.",
  audience: "both",
  aiMetadata: { description: "Retrieves the current status and details of a Skyvern workflow or task run by its run ID. Use to poll a run started via Run Agent Task or Run Workflow until it completes and to read its results. Read-only and idempotent.", idempotent: !0 },
  props: {
    runId: import_pieces_framework5.Property.ShortText({
      displayName: "Workflow/Task Run ID",
      required: !0
    })
  },
  async run(context) {
    let { runId } = context.propsValue;
    return await skyvernApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.GET,
      resourceUri: `/runs/${runId}`
    });
  }
});

// ../ap/packages/pieces/community/skyvern/src/lib/actions/run-agent-task.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var runAgentTaskAction = (0, import_pieces_framework6.createAction)({
  auth: skyvernAuth,
  name: "run-agent-task",
  displayName: "Run Agent Task",
  description: "Runs task with specified prompt.",
  audience: "both",
  aiMetadata: { description: "Launches a Skyvern browser-automation agent task from a natural-language prompt to navigate and act on a website, optionally starting at a given URL and extracting structured data via a schema. Use to have an AI agent perform a web task (browse, fill forms, click) rather than running a predefined workflow. Each call starts a new asynchronous run, so it is not idempotent; the response returns a run identifier you poll separately with Get Workflow/Task Run.", idempotent: !1 },
  props: {
    prompt: import_pieces_framework6.Property.ShortText({
      displayName: "Prompt",
      description: "The goal or task description for Skyvern to accomplish.",
      required: !0
    }),
    url: import_pieces_framework6.Property.ShortText({
      displayName: "URL",
      description: "The starting URL for the task.",
      required: !1
    }),
    engine: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Engine",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Skyvern 1.0", value: "skyvern-1.0" },
          { label: "Skyvern 2.0", value: "skyvern-2.0" },
          { label: "OpenAI CUA", value: "openai-cua" },
          { label: "Anthropic CUA", value: "anthropic-cua" }
        ]
      }
    }),
    webhookUrl: import_pieces_framework6.Property.ShortText({
      displayName: "Webhook Callback URL",
      description: "URL to send task status updates to after a run is finished.",
      required: !1
    }),
    proxyLocation: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Proxy Location",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Residential", value: "RESIDENTIAL" },
          { label: "Spain", value: "RESIDENTIAL_ES" },
          { label: "Ireland", value: "RESIDENTIAL_IE" },
          { label: "United Kingdom", value: "RESIDENTIAL_GB" },
          { label: "India", value: "RESIDENTIAL_IN" },
          { label: "Japan", value: "RESIDENTIAL_JP" },
          { label: "France", value: "RESIDENTIAL_FR" },
          { label: "Germany", value: "RESIDENTIAL_DE" },
          { label: "New Zealand", value: "RESIDENTIAL_NZ" },
          { label: "South Africa", value: "RESIDENTIAL_ZA" },
          { label: "Argentina", value: "RESIDENTIAL_AR" },
          { label: "ISP Proxy", value: "RESIDENTIAL_ISP" },
          { label: "California (US)", value: "US-CA" },
          { label: "New York (US)", value: "US-NY" },
          { label: "Texas (US)", value: "US-TX" },
          { label: "Florida (US)", value: "US-FL" },
          { label: "Washington (US)", value: "US-WA" },
          { label: "No Proxy", value: "NONE" }
        ]
      }
    }),
    maxSteps: import_pieces_framework6.Property.Number({
      displayName: "Max Steps",
      required: !1,
      description: "Maximum number of steps the task can take. Task will fail if it exceeds this number."
    }),
    dataExtractionSchema: import_pieces_framework6.Property.Json({
      displayName: "Data Extraction Schema",
      required: !1,
      description: "The schema for data to be extracted from the webpage."
    })
  },
  async run(context) {
    let { prompt, proxyLocation, url, webhookUrl, dataExtractionSchema, maxSteps, engine } = context.propsValue;
    return await skyvernApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      resourceUri: "/run/tasks",
      body: {
        prompt,
        url,
        engine,
        proxy_location: proxyLocation,
        data_extraction_schema: dataExtractionSchema,
        max_steps: maxSteps,
        webhook_url: webhookUrl
      }
    });
  }
});

// ../ap/packages/pieces/community/skyvern/src/lib/actions/run-workflow.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/skyvern/src/lib/common/props.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var workflowId = import_pieces_framework7.Property.Dropdown({
  auth: skyvernAuth,
  displayName: "Workflow",
  refreshers: [],
  required: !0,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first."
      };
    let hasMore = !0, page = 1, workflows = [];
    do {
      let response = await skyvernApiCall({
        apiKey: auth.secret_text,
        method: import_pieces_common7.HttpMethod.GET,
        resourceUri: "/workflows",
        query: {
          page_size: 100,
          page
        }
      });
      if ((0, import_pieces_framework8.isNil)(response) || !Array.isArray(response)) break;
      workflows.push(...response), hasMore = response.length > 0, page++;
    } while (hasMore);
    return {
      disabled: !1,
      options: workflows.map((workflow) => ({
        label: workflow.title,
        value: workflow.workflow_permanent_id
      }))
    };
  }
}), workflowParams = import_pieces_framework7.Property.DynamicProperties({
  auth: skyvernAuth,
  displayName: "Workflow Params",
  refreshers: ["workflowId"],
  required: !1,
  props: async ({ auth, workflowId: workflowId2 }) => {
    if (!auth || !workflowId2) return {};
    let hasMore = !0, page = 1, matchedWorkflow;
    do {
      let response = await skyvernApiCall({
        apiKey: auth.secret_text,
        method: import_pieces_common7.HttpMethod.GET,
        resourceUri: "/workflows",
        query: {
          page_size: 100,
          page
        }
      });
      if ((0, import_pieces_framework8.isNil)(response) || !Array.isArray(response) || (matchedWorkflow = response.find(
        (workflow) => workflow.workflow_permanent_id === workflowId2
      ), matchedWorkflow)) break;
      hasMore = response.length > 0, page++;
    } while (hasMore);
    if ((0, import_pieces_framework8.isNil)(matchedWorkflow)) return {};
    let fields = {};
    for (let field of matchedWorkflow.workflow_definition.parameters) {
      if (field.parameter_type !== "workflow") continue;
      let { key, workflow_parameter_type, default_value: defaultValue } = field, required = (0, import_pieces_framework8.isNil)(defaultValue), displayName = key;
      if (workflow_parameter_type === "credential_id") {
        let credentials = await skyvernApiCall({
          apiKey: auth.secret_text,
          method: import_pieces_common7.HttpMethod.GET,
          resourceUri: "/credentials",
          query: {
            page_size: 100
          }
        }) || [];
        fields[field.key] = import_pieces_framework7.Property.StaticDropdown({
          displayName,
          required,
          defaultValue,
          options: {
            disabled: !1,
            options: credentials.map((cred) => ({
              label: cred.name,
              value: cred.credential_id
            }))
          }
        });
        continue;
      }
      switch (workflow_parameter_type) {
        case "string":
        case "file_url":
          fields[field.key] = import_pieces_framework7.Property.ShortText({
            displayName,
            required,
            defaultValue
          });
          break;
        case "float":
        case "integer":
          fields[field.key] = import_pieces_framework7.Property.Number({
            displayName,
            required,
            defaultValue
          });
          break;
        case "boolean":
          fields[field.key] = import_pieces_framework7.Property.Checkbox({
            displayName,
            required,
            defaultValue
          });
          break;
        case "json":
          fields[field.key] = import_pieces_framework7.Property.Json({
            displayName,
            required,
            defaultValue
          });
          break;
        default:
          break;
      }
    }
    return fields;
  }
});

// ../ap/packages/pieces/community/skyvern/src/lib/actions/run-workflow.ts
var runWorkflowAction = (0, import_pieces_framework9.createAction)({
  auth: skyvernAuth,
  name: "run-workflow",
  displayName: "Run Workflow",
  description: "Runs the workflow.",
  audience: "both",
  aiMetadata: { description: "Starts a run of an existing Skyvern workflow identified by its workflow ID, passing any workflow parameters. Use when an agent should execute a predefined multi-step automation rather than an ad-hoc prompt-driven task. Each call triggers a new asynchronous run (not idempotent); the response returns a run identifier you poll separately with Get Workflow/Task Run.", idempotent: !1 },
  props: {
    workflowId,
    title: import_pieces_framework9.Property.ShortText({
      displayName: "Workflow Run Title",
      required: !1,
      description: "The title for this workflow run."
    }),
    proxyLocation: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Proxy Location",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Residential", value: "RESIDENTIAL" },
          { label: "Spain", value: "RESIDENTIAL_ES" },
          { label: "Ireland", value: "RESIDENTIAL_IE" },
          { label: "United Kingdom", value: "RESIDENTIAL_GB" },
          { label: "India", value: "RESIDENTIAL_IN" },
          { label: "Japan", value: "RESIDENTIAL_JP" },
          { label: "France", value: "RESIDENTIAL_FR" },
          { label: "Germany", value: "RESIDENTIAL_DE" },
          { label: "New Zealand", value: "RESIDENTIAL_NZ" },
          { label: "South Africa", value: "RESIDENTIAL_ZA" },
          { label: "Argentina", value: "RESIDENTIAL_AR" },
          { label: "ISP Proxy", value: "RESIDENTIAL_ISP" },
          { label: "California (US)", value: "US-CA" },
          { label: "New York (US)", value: "US-NY" },
          { label: "Texas (US)", value: "US-TX" },
          { label: "Florida (US)", value: "US-FL" },
          { label: "Washington (US)", value: "US-WA" },
          { label: "No Proxy", value: "NONE" }
        ]
      }
    }),
    webhookUrl: import_pieces_framework9.Property.ShortText({
      displayName: "Webhook Callback URL",
      required: !1
    }),
    parameters: workflowParams
  },
  async run(context) {
    let { workflowId: workflowId2, webhookUrl, proxyLocation, parameters } = context.propsValue;
    return await skyvernApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.POST,
      resourceUri: "/run/workflows",
      body: {
        workflow_id: workflowId2,
        proxy_location: proxyLocation,
        parameters,
        webhook_url: webhookUrl
      }
    });
  }
});

// ../ap/packages/pieces/community/skyvern/src/index.ts
var skyvern = (0, import_pieces_framework10.createPiece)({
  displayName: "Skyvern",
  auth: skyvernAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/skyvern.jpg",
  authors: ["rimjhimyadav", "kishanprmr"],
  actions: [
    runAgentTaskAction,
    runWorkflowAction,
    cancelRunAction,
    getRunAction,
    findWorkflowAction,
    (0, import_pieces_common9.createCustomApiCallAction)({
      auth: skyvernAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  skyvern
});
