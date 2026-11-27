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

// ../ap/packages/pieces/community/algolia/src/index.ts
var index_exports = {};
__export(index_exports, {
  algolia: () => algolia
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/algolia/src/lib/actions/browse-records.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/algolia/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/algolia/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function validateAlgoliaAuth(auth) {
  await listAlgoliaIndices({
    auth,
    limit: 1
  });
}
async function listAlgoliaIndices({
  auth,
  limit = 1e3
}) {
  let indices = [], page = 0;
  for (; indices.length < limit; ) {
    let remaining = limit - indices.length, response = await sendRequest({
      auth,
      method: import_pieces_common.HttpMethod.GET,
      path: "/indexes",
      queryParams: {
        page: String(page),
        hitsPerPage: String(Math.min(remaining, ALGOLIA_MAX_PAGE_SIZE))
      }
    });
    if (indices.push(...response.items), response.items.length === 0 || page + 1 >= response.nbPages)
      break;
    page += 1;
  }
  return indices;
}
async function saveAlgoliaRecords({
  auth,
  indexName,
  records
}) {
  let response = await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.POST,
    path: `/indexes/${encodeURIComponent(indexName)}/batch`,
    body: {
      requests: records.map((record) => ({
        action: "addObject",
        body: record
      }))
    }
  });
  return await waitForTask({
    auth,
    indexName,
    taskId: response.taskID
  }), response;
}
async function browseAlgoliaRecords({
  auth,
  indexName,
  limit = 1e4
}) {
  let records = [], cursor;
  do {
    let response = await sendRequest({
      auth,
      method: import_pieces_common.HttpMethod.POST,
      path: `/indexes/${encodeURIComponent(indexName)}/browse`,
      body: cursor ? { cursor } : {}
    });
    records.push(...response.hits), cursor = response.cursor;
  } while (cursor && records.length < limit);
  return records.slice(0, limit);
}
async function deleteAlgoliaRecords({
  auth,
  indexName,
  objectIDs
}) {
  let response = await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.POST,
    path: `/indexes/${encodeURIComponent(indexName)}/batch`,
    body: {
      requests: objectIDs.map((objectID) => ({
        action: "deleteObject",
        body: {
          objectID
        }
      }))
    }
  });
  return await waitForTask({
    auth,
    indexName,
    taskId: response.taskID
  }), response;
}
async function waitForTask({
  auth,
  indexName,
  taskId
}) {
  for (let attempt = 0; attempt < ALGOLIA_TASK_MAX_ATTEMPTS; attempt += 1) {
    if ((await sendRequest({
      auth,
      method: import_pieces_common.HttpMethod.GET,
      path: `/indexes/${encodeURIComponent(indexName)}/task/${taskId}`
    })).status === "published")
      return;
    await sleep(ALGOLIA_TASK_POLL_INTERVAL_MILLISECONDS);
  }
  throw new Error("The Algolia operation is taking too long. Try again or check your Algolia dashboard for the task status.");
}
async function sendRequest({
  auth,
  method,
  path,
  body,
  queryParams
}) {
  let request = __spreadValues(__spreadValues({
    method,
    url: `https://${auth.props.applicationId}.algolia.net/1${path}`,
    headers: {
      "x-algolia-application-id": auth.props.applicationId,
      "x-algolia-api-key": auth.props.apiKey,
      Accept: "application/json",
      "Content-Type": "application/json"
    }
  }, body ? { body } : {}), queryParams ? { queryParams } : {});
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}
function sleep(milliseconds) {
  return new Promise((resolve) => {
    setTimeout(resolve, milliseconds);
  });
}
var ALGOLIA_MAX_PAGE_SIZE = 1e3, ALGOLIA_TASK_MAX_ATTEMPTS = 30, ALGOLIA_TASK_POLL_INTERVAL_MILLISECONDS = 1e3;

