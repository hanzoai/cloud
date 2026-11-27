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

// ../ap/packages/pieces/community/valyu/src/index.ts
var index_exports = {};
__export(index_exports, {
  valyu: () => valyu
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/valyu/src/lib/actions/search.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/valyu/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.valyu.ai";
async function makeRequest(auth, method, path, body) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      "x-api-key": `${auth}`,
      "Content-Type": "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/valyu/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var markdownDescription = "Obtain your API key from [Valyu Platform](https://platform.valyu.ai/user/account/apikeys).", valyuAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/v1/datasources"), {
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

// ../ap/packages/pieces/community/valyu/src/lib/actions/search.ts
var searchAction = (0, import_pieces_framework2.createAction)({
  name: "search",
  displayName: "Search",
  description: "Search the web, research papers, and proprietary datasets to retrieve relevant context.",
  audience: "both",
  aiMetadata: { description: "Runs a Valyu search over web pages, news, and/or Valyu proprietary datasets and returns ranked content for a free-text query. Use to gather grounding context or sources for a question; set search_type to scope to web, news, or proprietary indices (default searches all), and use url_only to return links without content. Read-only retrieval, so repeating the same query is safe.", idempotent: !0 },
  auth: valyuAuth,
  props: {
    query: import_pieces_framework2.Property.ShortText({
      displayName: "Query",
      description: "The search query to find relevant content.",
      required: !0
    }),
    max_num_results: import_pieces_framework2.Property.Number({
      displayName: "Max Results",
      description: "Maximum number of results to return (1-20 for standard API keys, up to 100 with special API key).",
      required: !1,
      defaultValue: 10
    }),
    search_type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Search Type",
      description: "Type of search to perform. 'web' searches web content. 'proprietary' uses Valyu's full-text multimodal indices. 'news' returns only news articles.",
      required: !1,
      defaultValue: "all",
      options: {
        options: [
          { label: "All", value: "all" },
          { label: "Web", value: "web" },
          { label: "Proprietary", value: "proprietary" },
          { label: "News", value: "news" }
        ]
      }
    }),
    fast_mode: import_pieces_framework2.Property.Checkbox({
      displayName: "Fast Mode",
      description: "Enable fast mode for reduced latency but shorter results. Best for general purpose queries.",
      required: !1,
      defaultValue: !1
    }),
    max_price: import_pieces_framework2.Property.Number({
      displayName: "Max Price (CPM)",
      description: "Maximum price in dollars for a thousand retrievals (CPM). Only applies when provided.",
      required: !1
    }),
    relevance_threshold: import_pieces_framework2.Property.Number({
      displayName: "Relevance Threshold",
      description: "Minimum relevance score for results (0.0-1.0).",
      required: !1,
      defaultValue: 0.5
    }),
    included_sources: import_pieces_framework2.Property.Array({
      displayName: "Included Sources",
      description: "Specific sources to search (URLs, domains or dataset names).",
      required: !1
    }),
    excluded_sources: import_pieces_framework2.Property.Array({
      displayName: "Excluded Sources",
      description: "Specific sources to exclude from search (URLs, domains, or dataset names).",
      required: !1
    }),
    category: import_pieces_framework2.Property.ShortText({
      displayName: "Category",
      description: 'Natural language category/guide phrase to help guide the search (e.g., "agentic use-cases").',
      required: !1
    }),
    response_length: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Response Length",
      description: "Controls the length of content returned per result.",
      required: !1,
      defaultValue: "short",
      options: {
        options: [
          { label: "Short (25k chars)", value: "short" },
          { label: "Medium (50k chars)", value: "medium" },
          { label: "Large (100k chars)", value: "large" },
          { label: "Max (full content)", value: "max" }
        ]
      }
    }),
    country_code: import_pieces_framework2.Property.ShortText({
      displayName: "Country Code",
      description: "2-letter ISO country code to bias search results (e.g., US, GB, DE).",
      required: !1
    }),
    start_date: import_pieces_framework2.Property.ShortText({
      displayName: "Start Date",
      description: "Start date for time-filtered searches (YYYY-MM-DD).",
      required: !1
    }),
    end_date: import_pieces_framework2.Property.ShortText({
      displayName: "End Date",
      description: "End date for time-filtered searches (YYYY-MM-DD).",
      required: !1
    }),
    url_only: import_pieces_framework2.Property.Checkbox({
      displayName: "URL Only",
      description: "When set to true, only returns URLs for results (no content). Only applies when search_type is web or news.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let apiKey = context.auth, body = {
      query: context.propsValue.query
    }, optionalProps = [
      "max_num_results",
      "search_type",
      "fast_mode",
      "max_price",
      "relevance_threshold",
      "included_sources",
      "excluded_sources",
      "category",
      "response_length",
      "country_code",
      "start_date",
      "end_date",
      "url_only"
    ];
    for (let prop of optionalProps) {
      let val = context.propsValue[prop];
      val != null && val !== "" && (body[prop] = val);
    }
    return await makeRequest(apiKey.secret_text, import_pieces_common3.HttpMethod.POST, "/v1/search", body);
  }
});

