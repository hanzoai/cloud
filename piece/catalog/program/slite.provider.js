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

// ../ap/packages/pieces/community/slite/src/index.ts
var index_exports = {};
__export(index_exports, {
  slite: () => slite
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/slite/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/slite/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.slite.com/v1", MAX_CHILDREN_PAGES = 100;
async function apiCall({
  apiKey,
  method,
  resourceUri,
  query,
  body
}) {
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (queryParams[key] = Array.isArray(value) ? value.join(",") : String(value));
  let request = {
    method,
    url: `${BASE_URL}${resourceUri}`,
    headers: {
      "x-slite-api-key": apiKey,
      "Content-Type": "application/json",
      Accept: "application/json"
    },
    queryParams,
    body: compactBody(body)
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}
async function getAllChildren({
  apiKey,
  noteId: noteId2,
  maxPages = MAX_CHILDREN_PAGES
}) {
  var _a;
  let notes = [], cursor, pages = 0;
  do {
    let response = await apiCall({
      apiKey,
      method: import_pieces_common.HttpMethod.GET,
      resourceUri: `/notes/${noteId2}/children`,
      query: cursor ? { cursor } : void 0
    });
    notes.push(...(_a = response.notes) != null ? _a : []), cursor = response.hasNextPage && response.nextCursor ? response.nextCursor : void 0, pages += 1;
  } while (cursor && pages < maxPages);
  return notes;
}
function compactBody(body) {
  if (body === null || typeof body != "object" || Array.isArray(body))
    return body;
  let entries = Object.entries(body).filter(
    ([, value]) => !isNilOrEmpty(value)
  );
  return Object.fromEntries(entries);
}
function isNilOrEmpty(value) {
  return value == null ? !0 : Array.isArray(value) ? value.length === 0 : typeof value == "object" && Object.keys(value).length === 0;
}
var sliteApi = {
  baseUrl: BASE_URL,
  call: apiCall,
  getAllChildren
};

// ../ap/packages/pieces/community/slite/src/lib/auth.ts
var markdownDescription = `
**Authenticate with your Slite API key.**

1. Open your organization menu at the top-left of the Slite app.
2. Go to **Settings \u2192 API**.
3. Click **Create a new key** and follow the steps.
4. Copy the key (it is shown only once) and paste it here.
`, sliteAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await sliteApi.call({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: "/me"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key. Generate one in Slite under Settings \u2192 API, then paste it here."
      };
    }
  }
});

// ../ap/packages/pieces/community/slite/src/lib/actions/ask-question.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/slite/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var noteId = ({
  required = !0,
  displayName = "Doc",
  description = "Start typing to search your docs by title."
} = {}) => import_pieces_framework2.Property.Dropdown({
  displayName,
  description,
  auth: sliteAuth,
  required,
  refreshers: [],
  options: async ({ auth, searchValue }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Slite account first."
      };
    try {
      let query = typeof searchValue == "string" ? searchValue : void 0, hits = (_a = (await sliteApi.call({
        apiKey: auth.secret_text,
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: "/search-notes",
        query: { query, hitsPerPage: 100 }
      })).hits) != null ? _a : [];
      return hits.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No docs found."
      } : {
        disabled: !1,
        options: hits.map((hit) => ({
          label: hit.title || hit.id,
          value: hit.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Could not load docs. Check your connection."
      };
    }
  }
}), sliteProps = {
  noteId
};

// ../ap/packages/pieces/community/slite/src/lib/actions/ask-question.ts
var sliteAskQuestionAction = (0, import_pieces_framework3.createAction)({
  auth: sliteAuth,
  name: "ask_question",
  displayName: "Ask Question",
  description: "Asks a question in natural language and returns an answer drawn from your Slite docs, with sources.",
  audience: "both",
  aiMetadata: {
    description: "Runs Slite's AskX over the workspace knowledge base, returning a natural-language answer synthesized from indexed docs along with the source docs it drew from. Choose this for question-answering or retrieval over team docs rather than keyword lookup; optionally scope the answer to docs nested under a given parent doc id. Read-only and idempotent: repeating the same question produces the same answer for the current index state.",
    idempotent: !0
  },
  props: {
    question: import_pieces_framework3.Property.LongText({
      displayName: "Question",
      description: "The question to ask your Slite knowledge base.",
      required: !0
    }),
    parent_note_id: sliteProps.noteId({
      required: !1,
      displayName: "Parent Doc",
      description: "Limit the answer to docs nested under this parent."
    })
  },
  async run(context) {
    let { question, parent_note_id } = context.propsValue;
    return await sliteApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      resourceUri: "/ask",
      query: {
        question,
        parentNoteId: parent_note_id
      }
    });
  }
});

