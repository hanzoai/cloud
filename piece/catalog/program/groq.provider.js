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

// ../ap/packages/pieces/community/groq/src/index.ts
var index_exports = {};
__export(index_exports, {
  groq: () => groq,
  groqAuth: () => groqAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/groq/src/lib/actions/ask-groq.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/groq/src/lib/output-schemas.ts
var askAiActionOutputSchema = {
  fields: [
    {
      key: "answer",
      label: "Answer",
      value: "",
      listItems: [
        {
          key: "Content",
          label: "Content"
        }
      ]
    }
  ]
}, transcribeAudioActionOutputSchema = {
  fields: [
    {
      key: "text",
      label: "Transcribed Text"
    },
    {
      key: "id",
      label: "Request ID",
      value: "x_groq.id"
    }
  ]
}, translateAudioActionOutputSchema = {
  fields: [
    {
      key: "text",
      label: "Translated Text",
      value: "text"
    }
  ]
};

// ../ap/packages/pieces/community/groq/src/lib/actions/ask-groq.ts
var askGroq = (0, import_pieces_framework.createAction)({
  audience: "both",
  auth: groqAuth,
  name: "ask-ai",
  classification: "READ",
  displayName: "Ask AI",
  description: "Ask Groq anything using fast language models.",
  aiMetadata: { description: "Sends a prompt to a Groq-hosted chat model (Whisper speech models are excluded from the model list) and returns the generated text, with optional sampling controls and a roles array for system instructions. Runs stateless by default, or shares conversation history across runs and flows when a memory key is set. This is the only text-generation action in the piece - pick the sibling Transcribe Audio or Translate Audio actions when the input is a sound file rather than text. Requires a model, a question, and a maximum token count; not idempotent: each call produces a fresh completion and, when a memory key is set, appends to the stored project-scoped history.", idempotent: !1 },
  props: {
    model: import_pieces_framework.Property.Dropdown({
      auth: groqAuth,
      displayName: "Model",
      required: !0,
      description: "The model which will generate the completion.",
      refreshers: [],
      defaultValue: "llama-3.1-70b-versatile",
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your Groq account first.",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await import_pieces_common.httpClient.sendRequest({
              url: "https://api.groq.com/openai/v1/models",
              method: import_pieces_common.HttpMethod.GET,
              authentication: {
                type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
                token: auth.secret_text
              }
            })).body.data.filter(
              (model) => !model.id.toLowerCase().includes("whisper")
            ).map((model) => ({
              label: model.id,
              value: model.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Couldn't load models, API key is invalid"
          };
        }
      }
    }),
    prompt: import_pieces_framework.Property.LongText({
      displayName: "Question",
      required: !0
    }),
    temperature: import_pieces_framework.Property.Number({
      displayName: "Temperature",
      required: !1,
      description: "Controls randomness: Lowering results in less random completions. As the temperature approaches zero, the model will become deterministic and repetitive.",
      defaultValue: 0.9
    }),
    maxTokens: import_pieces_framework.Property.Number({
      displayName: "Maximum Tokens",
      required: !0,
      description: "The maximum number of tokens to generate. The total length of input tokens and generated tokens is limited by the model's context length.",
      defaultValue: 2048
    }),
    topP: import_pieces_framework.Property.Number({
      displayName: "Top P",
      required: !1,
      description: "An alternative to sampling with temperature, called nucleus sampling, where the model considers the results of the tokens with top_p probability mass. So 0.1 means only the tokens comprising the top 10% probability mass are considered.",
      defaultValue: 1
    }),
    frequencyPenalty: import_pieces_framework.Property.Number({
      displayName: "Frequency penalty",
      required: !1,
      description: "Number between -2.0 and 2.0. Positive values penalize new tokens based on their existing frequency in the text so far, decreasing the model's likelihood to repeat the same line verbatim.",
      defaultValue: 0
    }),
    presencePenalty: import_pieces_framework.Property.Number({
      displayName: "Presence penalty",
      required: !1,
      description: "Number between -2.0 and 2.0. Positive values penalize new tokens based on whether they appear in the text so far, increasing the model's likelihood to talk about new topics.",
      defaultValue: 0.6
    }),
    memoryKey: import_pieces_framework.Property.ShortText({
      displayName: "Memory Key",
      description: "A memory key that will keep the chat history shared across runs and flows. Keep it empty to leave Groq without memory of previous messages.",
      required: !1
    }),
    roles: import_pieces_framework.Property.Json({
      displayName: "Roles",
      required: !1,
      description: "Array of roles to specify more accurate response",
      defaultValue: [{ role: "system", content: "You are a helpful assistant." }]
    })
  },
  outputSchema: askAiActionOutputSchema,
  async run({ auth, propsValue, store }) {
    var _a, _b;
    let {
      model,
      temperature,
      maxTokens,
      topP,
      frequencyPenalty,
      presencePenalty,
      prompt,
      memoryKey
    } = propsValue, messageHistory = [];
    memoryKey && (messageHistory = (_a = await store.get(memoryKey, import_pieces_framework.StoreScope.PROJECT)) != null ? _a : []), messageHistory.push({
      role: "user",
      content: prompt
    });
    let roles = (propsValue.roles ? propsValue.roles : []).map((item) => {
      if (!["system", "user", "assistant"].includes(item.role))
        throw new Error("The only available roles are: [system, user, assistant]");
      return {
        role: item.role,
        content: item.content
      };
    }), completion = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.groq.com/openai/v1/chat/completions",
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      body: {
        model,
        messages: [...roles, ...messageHistory],
        temperature,
        top_p: topP,
        frequency_penalty: frequencyPenalty,
        presence_penalty: presencePenalty,
        max_completion_tokens: maxTokens
      }
    });
    messageHistory = [...messageHistory, completion.body.choices[0].message], memoryKey && await store.put(memoryKey, messageHistory, import_pieces_framework.StoreScope.PROJECT);
    let rawContent = completion.body.choices[0].message.content, thinkRegex = /<think>([\s\S]*?)<\/think>/, thinkMatch = rawContent.match(thinkRegex), responseStructure = [];
    if (thinkMatch) {
      let thinkContent = thinkMatch[1].trim(), finalContent = ((_b = rawContent.split("</think>").pop()) == null ? void 0 : _b.trim()) || "";
      responseStructure.push({
        Think: thinkContent,
        Content: finalContent
      });
    } else
      responseStructure.push({
        Think: null,
        Content: rawContent
      });
    return responseStructure;
  }
});