// ../ap/packages/pieces/community/valyu/src/lib/actions/extract-content.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var extractContentAction = (0, import_pieces_framework3.createAction)({
  name: "extract_content",
  displayName: "Extract Content",
  description: "Extract clean, structured content from web pages with optional AI-powered summarization.",
  audience: "both",
  aiMetadata: { description: "Fetches and extracts clean text content from a list of 1-10 web page URLs you already have. Optionally adds AI processing via the summary mode (none, a default summary, custom summary instructions, or structured JSON extraction against a supplied schema). Use when you have specific URLs to read rather than a query to search; read-only fetch, so repeating the same request is safe.", idempotent: !0 },
  auth: valyuAuth,
  props: {
    urls: import_pieces_framework3.Property.Array({
      displayName: "URLs",
      description: "URLs to extract content from (1-10 URLs per request).",
      required: !0
    }),
    response_length: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Response Length",
      description: "Controls the length of content returned per result.",
      required: !1,
      defaultValue: "short",
      options: {
        options: [
          { label: "Short (25k chars)", value: "short" },
          { label: "Medium (50k chars)", value: "medium" },
          { label: "Large (100k chars)", value: "large" },
          { label: "Max (no limit)", value: "max" }
        ]
      }
    }),
    max_price_dollars: import_pieces_framework3.Property.Number({
      displayName: "Max Price",
      description: "Maximum cost limit in dollars for the entire request.",
      required: !1
    }),
    extract_effort: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Extract Effort",
      description: "Processing effort level.",
      required: !1,
      defaultValue: "normal",
      options: {
        options: [
          { label: "Normal (fastest)", value: "normal" },
          { label: "High (better quality)", value: "high" },
          { label: "Auto (chooses automatically)", value: "auto" }
        ]
      }
    }),
    screenshot: import_pieces_framework3.Property.Checkbox({
      displayName: "Screenshot",
      description: "Capture page screenshots. Results will include a screenshot_url field.",
      required: !1,
      defaultValue: !1
    }),
    enable_summary: import_pieces_framework3.Property.StaticDropdown({
      displayName: "AI Processing",
      description: "Enable AI-powered processing of the content.",
      required: !1,
      defaultValue: "none",
      options: {
        options: [
          { label: "None", value: "none" },
          { label: "Default Summary", value: "default" },
          { label: "Custom Instructions", value: "custom" },
          { label: "Structured JSON", value: "structured" }
        ]
      }
    }),
    summaryOptions: import_pieces_framework3.Property.DynamicProperties({
      displayName: "Summary Options",
      required: !1,
      refreshers: ["enable_summary"],
      auth: valyuAuth,
      props: async (propsValue) => {
        let summaryMode = propsValue.enable_summary;
        return summaryMode === "custom" ? {
          custom_summary_instructions: import_pieces_framework3.Property.LongText({
            displayName: "Summary Instructions",
            description: "Custom instructions for AI summarization (up to 500 characters).",
            required: !1
          })
        } : summaryMode === "structured" ? {
          json_schema: import_pieces_framework3.Property.LongText({
            displayName: "JSON Schema",
            description: "JSON schema for structured data extraction (see json-schema.org).",
            required: !1
          })
        } : {};
      }
    })
  },
  async run(context) {
    let apiKey = context.auth, body = {
      urls: context.propsValue.urls
    }, optionalProps = [
      "response_length",
      "max_price_dollars",
      "extract_effort",
      "screenshot"
    ];
    for (let prop of optionalProps) {
      let val = context.propsValue[prop];
      val != null && val !== "" && (body[prop] = val);
    }
    let summaryMode = context.propsValue.enable_summary, summaryOptions = context.propsValue.summaryOptions;
    if (summaryMode === "default")
      body.summary = !0;
    else if (summaryMode === "custom" && (summaryOptions != null && summaryOptions.custom_summary_instructions))
      body.summary = summaryOptions.custom_summary_instructions;
    else if (summaryMode === "structured" && (summaryOptions != null && summaryOptions.json_schema))
      try {
        body.summary = JSON.parse(summaryOptions.json_schema);
      } catch (e) {
        throw new Error("Invalid JSON schema provided");
      }
    return await makeRequest(apiKey, import_pieces_common4.HttpMethod.POST, "/v1/contents", body);
  }
});

