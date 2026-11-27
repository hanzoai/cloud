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

// ../ap/packages/pieces/community/bumpups/src/index.ts
var index_exports = {};
__export(index_exports, {
  bumpups: () => bumpups
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bumpups/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), BumpupsAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Bumpups API Key",
  description: `**Enter your Bumpups API Key.**
---
### How to obtain your API key
1. Sign up or log in at [bumpups.com](https://bumpups.com).
2. Go to **Settings** \u2192 **API**.
3. Enable API access and generate a key.
4. Copy and paste it here.
`,
  required: !0
});

// ../ap/packages/pieces/community/bumpups/src/lib/actions/generate-creator-description.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bumpups/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.bumpups.com";
async function makeRequest(apiKey, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "X-Api-Key": apiKey,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/bumpups/src/lib/actions/generate-creator-description.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), generateCreatorDescription = (0, import_pieces_framework2.createAction)({
  auth: BumpupsAuth,
  name: "generateCreatorDescription",
  classification: "READ",
  displayName: "Generate Creator Description",
  description: "Generates a compelling description for a YouTube video based on its content.",
  audience: "both",
  aiMetadata: { description: "Calls the Bumpups AI model on a public YouTube video URL to generate a ready-to-use video description from the video content. Use when an agent needs a written description for a specific YouTube video; the video URL is required and an optional language code controls the output language. Not idempotent \u2014 each call invokes the model and may produce different text.", idempotent: !1 },
  props: {
    videoUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Video URL",
      description: "The YouTube video URL.",
      required: !0
    }),
    model: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Model",
      required: !0,
      defaultValue: "bump-1.0",
      options: {
        disabled: !1,
        options: [
          { label: "bump-1.0", value: "bump-1.0" }
        ]
      }
    }),
    language: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Language",
      required: !1,
      defaultValue: "en",
      options: {
        disabled: !1,
        options: [
          { label: "English (en)", value: "en" },
          { label: "Hindi (hi)", value: "hi" },
          { label: "Spanish (es)", value: "es" },
          { label: "Portuguese (pt)", value: "pt" },
          { label: "Russian (ru)", value: "ru" },
          { label: "German (de)", value: "de" },
          { label: "French (fr)", value: "fr" },
          { label: "Japanese (ja)", value: "ja" },
          { label: "Korean (ko)", value: "ko" },
          { label: "Arabic (ar)", value: "ar" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      url: propsValue.videoUrl,
      model: propsValue.model,
      languaege: propsValue.language || "en"
    };
    return await makeRequest(
      auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/creator/description",
      body
    );
  }
});

// ../ap/packages/pieces/community/bumpups/src/lib/actions/generate-creator-hashtags.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), generateCreatorHashtags = (0, import_pieces_framework3.createAction)({
  auth: BumpupsAuth,
  name: "generateCreatorHashtags",
  classification: "READ",
  displayName: "Generate Creator Hashtags",
  description: "Generates relevant hashtags for a YouTube video based on its content.",
  audience: "both",
  aiMetadata: { description: "Calls the Bumpups AI model on a public YouTube video URL to generate social tags from the video content, with an output-format option to return either hashtags or plain keywords. Use when an agent needs discoverability tags for a specific YouTube video; the video URL is required and an optional language code sets the output language. Not idempotent \u2014 each call invokes the model and may produce different tags.", idempotent: !1 },
  props: {
    videoUrl: import_pieces_framework3.Property.ShortText({
      displayName: "Video URL",
      description: "The YouTube video URL.",
      required: !0
    }),
    model: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Model",
      required: !0,
      defaultValue: "bump-1.0",
      options: {
        disabled: !1,
        options: [{ label: "bump-1.0", value: "bump-1.0" }]
      }
    }),
    language: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Language",
      required: !1,
      defaultValue: "en",
      options: {
        disabled: !1,
        options: [
          { label: "English (en)", value: "en" },
          { label: "Hindi (hi)", value: "hi" },
          { label: "Spanish (es)", value: "es" },
          { label: "Portuguese (pt)", value: "pt" },
          { label: "Russian (ru)", value: "ru" },
          { label: "German (de)", value: "de" },
          { label: "French (fr)", value: "fr" },
          { label: "Japanese (ja)", value: "ja" },
          { label: "Korean (ko)", value: "ko" },
          { label: "Arabic (ar)", value: "ar" }
        ]
      }
    }),
    output_format: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Output Format",
      required: !1,
      defaultValue: "text",
      options: {
        disabled: !1,
        options: [
          { label: "Hashtags", value: "hashtags" },
          { label: "Keywords", value: "keywords" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      url: propsValue.videoUrl,
      model: propsValue.model,
      language: propsValue.language || "en",
      output_format: propsValue.output_format || "hashtags"
    };
    return await makeRequest(
      auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/creator/hashtags",
      body
    );
  }
});

