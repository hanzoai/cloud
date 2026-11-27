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

// ../ap/packages/pieces/community/predis-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  predisAi: () => predisAi,
  predisAiAuth: () => predisAiAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/predis-ai/src/lib/actions/create-content.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/predis-ai/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), predisAiCommon = {
  baseUrl: "https://brain.predis.ai/predis_api/v1"
};
async function predisAiApiCall(apiKey, method, endpoint, body) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${predisAiCommon.baseUrl}${endpoint}`,
    headers: {
      Authorization: apiKey
    },
    body
  })).body;
}
var INPUT_LANGUAGES = [
  { label: "English", value: "english" },
  { label: "Malay", value: "malay" },
  { label: "Chinese (Traditional)", value: "chinese (traditional)" },
  { label: "Croatian", value: "croatian" },
  { label: "Dutch", value: "dutch" },
  { label: "Indonesian", value: "indonesian" },
  { label: "Vietnamese", value: "vietnamese" },
  { label: "Portuguese (BR)", value: "portuguese_br" },
  { label: "Finnish", value: "finnish" },
  { label: "French", value: "french" },
  { label: "German", value: "german" },
  { label: "Italian", value: "italian" },
  { label: "Norwegian", value: "norwegian" },
  { label: "Polish", value: "polish" },
  { label: "Romanian", value: "romanian" },
  { label: "Czech", value: "czech" },
  { label: "Spanish", value: "spanish" },
  { label: "Swedish", value: "swedish" },
  { label: "Turkish", value: "turkish" },
  { label: "Danish", value: "danish" },
  { label: "Hungarian", value: "hungarian" }
], OUTPUT_LANGUAGES = INPUT_LANGUAGES, MEDIA_TYPES = [
  { label: "Single Image", value: "single_image" },
  { label: "Carousel", value: "carousel" },
  { label: "Video", value: "video" }
], POST_TYPES = [
  { label: "Generic", value: "generic" },
  { label: "Meme", value: "meme" },
  { label: "Quotes", value: "quotes" }
], MODEL_VERSIONS = [
  { label: "Version 4 (Higher Quality, supports single_image & carousel)", value: "4" },
  { label: "Version 2 (Supports all media types)", value: "2" }
], VIDEO_DURATIONS = [
  { label: "Short", value: "short" },
  { label: "Long", value: "long" }
], COLOR_PALETTE_TYPES = [
  { label: "AI Suggested", value: "ai_suggested" },
  { label: "Brand", value: "brand" }
];

// ../ap/packages/pieces/community/predis-ai/src/lib/actions/create-content.ts
var createContent = (0, import_pieces_framework.createAction)({
  auth: predisAiAuth,
  name: "create_content",
  displayName: "Create Content",
  description: "Generate posts including videos, carousels, images, quotes, and memes.",
  audience: "both",
  aiMetadata: { description: "Generates social media content (single image, carousel, video, quote, or meme) on Predis.ai from a text topic for a given brand. Use to produce ready-to-post creatives; the media type, post type, model version, languages, and quantity (1-10 posts) are configurable. Requires a Predis.ai brand_id and a topic of at least 20 characters and 3 words. Not idempotent: each call kicks off a new generation job and produces new posts.", idempotent: !1 },
  props: {
    brand_id: import_pieces_framework.Property.ShortText({
      displayName: "Brand ID",
      description: "Unique identifier of your brand. Find it in Predis.ai under your brand settings.",
      required: !0
    }),
    text: import_pieces_framework.Property.LongText({
      displayName: "Topic",
      description: "The topic for your post. Must be at least 20 characters and 3 words.",
      required: !0
    }),
    model_version: import_pieces_framework.Property.StaticDropdown({
      displayName: "Model Version",
      description: "Version 4 offers higher quality but only supports single_image and carousel.",
      required: !1,
      defaultValue: "4",
      options: {
        disabled: !1,
        options: MODEL_VERSIONS
      }
    }),
    media_type: import_pieces_framework.Property.StaticDropdown({
      displayName: "Media Type",
      description: "Type of media to generate. Video is only supported by model version 2.",
      required: !1,
      defaultValue: "single_image",
      options: {
        disabled: !1,
        options: MEDIA_TYPES
      }
    }),
    post_type: import_pieces_framework.Property.StaticDropdown({
      displayName: "Post Type",
      description: "Type of post to generate. Only supported by model version 2.",
      required: !1,
      defaultValue: "generic",
      options: {
        disabled: !1,
        options: POST_TYPES
      }
    }),
    n_posts: import_pieces_framework.Property.Number({
      displayName: "Number of Posts",
      description: "Number of posts to generate (1-10).",
      required: !1,
      defaultValue: 1
    }),
    input_language: import_pieces_framework.Property.StaticDropdown({
      displayName: "Input Language",
      description: "Language of your input text.",
      required: !1,
      defaultValue: "english",
      options: {
        disabled: !1,
        options: INPUT_LANGUAGES
      }
    }),
    output_language: import_pieces_framework.Property.StaticDropdown({
      displayName: "Output Language",
      description: "Language for the generated post.",
      required: !1,
      defaultValue: "english",
      options: {
        disabled: !1,
        options: OUTPUT_LANGUAGES
      }
    }),
    video_duration: import_pieces_framework.Property.StaticDropdown({
      displayName: "Video Duration",
      description: "Required when media type is video.",
      required: !1,
      options: {
        disabled: !1,
        options: VIDEO_DURATIONS
      }
    }),
    color_palette_type: import_pieces_framework.Property.StaticDropdown({
      displayName: "Color Palette",
      description: "Color palette for generated content.",
      required: !1,
      defaultValue: "ai_suggested",
      options: {
        disabled: !1,
        options: COLOR_PALETTE_TYPES
      }
    }),
    author: import_pieces_framework.Property.ShortText({
      displayName: "Author",
      description: "Author name. Required when post type is quotes.",
      required: !1
    }),
    template_ids: import_pieces_framework.Property.Array({
      displayName: "Template IDs",
      description: "List of template IDs to use. Only supported by model version 2.",
      required: !1
    }),
    media_urls: import_pieces_framework.Property.Array({
      displayName: "Media URLs",
      description: "List of image/video URLs to use in your post. Only supported by model version 2.",
      required: !1
    })
  },
  async run(context) {
    let {
      brand_id,
      text,
      model_version,
      media_type,
      post_type,
      n_posts,
      input_language,
      output_language,
      video_duration,
      color_palette_type,
      author,
      template_ids,
      media_urls
    } = context.propsValue, body = {
      brand_id,
      text
    };
    return model_version && (body.model_version = model_version), media_type && (body.media_type = media_type), post_type && (body.post_type = post_type), n_posts && (body.n_posts = n_posts), input_language && (body.input_language = input_language), output_language && (body.output_language = output_language), video_duration && (body.video_duration = video_duration), color_palette_type && (body.color_palette_type = color_palette_type), author && (body.author = author), template_ids && template_ids.length > 0 && (body.template_ids = template_ids), media_urls && media_urls.length > 0 && (body.media_urls = media_urls), await predisAiApiCall(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/create_content/",
      body
    );
  }
});

// ../ap/packages/pieces/community/predis-ai/src/lib/actions/index.ts
var predisAiActions = [createContent];

// ../ap/packages/pieces/community/predis-ai/src/lib/triggers/post-created.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var postCreated = (0, import_pieces_framework2.createTrigger)({
  auth: predisAiAuth,
  name: "post_created",
  displayName: "Post Created",
  description: "Triggers when a post generation completes or fails.",
  aiMetadata: {
    description: "Fires when a Predis.ai content generation job finishes, reaching either completed or error status. Represents the asynchronous result of a Create Content request, carrying the post status, caption, post id, and generated media. Requires the Predis.ai webhook URL to be configured manually in the account REST API settings."
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  props: {
    markdown: import_pieces_framework2.Property.MarkDown({
      value: `## Predis.ai Webhook Setup
