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

// ../ap/packages/pieces/community/hackernews/src/index.ts
var index_exports = {};
__export(index_exports, {
  hackernews: () => hackernews
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/hackernews/src/lib/actions/top-stories-in-hacker-news.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), fetchTopStories = (0, import_pieces_framework.createAction)({
  name: "fetch_top_stories",
  // Must be a unique across the piece, this shouldn't be changed.
  classification: "SEARCH",
  displayName: "Fetch Top Stories",
  description: "Fetch top stories from hackernews",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the current top stories from Hacker News, returning the first N stories (N set by the number-of-stories input) with their full item details. Use to surface or summarize what is trending on Hacker News right now. Read-only and idempotent; a larger N simply fetches more of the same ranked list.",
    idempotent: !0
  },
  props: {
    // Properties to ask from the user, in this ask we will take number of
    number_of_stories: import_pieces_framework.Property.Number({
      displayName: "Number of Stories",
      description: void 0,
      required: !0
    })
  },
  async run(configValue) {
    let HACKER_NEWS_API_URL = "https://hacker-news.firebaseio.com/v0/", topStoryIds = (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${HACKER_NEWS_API_URL}topstories.json`
    })).body, topStories = [];
    for (let i = 0; i < Math.min(configValue.propsValue.number_of_stories, topStoryIds.length); i++) {
      let storyId = topStoryIds[i], storyResponse = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${HACKER_NEWS_API_URL}item/${storyId}.json`
      });
      topStories.push(storyResponse.body);
    }
    return topStories;
  }
});

// ../ap/packages/pieces/community/hackernews/src/index.ts
var hackernews = (0, import_pieces_framework2.createPiece)({
  displayName: "Hacker News",
  description: "A social news website",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/hackernews.png",
  auth: import_pieces_framework2.PieceAuth.None(),
  categories: [],
  authors: ["kishanprmr", "AbdulTheActivePiecer", "khaledmashaly", "abuaboud"],
  actions: [fetchTopStories],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  hackernews
});