// ../ap/packages/pieces/community/valyu/src/lib/actions/answer.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var answerAction = (0, import_pieces_framework4.createAction)({
  name: "answer",
  displayName: "Answer Query",
  description: "Get AI-generated answers using search results. Combines web/proprietary search with AI processing.",
  audience: "both",
  aiMetadata: { description: "Answers a question by running a Valyu search and synthesizing the results into an AI-generated response. Use when you want a direct answer rather than raw search hits; output_format can return unstructured text or structured JSON conforming to a supplied schema. Read-only retrieval with no stored side effect, so repeating the same query is safe.", idempotent: !0 },
  auth: valyuAuth,
  props: {
    query: import_pieces_framework4.Property.ShortText({
      displayName: "Query",
      description: "The question or topic to get an AI-generated answer for.",
      required: !0
    }),
    system_instructions: import_pieces_framework4.Property.LongText({
      displayName: "System Instructions",
      description: "Custom instructions for AI processing (up to 2000 characters).",
      required: !1
    }),
    output_format: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Output Format",
      description: "Choose the format for the AI response.",
      required: !1,
      defaultValue: "unstructured",
      options: {
        options: [
          { label: "Unstructured Text", value: "unstructured" },
          { label: "Structured JSON", value: "structured" }
        ]
      }
    }),
    structuredOptions: import_pieces_framework4.Property.DynamicProperties({
      displayName: "Structured Output Options",
      required: !1,
      refreshers: ["output_format"],
      auth: valyuAuth,
      props: async (propsValue) => propsValue.output_format === "structured" ? {
        json_schema: import_pieces_framework4.Property.LongText({
          displayName: "JSON Schema",
          description: "JSON schema for structured output (see json-schema.org).",
          required: !1
        })
      } : {}
    }),
    search_type: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Search Type",
      description: "Type of search to perform.",
      required: !1,
      defaultValue: "all",
      options: {
        options: [
          { label: "All", value: "all" },
          { label: "Web", value: "web" },
          { label: "Proprietary", value: "proprietary" },
          { label: "News", value: "news" }
        ]
      }
    }),
    fast_mode: import_pieces_framework4.Property.Checkbox({
      displayName: "Fast Mode",
      description: "Enable fast mode for reduced latency but shorter results.",
      required: !1,
      defaultValue: !1
    }),
    data_max_price: import_pieces_framework4.Property.Number({
      displayName: "Data Max Price",
      description: "Maximum price in dollars for data retrieval (search costs only).",
      required: !1,
      defaultValue: 1
    }),
    included_sources: import_pieces_framework4.Property.Array({
      displayName: "Included Sources",
      description: "Specific sources to search (URLs, domains or dataset names).",
      required: !1
    }),
    excluded_sources: import_pieces_framework4.Property.Array({
      displayName: "Excluded Sources",
      description: "Specific sources to exclude from search.",
      required: !1
    }),
    start_date: import_pieces_framework4.Property.ShortText({
      displayName: "Start Date",
      description: "Start date filter for search results (YYYY-MM-DD).",
      required: !1
    }),
    end_date: import_pieces_framework4.Property.ShortText({
      displayName: "End Date",
      description: "End date filter for search results (YYYY-MM-DD).",
      required: !1
    }),
    country_code: import_pieces_framework4.Property.ShortText({
      displayName: "Country Code",
      description: "2-letter ISO country code to bias search results (e.g., US, GB).",
      required: !1
    }),
    streaming: import_pieces_framework4.Property.Checkbox({
      displayName: "Streaming",
      description: "Enable SSE streaming. Returns search results first, then content deltas.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let apiKey = context.auth, body = {
      query: context.propsValue.query
    }, optionalProps = [
      "system_instructions",
      "search_type",
      "fast_mode",
      "data_max_price",
      "included_sources",
      "excluded_sources",
      "start_date",
      "end_date",
      "country_code",
      "streaming"
    ];
    for (let prop of optionalProps) {
      let val = context.propsValue[prop];
      val != null && val !== "" && (body[prop] = val);
    }
    let structuredOptions = context.propsValue.structuredOptions;
    if (context.propsValue.output_format === "structured" && (structuredOptions != null && structuredOptions.json_schema))
      try {
        body.structured_output = JSON.parse(structuredOptions.json_schema);
      } catch (e) {
        throw new Error("Invalid JSON schema provided");
      }
    return await makeRequest(apiKey, import_pieces_common5.HttpMethod.POST, "/v1/answer", body);
  }
});

