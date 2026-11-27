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

// ../ap/packages/pieces/community/omni-co/src/index.ts
var index_exports = {};
__export(index_exports, {
  omniCo: () => omniCo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/omni-co/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/omni-co/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://blobsrus.omniapp.co/api/v1";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/omni-co/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), omniAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Omni API Key",
  description: `
`,
  required: !0,
  validate: async ({ auth }) => {
    if (auth)
      try {
        return await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/models", {}), {
          valid: !0
        };
      } catch (error) {
        return {
          valid: !1,
          error: "Invalid Api Key"
        };
      }
    return {
      valid: !1,
      error: "Invalid Api Key"
    };
  }
});

// ../ap/packages/pieces/community/omni-co/src/lib/actions/create-a-document.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/omni-co/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var modelIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: omniAuth,
  displayName: "Model ID",
  description: "Select the model containing the database",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/models",
          {}
        )).records.map((model) => ({
          label: model.name,
          value: model.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading teams"
      };
    }
  }
}), documentIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: omniAuth,
  displayName: "Document",
  description: "Select the document",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/documents",
          {}
        )).records.map((document) => ({
          label: document.name,
          value: document.identifier
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading documents"
      };
    }
  }
}), scheduleIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: omniAuth,
  displayName: "Schedule",
  description: "Select the schedule",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/schedules",
          {}
        )).records.map((schedule) => ({
          label: schedule.name,
          value: schedule.identifier
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading schedules"
      };
    }
  }
});

// ../ap/packages/pieces/community/omni-co/src/lib/actions/create-a-document.ts
var createADocument = (0, import_pieces_framework3.createAction)({
  auth: omniAuth,
  name: "createADocument",
  displayName: "Create a document",
  description: "Creates a new document",
  audience: "both",
  aiMetadata: {
    description: "Creates a new Omni document (workbook) under a given model, optionally seeding it with query presentations. Use to programmatically provision a document for analysis or dashboards. Not idempotent: each call creates a separate document even with identical inputs.",
    idempotent: !1
  },
  props: {
    modelId: modelIdDropdown,
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Document Name",
      description: "The name of the document",
      required: !0
    }),
    queryPresentations: import_pieces_framework3.Property.Array({
      displayName: "Query Presentations",
      description: "An array of queryPresentation objects, each representing a query in the document's workbook",
      required: !1
    })
  },
  async run(context) {
    let { modelId, name, queryPresentations } = context.propsValue, body = {
      modelId,
      name
    };
    return queryPresentations && (body.queryPresentations = queryPresentations), await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/documents",
      body
    );
  }
});