// ../ap/packages/pieces/community/groq/src/lib/actions/transcribe-audio.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");
var transcribeAudio = (0, import_pieces_framework2.createAction)({
  audience: "both",
  auth: groqAuth,
  name: "transcribe-audio",
  classification: "READ",
  displayName: "Transcribe Audio",
  description: "Transcribes audio into text in the input language.",
  aiMetadata: { description: "Runs Groq speech-to-text (Whisper) over an uploaded audio file and returns the spoken content transcribed in its original language; a response-format prop switches between plain text, JSON, and verbose JSON with segment timings. Choose this over the sibling Translate Audio action whenever the transcript must stay in the language that was spoken, since Translate Audio always renders English. Requires an audio file (flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, webm) and a Whisper model, with an optional ISO-639-1 language hint that improves accuracy; not idempotent: each call re-runs the model and may return slightly different text.", idempotent: !1 },
  props: {
    file: import_pieces_framework2.Property.File({
      displayName: "Audio File",
      required: !0,
      description: "The audio file to transcribe. Supported formats: flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, webm."
    }),
    model: import_pieces_framework2.Property.Dropdown({
      displayName: "Model",
      auth: groqAuth,
      required: !0,
      description: "The model to use for transcription.",
      refreshers: [],
      defaultValue: "whisper-large-v3",
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your Groq account first.",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await import_pieces_common2.httpClient.sendRequest({
              url: "https://api.groq.com/openai/v1/models",
              method: import_pieces_common2.HttpMethod.GET,
              authentication: {
                type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
                token: auth.secret_text
              }
            })).body.data.filter(
              (model) => model.id.toLowerCase().includes("whisper")
            ).map((model) => ({
              label: model.id,
              value: model.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Couldn't load models, API key is invalid"
          };
        }
      }
    }),
    language: import_pieces_framework2.Property.ShortText({
      displayName: "Language",
      required: !1,
      description: 'The language of the input audio in ISO-639-1 format (e.g., "en" for English). This will improve accuracy and latency.'
    }),
    prompt: import_pieces_framework2.Property.LongText({
      displayName: "Prompt",
      required: !1,
      description: "An optional text to guide the model's style or continue a previous audio segment. The prompt should match the audio language."
    }),
    temperature: import_pieces_framework2.Property.Number({
      displayName: "Temperature",
      required: !1,
      description: "The sampling temperature, between 0 and 1. Higher values like 0.8 will make the output more random, while lower values like 0.2 will make it more focused and deterministic.",
      defaultValue: 0
    }),
    responseFormat: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Response Format",
      required: !1,
      description: "The format of the transcript output.",
      defaultValue: "json",
      options: {
        disabled: !1,
        options: [
          { label: "JSON", value: "json" },
          { label: "Text", value: "text" },
          { label: "Verbose JSON", value: "verbose_json" }
        ]
      }
    })
  },
  outputSchema: transcribeAudioActionOutputSchema,
  async run({ auth, propsValue }) {
    let { file, model, language, prompt, temperature, responseFormat } = propsValue, formData = new FormData();
    return formData.append("file", new Blob([file.data]), file.filename), formData.append("model", model), language && formData.append("language", language), prompt && formData.append("prompt", prompt), temperature !== void 0 && formData.append("temperature", temperature.toString()), responseFormat && formData.append("response_format", responseFormat), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.groq.com/openai/v1/audio/transcriptions",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      headers: {
        "Content-Type": "multipart/form-data"
      },
      body: formData
    })).body;
  }
});

