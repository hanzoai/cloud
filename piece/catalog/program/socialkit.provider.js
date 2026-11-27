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

// ../ap/packages/pieces/community/socialkit/src/index.ts
var index_exports = {};
__export(index_exports, {
  socialkit: () => socialkit
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/socialkit/src/lib/actions/get-youtube-details.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/socialkit/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), socialkitApiUrl = "https://api.socialkit.dev", socialkitAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Access Key",
  description: `
    To get your Access Key:
    1. Sign up at SocialKit Dashboard
    2. Copy your Access Key from the project Access Keys tab.
    **Important:** Keep your Access Key confidential.
  `,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${socialkitApiUrl}/youtube/stats?access_key=${auth}&url=https://youtube.com/watch?v=dQw4w9WgXcQ`
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid Access Key."
      };
    }
  }
});

// ../ap/packages/pieces/community/socialkit/src/lib/actions/get-youtube-details.ts
var socialkitApiUrl2 = "https://api.socialkit.dev", getYoutubeDetails = (0, import_pieces_framework2.createAction)({
  auth: socialkitAuth,
  name: "get_youtube_details",
  classification: "READ",
  displayName: "Get YouTube Details",
  description: "Get detailed information about any YouTube video including title, description, view count, likes, dislikes, duration, and channel information.",
  audience: "both",
  aiMetadata: { description: "Looks up metadata and statistics for a single YouTube video (title, description, view/like/dislike counts, duration, channel info) given its watch URL. Use to enrich or inspect a known video. Read-only and idempotent; requires a valid YouTube video URL.", idempotent: !0 },
  props: {
    url: import_pieces_framework2.Property.ShortText({
      displayName: "YouTube Video URL",
      description: "The URL of the YouTube video you want to get details from. (e.g., https://www.youtube.com/watch?v=VIDEO_ID)",
      required: !0
    })
  },
  async run(context) {
    let { url } = context.propsValue, accessKey = context.auth;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `${socialkitApiUrl2}/youtube/stats`,
      queryParams: {
        access_key: accessKey.secret_text,
        url
      }
    })).body.data;
  }
});

// ../ap/packages/pieces/community/socialkit/src/lib/actions/get-youtube-transcript.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var socialkitApiUrl3 = "https://api.socialkit.dev", getYoutubeTranscript = (0, import_pieces_framework3.createAction)({
  name: "get_youtube_transcript",
  classification: "READ",
  auth: socialkitAuth,
  displayName: "Get YouTube Transcript",
  description: "Extract the full transcript with timestamps from any YouTube video that has captions or subtitles available.",
  audience: "both",
  aiMetadata: { description: "Retrieves the full timestamped transcript of a YouTube video from its watch URL. Use when you need the spoken text/captions for downstream summarization, search, or analysis. Read-only and idempotent; only works for videos that have captions or subtitles available.", idempotent: !0 },
  props: {
    url: import_pieces_framework3.Property.ShortText({
      displayName: "YouTube Video URL",
      description: "The URL of the YouTube video to get the transcript from. (e.g., https://www.youtube.com/watch?v=VIDEO_ID)",
      required: !0
    })
  },
  async run(context) {
    let { url } = context.propsValue, accessKey = context.auth;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `${socialkitApiUrl3}/youtube/transcript`,
      queryParams: {
        access_key: accessKey.secret_text,
        url
      }
    })).body.data;
  }
});

// ../ap/packages/pieces/community/socialkit/src/lib/actions/get-youtube-summary.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var socialkitApiUrl4 = "https://api.socialkit.dev", getYoutubeSummary = (0, import_pieces_framework4.createAction)({
  auth: socialkitAuth,
  name: "get_youtube_summary",
  classification: "READ",
  displayName: "Get YouTube Summary",
  description: "Generates an AI-powered summary of a YouTube video.",
  audience: "both",
  aiMetadata: { description: "Returns an AI-generated summary of a YouTube video given its watch URL. Use when you want the gist of a video without processing the raw transcript yourself. The summary is derived server-side from the video; the call is read-only and idempotent for a given URL.", idempotent: !0 },
  props: {
    url: import_pieces_framework4.Property.ShortText({
      displayName: "YouTube Video URL",
      description: "The URL of the YouTube video to summarize. (e.g., https://www.youtube.com/watch?v=VIDEO_ID)",
      required: !0
    })
  },
  async run(context) {
    let { url } = context.propsValue, accessKey = context.auth;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `${socialkitApiUrl4}/youtube/summarize`,
      queryParams: {
        access_key: accessKey.secret_text,
        url
      }
    })).body.data;
  }
});

// ../ap/packages/pieces/community/socialkit/src/lib/actions/get-youtube-comment.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var socialkitApiUrl5 = "https://api.socialkit.dev", getYoutubeComments = (0, import_pieces_framework5.createAction)({
  auth: socialkitAuth,
  name: "get_youtube_comments",
  classification: "SEARCH",
  displayName: "Get YouTube Comments",
  description: "Fetch comments from  YouTube video with options to sort and limit results.",
  audience: "both",
  aiMetadata: { description: "Fetches comments for a YouTube video given its watch URL, optionally capped by a limit (default 10, max 100) and ordered newest-first or by top engagement. Use to read audience reactions or feed comment text into analysis. Read-only and idempotent, though results may shift as new comments are posted.", idempotent: !0 },
  props: {
    url: import_pieces_framework5.Property.ShortText({
      displayName: "YouTube Video URL",
      description: "The URL of the YouTube video to fetch comments from. (e.g., https://www.youtube.com/watch?v=VIDEO_ID)",
      required: !0
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "The number of comments to retrieve (default is 10, maximum is 100).",
      required: !1,
      defaultValue: 10
    }),
    sortBy: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Sort By",
      description: "The sorting order for the comments.",
      required: !1,
      options: {
        options: [
          { label: "Newest", value: "new" },
          { label: "Top", value: "top" }
        ]
      },
      defaultValue: "new"
    })
  },
  async run(context) {
    let { url, limit, sortBy } = context.propsValue, queryParams = {
      access_key: context.auth.secret_text,
      url
    };
    return limit && (queryParams.limit = limit.toString()), sortBy && (queryParams.sortBy = sortBy), (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `${socialkitApiUrl5}/youtube/comments`,
      queryParams
    })).body.data;
  }
});

// ../ap/packages/pieces/community/socialkit/src/index.ts
var socialkitApiUrl6 = "https://api.socialkit.dev", socialkit = (0, import_pieces_framework6.createPiece)({
  displayName: "Socialkit",
  auth: socialkitAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/socialkit.png",
  authors: ["david-oluwaseun420"],
  categories: [import_pieces_framework7.PieceCategory.CONTENT_AND_FILES],
  actions: [
    getYoutubeDetails,
    getYoutubeTranscript,
    getYoutubeSummary,
    getYoutubeComments,
    (0, import_pieces_common6.createCustomApiCallAction)({
      auth: socialkitAuth,
      baseUrl: () => socialkitApiUrl6,
      authMapping: async (auth) => ({
        "x-access-key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  socialkit
});
