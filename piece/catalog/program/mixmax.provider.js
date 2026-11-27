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

// ../ap/packages/pieces/community/mixmax/src/index.ts
var index_exports = {};
__export(index_exports, {
  mixmax: () => mixmax,
  mixmaxAuth: () => mixmaxAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mixmax/src/lib/actions/create-code-snippet.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mixmax/src/lib/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), mixmaxAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: "Your Mixmax API token (X-API-Token). Find it at Settings > API in Mixmax.",
  required: !0,
  validate: async ({ auth }) => {
    let { error } = await (0, import_pieces_framework2.tryCatch)(
      () => import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.mixmax.com/v1/users/me",
        headers: { "X-API-Token": auth }
      })
    );
    return error ? { valid: !1, error: "Invalid API token. Please check your Mixmax credentials." } : { valid: !0 };
  }
});

// ../ap/packages/pieces/community/mixmax/src/lib/common.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), mixmaxApiClient = { getRequest, postRequest };
async function getRequest({ auth, endpoint, queryParams }) {
  let url = new URL(`${BASE_URL}${endpoint}`);
  if (queryParams)
    for (let [key, value] of Object.entries(queryParams))
      value && url.searchParams.set(key, value);
  return import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.GET,
    url: url.toString(),
    headers: buildHeaders(auth)
  });
}
async function postRequest({ auth, endpoint, body }) {
  return import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.POST,
    url: `${BASE_URL}${endpoint}`,
    headers: buildHeaders(auth),
    body
  });
}
function buildHeaders(auth) {
  return {
    "X-API-Token": auth.secret_text,
    "Content-Type": "application/json"
  };
}
var BASE_URL = "https://api.mixmax.com/v1";

