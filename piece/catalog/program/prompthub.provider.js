"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/prompthub/src/index.ts
var index_exports = {};
__export(index_exports, {
  prompthub: () => prompthub,
  prompthubAuth: () => prompthubAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/prompthub/src/lib/actions/list-projects.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/prompthub/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var PromptHubClient = class {
  constructor(apiKey, options) {
    __publicField(this, "apiKey", apiKey);
    __publicField(this, "baseUrl");
    __publicField(this, "timeoutMs");
    __publicField(this, "maxRetries");
    __publicField(this, "initialBackoffMs");
    var _a, _b, _c, _d;
    this.baseUrl = ((_a = options == null ? void 0 : options.baseUrl) != null ? _a : "https://app.prompthub.us/api/v1").replace(/\/$/, ""), this.timeoutMs = (_b = options == null ? void 0 : options.timeoutMs) != null ? _b : 3e4, this.maxRetries = (_c = options == null ? void 0 : options.maxRetries) != null ? _c : 3, this.initialBackoffMs = (_d = options == null ? void 0 : options.initialBackoffMs) != null ? _d : 500;
  }
  async validateToken() {
    try {
      return await this.get("/me"), !0;
    } catch (e) {
      if ((e == null ? void 0 : e.statusCode) === 401 || (e == null ? void 0 : e.statusCode) === 403) return !1;
      throw e;
    }
  }
  async getMe() {
    return this.get("/me");
  }
  async listProjects(teamId, query) {
    return this.get(`/teams/${teamId}/projects`, query);
  }
  async getProjectHead(projectId, query) {
    return this.get(`/projects/${projectId}/head`, query);
  }
  async runProject(projectId, body, timeoutMs) {
    return this.post(`/projects/${projectId}/run`, body, timeoutMs);
  }
  async get(path, query) {
    let url = `${this.baseUrl}${path}`, req = {
      method: import_pieces_common.HttpMethod.GET,
      url,
      queryParams: this.buildQuery(query),
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.apiKey
      },
      timeout: this.timeoutMs,
      headers: {
        Accept: "application/json"
      }
    };
    return this.sendWithRetry(req);
  }
  async post(path, body, timeoutOverrideMs) {
    let url = `${this.baseUrl}${path}`, req = {
      method: import_pieces_common.HttpMethod.POST,
      url,
      body,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.apiKey
      },
      timeout: timeoutOverrideMs != null ? timeoutOverrideMs : this.timeoutMs,
      headers: {
        "Content-Type": "application/json",
        Accept: "application/json"
      }
    };
    return this.sendWithRetry(req);
  }
  async sendWithRetry(request) {
    var _a, _b;
    let attempt = 0, lastError;
    for (; attempt <= this.maxRetries; )
      try {
        let res = await import_pieces_common.httpClient.sendRequest(request), status = (_a = res.status) != null ? _a : 200;
        if (status >= 200 && status < 300)
          return res.body;
        let error = new Error(`PromptHub API error ${status}`);
        throw error.statusCode = status, error.body = res.body, error;
      } catch (err) {
        lastError = err;
        let status = (_b = err == null ? void 0 : err.statusCode) != null ? _b : err == null ? void 0 : err.status, isRateLimit = status === 429, isAuth = status === 401 || status === 403, isServer = status >= 500 && status < 600;
        if (isAuth)
          throw err.message = "Unauthorized or forbidden. Check PromptHub token and permissions (team/project access).", err;
        if (!(isRateLimit || isServer))
          throw err;
        if (attempt === this.maxRetries)
          break;
        let backoff = this.exponentialBackoffWithJitter(attempt);
        await this.sleep(backoff), attempt++;
      }
    throw lastError;
  }
  buildQuery(query) {
    if (!query) return;
    let out = {};
    return Object.entries(query).forEach(([k, v]) => {
      v != null && (out[k] = String(v));
    }), out;
  }
  exponentialBackoffWithJitter(attempt) {
    let base = this.initialBackoffMs * Math.pow(2, attempt), jitter = Math.floor(Math.random() * this.initialBackoffMs);
    return base + jitter;
  }
  sleep(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }
};

