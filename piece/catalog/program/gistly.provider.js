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

// ../ap/packages/pieces/community/gistly/src/index.ts
var index_exports = {};
__export(index_exports, {
  gistly: () => gistly,
  gistlyAuth: () => gistlyAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/gistly/src/lib/actions/get-transcript.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/gistly/src/lib/config.ts
var gistlyConfig = {
  baseUrl: "https://api.gist.ly",
  accessTokenHeaderKey: "x-api-key"
};

// ../ap/packages/pieces/community/gistly/src/lib/actions/get-transcript.ts
var getTranscriptAction = (0, import_pieces_framework.createAction)({
  name: "get_transcript",
  displayName: "Get Transcript",
  description: "Fetches transcript of a YouTube video.",
  audience: "both",
  aiMetadata: { description: "Retrieves the transcript/captions of a YouTube video from its URL via the Gistly API. Use this to get the spoken text of a video for summarizing, searching, or analysis. Set the Merge Text option to return one continuous text block, or leave it off to get timestamped chunks. Read-only and idempotent \u2014 fetching the same URL returns the same transcript.", idempotent: !0 },
  auth: gistlyAuth,
  props: {
    url: import_pieces_framework.Property.ShortText({
      displayName: "YouTube URL",
      required: !0
    }),
    text: import_pieces_framework.Property.Checkbox({
      displayName: "Merge Text",
      description: "If true, the transcript will be merged into a single text instead of timestamped chunks.",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let { url, text } = context.propsValue;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${gistlyConfig.baseUrl}/youtube/transcript`,
      headers: {
        [gistlyConfig.accessTokenHeaderKey]: context.auth.secret_text
      },
      queryParams: {
        url,
        text: text ? "true" : "false"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/gistly/src/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var markdownDescription = `
To obtain your free Gistly API Key, sign up at [Gistly](https://gist.ly/youtube-transcript-api) and then copy the key available in the [dashboard](https://api-portal.gist.ly/).
`, gistlyAuth = import_pieces_framework2.PieceAuth.SecretText({
  description: markdownDescription,
  displayName: "API Key",
  required: !0,
  validate: async (auth) => {
    try {
      return await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `${gistlyConfig.baseUrl}/health`,
        headers: {
          [gistlyConfig.accessTokenHeaderKey]: auth.auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key."
      };
    }
  }
}), gistly = (0, import_pieces_framework2.createPiece)({
  displayName: "Gistly",
  auth: gistlyAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/gistly.svg",
  authors: ["rafalzawadzki"],
  categories: [import_pieces_framework3.PieceCategory.ARTIFICIAL_INTELLIGENCE, import_pieces_framework3.PieceCategory.DEVELOPER_TOOLS, import_pieces_framework3.PieceCategory.CONTENT_AND_FILES],
  description: "YouTube Transcripts",
  actions: [getTranscriptAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  gistly,
  gistlyAuth
});