// ../ap/packages/pieces/community/groq/src/lib/actions/translate-audio.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var translateAudio = (0, import_pieces_framework3.createAction)({
  audience: "both",
  auth: groqAuth,
  name: "translate-audio",
  classification: "READ",
  displayName: "Translate Audio",
  description: "Translates audio into English text.",
  aiMetadata: { description: "Runs Groq speech-to-text (Whisper) over an uploaded audio file and returns the spoken content as English text, translating from whatever language was spoken; a response-format prop switches between plain text, JSON, and verbose JSON with segment timings. Pick this only when English output is wanted - the target language cannot be changed, and the sibling Transcribe Audio action keeps the transcript in the original spoken language. Requires an audio file (flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, webm) and a Whisper model; not idempotent: each call re-runs the model and may return slightly different text.", idempotent: !1 },
  props: {
    file: import_pieces_framework3.Property.File({
      displayName: "Audio File",
      required: !0,
      description: "The audio file to translate. Supported formats: flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, webm."
    }),
    model: import_pieces_framework3.Property.Dropdown({
      displayName: "Model",
      required: !0,
      description: "The model to use for translation.",
      refreshers: [],
      defaultValue: "whisper-large-v3",
      auth: groqAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your Groq account first.",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await import_pieces_common3.httpClient.sendRequest({
              url: "https://api.groq.com/openai/v1/models",
              method: import_pieces_common3.HttpMethod.GET,
              authentication: {
                type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
                token: auth.secret_text
              }
            })).body.data.filter(
              (model) => model.id.toLowerCase().includes("whisper")
            ).map((model) => ({
              label: model.id,
              value: model.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Couldn't load models, API key is invalid"
          };
        }
      }
    }),
    prompt: import_pieces_framework3.Property.LongText({
      displayName: "Prompt",
      required: !1,
      description: "An optional text in English to guide the model's style or continue a previous audio segment."
    }),
    temperature: import_pieces_framework3.Property.Number({
      displayName: "Temperature",
      required: !1,
      description: "The sampling temperature, between 0 and 1. Higher values like 0.8 will make the output more random, while lower values like 0.2 will make it more focused and deterministic.",
      defaultValue: 0
    }),
    responseFormat: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Response Format",
      required: !1,
      description: "The format of the translation output.",
      defaultValue: "json",
      options: {
        disabled: !1,
        options: [
          { label: "JSON", value: "json" },
          { label: "Text", value: "text" },
          { label: "Verbose JSON", value: "verbose_json" }
        ]
      }
    })
  },
  outputSchema: translateAudioActionOutputSchema,
  async run({ auth, propsValue }) {
    let { file, model, prompt, temperature, responseFormat } = propsValue, formData = new FormData();
    return formData.append("file", new Blob([file.data]), file.filename), formData.append("model", model), prompt && formData.append("prompt", prompt), temperature !== void 0 && formData.append("temperature", temperature.toString()), responseFormat && formData.append("response_format", responseFormat), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://api.groq.com/openai/v1/audio/translations",
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      headers: {
        "Content-Type": "multipart/form-data"
      },
      body: formData
    })).body;
  }
});

// ../ap/packages/pieces/community/groq/src/index.ts
var baseUrl = "https://api.groq.com/openai/v1", groqAuth = import_pieces_framework4.PieceAuth.SecretText({
  description: "Enter your Groq API Key",
  displayName: "API Key",
  required: !0,
  validate: async (auth) => {
    try {
      return await import_pieces_common4.httpClient.sendRequest({
        url: `${baseUrl}/models`,
        method: import_pieces_common4.HttpMethod.GET,
        authentication: {
          type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
          token: auth.auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key"
      };
    }
  }
}), groq = (0, import_pieces_framework4.createPiece)({
  displayName: "Groq",
  description: "Use Groq's fast language models and audio processing capabilities.",
  minimumSupportedRelease: "0.9.0",
  logoUrl: "https://cdn.activepieces.com/pieces/groq.png",
  categories: [import_pieces_framework5.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  auth: groqAuth,
  actions: [
    askGroq,
    transcribeAudio,
    translateAudio,
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: groqAuth,
      baseUrl: () => baseUrl,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  authors: ["abuaboud"],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  groq,
  groqAuth
});