// ../ap/packages/pieces/community/prompthub/src/lib/common/props.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), listProjectsProps = {
  teamId: import_pieces_framework.Property.Number({
    displayName: "Team ID",
    description: "The ID of the team. Can be found in your browser's URL bar when viewing Team Settings, or use current_team_id from /me endpoint.",
    required: !0
  }),
  group: import_pieces_framework.Property.ShortText({
    displayName: "Group Name",
    description: "Filter projects from a specific group. Must be URL-encoded if the group name contains spaces.",
    required: !1
  }),
  model: import_pieces_framework.Property.ShortText({
    displayName: "Model",
    description: "Filter projects where the head uses a specific model (e.g., gpt-4, claude-2, etc.).",
    required: !1
  }),
  provider: import_pieces_framework.Property.StaticDropdown({
    displayName: "Provider",
    description: "Filter projects where the head uses a specific model provider.",
    required: !1,
    options: {
      options: [
        { label: "OpenAI", value: "OpenAI" },
        { label: "Anthropic", value: "Anthropic" },
        { label: "Azure", value: "Azure" },
        { label: "Google", value: "Google" },
        { label: "Amazon", value: "Amazon" }
      ]
    }
  })
}, getProjectHeadProps = {
  projectId: import_pieces_framework.Property.Number({
    displayName: "Project ID",
    description: "The ID of the project. Can be found in your browser's URL bar when viewing that project.",
    required: !0
  }),
  branch: import_pieces_framework.Property.ShortText({
    displayName: "Branch",
    description: "Use the head of a specific branch. Defaults to your project's master/main branch. Must be URL-encoded if the branch name contains spaces or special characters.",
    required: !1
  }),
  fallback: import_pieces_framework.Property.Checkbox({
    displayName: "Use Fallback",
    description: "When enabled, any placeholders not provided in variables will fall back to your Project/Team defaults. Your variable overrides always take precedence.",
    required: !1
  }),
  variables: import_pieces_framework.Property.Object({
    displayName: "Variables",
    description: "Key-value pairs to override placeholders in the prompt. Both keys and values will be URL-encoded automatically.",
    required: !1
  })
}, runPromptProps = {
  projectId: import_pieces_framework.Property.Number({
    displayName: "Project ID",
    description: "The ID of the project to run.",
    required: !0
  }),
  branch: import_pieces_framework.Property.ShortText({
    displayName: "Branch",
    description: "The branch name to run from (defaults to main/master).",
    required: !1
  }),
  hash: import_pieces_framework.Property.ShortText({
    displayName: "Hash",
    description: "Specific commit hash to run from.",
    required: !1
  }),
  variables: import_pieces_framework.Property.Object({
    displayName: "Variables",
    description: "Key-value pairs to pass as variables to the prompt.",
    required: !1
  }),
  messages: import_pieces_framework.Property.Array({
    displayName: "Messages",
    description: "Chat messages for chat-based projects.",
    required: !1,
    properties: {
      role: import_pieces_framework.Property.StaticDropdown({
        displayName: "Role",
        required: !0,
        options: {
          options: [
            { label: "system", value: "system" },
            { label: "user", value: "user" },
            { label: "assistant", value: "assistant" }
          ]
        }
      }),
      content: import_pieces_framework.Property.LongText({ displayName: "Content", required: !0 })
    }
  }),
  prompt: import_pieces_framework.Property.LongText({
    displayName: "Prompt",
    description: "Override prompt text for the project.",
    required: !1
  }),
  metadata: import_pieces_framework.Property.Object({
    displayName: "Metadata",
    description: "Additional metadata to include with the run.",
    required: !1
  }),
  timeoutSeconds: import_pieces_framework.Property.Number({
    displayName: "Timeout Seconds",
    description: "Request timeout in seconds (max 600).",
    required: !1
  })
};
function sanitizeVariables(vars) {
  let out = {};
  for (let [k, v] of Object.entries(vars))
    v !== void 0 && (typeof v == "string" || typeof v == "number" || typeof v == "boolean" || v === null ? out[k] = v : typeof v == "object" ? out[k] = JSON.stringify(v) : out[k] = String(v));
  return out;
}

