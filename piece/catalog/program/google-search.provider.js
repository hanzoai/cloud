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

// ../ap/packages/pieces/community/google-search/src/index.ts
var index_exports = {};
__export(index_exports, {
  googleSearch: () => googleSearch
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/google-search/src/lib/actions/search.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/google-search/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), markdownDescription = `
Follow these steps to obtain your Google Cloud API Key:

1. Go to [Google Cloud Console](https://console.cloud.google.com/).
2. Create or select a project.
3. Navigate to **APIs & Services** > **Enabled APIs & services**.
4. Click **+ ENABLE APIS AND SERVICES** and enable **Vertex AI Search** (Discovery Engine API).
5. Go to **APIs & Services** > **Credentials**.
6. Click **Create Credentials** > **API key**.
7. Copy the API key and paste it below.
`, googleSearchAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdownDescription,
  required: !0,
  props: {
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your Google Cloud API key with Vertex AI Search access.",
      required: !0
    }),
    projectId: import_pieces_framework.Property.ShortText({
      displayName: "Project ID",
      description: "Your Google Cloud project ID.",
      required: !0
    }),
    appId: import_pieces_framework.Property.ShortText({
      displayName: "App ID",
      description: "The Vertex AI Search app (engine) ID.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      let { apiKey, projectId, appId } = auth, url = `https://discoveryengine.googleapis.com/v1/projects/${projectId}/locations/global/collections/default_collection/engines/${appId}/servingConfigs/default_search:searchLite?key=${apiKey}`;
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url,
        headers: { "Content-Type": "application/json" },
        body: {
          query: "test"
        }
      }), { valid: !0 };
    } catch (error) {
      return {
        valid: !1,
        error: `Authentication failed: ${error instanceof Error ? error.message : "Unknown error"}`
      };
    }
  }
});

// ../ap/packages/pieces/community/google-search/src/lib/actions/search.ts
var searchAction = (0, import_pieces_framework2.createAction)({
  auth: googleSearchAuth,
  name: "search",
  displayName: "Search",
  description: "Search for content using Vertex AI Search (searchLite).",
  audience: "both",
  aiMetadata: {
    description: "Runs a query against a Google Cloud Vertex AI Search (Discovery Engine) app and returns matching results from that app's indexed data store. Choose this to search a private/enterprise corpus configured in Discovery Engine \u2014 not the public Google web. Requires the query text plus the connection's project, app, and API key; an optional user pseudo ID can be passed to improve personalization. Read-only and idempotent \u2014 the same query returns the same results without side effects.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework2.Property.ShortText({
      displayName: "Search Query",
      description: "The query text to search.",
      required: !0
    }),
    userPseudoId: import_pieces_framework2.Property.ShortText({
      displayName: "User Pseudo ID",
      description: "A pseudonymized identifier for the user (max 128 chars). Improves personalization.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { apiKey, projectId, appId } = auth.props, url = `https://discoveryengine.googleapis.com/v1/projects/${projectId}/locations/global/collections/default_collection/engines/${appId}/servingConfigs/default_search:searchLite?key=${apiKey}`, body = {
      query: propsValue.query
    };
    return propsValue.userPseudoId && (body.userPseudoId = propsValue.userPseudoId), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url,
      headers: { "Content-Type": "application/json" },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/google-search/src/index.ts
var googleSearch = (0, import_pieces_framework3.createPiece)({
  displayName: "Google Search",
  description: "Search using Vertex AI Search (Discovery Engine)",
  auth: googleSearchAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/google-search.png",
  authors: ["onyedikachi-david"],
  actions: [searchAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  googleSearch
});