// ../ap/packages/pieces/community/omni-co/src/lib/actions/create-a-schedule.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), createASchedule = (0, import_pieces_framework4.createAction)({
  auth: omniAuth,
  name: "createASchedule",
  displayName: "Create a schedule",
  description: "Creates a scheduled task for a dashboard with support for email, SFTP, and webhook destinations",
  audience: "both",
  aiMetadata: {
    description: "Creates a scheduled delivery for an Omni dashboard, sending output on a cron-defined cadence to one of three destination types (email, webhook, or SFTP) in a chosen format. Use to automate recurring report delivery. The required destination type determines which optional fields apply (e.g. recipients for email, url for webhook). Not idempotent: each call creates a new schedule even with identical inputs.",
    idempotent: !1
  },
  props: {
    identifier: import_pieces_framework4.Property.ShortText({
      displayName: "Dashboard Identifier",
      description: " The string after /dashboards is the dashboards ID; for example: https://blobsrus.omniapp.co/dashboards/12db1a0a",
      required: !0
    }),
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Schedule Name",
      description: "The name of the schedule/task",
      required: !0
    }),
    schedule: import_pieces_framework4.Property.ShortText({
      displayName: "Schedule (Cron Expression)",
      description: 'Cron expression for the schedule (e.g., "0 9 ? * * *" for 9 AM UTC daily) https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-scheduled-rule-pattern.html',
      required: !0
    }),
    timezone: import_pieces_framework4.Property.ShortText({
      displayName: "Timezone",
      description: 'Timezone for the schedule (e.g., "UTC", "America/New_York") Refer : https://data.iana.org/time-zones/tzdb-2021a/zone1970.tab',
      required: !0
    }),
    format: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Format",
      description: "Output format for the schedule",
      options: {
        disabled: !1,
        options: [
          { label: "Link Only", value: "link_only" },
          { label: "PDF", value: "pdf" },
          { label: "PNG", value: "png" },
          { label: "CSV", value: "csv" },
          { label: "XLSX", value: "xlsx" },
          { label: "JSON", value: "json" }
        ]
      },
      required: !0
    }),
    destinationType: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Destination Type",
      description: "Type of destination for the schedule",
      options: {
        disabled: !1,
        options: [
          { label: "Email", value: "email" },
          { label: "Webhook", value: "webhook" },
          { label: "SFTP", value: "sftp" }
        ]
      },
      required: !0
    }),
    url: import_pieces_framework4.Property.ShortText({
      displayName: "URL",
      description: "Webhook URL (required when destinationType is webhook)",
      required: !1
    }),
    recipients: import_pieces_framework4.Property.Array({
      displayName: "Recipients",
      description: "Email recipients (required when destinationType is email)",
      required: !1
    }),
    subject: import_pieces_framework4.Property.ShortText({
      displayName: "Email Subject",
      description: "Subject line for email delivery",
      required: !1
    }),
    paperFormat: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Paper Format",
      description: "Paper format for PDF output",
      options: {
        disabled: !1,
        options: [
          { label: "Letter", value: "letter" },
          { label: "A4", value: "a4" }
        ]
      },
      required: !1
    }),
    paperOrientation: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Paper Orientation",
      description: "Paper orientation for PDF output",
      options: {
        disabled: !1,
        options: [
          { label: "Portrait", value: "portrait" },
          { label: "Landscape", value: "landscape" }
        ]
      },
      required: !1
    })
  },
  async run(context) {
    let {
      identifier,
      name,
      schedule,
      timezone,
      format,
      destinationType,
      url,
      recipients,
      subject,
      paperFormat,
      paperOrientation
    } = context.propsValue, body = {
      identifier,
      name,
      schedule,
      timezone,
      format,
      destinationType
    };
    return url && (body.url = url), recipients && (body.recipients = recipients), subject && (body.subject = subject), paperFormat && (body.paperFormat = paperFormat), paperOrientation && (body.paperOrientation = paperOrientation), await makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      "/schedules",
      body
    );
  }
});

// ../ap/packages/pieces/community/omni-co/src/lib/actions/delete-a-document.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var deleteADocument = (0, import_pieces_framework5.createAction)({
  auth: omniAuth,
  name: "deleteADocument",
  displayName: "Delete a document",
  description: "Deletes the specified document and places it in the Trash",
  audience: "both",
  aiMetadata: {
    description: "Deletes an Omni document by its ID, moving it to the Trash. Use to remove a document the agent has located. Effectively idempotent: once the document is trashed, repeating the call with the same ID has no further effect (subsequent calls may error if the ID no longer resolves).",
    idempotent: !0
  },
  props: {
    documentId: documentIdDropdown
  },
  async run(context) {
    let { documentId } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.DELETE,
      `/documents/${documentId}`,
      {}
    );
  }
});

// ../ap/packages/pieces/community/omni-co/src/lib/actions/delete-a-schedule.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common");
var deleteASchedule = (0, import_pieces_framework6.createAction)({
  auth: omniAuth,
  name: "deleteASchedule",
  displayName: "Delete a schedule",
  description: "Deletes a schedule using its UUID",
  audience: "both",
  aiMetadata: {
    description: "Deletes an Omni dashboard schedule by its UUID. Use to stop a recurring delivery the agent has identified. Effectively idempotent: once removed, repeating the call with the same UUID has no further effect.",
    idempotent: !0
  },
  props: {
    scheduleId: scheduleIdDropdown
  },
  async run(context) {
    let { scheduleId } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common7.HttpMethod.DELETE,
      `/schedules/${scheduleId}`,
      {}
    );
  }
});