// ../ap/packages/pieces/community/valyu/src/lib/actions/create-deep-research-task.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createDeepResearchTaskAction = (0, import_pieces_framework5.createAction)({
  name: "create_deep_research_task",
  displayName: "Create Deep Research Task",
  description: "Create an asynchronous deep research task that searches multiple sources and generates detailed reports.",
  audience: "both",
  aiMetadata: { description: "Starts a long-running, asynchronous deep-research task that searches multiple sources and produces a detailed report; mode trades depth for time (fast, standard, or heavy up to ~90 min). Use for thorough multi-source investigation rather than a quick lookup; the call returns a task to poll or be notified about via webhook, not the finished report. Each call creates a new task, so it is not idempotent.", idempotent: !1 },
  auth: valyuAuth,
  props: {
    query: import_pieces_framework5.Property.LongText({
      displayName: "Query",
      description: "Research query or task description.",
      required: !0
    }),
    mode: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Mode",
      description: "Research depth and duration.",
      required: !1,
      defaultValue: "standard",
      options: {
        options: [
          { label: "Fast (~2-5 min)", value: "fast" },
          { label: "Standard (~10-20 min)", value: "standard" },
          { label: "Heavy (~90 min)", value: "heavy" }
        ]
      }
    }),
    output_format: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Output Format",
      description: "Format for the research output.",
      required: !1,
      defaultValue: "markdown",
      options: {
        options: [
          { label: "Markdown", value: "markdown" },
          { label: "Markdown + PDF", value: "markdown_pdf" },
          { label: "Structured JSON", value: "structured" }
        ]
      }
    }),
    structuredOptions: import_pieces_framework5.Property.DynamicProperties({
      displayName: "Structured Output Options",
      required: !1,
      refreshers: ["output_format"],
      auth: valyuAuth,
      props: async (propsValue) => propsValue.output_format === "structured" ? {
        json_schema: import_pieces_framework5.Property.LongText({
          displayName: "JSON Schema",
          description: "JSON schema for structured output.",
          required: !1
        })
      } : {}
    }),
    strategy: import_pieces_framework5.Property.LongText({
      displayName: "Strategy",
      description: 'Natural language strategy instructions (e.g., "Focus on peer-reviewed sources").',
      required: !1
    }),
    search_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Search Type",
      description: "Type of search to perform.",
      required: !1,
      defaultValue: "all",
      options: {
        options: [
          { label: "All", value: "all" },
          { label: "Web", value: "web" },
          { label: "Proprietary", value: "proprietary" }
        ]
      }
    }),
    included_sources: import_pieces_framework5.Property.StaticMultiSelectDropdown({
      displayName: "Included Sources",
      description: "Restrict search to specific source types.",
      required: !1,
      options: {
        options: [
          { label: "Web", value: "web" },
          { label: "Academic", value: "academic" },
          { label: "Finance", value: "finance" },
          { label: "Patent", value: "patent" },
          { label: "Transportation", value: "transportation" },
          { label: "Politics", value: "politics" },
          { label: "Legal", value: "legal" }
        ]
      }
    }),
    excluded_sources: import_pieces_framework5.Property.StaticMultiSelectDropdown({
      displayName: "Excluded Sources",
      description: "Exclude specific source types from search.",
      required: !1,
      options: {
        options: [
          { label: "Web", value: "web" },
          { label: "Academic", value: "academic" },
          { label: "Finance", value: "finance" },
          { label: "Patent", value: "patent" },
          { label: "Transportation", value: "transportation" },
          { label: "Politics", value: "politics" },
          { label: "Legal", value: "legal" }
        ]
      }
    }),
    start_date: import_pieces_framework5.Property.ShortText({
      displayName: "Start Date",
      description: "Filter content published on or after this date (YYYY-MM-DD).",
      required: !1
    }),
    end_date: import_pieces_framework5.Property.ShortText({
      displayName: "End Date",
      description: "Filter content published on or before this date (YYYY-MM-DD).",
      required: !1
    }),
    urls: import_pieces_framework5.Property.Array({
      displayName: "URLs",
      description: "URLs to extract content from (max 10).",
      required: !1
    }),
    code_execution: import_pieces_framework5.Property.Checkbox({
      displayName: "Code Execution",
      description: "Enable code execution during research.",
      required: !1,
      defaultValue: !0
    }),
    previous_reports: import_pieces_framework5.Property.Array({
      displayName: "Previous Report IDs",
      description: "Previous deep research IDs to use as context (max 3).",
      required: !1
    }),
    webhook_url: import_pieces_framework5.Property.ShortText({
      displayName: "Webhook URL",
      description: "HTTPS URL for completion notifications.",
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth, body = {
      query: context.propsValue.query
    };
    context.propsValue.mode && (body.mode = context.propsValue.mode);
    let outputFormat = context.propsValue.output_format, structuredOptions = context.propsValue.structuredOptions;
    if (outputFormat === "markdown")
      body.output_formats = ["markdown"];
    else if (outputFormat === "markdown_pdf")
      body.output_formats = ["markdown", "pdf"];
    else if (outputFormat === "structured" && (structuredOptions != null && structuredOptions.json_schema))
      try {
        body.output_formats = [JSON.parse(structuredOptions.json_schema)];
      } catch (e) {
        throw new Error("Invalid JSON schema provided");
      }
    context.propsValue.strategy && (body.strategy = context.propsValue.strategy);
    let search = {};
    return context.propsValue.search_type && (search.search_type = context.propsValue.search_type), context.propsValue.included_sources && context.propsValue.included_sources.length > 0 && (search.included_sources = context.propsValue.included_sources), context.propsValue.excluded_sources && context.propsValue.excluded_sources.length > 0 && (search.excluded_sources = context.propsValue.excluded_sources), context.propsValue.start_date && (search.start_date = context.propsValue.start_date), context.propsValue.end_date && (search.end_date = context.propsValue.end_date), Object.keys(search).length > 0 && (body.search = search), context.propsValue.urls && context.propsValue.urls.length > 0 && (body.urls = context.propsValue.urls), context.propsValue.code_execution !== void 0 && (body.code_execution = context.propsValue.code_execution), context.propsValue.previous_reports && context.propsValue.previous_reports.length > 0 && (body.previous_reports = context.propsValue.previous_reports), context.propsValue.webhook_url && (body.webhook_url = context.propsValue.webhook_url), await makeRequest(apiKey, import_pieces_common6.HttpMethod.POST, "/v1/deepresearch/tasks", body);
  }
});

