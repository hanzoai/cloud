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

// ../ap/packages/pieces/community/easy-peasy-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  easyPeasyAi: () => easyPeasyAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/easy-peasy-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), easyPeasyAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Easy Peasy AI API Key. Get it from [Easy Peasy AI Settings](https://easy-peasy.ai/settings) - Navigate to the **API** tab from the top bar",
  required: !0
});

// ../ap/packages/pieces/community/easy-peasy-ai/src/lib/actions/custom-generator-text.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/easy-peasy-ai/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://easy-peasy.ai";
async function makeRequest(apiKey, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "Content-Type": "application/json",
        "x-api-key": apiKey
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/easy-peasy-ai/src/lib/actions/custom-generator-text.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), customGeneratorText = (0, import_pieces_framework2.createAction)({
  auth: easyPeasyAiAuth,
  name: "customGeneratorText",
  displayName: "Custom Generator (Text)",
  description: "Generate custom text for any purpose using the EasyPeasy AI template engine",
  audience: "both",
  aiMetadata: {
    description: "Generate freeform text from keywords/instructions via Easy-Peasy.AI's custom-generator preset. Choose this for open-ended copy or content generation where no purpose-specific template fits; supply the prompt in Keywords, optionally set language, output count, and a GPT-4 toggle. Each call produces fresh generated text, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    keywords: import_pieces_framework2.Property.LongText({
      displayName: "Keywords",
      description: "What do you want to generate? (max 1000 characters)",
      required: !0
    }),
    extra1: import_pieces_framework2.Property.LongText({
      displayName: "Background Information",
      description: "Additional context or background information (optional, max 1000 characters)",
      required: !1
    }),
    outputs: import_pieces_framework2.Property.Number({
      displayName: "Number of Outputs",
      description: "How many outputs to generate (default: 1)",
      required: !1,
      defaultValue: 1
    }),
    language: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Language",
      description: "Language for the generated text (default: English)",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "English", value: "English" },
          { label: "Spanish", value: "Spanish" },
          { label: "French", value: "French" },
          { label: "German", value: "German" },
          { label: "Italian", value: "Italian" },
          { label: "Portuguese", value: "Portuguese" },
          { label: "Dutch", value: "Dutch" },
          { label: "Russian", value: "Russian" },
          { label: "Chinese", value: "Chinese" },
          { label: "Japanese", value: "Japanese" },
          { label: "Korean", value: "Korean" },
          { label: "Hindi", value: "Hindi" },
          { label: "Arabic", value: "Arabic" }
        ]
      },
      defaultValue: "English"
    }),
    shouldUseGPT4: import_pieces_framework2.Property.Checkbox({
      displayName: "Use GPT-4",
      description: "Use GPT-4 model for generation (may incur higher costs)",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { keywords, extra1, outputs, language, shouldUseGPT4 } = context.propsValue, payload = {
      preset: "custom-generator",
      keywords,
      outputs: outputs || 1,
      language: language || "English",
      shouldUseGPT4: shouldUseGPT4 || !1
    };
    return extra1 && Object.assign(payload, { extra1 }), await makeRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/api/generate",
      payload
    );
  }
});

// ../ap/packages/pieces/community/easy-peasy-ai/src/lib/actions/get-ai-transcription.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var getAiTranscription = (0, import_pieces_framework3.createAction)({
  auth: easyPeasyAiAuth,
  name: "getAiTranscription",
  displayName: "Get AI Transcription",
  description: "Generate AI transcriptions from audio files with speaker detection and enhanced quality options",
  audience: "both",
  aiMetadata: {
    description: "Submit an audio file (by URL) to Easy-Peasy.AI for transcription, with optional speaker detection and enhanced-quality processing. Choose this to convert podcasts, interviews, meetings, lectures, or other recordings into text; the audio URL, a name, and an audio type are required. Each call creates a new transcription, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    url: import_pieces_framework3.Property.ShortText({
      displayName: "Audio File URL",
      description: "URL to the audio file to transcribe",
      required: !0
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Transcription Name",
      description: "Name or title for this transcription",
      required: !0
    }),
    audio_type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Audio Type",
      description: "Type of audio content being transcribed",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Podcast", value: "podcast" },
          { label: "Interview", value: "interview" },
          { label: "Meeting", value: "meeting" },
          { label: "Lecture", value: "lecture" },
          { label: "Presentation", value: "presentation" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    language: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Language",
      description: "Language of the audio content",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "English", value: "English" },
          { label: "Spanish", value: "Spanish" },
          { label: "French", value: "French" },
          { label: "German", value: "German" },
          { label: "Italian", value: "Italian" },
          { label: "Portuguese", value: "Portuguese" },
          { label: "Dutch", value: "Dutch" },
          { label: "Russian", value: "Russian" },
          { label: "Chinese", value: "Chinese" },
          { label: "Japanese", value: "Japanese" },
          { label: "Korean", value: "Korean" },
          { label: "Hindi", value: "Hindi" },
          { label: "Arabic", value: "Arabic" }
        ]
      },
      defaultValue: "English"
    }),
    detect_speakers: import_pieces_framework3.Property.Checkbox({
      displayName: "Detect Speakers",
      description: "Automatically detect and identify different speakers in the audio",
      required: !1,
      defaultValue: !0
    }),
    enhanced_quality: import_pieces_framework3.Property.Checkbox({
      displayName: "Enhanced Quality",
      description: "Use enhanced quality processing for better transcription accuracy",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let {
      url,
      name,
      audio_type,
      language,
      detect_speakers,
      enhanced_quality
    } = context.propsValue, payload = {
      url,
      name,
      audio_type,
      language: language || "English",
      detect_speakers: detect_speakers || !0,
      enhanced_quality: enhanced_quality || !0
    };
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/api/transcriptions",
      payload
    );
  }
});