// ../ap/packages/pieces/community/omni-co/src/lib/actions/move-document.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common");
var moveDocument = (0, import_pieces_framework7.createAction)({
  auth: omniAuth,
  name: "moveDocument",
  displayName: "Move document",
  description: "Moves a document to a new folder or to the root level",
  audience: "both",
  aiMetadata: {
    description: 'Moves an Omni document to a target folder path, or to the root level when the path is set to "null". Optionally sets the sharing scope (organization or restricted). Use to reorganize where a document lives. Idempotent: moving a document to the same destination repeatedly yields the same placement.',
    idempotent: !0
  },
  props: {
    identifier: documentIdDropdown,
    folderPath: import_pieces_framework7.Property.ShortText({
      displayName: "Folder Path",
      description: "The path of the destination folder. Use null to move the document to the root level (no folder)",
      required: !0
    }),
    scope: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Scope",
      description: "Optional sharing scope for the document. If not provided, the scope will be computed",
      options: {
        disabled: !1,
        options: [
          { label: "Organization", value: "organization" },
          { label: "Restricted", value: "restricted" }
        ]
      },
      required: !1
    })
  },
  async run(context) {
    let { identifier, folderPath, scope } = context.propsValue, body = {
      folderPath: folderPath === "null" ? null : folderPath
    };
    return scope && (body.scope = scope), await makeRequest(
      context.auth.secret_text,
      import_pieces_common8.HttpMethod.PUT,
      `/documents/${identifier}/move`,
      body
    );
  }
});

// ../ap/packages/pieces/community/omni-co/src/lib/actions/run-query.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), runQuery = (0, import_pieces_framework8.createAction)({
  auth: omniAuth,
  name: "runQuery",
  displayName: "Run query",
  description: "Runs a query and returns the results as base64 encoded Apache Arrow table or in specified format",
  audience: "both",
  aiMetadata: {
    description: "Executes an Omni query (passed as a JSON query object) and returns the result rows, in base64 Apache Arrow by default or in CSV/JSON/XLSX when a result type is set; can instead return only the execution plan without running. Use to read data from Omni for analysis or downstream steps. Read-only and idempotent: it fetches data and does not mutate state. Requires a valid query JSON object (obtainable from an Omni workbook Inspector panel).",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework8.Property.Json({
      displayName: "Query",
      description: "A JSON object representing the query to be run. You can retrieve a query's JSON object from an Omni workbook by opening the Inspector panel (Option + 9 on Mac, Alt + 9 on Windows) and copying the Query structure",
      required: !0
    }),
    limit: import_pieces_framework8.Property.Number({
      displayName: "Limit",
      description: "Number of rows to be returned (defaults to 1000). Set to null for unlimited results",
      required: !1
    }),
    cache: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Cache Policy",
      description: "Optional cache policy to control how query caching behaves",
      options: {
        disabled: !1,
        options: [
          { label: "Standard", value: "Standard" },
          { label: "Skip Requery (default)", value: "SkipRequery" },
          { label: "Skip Cache", value: "SkipCache" }
        ]
      },
      required: !1
    }),
    formatResults: import_pieces_framework8.Property.Checkbox({
      displayName: "Format Results",
      description: "Apply formatting to numeric and currency values (currency symbols and thousand separators)",
      required: !1,
      defaultValue: !0
    }),
    resultType: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Result Type",
      description: "Specifies the format of query results. If omitted, returns base64 encoded Apache Arrow format",
      options: {
        disabled: !1,
        options: [
          { label: "CSV", value: "csv" },
          { label: "JSON", value: "json" },
          { label: "XLSX", value: "xlsx" }
        ]
      },
      required: !1
    }),
    planOnly: import_pieces_framework8.Property.Checkbox({
      displayName: "Plan Only",
      description: "If enabled, returns the query execution plan without running the query",
      required: !1,
      defaultValue: !1
    }),
    userId: import_pieces_framework8.Property.ShortText({
      displayName: "User ID",
      description: "Optional UUID to run the query as a specific user",
      required: !1
    })
  },
  async run(context) {
    let { query, limit, cache, formatResults, resultType, planOnly, userId } = context.propsValue, body = {
      query
    };
    return limit != null && (body.limit = limit), cache && (body.cache = cache), formatResults !== void 0 && (body.formatResults = formatResults), resultType && (body.resultType = resultType), planOnly && (body.planOnly = planOnly), userId && (body.userId = userId), await makeRequest(
      context.auth.secret_text,
      import_pieces_common9.HttpMethod.POST,
      "/query/run",
      body
    );
  }
});