// ../ap/packages/pieces/community/slite/src/lib/actions/create-doc.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var sliteCreateDocAction = (0, import_pieces_framework4.createAction)({
  auth: sliteAuth,
  name: "create_doc",
  displayName: "Create Doc",
  description: "Creates a doc inside a parent doc or your private channel.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new Slite doc with the given title and optional body, placed under a parent doc id or, if no parent is given, in the caller's private channel. Content may be supplied as Markdown or HTML, and a template can be applied by id. Not idempotent: each call creates a separate doc even with identical input.",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Title",
      description: "The title of the new doc.",
      required: !0
    }),
    parent_note_id: sliteProps.noteId({
      required: !1,
      displayName: "Parent Doc",
      description: "Where to create the new doc. Leave blank to create it in your private channel."
    }),
    content: import_pieces_framework4.Property.LongText({
      displayName: "Content",
      description: "The body of the doc.",
      required: !1
    }),
    content_format: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Content Format",
      description: "How the content above is written.",
      required: !1,
      defaultValue: "markdown",
      options: {
        options: [
          { label: "Markdown", value: "markdown" },
          { label: "HTML", value: "html" }
        ]
      }
    }),
    template_id: import_pieces_framework4.Property.ShortText({
      displayName: "Template ID",
      description: "Apply a template to the new doc by its ID.",
      required: !1
    }),
    attributes: import_pieces_framework4.Property.Array({
      displayName: "Attributes",
      description: "Attributes for the parent collection, ordered by column. Ignored if they don't match the column type.",
      required: !1
    })
  },
  async run(context) {
    let { title, parent_note_id, content, content_format, template_id, attributes } = context.propsValue;
    return await sliteApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      resourceUri: "/notes",
      body: __spreadValues({
        title,
        parentNoteId: parent_note_id,
        templateId: template_id,
        attributes
      }, content ? content_format === "html" ? { html: content } : { markdown: content } : {})
    });
  }
});

// ../ap/packages/pieces/community/slite/src/lib/actions/fetch-sub-docs.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var sliteFetchSubDocsAction = (0, import_pieces_framework5.createAction)({
  auth: sliteAuth,
  name: "fetch_sub_docs",
  displayName: "Fetch Sub Docs",
  description: "Returns the list of sub docs nested under a parent doc.",
  audience: "both",
  aiMetadata: {
    description: "Lists the child docs nested directly under a given parent doc id in Slite, paging through all children. Use it to enumerate the contents of a collection or folder doc when you have its id. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    note_id: sliteProps.noteId({
      required: !0,
      displayName: "Parent Doc",
      description: "The doc whose sub docs you want to list."
    })
  },
  async run(context) {
    let { note_id } = context.propsValue;
    if (!note_id)
      return { notes: [], total: 0 };
    let notes = await sliteApi.getAllChildren({
      apiKey: context.auth.secret_text,
      noteId: note_id
    });
    return {
      notes,
      total: notes.length
    };
  }
});

// ../ap/packages/pieces/community/slite/src/lib/actions/replace-doc.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var sliteReplaceDocAction = (0, import_pieces_framework6.createAction)({
  auth: sliteAuth,
  name: "replace_doc",
  displayName: "Replace Doc",
  description: "Replaces the entire content and/or title of a doc. Fields you leave blank are kept unchanged.",
  audience: "both",
  aiMetadata: {
    description: "Overwrites an existing Slite doc identified by its note id, replacing the whole body (Markdown or HTML) and/or the title; omitted fields are left untouched. Use this for a full-content rewrite rather than a targeted block edit (see Update Doc for that). Idempotent: applying the same input to the same doc id leaves it in the same final state.",
    idempotent: !0
  },
  props: {
    note_id: sliteProps.noteId({ required: !0 }),
    title: import_pieces_framework6.Property.ShortText({
      displayName: "Title",
      description: "A new title for the doc.",
      required: !1
    }),
    content: import_pieces_framework6.Property.LongText({
      displayName: "Content",
      description: "New content that replaces the entire doc body.",
      required: !1
    }),
    content_format: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Content Format",
      description: "How the content above is written.",
      required: !1,
      defaultValue: "markdown",
      options: {
        options: [
          { label: "Markdown", value: "markdown" },
          { label: "HTML", value: "html" }
        ]
      }
    }),
    attributes: import_pieces_framework6.Property.Array({
      displayName: "Attributes",
      description: "Attributes for the parent collection, ordered by column.",
      required: !1
    })
  },
  async run(context) {
    let { note_id, title, content, content_format, attributes } = context.propsValue;
    return await sliteApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.PUT,
      resourceUri: `/notes/${note_id}`,
      body: __spreadValues({
        title,
        attributes
      }, content ? content_format === "html" ? { html: content } : { markdown: content } : {})
    });
  }
});

