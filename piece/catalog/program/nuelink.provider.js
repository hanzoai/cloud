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

// ../ap/packages/pieces/community/nuelink/src/index.ts
var index_exports = {};
__export(index_exports, {
  nuelink: () => nuelink,
  nuelinkAuth: () => nuelinkAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/nuelink/src/lib/actions/create-post.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var createPost = (0, import_pieces_framework.createAction)({
  // auth: check https://www.activepieces.com/docs/developers/piece-reference/authentication,
  name: "createPost",
  auth: nuelinkAuth,
  displayName: "Create Post",
  description: "Creates a post on nuelink.",
  audience: "both",
  aiMetadata: { description: "Publishes a social media post through Nuelink to the connected social channels, with a required caption and optional media URL, title (for Reels/videos/Pinterest), alt text, and flags to share Instagram Reels to the feed or treat videos as short-form vertical content. Choose this to publish or schedule content via Nuelink. Not idempotent: each call creates a new post.", idempotent: !1 },
  props: {
    body: import_pieces_framework.Property.LongText({
      displayName: "Caption",
      required: !0,
      description: "The your post caption here"
    }),
    media: import_pieces_framework.Property.LongText({
      displayName: "Media",
      required: !1,
      description: "Enter the media URL here"
    }),
    title: import_pieces_framework.Property.LongText({
      displayName: "Title",
      required: !1,
      description: "Enter a title to use for your Reels, videos and/or Pinterest posts."
    }),
    altText: import_pieces_framework.Property.LongText({
      displayName: "Alt Text",
      required: !1,
      description: "Enter an alt text for your image or video."
    }),
    shareToFeed: import_pieces_framework.Property.Checkbox({
      displayName: "Share Instagram Reel to Feed",
      required: !1,
      description: "Share your Instagram Reel to your feed as well.",
      defaultValue: !1
    }),
    postAsShort: import_pieces_framework.Property.Checkbox({
      displayName: "Treat videos as short-form vertical videos",
      required: !1,
      description: "We will treat the videos imported as short-form vertical videos (Reels, Shorts, Tiktoks etc...).",
      defaultValue: !1
    })
  },
  async run(context) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://nuelink.com/api/v1/pabbly",
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`
      },
      body: {
        body: context.propsValue.body,
        media: context.propsValue.media,
        title: context.propsValue.title,
        altText: context.propsValue.altText,
        shareToFeed: context.propsValue.shareToFeed,
        postAsShort: context.propsValue.postAsShort
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/nuelink/src/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), nuelinkAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Please use **Nuelink API Key**."
}), nuelink = (0, import_pieces_framework2.createPiece)({
  displayName: "Nuelink",
  auth: nuelinkAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/nuelink.png",
  categories: [import_pieces_framework3.PieceCategory.CONTENT_AND_FILES, import_pieces_framework3.PieceCategory.MARKETING],
  authors: ["AouladLahceneOussama"],
  actions: [createPost],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  nuelink,
  nuelinkAuth
});
