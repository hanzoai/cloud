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

// ../ap/packages/pieces/community/bubble/src/index.ts
var index_exports = {};
__export(index_exports, {
  bubble: () => bubble
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bubble/src/lib/actions/create-thing.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bubble/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), bubbleAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `Enter Bubble Connection Details
  In the bubble editor click Settings > API 
  1. Your app name is https://appname.bubbleapps.io
  2. Enter/Generate an API key
  `,
  props: {
    appname: import_pieces_framework.Property.ShortText({
      displayName: "App name",
      description: "Enter the app name",
      required: !0
    }),
    token: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Token",
      description: "Enter the access token",
      required: !0
    })
  },
  // Optional Validation
  validate: async ({ auth }) => auth ? {
    valid: !0
  } : {
    valid: !1,
    error: "Please enter a valid app name and token"
  },
  required: !0
});

// ../ap/packages/pieces/community/bubble/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), bubbleCommon = {
  typename: import_pieces_framework2.Property.ShortText({
    displayName: "Typename",
    required: !0
  }),
  fields: import_pieces_framework2.Property.Json({
    displayName: "Body",
    required: !1
  }),
  thing_id: import_pieces_framework2.Property.ShortText({
    displayName: "Thing ID",
    required: !0
  })
};

// ../ap/packages/pieces/community/bubble/src/lib/actions/create-thing.ts
var bubbleCreateThingAction = (0, import_pieces_framework3.createAction)({
  auth: bubbleAuth,
  name: "bubble_create_thing",
  classification: "WRITE",
  displayName: "Create Thing",
  description: "Create a thing",
  audience: "both",
  aiMetadata: { description: 'Create a new record ("thing") of a given data type in a Bubble app via the Bubble Data API. Use to add a new entry; supply the type name and a fields object with the values to set. Not idempotent \u2014 each call creates a separate record.', idempotent: !1 },
  props: {
    typename: bubbleCommon.typename,
    fields: bubbleCommon.fields
  },
  async run(context) {
    let { appname, token } = context.auth.props, { typename, fields } = context.propsValue, server_url = `https://${appname}.bubbleapps.io/api/1.1/obj/${typename}`;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: server_url,
      headers: {
        "user-agent": "activepieces",
        Authorization: `Bearer ${token}`
      },
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token
      },
      body: fields
    })).body;
  }
});