// ../ap/packages/pieces/community/omni-co/src/lib/actions/edit-schedule.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common");
var editSchedule = (0, import_pieces_framework9.createAction)({
  auth: omniAuth,
  name: "editSchedule",
  displayName: "Edit schedule",
  description: "Updates a scheduled task. Only properties included will be updated",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Omni dashboard schedule, identified by its UUID, replacing its cadence, format, and destination settings. Use to modify a recurring delivery the agent has located rather than recreating it. Idempotent: applying the same values to the same schedule yields the same end state.",
    idempotent: !0
  },
  props: {
    scheduleId: scheduleIdDropdown,
    name: import_pieces_framework9.Property.ShortText({
      displayName: "Schedule Name",
      description: "The name of the schedule/task",
      required: !0
    }),
    schedule: import_pieces_framework9.Property.ShortText({
      displayName: "Schedule (Cron Expression)",
      description: 'Cron expression for the schedule (e.g., "0 9 ? * * *" for 9 AM UTC daily) https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-scheduled-rule-pattern.html',
      required: !0
    }),
    timezone: import_pieces_framework9.Property.ShortText({
      displayName: "Timezone",
      description: 'Timezone for the schedule (e.g., "UTC", "America/New_York") Refer : https://data.iana.org/time-zones/tzdb-2021a/zone1970.tab',
      required: !0
    }),
    format: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Format",
      description: "Output format for the schedule",
      options: {
        disabled: !1,
        options: [
          { label: "Link Only", value: "link_only" },
          { label: "PDF", value: "pdf" },
          { label: "PNG", value: "png" },
          { label: "CSV", value: "csv" },
          { label: "XLSX", value: "xlsx" },
          { label: "JSON", value: "json" }
        ]
      },
      required: !0
    }),
    destinationType: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Destination Type",
      description: "Type of destination for the schedule",
      options: {
        disabled: !1,
        options: [
          { label: "Email", value: "email" },
          { label: "Webhook", value: "webhook" },
          { label: "SFTP", value: "sftp" }
        ]
      },
      required: !0
    }),
    url: import_pieces_framework9.Property.ShortText({
      displayName: "URL",
      description: "Webhook URL (required when destinationType is webhook)",
      required: !1
    }),
    recipients: import_pieces_framework9.Property.Array({
      displayName: "Recipients",
      description: "Email recipients (required when destinationType is email)",
      required: !1
    }),
    subject: import_pieces_framework9.Property.ShortText({
      displayName: "Email Subject",
      description: "Subject line for email delivery",
      required: !1
    }),
    paperFormat: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Paper Format",
      description: "Paper format for PDF output",
      options: {
        disabled: !1,
        options: [
          { label: "Letter", value: "letter" },
          { label: "A4", value: "a4" }
        ]
      },
      required: !1
    }),
    paperOrientation: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Paper Orientation",
      description: "Paper orientation for PDF output",
      options: {
        disabled: !1,
        options: [
          { label: "Portrait", value: "portrait" },
          { label: "Landscape", value: "landscape" }
        ]
      },
      required: !1
    })
  },
  async run(context) {
    let {
      scheduleId,
      name,
      schedule,
      timezone,
      format,
      destinationType,
      url,
      recipients,
      subject,
      paperFormat,
      paperOrientation
    } = context.propsValue, body = {};
    return name && (body.name = name), schedule && (body.schedule = schedule), timezone && (body.timezone = timezone), format && (body.format = format), destinationType && (body.destinationType = destinationType), url && (body.url = url), recipients && (body.recipients = recipients), subject && (body.subject = subject), paperFormat && (body.paperFormat = paperFormat), paperOrientation && (body.paperOrientation = paperOrientation), await makeRequest(
      context.auth.secret_text,
      import_pieces_common10.HttpMethod.PUT,
      `/schedules/${scheduleId}`,
      body
    );
  }
});

