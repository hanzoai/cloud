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

// ../ap/packages/pieces/community/magicslides/src/index.ts
var index_exports = {};
__export(index_exports, {
  magicslides: () => magicslides
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/magicslides/src/lib/actions/create-ppt-from-topic.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/magicslides/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), MagicSlidesAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `**Enter your MagicSlides Access ID (API key).**
---
### How to obtain your API key
1. Sign up or log in at [magicslides.app](https://www.magicslides.app/).
2. Go to **Dashboard \u2192 API**.
3. Copy your **Access ID** and paste it here.
4. And copy your registered email address and paste it here.
`,
  required: !0,
  props: {
    accessId: import_pieces_framework.Property.ShortText({
      displayName: "Access ID",
      description: "Enter your MagicSlides Access ID",
      required: !0
    }),
    email: import_pieces_framework.Property.ShortText({
      displayName: "Email",
      description: "Enter your registered email address.",
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/magicslides/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.magicslides.app/public/api";
async function makeRequest(method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/magicslides/src/lib/actions/create-ppt-from-topic.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), createPptFromTopic = (0, import_pieces_framework2.createAction)({
  auth: MagicSlidesAuth,
  name: "createPptFromTopic",
  displayName: "Create PPT from Topic",
  description: "Generates a PPT presentation from a given topic.",
  audience: "both",
  aiMetadata: { description: "Generate a new PowerPoint presentation from a short topic string using AI, with optional control over slide count, language, template, AI model, and image sourcing. Choose this when the only input is a subject line and you want the AI to research and author the deck; use the summary or YouTube variants instead when starting from existing text or a video. Each call generates a fresh presentation, so it is not idempotent.", idempotent: !1 },
  props: {
    topic: import_pieces_framework2.Property.ShortText({
      displayName: "Topic",
      required: !0
    }),
    slideCount: import_pieces_framework2.Property.Number({
      displayName: "Number of slides",
      required: !1,
      defaultValue: 10
    }),
    language: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Language",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English", value: "en" },
          { label: "Hindi", value: "hi" },
          { label: "Spanish", value: "es" },
          { label: "French", value: "fr" },
          { label: "German", value: "de" },
          { label: "Chinese", value: "zh" }
        ]
      }
    }),
    template: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Template",
      required: !1,
      defaultValue: "bullet-point1",
      options: {
        options: [
          { label: "Bullet Point 1 (default)", value: "bullet-point1" },
          { label: "Bullet Point 2", value: "bullet-point2" },
          { label: "Bullet Point 4", value: "bullet-point4" },
          { label: "Bullet Point 5", value: "bullet-point5" },
          { label: "Bullet Point 6", value: "bullet-point6" },
          { label: "Bullet Point 7", value: "bullet-point7" },
          { label: "Bullet Point 8", value: "bullet-point8" },
          { label: "Bullet Point 9", value: "bullet-point9" },
          { label: "Bullet Point 10", value: "bullet-point10" },
          { label: "Pitch Deck Original", value: "pitchdeckorignal" },
          { label: "Pitch Deck 2", value: "pitch-deck-2" },
          { label: "Pitch Deck 3", value: "pitch-deck-3" },
          { label: "Custom 2", value: "custom2" },
          { label: "Custom 3", value: "custom3" },
          { label: "Vertical Bullet Point 1", value: "verticalBulletPoint1" },
          { label: "Vertical Custom 1", value: "verticalCustom1" }
        ]
      }
    }),
    model: import_pieces_framework2.Property.StaticDropdown({
      displayName: "AI Model",
      required: !1,
      defaultValue: "gpt-4",
      options: {
        options: [
          { label: "GPT-4", value: "gpt-4" },
          { label: "GPT-3.5", value: "gpt-3.5" }
        ]
      }
    }),
    extraInfoSource: import_pieces_framework2.Property.ShortText({
      displayName: "Extra Information Source",
      description: "Provide a URL or text to give additional context for the presentation.",
      required: !1
    }),
    aiImages: import_pieces_framework2.Property.Checkbox({
      displayName: "Use AI Images",
      required: !1,
      defaultValue: !1
    }),
    imageForEachSlide: import_pieces_framework2.Property.Checkbox({
      displayName: "Image for Each Slide",
      required: !1,
      defaultValue: !0
    }),
    googleImage: import_pieces_framework2.Property.Checkbox({
      displayName: "Use Google Images",
      required: !1,
      defaultValue: !1
    }),
    googleText: import_pieces_framework2.Property.Checkbox({
      displayName: "Use Google Text",
      required: !1,
      defaultValue: !1
    }),
    include_images: import_pieces_framework2.Property.Checkbox({
      displayName: "Include images",
      required: !1,
      defaultValue: !0
    }),
    presentationFor: import_pieces_framework2.Property.ShortText({
      displayName: "Presentation For (Audience)",
      required: !1
    }),
    watermark: import_pieces_framework2.Property.Json({
      displayName: "Watermark",
      description: 'Add a watermark to the presentation. e.g., {"width": "48", "height": "48", "brandURL": "https://...png", "position": "BottomRight"}',
      required: !1
    })
  },
  async run(context) {
    let {
      topic,
      slideCount,
      language,
      template,
      model,
      aiImages,
      imageForEachSlide,
      googleImage,
      googleText,
      presentationFor,
      watermark,
      extraInfoSource
    } = context.propsValue, payload = {
      topic,
      accessId: context.auth.props.accessId,
      email: context.auth.props.email,
      slideCount,
      language,
      template,
      model,
      aiImages,
      imageForEachSlide,
      googleImage,
      googleText,
      presentationFor,
      watermark,
      extraInfoSource
    };
    return await makeRequest(
      import_pieces_common2.HttpMethod.POST,
      "/ppt_from_topic",
      payload
    );
  }
});