// ../ap/packages/pieces/community/bumpups/src/lib/actions/send-chat.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), sendChat = (0, import_pieces_framework4.createAction)({
  name: "send_chat",
  classification: "READ",
  displayName: "Send Chat",
  description: "Creates an interactive chat response for a given YouTube video using the bump-1.0 model. Provide the video URL and an optional prompt.",
  audience: "both",
  aiMetadata: { description: "Sends a question or instruction about a public YouTube video to the Bumpups chat model and returns its answer grounded in the video content, with an output-format option for plain text or markdown. Use for free-form queries about a specific video (the general-purpose option versus the fixed description/hashtags/takeaways/titles/timestamps actions); the video URL is required, while the prompt is optional and defaults to a summary if omitted (max 500 chars). Not idempotent \u2014 each call invokes the model and may produce different responses.", idempotent: !1 },
  auth: BumpupsAuth,
  props: {
    videoUrl: import_pieces_framework4.Property.ShortText({
      displayName: "Video URL",
      description: "The YouTube video URL.",
      required: !0
    }),
    model: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Model",
      required: !0,
      defaultValue: "bump-1.0",
      options: {
        disabled: !1,
        options: [
          { label: "bump-1.0", value: "bump-1.0" }
        ]
      }
    }),
    prompt: import_pieces_framework4.Property.LongText({
      displayName: "Prompt",
      description: 'Message or query about the video (max 500 chars). If empty, defaults to "summary".',
      required: !1
    }),
    language: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Language",
      required: !1,
      defaultValue: "en",
      options: {
        disabled: !1,
        options: [
          { label: "English (en)", value: "en" },
          { label: "Hindi (hi)", value: "hi" },
          { label: "Spanish (es)", value: "es" },
          { label: "Portuguese (pt)", value: "pt" },
          { label: "Russian (ru)", value: "ru" },
          { label: "German (de)", value: "de" },
          { label: "French (fr)", value: "fr" },
          { label: "Japanese (ja)", value: "ja" },
          { label: "Korean (ko)", value: "ko" },
          { label: "Arabic (ar)", value: "ar" }
        ]
      }
    }),
    output_format: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Output Format",
      required: !1,
      defaultValue: "text",
      options: {
        disabled: !1,
        options: [
          { label: "Text", value: "text" },
          { label: "Markdown", value: "markdown" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      url: propsValue.videoUrl,
      model: propsValue.model,
      prompt: propsValue.prompt || "summary",
      language: propsValue.language || "en",
      output_format: propsValue.output_format || "text"
    };
    return await makeRequest(
      auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/chat",
      body
    );
  }
});

// ../ap/packages/pieces/community/bumpups/src/lib/actions/generate-creator-takeaways.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), generateCreatorTakeaways = (0, import_pieces_framework5.createAction)({
  auth: BumpupsAuth,
  name: "generateCreatorTakeaways",
  classification: "READ",
  displayName: "Generate Creator Takeaways",
  description: "Generates key takeaways for a YouTube video based on its content.",
  audience: "both",
  aiMetadata: { description: "Calls the Bumpups AI model on a public YouTube video URL to generate key takeaways summarizing the video content, optionally including emojis. Use when an agent needs a bullet-style summary of the main points of a specific YouTube video; the video URL is required and an optional language code sets the output language. Not idempotent \u2014 each call invokes the model and may produce different text.", idempotent: !1 },
  props: {
    videoUrl: import_pieces_framework5.Property.ShortText({
      displayName: "Video URL",
      description: "The YouTube video URL.",
      required: !0
    }),
    model: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Model",
      required: !0,
      defaultValue: "bump-1.0",
      options: {
        disabled: !1,
        options: [{ label: "bump-1.0", value: "bump-1.0" }]
      }
    }),
    language: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Language",
      required: !1,
      defaultValue: "en",
      options: {
        disabled: !1,
        options: [
          { label: "English (en)", value: "en" },
          { label: "Hindi (hi)", value: "hi" },
          { label: "Spanish (es)", value: "es" },
          { label: "Portuguese (pt)", value: "pt" },
          { label: "Russian (ru)", value: "ru" },
          { label: "German (de)", value: "de" },
          { label: "French (fr)", value: "fr" },
          { label: "Japanese (ja)", value: "ja" },
          { label: "Korean (ko)", value: "ko" },
          { label: "Arabic (ar)", value: "ar" }
        ]
      }
    }),
    emojis_enabled: import_pieces_framework5.Property.Checkbox({
      displayName: "Include Emojis",
      description: "Whether to include emojis in the generated takeaways.",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      url: propsValue.videoUrl,
      model: propsValue.model,
      language: propsValue.language || "en",
      emojis_enabled: propsValue.emojis_enabled || !1
    };
    return await makeRequest(
      auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      "/creator/takeaways",
      body
    );
  }
});

