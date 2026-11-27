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

// ../ap/packages/pieces/community/supadata/src/index.ts
var index_exports = {};
__export(index_exports, {
  supadata: () => supadata,
  supadataAuth: () => supadataAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/supadata/src/lib/actions/get-transcript.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/supadata/src/lib/config.ts
var supadataConfig = {
  baseUrl: "https://api.supadata.ai/v1",
  accessTokenHeaderKey: "x-api-key"
};

// ../ap/packages/pieces/community/supadata/src/lib/actions/get-transcript.ts
var getTranscriptAction = (0, import_pieces_framework.createAction)({
  name: "get_transcript",
  displayName: "Get Transcript",
  description: "Fetches transcript of a YouTube video.",
  audience: "both",
  aiMetadata: { description: "Retrieves the transcript text of a single YouTube video via the Supadata API, given the video URL. Use to read the spoken content of a video for summarization, search, or analysis; optionally request a preferred language and choose between one merged text block or timestamped chunks. Read-only and idempotent.", idempotent: !0 },
  auth: supadataAuth,
  props: {
    url: import_pieces_framework.Property.ShortText({
      displayName: "YouTube URL",
      description: "The URL of a single YouTube video.",
      required: !0
    }),
    lang: import_pieces_framework.Property.ShortText({
      displayName: "Language Preference",
      description: "Preferred language of the transcript. If not available, the first available language will be returned.",
      required: !1
    }),
    text: import_pieces_framework.Property.Checkbox({
      displayName: "Merge Text",
      description: "If true, the transcript will be merged into a single text instead of timestamped chunks.",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let { url, text, lang } = context.propsValue, qs = {
      url,
      text: text ? "true" : "false"
    };
    return lang && (qs.lang = lang), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${supadataConfig.baseUrl}/youtube/transcript`,
      headers: {
        [supadataConfig.accessTokenHeaderKey]: context.auth.secret_text
      },
      queryParams: qs
    })).body;
  }
});

// ../ap/packages/pieces/community/supadata/src/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var markdownDescription = `
To obtain your free Supadata API Key, sign up at [Supadata](https://supadata.ai) and then copy the key available in the [dashboard](https://dash.supadata.ai).
`, supadataAuth = import_pieces_framework2.PieceAuth.SecretText({
  description: markdownDescription,
  displayName: "API Key",
  required: !0,
  validate: async (auth) => {
    try {
      return await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `${supadataConfig.baseUrl}/health`,
        headers: {
          [supadataConfig.accessTokenHeaderKey]: auth.auth
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
}), supadata = (0, import_pieces_framework2.createPiece)({
  displayName: "Supadata",
  auth: supadataAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/supadata.svg",
  authors: ["rafalzawadzki"],
  categories: [import_pieces_framework3.PieceCategory.ARTIFICIAL_INTELLIGENCE, import_pieces_framework3.PieceCategory.DEVELOPER_TOOLS, import_pieces_framework3.PieceCategory.CONTENT_AND_FILES],
  description: "YouTube Transcripts",
  actions: [getTranscriptAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  supadata,
  supadataAuth
});
