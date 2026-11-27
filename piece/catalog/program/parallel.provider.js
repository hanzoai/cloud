"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/parallel/src/index.ts
var index_exports = {};
__export(index_exports, {
  parallel: () => parallel
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/parallel/src/lib/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/parallel/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function callApi({
  apiKey,
  method,
  path,
  body,
  queryParams
}) {
  let request = {
    method,
    url: `${PARALLEL_BASE_URL}${path}`,
    headers: {
      "x-api-key": apiKey,
      "Content-Type": "application/json"
    },
    body,
    queryParams
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}
var parallelClient = {
  request: callApi
}, PARALLEL_BASE_URL = "https://api.parallel.ai", TASK_PROCESSORS = [
  { label: "Lite", value: "lite" },
  { label: "Base", value: "base" },
  { label: "Core", value: "core" },
  { label: "Pro", value: "pro" },
  { label: "Ultra", value: "ultra" }
], FINDALL_GENERATORS = [
  { label: "Base", value: "base" },
  { label: "Core", value: "core" },
  { label: "Pro", value: "pro" },
  { label: "Preview", value: "preview" }
];

// ../ap/packages/pieces/community/parallel/src/lib/auth.ts
var parallelAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: `
**Get your Parallel API key:**

1. Sign in at https://platform.parallel.ai
2. Navigate to **API Keys**
3. Create a new key and paste it here.
  `,
  validate: async ({ auth }) => {
    try {
      return await parallelClient.request({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.POST,
        path: "/v1/search",
        body: {
          objective: "API key validation",
          search_queries: ["parallel ai api"],
          mode: "basic",
          max_chars_total: 100
        }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API key" };
    }
  }
});

// ../ap/packages/pieces/community/parallel/src/lib/actions/chat-completion.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var chatCompletionAction = (0, import_pieces_framework2.createAction)({
  auth: parallelAuth,
  name: "chat_completion",
  displayName: "Chat Completion",
  description: "Get a chat completion using a Parallel processor. OpenAI-compatible interface; processor names like 'speed', 'base', 'core', 'pro', 'ultra' map to Parallel tiers.",
  audience: "both",
  aiMetadata: {
    description: "Generate a chat completion from Parallel's web-grounded research models through an OpenAI-compatible interface, sending a user message plus an optional system message. Use it for a one-shot answer backed by Parallel's research rather than starting a tracked task run; choose a processor tier via the model name (speed, base, core, pro, ultra) and optionally force free text, a JSON object, or a named JSON Schema response. Not idempotent: each call generates a fresh completion.",
    idempotent: !1
  },
  props: {
    model: import_pieces_framework2.Property.ShortText({
      displayName: "Model / Processor",
      description: "Model name to use (e.g. `speed`, `base`, `core`, `pro`, `ultra`).",
      required: !0,
      defaultValue: "speed"
    }),
    system: import_pieces_framework2.Property.LongText({
      displayName: "System Message",
      description: "Optional system message to set the assistant behaviour.",
      required: !1
    }),
    user_message: import_pieces_framework2.Property.LongText({
      displayName: "User Message",
      description: "The user prompt to send.",
      required: !0
    }),
    response_format: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Whether the model should return free text or a JSON object.",
      required: !1,
      defaultValue: "text",
      options: {
        options: [
          { label: "Text", value: "text" },
          { label: "JSON Object", value: "json_object" },
          { label: "JSON Schema", value: "json_schema" }
        ]
      }
    }),
    json_schema: import_pieces_framework2.Property.Json({
      displayName: "JSON Schema",
      description: 'Used when Response Format is "JSON Schema". Provide a JSON Schema object.',
      required: !1
    }),
    json_schema_name: import_pieces_framework2.Property.ShortText({
      displayName: "JSON Schema Name",
      description: "Name for the JSON schema (required when Response Format is JSON Schema).",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let props = context.propsValue, messages = [];
    props.system && messages.push({ role: "system", content: props.system }), messages.push({ role: "user", content: props.user_message });
    let body = {
      model: props.model,
      messages
    };
    if (props.response_format === "json_object")
      body.response_format = { type: "json_object" };
    else if (props.response_format === "json_schema") {
      if (!props.json_schema || typeof props.json_schema != "object")
        throw new Error('JSON Schema is required when Response Format is "JSON Schema".');
      body.response_format = {
        type: "json_schema",
        json_schema: {
          name: (_a = props.json_schema_name) != null ? _a : "response",
          schema: props.json_schema
        }
      };
    }
    return await parallelClient.request({
      apiKey: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/v1beta/chat/completions",
      body
    });
  }
});

// ../ap/packages/pieces/community/parallel/src/lib/actions/create-findall-run.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var createFindAllRunAction = (0, import_pieces_framework3.createAction)({
  auth: parallelAuth,
  name: "create_findall_run",
  displayName: "Create FindAll Run",
  description: "Discover and verify entities matching plain-language criteria. Match conditions can be supplied directly or auto-generated from the objective.",
  audience: "both",
  aiMetadata: {
    description: "Start a Parallel FindAll run that discovers and verifies a list of entities (e.g. companies or people) matching a plain-language objective. Match conditions can be supplied explicitly or, when left empty, are auto-generated from the objective via an extra ingest call. Use it to build verified entity lists rather than answering a single question; pick a generator tier and a match limit (5-1000). Not idempotent: each call queues a new run whose id is then read with Get FindAll Result.",
    idempotent: !1
  },
  props: {
    objective: import_pieces_framework3.Property.LongText({
      displayName: "Objective",
      description: 'Natural-language description of the target entities. e.g. "AI companies that raised Series A funding in 2024".',
      required: !0
    }),
    entity_type: import_pieces_framework3.Property.ShortText({
      displayName: "Entity Type",
      description: 'Type of entities to find (e.g. "company", "people").',
      required: !0
    }),
    match_conditions: import_pieces_framework3.Property.Json({
      displayName: "Match Conditions",
      description: 'Array of `{ "name": "...", "description": "..." }` objects describing the criteria each match must satisfy. Leave empty to auto-generate from the objective.',
      required: !1,
      defaultValue: []
    }),
    generator: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Generator",
      description: "Generator tier \u2014 higher tiers use deeper search at higher cost.",
      required: !0,
      defaultValue: "core",
      options: { options: FINDALL_GENERATORS }
    }),
    match_limit: import_pieces_framework3.Property.Number({
      displayName: "Match Limit",
      description: "Maximum number of matches to find (5\u20131000). Defaults to 100.",
      required: !0,
      defaultValue: 100
    }),
    metadata: import_pieces_framework3.Property.Object({
      displayName: "Metadata",
      description: "Optional key/value metadata stored with the run.",
      required: !1
    })
  },
  async run(context) {
    let props = context.propsValue, apiKey = context.auth.secret_text, providedConditions = normalizeMatchConditions(props.match_conditions), entityType = props.entity_type, generator = props.generator, matchConditions = providedConditions;
    if (matchConditions.length === 0) {
      let ingested = await parallelClient.request({
        apiKey,
        method: import_pieces_common4.HttpMethod.POST,
        path: "/v1beta/findall/ingest",
        body: { objective: props.objective }
      });
      if (!ingested.match_conditions || ingested.match_conditions.length === 0)
        throw new Error(
          "Could not auto-generate match conditions from the objective. Please provide them explicitly."
        );
      matchConditions = ingested.match_conditions, !entityType && ingested.entity_type && (entityType = ingested.entity_type), ingested.generator && (generator = ingested.generator);
    }
    let body = {
      objective: props.objective,
      entity_type: entityType,
      match_conditions: matchConditions,
      generator,
      match_limit: props.match_limit
    };
    return props.metadata && Object.keys(props.metadata).length > 0 && (body.metadata = props.metadata), await parallelClient.request({
      apiKey,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/v1beta/findall/runs",
      body
    });
  }
});
function normalizeMatchConditions(raw) {
  if (!Array.isArray(raw)) return [];
  let out = [];
  for (let item of raw)
    item && typeof item == "object" && "name" in item && "description" in item && typeof item.name == "string" && typeof item.description == "string" && out.push({
      name: item.name,
      description: item.description
    });
  return out;
}