// ../ap/packages/pieces/community/bumpups/src/lib/actions/generate-creator-titles.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), generateCreatorTitles = (0, import_pieces_framework6.createAction)({
  auth: BumpupsAuth,
  name: "generateCreatorTitles",
  classification: "READ",
  displayName: "Generate Creator Titles",
  description: "Generates engaging titles for a YouTube video based on its content.",
  audience: "both",
  aiMetadata: { description: "Calls the Bumpups AI model on a public YouTube video URL to generate engaging title suggestions from the video content. Use when an agent needs candidate titles for a specific YouTube video; the video URL is required and an optional language code sets the output language. Not idempotent \u2014 each call invokes the model and may produce different titles.", idempotent: !1 },
  props: {
    videoUrl: import_pieces_framework6.Property.ShortText({
      displayName: "Video URL",
      description: "The YouTube video URL.",
      required: !0
    }),
    model: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Model",
      required: !0,
      defaultValue: "bump-1.0",
      options: {
        disabled: !1,
        options: [
          { label: "bump-1.0", value: "bump-1.0" }
        ]
      }
    }),
    language: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Language",
      required: !1,
      defaultValue: "en",
      options: {
        disabled: !1,
        options: [
          { label: "English (en)", value: "en" },
          { label: "Hindi (hi)", value: "hi" },
          { label: "Spanish (es)", value: "es" },
          { label: "Portuguese (pt)", value: "pt" },
          { label: "Russian (ru)", value: "ru" },
          { label: "German (de)", value: "de" },
          { label: "French (fr)", value: "fr" },
          { label: "Japanese (ja)", value: "ja" },
          { label: "Korean (ko)", value: "ko" },
          { label: "Arabic (ar)", value: "ar" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      url: propsValue.videoUrl,
      model: propsValue.model,
      language: propsValue.language || "en"
    };
    return await makeRequest(
      auth.secret_text,
      import_pieces_common6.HttpMethod.POST,
      "/creator/titles",
      body
    );
  }
});

// ../ap/packages/pieces/community/bumpups/src/lib/actions/generate-timestamps.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), generateTimestamps = (0, import_pieces_framework7.createAction)({
  auth: BumpupsAuth,
  name: "generateTimestamps",
  classification: "READ",
  displayName: "Generate Timestamps",
  description: "Generates detailed timestamps for a YouTube video based on its content.",
  audience: "both",
  aiMetadata: { description: "Calls the Bumpups AI model on a public YouTube video URL to generate chapter timestamps from the video content, with a style option for long or short timestamp segments. Use when an agent needs chaptered timestamps for a specific YouTube video; the video URL is required and an optional language code sets the output language. Not idempotent \u2014 each call invokes the model and may produce different timestamps.", idempotent: !1 },
  props: {
    videoUrl: import_pieces_framework7.Property.ShortText({
      displayName: "Video URL",
      description: "The YouTube video URL.",
      required: !0
    }),
    model: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Model",
      required: !0,
      defaultValue: "bump-1.0",
      options: {
        disabled: !1,
        options: [
          { label: "bump-1.0", value: "bump-1.0" }
        ]
      }
    }),
    language: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Language",
      required: !1,
      defaultValue: "en",
      options: {
        disabled: !1,
        options: [
          { label: "English (en)", value: "en" },
          { label: "Hindi (hi)", value: "hi" },
          { label: "Spanish (es)", value: "es" },
          { label: "Portuguese (pt)", value: "pt" },
          { label: "Russian (ru)", value: "ru" },
          { label: "German (de)", value: "de" },
          { label: "French (fr)", value: "fr" },
          { label: "Japanese (ja)", value: "ja" },
          { label: "Korean (ko)", value: "ko" },
          { label: "Arabic (ar)", value: "ar" }
        ]
      }
    }),
    timestamps_style: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Timestamps Style",
      description: "Preferred length of each timestamp.",
      required: !1,
      defaultValue: "long",
      options: {
        disabled: !1,
        options: [
          { label: "Long", value: "long" },
          { label: "Short", value: "short" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      url: propsValue.videoUrl,
      model: propsValue.model,
      language: propsValue.language || "en",
      timestamps_style: propsValue.timestamps_style || "long"
    };
    return await makeRequest(
      auth.secret_text,
      import_pieces_common7.HttpMethod.POST,
      "/general/timestamps",
      body
    );
  }
});

// ../ap/packages/pieces/community/bumpups/src/index.ts
var import_pieces_common8 = require("@activepieces/pieces-common");
var bumpups = (0, import_pieces_framework8.createPiece)({
  displayName: "Bumpups",
  auth: BumpupsAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/bumpups.png",
  authors: ["Niket2035", "sanket-a11y"],
  actions: [
    generateCreatorDescription,
    generateCreatorHashtags,
    generateCreatorTakeaways,
    generateCreatorTitles,
    generateTimestamps,
    sendChat,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: BumpupsAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        "X-Api-Key": `${auth}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  bumpups
});