// ../ap/packages/pieces/community/omni-co/src/lib/actions/generate-query.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common");
var generateQuery = (0, import_pieces_framework10.createAction)({
  auth: omniAuth,
  name: "generateQuery",
  displayName: "Generate query",
  description: "Transforms a natural language description into a structured Omni query",
  audience: "both",
  aiMetadata: {
    description: "Generates a structured Omni query object from a natural-language prompt against a given model, optionally guided by a topic, model branch, or a context query, and can emit a more strictly structured format. Use to translate a plain-English analytics request into a runnable query (then pass the result to Run query). Not idempotent: AI generation may return different output across calls for the same prompt.",
    idempotent: !1
  },
  props: {
    modelId: modelIdDropdown,
    prompt: import_pieces_framework10.Property.LongText({
      displayName: "Prompt",
      description: 'The natural language description of the query you want to generate (e.g., "Show me all users who signed up in the last month")',
      required: !0
    }),
    currentTopicName: import_pieces_framework10.Property.ShortText({
      displayName: "Current Topic Name",
      description: "The name of the topic to use as context for the query generation",
      required: !1
    }),
    branchId: import_pieces_framework10.Property.ShortText({
      displayName: "Branch ID",
      description: "The ID of the model branch to use for query generation. If not provided, the main branch will be used",
      required: !1
    }),
    contextQuery: import_pieces_framework10.Property.Json({
      displayName: "Context Query",
      description: "The query object to provide as context. This can be used to reference previous queries or provide additional context for the generation",
      required: !1
    }),
    structured: import_pieces_framework10.Property.Checkbox({
      displayName: "Structured Output",
      description: "When enabled, the API will return a more strictly structured query format",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      modelId,
      prompt,
      currentTopicName,
      branchId,
      contextQuery,
      structured
    } = context.propsValue, body = {
      modelId,
      prompt
    };
    return currentTopicName && (body.currentTopicName = currentTopicName), branchId && (body.branchId = branchId), contextQuery && (body.contextQuery = contextQuery), structured && (body.structured = structured), await makeRequest(
      context.auth.secret_text,
      import_pieces_common11.HttpMethod.POST,
      "/ai/generate-query",
      body
    );
  }
});

// ../ap/packages/pieces/community/omni-co/src/index.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), omniCo = (0, import_pieces_framework11.createPiece)({
  displayName: "Omni",
  auth: omniAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/omni-co.png",
  categories: [import_pieces_framework12.PieceCategory.BUSINESS_INTELLIGENCE],
  authors: ["sanket-a11y"],
  actions: [
    createADocument,
    createASchedule,
    deleteADocument,
    deleteASchedule,
    editSchedule,
    generateQuery,
    moveDocument,
    runQuery,
    (0, import_pieces_common12.createCustomApiCallAction)({
      baseUrl: () => "https://blobsrus.omniapp.co/api/v1",
      auth: omniAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  omniCo
});