// ../ap/packages/pieces/community/parallel/src/lib/actions/create-task-run.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var createTaskRunAction = (0, import_pieces_framework4.createAction)({
  auth: parallelAuth,
  name: "create_task_run",
  displayName: "Create Task Run",
  description: "Initiate a Parallel research task. Returns immediately with a queued run; use Get Task Run Result to wait for completion.",
  audience: "both",
  aiMetadata: {
    description: "Start an asynchronous multi-hop research task on Parallel for a free-text or JSON input, selecting a speed/depth processor tier and optionally constraining output to plain text or a JSON Schema. Use it to kick off deeper research than a single search; it returns immediately with a queued run id that you then poll with Get Task Run Status or block on with Get Task Run Result. Not idempotent: each call queues a new run.",
    idempotent: !1
  },
  props: {
    input: import_pieces_framework4.Property.LongText({
      displayName: "Input",
      description: 'Input to the task (free text or a JSON string). Example: "What was the GDP of France in 2023?"',
      required: !0
    }),
    processor: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Processor",
      description: "Speed/depth tier for the task.",
      required: !0,
      defaultValue: "base",
      options: { options: TASK_PROCESSORS }
    }),
    output_schema_type: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Output Schema Type",
      description: "How the task should structure its output.",
      required: !1,
      defaultValue: "auto",
      options: {
        options: [
          { label: "Auto", value: "auto" },
          { label: "Text", value: "text" },
          { label: "JSON Schema", value: "json" }
        ]
      }
    }),
    output_text_description: import_pieces_framework4.Property.LongText({
      displayName: "Output Description (Text)",
      description: 'Used when Output Schema Type is "Text". Plain-language description of the desired output.',
      required: !1
    }),
    output_json_schema: import_pieces_framework4.Property.Json({
      displayName: "Output JSON Schema",
      description: 'Used when Output Schema Type is "JSON Schema". A JSON Schema object describing the desired structured output.',
      required: !1
    }),
    include_domains: import_pieces_framework4.Property.Array({
      displayName: "Include Domains",
      description: "Optional source policy: only return results from these domains.",
      required: !1
    }),
    exclude_domains: import_pieces_framework4.Property.Array({
      displayName: "Exclude Domains",
      description: "Optional source policy: exclude results from these domains.",
      required: !1
    }),
    metadata: import_pieces_framework4.Property.Object({
      displayName: "Metadata",
      description: "Optional key/value metadata stored with the run (keys \u226416 chars, values \u2264512 chars).",
      required: !1
    })
  },
  async run(context) {
    var _a, _b;
    let props = context.propsValue, includeDomains = ((_a = props.include_domains) != null ? _a : []).filter(
      (d) => typeof d == "string" && d.trim().length > 0
    ), excludeDomains = ((_b = props.exclude_domains) != null ? _b : []).filter(
      (d) => typeof d == "string" && d.trim().length > 0
    ), sourcePolicy = includeDomains.length || excludeDomains.length ? __spreadValues(__spreadValues({}, includeDomains.length ? { include_domains: includeDomains } : {}), excludeDomains.length ? { exclude_domains: excludeDomains } : {}) : void 0, taskSpec = buildTaskSpec({
      type: props.output_schema_type,
      textDescription: props.output_text_description,
      jsonSchema: props.output_json_schema
    }), body = {
      input: props.input,
      processor: props.processor
    };
    return taskSpec && (body.task_spec = taskSpec), sourcePolicy && (body.source_policy = sourcePolicy), props.metadata && Object.keys(props.metadata).length > 0 && (body.metadata = props.metadata), await parallelClient.request({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/v1/tasks/runs",
      body
    });
  }
});
function buildTaskSpec({
  type,
  textDescription,
  jsonSchema
}) {
  if (!(!type || type === "auto")) {
    if (type === "text")
      return textDescription ? {
        output_schema: { type: "text", description: textDescription }
      } : void 0;
    if (type === "json")
      return !jsonSchema || typeof jsonSchema != "object" ? void 0 : {
        output_schema: { type: "json", json_schema: jsonSchema }
      };
  }
}