// ../ap/packages/pieces/community/prompthub/src/lib/output-schemas.ts
var currentTeamFields = [
  { key: "id", label: "Team ID", format: "number" },
  { key: "user_id", label: "Owner User ID", format: "number" },
  { key: "name", label: "Team Name" }
], promptHeadBranchFields = [
  { key: "id", label: "Branch ID", format: "number" },
  { key: "name", label: "Branch Name" },
  { key: "protected", label: "Protected", format: "boolean" }
], promptHeadConfigurationFields = [
  { key: "max_tokens", label: "Max Tokens", format: "number" },
  { key: "temperature", label: "Temperature", format: "number" },
  { key: "top_p", label: "Top P", format: "number" },
  { key: "top_k", label: "Top K", format: "number" },
  { key: "presence_penalty", label: "Presence Penalty", format: "number" },
  { key: "frequency_penalty", label: "Frequency Penalty", format: "number" },
  { key: "response_format", label: "Response Format" },
  { key: "reasoning_effort", label: "Reasoning Effort" },
  { key: "extended_thinking", label: "Extended Thinking", format: "boolean" }
], promptFormattedRequestFields = [
  { key: "model", label: "Model" },
  {
    key: "messages",
    label: "Messages",
    labelKey: "role",
    listItems: [
      { key: "role", label: "Role" },
      { key: "content", label: "Content" }
    ]
  },
  { key: "max_tokens", label: "Max Tokens", format: "number" },
  { key: "temperature", label: "Temperature", format: "number" },
  { key: "top_p", label: "Top P", format: "number" },
  {
    key: "response_format",
    label: "Response Format",
    children: [{ key: "type", label: "Type" }]
  }
], promptHeadProjectFields = [
  { key: "id", label: "Project ID", format: "number" },
  { key: "type", label: "Type" },
  { key: "name", label: "Project Name" },
  { key: "description", label: "Description" }
], promptHeadFields = [
  { key: "provider", label: "Provider" },
  { key: "model", label: "Model" },
  { key: "prompt", label: "Prompt" },
  { key: "system_message", label: "System Message" },
  { key: "formatted_request", label: "Formatted Request", children: promptFormattedRequestFields },
  { key: "hash", label: "Hash" },
  { key: "commit_title", label: "Commit Title" },
  { key: "commit_description", label: "Commit Description" },
  {
    key: "variables",
    label: "Variables",
    children: [
      { key: "system_message", label: "System Message Variables" },
      { key: "prompt", label: "Prompt Variables" }
    ]
  },
  { key: "branch", label: "Branch", children: promptHeadBranchFields },
  { key: "configuration", label: "Configuration", children: promptHeadConfigurationFields }
], getCurrentAccountActionOutputSchema = {
  fields: [
    { key: "id", label: "Account ID", format: "number" },
    { key: "name", label: "Name" },
    { key: "email", label: "Email", format: "email" },
    { key: "avatar", label: "Avatar", format: "image" },
    { key: "current_team_id", label: "Current Team ID", format: "number" },
    { key: "current_project_id", label: "Current Project ID", format: "number" },
    { key: "current_prompt_id", label: "Current Prompt ID", format: "number" },
    { key: "created_at", label: "Created At", format: "datetime" },
    { key: "current_team", label: "Current Team", children: currentTeamFields }
  ]
}, listProjectsActionOutputSchema = {
  itemLabel: "{name}",
  fields: [
    {
      key: "projects",
      label: "Projects",
      value: "",
      listItems: [
        { key: "id", label: "Project ID", format: "number" },
        { key: "name", label: "Project Name" },
        { key: "description", label: "Description" },
        { key: "head", label: "Head", children: promptHeadFields },
        { key: "groups", label: "Groups" }
      ]
    }
  ]
}, getProjectHeadActionOutputSchema = {
  fields: [
    { key: "id", label: "Head ID", format: "number" },
    ...promptHeadFields,
    { key: "project", label: "Project", children: promptHeadProjectFields }
  ]
}, runPromptActionOutputSchema = {
  fields: [
    { key: "id", label: "Run ID", format: "number" },
    { key: "transaction_id", label: "Transaction ID", format: "number" },
    { key: "provider", label: "Provider" },
    { key: "model", label: "Model" },
    { key: "text", label: "Response Text" },
    { key: "finish_reason", label: "Finish Reason" },
    { key: "prompt_tokens", label: "Prompt Tokens", format: "number" },
    { key: "completion_tokens", label: "Completion Tokens", format: "number" },
    { key: "total_tokens", label: "Total Tokens", format: "number" },
    { key: "cost", label: "Cost", format: "currency", currency: "USD" },
    { key: "latency", label: "Latency (ms)", format: "duration" },
    { key: "total_time", label: "Total Time (ms)", format: "duration" },
    { key: "project", label: "Project", children: promptHeadProjectFields }
  ]
};

