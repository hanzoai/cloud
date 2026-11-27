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

// ../ap/packages/pieces/community/air-ops/src/index.ts
var index_exports = {};
__export(index_exports, {
  airOps: () => airOps,
  airOpsAuth: () => airOpsAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/air-ops/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.airops.com";
async function makeRequest(auth, method, path, body) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      Authorization: `Bearer ${auth}`,
      "Content-Type": "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/air-ops/src/lib/actions/run-workflow.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var runWorkflow = (0, import_pieces_framework.createAction)({
  auth: airOpsAuth,
  name: "run_workflow",
  classification: "WRITE",
  displayName: "Run Workflow",
  description: "Execute an AirOps workflow synchronously.",
  audience: "both",
  aiMetadata: { description: "Run a selected AirOps workflow synchronously and wait for its result, passing optional input values. Use when you need the workflow output back in the same call rather than polling; for long-running workflows prefer the async variant. Requires the target workflow (selected from the account) and triggers a new execution each time, so it is not idempotent.", idempotent: !1 },
  props: {
    app: import_pieces_framework.Property.Dropdown({
      displayName: "Workflow",
      description: "Select the workflow to execute.",
      required: !0,
      refreshers: [],
      auth: airOpsAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your AirOps account first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await makeRequest(
              auth.secret_text,
              import_pieces_common2.HttpMethod.GET,
              "/public_api/airops_apps"
            )).map((app) => ({
              label: app.name,
              value: app.uuid
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load workflows",
            options: []
          };
        }
      }
    }),
    inputs: import_pieces_framework.Property.Json({
      displayName: "Inputs",
      description: "Input values for the workflow.",
      required: !1,
      defaultValue: {}
    }),
    inputs_schema: import_pieces_framework.Property.Json({
      displayName: "Inputs Schema",
      description: "Schema defining the workflow inputs (advanced).",
      required: !1
    }),
    definition: import_pieces_framework.Property.Json({
      displayName: "Definition",
      description: "Custom workflow definition steps (advanced).",
      required: !1
    })
  },
  async run(context) {
    let { app, inputs, inputs_schema, definition } = context.propsValue, body = {};
    return inputs && (body.inputs = inputs), inputs_schema && (body.inputs_schema = inputs_schema), definition && (body.definition = definition), await makeRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      `/public_api/airops_apps/${app}/execute_definition`,
      body
    );
  }
});

// ../ap/packages/pieces/community/air-ops/src/lib/actions/run-workflow-async.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var runWorkflowAsync = (0, import_pieces_framework2.createAction)({
  auth: airOpsAuth,
  name: "run_workflow_async",
  classification: "WRITE",
  displayName: "Run Workflow (Async)",
  description: "Queue an AirOps workflow for asynchronous execution.",
  audience: "both",
  aiMetadata: { description: "Queue a selected AirOps workflow for asynchronous execution and return immediately without waiting for the result. Use for long-running workflows, then retrieve the outcome later with Get Execution; use the synchronous Run Workflow instead when you need the output in the same call. Each call starts a new execution, so it is not idempotent.", idempotent: !1 },
  props: {
    app: import_pieces_framework2.Property.Dropdown({
      displayName: "Workflow",
      description: "Select the workflow to execute.",
      required: !0,
      refreshers: [],
      auth: airOpsAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your AirOps account first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await makeRequest(
              auth.secret_text,
              import_pieces_common3.HttpMethod.GET,
              "/public_api/airops_apps"
            )).map((app) => ({
              label: app.name,
              value: app.uuid
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load workflows",
            options: []
          };
        }
      }
    }),
    inputs: import_pieces_framework2.Property.Json({
      displayName: "Inputs",
      description: "Input values for the workflow.",
      required: !1,
      defaultValue: {}
    }),
    inputs_schema: import_pieces_framework2.Property.Json({
      displayName: "Inputs Schema",
      description: "Schema defining the workflow inputs (advanced).",
      required: !1
    }),
    definition: import_pieces_framework2.Property.Json({
      displayName: "Definition",
      description: "Custom workflow definition steps (advanced).",
      required: !1
    })
  },
  async run(context) {
    let { app, inputs, inputs_schema, definition } = context.propsValue, body = {};
    return inputs && (body.inputs = inputs), inputs_schema && (body.inputs_schema = inputs_schema), definition && (body.definition = definition), await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      `/public_api/airops_apps/${app}/async_execute_definition`,
      body
    );
  }
});

// ../ap/packages/pieces/community/air-ops/src/lib/actions/get-execution.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getExecution = (0, import_pieces_framework3.createAction)({
  auth: airOpsAuth,
  name: "get_execution",
  classification: "READ",
  displayName: "Get Execution",
  description: "Get an execution by UUID.",
  audience: "both",
  aiMetadata: { description: "Retrieve a single workflow execution and its status, inputs, and output by looking up its UUID within a selected AirOps workflow. Use to poll or check the result of an execution started by Run Workflow (Async), or to inspect any past run. Requires both the workflow and the execution UUID, and throws if no matching execution exists. Read-only, so repeated calls are idempotent.", idempotent: !0 },
  props: {
    app: import_pieces_framework3.Property.Dropdown({
      displayName: "Workflow",
      description: "Select the workflow.",
      required: !0,
      refreshers: [],
      auth: airOpsAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your AirOps account first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await makeRequest(
              auth.secret_text,
              import_pieces_common4.HttpMethod.GET,
              "/public_api/airops_apps"
            )).map((app) => ({
              label: app.name,
              value: app.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load workflows",
            options: []
          };
        }
      }
    }),
    execution_uuid: import_pieces_framework3.Property.ShortText({
      displayName: "Execution UUID",
      description: "The UUID of the execution to retrieve.",
      required: !0
    })
  },
  async run(context) {
    let { app, execution_uuid } = context.propsValue, execution = (await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.GET,
      `/public_api/airops_apps/${app}/executions`
    )).data.find((e) => e.uuid === execution_uuid);
    if (!execution)
      throw new Error(`Execution with UUID "${execution_uuid}" not found.`);
    return execution;
  }
});

// ../ap/packages/pieces/community/air-ops/src/index.ts
var airOpsAuth = import_pieces_framework4.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your API key can be found in the **Workspace Settings** section of your AirOps account.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await makeRequest(auth, import_pieces_common5.HttpMethod.GET, "/public_api/airops_apps"), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key."
      };
    }
  }
}), airOps = (0, import_pieces_framework4.createPiece)({
  displayName: "AirOps",
  description: "Build and deploy AI-powered workflows and agents.",
  auth: airOpsAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/air-ops.png",
  categories: [import_pieces_framework5.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["onyedikachi-david"],
  actions: [
    runWorkflow,
    runWorkflowAsync,
    getExecution,
    (0, import_pieces_common5.createCustomApiCallAction)({
      auth: airOpsAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  airOps,
  airOpsAuth
});
