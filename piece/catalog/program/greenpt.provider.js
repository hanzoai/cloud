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

// ../ap/packages/pieces/community/greenpt/src/index.ts
var index_exports = {};
__export(index_exports, {
  greenpt: () => greenpt
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/greenpt/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), greenptAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "API Key for Greenpt",
  required: !0
});

// ../ap/packages/pieces/community/greenpt/src/lib/actions/chat-completion.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/greenpt/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.greenpt.ai/v1";
async function makeRequest(apikey, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${apikey}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/greenpt/src/lib/actions/chat-completion.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), chatCompletion = (0, import_pieces_framework2.createAction)({
  audience: "both",
  auth: greenptAuth,
  name: "chatCompletion",
  displayName: "Ask GreenPT",
  description: "",
  aiMetadata: { description: "Sends one user message to a GreenPT chat model (green-l, green-r, or their raw variants) and returns the generated reply as plain text, carrying a single user turn only with no system prompt or conversation history. Choose this for text generation or question answering on GreenPT; use Create Embeddings for vector output and Transcribe Audio for speech-to-text. Not idempotent: each call runs a fresh, non-deterministic completion.", idempotent: !1 },
  props: {
    model: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Model",
      description: "The model to use ",
      required: !0,
      options: {
        disabled: !1,
        options: [
          {
            label: "Green L",
            value: "green-l"
          },
          {
            label: "Green L Raw",
            value: "green-l-raw"
          },
          {
            label: "Green R",
            value: "green-r"
          },
          {
            label: "Green R Raw",
            value: "green-r-raw"
          }
        ]
      }
    }),
    messages: import_pieces_framework2.Property.LongText({
      displayName: "Message",
      description: "Message to send",
      required: !0
    })
  },
  async run(context) {
    let { model, messages } = context.propsValue;
    return (await makeRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/chat/completions",
      {
        model,
        messages: [{ role: "user", content: messages }],
        stream: !1
      }
    )).choices[0].message.content;
  }
});

// ../ap/packages/pieces/community/greenpt/src/lib/actions/create-embeddings.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), createEmbeddings = (0, import_pieces_framework3.createAction)({
  audience: "both",
  auth: greenptAuth,
  name: "createEmbeddings",
  displayName: "Create Embeddings",
  description: "Generate embeddings for text input using GreenPT models for semantic search and similarity matching",
  aiMetadata: { description: "Converts text into embedding vectors with GreenPT's fixed green-embedding model, returned as floats or base64; newline-separated input is split so each non-empty line is embedded as its own vector, while single-line input embeds one string. Use for semantic search, clustering, or similarity scoring, rather than Ask GreenPT which returns generated text. Idempotent: no resource is created and the same text with the same encoding format yields the same vectors.", idempotent: !0 },
  props: {
    input: import_pieces_framework3.Property.LongText({
      displayName: "Input Text",
      description: "Input text to embed. Can be a single string or multiple texts separated by newlines",
      required: !0
    }),
    encoding_format: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Encoding Format",
      description: "The format to return the embeddings in",
      required: !1,
      defaultValue: "float",
      options: {
        disabled: !1,
        options: [
          {
            label: "Float",
            value: "float"
          },
          {
            label: "Base64",
            value: "base64"
          }
        ]
      }
    })
  },
  async run(context) {
    let { input, encoding_format } = context.propsValue, inputData = input.includes(`
`) ? input.split(`
`).filter((line) => line.trim().length > 0) : input;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/embeddings",
      {
        model: "green-embedding",
        input: inputData,
        encoding_format
      }
    );
  }
});