// ../ap/packages/pieces/community/parallel/src/lib/actions/extract.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var extractAction = (0, import_pieces_framework5.createAction)({
  auth: parallelAuth,
  name: "extract",
  displayName: "Extract Web Content",
  description: "Extract clean, citation-aware content from specific web URLs. Up to 20 URLs per request.",
  audience: "both",
  aiMetadata: {
    description: "Fetch and extract clean, citation-aware content from a known list of web URLs (up to 20 per request) via Parallel's research API. Use it when you already have the pages and want their readable content rather than searching for them; an optional objective and queries focus the returned excerpts, and full-page markdown can be requested at the cost of higher latency. Requires at least one URL. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    urls: import_pieces_framework5.Property.Array({
      displayName: "URLs",
      description: "List of URLs to extract content from (max 20).",
      required: !0
    }),
    objective: import_pieces_framework5.Property.LongText({
      displayName: "Objective",
      description: "Natural-language description of the goal driving the extraction. Used to focus excerpts on the most relevant content.",
      required: !1
    }),
    search_queries: import_pieces_framework5.Property.Array({
      displayName: "Search Queries",
      description: "Optional keyword search queries used together with the objective to focus excerpts.",
      required: !1
    }),
    full_content: import_pieces_framework5.Property.Checkbox({
      displayName: "Include Full Content",
      description: "When enabled, requests full markdown of each page in addition to excerpts. May increase latency.",
      required: !1,
      defaultValue: !1
    }),
    max_chars_total: import_pieces_framework5.Property.Number({
      displayName: "Max Total Characters",
      description: "Upper bound on total characters across excerpts from all results.",
      required: !1
    }),
    session_id: import_pieces_framework5.Property.ShortText({
      displayName: "Session ID",
      description: "Optional session identifier to share context across search/extract calls.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b;
    let urls = ((_a = context.propsValue.urls) != null ? _a : []).filter(
      (u) => typeof u == "string" && u.trim().length > 0
    );
    if (urls.length === 0)
      throw new Error("At least one URL is required.");
    let queries = ((_b = context.propsValue.search_queries) != null ? _b : []).filter(
      (q) => typeof q == "string" && q.trim().length > 0
    ), body = { urls };
    return context.propsValue.objective && (body.objective = context.propsValue.objective), queries.length && (body.search_queries = queries), context.propsValue.max_chars_total !== void 0 && context.propsValue.max_chars_total !== null && (body.max_chars_total = context.propsValue.max_chars_total), context.propsValue.session_id && (body.session_id = context.propsValue.session_id), context.propsValue.full_content && (body.advanced_settings = {
      full_content: { enabled: !0 }
    }), await parallelClient.request({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      path: "/v1/extract",
      body
    });
  }
});