// ../ap/packages/pieces/community/slite/src/lib/actions/index-askx-object.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var sliteIndexAskxObjectAction = (0, import_pieces_framework7.createAction)({
  auth: sliteAuth,
  name: "index_askx_object",
  displayName: "Index AskX Object",
  description: "Sends an object to be indexed by AskX for your custom source. Note: Slite has deprecated this endpoint.",
  audience: "both",
  aiMetadata: {
    description: "Upserts a single object (title plus Markdown or HTML content) into a custom AskX data source so it becomes searchable by Ask Question, keyed by the source's root id and the object's own id. Note: Slite has deprecated this endpoint. Idempotent: re-sending the same root id and object id overwrites that indexed entry rather than adding a duplicate.",
    idempotent: !0
  },
  props: {
    root_id: import_pieces_framework7.Property.ShortText({
      displayName: "Root ID",
      description: "The root ID of your custom data source (created in Slite).",
      required: !0
    }),
    object_id: import_pieces_framework7.Property.ShortText({
      displayName: "Object ID",
      description: "A unique identifier for this object within the source.",
      required: !0
    }),
    title: import_pieces_framework7.Property.ShortText({
      displayName: "Title",
      description: "The title of the object.",
      required: !0
    }),
    content: import_pieces_framework7.Property.LongText({
      displayName: "Content",
      description: "The content of the object.",
      required: !0
    }),
    type: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Content Format",
      description: "How the content above is written.",
      required: !0,
      defaultValue: "markdown",
      options: {
        options: [
          { label: "Markdown", value: "markdown" },
          { label: "HTML", value: "html" }
        ]
      }
    }),
    updated_at: import_pieces_framework7.Property.DateTime({
      displayName: "Updated At",
      description: "When the object was last modified.",
      required: !0
    }),
    url: import_pieces_framework7.Property.ShortText({
      displayName: "URL",
      description: "The redirect URL for the object.",
      required: !0
    })
  },
  async run(context) {
    let { root_id, object_id, title, content, type, updated_at, url } = context.propsValue;
    return await sliteApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.POST,
      resourceUri: "/ask/index",
      body: {
        rootId: root_id,
        id: object_id,
        title,
        content,
        type,
        updatedAt: updated_at,
        url
      }
    });
  }
});

// ../ap/packages/pieces/community/slite/src/lib/actions/fetch-doc.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var sliteFetchDocAction = (0, import_pieces_framework8.createAction)({
  auth: sliteAuth,
  name: "fetch_doc",
  displayName: "Fetch Doc",
  description: "Returns a readable doc by its ID.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a single Slite doc by its note id, returning its content in the requested format (Markdown, HTML, or SliteML). Use it when you already have a doc id and need to read its body. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    note_id: sliteProps.noteId({ required: !0 }),
    format: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Content Format",
      description: "The format to return the doc content in.",
      required: !1,
      defaultValue: "md",
      options: {
        options: [
          { label: "Markdown", value: "md" },
          { label: "HTML", value: "html" },
          { label: "SliteML", value: "sliteml" }
        ]
      }
    })
  },
  async run(context) {
    let { note_id, format } = context.propsValue;
    return await sliteApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.GET,
      resourceUri: `/notes/${note_id}`,
      query: { format }
    });
  }
});