// ../ap/packages/pieces/community/greenpt/src/lib/actions/transcribe-audio.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var transcribeAudio = (0, import_pieces_framework4.createAction)({
  audience: "both",
  auth: greenptAuth,
  name: "transcribeAudio",
  displayName: "Transcribe Audio",
  description: "Transcribe pre-recorded audio files with speaker diarization and advanced features",
  aiMetadata: { description: "Transcribes a pre-recorded audio file with a GreenPT speech-to-text model (green-s or green-s-pro), with optional speaker diarization, punctuation, smart formatting, filler words, numeral conversion, and sentiment, topic, or intent analysis. Requires the audio as a file input, supplied either as base64 data or as a URL the engine fetches, language is auto-detected unless a code is supplied, and only the plain transcript text is returned. Use this for speech input; use Ask GreenPT for text generation. Read-only inference and idempotent: nothing is created and the same file with the same options yields the same transcript.", idempotent: !0 },
  props: {
    audioUrl: import_pieces_framework4.Property.File({
      displayName: "Audio File",
      description: "Audio file to transcribe (WAV, MP3, FLAC, etc.)",
      required: !0
    }),
    model: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Model",
      description: "Speech-to-text model to use",
      required: !1,
      defaultValue: "green-s",
      options: {
        disabled: !1,
        options: [
          {
            label: "Green S",
            value: "green-s"
          },
          {
            label: "Green S Pro",
            value: "green-s-pro"
          }
        ]
      }
    }),
    language: import_pieces_framework4.Property.ShortText({
      displayName: "Language",
      description: "Language code (e.g., en, fr, de). Auto-detected if not specified",
      required: !1
    }),
    diarize: import_pieces_framework4.Property.Checkbox({
      displayName: "Speaker Diarization",
      description: "Enable speaker diarization to identify different speakers",
      required: !1,
      defaultValue: !1
    }),
    punctuate: import_pieces_framework4.Property.Checkbox({
      displayName: "Punctuate",
      description: "Add punctuation and capitalization to transcript",
      required: !1,
      defaultValue: !0
    }),
    smart_format: import_pieces_framework4.Property.Checkbox({
      displayName: "Smart Format",
      description: "Apply formatting to transcript output for improved readability",
      required: !1,
      defaultValue: !1
    }),
    filler_words: import_pieces_framework4.Property.Checkbox({
      displayName: "Include Filler Words",
      description: 'Include filler words like "uh" and "um" in transcript',
      required: !1,
      defaultValue: !1
    }),
    numerals: import_pieces_framework4.Property.Checkbox({
      displayName: "Convert Numerals",
      description: "Convert numbers from written format to numerical format",
      required: !1,
      defaultValue: !1
    }),
    sentiment: import_pieces_framework4.Property.Checkbox({
      displayName: "Analyze Sentiment",
      description: "Analyze sentiment throughout the transcript",
      required: !1,
      defaultValue: !1
    }),
    topics: import_pieces_framework4.Property.Checkbox({
      displayName: "Detect Topics",
      description: "Detect topics throughout the transcript",
      required: !1,
      defaultValue: !1
    }),
    intents: import_pieces_framework4.Property.Checkbox({
      displayName: "Recognize Intents",
      description: "Recognize speaker intent throughout the transcript",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      audioUrl,
      model,
      language,
      diarize,
      punctuate,
      smart_format,
      filler_words,
      numerals,
      sentiment,
      topics,
      intents
    } = context.propsValue, queryParams = new URLSearchParams();
    model && queryParams.append("model", model), language && queryParams.append("language", language), diarize && queryParams.append("diarize", "true"), punctuate && queryParams.append("punctuate", "true"), smart_format && queryParams.append("smart_format", "true"), filler_words && queryParams.append("filler_words", "true"), numerals && queryParams.append("numerals", "true"), sentiment && queryParams.append("sentiment", "true"), topics && queryParams.append("topics", "true"), intents && queryParams.append("intents", "true");
    let url = `https://api.greenpt.ai/v1/listen?${queryParams.toString()}`, fileData = audioUrl.data;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url,
      headers: {
        Authorization: `Token ${context.auth.secret_text}`,
        "Content-Type": "audio/wav"
      },
      body: fileData
    })).body.results.channels[0].alternatives[0].transcript;
  }
});

// ../ap/packages/pieces/community/greenpt/src/index.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), greenpt = (0, import_pieces_framework5.createPiece)({
  displayName: "GreenPT",
  description: "GreenPT is a green AI and privacy friendly GPT-powered chat platform",
  auth: greenptAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/greenpt.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework6.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  actions: [
    chatCompletion,
    createEmbeddings,
    transcribeAudio,
    (0, import_pieces_common5.createCustomApiCallAction)({
      auth: greenptAuth,
      baseUrl: () => "https://api.greenpt.ai/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  greenpt
});