// ../ap/packages/pieces/community/bubble/src/lib/actions/delete-thing.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var bubbleDeleteThingAction = (0, import_pieces_framework4.createAction)({
  auth: bubbleAuth,
  name: "bubble_delete_thing",
  classification: "DESTRUCTIVE",
  displayName: "Delete Thing",
  description: "Delete a thing",
  audience: "both",
  aiMetadata: { description: 'Permanently delete a single record ("thing") from a Bubble app by its unique id and data type, via the Bubble Data API. Use when an agent must remove a specific record it already knows the id of. Effectively idempotent (deleting an already-deleted id is a no-op) but destructive \u2014 verify the id before calling.', idempotent: !0 },
  props: {
    typename: bubbleCommon.typename,
    thing_id: bubbleCommon.thing_id
  },
  async run(context) {
    let { appname, token } = context.auth.props, { typename, thing_id } = context.propsValue, server_url = `https://${appname}.bubbleapps.io/api/1.1/obj/${typename}/${thing_id}`;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.DELETE,
      url: server_url,
      headers: {
        "user-agent": "activepieces",
        Authorization: `Bearer ${token}`
      },
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/bubble/src/lib/actions/get-thing.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var bubbleGetThingAction = (0, import_pieces_framework5.createAction)({
  auth: bubbleAuth,
  name: "bubble_get_thing",
  classification: "READ",
  displayName: "Get Thing",
  description: "Get a thing by id",
  audience: "both",
  aiMetadata: { description: 'Fetch a single record ("thing") of a given data type from a Bubble app by its unique id, via the Bubble Data API. Use to read the current values of one known record. Read-only and idempotent.', idempotent: !0 },
  props: {
    typename: bubbleCommon.typename,
    thing_id: bubbleCommon.thing_id
  },
  async run(context) {
    let { appname, token } = context.auth.props, { typename, thing_id } = context.propsValue, server_url = `https://${appname}.bubbleapps.io/api/1.1/obj/${typename}`;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: server_url,
      headers: {
        "user-agent": "activepieces",
        Authorization: `Bearer ${token}`
      },
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token
      },
      body: {
        constraint: `id = ${thing_id}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/bubble/src/lib/actions/list-things.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var bubbleListThingsAction = (0, import_pieces_framework6.createAction)({
  auth: bubbleAuth,
  name: "bubble_list_things",
  classification: "SEARCH",
  displayName: "List Thing",
  description: "List things by type",
  audience: "both",
  aiMetadata: { description: 'Search and list records ("things") of a given data type in a Bubble app via the Bubble Data API, filtering by a single constraint (e.g. equals, text contains, greater/less than, in, empty, geographic_search) on a chosen field and value, with cursor and limit for pagination. Use to find records matching a condition or to page through a type. Read-only and idempotent.', idempotent: !0 },
  props: {
    typename: bubbleCommon.typename,
    constraint: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Constraint",
      required: !0,
      options: {
        options: [
          {
            label: "equals or not equal",
            value: "equals or not equal"
          },
          {
            label: "text contains or not text contains",
            value: "text contains or not text contains"
          },
          {
            label: "greater than or less than",
            value: "greater than or less than"
          },
          {
            label: "in or not in",
            value: "in or not in"
          },
          {
            label: "contains or not contains",
            value: "contains or not contains"
          },
          {
            label: "empty or not empty",
            value: "empty or not empty"
          },
          {
            label: "geographic_search",
            value: "geographic_search"
          }
        ]
      }
    }),
    field: import_pieces_framework6.Property.ShortText({
      displayName: "Field",
      required: !0
    }),
    value: import_pieces_framework6.Property.ShortText({
      displayName: "Value",
      required: !0
    }),
    cursor: import_pieces_framework6.Property.Number({
      displayName: "Start from",
      required: !0
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      required: !0
    })
  },
  async run(context) {
    let { appname, token } = context.auth.props, { typename, constraint, field, value, cursor, limit } = context.propsValue, server_url = `https://${appname}.bubbleapps.io/api/1.1/obj/${typename}`;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: server_url,
      headers: {
        "user-agent": "activepieces",
        Authorization: `Bearer ${token}`
      },
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token
      },
      body: {
        constraint,
        field,
        value,
        cursor,
        limit
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/bubble/src/lib/actions/update-thing.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var bubbleUpdateThingAction = (0, import_pieces_framework7.createAction)({
  auth: bubbleAuth,
  name: "bubble_update_thing",
  classification: "WRITE",
  displayName: "Update Thing",
  description: "Updates a thing",
  audience: "both",
  aiMetadata: { description: 'Update an existing record ("thing") in a Bubble app, identified by its unique id and data type, by patching the supplied fields via the Bubble Data API. Use to modify specific fields of a known record; only the provided fields are changed. Targets one record so repeats with the same input converge, but it mutates server state on each call.', idempotent: !1 },
  props: {
    typename: bubbleCommon.typename,
    thing_id: bubbleCommon.thing_id,
    fields: bubbleCommon.fields
  },
  async run(context) {
    let { appname, token } = context.auth.props, { typename, thing_id } = context.propsValue, server_url = `https://${appname}.bubbleapps.io/api/1.1/obj/${typename}/${thing_id}`;
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.PATCH,
      url: server_url,
      headers: {
        "user-agent": "activepieces",
        Authorization: `Bearer ${token}`
      },
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token
      },
      body: context.propsValue.fields
    })).body;
  }
});

// ../ap/packages/pieces/community/bubble/src/index.ts
var bubble = (0, import_pieces_framework8.createPiece)({
  displayName: "Bubble",
  description: "No-code platform for web and mobile apps",
  auth: import_pieces_framework8.PieceAuth.None(),
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/bubble.png",
  categories: [import_pieces_framework9.PieceCategory.DEVELOPER_TOOLS],
  authors: ["TaskMagicKyle", "kishanprmr", "abuaboud"],
  actions: [
    bubbleCreateThingAction,
    bubbleDeleteThingAction,
    bubbleUpdateThingAction,
    bubbleGetThingAction,
    bubbleListThingsAction
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  bubble
});
