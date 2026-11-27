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

// ../ap/packages/pieces/community/captain-data/src/index.ts
var index_exports = {};
__export(index_exports, {
  CAPTAIN_DATA_BASE_URL: () => CAPTAIN_DATA_BASE_URL,
  captainData: () => captainData,
  captainDataAuth: () => captainDataAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/captain-data/src/lib/actions/launch-workflow.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/captain-data/src/lib/common.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var workflowProp = import_pieces_framework.Property.Dropdown({
  auth: captainDataAuth,
  displayName: "Workflow",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await import_pieces_common.httpClient.sendRequest({
      url: `${CAPTAIN_DATA_BASE_URL}/workflows`,
      method: import_pieces_common.HttpMethod.GET,
      headers: {
        Authorization: `x-api-key ${auth.props.apiKey}`,
        "x-project-id": auth.props.projectId
      }
    })).body.map((workflow) => ({ label: workflow.name, value: workflow.uid }))
  } : {
    disabled: !0,
    options: []
  }
});

// ../ap/packages/pieces/community/captain-data/src/lib/actions/launch-workflow.ts
var launchWorkflow = (0, import_pieces_framework2.createAction)({
  // auth: check https://www.activepieces.com/docs/developers/piece-reference/authentication,
  name: "launchWorkflow",
  classification: "WRITE",
  displayName: "Launch a workflow",
  description: "",
  audience: "both",
  aiMetadata: {
    description: "Schedules a run of an existing Captain Data workflow, identified by its workflow UID, optionally supplying input rows, per-step account/parameter configuration, and a start delay. Use this to kick off a scraping/enrichment automation; it creates a new job on each call, so it is not idempotent. Each step requires the account UIDs and step UID matching the workflow definition.",
    idempotent: !1
  },
  auth: captainDataAuth,
  props: {
    workflow: workflowProp,
    jobName: import_pieces_framework2.Property.ShortText({
      displayName: "Job name",
      required: !1
    }),
    inputs: import_pieces_framework2.Property.Json({
      displayName: "Inputs",
      description: "Inputs are the starting point of your workflow.",
      defaultValue: [],
      required: !1
    }),
    steps: import_pieces_framework2.Property.Json({
      displayName: "Steps",
      description: `You need to configure each steps with:
- accounts : The identifiers of the accounts you want to use for this step. The accounts indicated must match the integration used in the step (e.g A Linkedin account for a Search Linkedin People Profile step). You can find the UIDs in the "Integrations' tab on the platform by clicking on a specific account "Action" button (copy account UID).
- accounts_rotation_enabled: (Optional) Whether or not you want to enable the Accounts Rotation feature for this step (only certain Linkedin & Outlook automations are applicable for this feature).
- parameters: The specific parameters for this given step - (Can be empty but is required)
- step_uid: The UID of the step to configure. You can find it in the API Playground, on Captain Data's platform by clicking on "view body to schedule".
.See https://docs.captaindata.co/#36e905b6-3a31-4bcd-8c6f-0eb6093b5a8a for details`,
      defaultValue: [],
      required: !1
    }),
    delay: import_pieces_framework2.Property.Number({
      displayName: "Delay (seconds)",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let payload = {
      job_name: propsValue.jobName,
      inputs: propsValue.inputs,
      steps: propsValue.steps,
      delay: propsValue.delay
    };
    return (await import_pieces_common2.httpClient.sendRequest({
      url: `${CAPTAIN_DATA_BASE_URL}/workflows/${propsValue.workflow}/schedule`,
      method: import_pieces_common2.HttpMethod.POST,
      headers: {
        "Content-Type": "application/json",
        Authorization: `x-api-key ${auth.props.apiKey}`,
        "x-project-id": auth.props.projectId
      },
      body: JSON.stringify(payload)
    })).body;
  }
});

// ../ap/packages/pieces/community/captain-data/src/lib/actions/get-job-results.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getJobResults = (0, import_pieces_framework3.createAction)({
  // auth: check https://www.activepieces.com/docs/developers/piece-reference/authentication,
  name: "getJobResults",
  classification: "READ",
  displayName: "Get job results",
  description: "Get all results for a specific job",
  audience: "both",
  aiMetadata: {
    description: "Retrieves all output records produced by a specific Captain Data job (a single past run of a workflow), identified by its workflow UID and job UID. Use this to read the data a launched workflow has gathered. Read-only and idempotent; results are not paginated.",
    idempotent: !0
  },
  auth: captainDataAuth,
  props: {
    workflow: workflowProp,
    job: import_pieces_framework3.Property.Dropdown({
      auth: captainDataAuth,
      displayName: "Job",
      required: !0,
      refreshers: ["workflow"],
      options: async ({ auth, workflow }) => !auth || !workflow ? {
        disabled: !0,
        options: []
      } : {
        disabled: !1,
        options: (await import_pieces_common3.httpClient.sendRequest({
          url: `${CAPTAIN_DATA_BASE_URL}/workflows/${workflow}/jobs`,
          method: import_pieces_common3.HttpMethod.GET,
          headers: {
            Authorization: `x-api-key ${auth.props.apiKey}`,
            "x-project-id": auth.props.projectId
          }
        })).body.map(
          (job) => ({
            value: job.uid,
            label: job.start_time
          })
        )
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common3.httpClient.sendRequest({
      url: `${CAPTAIN_DATA_BASE_URL}/jobs/${propsValue.job}/results`,
      method: import_pieces_common3.HttpMethod.GET,
      headers: {
        Authorization: `x-api-key ${auth.props.apiKey}`,
        "x-project-id": auth.props.projectId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/captain-data/src/index.ts
var CAPTAIN_DATA_BASE_URL = "https://api.captaindata.co/v3", captainDataAuth = import_pieces_framework4.PieceAuth.CustomAuth({
  required: !0,
  props: {
    apiKey: import_pieces_framework4.PieceAuth.SecretText({
      displayName: "API key",
      required: !0
    }),
    projectId: import_pieces_framework4.Property.ShortText({
      displayName: "Project ID",
      required: !0
    })
  }
}), captainData = (0, import_pieces_framework4.createPiece)({
  displayName: "Captain-data",
  auth: captainDataAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/captain-data.png",
  authors: ["AdamSelene"],
  actions: [
    launchWorkflow,
    getJobResults,
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: captainDataAuth,
      baseUrl: () => CAPTAIN_DATA_BASE_URL,
      authMapping: async (auth) => ({
        Authorization: `x-api-key ${auth.props.apiKey}`,
        "x-project-id": auth.props.projectId
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  CAPTAIN_DATA_BASE_URL,
  captainData,
  captainDataAuth
});