// ../ap/packages/pieces/community/easy-peasy-ai/src/lib/actions/generate-ai-image.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), generateAiImage = (0, import_pieces_framework4.createAction)({
  auth: easyPeasyAiAuth,
  name: "generateAiImage",
  displayName: "Generate AI Image",
  description: "Generate AI images based on text prompts using various models and styles",
  audience: "both",
  aiMetadata: {
    description: "Generate an image from a text prompt via Easy-Peasy.AI, selecting the generation model (DALL-E 3/2, Midjourney, or Stable Diffusion) and optionally style, artist inspiration, dimensions, HD quality, and a reference image URL. Choose this to produce visual content from a description; the prompt and model are required. Each call creates a new image, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    prompt: import_pieces_framework4.Property.LongText({
      displayName: "Prompt",
      description: "Description of the image you want to generate",
      required: !0
    }),
    model: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Model",
      description: "AI model to use for image generation",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "DALL-E 3", value: "DALL-E 3" },
          { label: "DALL-E 2", value: "DALL-E 2" },
          { label: "Midjourney", value: "Midjourney" },
          { label: "Stable Diffusion", value: "Stable Diffusion" }
        ]
      }
    }),
    style: import_pieces_framework4.Property.ShortText({
      displayName: "Style",
      description: 'Art style or aesthetic for the generated image (e.g., "oil painting", "watercolor", "cyberpunk")',
      required: !1
    }),
    artist: import_pieces_framework4.Property.ShortText({
      displayName: "Artist Inspiration",
      description: 'Art style inspired by a specific artist (e.g., "Van Gogh", "Picasso")',
      required: !1
    }),
    dimensions: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Dimensions",
      description: "Image dimensions/aspect ratio",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "1024x1024", value: "1024x1024" },
          { label: "1024x1792", value: "1024x1792" },
          { label: "1792x1024", value: "1792x1024" },
          { label: "512x512", value: "512x512" },
          { label: "256x256", value: "256x256" }
        ]
      },
      defaultValue: "1024x1024"
    }),
    useHD: import_pieces_framework4.Property.Checkbox({
      displayName: "Use HD Quality",
      description: "Generate image in HD quality (may incur higher costs)",
      required: !1,
      defaultValue: !0
    }),
    image: import_pieces_framework4.Property.ShortText({
      displayName: "Image Reference URL",
      description: "Optional URL to a reference image for style consistency",
      required: !1
    })
  },
  async run(context) {
    let { prompt, model, style, artist, dimensions, useHD, image } = context.propsValue, payload = {
      prompt,
      model,
      useHD: useHD || !0
    };
    return style && (payload.style = style), artist && (payload.artist = artist), dimensions && (payload.dimensions = dimensions), image && (payload.image = image), await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/api/generate-image",
      payload
    );
  }
});

// ../ap/packages/pieces/community/easy-peasy-ai/src/index.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), easyPeasyAi = (0, import_pieces_framework5.createPiece)({
  displayName: "Easy-Peasy.AI",
  description: "Create professional-quality music in any genre with just a text prompt. From hip-hop to classical, our AI generates custom tracks for your projects in seconds.",
  auth: easyPeasyAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/easy-peasy-ai.png",
  categories: [import_pieces_framework6.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["sanket-a11y"],
  actions: [
    customGeneratorText,
    generateAiImage,
    getAiTranscription,
    (0, import_pieces_common5.createCustomApiCallAction)({
      auth: easyPeasyAiAuth,
      baseUrl: () => "https://easy-peasy.ai",
      authMapping: async (auth) => ({
        "Content-Type": "application/json",
        "x-api-key": `${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  easyPeasyAi
});
