"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/goodmem/src/index.ts
var index_exports = {};
__export(index_exports, {
  goodmem: () => goodmem,
  goodmemAuth: () => goodmemAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/goodmem/src/lib/actions/create-memory.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/goodmem/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
function extractAuthFromContext(auth) {
  if (typeof auth == "string")
    throw new Error("Invalid auth format");
  let actualAuth = auth.props;
  if (!actualAuth || !actualAuth.baseUrl || !actualAuth.apiKey)
    throw new Error(
      "Invalid authentication: Base URL and API Key are required"
    );
  return {
    baseUrl: actualAuth.baseUrl,
    apiKey: actualAuth.apiKey
  };
}
function getBaseUrl(baseUrl) {
  return baseUrl.replace(/\/$/, "");
}
function getCommonHeaders(apiKey) {
  return {
    "X-API-Key": apiKey,
    "Content-Type": "application/json",
    Accept: "application/json"
  };
}
var spaceIdDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Space",
  description: "Select a space to use",
  required: !0,
  refreshers: ["auth"],
  auth: goodmemAuth,
  async options({ auth }) {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your GoodMem account first",
        options: []
      };
    try {
      let { baseUrl, apiKey } = auth.props, baseUrlClean = baseUrl.replace(/\/$/, ""), body = (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${baseUrlClean}/v1/spaces`,
        headers: getCommonHeaders(apiKey)
      })).body;
      return {
        disabled: !1,
        options: (Array.isArray(body) ? body : (_a = body == null ? void 0 : body.spaces) != null ? _a : []).map((s) => ({
          label: s.name,
          value: s.spaceId
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load spaces. Check your connection.",
        options: []
      };
    }
  }
}), multiSpaceDropdown = import_pieces_framework.Property.MultiSelectDropdown({
  displayName: "Spaces",
  description: "Select one or more spaces to search across",
  required: !0,
  refreshers: ["auth"],
  auth: goodmemAuth,
  async options({ auth }) {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your GoodMem account first",
        options: []
      };
    try {
      let { baseUrl, apiKey } = auth.props, baseUrlClean = baseUrl.replace(/\/$/, ""), body = (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${baseUrlClean}/v1/spaces`,
        headers: getCommonHeaders(apiKey)
      })).body;
      return {
        disabled: !1,
        options: (Array.isArray(body) ? body : (_a = body == null ? void 0 : body.spaces) != null ? _a : []).map((s) => ({
          label: s.name,
          value: s.spaceId
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load spaces. Check your connection.",
        options: []
      };
    }
  }
}), rerankerDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Reranker",
  description: "Optional reranker model to improve result ordering",
  required: !1,
  refreshers: ["auth"],
  auth: goodmemAuth,
  async options({ auth }) {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your GoodMem account first",
        options: []
      };
    try {
      let { baseUrl, apiKey } = auth.props, baseUrlClean = baseUrl.replace(/\/$/, ""), body = (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${baseUrlClean}/v1/rerankers`,
        headers: getCommonHeaders(apiKey)
      })).body;
      return {
        disabled: !1,
        options: (Array.isArray(body) ? body : (_a = body == null ? void 0 : body.rerankers) != null ? _a : []).map((r) => ({
          label: r.displayName,
          value: r.rerankerId
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load rerankers. Check your connection.",
        options: []
      };
    }
  }
}), llmDropdown = import_pieces_framework.Property.Dropdown(
  {
    displayName: "LLM",
    description: "Optional LLM to generate contextual responses alongside retrieved chunks",
    required: !1,
    refreshers: ["auth"],
    auth: goodmemAuth,
    async options({ auth }) {
      var _a;
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Connect your GoodMem account first",
          options: []
        };
      try {
        let { baseUrl, apiKey } = auth.props, baseUrlClean = baseUrl.replace(/\/$/, ""), body = (await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `${baseUrlClean}/v1/llms`,
          headers: getCommonHeaders(apiKey)
        })).body;
        return {
          disabled: !1,
          options: (Array.isArray(body) ? body : (_a = body == null ? void 0 : body.llms) != null ? _a : []).map((l) => ({
            label: l.displayName,
            value: l.llmId
          }))
        };
      } catch (error) {
        return {
          disabled: !0,
          placeholder: "Failed to load LLMs. Check your connection.",
          options: []
        };
      }
    }
  }
);

// ../ap/packages/pieces/community/goodmem/src/lib/actions/create-memory.ts
var createMemory = (0, import_pieces_framework2.createAction)({
  auth: goodmemAuth,
  name: "create_memory",
  displayName: "Create Memory",
  description: "Store a document as a new memory in a space. The memory is processed asynchronously - chunked into searchable pieces and embedded into vectors. Accepts a file or plain text.",
  audience: "both",
  aiMetadata: { description: "Stores content as a new memory in a GoodMem space, which is then chunked and embedded asynchronously for later semantic retrieval. Provide the content either as a file or as plain text (the file takes priority when both are given), along with the target space ID. Use it to ingest documents into a memory store. Not idempotent: each call creates a separate memory, so repeated calls produce duplicates.", idempotent: !1 },
  props: {
    spaceId: spaceIdDropdown,
    file: import_pieces_framework2.Property.File({
      displayName: "File",
      description: "A file to store as memory (PDF, DOCX, image, etc.). Connect the output of a trigger or action that provides a file. Content type is auto-detected from the file extension.",
      required: !1
    }),
    textContent: import_pieces_framework2.Property.LongText({
      displayName: "Text Content",
      description: "Plain text content to store as memory (sent as text/plain). If both File and Text Content are provided, the file takes priority.",
      required: !1
    }),
    source: import_pieces_framework2.Property.ShortText({
      displayName: "Source",
      description: 'Where this memory came from (e.g., "google-drive", "gmail", "manual upload"). Stored in metadata.source',
      required: !1
    }),
    author: import_pieces_framework2.Property.ShortText({
      displayName: "Author",
      description: "The author or creator of the content. Stored in metadata.author",
      required: !1
    }),
    tags: import_pieces_framework2.Property.ShortText({
      displayName: "Tags",
      description: 'Comma-separated tags for categorization (e.g., "legal,research,important"). Stored in metadata.tags as an array',
      required: !1
    }),
    metadata: import_pieces_framework2.Property.Json({
      displayName: "Additional Metadata",
      description: "Extra key-value metadata as JSON. Merged with Source, Author, and Tags fields above",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { spaceId, file, textContent, source, author, tags, metadata } = context.propsValue, { baseUrl: rawBaseUrl, apiKey } = extractAuthFromContext(context.auth), baseUrl = getBaseUrl(rawBaseUrl), requestBody = {
      spaceId
    };
    if (file && file.base64) {
      let mimeType = (file.extension ? getMimeType(file.extension) : null) || "application/octet-stream";
      if (mimeType.startsWith("text/")) {
        let decoded = Buffer.from(file.base64, "base64").toString("utf-8");
        requestBody.contentType = mimeType, requestBody.originalContent = decoded;
      } else
        requestBody.contentType = mimeType, requestBody.originalContentB64 = file.base64;
    } else if (textContent)
      requestBody.contentType = "text/plain", requestBody.originalContent = textContent;
    else
      return {
        success: !1,
        error: "No content provided. Please provide a file or text content."
      };
    let mergedMetadata = {};
    metadata && typeof metadata == "object" && Object.keys(metadata).length > 0 && Object.assign(mergedMetadata, metadata), source && (mergedMetadata.source = source), author && (mergedMetadata.author = author), tags && (mergedMetadata.tags = tags.split(",").map((t) => t.trim()).filter((t) => t.length > 0)), Object.keys(mergedMetadata).length > 0 && (requestBody.metadata = mergedMetadata);
    try {
      return (await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.POST,
        url: `${baseUrl}/v1/memories`,
        headers: getCommonHeaders(apiKey),
        body: requestBody
      })).body;
    } catch (error) {
      return {
        success: !1,
        error: error.message || "Failed to create memory",
        details: ((_a = error.response) == null ? void 0 : _a.body) || error
      };
    }
  }
});
function getMimeType(extension) {
  return {
    pdf: "application/pdf",
    png: "image/png",
    jpg: "image/jpeg",
    jpeg: "image/jpeg",
    gif: "image/gif",
    webp: "image/webp",
    txt: "text/plain",
    html: "text/html",
    md: "text/markdown",
    csv: "text/csv",
    json: "application/json",
    xml: "application/xml",
    doc: "application/msword",
    docx: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    xls: "application/vnd.ms-excel",
    xlsx: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    ppt: "application/vnd.ms-powerpoint",
    pptx: "application/vnd.openxmlformats-officedocument.presentationml.presentation"
  }[extension.toLowerCase().replace(".", "")] || null;
}

// ../ap/packages/pieces/community/goodmem/src/lib/actions/retrieve-memories.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var retrieveMemories = (0, import_pieces_framework3.createAction)({
  auth: goodmemAuth,
  name: "retrieve_memories",
  displayName: "Retrieve Memories",
  description: "Perform similarity-based semantic retrieval across one or more spaces. Returns matching chunks ranked by relevance, with optional full memory definitions.",
  audience: "both",
  aiMetadata: { description: "Runs a semantic (vector-similarity) search over one or more GoodMem spaces using a natural-language query and returns the matching memory chunks ranked by relevance, optionally reranked or LLM-post-processed. Use it to recall stored knowledge relevant to a question; requires at least one space ID. Read-only and idempotent, though by default it retries for up to 60 seconds when no results are found so freshly added memories have time to finish indexing.", idempotent: !0 },
  props: {
    query: import_pieces_framework3.Property.LongText({
      displayName: "Query",
      description: "A natural language query used to find semantically similar memory chunks",
      required: !0
    }),
    spaceIds: multiSpaceDropdown,
    maxResults: import_pieces_framework3.Property.Number({
      displayName: "Maximum Results",
      description: "Limit the number of returned memories",
      required: !1,
      defaultValue: 5
    }),
    includeMemoryDefinition: import_pieces_framework3.Property.Checkbox({
      displayName: "Include Memory Definition",
      description: "Fetch the full memory metadata (source document info, processing status) alongside the matched chunks",
      required: !1,
      defaultValue: !0
    }),
    waitForIndexing: import_pieces_framework3.Property.Checkbox({
      displayName: "Wait for Indexing",
      description: "Retry for up to 60 seconds when no results are found. Enable this when memories were just added and may still be undergoing chunking and embedding",
      required: !1,
      defaultValue: !0
    }),
    rerankerId: rerankerDropdown,
    llmId: llmDropdown,
    relevanceThreshold: import_pieces_framework3.Property.Number({
      displayName: "Relevance Threshold",
      description: "Minimum score (0-1) for including results. Only used when Reranker or LLM is set.",
      required: !1
    }),
    llmTemperature: import_pieces_framework3.Property.Number({
      displayName: "LLM Temperature",
      description: "Creativity setting for LLM generation (0-2). Only used when LLM ID is set.",
      required: !1
    }),
    chronologicalResort: import_pieces_framework3.Property.Checkbox({
      displayName: "Chronological Resort",
      description: "Reorder results by creation time instead of relevance score",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a;
    let { query, spaceIds, maxResults, includeMemoryDefinition, waitForIndexing, rerankerId, llmId, relevanceThreshold, llmTemperature, chronologicalResort } = context.propsValue, { baseUrl: rawBaseUrl, apiKey } = extractAuthFromContext(context.auth), baseUrl = getBaseUrl(rawBaseUrl), spaceKeys = (Array.isArray(spaceIds) ? spaceIds : [spaceIds]).filter((id) => id && id.length > 0).map((spaceId) => ({ spaceId }));
    if (spaceKeys.length === 0)
      return {
        success: !1,
        error: "At least one space must be selected."
      };
    let requestBody = {
      message: query,
      spaceKeys,
      requestedSize: maxResults || 5,
      fetchMemory: includeMemoryDefinition !== !1
    };
    if (rerankerId || llmId) {
      let config = {};
      rerankerId && (config.reranker_id = rerankerId), llmId && (config.llm_id = llmId), relevanceThreshold != null && (config.relevance_threshold = relevanceThreshold), llmTemperature != null && (config.llm_temp = llmTemperature), maxResults && (config.max_results = maxResults), chronologicalResort === !0 && (config.chronological_resort = !0), requestBody.postProcessor = {
        name: "com.goodmem.retrieval.postprocess.ChatPostProcessorFactory",
        config
      };
    }
    let maxWaitMs = 6e4, pollIntervalMs = 5e3, shouldWait = waitForIndexing !== !1, startTime = Date.now(), lastResult = null;
    try {
      do {
        let retrieveHeaders = __spreadProps(__spreadValues({}, getCommonHeaders(apiKey)), {
          Accept: "application/x-ndjson"
        }), response = await import_pieces_common3.httpClient.sendRequest({
          method: import_pieces_common3.HttpMethod.POST,
          url: `${baseUrl}/v1/memories:retrieve`,
          headers: retrieveHeaders,
          body: requestBody
        }), results = [], memories = [], resultSetId = "", abstractReply = null;
        if ((typeof response.body == "string" ? response.body : JSON.stringify(response.body)).trim().split(`
`).forEach((line) => {
          var _a2, _b, _c;
          let jsonStr = line.trim();
          if (jsonStr && (jsonStr.startsWith("data:") && (jsonStr = jsonStr.substring(5).trim()), !(jsonStr.startsWith("event:") || jsonStr === "")))
            try {
              let item = JSON.parse(jsonStr);
              item.resultSetBoundary ? resultSetId = item.resultSetBoundary.resultSetId : item.memoryDefinition ? memories.push(item.memoryDefinition) : item.abstractReply ? abstractReply = item.abstractReply : item.retrievedItem && results.push({
                chunkId: (_a2 = item.retrievedItem.chunk) == null ? void 0 : _a2.chunkId,
                chunkText: (_b = item.retrievedItem.chunk) == null ? void 0 : _b.chunkText,
                memoryId: (_c = item.retrievedItem.chunk) == null ? void 0 : _c.memoryId,
                relevanceScore: item.retrievedItem.relevanceScore,
                memoryIndex: item.retrievedItem.memoryIndex
              });
            } catch (parseError) {
            }
        }), lastResult = __spreadValues({
          success: !0,
          resultSetId,
          results,
          memories,
          totalResults: results.length,
          query
        }, abstractReply ? { abstractReply } : {}), results.length > 0 || !shouldWait)
          return lastResult;
        if (Date.now() - startTime >= maxWaitMs)
          return __spreadProps(__spreadValues({}, lastResult), {
            message: "No results found after waiting 60 seconds for indexing. Memories may still be processing."
          });
        await new Promise((resolve) => setTimeout(resolve, pollIntervalMs));
      } while (!0);
    } catch (error) {
      return {
        success: !1,
        error: error.message || "Failed to retrieve memories",
        details: ((_a = error.response) == null ? void 0 : _a.body) || error
      };
    }
  }
});

// ../ap/packages/pieces/community/goodmem/src/lib/actions/delete-memory.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var deleteMemory = (0, import_pieces_framework4.createAction)({
  auth: goodmemAuth,
  name: "delete_memory",
  displayName: "Delete Memory",
  description: "Permanently delete a memory and its associated chunks and vector embeddings.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a GoodMem memory and its associated chunks and vector embeddings, identified by memory ID. Use it to remove stored content from a space. This is destructive but idempotent: once the memory is gone, repeating the call with the same ID leaves the end state unchanged.", idempotent: !0 },
  props: {
    memoryId: import_pieces_framework4.Property.ShortText({
      displayName: "Memory ID",
      description: "The UUID of the memory to delete (returned by Create Memory)",
      required: !0
    })
  },
  async run(context) {
    var _a;
    let { memoryId } = context.propsValue, { baseUrl: rawBaseUrl, apiKey } = extractAuthFromContext(context.auth), baseUrl = getBaseUrl(rawBaseUrl);
    try {
      return await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.DELETE,
        url: `${baseUrl}/v1/memories/${memoryId}`,
        headers: getCommonHeaders(apiKey)
      }), {
        success: !0,
        memoryId,
        message: "Memory deleted successfully"
      };
    } catch (error) {
      return {
        success: !1,
        error: error.message || "Failed to delete memory",
        details: ((_a = error.response) == null ? void 0 : _a.body) || error
      };
    }
  }
});

// ../ap/packages/pieces/community/goodmem/src/lib/actions/get-memory.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getMemory = (0, import_pieces_framework5.createAction)({
  auth: goodmemAuth,
  name: "get_memory",
  displayName: "Get Memory",
  description: "Fetch a specific memory record by its ID, including metadata, processing status, and optionally the original content.",
  audience: "both",
  aiMetadata: { description: "Looks up a single GoodMem memory by its ID and returns its metadata and processing status, optionally including the original document content. Use it when you already have a memory ID (e.g. from Create Memory or a retrieval result) and need its full details. Read-only and idempotent.", idempotent: !0 },
  props: {
    memoryId: import_pieces_framework5.Property.ShortText({
      displayName: "Memory ID",
      description: "The UUID of the memory to fetch (returned by Create Memory)",
      required: !0
    }),
    includeContent: import_pieces_framework5.Property.Checkbox({
      displayName: "Include Content",
      description: "Fetch the original document content of the memory in addition to its metadata",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    var _a;
    let { memoryId, includeContent } = context.propsValue, { baseUrl: rawBaseUrl, apiKey } = extractAuthFromContext(context.auth), baseUrl = getBaseUrl(rawBaseUrl);
    try {
      return {
        success: !0,
        memory: (await import_pieces_common5.httpClient.sendRequest({
          method: import_pieces_common5.HttpMethod.GET,
          url: `${baseUrl}/v1/memories/${memoryId}?includeContent=${includeContent != null ? includeContent : !1}`,
          headers: getCommonHeaders(apiKey)
        })).body
      };
    } catch (error) {
      return {
        success: !1,
        error: error.message || "Failed to get memory",
        details: ((_a = error.response) == null ? void 0 : _a.body) || error
      };
    }
  }
});

// ../ap/packages/pieces/community/goodmem/src/lib/actions/create-space.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createSpace = (0, import_pieces_framework6.createAction)({
  auth: goodmemAuth,
  name: "create_space",
  displayName: "Create Space",
  description: "Create a new space or reuse an existing one. A space is a logical container for organizing related memories, configured with embedders that convert text to vector embeddings",
  audience: "both",
  aiMetadata: { description: "Provisions a GoodMem space (a named container of memories with a configured embedder model) and returns its ID, which is required before storing or retrieving memories. Use it to set up a target space when one does not already exist. It is idempotent on the space name: if a space with the same name already exists, its existing ID is returned instead of creating a duplicate.", idempotent: !0 },
  props: {
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Space Name",
      description: "A unique name for the space. If a space with this name already exists, its ID will be returned instead of creating a duplicate",
      required: !0
    }),
    embedderId: import_pieces_framework6.Property.Dropdown({
      displayName: "Embedder",
      description: "The embedder model that converts text into vector representations for similarity search",
      required: !0,
      refreshers: [],
      auth: goodmemAuth,
      async options({ auth }) {
        if (!auth)
          return { disabled: !0, placeholder: "Connect your GoodMem account first", options: [] };
        try {
          let authProps = auth.props || auth, baseUrl = (authProps.baseUrl || "").replace(/\/$/, ""), apiKey = authProps.apiKey || "", body = (await import_pieces_common6.httpClient.sendRequest({
            method: import_pieces_common6.HttpMethod.GET,
            url: `${baseUrl}/v1/embedders`,
            headers: {
              "X-API-Key": apiKey,
              "Content-Type": "application/json",
              Accept: "application/json"
            }
          })).body;
          return {
            disabled: !1,
            options: (Array.isArray(body) ? body : (body == null ? void 0 : body.embedders) || []).map((e) => ({
              label: `${e.displayName || e.name || e.modelIdentifier || "Unnamed"} (${e.modelIdentifier || e.model || "unknown"})`,
              value: e.embedderId || e.id
            }))
          };
        } catch (error) {
          return { disabled: !0, placeholder: "Failed to load embedders. Check your connection.", options: [] };
        }
      }
    }),
    advancedChunking: import_pieces_framework6.Property.DynamicProperties({
      displayName: "Advanced Chunking Options",
      required: !1,
      refreshers: [],
      auth: import_pieces_framework6.PieceAuth.None(),
      props: async () => ({
        chunkSize: import_pieces_framework6.Property.Number({
          displayName: "Chunk Size",
          description: "Number of characters per chunk when splitting documents",
          required: !1,
          defaultValue: 256
        }),
        chunkOverlap: import_pieces_framework6.Property.Number({
          displayName: "Chunk Overlap",
          description: "Number of overlapping characters between consecutive chunks",
          required: !1,
          defaultValue: 25
        }),
        keepStrategy: import_pieces_framework6.Property.StaticDropdown({
          displayName: "Keep Separator Strategy",
          description: "Where to attach the separator when splitting",
          required: !1,
          defaultValue: "KEEP_END",
          options: {
            disabled: !1,
            options: [
              { label: "Keep at End (default)", value: "KEEP_END" },
              { label: "Keep at Start", value: "KEEP_START" },
              { label: "Discard", value: "DISCARD" }
            ]
          }
        }),
        lengthMeasurement: import_pieces_framework6.Property.StaticDropdown({
          displayName: "Length Measurement",
          description: "How chunk size is measured",
          required: !1,
          defaultValue: "CHARACTER_COUNT",
          options: {
            disabled: !1,
            options: [
              { label: "Character Count (default)", value: "CHARACTER_COUNT" },
              { label: "Token Count", value: "TOKEN_COUNT" }
            ]
          }
        })
      })
    })
  },
  async run(context) {
    var _a;
    let { name, embedderId, advancedChunking } = context.propsValue, { baseUrl: rawBaseUrl, apiKey } = extractAuthFromContext(context.auth), baseUrl = getBaseUrl(rawBaseUrl), chunkSize = (advancedChunking == null ? void 0 : advancedChunking.chunkSize) || 256, chunkOverlap = (advancedChunking == null ? void 0 : advancedChunking.chunkOverlap) || 25, keepStrategy = (advancedChunking == null ? void 0 : advancedChunking.keepStrategy) || "KEEP_END", lengthMeasurement = (advancedChunking == null ? void 0 : advancedChunking.lengthMeasurement) || "CHARACTER_COUNT";
    try {
      let body = (await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.GET,
        url: `${baseUrl}/v1/spaces`,
        headers: getCommonHeaders(apiKey)
      })).body, spaces = Array.isArray(body) ? body : (body == null ? void 0 : body.spaces) || [];
      if (spaces.length > 0) {
        let existing = spaces.find((s) => s.name === name);
        if (existing)
          return {
            success: !0,
            spaceId: existing.spaceId,
            name: existing.name,
            embedderId,
            message: "Space already exists, reusing existing space",
            reused: !0
          };
      }
    } catch (listError) {
    }
    let requestBody = {
      name,
      spaceEmbedders: [{ embedderId, defaultRetrievalWeight: 1 }],
      defaultChunkingConfig: {
        recursive: {
          chunkSize,
          chunkOverlap,
          separators: [`

`, `
`, ". ", " ", ""],
          keepStrategy,
          separatorIsRegex: !1,
          lengthMeasurement
        }
      }
    };
    try {
      let response = await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.POST,
        url: `${baseUrl}/v1/spaces`,
        headers: getCommonHeaders(apiKey),
        body: requestBody
      });
      return {
        success: !0,
        spaceId: response.body.spaceId,
        name: response.body.name,
        embedderId,
        chunkingConfig: requestBody.defaultChunkingConfig,
        message: "Space created successfully",
        reused: !1
      };
    } catch (error) {
      return {
        success: !1,
        error: error.message || "Failed to create space",
        details: ((_a = error.response) == null ? void 0 : _a.body) || error
      };
    }
  }
});

// ../ap/packages/pieces/community/goodmem/src/index.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), goodmemAuth = import_pieces_framework7.PieceAuth.CustomAuth({
  displayName: "GoodMem Authentication",
  description: "Connect to your GoodMem API instance for vector-based memory storage and semantic retrieval",
  props: {
    baseUrl: import_pieces_framework7.Property.ShortText({
      displayName: "Base URL",
      description: "The base URL of your GoodMem API server (e.g., https://api.goodmem.ai or http://localhost:8080)",
      required: !0
    }),
    apiKey: import_pieces_framework7.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your GoodMem API key for authentication (X-API-Key)",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      let baseUrl = auth.baseUrl.replace(/\/$/, "");
      return await import_pieces_common7.httpClient.sendRequest({
        method: import_pieces_common7.HttpMethod.GET,
        url: `${baseUrl}/v1/spaces`,
        headers: {
          "X-API-Key": auth.apiKey,
          "Content-Type": "application/json"
        }
      }), {
        valid: !0
      };
    } catch (error) {
      return {
        valid: !1,
        error: "Invalid API key or base URL. Please check your credentials."
      };
    }
  },
  required: !0
}), goodmem = (0, import_pieces_framework7.createPiece)({
  displayName: "GoodMem",
  description: "Store documents as memories with vector embeddings and perform similarity-based semantic retrieval using GoodMem",
  auth: goodmemAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/goodmem.png",
  categories: [import_pieces_framework8.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["bashareid", "sanket-a11y"],
  actions: [
    createSpace,
    createMemory,
    retrieveMemories,
    getMemory,
    deleteMemory
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  goodmem,
  goodmemAuth
});