// ../ap/packages/pieces/community/magicslides/src/lib/actions/create-ppt-from-summary.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var createPptFromSummary = (0, import_pieces_framework3.createAction)({
  auth: MagicSlidesAuth,
  name: "createPptFromText",
  displayName: "Create PPT from Text/Summary",
  description: "Generates a PPT presentation from provided text or summary.",
  audience: "both",
  aiMetadata: { description: "Generate a new PowerPoint presentation from a block of supplied text or summary using AI, with optional control over slide count, language, template, AI model, and image sourcing. Choose this when you already have the source content as text and want it turned into a deck, rather than the topic variant (generates from a subject line) or the YouTube variant. Each call generates a fresh presentation, so it is not idempotent.", idempotent: !1 },
  props: {
    msSummaryText: import_pieces_framework3.Property.LongText({
      displayName: "Summary Text",
      required: !0
    }),
    slideCount: import_pieces_framework3.Property.Number({
      displayName: "Number of Slides",
      required: !1,
      defaultValue: 10
    }),
    language: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Language",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English", value: "en" },
          { label: "Hindi", value: "hi" },
          { label: "Spanish", value: "es" },
          { label: "French", value: "fr" },
          { label: "German", value: "de" },
          { label: "Chinese", value: "zh" }
        ]
      }
    }),
    template: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Template",
      required: !1,
      defaultValue: "bullet-point1",
      options: {
        options: [
          { label: "Bullet Point 1 (default)", value: "bullet-point1" },
          { label: "Bullet Point 2", value: "bullet-point2" },
          { label: "Bullet Point 4", value: "bullet-point4" },
          { label: "Bullet Point 5", value: "bullet-point5" },
          { label: "Bullet Point 6", value: "bullet-point6" },
          { label: "Bullet Point 7", value: "bullet-point7" },
          { label: "Bullet Point 8", value: "bullet-point8" },
          { label: "Bullet Point 9", value: "bullet-point9" },
          { label: "Bullet Point 10", value: "bullet-point10" },
          { label: "Pitch Deck Original", value: "pitchdeckorignal" },
          { label: "Pitch Deck 2", value: "pitch-deck-2" },
          { label: "Pitch Deck 3", value: "pitch-deck-3" },
          { label: "Custom 2", value: "custom2" },
          { label: "Custom 3", value: "custom3" },
          { label: "Vertical Bullet Point 1", value: "verticalBulletPoint1" },
          { label: "Vertical Custom 1", value: "verticalCustom1" }
        ]
      }
    }),
    model: import_pieces_framework3.Property.StaticDropdown({
      displayName: "AI Model",
      required: !1,
      defaultValue: "gpt-4",
      options: {
        options: [
          { label: "GPT-4", value: "gpt-4" },
          { label: "GPT-3.5", value: "gpt-3.5" }
        ]
      }
    }),
    aiImages: import_pieces_framework3.Property.Checkbox({
      displayName: "Use AI Images",
      required: !1,
      defaultValue: !1
    }),
    imageForEachSlide: import_pieces_framework3.Property.Checkbox({
      displayName: "Image for Each Slide",
      required: !1,
      defaultValue: !0
    }),
    googleImage: import_pieces_framework3.Property.Checkbox({
      displayName: "Use Google Images",
      required: !1,
      defaultValue: !1
    }),
    googleText: import_pieces_framework3.Property.Checkbox({
      displayName: "Use Google Text",
      required: !1,
      defaultValue: !1
    }),
    presentationFor: import_pieces_framework3.Property.ShortText({
      displayName: "Presentation For (Audience)",
      required: !1
    }),
    watermark: import_pieces_framework3.Property.Json({
      displayName: "Watermark",
      description: 'Optional watermark e.g., {"width":"48","height":"48","brandURL":"https://...png","position":"BottomRight"}',
      required: !1
    })
  },
  async run(context) {
    let {
      msSummaryText,
      slideCount,
      language,
      template,
      model,
      aiImages,
      imageForEachSlide,
      googleImage,
      googleText,
      presentationFor,
      watermark
    } = context.propsValue, payload = {
      accessId: context.auth.props.accessId,
      email: context.auth.props.email,
      msSummaryText,
      slideCount,
      language,
      template,
      model,
      aiImages,
      imageForEachSlide,
      googleImage,
      googleText,
      presentationFor,
      watermark
    };
    return await makeRequest(
      import_pieces_common3.HttpMethod.POST,
      "/ppt_from_summery",
      payload
    );
  }
});

