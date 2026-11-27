"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/textcortex-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  textcortexAi: () => textcortexAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/textcortex-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), textcortexAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your TextCortex API key:
1. Sign up for free at https://textcortex.com
2. Go to your account settings
3. Navigate to the API Key section
4. Generate and copy your API key

Your API key should start with 'gAAAAAB...'`,
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b, _c, _d;
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: "https://api.textcortex.com/v1/texts/completions",
        headers: {
          "Content-Type": "application/json"
        },
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        },
        body: {
          text: "test",
          max_tokens: 1
        }
      }), { valid: !0 };
    } catch (e) {
      return ((_a = e.response) == null ? void 0 : _a.status) === 401 ? {
        valid: !1,
        error: "Invalid API key. Please check your TextCortex API key and try again."
      } : ((_b = e.response) == null ? void 0 : _b.status) === 429 ? {
        valid: !1,
        error: "Rate limit exceeded. Please wait and try again or upgrade your TextCortex plan."
      } : ((_c = e.response) == null ? void 0 : _c.status) === 400 ? { valid: !0 } : (_d = e.message) != null && _d.toLowerCase().includes("network") ? {
        valid: !1,
        error: "Network error. Please check your internet connection and try again."
      } : {
        valid: !1,
        error: "Authentication failed. Please verify your API key is correct."
      };
    }
  }
});

// ../ap/packages/pieces/community/textcortex-ai/src/lib/common/common.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), baseUrl = "https://api.textcortex.com/v1";
var LANGUAGES = [
  { label: "English", value: "en" },
  { label: "Spanish", value: "es" },
  { label: "French", value: "fr" },
  { label: "German", value: "de" },
  { label: "Italian", value: "it" },
  { label: "Portuguese", value: "pt" },
  { label: "Dutch", value: "nl" },
  { label: "Romanian", value: "ro" },
  { label: "Ukrainian", value: "uk" },
  { label: "Russian", value: "ru" },
  { label: "Polish", value: "pl" },
  { label: "Turkish", value: "tr" },
  { label: "Czech", value: "cs" },
  { label: "Hungarian", value: "hu" },
  { label: "Bulgarian", value: "bg" },
  { label: "Croatian", value: "hr" },
  { label: "Serbian", value: "sr" },
  { label: "Slovak", value: "sk" },
  { label: "Slovenian", value: "sl" },
  { label: "Swedish", value: "sv" },
  { label: "Norwegian", value: "no" },
  { label: "Danish", value: "da" },
  { label: "Finnish", value: "fi" },
  { label: "Greek", value: "el" },
  { label: "Arabic", value: "ar" },
  { label: "Hebrew", value: "he" },
  { label: "Hindi", value: "hi" },
  { label: "Chinese (Simplified)", value: "zh" },
  { label: "Japanese", value: "ja" },
  { label: "Korean", value: "ko" },
  { label: "Thai", value: "th" },
  { label: "Vietnamese", value: "vi" },
  { label: "Indonesian", value: "id" },
  { label: "Malay", value: "ms" }
], PROGRAMMING_LANGUAGES = [
  { label: "Python", value: "python" },
  { label: "Java", value: "java" },
  { label: "JavaScript", value: "javascript" },
  { label: "Go", value: "go" },
  { label: "PHP", value: "php" },
  { label: "JS Regex", value: "js_regex" }
], AI_MODELS = [
  { label: "GPT-4o", value: "gpt-4o" },
  { label: "GPT-4-1", value: "gpt-4-1" },
  { label: "GPT-5", value: "gpt-5" },
  { label: "GPT-4o Mini", value: "gpt-4o-mini" },
  { label: "o1-mini", value: "o1-mini" },
  { label: "o1-preview", value: "o1-preview" },
  { label: "o1", value: "o1" },
  { label: "o3-mini", value: "o3-mini" },
  { label: "o3", value: "o3" },
  { label: "o4-mini", value: "o4-mini" },
  { label: "Claude 3 Haiku", value: "claude-3-haiku" },
  { label: "Claude 3.5 Sonnet", value: "claude-3-5-sonnet" },
  { label: "Claude 3.7 Sonnet", value: "claude-3-7-sonnet" },
  { label: "Claude 3.7 Sonnet Thinking", value: "claude-3-7-sonnet-thinking" },
  { label: "Claude 4 Sonnet", value: "claude-4-sonnet" },
  { label: "Claude 4 Sonnet Thinking", value: "claude-4-sonnet-thinking" },
  { label: "Claude 3.5 Haiku", value: "claude-3-5-haiku" },
  { label: "Mistral Small", value: "mistral-small" },
  { label: "Mistral Large", value: "mistral-large" },
  { label: "DeepSeek R1", value: "deepseek-r1" },
  { label: "Kimi K2", value: "kimi-k2" },
  { label: "Gemini 2.0 Flash", value: "gemini-2-0-flash" },
  { label: "Gemini 2.5 Pro", value: "gemini-2-5-pro" },
  { label: "Gemini 2.5 Flash", value: "gemini-2-5-flash" },
  { label: "Gemini 2.5 Flash Thinking", value: "gemini-2-5-flash-thinking" },
  { label: "Grok 2", value: "grok-2" },
  { label: "Grok 4", value: "grok-4" }
], FORMALITY_LEVELS = [
  { label: "Default", value: "default" },
  { label: "More Formal", value: "more" },
  { label: "Less Formal", value: "less" },
  { label: "Prefer More Formal", value: "prefer_more" },
  { label: "Prefer Less Formal", value: "prefer_less" }
];
var API_ENDPOINTS = {
  CODES: "/codes",
  EMAILS: "/texts/emails",
  PARAPHRASES: "/texts/paraphrases",
  PRODUCT_DESCRIPTIONS: "/texts/products/descriptions",
  SOCIAL_MEDIA_POSTS: "/texts/social-media-posts",
  SUMMARIZATIONS: "/texts/summarizations",
  TRANSLATIONS: "/texts/translations",
  COMPLETIONS: "/texts/completions"
};

// ../ap/packages/pieces/community/textcortex-ai/src/lib/actions/send-prompt.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/textcortex-ai/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var textcortexCommon = {
  async apiCall({
    auth,
    method,
    resourceUri,
    body,
    headers = {}
  }) {
    let url = `${baseUrl}${resourceUri}`, requestHeaders = __spreadValues({
      "Content-Type": "application/json"
    }, headers), requestConfig = {
      method,
      url,
      headers: requestHeaders,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      body
    };
    return await import_pieces_common2.httpClient.sendRequest(requestConfig);
  }
};

// ../ap/packages/pieces/community/textcortex-ai/src/lib/actions/send-prompt.ts
var sendPrompt = (0, import_pieces_framework3.createAction)({
  auth: textcortexAuth,
  name: "send_prompt",
  displayName: "Send Prompt",
  description: "Send a custom prompt to TextCortex AI and generate a completion.",
  audience: "both",
  aiMetadata: { description: "Send a free-form prompt to TextCortex AI and get back generated text completion(s). Choose this for open-ended generation that does not fit a specialized template (paraphrase, summary, translation, email, code, etc.); selectable model, formality, languages, and number of outputs. Not idempotent: each call generates fresh, non-deterministic text and consumes account credits.", idempotent: !1 },
  props: {
    text: import_pieces_framework3.Property.LongText({
      displayName: "Text to Complete",
      description: 'Enter the beginning of your text and AI will complete it (e.g., "The benefits of renewable energy are")',
      required: !0
    }),
    model: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Model",
      description: "The language model to use",
      required: !1,
      defaultValue: "gemini-2-0-flash",
      options: {
        options: AI_MODELS
      }
    }),
    formality: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Formality",
      description: "The formality of the generated text",
      required: !1,
      defaultValue: "default",
      options: {
        options: FORMALITY_LEVELS
      }
    }),
    source_lang: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Source Language",
      description: "The language of the source text",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (Default)", value: "en" },
          { label: "Auto-detect", value: "auto" },
          ...LANGUAGES.filter((lang) => lang.value !== "en")
        ]
      }
    }),
    target_lang: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Target Language",
      description: "The language for the generated text",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (American)", value: "en" },
          { label: "English (British)", value: "en-gb" },
          { label: "Spanish", value: "es" },
          { label: "French", value: "fr" },
          { label: "German", value: "de" },
          { label: "Portuguese (Brazilian)", value: "pt-br" },
          { label: "Portuguese", value: "pt" },
          ...LANGUAGES.filter((lang) => !["en", "pt", "es", "fr", "de"].includes(lang.value))
        ]
      }
    }),
    max_tokens: import_pieces_framework3.Property.Number({
      displayName: "Max Tokens",
      description: "Maximum length of generated text (1-4096 tokens)",
      required: !1,
      defaultValue: 2048
    }),
    temperature: import_pieces_framework3.Property.Number({
      displayName: "Temperature",
      description: "Controls creativity (0.0-2.0). Higher values = more creative, lower = more focused.",
      required: !1
    }),
    n: import_pieces_framework3.Property.Number({
      displayName: "Number of Outputs",
      description: "How many completions to generate (1-5)",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g;
    try {
      let requestBody = {
        text: context.propsValue.text
      };
      context.propsValue.model && context.propsValue.model !== "gemini-2-0-flash" && (requestBody.model = context.propsValue.model), context.propsValue.formality && context.propsValue.formality !== "default" && (requestBody.formality = context.propsValue.formality), context.propsValue.source_lang && context.propsValue.source_lang !== "en" && (requestBody.source_lang = context.propsValue.source_lang), context.propsValue.target_lang && context.propsValue.target_lang !== "en" && (requestBody.target_lang = context.propsValue.target_lang), context.propsValue.max_tokens && context.propsValue.max_tokens !== 2048 && (requestBody.max_tokens = context.propsValue.max_tokens), context.propsValue.temperature !== void 0 && (requestBody.temperature = context.propsValue.temperature), context.propsValue.n && context.propsValue.n !== 1 && (requestBody.n = context.propsValue.n);
      let response = await textcortexCommon.apiCall({
        auth: context.auth,
        method: import_pieces_common3.HttpMethod.POST,
        resourceUri: API_ENDPOINTS.COMPLETIONS,
        body: requestBody
      }), outputs = ((_a = response.body.data) == null ? void 0 : _a.outputs) || [];
      return {
        success: !0,
        text: outputs.length > 0 ? outputs[0].text : response.body.text || response.body,
        outputs,
        remaining_credits: (_b = response.body.data) == null ? void 0 : _b.remaining_credits,
        metadata: {
          prompt: context.propsValue.text,
          model: context.propsValue.model || "gemini-2-0-flash",
          formality: context.propsValue.formality || "default",
          parameters: {
            max_tokens: context.propsValue.max_tokens || 2048,
            temperature: context.propsValue.temperature,
            n: context.propsValue.n || 1,
            source_lang: context.propsValue.source_lang || "en",
            target_lang: context.propsValue.target_lang || "en"
          },
          timestamp: (/* @__PURE__ */ new Date()).toISOString()
        }
      };
    } catch (error) {
      throw ((_c = error.response) == null ? void 0 : _c.status) === 401 ? new Error("Invalid API key. Please check your TextCortex API key.") : ((_d = error.response) == null ? void 0 : _d.status) === 429 ? new Error("Rate limit exceeded. Please wait and try again or upgrade your TextCortex plan.") : ((_e = error.response) == null ? void 0 : _e.status) === 400 ? new Error("Invalid request. Please check your input parameters.") : (_f = error.message) != null && _f.includes("network") || (_g = error.message) != null && _g.includes("timeout") ? new Error("Network error. Please check your connection and try again.") : new Error(`Text generation failed: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/textcortex-ai/src/lib/actions/create-paraphrase.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createParaphrase = (0, import_pieces_framework4.createAction)({
  auth: textcortexAuth,
  name: "create_paraphrase",
  displayName: "Create Paraphrase",
  description: "Rewrite text while preserving its meaning.",
  audience: "both",
  aiMetadata: { description: "Rewrite text via TextCortex AI to express the same meaning in different wording. Provide the content either as inline text or as a stored file ID (one is required); supports a default or embeddings rewrite mode plus model, formality, and language options. Not idempotent: each call produces fresh, non-deterministic output and consumes account credits.", idempotent: !1 },
  props: {
    text: import_pieces_framework4.Property.LongText({
      displayName: "Text to Paraphrase",
      description: "The text to paraphrase",
      required: !1
    }),
    file_id: import_pieces_framework4.Property.ShortText({
      displayName: "File ID",
      description: "ID of the file to paraphrase",
      required: !1
    }),
    mode: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Mode",
      description: "Paraphrase mode",
      required: !1,
      defaultValue: "default",
      options: {
        options: [
          { label: "Default", value: "default" },
          { label: "Embeddings", value: "embeddings" }
        ]
      }
    }),
    model: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Model",
      description: "The language model to use",
      required: !1,
      defaultValue: "gemini-2-0-flash",
      options: {
        options: AI_MODELS
      }
    }),
    formality: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Formality",
      description: "The formality of the generated text",
      required: !1,
      defaultValue: "default",
      options: {
        options: FORMALITY_LEVELS
      }
    }),
    source_lang: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Source Language",
      description: "The language of the source text",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (Default)", value: "en" },
          { label: "Auto-detect", value: "auto" },
          ...LANGUAGES.filter((lang) => lang.value !== "en")
        ]
      }
    }),
    target_lang: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Target Language",
      description: "The language for the paraphrased text",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (American)", value: "en" },
          { label: "English (British)", value: "en-gb" },
          { label: "Spanish", value: "es" },
          { label: "French", value: "fr" },
          { label: "German", value: "de" },
          { label: "Portuguese (Brazilian)", value: "pt-br" },
          { label: "Portuguese", value: "pt" },
          ...LANGUAGES.filter((lang) => !["en", "pt", "es", "fr", "de"].includes(lang.value))
        ]
      }
    }),
    max_tokens: import_pieces_framework4.Property.Number({
      displayName: "Max Tokens",
      description: "Maximum length of paraphrased text (1-4096 tokens)",
      required: !1,
      defaultValue: 2048
    }),
    temperature: import_pieces_framework4.Property.Number({
      displayName: "Temperature",
      description: "Controls creativity (0.0-2.0). Higher values = more creative, lower = more focused.",
      required: !1
    }),
    n: import_pieces_framework4.Property.Number({
      displayName: "Number of Outputs",
      description: "How many paraphrases to generate (1-5)",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g;
    try {
      if (!context.propsValue.text && !context.propsValue.file_id)
        throw new Error('Please provide either "Text to Paraphrase" or "File ID" to paraphrase.');
      let requestBody = {};
      context.propsValue.text && (requestBody.text = context.propsValue.text), context.propsValue.file_id && (requestBody.file_id = context.propsValue.file_id), context.propsValue.mode && context.propsValue.mode !== "default" && (requestBody.mode = context.propsValue.mode), context.propsValue.model && context.propsValue.model !== "gemini-2-0-flash" && (requestBody.model = context.propsValue.model), context.propsValue.formality && context.propsValue.formality !== "default" && (requestBody.formality = context.propsValue.formality), context.propsValue.source_lang && context.propsValue.source_lang !== "en" && (requestBody.source_lang = context.propsValue.source_lang), context.propsValue.target_lang && context.propsValue.target_lang !== "en" && (requestBody.target_lang = context.propsValue.target_lang), context.propsValue.max_tokens && context.propsValue.max_tokens !== 2048 && (requestBody.max_tokens = context.propsValue.max_tokens), context.propsValue.temperature !== void 0 && (requestBody.temperature = context.propsValue.temperature), context.propsValue.n && context.propsValue.n !== 1 && (requestBody.n = context.propsValue.n);
      let response = await textcortexCommon.apiCall({
        auth: context.auth,
        method: import_pieces_common4.HttpMethod.POST,
        resourceUri: API_ENDPOINTS.PARAPHRASES,
        body: requestBody
      }), outputs = ((_a = response.body.data) == null ? void 0 : _a.outputs) || [], paraphrasedText = outputs.length > 0 ? outputs[0].text : response.body.text || response.body;
      return {
        success: !0,
        original_text: context.propsValue.text,
        paraphrased_text: paraphrasedText,
        outputs,
        remaining_credits: (_b = response.body.data) == null ? void 0 : _b.remaining_credits,
        metadata: {
          mode: context.propsValue.mode || "default",
          model: context.propsValue.model || "gemini-2-0-flash",
          formality: context.propsValue.formality || "default",
          file_id: context.propsValue.file_id,
          parameters: {
            max_tokens: context.propsValue.max_tokens || 2048,
            temperature: context.propsValue.temperature,
            n: context.propsValue.n || 1,
            source_lang: context.propsValue.source_lang || "en",
            target_lang: context.propsValue.target_lang || "en"
          },
          timestamp: (/* @__PURE__ */ new Date()).toISOString()
        }
      };
    } catch (error) {
      throw ((_c = error.response) == null ? void 0 : _c.status) === 401 ? new Error("Invalid API key. Please check your TextCortex API key.") : ((_d = error.response) == null ? void 0 : _d.status) === 429 ? new Error("Rate limit exceeded. Please wait and try again or upgrade your TextCortex plan.") : ((_e = error.response) == null ? void 0 : _e.status) === 400 ? new Error("Invalid request. Please check your input parameters.") : (_f = error.message) != null && _f.includes("network") || (_g = error.message) != null && _g.includes("timeout") ? new Error("Network error. Please check your connection and try again.") : new Error(`Paraphrase failed: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/textcortex-ai/src/lib/actions/create-social-caption.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createSocialMediaCaption = (0, import_pieces_framework5.createAction)({
  auth: textcortexAuth,
  name: "create_social_media_caption",
  displayName: "Create Social Media Caption",
  description: "Generate a caption tailored for a specific social media channel (requires channel and keywords).",
  audience: "both",
  aiMetadata: { description: "Generate a social media caption with TextCortex AI from a description of the post, tuned for a specific platform (Twitter or LinkedIn). Requires the post context and a platform selection; optional comma-separated keywords are woven into the caption. Not idempotent: each call produces fresh, non-deterministic text and consumes account credits.", idempotent: !1 },
  props: {
    context: import_pieces_framework5.Property.LongText({
      displayName: "Post Context",
      description: 'Describe what you want to post about (e.g., "Announcing our new product launch with 50% discount")',
      required: !0
    }),
    mode: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Platform",
      description: "The platform to generate a post for",
      required: !0,
      options: {
        options: [
          { label: "Twitter", value: "twitter" },
          { label: "LinkedIn", value: "linkedin" }
        ]
      }
    }),
    keywords: import_pieces_framework5.Property.ShortText({
      displayName: "Keywords",
      description: "Keywords to be included in the post (comma-separated)",
      required: !1
    }),
    model: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Model",
      description: "The language model to use",
      required: !1,
      defaultValue: "gemini-2-0-flash",
      options: {
        options: AI_MODELS
      }
    }),
    formality: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Formality",
      description: "The formality of the generated text",
      required: !1,
      defaultValue: "default",
      options: {
        options: FORMALITY_LEVELS
      }
    }),
    source_lang: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Source Language",
      description: "The language of the source text",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (Default)", value: "en" },
          { label: "Auto-detect", value: "auto" },
          ...LANGUAGES.filter((lang) => lang.value !== "en")
        ]
      }
    }),
    target_lang: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Target Language",
      description: "The language for the caption",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (American)", value: "en" },
          { label: "English (British)", value: "en-gb" },
          { label: "Spanish", value: "es" },
          { label: "French", value: "fr" },
          { label: "German", value: "de" },
          { label: "Portuguese (Brazilian)", value: "pt-br" },
          { label: "Portuguese", value: "pt" },
          ...LANGUAGES.filter((lang) => !["en", "pt", "es", "fr", "de"].includes(lang.value))
        ]
      }
    }),
    max_tokens: import_pieces_framework5.Property.Number({
      displayName: "Max Tokens",
      description: "Maximum length of caption (1-4096 tokens)",
      required: !1,
      defaultValue: 2048
    }),
    temperature: import_pieces_framework5.Property.Number({
      displayName: "Temperature",
      description: "Controls creativity (0.0-2.0). Higher values = more creative, lower = more focused.",
      required: !1
    }),
    n: import_pieces_framework5.Property.Number({
      displayName: "Number of Outputs",
      description: "How many captions to generate (1-5)",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g;
    try {
      let requestBody = {
        context: context.propsValue.context,
        mode: context.propsValue.mode
      };
      context.propsValue.keywords && (requestBody.keywords = context.propsValue.keywords.split(",").map((k) => k.trim())), context.propsValue.model && context.propsValue.model !== "gemini-2-0-flash" && (requestBody.model = context.propsValue.model), context.propsValue.formality && context.propsValue.formality !== "default" && (requestBody.formality = context.propsValue.formality), context.propsValue.source_lang && context.propsValue.source_lang !== "en" && (requestBody.source_lang = context.propsValue.source_lang), context.propsValue.target_lang && context.propsValue.target_lang !== "en" && (requestBody.target_lang = context.propsValue.target_lang), context.propsValue.max_tokens && context.propsValue.max_tokens !== 2048 && (requestBody.max_tokens = context.propsValue.max_tokens), context.propsValue.temperature !== void 0 && (requestBody.temperature = context.propsValue.temperature), context.propsValue.n && context.propsValue.n !== 1 && (requestBody.n = context.propsValue.n);
      let response = await textcortexCommon.apiCall({
        auth: context.auth,
        method: import_pieces_common5.HttpMethod.POST,
        resourceUri: API_ENDPOINTS.SOCIAL_MEDIA_POSTS,
        body: requestBody
      }), outputs = ((_a = response.body.data) == null ? void 0 : _a.outputs) || [];
      return {
        success: !0,
        caption: outputs.length > 0 ? outputs[0].text : response.body.text || response.body,
        outputs,
        remaining_credits: (_b = response.body.data) == null ? void 0 : _b.remaining_credits,
        metadata: {
          context: context.propsValue.context,
          platform: context.propsValue.mode,
          keywords: context.propsValue.keywords,
          model: context.propsValue.model || "gemini-2-0-flash",
          formality: context.propsValue.formality || "default",
          parameters: {
            max_tokens: context.propsValue.max_tokens || 2048,
            temperature: context.propsValue.temperature,
            n: context.propsValue.n || 1,
            source_lang: context.propsValue.source_lang || "en",
            target_lang: context.propsValue.target_lang || "en"
          },
          timestamp: (/* @__PURE__ */ new Date()).toISOString()
        }
      };
    } catch (error) {
      throw ((_c = error.response) == null ? void 0 : _c.status) === 401 ? new Error("Invalid API key. Please check your TextCortex API key.") : ((_d = error.response) == null ? void 0 : _d.status) === 429 ? new Error("Rate limit exceeded. Please wait and try again or upgrade your TextCortex plan.") : ((_e = error.response) == null ? void 0 : _e.status) === 400 ? new Error("Invalid request. Please check your input parameters.") : (_f = error.message) != null && _f.includes("network") || (_g = error.message) != null && _g.includes("timeout") ? new Error("Network error. Please check your connection and try again.") : new Error(`Caption generation failed: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/textcortex-ai/src/lib/actions/create-translation.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createTranslation = (0, import_pieces_framework6.createAction)({
  auth: textcortexAuth,
  name: "create_translation",
  displayName: "Create Translation",
  description: "Translate input text into a target language.",
  audience: "both",
  aiMetadata: { description: "Translate text into a chosen target language using TextCortex AI. Requires the source text and a target language; source language can be auto-detected, and formality can be controlled. Not idempotent: each call invokes the model fresh and consumes account credits.", idempotent: !1 },
  props: {
    text: import_pieces_framework6.Property.LongText({
      displayName: "Text to Translate",
      description: "The text to translate",
      required: !0
    }),
    target_lang: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Target Language",
      description: "The language to translate to",
      required: !0,
      options: {
        options: [
          { label: "English (American)", value: "en" },
          { label: "English (British)", value: "en-gb" },
          { label: "Spanish", value: "es" },
          { label: "French", value: "fr" },
          { label: "German", value: "de" },
          { label: "Portuguese (Brazilian)", value: "pt-br" },
          { label: "Portuguese", value: "pt" },
          ...LANGUAGES.filter((lang) => !["en", "pt", "es", "fr", "de"].includes(lang.value))
        ]
      }
    }),
    source_lang: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Source Language",
      description: "The language of the text to translate",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (Default)", value: "en" },
          { label: "Auto-detect", value: "auto" },
          ...LANGUAGES.filter((lang) => lang.value !== "en")
        ]
      }
    }),
    formality: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Formality",
      description: "The formality of the generated text",
      required: !1,
      defaultValue: "default",
      options: {
        options: FORMALITY_LEVELS
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g;
    try {
      let requestBody = {
        text: context.propsValue.text,
        target_lang: context.propsValue.target_lang
      };
      context.propsValue.source_lang && context.propsValue.source_lang !== "en" && (requestBody.source_lang = context.propsValue.source_lang), context.propsValue.formality && context.propsValue.formality !== "default" && (requestBody.formality = context.propsValue.formality);
      let response = await textcortexCommon.apiCall({
        auth: context.auth,
        method: import_pieces_common6.HttpMethod.POST,
        resourceUri: API_ENDPOINTS.TRANSLATIONS,
        body: requestBody
      }), translatedText = ((_a = response.body.data) == null ? void 0 : _a.text) || response.body.text || response.body;
      return {
        success: !0,
        original_text: context.propsValue.text,
        translated_text: translatedText,
        remaining_credits: (_b = response.body.data) == null ? void 0 : _b.remaining_credits,
        metadata: {
          source_language: context.propsValue.source_lang || "en",
          target_language: context.propsValue.target_lang,
          formality: context.propsValue.formality || "default",
          timestamp: (/* @__PURE__ */ new Date()).toISOString()
        }
      };
    } catch (error) {
      throw ((_c = error.response) == null ? void 0 : _c.status) === 401 ? new Error("Invalid API key. Please check your TextCortex API key.") : ((_d = error.response) == null ? void 0 : _d.status) === 429 ? new Error("Rate limit exceeded. Please wait and try again or upgrade your TextCortex plan.") : ((_e = error.response) == null ? void 0 : _e.status) === 400 ? new Error("Invalid request. Please check your input parameters.") : (_f = error.message) != null && _f.includes("network") || (_g = error.message) != null && _g.includes("timeout") ? new Error("Network error. Please check your connection and try again.") : new Error(`Translation failed: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/textcortex-ai/src/lib/actions/create-code.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var createCode = (0, import_pieces_framework7.createAction)({
  auth: textcortexAuth,
  name: "create_code",
  displayName: "Create Code",
  description: "Generate code in a specified programming language based on instructions.",
  audience: "both",
  aiMetadata: { description: "Generate a code snippet with TextCortex AI from a natural-language description of what the code should do. Requires the instructions and a target programming language; selectable model and number of outputs. Not idempotent: each call produces fresh, non-deterministic code and consumes account credits.", idempotent: !1 },
  props: {
    text: import_pieces_framework7.Property.LongText({
      displayName: "Code Instructions",
      description: 'Describe what you want the code to do (e.g., "Create a function to sort an array")',
      required: !0
    }),
    mode: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Programming Language",
      description: "Select the programming language for code generation",
      required: !0,
      options: {
        options: PROGRAMMING_LANGUAGES
      }
    }),
    model: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Model",
      description: "The language model to use",
      required: !1,
      defaultValue: "gemini-2-0-flash",
      options: {
        options: AI_MODELS
      }
    }),
    max_tokens: import_pieces_framework7.Property.Number({
      displayName: "Max Tokens",
      description: "Maximum length of generated code (1-4096 tokens)",
      required: !1,
      defaultValue: 2048
    }),
    temperature: import_pieces_framework7.Property.Number({
      displayName: "Temperature",
      description: "Controls creativity (0.0-2.0). Higher values = more creative, lower = more focused.",
      required: !1
    }),
    n: import_pieces_framework7.Property.Number({
      displayName: "Number of Outputs",
      description: "How many code snippets to generate (1-5)",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g;
    try {
      let requestBody = {
        text: context.propsValue.text,
        mode: context.propsValue.mode
      };
      context.propsValue.model && context.propsValue.model !== "gemini-2-0-flash" && (requestBody.model = context.propsValue.model), context.propsValue.max_tokens && context.propsValue.max_tokens !== 2048 && (requestBody.max_tokens = context.propsValue.max_tokens), context.propsValue.temperature !== void 0 && (requestBody.temperature = context.propsValue.temperature), context.propsValue.n && context.propsValue.n !== 1 && (requestBody.n = context.propsValue.n);
      let response = await textcortexCommon.apiCall({
        auth: context.auth,
        method: import_pieces_common7.HttpMethod.POST,
        resourceUri: API_ENDPOINTS.CODES,
        body: requestBody
      }), outputs = ((_a = response.body.data) == null ? void 0 : _a.outputs) || [];
      return {
        success: !0,
        code: outputs.length > 0 ? outputs[0].text : response.body.text || response.body,
        outputs,
        remaining_credits: (_b = response.body.data) == null ? void 0 : _b.remaining_credits,
        metadata: {
          instruction: context.propsValue.text,
          programming_language: context.propsValue.mode,
          model: context.propsValue.model || "gemini-2-0-flash",
          parameters: {
            max_tokens: context.propsValue.max_tokens || 2048,
            temperature: context.propsValue.temperature,
            n: context.propsValue.n || 1
          },
          timestamp: (/* @__PURE__ */ new Date()).toISOString()
        }
      };
    } catch (error) {
      throw ((_c = error.response) == null ? void 0 : _c.status) === 401 ? new Error("Invalid API key. Please check your TextCortex API key.") : ((_d = error.response) == null ? void 0 : _d.status) === 429 ? new Error("Rate limit exceeded. Please wait and try again or upgrade your TextCortex plan.") : ((_e = error.response) == null ? void 0 : _e.status) === 400 ? new Error("Invalid request. Please check your input parameters.") : (_f = error.message) != null && _f.includes("network") || (_g = error.message) != null && _g.includes("timeout") ? new Error("Network error. Please check your connection and try again.") : new Error(`Code generation failed: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/textcortex-ai/src/lib/actions/create-email.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var createEmail = (0, import_pieces_framework8.createAction)({
  auth: textcortexAuth,
  name: "create_email",
  displayName: "Create Email",
  description: 'Compose an email using context, recipient ("To"), and sender ("From") metadata.',
  audience: "both",
  aiMetadata: { description: "Draft an email with TextCortex AI from supplied context plus optional sender/recipient. Pick an email mode (reply, general, customer support, cold, or from-bullets) which unlocks mode-specific inputs such as the received email and instructions for replies, purpose for cold outreach, or company details for support. Not idempotent: each call produces fresh, non-deterministic text and consumes account credits.", idempotent: !1 },
  props: {
    context: import_pieces_framework8.Property.LongText({
      displayName: "Context",
      description: "Context of the email. For example, a summary, or a bullet point list",
      required: !0
    }),
    mode: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Email Mode",
      description: "The style or tone to be used when writing",
      required: !1,
      defaultValue: "general",
      options: {
        options: [
          { label: "Reply", value: "reply" },
          { label: "General", value: "general" },
          { label: "Customer Support", value: "customer_support" },
          { label: "Cold Email", value: "cold" },
          { label: "From Bullets", value: "from_bullets" }
        ]
      }
    }),
    to: import_pieces_framework8.Property.ShortText({
      displayName: "To",
      description: "The recipient of the email",
      required: !1
    }),
    from: import_pieces_framework8.Property.ShortText({
      displayName: "From",
      description: "The sender of the email",
      required: !1
    }),
    instructions: import_pieces_framework8.Property.LongText({
      displayName: "Instructions",
      description: "Instructions for the email. Available when mode = reply",
      required: !1
    }),
    received_email: import_pieces_framework8.Property.LongText({
      displayName: "Received Email",
      description: "The email that is being replied to. Available when mode = reply",
      required: !1
    }),
    purpose: import_pieces_framework8.Property.ShortText({
      displayName: "Purpose",
      description: "Purpose of the email. Available when mode = cold",
      required: !1
    }),
    company_details: import_pieces_framework8.Property.LongText({
      displayName: "Company Details",
      description: "Details regarding the company. Available when mode = customer_support",
      required: !1
    }),
    model: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Model",
      description: "The language model to use",
      required: !1,
      defaultValue: "gemini-2-0-flash",
      options: {
        options: AI_MODELS
      }
    }),
    formality: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Formality",
      description: "The formality of the generated text",
      required: !1,
      defaultValue: "default",
      options: {
        options: FORMALITY_LEVELS
      }
    }),
    source_lang: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Source Language",
      description: "The language of the source text",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (Default)", value: "en" },
          { label: "Auto-detect", value: "auto" },
          ...LANGUAGES.filter((lang) => lang.value !== "en")
        ]
      }
    }),
    target_lang: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Target Language",
      description: "The language for the email",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (American)", value: "en" },
          { label: "English (British)", value: "en-gb" },
          { label: "Spanish", value: "es" },
          { label: "French", value: "fr" },
          { label: "German", value: "de" },
          { label: "Portuguese (Brazilian)", value: "pt-br" },
          { label: "Portuguese", value: "pt" },
          ...LANGUAGES.filter((lang) => !["en", "pt", "es", "fr", "de"].includes(lang.value))
        ]
      }
    }),
    max_tokens: import_pieces_framework8.Property.Number({
      displayName: "Max Tokens",
      description: "Maximum length of email (1-4096 tokens)",
      required: !1,
      defaultValue: 2048
    }),
    temperature: import_pieces_framework8.Property.Number({
      displayName: "Temperature",
      description: "Controls creativity (0.0-2.0). Higher values = more creative, lower = more focused.",
      required: !1
    }),
    n: import_pieces_framework8.Property.Number({
      displayName: "Number of Outputs",
      description: "How many email drafts to generate (1-5)",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g;
    try {
      let requestBody = {};
      context.propsValue.context && (requestBody.context = context.propsValue.context), context.propsValue.mode && context.propsValue.mode !== "general" && (requestBody.mode = context.propsValue.mode), context.propsValue.to && (requestBody.to = context.propsValue.to), context.propsValue.from && (requestBody.from = context.propsValue.from), context.propsValue.instructions && (requestBody.instructions = context.propsValue.instructions), context.propsValue.received_email && (requestBody.received_email = context.propsValue.received_email), context.propsValue.purpose && (requestBody.purpose = context.propsValue.purpose), context.propsValue.company_details && (requestBody.company_details = context.propsValue.company_details), context.propsValue.model && context.propsValue.model !== "gemini-2-0-flash" && (requestBody.model = context.propsValue.model), context.propsValue.formality && context.propsValue.formality !== "default" && (requestBody.formality = context.propsValue.formality), context.propsValue.source_lang && context.propsValue.source_lang !== "en" && (requestBody.source_lang = context.propsValue.source_lang), context.propsValue.target_lang && context.propsValue.target_lang !== "en" && (requestBody.target_lang = context.propsValue.target_lang), context.propsValue.max_tokens && context.propsValue.max_tokens !== 2048 && (requestBody.max_tokens = context.propsValue.max_tokens), context.propsValue.temperature !== void 0 && (requestBody.temperature = context.propsValue.temperature), context.propsValue.n && context.propsValue.n !== 1 && (requestBody.n = context.propsValue.n);
      let response = await textcortexCommon.apiCall({
        auth: context.auth,
        method: import_pieces_common8.HttpMethod.POST,
        resourceUri: API_ENDPOINTS.EMAILS,
        body: requestBody
      }), outputs = ((_a = response.body.data) == null ? void 0 : _a.outputs) || [];
      return {
        success: !0,
        email: outputs.length > 0 ? outputs[0].text : response.body.text || response.body,
        outputs,
        remaining_credits: (_b = response.body.data) == null ? void 0 : _b.remaining_credits,
        metadata: {
          context: context.propsValue.context,
          mode: context.propsValue.mode || "general",
          to: context.propsValue.to,
          from: context.propsValue.from,
          purpose: context.propsValue.purpose,
          model: context.propsValue.model || "gemini-2-0-flash",
          formality: context.propsValue.formality || "default",
          parameters: {
            max_tokens: context.propsValue.max_tokens || 2048,
            temperature: context.propsValue.temperature,
            n: context.propsValue.n || 1,
            source_lang: context.propsValue.source_lang || "en",
            target_lang: context.propsValue.target_lang || "en"
          },
          timestamp: (/* @__PURE__ */ new Date()).toISOString()
        }
      };
    } catch (error) {
      throw ((_c = error.response) == null ? void 0 : _c.status) === 401 ? new Error("Invalid API key. Please check your TextCortex API key.") : ((_d = error.response) == null ? void 0 : _d.status) === 429 ? new Error("Rate limit exceeded. Please wait and try again or upgrade your TextCortex plan.") : ((_e = error.response) == null ? void 0 : _e.status) === 400 ? new Error("Invalid request. Please check your input parameters.") : (_f = error.message) != null && _f.includes("network") || (_g = error.message) != null && _g.includes("timeout") ? new Error("Network error. Please check your connection and try again.") : new Error(`Email generation failed: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/textcortex-ai/src/lib/actions/create-product-description.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var createProductDescription = (0, import_pieces_framework9.createAction)({
  auth: textcortexAuth,
  name: "create_product_description",
  displayName: "Create Product Description",
  description: "Create a product description using details like name, brand, category, features, keywords.",
  audience: "both",
  aiMetadata: { description: "Generate a marketing product description with TextCortex AI from product attributes (name, brand, category, features, and comma-separated keywords). All fields are optional, so supply whatever product details are available; model, formality, and language are configurable. Not idempotent: each call produces fresh, non-deterministic text and consumes account credits.", idempotent: !1 },
  props: {
    name: import_pieces_framework9.Property.ShortText({
      displayName: "Product Name",
      description: "Name of the product",
      required: !1
    }),
    description: import_pieces_framework9.Property.LongText({
      displayName: "Product Features",
      description: "Features of the product",
      required: !1
    }),
    brand: import_pieces_framework9.Property.ShortText({
      displayName: "Brand",
      description: "Brand of the product",
      required: !1
    }),
    category: import_pieces_framework9.Property.ShortText({
      displayName: "Category",
      description: "Category of the product",
      required: !1
    }),
    keywords: import_pieces_framework9.Property.ShortText({
      displayName: "Keywords",
      description: "Keywords to include in the product description (comma-separated)",
      required: !1
    }),
    model: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Model",
      description: "The language model to use",
      required: !1,
      defaultValue: "gemini-2-0-flash",
      options: {
        options: AI_MODELS
      }
    }),
    formality: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Formality",
      description: "The formality of the generated text",
      required: !1,
      defaultValue: "default",
      options: {
        options: FORMALITY_LEVELS
      }
    }),
    source_lang: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Source Language",
      description: "The language of the source text",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (Default)", value: "en" },
          { label: "Auto-detect", value: "auto" },
          ...LANGUAGES.filter((lang) => lang.value !== "en")
        ]
      }
    }),
    target_lang: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Target Language",
      description: "The language for the product description",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (American)", value: "en" },
          { label: "English (British)", value: "en-gb" },
          { label: "Spanish", value: "es" },
          { label: "French", value: "fr" },
          { label: "German", value: "de" },
          { label: "Portuguese (Brazilian)", value: "pt-br" },
          { label: "Portuguese", value: "pt" },
          ...LANGUAGES.filter((lang) => !["en", "pt", "es", "fr", "de"].includes(lang.value))
        ]
      }
    }),
    max_tokens: import_pieces_framework9.Property.Number({
      displayName: "Max Tokens",
      description: "Maximum length of product description (1-4096 tokens)",
      required: !1,
      defaultValue: 2048
    }),
    temperature: import_pieces_framework9.Property.Number({
      displayName: "Temperature",
      description: "Controls creativity (0.0-2.0). Higher values = more creative, lower = more focused.",
      required: !1
    }),
    n: import_pieces_framework9.Property.Number({
      displayName: "Number of Outputs",
      description: "How many descriptions to generate (1-5)",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g;
    try {
      let requestBody = {};
      context.propsValue.name && (requestBody.name = context.propsValue.name), context.propsValue.description && (requestBody.description = context.propsValue.description), context.propsValue.brand && (requestBody.brand = context.propsValue.brand), context.propsValue.category && (requestBody.category = context.propsValue.category), context.propsValue.keywords && (requestBody.keywords = context.propsValue.keywords.split(",").map((k) => k.trim())), context.propsValue.model && context.propsValue.model !== "gemini-2-0-flash" && (requestBody.model = context.propsValue.model), context.propsValue.formality && context.propsValue.formality !== "default" && (requestBody.formality = context.propsValue.formality), context.propsValue.source_lang && context.propsValue.source_lang !== "en" && (requestBody.source_lang = context.propsValue.source_lang), context.propsValue.target_lang && context.propsValue.target_lang !== "en" && (requestBody.target_lang = context.propsValue.target_lang), context.propsValue.max_tokens && context.propsValue.max_tokens !== 2048 && (requestBody.max_tokens = context.propsValue.max_tokens), context.propsValue.temperature !== void 0 && (requestBody.temperature = context.propsValue.temperature), context.propsValue.n && context.propsValue.n !== 1 && (requestBody.n = context.propsValue.n);
      let response = await textcortexCommon.apiCall({
        auth: context.auth,
        method: import_pieces_common9.HttpMethod.POST,
        resourceUri: API_ENDPOINTS.PRODUCT_DESCRIPTIONS,
        body: requestBody
      }), outputs = ((_a = response.body.data) == null ? void 0 : _a.outputs) || [];
      return {
        success: !0,
        product_description: outputs.length > 0 ? outputs[0].text : response.body.text || response.body,
        outputs,
        remaining_credits: (_b = response.body.data) == null ? void 0 : _b.remaining_credits,
        metadata: {
          product_name: context.propsValue.name,
          brand: context.propsValue.brand,
          category: context.propsValue.category,
          keywords: context.propsValue.keywords,
          model: context.propsValue.model || "gemini-2-0-flash",
          formality: context.propsValue.formality || "default",
          parameters: {
            max_tokens: context.propsValue.max_tokens || 2048,
            temperature: context.propsValue.temperature,
            n: context.propsValue.n || 1,
            source_lang: context.propsValue.source_lang || "en",
            target_lang: context.propsValue.target_lang || "en"
          },
          timestamp: (/* @__PURE__ */ new Date()).toISOString()
        }
      };
    } catch (error) {
      throw ((_c = error.response) == null ? void 0 : _c.status) === 401 ? new Error("Invalid API key. Please check your TextCortex API key.") : ((_d = error.response) == null ? void 0 : _d.status) === 429 ? new Error("Rate limit exceeded. Please wait and try again or upgrade your TextCortex plan.") : ((_e = error.response) == null ? void 0 : _e.status) === 400 ? new Error("Invalid request. Please check your input parameters.") : (_f = error.message) != null && _f.includes("network") || (_g = error.message) != null && _g.includes("timeout") ? new Error("Network error. Please check your connection and try again.") : new Error(`Product description generation failed: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/textcortex-ai/src/lib/actions/create-summary.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var createSummary = (0, import_pieces_framework10.createAction)({
  auth: textcortexAuth,
  name: "create_summary",
  displayName: "Create Summary",
  description: "Summarize input text to concise form.",
  audience: "both",
  aiMetadata: { description: "Condense text into a shorter summary using TextCortex AI. Provide the content either as inline text or as a stored file ID (one is required); supports a default or embeddings summarization mode plus model, formality, and language options. Not idempotent: each call produces fresh, non-deterministic output and consumes account credits.", idempotent: !1 },
  props: {
    text: import_pieces_framework10.Property.LongText({
      displayName: "Text to Summarize",
      description: "The text to summarize",
      required: !1
    }),
    file_id: import_pieces_framework10.Property.ShortText({
      displayName: "File ID",
      description: "ID of the file to summarize",
      required: !1
    }),
    mode: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Mode",
      description: "Summarization mode",
      required: !1,
      defaultValue: "default",
      options: {
        options: [
          { label: "Default", value: "default" },
          { label: "Embeddings", value: "embeddings" }
        ]
      }
    }),
    model: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Model",
      description: "The language model to use",
      required: !1,
      defaultValue: "gemini-2-0-flash",
      options: {
        options: AI_MODELS
      }
    }),
    formality: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Formality",
      description: "The formality of the generated text",
      required: !1,
      defaultValue: "default",
      options: {
        options: FORMALITY_LEVELS
      }
    }),
    source_lang: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Source Language",
      description: "The language of the source text",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (Default)", value: "en" },
          { label: "Auto-detect", value: "auto" },
          ...LANGUAGES.filter((lang) => lang.value !== "en")
        ]
      }
    }),
    target_lang: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Target Language",
      description: "The language for the summary",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English (American)", value: "en" },
          { label: "English (British)", value: "en-gb" },
          { label: "Spanish", value: "es" },
          { label: "French", value: "fr" },
          { label: "German", value: "de" },
          { label: "Portuguese (Brazilian)", value: "pt-br" },
          { label: "Portuguese", value: "pt" },
          ...LANGUAGES.filter((lang) => !["en", "pt", "es", "fr", "de"].includes(lang.value))
        ]
      }
    }),
    max_tokens: import_pieces_framework10.Property.Number({
      displayName: "Max Tokens",
      description: "Maximum length of summary (1-4096 tokens)",
      required: !1,
      defaultValue: 2048
    }),
    temperature: import_pieces_framework10.Property.Number({
      displayName: "Temperature",
      description: "Controls creativity (0.0-2.0). Higher values = more creative, lower = more focused.",
      required: !1
    }),
    n: import_pieces_framework10.Property.Number({
      displayName: "Number of Outputs",
      description: "How many summaries to generate (1-5)",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g;
    try {
      if (!context.propsValue.text && !context.propsValue.file_id)
        throw new Error('Please provide either "Text to Summarize" or "File ID" to create a summary.');
      let requestBody = {};
      context.propsValue.text && (requestBody.text = context.propsValue.text), context.propsValue.file_id && (requestBody.file_id = context.propsValue.file_id), context.propsValue.mode && context.propsValue.mode !== "default" && (requestBody.mode = context.propsValue.mode), context.propsValue.model && context.propsValue.model !== "gemini-2-0-flash" && (requestBody.model = context.propsValue.model), context.propsValue.formality && context.propsValue.formality !== "default" && (requestBody.formality = context.propsValue.formality), context.propsValue.source_lang && context.propsValue.source_lang !== "en" && (requestBody.source_lang = context.propsValue.source_lang), context.propsValue.target_lang && context.propsValue.target_lang !== "en" && (requestBody.target_lang = context.propsValue.target_lang), context.propsValue.max_tokens && context.propsValue.max_tokens !== 2048 && (requestBody.max_tokens = context.propsValue.max_tokens), context.propsValue.temperature !== void 0 && (requestBody.temperature = context.propsValue.temperature), context.propsValue.n && context.propsValue.n !== 1 && (requestBody.n = context.propsValue.n);
      let response = await textcortexCommon.apiCall({
        auth: context.auth,
        method: import_pieces_common10.HttpMethod.POST,
        resourceUri: API_ENDPOINTS.SUMMARIZATIONS,
        body: requestBody
      }), outputs = ((_a = response.body.data) == null ? void 0 : _a.outputs) || [];
      return {
        success: !0,
        summary: outputs.length > 0 ? outputs[0].text : response.body.text || response.body,
        outputs,
        remaining_credits: (_b = response.body.data) == null ? void 0 : _b.remaining_credits,
        metadata: {
          original_text_length: context.propsValue.text ? context.propsValue.text.length : 0,
          mode: context.propsValue.mode || "default",
          model: context.propsValue.model || "gemini-2-0-flash",
          formality: context.propsValue.formality || "default",
          file_id: context.propsValue.file_id,
          parameters: {
            max_tokens: context.propsValue.max_tokens || 2048,
            temperature: context.propsValue.temperature,
            n: context.propsValue.n || 1,
            source_lang: context.propsValue.source_lang || "en",
            target_lang: context.propsValue.target_lang || "en"
          },
          timestamp: (/* @__PURE__ */ new Date()).toISOString()
        }
      };
    } catch (error) {
      throw ((_c = error.response) == null ? void 0 : _c.status) === 401 ? new Error("Invalid API key. Please check your TextCortex API key.") : ((_d = error.response) == null ? void 0 : _d.status) === 429 ? new Error("Rate limit exceeded. Please wait and try again or upgrade your TextCortex plan.") : ((_e = error.response) == null ? void 0 : _e.status) === 400 ? new Error("Invalid request. Please check your input parameters.") : (_f = error.message) != null && _f.includes("network") || (_g = error.message) != null && _g.includes("timeout") ? new Error("Network error. Please check your connection and try again.") : new Error(`Summary generation failed: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/textcortex-ai/src/index.ts
var textcortexAi = (0, import_pieces_framework11.createPiece)({
  displayName: "TextCortex AI",
  description: "AI-powered writing assistant for content creation, code generation, translations, and more using multiple AI models.",
  auth: textcortexAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/textcortex-ai.png",
  categories: [import_pieces_framework12.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["owuzo", "onyedikachi-david"],
  actions: [
    sendPrompt,
    createParaphrase,
    createSocialMediaCaption,
    createTranslation,
    createCode,
    createEmail,
    createProductDescription,
    createSummary,
    (0, import_pieces_common11.createCustomApiCallAction)({
      auth: textcortexAuth,
      baseUrl: () => baseUrl,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  textcortexAi
});