// ../ap/packages/pieces/community/prompthub/src/lib/actions/list-projects.ts
var listProjects = (0, import_pieces_framework2.createAction)({
  name: "list_projects",
  displayName: "List Projects",
  description: "List PromptHub projects for a team. Returns information about each project's head revision and groups.",
  aiMetadata: { description: "List the prompt projects belonging to a PromptHub team, each with its head revision and group assignments. Use this to discover available projects and their IDs before reading a head or running a prompt; optional group/model/provider filters narrow the results. Read-only and idempotent. Requires the numeric team ID.", idempotent: !0 },
  props: listProjectsProps,
  auth: prompthubAuth,
  outputSchema: listProjectsActionOutputSchema,
  async run({ auth, propsValue }) {
    var _a;
    let result = await new PromptHubClient(auth.secret_text).listProjects(propsValue.teamId, {
      group: propsValue.group,
      model: propsValue.model,
      provider: propsValue.provider
    }), data = (_a = result == null ? void 0 : result.data) != null ? _a : result;
    return Array.isArray(data) ? data.map((p) => ({
      id: p.id,
      name: p.name,
      description: p.description,
      head: p.head ? {
        provider: p.head.provider,
        model: p.head.model,
        prompt: p.head.prompt,
        system_message: p.head.system_message,
        formatted_request: p.head.formatted_request,
        hash: p.head.hash,
        commit_title: p.head.commit_title,
        commit_description: p.head.commit_description,
        variables: p.head.variables,
        branch: p.head.branch,
        configuration: p.head.configuration
      } : null,
      groups: p.groups || []
    })) : data;
  }
});

// ../ap/packages/pieces/community/prompthub/src/lib/actions/get-project-head.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var getProjectHead = (0, import_pieces_framework3.createAction)({
  name: "get_project_head",
  displayName: "Get Project Head",
  description: "Get the production-ready version of a PromptHub project (typically the last commit on master/main branch). Useful for integrating prompts into your application.",
  aiMetadata: { description: "Fetch the head (production-ready) revision of a PromptHub project, returning its prompt text, system message, model/provider configuration, and variables. Use this to read a project's current prompt without executing it; an optional branch targets a non-default branch and supplied variables can be interpolated into the returned request. Read-only and idempotent. Requires the numeric project ID.", idempotent: !0 },
  props: getProjectHeadProps,
  auth: prompthubAuth,
  outputSchema: getProjectHeadActionOutputSchema,
  async run({ auth, propsValue }) {
    var _a, _b;
    let client = new PromptHubClient(auth.secret_text), q = {};
    propsValue.branch && (q.branch = propsValue.branch), propsValue.fallback !== void 0 && (q.fallback = propsValue.fallback ? 1 : 0);
    let vars = sanitizeVariables((_a = propsValue.variables) != null ? _a : {});
    for (let [k, v] of Object.entries(vars))
      q[`variables[${encodeURIComponent(k)}]`] = encodeURIComponent(String(v));
    let result = await client.getProjectHead(propsValue.projectId, q), data = (_b = result == null ? void 0 : result.data) != null ? _b : result;
    return {
      id: data == null ? void 0 : data.id,
      provider: data == null ? void 0 : data.provider,
      model: data == null ? void 0 : data.model,
      prompt: data == null ? void 0 : data.prompt,
      system_message: data == null ? void 0 : data.system_message,
      formatted_request: data == null ? void 0 : data.formatted_request,
      hash: data == null ? void 0 : data.hash,
      commit_title: data == null ? void 0 : data.commit_title,
      commit_description: data == null ? void 0 : data.commit_description,
      variables: data == null ? void 0 : data.variables,
      project: data == null ? void 0 : data.project,
      branch: data == null ? void 0 : data.branch,
      configuration: data == null ? void 0 : data.configuration
    };
  }
});

