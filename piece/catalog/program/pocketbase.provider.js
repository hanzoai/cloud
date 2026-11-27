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

// ../ap/packages/pieces/community/pocketbase/src/index.ts
var index_exports = {};
__export(index_exports, {
  pocketbase: () => pocketbase,
  pocketbaseAuth: () => pocketbaseAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/pocketbase/src/lib/actions/get-full-list.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/pocketbase/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
function normalizeHost(host) {
  return host.replace(/\/+$/, "");
}
async function pocketbaseAuthenticate(host, email, password) {
  var _a;
  let token = (_a = (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${host}/api/collections/_superusers/auth-with-password`,
    body: {
      identity: email,
      password
    }
  })).body) == null ? void 0 : _a.token;
  if (!token)
    throw new Error("Authentication failed: no token returned from PocketBase.");
  return token;
}

// ../ap/packages/pieces/community/pocketbase/src/lib/actions/get-full-list.ts
var getFullList = (0, import_pieces_framework.createAction)({
  name: "getFullList",
  displayName: "Get Full List",
  description: "Gets all the data for a given collection",
  audience: "both",
  aiMetadata: { description: "Fetches every record in a PocketBase collection, auto-paginating through all pages so the full set is returned in one call. Optionally narrow with a filter expression, sort, or field selection; with no filter it returns the entire collection. Use when you need the complete dataset rather than a single page; for large collections prefer Get List to page through results. Read-only and idempotent.", idempotent: !0 },
  auth: pocketbaseAuth,
  props: {
    collection: import_pieces_framework.Property.ShortText({
      displayName: "Collection Name",
      description: "The name of the PocketBase collection",
      required: !0
    }),
    sort: import_pieces_framework.Property.ShortText({
      displayName: "Sort",
      description: "Order attribute(s). Use - for DESC, + for ASC. E.g.: -created,id",
      required: !1
    }),
    filter: import_pieces_framework.Property.ShortText({
      displayName: "Filter",
      description: "Filter expression. E.g.: (id='abc' && created>'2022-01-01')",
      required: !1
    }),
    expand: import_pieces_framework.Property.ShortText({
      displayName: "Expand",
      description: "Auto expand relations. E.g.: relField1,relField2.subRelField",
      required: !1
    }),
    fields: import_pieces_framework.Property.ShortText({
      displayName: "Fields",
      description: "Comma separated fields to return. E.g.: *,expand.relField.name",
      required: !1
    })
  },
  async run(context) {
    let { host: rawHost, email, password } = context.auth.props, host = normalizeHost(rawHost), { collection, sort, filter, expand, fields } = context.propsValue, token = await pocketbaseAuthenticate(host, email, password), allItems = [], page = 1, perPage = 200, serverTotalItems = 0;
    for (; ; ) {
      let queryParams = {
        page: String(page),
        perPage: String(perPage),
        skipTotal: "false"
      };
      sort && (queryParams.sort = sort), filter && (queryParams.filter = filter), expand && (queryParams.expand = expand), fields && (queryParams.fields = fields);
      let body = (await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `${host}/api/collections/${encodeURIComponent(collection)}/records`,
        headers: { Authorization: `Bearer ${token}` },
        queryParams
      })).body;
      if (!body.items || body.items.length === 0 || (serverTotalItems = body.totalItems, allItems = allItems.concat(body.items), allItems.length >= body.totalItems)) break;
      page++;
    }
    return { items: allItems, totalItems: serverTotalItems };
  }
});

// ../ap/packages/pieces/community/pocketbase/src/lib/actions/get-list.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getList = (0, import_pieces_framework2.createAction)({
  name: "getList",
  displayName: "Get List",
  description: "Gets a paginated list of records from a collection",
  audience: "both",
  aiMetadata: { description: "Retrieves a single page of records from a PocketBase collection, controlled by page and per-page size, with optional filter expression, sort, and field selection. Use when you want bounded results or to iterate page by page; for the entire collection in one call use Get Full List instead. Read-only and idempotent.", idempotent: !0 },
  auth: pocketbaseAuth,
  props: {
    collection: import_pieces_framework2.Property.ShortText({
      displayName: "Collection Name",
      description: "The name of the PocketBase collection",
      required: !0
    }),
    page: import_pieces_framework2.Property.Number({
      displayName: "Page",
      description: "The page (offset) of the paginated list (default: 1)",
      required: !1
    }),
    perPage: import_pieces_framework2.Property.Number({
      displayName: "Per Page",
      description: "Max returned records per page (default: 30)",
      required: !1
    }),
    sort: import_pieces_framework2.Property.ShortText({
      displayName: "Sort",
      description: "Order attribute(s). Use - for DESC, + for ASC. E.g.: -created,id",
      required: !1
    }),
    filter: import_pieces_framework2.Property.ShortText({
      displayName: "Filter",
      description: "Filter expression. E.g.: (id='abc' && created>'2022-01-01')",
      required: !1
    }),
    expand: import_pieces_framework2.Property.ShortText({
      displayName: "Expand",
      description: "Auto expand relations. E.g.: relField1,relField2.subRelField",
      required: !1
    }),
    fields: import_pieces_framework2.Property.ShortText({
      displayName: "Fields",
      description: "Comma separated fields to return. E.g.: *,expand.relField.name",
      required: !1
    }),
    skipTotal: import_pieces_framework2.Property.Checkbox({
      displayName: "Skip Total",
      description: "Skip total counts query for faster results (totalItems and totalPages will be -1)",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { host: rawHost, email, password } = context.auth.props, host = normalizeHost(rawHost), { collection, page, perPage, sort, filter, expand, fields, skipTotal } = context.propsValue, token = await pocketbaseAuthenticate(host, email, password), queryParams = {};
    return page && (queryParams.page = String(page)), perPage && (queryParams.perPage = String(perPage)), sort && (queryParams.sort = sort), filter && (queryParams.filter = filter), expand && (queryParams.expand = expand), fields && (queryParams.fields = fields), skipTotal && (queryParams.skipTotal = "true"), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `${host}/api/collections/${encodeURIComponent(collection)}/records`,
      headers: {
        Authorization: `Bearer ${token}`
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/pocketbase/src/lib/actions/get-record.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getRecord = (0, import_pieces_framework3.createAction)({
  name: "getRecord",
  displayName: "Get Record",
  description: "Gets a single record by ID from a collection",
  audience: "both",
  aiMetadata: { description: "Fetches one record from a PocketBase collection by its record ID, optionally expanding relations or limiting returned fields. Use when you already know the exact record ID; to search or list by criteria use Get List or Get Full List instead. Read-only and idempotent.", idempotent: !0 },
  auth: pocketbaseAuth,
  props: {
    collection: import_pieces_framework3.Property.ShortText({
      displayName: "Collection Name",
      description: "The name of the PocketBase collection",
      required: !0
    }),
    recordId: import_pieces_framework3.Property.ShortText({
      displayName: "Record ID",
      description: "ID of the record to view",
      required: !0
    }),
    expand: import_pieces_framework3.Property.ShortText({
      displayName: "Expand",
      description: "Auto expand relations. E.g.: relField1,relField2.subRelField",
      required: !1
    }),
    fields: import_pieces_framework3.Property.ShortText({
      displayName: "Fields",
      description: "Comma separated fields to return. E.g.: *,expand.relField.name",
      required: !1
    })
  },
  async run(context) {
    let { host: rawHost, email, password } = context.auth.props, host = normalizeHost(rawHost), { collection, recordId, expand, fields } = context.propsValue, token = await pocketbaseAuthenticate(host, email, password), queryParams = {};
    return expand && (queryParams.expand = expand), fields && (queryParams.fields = fields), (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `${host}/api/collections/${encodeURIComponent(collection)}/records/${encodeURIComponent(recordId)}`,
      headers: {
        Authorization: `Bearer ${token}`
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/pocketbase/src/lib/actions/create-record.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createRecord = (0, import_pieces_framework4.createAction)({
  name: "createRecord",
  displayName: "Create Record",
  description: "Creates a new record in a collection",
  audience: "both",
  aiMetadata: { description: "Creates a new record in a PocketBase collection from a JSON object of field values. Use to insert data; this is not idempotent \u2014 each call creates a distinct record (PocketBase assigns a fresh ID), so repeating it produces duplicates. To change an existing record use Update Record instead.", idempotent: !1 },
  auth: pocketbaseAuth,
  props: {
    collection: import_pieces_framework4.Property.ShortText({
      displayName: "Collection Name",
      description: "The name of the PocketBase collection",
      required: !0
    }),
    recordData: import_pieces_framework4.Property.Json({
      displayName: "Record Data",
      description: 'JSON object with the fields to create. E.g.: { "title": "test", "score": 123 }',
      required: !0
    }),
    expand: import_pieces_framework4.Property.ShortText({
      displayName: "Expand",
      description: "Auto expand relations. E.g.: relField1,relField2.subRelField",
      required: !1
    }),
    fields: import_pieces_framework4.Property.ShortText({
      displayName: "Fields",
      description: "Comma separated fields to return. E.g.: *,expand.relField.name",
      required: !1
    })
  },
  async run(context) {
    let { host: rawHost, email, password } = context.auth.props, host = normalizeHost(rawHost), { collection, recordData, expand, fields } = context.propsValue, token = await pocketbaseAuthenticate(host, email, password), queryParams = {};
    return expand && (queryParams.expand = expand), fields && (queryParams.fields = fields), (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: `${host}/api/collections/${encodeURIComponent(collection)}/records`,
      headers: {
        Authorization: `Bearer ${token}`
      },
      queryParams,
      body: recordData
    })).body;
  }
});

// ../ap/packages/pieces/community/pocketbase/src/lib/actions/update-record.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var updateRecord = (0, import_pieces_framework5.createAction)({
  name: "updateRecord",
  displayName: "Update Record",
  description: "Updates a single record in a collection",
  audience: "both",
  aiMetadata: { description: "Updates an existing record in a PocketBase collection, identified by its record ID, applying a JSON object of field values as a partial patch (only supplied fields change). Use when the record already exists and you know its ID; to insert a new record use Create Record instead. Idempotent \u2014 re-applying the same field values to the same ID yields the same result.", idempotent: !0 },
  auth: pocketbaseAuth,
  props: {
    collection: import_pieces_framework5.Property.ShortText({
      displayName: "Collection Name",
      description: "The name of the PocketBase collection",
      required: !0
    }),
    recordId: import_pieces_framework5.Property.ShortText({
      displayName: "Record ID",
      description: "ID of the record to update",
      required: !0
    }),
    recordData: import_pieces_framework5.Property.Json({
      displayName: "Record Data",
      description: 'JSON object with the fields to update. E.g.: { "title": "test", "score": 123 }',
      required: !0
    }),
    expand: import_pieces_framework5.Property.ShortText({
      displayName: "Expand",
      description: "Auto expand relations. E.g.: relField1,relField2.subRelField",
      required: !1
    }),
    fields: import_pieces_framework5.Property.ShortText({
      displayName: "Fields",
      description: "Comma separated fields to return. E.g.: *,expand.relField.name",
      required: !1
    })
  },
  async run(context) {
    let { host: rawHost, email, password } = context.auth.props, host = normalizeHost(rawHost), { collection, recordId, recordData, expand, fields } = context.propsValue, token = await pocketbaseAuthenticate(host, email, password), queryParams = {};
    return expand && (queryParams.expand = expand), fields && (queryParams.fields = fields), (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.PATCH,
      url: `${host}/api/collections/${encodeURIComponent(collection)}/records/${encodeURIComponent(recordId)}`,
      headers: {
        Authorization: `Bearer ${token}`
      },
      queryParams,
      body: recordData
    })).body;
  }
});

// ../ap/packages/pieces/community/pocketbase/src/lib/actions/delete-record.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var deleteRecord = (0, import_pieces_framework6.createAction)({
  name: "deleteRecord",
  displayName: "Delete Record",
  description: "Deletes a single record from a collection",
  audience: "both",
  aiMetadata: { description: "Permanently deletes one record from a PocketBase collection by its record ID. Use when you need to remove a specific record you can identify by ID. Idempotent on the end state \u2014 the record ends up absent \u2014 though a repeat call against an already-deleted ID will error since the record no longer exists.", idempotent: !0 },
  auth: pocketbaseAuth,
  props: {
    collection: import_pieces_framework6.Property.ShortText({
      displayName: "Collection Name",
      description: "The name of the PocketBase collection",
      required: !0
    }),
    recordId: import_pieces_framework6.Property.ShortText({
      displayName: "Record ID",
      description: "ID of the record to delete",
      required: !0
    })
  },
  async run(context) {
    let { host: rawHost, email, password } = context.auth.props, host = normalizeHost(rawHost), { collection, recordId } = context.propsValue, token = await pocketbaseAuthenticate(host, email, password);
    return await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.DELETE,
      url: `${host}/api/collections/${encodeURIComponent(collection)}/records/${encodeURIComponent(recordId)}`,
      headers: {
        Authorization: `Bearer ${token}`
      }
    }), { success: !0 };
  }
});

// ../ap/packages/pieces/community/pocketbase/src/index.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), markdown = `
Provide your PocketBase **host URL** and **superuser credentials**.

Find your admin panel at \`https://your-host/_/\`.
`, pocketbaseAuth = import_pieces_framework7.PieceAuth.CustomAuth({
  required: !0,
  description: markdown,
  props: {
    host: import_pieces_framework7.Property.ShortText({
      displayName: "Host",
      description: "Your Pocketbase host URL (e.g., https://pocketbase.your-project.com)",
      required: !0
    }),
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Superuser Email",
      description: "The email address of your Pocketbase superuser",
      required: !0
    }),
    password: import_pieces_framework7.PieceAuth.SecretText({
      displayName: "Superuser Password",
      description: "The password of your Pocketbase superuser",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    var _a, _b;
    try {
      let host = auth.host.replace(/\/+$/, ""), { email, password } = auth;
      try {
        let parsedUrl = new URL(host);
        if (parsedUrl.protocol !== "https:" && parsedUrl.protocol !== "http:")
          return {
            valid: !1,
            error: "Host URL must use HTTP or HTTPS protocol"
          };
      } catch (e) {
        return {
          valid: !1,
          error: "Please enter a valid URL"
        };
      }
      try {
        return (_a = (await import_pieces_common8.httpClient.sendRequest({
          method: import_pieces_common8.HttpMethod.POST,
          url: `${host}/api/collections/_superusers/auth-with-password`,
          body: {
            identity: email,
            password
          }
        })).body) != null && _a.token ? { valid: !0 } : {
          valid: !1,
          error: "Authentication failed. Please check your superuser email and password."
        };
      } catch (requestError) {
        let status = (_b = requestError == null ? void 0 : requestError.response) == null ? void 0 : _b.status;
        return status === 400 || status === 401 || status === 403 ? {
          valid: !1,
          error: "Invalid superuser email or password."
        } : {
          valid: !1,
          error: `Could not connect to PocketBase: ${requestError instanceof Error ? requestError.message : "Unknown error"}. Please check your host URL.`
        };
      }
    } catch (error) {
      return {
        valid: !1,
        error: `Failed to connect to PocketBase: ${error instanceof Error ? error.message : "Unknown error"}. Please check your credentials.`
      };
    }
  }
}), pocketbase = (0, import_pieces_framework7.createPiece)({
  displayName: "Pocketbase",
  description: "Interact with your PocketBase instance using superuser credentials.",
  auth: pocketbaseAuth,
  categories: [import_pieces_framework8.PieceCategory.DEVELOPER_TOOLS],
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/pocketbase.png",
  authors: ["anasaijaz"],
  actions: [getFullList, getList, getRecord, createRecord, updateRecord, deleteRecord],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pocketbase,
  pocketbaseAuth
});
