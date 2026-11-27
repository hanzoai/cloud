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

// ../ap/packages/pieces/community/deepl/src/index.ts
var index_exports = {};
__export(index_exports, {
  deepl: () => deepl,
  deeplAuth: () => deeplAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/deepl/src/lib/actions/translate-text.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var translateText = (0, import_pieces_framework.createAction)({
  name: "translate_text",
  auth: deeplAuth,
  displayName: "Translate text",
  description: "Translate a text to the target language",
  audience: "both",
  aiMetadata: { description: "Translates a block of plain text into a target language using DeepL's neural machine translation. Source language is auto-detected unless explicitly set, and an optional glossary, formality level, and XML/HTML tag handling can be supplied. Choose this to translate natural-language content; the target language is required. Idempotent: re-translating the same text with the same settings yields the same result and creates no resource.", idempotent: !0 },
  props: {
    text: import_pieces_framework.Property.LongText({
      displayName: "Text",
      required: !0,
      description: "Text to be translated. Only UTF-8-encoded plain text is supported."
    }),
    target_lang: import_pieces_framework.Property.StaticDropdown({
      displayName: "Target language",
      description: "The language into which the text should be translated.",
      required: !0,
      options: {
        options: [
          { value: "BG", label: "Bulgarian" },
          { value: "CS", label: "Czech" },
          { value: "DA", label: "Danish" },
          { value: "DE", label: "German" },
          { value: "EL", label: "Greek" },
          { value: "EN-GB", label: "English (British)" },
          { value: "EN-US", label: "English (American)" },
          { value: "ES", label: "Spanish" },
          { value: "ET", label: "Estonian" },
          { value: "FI", label: "Finnish" },
          { value: "FR", label: "French" },
          { value: "HU", label: "Hungarian" },
          { value: "ID", label: "Indonesian" },
          { value: "IT", label: "Italian" },
          { value: "JA", label: "Japanese" },
          { value: "KO", label: "Korean" },
          { value: "LT", label: "Lithuanian" },
          { value: "LV", label: "Latvian" },
          { value: "NB", label: "Norwegian" },
          { value: "NL", label: "Dutch" },
          { value: "PL", label: "Polish" },
          { value: "PT-BR", label: "Portuguese (Brazilian)" },
          { value: "PT-PT", label: "Portuguese" },
          { value: "RO", label: "Romanian" },
          { value: "RU", label: "Russian" },
          { value: "SK", label: "Slovak" },
          { value: "SL", label: "Slovenian" },
          { value: "SV", label: "Swedish" },
          { value: "TR", label: "Turkish" },
          { value: "UK", label: "Ukrainian" },
          { value: "ZH", label: "Chinese (simplified)" }
        ]
      }
    }),
    source_lang: import_pieces_framework.Property.StaticDropdown({
      displayName: "Source language",
      description: "Language of the text to be translated",
      required: !1,
      options: {
        options: [
          { value: "BG", label: "Bulgarian" },
          { value: "CS", label: "Czech" },
          { value: "DA", label: "Danish" },
          { value: "DE", label: "German" },
          { value: "EL", label: "Greek" },
          { value: "EN", label: "English" },
          { value: "ES", label: "Spanish" },
          { value: "ET", label: "Estonian" },
          { value: "FI", label: "Finnish" },
          { value: "FR", label: "French" },
          { value: "HU", label: "Hungarian" },
          { value: "ID", label: "Indonesian" },
          { value: "IT", label: "Italian" },
          { value: "JA", label: "Japanese" },
          { value: "KO", label: "Korean" },
          { value: "LT", label: "Lithuanian" },
          { value: "LV", label: "Latvian" },
          { value: "NB", label: "Norwegian" },
          { value: "NL", label: "Dutch" },
          { value: "PL", label: "Polish" },
          { value: "PT", label: "Portuguese" },
          { value: "RO", label: "Romanian" },
          { value: "RU", label: "Russian" },
          { value: "SK", label: "Slovak" },
          { value: "SL", label: "Slovenian" },
          { value: "SV", label: "Swedish" },
          { value: "TR", label: "Turkish" },
          { value: "UK", label: "Ukrainian" },
          { value: "ZH", label: "Chinese" }
        ]
      }
    }),
    split_sentences: import_pieces_framework.Property.StaticDropdown({
      displayName: "Split sentences",
      description: "Sets whether the translation engine should first split the input into sentences. For text translations where tag_handling is not set to html, the default value is 1, meaning the engine splits on punctuation and on newlines.",
      required: !1,
      options: {
        options: [
          {
            value: "0",
            label: "No splitting at all, whole input is treated as one sentence"
          },
          { value: "1", label: "Splits on punctuation and on newlines" },
          {
            value: "nonewlines",
            label: "Splits on punctuation only, ignoring newlines"
          }
        ]
      }
    }),
    preserve_formatting: import_pieces_framework.Property.StaticDropdown({
      displayName: "Preserve formatting",
      description: "Sets whether the translation engine should respect the original formatting, even if it would usually correct some aspects.",
      required: !1,
      options: {
        options: [
          { value: !0, label: "Yes" },
          { value: !1, label: "No" }
        ]
      }
    }),
    formality: import_pieces_framework.Property.StaticDropdown({
      displayName: "Formality",
      description: "Sets whether the translated text should lean towards formal or informal language.",
      required: !1,
      options: {
        options: [
          { value: "default", label: "Default" },
          { value: "more", label: "For a more formal language" },
          { value: "less", label: "For a more informal language" },
          {
            value: "prefer_more",
            label: "For a more formal language if available, otherwise fallback to default formality"
          },
          {
            value: "prefer_less",
            label: "For a more informal language if available, otherwise fallback to default formality"
          }
        ]
      }
    }),
    glossary_id: import_pieces_framework.Property.ShortText({
      displayName: "Glossary id",
      description: "Specify the glossary to use for the translation.",
      required: !1
    }),
    tag_handling: import_pieces_framework.Property.StaticDropdown({
      displayName: "Tag handling",
      description: "Sets which kind of tags should be handled.",
      required: !1,
      options: {
        options: [
          { value: "xml", label: "Enable XML tag handling" },
          { value: "html", label: "Enable HTML tag handling" }
        ]
      }
    }),
    outline_detection: import_pieces_framework.Property.StaticDropdown({
      displayName: "Outline detection",
      description: "The automatic detection of the XML structure won't yield best results in all XML files.",
      required: !1,
      options: {
        options: [
          { value: !0, label: "Yes" },
          { value: !1, label: "No" }
        ]
      }
    }),
    non_splitting_tags: import_pieces_framework.Property.ShortText({
      displayName: "Non splitting tags",
      description: "Comma-separated list of XML or HTML tags.",
      required: !1
    }),
    splitting_tags: import_pieces_framework.Property.ShortText({
      displayName: "Splitting tags",
      description: "Comma-separated list of XML or HTML tags.",
      required: !1
    }),
    ignore_tags: import_pieces_framework.Property.ShortText({
      displayName: "Ignore tags",
      description: "Comma-separated list of XML or HTML tags.",
      required: !1
    })
  },
  async run(context) {
    let DEEPL_FREE_URL = "https://api-free.deepl.com/v2/translate", DEEPL_PAID_URL = "https://api.deepl.com/v2/translate", {
      text,
      target_lang,
      source_lang,
      split_sentences,
      preserve_formatting,
      formality,
      glossary_id,
      tag_handling,
      outline_detection,
      non_splitting_tags,
      splitting_tags,
      ignore_tags
    } = context.propsValue;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: context.auth.props.type === "free" ? DEEPL_FREE_URL : DEEPL_PAID_URL,
      headers: {
        Authorization: `DeepL-Auth-Key ${context.auth.props.key}`,
        "Content-Type": "application/json"
      },
      body: {
        text: [text],
        target_lang,
        source_lang,
        split_sentences,
        preserve_formatting,
        formality,
        glossary_id,
        tag_handling,
        outline_detection,
        non_splitting_tags: non_splitting_tags == null ? void 0 : non_splitting_tags.split(","),
        splitting_tags: splitting_tags == null ? void 0 : splitting_tags.split(","),
        ignore_tags: ignore_tags == null ? void 0 : ignore_tags.split(",")
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/deepl/src/index.ts
var markdownDescription = `
Follow these instructions to get your DeepL API Key:

1. Log in to your DeepL account.
2. Visit https://www.deepl.com/account/summary
3. Go to the API section and obtain your DeepL API Key.
`, deeplAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  description: markdownDescription,
  props: {
    key: import_pieces_framework2.Property.ShortText({
      displayName: "Api key",
      description: "Enter the api key",
      required: !0
    }),
    type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Api url",
      description: "Select api url",
      required: !0,
      options: {
        options: [
          {
            label: "Free API",
            value: "free"
          },
          {
            label: "Paid API",
            value: "paid"
          }
        ]
      }
    })
  },
  required: !0
}), deepl = (0, import_pieces_framework2.createPiece)({
  displayName: "DeepL",
  description: "AI-powered language translation",
  auth: deeplAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/deepl.png",
  categories: [],
  authors: ["BBND", "kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    translateText,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: (auth) => auth.type === "free" ? "https://api-free.deepl.com/v2" : "https://api.deepl.com/v2",
      // Replace with the actual base URL
      auth: deeplAuth,
      authMapping: async (auth) => ({
        Authorization: `DeepL-Auth-Key ${auth.props.key}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  deepl,
  deeplAuth
});