// ../ap/packages/pieces/community/mixmax/src/lib/actions/create-code-snippet.ts
var createCodeSnippet = (0, import_pieces_framework3.createAction)({
  auth: mixmaxAuth,
  name: "create_code_snippet",
  displayName: "Create Code Snippet",
  description: "Create a new code snippet in Mixmax. [See the documentation](https://developer.mixmax.com/reference/codesnippets-1)",
  audience: "both",
  aiMetadata: {
    description: "Creates a reusable code snippet in Mixmax from required HTML content, with optional title, syntax-highlighting language, background color, and editor theme. Use to save a code block for later insertion into emails. Not idempotent \u2014 each call creates a new snippet.",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Title",
      description: "The code snippet title",
      required: !1
    }),
    html: import_pieces_framework3.Property.LongText({
      displayName: "HTML",
      description: "The HTML content of the code snippet (as it appears in the editor)",
      required: !0
    }),
    language: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Language",
      description: "Programming language for syntax highlighting",
      required: !1,
      options: {
        options: [
          { label: "JavaScript", value: "javascript" },
          { label: "Python", value: "python" },
          { label: "HTML", value: "html" },
          { label: "CSS", value: "css" },
          { label: "TypeScript", value: "typescript" },
          { label: "Java", value: "java" },
          { label: "Ruby", value: "ruby" },
          { label: "PHP", value: "php" },
          { label: "Go", value: "golang" },
          { label: "Bash", value: "bash" },
          { label: "SQL", value: "sql" },
          { label: "JSON", value: "json" },
          { label: "XML", value: "xml" },
          { label: "Markdown", value: "markdown" }
        ]
      }
    }),
    background: import_pieces_framework3.Property.ShortText({
      displayName: "Background Color",
      description: "RGB background color (e.g., rgb(255, 255, 255))",
      required: !1,
      defaultValue: "rgb(255, 255, 255)"
    }),
    theme: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Editor Theme",
      description: "Code editor color theme",
      required: !1,
      options: {
        options: [
          { label: "Ambiance", value: "ambiance" },
          { label: "Monokai", value: "monokai" },
          { label: "GitHub", value: "github" },
          { label: "Solarized Dark", value: "solarized_dark" },
          { label: "Solarized Light", value: "solarized_light" },
          { label: "Tomorrow", value: "tomorrow" },
          { label: "Twilight", value: "twilight" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b, _c;
    let body = {
      html: propsValue.html,
      background: (_a = propsValue.background) != null ? _a : "rgb(255, 255, 255)",
      theme: (_b = propsValue.theme) != null ? _b : "ambiance",
      language: (_c = propsValue.language) != null ? _c : "javascript"
    };
    return propsValue.title && (body.title = propsValue.title), (await mixmaxApiClient.postRequest({
      auth,
      endpoint: "/codesnippets",
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/mixmax/src/lib/actions/create-contact.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var createContact = (0, import_pieces_framework4.createAction)({
  auth: mixmaxAuth,
  name: "create_contact",
  displayName: "Create Contact",
  description: "Create a new contact in Mixmax. [See the documentation](https://developer.mixmax.com/reference/contacts-1)",
  audience: "both",
  aiMetadata: {
    description: "Adds a new contact to the Mixmax contact list, keyed by email (the only required field; name is optional). Optionally enriches the contact with third-party data when enrichment sources are connected. Not idempotent \u2014 each call creates/registers the contact again.",
    idempotent: !1
  },
  props: {
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      description: "The contact email address",
      required: !0
    }),
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Name",
      description: "The contact full name",
      required: !1
    }),
    enrich: import_pieces_framework4.Property.Checkbox({
      displayName: "Enrich Contact",
      description: "Merge third-party data for this contact if enrichment sources are connected",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = { email: propsValue.email };
    return propsValue.name && (body.name = propsValue.name), propsValue.enrich && (body.enrich = propsValue.enrich), (await mixmaxApiClient.postRequest({
      auth,
      endpoint: "/contacts",
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/mixmax/src/lib/actions/find-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var findContact = (0, import_pieces_framework5.createAction)({
  auth: mixmaxAuth,
  name: "find_contact",
  displayName: "Find Contact",
  description: "Search for a contact in Mixmax. [See the documentation](https://developer.mixmax.com/reference/contactsquery)",
  audience: "both",
  aiMetadata: {
    description: "Searches Mixmax contacts by a query string (email address or name), which is required. Use to look up an existing contact before acting on it; optionally include Salesforce contacts in the results. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework5.Property.ShortText({
      displayName: "Search Query",
      description: "Email address or name to search for",
      required: !0
    }),
    includeSalesforceContacts: import_pieces_framework5.Property.Checkbox({
      displayName: "Include Salesforce Contacts",
      description: "Include Salesforce contacts in search results",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = { q: propsValue.query };
    return propsValue.includeSalesforceContacts && (queryParams.includeSalesforceContacts = "true"), (await mixmaxApiClient.getRequest({
      auth,
      endpoint: "/contacts/query",
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/mixmax/src/lib/actions/list-code-snippets.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var listCodeSnippets = (0, import_pieces_framework6.createAction)({
  auth: mixmaxAuth,
  name: "list_code_snippets",
  displayName: "List Code Snippets",
  description: "List all code snippets in Mixmax. [See the documentation](https://developer.mixmax.com/reference/codesnippets)",
  audience: "both",
  aiMetadata: {
    description: "Retrieves all code snippets in the Mixmax account. Takes no input. Use to enumerate available snippets or to find a snippet id before reusing it. Read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run({ auth }) {
    return (await mixmaxApiClient.getRequest({
      auth,
      endpoint: "/codesnippets"
    })).body;
  }
});

// ../ap/packages/pieces/community/mixmax/src/lib/actions/list-contacts.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var listContacts = (0, import_pieces_framework7.createAction)({
  auth: mixmaxAuth,
  name: "list_contacts",
  displayName: "List Contacts",
  description: "List contacts in Mixmax. [See the documentation](https://developer.mixmax.com/reference/contacts)",
  audience: "both",
  aiMetadata: {
    description: "Lists Mixmax contacts, optionally filtering by a name/email search term and sorting by name, email, last used, or used count. With no search term it returns all contacts; provide a term to narrow to matches. Use to enumerate or browse contacts rather than to look up a single one. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    search: import_pieces_framework7.Property.ShortText({
      displayName: "Search",
      description: "Filter contacts by name or email",
      required: !1
    }),
    sort: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Sort By",
      description: "Field to sort results by",
      required: !1,
      options: {
        options: [
          { label: "Name", value: "name" },
          { label: "Email", value: "email" },
          { label: "Last Used", value: "timestamp" },
          { label: "Used Count", value: "usedCount" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = {};
    return propsValue.search && (queryParams.search = propsValue.search), propsValue.sort && (queryParams.sort = propsValue.sort), (await mixmaxApiClient.getRequest({
      auth,
      endpoint: "/contacts",
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/mixmax/src/index.ts
var mixmax = (0, import_pieces_framework8.createPiece)({
  displayName: "Mixmax",
  description: "Email productivity and automation platform for Gmail \u2014 sequences, tracking, templates, and integrations",
  auth: mixmaxAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/mixmax.png",
  categories: [import_pieces_framework9.PieceCategory.COMMUNICATION],
  authors: ["tarai-dl", "onyedikachi-david"],
  actions: [
    createContact,
    findContact,
    listContacts,
    createCodeSnippet,
    listCodeSnippets,
    (0, import_pieces_common3.createCustomApiCallAction)({
      baseUrl: () => "https://api.mixmax.com/v1",
      auth: mixmaxAuth,
      authMapping: async (auth) => ({
        "X-API-Token": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mixmax,
  mixmaxAuth
});
