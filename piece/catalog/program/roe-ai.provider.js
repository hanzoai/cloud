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

// ../ap/packages/pieces/community/roe-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  roeAi: () => roeAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/roe-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), roeAiAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "Roe AI Auth",
  description: "Authenticate with Roe AI using your API key",
  props: {
    apiKey: import_pieces_framework.Property.ShortText({
      displayName: "API Key",
      description: "Enter your Roe AI API key",
      required: !0
    }),
    organization_id: import_pieces_framework.Property.ShortText({
      displayName: "Organization ID",
      description: "Enter your Roe AI Organization ID",
      required: !0
    })
  },
  required: !0
});

// ../ap/packages/pieces/community/roe-ai/src/lib/actions/run-query.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), runQuery = (0, import_pieces_framework2.createAction)({
  auth: roeAiAuth,
  name: "runQuery",
  displayName: "Run Query",
  description: "Execute a query synchronously and return results",
  audience: "both",
  aiMetadata: {
    description: "Executes an arbitrary SQL query against the Roe AI database synchronously and returns the results, optionally scoped to a worksheet ID or run with admin privileges. Use to read or manipulate data directly via SQL. Because the query can mutate data and each run executes independently, treat this as not idempotent.",
    idempotent: !1
  },
  props: {
    query: import_pieces_framework2.Property.LongText({
      displayName: "SQL Query",
      description: "SQL query to execute",
      required: !0
    }),
    worksheet_id: import_pieces_framework2.Property.ShortText({
      displayName: "Worksheet ID",
      description: "Optional worksheet ID",
      required: !1
    }),
    use_admin: import_pieces_framework2.Property.Checkbox({
      displayName: "Use Admin Privileges",
      description: "Use admin privileges for the query",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { query, worksheet_id, use_admin } = context.propsValue, { apiKey, organization_id } = context.auth.props, payload = {
      query,
      organization_id
    };
    return worksheet_id && (payload.worksheet_id = worksheet_id), use_admin && (payload.use_admin = use_admin), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.roe-ai.com/v1/database/query",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${apiKey}`
      },
      body: payload
    })).body;
  }
});

// ../ap/packages/pieces/community/roe-ai/src/lib/actions/run-agent.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/roe-ai/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), agentIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: roeAiAuth,
  displayName: "Agent",
  description: "Select Agent ",
  refreshers: [],
  required: !0,
  options: async (propsValue) => {
    var _a, _b;
    let apiKey = (_a = propsValue.auth) == null ? void 0 : _a.props.apiKey, organization_id = (_b = propsValue.auth) == null ? void 0 : _b.props.organization_id;
    if (!apiKey)
      return {
        disabled: !0,
        options: []
      };
    let agents = [], nextUrl = `https://api.roe-ai.com/v1/agents?organization_id=${organization_id}`;
    for (; nextUrl; ) {
      let response = await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: nextUrl,
        headers: {
          Authorization: `Bearer ${apiKey}`
        }
      }), pageAgents = response.body.results;
      agents.push(...pageAgents), nextUrl = response.body.next || null;
    }
    return {
      disabled: !1,
      options: agents.map((agent) => ({
        label: agent.name,
        value: agent.id
      }))
    };
  }
});

// ../ap/packages/pieces/community/roe-ai/src/lib/actions/run-agent.ts
var runAgent = (0, import_pieces_framework4.createAction)({
  auth: roeAiAuth,
  name: "runAgent",
  displayName: "Run Agent",
  description: "Execute an agent with provided inputs and return results immediately",
  audience: "both",
  aiMetadata: {
    description: "Runs a configured Roe AI agent by its agent ID, passing dynamic input fields (text or file inputs the agent expects) and optional metadata, and returns the execution result synchronously. Use to invoke a prebuilt agent rather than write a raw query. Each call triggers a fresh agent run, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    agent_id: agentIdDropdown,
    agent_input: import_pieces_framework4.Property.Object({
      displayName: "Agent Input",
      description: "Dynamic input fields based on agent configuration. Can include text or file inputs.",
      required: !0
    }),
    metadata: import_pieces_framework4.Property.Object({
      displayName: "Metadata",
      description: "Optional metadata as JSON object",
      required: !1
    })
  },
  async run(context) {
    let { agent_id, agent_input, metadata } = context.propsValue, { apiKey, organization_id } = context.auth.props, payload = {
      organization_id
    };
    return agent_input && (payload.agent_input = agent_input), metadata && (payload.metadata = metadata), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `https://api.roe-ai.com/v1/agents/run/${agent_id}`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${apiKey}`
      },
      body: payload
    })).body;
  }
});

// ../ap/packages/pieces/community/roe-ai/src/index.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), roeAi = (0, import_pieces_framework5.createPiece)({
  displayName: "Roe AI",
  auth: roeAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/roe-ai.png",
  authors: ["sanket-a11y"],
  actions: [
    runAgent,
    runQuery,
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: roeAiAuth,
      baseUrl: () => "https://api.roe-ai.com/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.apiKey}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  roeAi
});