// ../ap/packages/pieces/community/prompthub/src/lib/actions/run-prompt.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var runPrompt = (0, import_pieces_framework4.createAction)({
  name: "run_prompt",
  displayName: "Run Prompt",
  description: "Run a PromptHub project with optional variables, branch/hash, and chat payload",
  aiMetadata: { description: "Execute a PromptHub project against its configured LLM and return the model completion. Use this to actually run a managed prompt rather than just read it; you can pin a specific version via branch or hash, supply variables for interpolation, and pass chat messages or an override prompt for the request. Not idempotent \u2014 each call invokes the model and incurs a new generation. Requires the numeric project ID.", idempotent: !1 },
  props: runPromptProps,
  auth: prompthubAuth,
  outputSchema: runPromptActionOutputSchema,
  async run({ auth, propsValue }) {
    var _a;
    let client = new PromptHubClient(auth.secret_text), body = {};
    propsValue.branch && (body.branch = propsValue.branch), propsValue.hash && (body.hash = propsValue.hash), propsValue.variables && (body.variables = sanitizeVariables(propsValue.variables)), propsValue.messages && (body.messages = propsValue.messages), propsValue.prompt && (body.prompt = propsValue.prompt), propsValue.metadata && (body.metadata = propsValue.metadata);
    let timeoutMs = propsValue.timeoutSeconds ? propsValue.timeoutSeconds * 1e3 : void 0, result = await client.runProject(propsValue.projectId, body, timeoutMs);
    return (_a = result == null ? void 0 : result.data) != null ? _a : result;
  }
});

// ../ap/packages/pieces/community/prompthub/src/lib/actions/get-current-account.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var getCurrentAccount = (0, import_pieces_framework5.createAction)({
  name: "get_current_account",
  displayName: "Get Current Account",
  description: "Get the authenticated PromptHub account associated with the API token, including its current team context.",
  audience: "ai",
  aiMetadata: { description: "Get the PromptHub account authenticated by the current API token, including its current team context. Use this first when you do not already have a team ID: it yields the `current_team_id` that List Team Projects requires, making it the head of the resolution chain (account -> team -> projects -> head/run). Read-only.", idempotent: !0 },
  props: {},
  auth: prompthubAuth,
  outputSchema: getCurrentAccountActionOutputSchema,
  async run({ auth }) {
    var _a;
    let result = await new PromptHubClient(auth.secret_text).getMe();
    return (_a = result == null ? void 0 : result.data) != null ? _a : result;
  }
});

// ../ap/packages/pieces/community/prompthub/src/index.ts
var prompthubAuth = import_pieces_framework6.PieceAuth.SecretText({
  displayName: "API Key",
  description: "PromptHub API token",
  required: !0,
  validate: async (auth) => {
    try {
      return await import_pieces_common2.httpClient.sendRequest({
        url: "https://app.prompthub.us/api/v1/me",
        method: import_pieces_common2.HttpMethod.GET,
        authentication: {
          type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
          token: auth.auth
        },
        timeout: 1e4
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key or insufficient permissions"
      };
    }
  }
}), prompthub = (0, import_pieces_framework6.createPiece)({
  displayName: "PromptHub",
  description: "Integrate with PromptHub projects, retrieve heads, and run prompts.",
  auth: prompthubAuth,
  minimumSupportedRelease: "0.87.0",
  logoUrl: "https://cdn.activepieces.com/pieces/prompthub.png",
  authors: ["sparkybug"],
  actions: [
    listProjects,
    getProjectHead,
    runPrompt,
    getCurrentAccount,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: prompthubAuth,
      baseUrl: () => "https://app.prompthub.us/api/v1",
      authMapping: async (auth) => ({
        authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  prompthub,
  prompthubAuth
});