// ../ap/packages/pieces/community/valyu/src/lib/actions/create-batch.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var createBatchAction = (0, import_pieces_framework6.createAction)({
  name: "create_batch",
  displayName: "Create Batch",
  description: "Create a batch for processing multiple DeepResearch tasks with shared configuration.",
  audience: "both",
  aiMetadata: { description: "Creates a DeepResearch batch container with shared configuration (mode, output format, search scope, webhook) that subsequent deep-research tasks can run under. Use when grouping multiple research tasks that should share settings. Each call creates a new batch, so it is not idempotent.", idempotent: !1 },
  auth: valyuAuth,
  props: {
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Batch Name",
      description: "Optional name for the batch.",
      required: !1
    }),
    mode: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Mode",
      description: "DeepResearch mode for all tasks in this batch.",
      required: !1,
      defaultValue: "standard",
      options: {
        options: [
          { label: "Fast (~2-5 min)", value: "fast" },
          { label: "Standard (~10-20 min)", value: "standard" },
          { label: "Heavy (~90 min)", value: "heavy" }
        ]
      }
    }),
    output_format: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Output Format",
      description: "Default output format for all tasks.",
      required: !1,
      defaultValue: "markdown",
      options: {
        options: [
          { label: "Markdown", value: "markdown" },
          { label: "Markdown + PDF", value: "markdown_pdf" },
          { label: "Structured JSON", value: "structured" }
        ]
      }
    }),
    structuredOptions: import_pieces_framework6.Property.DynamicProperties({
      displayName: "Structured Output Options",
      required: !1,
      refreshers: ["output_format"],
      auth: valyuAuth,
      props: async (propsValue) => propsValue.output_format === "structured" ? {
        json_schema: import_pieces_framework6.Property.LongText({
          displayName: "JSON Schema",
          description: "JSON schema for structured output.",
          required: !1
        })
      } : {}
    }),
    search_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Search Type",
      description: "Search type for all tasks.",
      required: !1,
      defaultValue: "all",
      options: {
        options: [
          { label: "All", value: "all" },
          { label: "Web", value: "web" },
          { label: "Proprietary", value: "proprietary" }
        ]
      }
    }),
    included_sources: import_pieces_framework6.Property.StaticMultiSelectDropdown({
      displayName: "Included Sources",
      description: "Restrict search to specific source types.",
      required: !1,
      options: {
        options: [
          { label: "Web", value: "web" },
          { label: "Academic", value: "academic" },
          { label: "Finance", value: "finance" },
          { label: "Patent", value: "patent" },
          { label: "Transportation", value: "transportation" },
          { label: "Politics", value: "politics" },
          { label: "Legal", value: "legal" }
        ]
      }
    }),
    excluded_sources: import_pieces_framework6.Property.StaticMultiSelectDropdown({
      displayName: "Excluded Sources",
      description: "Exclude specific source types from search.",
      required: !1,
      options: {
        options: [
          { label: "Web", value: "web" },
          { label: "Academic", value: "academic" },
          { label: "Finance", value: "finance" },
          { label: "Patent", value: "patent" },
          { label: "Transportation", value: "transportation" },
          { label: "Politics", value: "politics" },
          { label: "Legal", value: "legal" }
        ]
      }
    }),
    start_date: import_pieces_framework6.Property.ShortText({
      displayName: "Start Date",
      description: "Filter content published on or after this date (YYYY-MM-DD).",
      required: !1
    }),
    end_date: import_pieces_framework6.Property.ShortText({
      displayName: "End Date",
      description: "Filter content published on or before this date (YYYY-MM-DD).",
      required: !1
    }),
    webhook_url: import_pieces_framework6.Property.ShortText({
      displayName: "Webhook URL",
      description: "HTTPS URL to receive notifications when the batch completes.",
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth, body = {};
    context.propsValue.name && (body.name = context.propsValue.name), context.propsValue.mode && (body.mode = context.propsValue.mode);
    let outputFormat = context.propsValue.output_format, structuredOptions = context.propsValue.structuredOptions;
    if (outputFormat === "markdown")
      body.output_formats = ["markdown"];
    else if (outputFormat === "markdown_pdf")
      body.output_formats = ["markdown", "pdf"];
    else if (outputFormat === "structured" && (structuredOptions != null && structuredOptions.json_schema))
      try {
        body.output_formats = [JSON.parse(structuredOptions.json_schema)];
      } catch (e) {
        throw new Error("Invalid JSON schema provided");
      }
    let search = {};
    return context.propsValue.search_type && (search.search_type = context.propsValue.search_type), context.propsValue.included_sources && context.propsValue.included_sources.length > 0 && (search.included_sources = context.propsValue.included_sources), context.propsValue.excluded_sources && context.propsValue.excluded_sources.length > 0 && (search.excluded_sources = context.propsValue.excluded_sources), context.propsValue.start_date && (search.start_date = context.propsValue.start_date), context.propsValue.end_date && (search.end_date = context.propsValue.end_date), Object.keys(search).length > 0 && (body.search = search), context.propsValue.webhook_url && (body.webhook_url = context.propsValue.webhook_url), await makeRequest(apiKey, import_pieces_common7.HttpMethod.POST, "/v1/deepresearch/batches", body);
  }
});