To use this trigger, configure the webhook URL in your Predis.ai account:

1. Login to Predis.ai
2. Navigate to **Pricing & Account** \u2192 **Rest API**
3. Add the following webhook URL:
\`\`\`text
{{webhookUrl}}
\`\`\`
4. Save your changes

The webhook will fire when posts reach **completed** or **error** status.`
    })
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  },
  async test() {
    return [
      {
        status: "completed",
        caption: "Sample caption for your post",
        post_id: "sample_post_id_123",
        generated_media: [{ url: "https://example.com/media.png" }],
        brand_id: "sample_brand_id"
      }
    ];
  },
  sampleData: {
    status: "completed",
    caption: "Sample caption for your post",
    post_id: "sample_post_id_123",
    generated_media: [{ url: "https://example.com/media.png" }],
    brand_id: "sample_brand_id"
  }
});

// ../ap/packages/pieces/community/predis-ai/src/lib/triggers/index.ts
var predisAiTriggers = [postCreated];

// ../ap/packages/pieces/community/predis-ai/src/index.ts
var predisAiAuth = import_pieces_framework3.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "In Predis.ai, go to Pricing & Account -> Rest API to generate an API key."
}), predisAi = (0, import_pieces_framework3.createPiece)({
  displayName: "Predis.ai",
  description: "AI-powered social media content creation platform. Generate posts, videos, carousels, images, quotes, and memes with automated creation and webhook notifications.",
  auth: predisAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/predis-ai.png",
  authors: ["onyedikachi-david"],
  actions: predisAiActions,
  triggers: predisAiTriggers
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  predisAi,
  predisAiAuth
});
