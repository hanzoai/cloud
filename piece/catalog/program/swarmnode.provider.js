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

// ../ap/packages/pieces/community/swarmnode/src/index.ts
var index_exports = {};
__export(index_exports, {
  swarmnode: () => swarmnode
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/swarmnode/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/swarmnode/src/lib/common/constants.ts
var BASE_URL = "https://api.swarmnode.ai/v1";

// ../ap/packages/pieces/community/swarmnode/src/lib/common/auth.ts
var swarmnodeAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "You can obtain your API key from [Settings](https://app.swarmnode.ai/settings/api-keys/).",
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: BASE_URL + "/agents/",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key."
      };
    }
  }
});

// ../ap/packages/pieces/community/swarmnode/src/lib/actions/get-execution.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");
var getExecutionAction = (0, import_pieces_framework2.createAction)({
  name: "get-execution",
  auth: swarmnodeAuth,
  displayName: "Get Execution",
  description: "Gets AI Agent's execution details.",
  audience: "both",
  aiMetadata: { description: "Retrieves the details and result of a single SwarmNode agent execution by its agent executor job ID. Use to look up the status or output of a previously started execution. Read-only and idempotent.", idempotent: !0 },
  props: {
    jobId: import_pieces_framework2.Property.ShortText({
      displayName: "Agent Executor Job ID",
      required: !0
    })
  },
  async run(context) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: BASE_URL + `/executions/${context.propsValue.jobId}/`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/swarmnode/src/index.ts
var import_pieces_common5 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/swarmnode/src/lib/actions/execute-agent.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/swarmnode/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var agentIdDropdown = import_pieces_framework3.Property.Dropdown({
  displayName: "Agent",
  refreshers: [],
  auth: swarmnodeAuth,
  required: !0,
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: BASE_URL + "/agents/",
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      }
    })).body.results.map((agent) => ({
      label: agent.name,
      value: agent.id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please authenticate first"
  }
});

// ../ap/packages/pieces/community/swarmnode/src/lib/actions/execute-agent.ts
var import_pieces_common4 = require("@activepieces/pieces-common");
var executeAgentAction = (0, import_pieces_framework4.createAction)({
  name: "execute-agent",
  auth: swarmnodeAuth,
  displayName: "Execute Agent",
  description: "Executes an existing AI agent.",
  audience: "both",
  aiMetadata: { description: "Runs an existing SwarmNode AI agent (selected by agent ID) with a JSON input payload, then polls until the execution completes and returns its result. Use to trigger an agent and get its output in one step; the call blocks until success or times out after ~5 minutes. Each call starts a new execution, so it is not idempotent.", idempotent: !1 },
  props: {
    agentId: agentIdDropdown,
    payload: import_pieces_framework4.Property.Json({
      displayName: "Agent Input Payload",
      required: !0
    })
  },
  async run(context) {
    let { agentId, payload } = context.propsValue, jobId = (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: BASE_URL + "/agent-executor-jobs/create/",
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        agent_id: agentId,
        payload
      }
    })).body.id, status = "in_progress", timeoutAt = Date.now() + 300 * 1e3;
    for (; status != "success" && Date.now() < timeoutAt; ) {
      await new Promise((resolve) => setTimeout(resolve, 5e3));
      let pollResponse = await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.GET,
        url: BASE_URL + "/executions/",
        authentication: {
          type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
          token: context.auth.secret_text
        },
        queryParams: {
          agent_executor_job_id: jobId,
          agent_id: agentId
        }
      });
      if (status = pollResponse.body.results[0].status, status === "success") return pollResponse.body.results[0];
    }
    throw new Error("Execution Job timed out or failed.");
  }
});

// ../ap/packages/pieces/community/swarmnode/src/index.ts
var swarmnode = (0, import_pieces_framework5.createPiece)({
  displayName: "SwarmNode",
  auth: swarmnodeAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/swarmnode.png",
  authors: ["kishanprmr"],
  actions: [
    executeAgentAction,
    getExecutionAction,
    (0, import_pieces_common5.createCustomApiCallAction)({
      auth: swarmnodeAuth,
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
  swarmnode
});