// ../ap/packages/pieces/community/parallel/src/lib/actions/get-findall-result.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var getFindAllResultAction = (0, import_pieces_framework6.createAction)({
  auth: parallelAuth,
  name: "get_findall_result",
  displayName: "Get FindAll Result",
  description: "Return the current snapshot of matched candidates for a FindAll run, including any enrichment fields.",
  audience: "both",
  aiMetadata: {
    description: "Read the current snapshot of matched candidates and enrichment fields for a Parallel FindAll run by its id. Use it to poll or collect the entities discovered by Create FindAll Run; the run may still be in progress, so results can grow across calls. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    findall_id: import_pieces_framework6.Property.ShortText({
      displayName: "FindAll ID",
      description: "The FindAll run ID returned from Create FindAll Run.",
      required: !0
    })
  },
  async run(context) {
    return await parallelClient.request({
      apiKey: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      path: `/v1beta/findall/runs/${encodeURIComponent(context.propsValue.findall_id)}/result`
    });
  }
});

// ../ap/packages/pieces/community/parallel/src/lib/actions/get-task-run.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var getTaskRunAction = (0, import_pieces_framework7.createAction)({
  auth: parallelAuth,
  name: "get_task_run",
  displayName: "Get Task Run Status",
  description: "Retrieve the current status of a task run by its ID.",
  audience: "both",
  aiMetadata: {
    description: "Look up the current status of a previously created Parallel research task by its run id, without blocking. Use it to poll whether a task started with Create Task Run is still queued or running before fetching its result. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    run_id: import_pieces_framework7.Property.ShortText({
      displayName: "Run ID",
      description: "The task run ID, e.g. `trun_e0083b6aac0544eb8686e8d2a76533d2`.",
      required: !0
    })
  },
  async run(context) {
    return await parallelClient.request({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.GET,
      path: `/v1/tasks/runs/${encodeURIComponent(context.propsValue.run_id)}`
    });
  }
});