// ../ap/packages/pieces/community/slite/src/lib/actions/search-docs.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var sliteSearchDocsAction = (0, import_pieces_framework9.createAction)({
  auth: sliteAuth,
  name: "search_docs",
  displayName: "Search Docs",
  description: "Performs a keyword search across the docs you have access to.",
  audience: "both",
  aiMetadata: {
    description: "Runs a keyword search across the Slite docs the connected account can access, with optional scoping to a parent doc id, inclusion of archived docs, and pagination. Use this for term/title matching to locate docs and their ids; for natural-language question answering use Ask Question instead. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework9.Property.ShortText({
      displayName: "Search Term",
      description: "The keywords to search for.",
      required: !0
    }),
    parent_note_id: sliteProps.noteId({
      required: !1,
      displayName: "Parent Doc",
      description: "Limit results to docs nested under this parent."
    }),
    include_archived: import_pieces_framework9.Property.Checkbox({
      displayName: "Include Archived",
      description: "Also search archived docs.",
      required: !1,
      defaultValue: !1
    }),
    hits_per_page: import_pieces_framework9.Property.Number({
      displayName: "Results Per Page",
      description: "How many results to return per page (1\u2013100).",
      required: !1,
      defaultValue: 25
    }),
    page: import_pieces_framework9.Property.Number({
      displayName: "Page",
      description: "Which page of results to return (starts at 0).",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let { query, parent_note_id, include_archived, hits_per_page, page } = context.propsValue;
    return await sliteApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.GET,
      resourceUri: "/search-notes",
      query: {
        query,
        parentNoteId: parent_note_id,
        includeArchived: include_archived,
        hitsPerPage: hits_per_page,
        page
      }
    });
  }
});

// ../ap/packages/pieces/community/slite/src/lib/actions/update-doc.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var sliteUpdateDocAction = (0, import_pieces_framework10.createAction)({
  auth: sliteAuth,
  name: "update_doc",
  displayName: "Update Doc",
  description: "Updates a specific part (tile) of a doc. Fields you leave blank are kept unchanged.",
  audience: "both",
  aiMetadata: {
    description: "Edits a single tile (content block) within a Slite doc, identified by the note id plus the tile id, optionally setting its header, Markdown content, link, icon, and status badge; omitted fields stay unchanged. Choose this for a targeted block-level edit rather than rewriting the whole doc (see Replace Doc for that); the tile id must be copied from the block in Slite. Idempotent: applying the same input to the same tile yields the same final state.",
    idempotent: !0
  },
  props: {
    note_id: sliteProps.noteId({ required: !0 }),
    tile_id: import_pieces_framework10.Property.ShortText({
      displayName: "Tile ID",
      description: 'The block to update. In Slite, hover an empty line, open its menu, and choose "Copy block id".',
      required: !0
    }),
    title: import_pieces_framework10.Property.ShortText({
      displayName: "Title",
      description: "New header text for the tile.",
      required: !1
    }),
    content: import_pieces_framework10.Property.LongText({
      displayName: "Content",
      description: "New Markdown content for the tile.",
      required: !1
    }),
    url: import_pieces_framework10.Property.ShortText({
      displayName: "URL",
      description: "An external link for the tile.",
      required: !1
    }),
    icon_url: import_pieces_framework10.Property.ShortText({
      displayName: "Icon URL",
      description: "An icon to display on the tile.",
      required: !1
    }),
    status_label: import_pieces_framework10.Property.ShortText({
      displayName: "Status Label",
      description: "Text for the tile status badge.",
      required: !1
    }),
    status_color: import_pieces_framework10.Property.ShortText({
      displayName: "Status Color",
      description: "Status badge color as a hex code, e.g. #2F80ED.",
      required: !1
    })
  },
  async run(context) {
    let { note_id, tile_id, title, content, url, icon_url, status_label, status_color } = context.propsValue;
    return await sliteApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.PUT,
      resourceUri: `/notes/${note_id}/tiles/${tile_id}`,
      body: {
        title,
        content,
        url,
        iconURL: icon_url,
        status: status_label ? __spreadValues({ label: status_label }, status_color ? { colorHex: status_color } : {}) : void 0
      }
    });
  }
});

// ../ap/packages/pieces/community/slite/src/index.ts
var slite = (0, import_pieces_framework11.createPiece)({
  displayName: "Slite",
  description: "Slite is a modern knowledge base. Create, search, update, and ask questions about your team docs.",
  auth: sliteAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/slite.png",
  categories: [import_pieces_framework12.PieceCategory.PRODUCTIVITY],
  authors: ["onyedikachi-david"],
  actions: [
    sliteAskQuestionAction,
    sliteCreateDocAction,
    sliteFetchSubDocsAction,
    sliteReplaceDocAction,
    sliteIndexAskxObjectAction,
    sliteFetchDocAction,
    sliteSearchDocsAction,
    sliteUpdateDocAction,
    (0, import_pieces_common11.createCustomApiCallAction)({
      baseUrl: () => sliteApi.baseUrl,
      auth: sliteAuth,
      authMapping: async (auth) => ({
        "x-slite-api-key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  slite
});