// ../ap/packages/pieces/community/magicslides/src/lib/actions/create-ppt-from-youtube-video.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var createPptFromYoutubeVideo = (0, import_pieces_framework4.createAction)({
  auth: MagicSlidesAuth,
  name: "createPptFromYoutube",
  displayName: "Create PPT from YouTube Video",
  description: "Generates a PPT presentation from a YouTube video link.",
  audience: "both",
  aiMetadata: { description: "Generate a new PowerPoint presentation from a YouTube video URL using AI, which extracts the video content into slides with optional control over slide count, language, template, AI model, and image sourcing. Choose this when the source material is a video; use the topic or text/summary variants when starting from a subject line or existing text. Requires a valid YouTube URL. Each call generates a fresh presentation, so it is not idempotent.", idempotent: !1 },
  props: {
    youtubeURL: import_pieces_framework4.Property.ShortText({
      displayName: "YouTube Video URL",
      required: !0
    }),
    template: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Template",
      required: !1,
      defaultValue: "bullet-point1",
      options: {
        options: [
          { label: "Bullet Point 1", value: "bullet-point1" },
          { label: "Bullet Point 2", value: "bullet-point2" },
          { label: "Bullet Point 4", value: "bullet-point4" },
          { label: "Pitch Deck 3", value: "pitch-deck-3" },
          { label: "Pitch Deck 2", value: "pitch-deck-2" },
          { label: "Custom Dark 1", value: "custom Dark 1" },
          { label: "Vertical Bullet Point 1", value: "verticalBulletPoint1" }
        ]
      }
    }),
    language: import_pieces_framework4.Property.ShortText({
      displayName: "Language",
      required: !1,
      defaultValue: "en"
    }),
    slideCount: import_pieces_framework4.Property.Number({
      displayName: "Number of slides",
      required: !1,
      defaultValue: 10
    }),
    aiImages: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable AI Images",
      required: !1,
      defaultValue: !1
    }),
    imageForEachSlide: import_pieces_framework4.Property.Checkbox({
      displayName: "Include Image on Every Slide",
      required: !1,
      defaultValue: !0
    }),
    googleImage: import_pieces_framework4.Property.Checkbox({
      displayName: "Use Google Images",
      required: !1,
      defaultValue: !1
    }),
    googleText: import_pieces_framework4.Property.Checkbox({
      displayName: "Enhance Content with Google Search",
      required: !1,
      defaultValue: !1
    }),
    model: import_pieces_framework4.Property.StaticDropdown({
      displayName: "AI Model",
      required: !1,
      defaultValue: "gpt-4",
      options: {
        options: [
          { label: "GPT-4", value: "gpt-4" },
          { label: "GPT-3.5", value: "gpt-3.5" }
        ]
      }
    }),
    presentationFor: import_pieces_framework4.Property.ShortText({
      displayName: "Presentation For (Audience)",
      required: !1
    }),
    watermark: import_pieces_framework4.Property.Json({
      displayName: "Watermark",
      description: 'Optional watermark e.g., {"width":"48","height":"48","brandURL":"https://...png","position":"BottomRight"}',
      required: !1
    })
  },
  async run(context) {
    let {
      youtubeURL,
      template,
      language,
      slideCount,
      aiImages,
      imageForEachSlide,
      googleImage,
      googleText,
      model,
      presentationFor,
      watermark
    } = context.propsValue, payload = {
      youtubeURL,
      accessId: context.auth.props.accessId,
      email: context.auth.props.email,
      template,
      language,
      slideCount,
      aiImages,
      imageForEachSlide,
      googleImage,
      googleText,
      model,
      presentationFor,
      watermark
    };
    return await makeRequest(import_pieces_common4.HttpMethod.POST, "/ppt_from_youtube", payload);
  }
});

// ../ap/packages/pieces/community/magicslides/src/index.ts
var magicslides = (0, import_pieces_framework5.createPiece)({
  displayName: "MagicSlides",
  auth: MagicSlidesAuth,
  description: "Create PowerPoint presentations from topics, summaries, or YouTube videos using AI.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/magicslides.png",
  authors: ["Niket2035"],
  actions: [
    createPptFromTopic,
    createPptFromSummary,
    createPptFromYoutubeVideo
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  magicslides
});