// ../ap/packages/pieces/community/parallel/src/lib/actions/get-task-run-result.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var getTaskRunResultAction = (0, import_pieces_framework8.createAction)({
  auth: parallelAuth,
  name: "get_task_run_result",
  displayName: "Get Task Run Result",
  description: "Retrieve a task run result by run ID. Blocks until the run completes (or until the timeout is reached).",
  audience: "both",
  aiMetadata: {
    description: "Fetch the final result of a Parallel research task by its run id, blocking until the run completes or the configured timeout (default 600s) is reached. Use it after Create Task Run to wait for and collect the task output. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    run_id: import_pieces_framework8.Property.ShortText({
      displayName: "Run ID",
      description: "The task run ID returned from Create Task Run.",
      required: !0
    }),
    timeout: import_pieces_framework8.Property.Number({
      displayName: "Timeout (seconds)",
      description: "How long to wait for the run to complete. Defaults to 600 seconds.",
      required: !1,
      defaultValue: 600
    })
  },
  async run(context) {
    let queryParams = {};
    return context.propsValue.timeout !== void 0 && context.propsValue.timeout !== null && (queryParams.timeout = String(context.propsValue.timeout)), await parallelClient.request({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.GET,
      path: `/v1/tasks/runs/${encodeURIComponent(context.propsValue.run_id)}/result`,
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/parallel/src/lib/actions/search.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var searchAction = (0, import_pieces_framework9.createAction)({
  auth: parallelAuth,
  name: "search",
  displayName: "Search the Web",
  description: "Search the web with natural-language objectives. Returns LLM-optimized excerpts with citations.",
  audience: "both",
  aiMetadata: {
    description: "Run a web search via Parallel's research API and return ranked, citation-backed text excerpts for a natural-language objective plus one or more keyword queries. Use it to gather fresh web evidence on a topic; choose between a fast Basic mode and a deeper Advanced mode, and optionally restrict or exclude specific domains. At least one search query is required. Read-only and idempotent: re-running the same query has no side effect, though live web results may vary over time.",
    idempotent: !0
  },
  props: {
    objective: import_pieces_framework9.Property.LongText({
      displayName: "Objective",
      description: "Natural-language description of the underlying question or goal driving the search. Should be self-contained with enough context to understand the intent.",
      required: !1
    }),
    search_queries: import_pieces_framework9.Property.Array({
      displayName: "Search Queries",
      description: "Concise keyword search queries (3-6 words each). At least one query is required; 2-3 is best.",
      required: !0
    }),
    mode: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Mode",
      description: "Basic: low latency with 2-3 high-quality queries. Advanced: higher quality with deeper retrieval.",
      required: !1,
      defaultValue: "advanced",
      options: {
        options: [
          { label: "Basic", value: "basic" },
          { label: "Advanced", value: "advanced" }
        ]
      }
    }),
    max_chars_total: import_pieces_framework9.Property.Number({
      displayName: "Max Total Characters",
      description: "Upper bound on total characters across excerpts from all results.",
      required: !1
    }),
    include_domains: import_pieces_framework9.Property.Array({
      displayName: "Include Domains",
      description: "Optional list of domains to restrict results to (e.g. wikipedia.org, .gov).",
      required: !1
    }),
    exclude_domains: import_pieces_framework9.Property.Array({
      displayName: "Exclude Domains",
      description: "Optional list of domains to exclude from results.",
      required: !1
    }),
    session_id: import_pieces_framework9.Property.ShortText({
      displayName: "Session ID",
      description: "Optional session identifier to share context across search/extract calls.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let queries = ((_a = context.propsValue.search_queries) != null ? _a : []).filter(
      (q) => typeof q == "string" && q.trim().length > 0
    );
    if (queries.length === 0)
      throw new Error("At least one search query is required.");
    let includeDomains = ((_b = context.propsValue.include_domains) != null ? _b : []).filter(
      (d) => typeof d == "string" && d.trim().length > 0
    ), excludeDomains = ((_c = context.propsValue.exclude_domains) != null ? _c : []).filter(
      (d) => typeof d == "string" && d.trim().length > 0
    ), sourcePolicy = includeDomains.length || excludeDomains.length ? __spreadValues(__spreadValues({}, includeDomains.length ? { include_domains: includeDomains } : {}), excludeDomains.length ? { exclude_domains: excludeDomains } : {}) : void 0, body = {
      search_queries: queries
    };
    return context.propsValue.objective && (body.objective = context.propsValue.objective), context.propsValue.mode && (body.mode = context.propsValue.mode), context.propsValue.max_chars_total !== void 0 && context.propsValue.max_chars_total !== null && (body.max_chars_total = context.propsValue.max_chars_total), context.propsValue.session_id && (body.session_id = context.propsValue.session_id), sourcePolicy && (body.source_policy = sourcePolicy), await parallelClient.request({
      apiKey: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.POST,
      path: "/v1/search",
      body
    });
  }
});

// ../ap/packages/pieces/community/parallel/src/index.ts
var parallel = (0, import_pieces_framework10.createPiece)({
  displayName: "Parallel",
  description: "Web research APIs for AI: search, extract, multi-hop research tasks, entity discovery (FindAll), and web monitoring.",
  auth: parallelAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/parallel.png",
  categories: [import_pieces_framework11.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["sanket-a11y"],
  actions: [
    searchAction,
    extractAction,
    createTaskRunAction,
    getTaskRunAction,
    getTaskRunResultAction,
    createFindAllRunAction,
    getFindAllResultAction,
    chatCompletionAction,
    (0, import_pieces_common11.createCustomApiCallAction)({
      baseUrl: () => PARALLEL_BASE_URL,
      auth: parallelAuth,
      authMapping: async (auth) => ({
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  parallel
});