// ../ap/packages/pieces/community/algolia/src/lib/common/auth.ts
var algoliaAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "Connection",
  description: "Authenticate with your Algolia Application ID and API key.\n\nTo get your credentials:\n1. Sign in to the Algolia dashboard.\n2. Open **Settings > API Keys**.\n3. Copy your **Application ID**.\n4. Copy an API key that can list indexes and manage records.\n\nThis piece needs permissions for `listIndexes`, `browse`, `addObject`, and `deleteObject`. Search-only API keys won't work for these actions.",
  required: !0,
  props: {
    applicationId: import_pieces_framework.Property.ShortText({
      displayName: "Application ID",
      description: "Your Algolia application ID.",
      required: !0
    }),
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "An Algolia API key with index browsing and record write permissions.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    let { error } = await (0, import_pieces_framework2.tryCatch)(
      () => validateAlgoliaAuth({
        props: {
          applicationId: auth.applicationId,
          apiKey: auth.apiKey
        }
      })
    );
    return error ? {
      valid: !1,
      error: "Invalid Algolia credentials or missing required index permissions."
    } : {
      valid: !0
    };
  }
});

// ../ap/packages/pieces/community/algolia/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/algolia/src/lib/common/utils.ts
function parseRequiredString(value, fieldName) {
  if (typeof value != "string" || value.trim().length === 0)
    throw new Error(`${fieldName} is required.`);
  return value.trim();
}
function parseStringArray(value, fieldName) {
  if (!Array.isArray(value) || value.length === 0)
    throw new Error(`${fieldName} must contain at least one value.`);
  return value.map((entry, index) => {
    if (typeof entry != "string" || entry.trim().length === 0)
      throw new Error(`${fieldName} item ${index + 1} must be a non-empty string.`);
    return entry.trim();
  });
}
function parseAlgoliaRecordArray(value) {
  if (!Array.isArray(value) || value.length === 0)
    throw new Error("Records must be a non-empty array of JSON objects.");
  return value.map((entry, index) => {
    if (!isAlgoliaRecord(entry))
      throw new Error(
        `Record ${index + 1} must be a JSON object with JSON-compatible values.`
      );
    return entry;
  });
}
function isAlgoliaAuthValue(value) {
  if (!isObject(value))
    return !1;
  let props = value.props;
  return isObject(props) ? typeof props.applicationId == "string" && props.applicationId.trim().length > 0 && typeof props.apiKey == "string" && props.apiKey.trim().length > 0 : !1;
}
function isAlgoliaRecord(value) {
  return isObject(value) ? Object.values(value).every(isAlgoliaJsonValue) : !1;
}
function isAlgoliaJsonValue(value) {
  return typeof value == "string" || typeof value == "number" || typeof value == "boolean" || value === null ? !0 : Array.isArray(value) ? value.every(isAlgoliaJsonValue) : isObject(value) && Object.values(value).every(isAlgoliaJsonValue);
}
function isObject(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}

// ../ap/packages/pieces/community/algolia/src/lib/common/props.ts
var algoliaProps = {
  index: (required = !0) => import_pieces_framework3.Property.Dropdown({
    auth: algoliaAuth,
    displayName: "Index",
    description: "Select the Algolia index to use.",
    required,
    refreshers: ["auth"],
    options: async ({ auth }) => {
      if (!isAlgoliaAuthValue(auth))
        return {
          disabled: !0,
          options: [],
          placeholder: "Connect your account first"
        };
      let { data: indices, error } = await (0, import_pieces_framework4.tryCatch)(
        () => listAlgoliaIndices({
          auth
        })
      );
      return error ? {
        disabled: !0,
        options: [],
        placeholder: "Failed to load indices. Check your connection."
      } : indices.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No indices found. Save records to a new index first."
      } : {
        disabled: !1,
        options: indices.map((index) => ({
          label: index.name,
          value: index.name
        }))
      };
    }
  })
};

