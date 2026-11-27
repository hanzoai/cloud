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

// ../ap/packages/pieces/community/mem/src/index.ts
var index_exports = {};
__export(index_exports, {
  mem: () => mem
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mem/src/lib/actions/create-mem.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mem/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), memAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "You can obtain your API key by navigating to **Integrations\u2192 API**."
});

// ../ap/packages/pieces/community/mem/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function makeRequest(apiKey, method, path, body) {
  let url = `https://api.mem.ai/v2${path}`;
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url,
    headers: {
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/mem/src/lib/actions/create-mem.ts
var createMemAction = (0, import_pieces_framework2.createAction)({
  auth: memAuth,
  name: "create_mem",
  displayName: "Create Mem",
  description: "Save any content to Mem.ai for intelligent processing and future reference.",
  audience: "both",
  aiMetadata: {
    description: `Sends raw content (HTML, emails, transcripts, or plain notes) to Mem.ai's "mem-it" endpoint, where it is intelligently processed and filed into the knowledge base; optionally accepts processing instructions and background context. Use to capture unstructured material and let Mem decide how to organize it, rather than writing a specific note. Not idempotent: each call ingests the content anew.`,
    idempotent: !1
  },
  props: {
    input: import_pieces_framework2.Property.LongText({
      displayName: "Input",
      required: !0,
      description: "Raw content you want to remember - HTML, emails, transcripts, or simple notes."
    }),
    instructions: import_pieces_framework2.Property.LongText({
      displayName: "Instructions",
      required: !1,
      description: "Optional guidance on how you want this content processed or remembered."
    }),
    context: import_pieces_framework2.Property.LongText({
      displayName: "Context",
      required: !1,
      description: "Additional background information to relate this to your existing knowledge."
    })
  },
  async run(context) {
    let { input, instructions, context: contextInfo } = context.propsValue, apiKey = context.auth.secret_text, body = __spreadValues(__spreadValues({
      input
    }, instructions ? { instructions } : {}), contextInfo ? { context: contextInfo } : {});
    return await makeRequest(
      apiKey,
      import_pieces_common2.HttpMethod.POST,
      "/mem-it",
      body
    );
  }
});

// ../ap/packages/pieces/community/mem/src/lib/actions/create-note.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var createNoteAction = (0, import_pieces_framework3.createAction)({
  auth: memAuth,
  name: "create_note",
  displayName: "Create Note",
  description: "Log a plain-text Markdown note into Mem, optionally with formatting, templates, collections, and timestamps.",
  audience: "both",
  aiMetadata: {
    description: "Creates a Markdown note in Mem, using the first line as the title, and can assign it to one or more collections (creating them if they do not exist). Use when you want to store a specific, ready-formatted note rather than hand raw content to Mem's processor. Not idempotent: a new note is created on each call, even when a Note ID is supplied.",
    idempotent: !1
  },
  props: {
    content: import_pieces_framework3.Property.LongText({
      displayName: "Content",
      required: !0,
      description: "Markdown-formatted content. First line is treated as the note title."
    }),
    id: import_pieces_framework3.Property.ShortText({
      displayName: "Note ID",
      required: !1,
      description: "Optional UUID to assign to the note."
    }),
    add_to_collections: import_pieces_framework3.Property.Array({
      displayName: "Add to Collections",
      required: !1,
      description: "Collection titles or IDs to assign this note to. New collections will be created if they don\u2019t exist."
    })
  },
  async run(context) {
    let {
      content,
      id,
      add_to_collections
    } = context.propsValue, apiKey = context.auth.secret_text, body = __spreadValues(__spreadValues({
      content
    }, id ? { id } : {}), add_to_collections ? { add_to_collections } : {});
    return await makeRequest(
      apiKey,
      import_pieces_common3.HttpMethod.POST,
      "/notes",
      body
    );
  }
});

// ../ap/packages/pieces/community/mem/src/lib/actions/delete-note.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var deleteNoteAction = (0, import_pieces_framework4.createAction)({
  auth: memAuth,
  name: "delete_note",
  displayName: "Delete Note",
  description: "Delete a note in Mem by its ID.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a single note in Mem identified by its note ID. Use to remove a note you can address by ID. Destructive and not idempotent: the first call removes the note and a repeat with the same ID will fail because the note no longer exists.",
    idempotent: !1
  },
  props: {
    note_id: import_pieces_framework4.Property.ShortText({
      displayName: "Note ID",
      required: !0,
      description: "The ID of the note to delete."
    })
  },
  async run(context) {
    let { note_id } = context.propsValue, apiKey = context.auth.secret_text;
    return await makeRequest(
      apiKey,
      import_pieces_common4.HttpMethod.DELETE,
      `/notes/${note_id}`
    );
  }
});

// ../ap/packages/pieces/community/mem/src/index.ts
var mem = (0, import_pieces_framework5.createPiece)({
  displayName: "Mem",
  description: "Capture and organize your thoughts using Mem.ai",
  auth: memAuth,
  logoUrl: "https://cdn.activepieces.com/pieces/mem.png",
  authors: ["krushnarout", "kishanprmr"],
  categories: [import_pieces_framework6.PieceCategory.PRODUCTIVITY],
  actions: [
    createMemAction,
    createNoteAction,
    deleteNoteAction,
    (0, import_pieces_common5.createCustomApiCallAction)({
      auth: memAuth,
      baseUrl: () => "https://api.mem.ai/v2",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mem
});