// ../ap/packages/pieces/community/valyu/src/lib/actions/list-datasources.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var listDatasourcesAction = (0, import_pieces_framework7.createAction)({
  name: "list_datasources",
  displayName: "List Datasources",
  description: "List all available datasources with metadata, schemas, and pricing. Useful for discovering available data sources.",
  audience: "both",
  aiMetadata: { description: "Lists the Valyu datasources available to the account, with their metadata, schemas, and pricing, optionally filtered by category. Use to discover which proprietary data sources exist before scoping a search or research task. Read-only listing, so repeating the call is safe.", idempotent: !0 },
  auth: valyuAuth,
  props: {
    category: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Category",
      description: "Filter datasources by category.",
      required: !1,
      options: {
        options: [
          { label: "All", value: "" },
          { label: "Research & Academic", value: "research" },
          { label: "Healthcare", value: "healthcare" },
          { label: "Patents", value: "patents" },
          { label: "Financial Markets", value: "markets" },
          { label: "Company", value: "company" },
          { label: "Economic", value: "economic" },
          { label: "Predictions", value: "predictions" },
          { label: "Transportation", value: "transportation" },
          { label: "Legal", value: "legal" },
          { label: "Politics", value: "politics" }
        ]
      }
    })
  },
  async run(context) {
    let apiKey = context.auth, queryParams = {};
    context.propsValue.category && (queryParams.category = context.propsValue.category);
    let queryString = Object.keys(queryParams).length > 0 ? "?" + new URLSearchParams(queryParams).toString() : "";
    return await makeRequest(apiKey.secret_text, import_pieces_common8.HttpMethod.GET, `/v1/datasources${queryString}`);
  }
});

// ../ap/packages/pieces/community/valyu/src/index.ts
var import_pieces_common9 = require("@activepieces/pieces-common");
var valyu = (0, import_pieces_framework8.createPiece)({
  displayName: "Valyu",
  description: "Search the web, research papers, and proprietary datasets with intelligent query processing.",
  auth: valyuAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/valyu.png",
  categories: [import_pieces_framework9.PieceCategory.ARTIFICIAL_INTELLIGENCE, import_pieces_framework9.PieceCategory.PRODUCTIVITY],
  authors: ["onyedikachi-david"],
  actions: [
    searchAction,
    extractContentAction,
    answerAction,
    createDeepResearchTaskAction,
    createBatchAction,
    listDatasourcesAction,
    (0, import_pieces_common9.createCustomApiCallAction)({
      auth: valyuAuth,
      baseUrl: () => "https://api.valyu.ai",
      authMapping: async (auth) => ({
        "x-api-key": `${auth}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  valyu
});