// ../ap/packages/pieces/community/algolia/src/lib/actions/browse-records.ts
var browseRecordsAction = (0, import_pieces_framework5.createAction)({
  auth: algoliaAuth,
  name: "browse-records",
  classification: "SEARCH",
  displayName: "Browse Records",
  description: "Retrieves records from an Algolia index.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the raw records stored in an Algolia index, paging through them up to an optional limit (default 10,000). Use this to dump or inspect index contents; it is a read-only lookup, not a relevance-ranked query, so it does not accept a search term. Idempotent.",
    idempotent: !0
  },
  props: {
    indexName: algoliaProps.index(),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "Maximum number of records to retrieve. Defaults to 10,000.",
      required: !1,
      defaultValue: 1e4
    })
  },
  async run(context) {
    var _a;
    let indexName = parseRequiredString(context.propsValue.indexName, "Index");
    return browseAlgoliaRecords({
      auth: context.auth,
      indexName,
      limit: (_a = context.propsValue.limit) != null ? _a : 1e4
    });
  }
});

// ../ap/packages/pieces/community/algolia/src/lib/actions/delete-records.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var deleteRecordsAction = (0, import_pieces_framework6.createAction)({
  auth: algoliaAuth,
  name: "delete-records",
  classification: "DESTRUCTIVE",
  displayName: "Delete Records",
  description: "Deletes records from an Algolia index by objectID.",
  audience: "both",
  aiMetadata: {
    description: "Deletes records from an Algolia index by their objectIDs. Provide an array of the exact objectIDs to remove from the selected index. Idempotent: deleting the same IDs again leaves the index in the same state (already-absent IDs are simply no-ops).",
    idempotent: !0
  },
  props: {
    indexName: algoliaProps.index(),
    recordIds: import_pieces_framework6.Property.Array({
      displayName: "Record IDs",
      description: "Provide the Algolia objectIDs to delete from the selected index.",
      required: !0
    })
  },
  async run(context) {
    let indexName = parseRequiredString(context.propsValue.indexName, "Index"), objectIDs = parseStringArray(context.propsValue.recordIds, "Record IDs");
    return deleteAlgoliaRecords({
      auth: context.auth,
      indexName,
      objectIDs
    });
  }
});

// ../ap/packages/pieces/community/algolia/src/lib/actions/save-records.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var saveRecordsAction = (0, import_pieces_framework7.createAction)({
  auth: algoliaAuth,
  name: "save-records",
  classification: "WRITE",
  displayName: "Save Records",
  description: "Adds or updates records in an Algolia index.",
  audience: "both",
  aiMetadata: {
    description: "Adds or updates one or more records in an Algolia search index (creating the index if it does not yet exist). Pass an array of JSON objects; include an `objectID` on a record to overwrite that existing record, or omit it to have Algolia generate a new one. Not idempotent: records without an `objectID` create a fresh record on every call, so repeating the call appends duplicates.",
    idempotent: !1
  },
  props: {
    indexName: import_pieces_framework7.Property.ShortText({
      displayName: "Index Name",
      description: "Enter an existing index name or a new one \u2014 Algolia creates it automatically.",
      required: !0
    }),
    records: import_pieces_framework7.Property.Json({
      displayName: "Records",
      description: "Provide an array of JSON objects to save. Include `objectID` to update an existing record, or omit it to let Algolia create one.",
      required: !0
    })
  },
  async run(context) {
    let indexName = parseRequiredString(context.propsValue.indexName, "Index Name"), records = parseAlgoliaRecordArray(context.propsValue.records);
    return saveAlgoliaRecords({
      auth: context.auth,
      indexName,
      records
    });
  }
});

// ../ap/packages/pieces/community/algolia/src/index.ts
var algolia = (0, import_pieces_framework8.createPiece)({
  displayName: "Algolia",
  description: "Manage your Algolia search indices \u2014 add, browse, and delete records.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/algolia.png",
  categories: [import_pieces_framework9.PieceCategory.DEVELOPER_TOOLS],
  authors: ["veri5ied"],
  auth: algoliaAuth,
  actions: [
    saveRecordsAction,
    browseRecordsAction,
    deleteRecordsAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: algoliaAuth,
      baseUrl: (auth) => auth ? `https://${auth.props.applicationId}.algolia.net/1` : "",
      authMapping: async (auth) => ({
        "x-algolia-application-id": auth.props.applicationId,
        "x-algolia-api-key": auth.props.apiKey
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  algolia
});
