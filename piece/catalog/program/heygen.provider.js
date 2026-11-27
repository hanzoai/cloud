"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/heygen/src/index.ts
var index_exports = {};
__export(index_exports, {
  heygen: () => heygen
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common15 = require("@activepieces/pieces-common"), import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_framework17 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/heygen/src/lib/actions/create-a-video-from-template.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/heygen/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/heygen/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL_V1 = "https://api.heygen.com/v1", BASE_URL_V2 = "https://api.heygen.com/v2";
async function heygenApiCall({
  apiKey,
  method,
  resourceUri,
  query,
  body,
  apiVersion
}) {
  let qs = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (qs[key] = String(value));
  let url = (apiVersion === "v1" ? BASE_URL_V1 : BASE_URL_V2) + resourceUri, request = {
    method,
    url,
    headers: {
      "X-Api-Key": apiKey
    },
    queryParams: qs,
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}

// ../ap/packages/pieces/community/heygen/src/lib/common/auth.ts
var heygenAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can obtain your API key by navigating to your Space Settings in HeyGen App.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await heygenApiCall({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: "/user/me",
        apiVersion: "v1"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key."
      };
    }
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var folderDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Folder",
  description: "Select the folder to store the video.",
  required: !1,
  refreshers: [],
  auth: heygenAuth,
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: "/folders",
      apiVersion: "v1"
    })).data.folders.map((folder) => ({
      label: folder.name,
      value: folder.id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  }
}), brandVoiceDropdown = import_pieces_framework2.Property.Dropdown({
  auth: heygenAuth,
  displayName: "Brand Voice",
  description: "Select the Brand Voice to apply to the video.",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: "/brand_voice/list",
      apiVersion: "v1"
    })).data.list.map((voice) => ({
      label: voice.name,
      value: voice.id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  }
}), templateDropdown = import_pieces_framework2.Property.Dropdown({
  auth: heygenAuth,
  displayName: "Template",
  description: "Select the template to generate the video.",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: "/templates",
      apiVersion: "v2"
    })).data.templates.map((template) => ({
      label: template.name,
      value: template.template_id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  }
}), supportedLanguagesDropdown = import_pieces_framework2.Property.Dropdown({
  auth: heygenAuth,
  displayName: "Supported Language",
  description: "Select the language for video translation.",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: "/video_translate/target_languages",
      apiVersion: "v2"
    })).data.languages.map((lang) => ({
      label: lang,
      value: lang
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  }
}), templateVariables = import_pieces_framework2.Property.DynamicProperties({
  auth: heygenAuth,
  displayName: "Template Varriables",
  refreshers: ["templateId"],
  required: !1,
  props: async ({ auth, templateId }) => {
    if (!auth || !templateId) return {};
    let fields = {};
    try {
      let response = await heygenApiCall({
        apiKey: auth.secret_text,
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: `/template/${templateId}`,
        apiVersion: "v2"
      }), variables = response.data.variables;
      if (!(0, import_pieces_framework3.isNil)(variables)) return {};
      for (let [key, value] of Object.entries(response.data.variables)) {
        let fieldKey = key;
        switch (value.type) {
          case "text":
            fields[fieldKey] = import_pieces_framework2.Property.ShortText({
              displayName: fieldKey,
              required: !1,
              description: "Provide text value."
            });
            break;
          case "image":
            fields[fieldKey] = import_pieces_framework2.Property.ShortText({
              displayName: fieldKey,
              required: !1,
              description: "Provide image URL."
            });
            break;
          case "video":
            fields[fieldKey] = import_pieces_framework2.Property.ShortText({
              displayName: fieldKey,
              required: !1,
              description: "Provide video URL."
            });
            break;
          case "audio":
            fields[fieldKey] = import_pieces_framework2.Property.ShortText({
              displayName: fieldKey,
              required: !1,
              description: "Provide audio URL."
            });
            break;
          case "character": {
            let characters = await heygenApiCall({
              apiKey: auth,
              method: import_pieces_common3.HttpMethod.GET,
              resourceUri: "/avatars",
              apiVersion: "v2"
            }), options = [
              ...characters.avatars.map((avatar) => ({
                label: avatar.avatar_name,
                value: avatar.avatar_id
              })),
              ...characters.talking_photos.map((photo) => ({
                label: photo.talking_photo_name,
                value: photo.talking_photo_id
              }))
            ];
            fields[fieldKey] = import_pieces_framework2.Property.StaticDropdown({
              displayName: fieldKey,
              required: !1,
              description: "Select one of avatar or talking photo.",
              options: { disabled: !1, options }
            });
            break;
          }
          case "voice": {
            let voices = await heygenApiCall({
              apiKey: auth,
              method: import_pieces_common3.HttpMethod.GET,
              resourceUri: "/voices",
              apiVersion: "v2"
            });
            fields[fieldKey] = import_pieces_framework2.Property.StaticDropdown({
              displayName: fieldKey,
              required: !1,
              options: {
                disabled: !1,
                options: voices.voices.map((voice) => ({
                  label: voice.name,
                  value: voice.voice_id
                }))
              }
            });
            break;
          }
          default:
            break;
        }
      }
      return fields;
    } catch (error) {
      return console.error(`${error instanceof Error ? error.message : "Unknown error"}`), {};
    }
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/actions/create-a-video-from-template.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var createVideoFromTemplateAction = (0, import_pieces_framework4.createAction)({
  auth: heygenAuth,
  name: "create-video-from-template",
  classification: "WRITE",
  displayName: "Create Video from Template",
  description: "Create a video using a selected template.",
  audience: "both",
  aiMetadata: {
    description: "Submits a new HeyGen avatar-video render from an existing template, filling in the template variables (text, image/video/audio URLs, character, voice). Use to programmatically generate videos at scale from a reusable template; requires a valid template ID and produces a new render each call (not idempotent). Rendering is asynchronous \u2014 poll Retrieve Video Status or use the completion trigger for the finished output.",
    idempotent: !1
  },
  props: {
    templateId: templateDropdown,
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Video Title",
      required: !0,
      description: "Title of the generated video."
    }),
    caption: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable Captions",
      required: !1,
      defaultValue: !1
    }),
    includeGif: import_pieces_framework4.Property.Checkbox({
      displayName: "Include GIF Preview",
      required: !1,
      defaultValue: !1
    }),
    enableSharing: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable Public Sharing",
      required: !1,
      defaultValue: !1
    }),
    folderId: folderDropdown,
    brandVoiceId: brandVoiceDropdown,
    callbackUrl: import_pieces_framework4.Property.ShortText({
      displayName: "Callback URL",
      required: !1,
      description: "Webhook URL to notify when video rendering is complete."
    }),
    dimensionWidth: import_pieces_framework4.Property.Number({
      displayName: "Video Width",
      required: !1,
      defaultValue: 1280
    }),
    dimensionHeight: import_pieces_framework4.Property.Number({
      displayName: "Video Height",
      required: !1,
      defaultValue: 720
    }),
    variables: templateVariables
  },
  async run({ propsValue, auth }) {
    var _a;
    let {
      templateId,
      title,
      caption,
      includeGif,
      enableSharing,
      folderId,
      brandVoiceId,
      callbackUrl,
      dimensionWidth,
      dimensionHeight
    } = propsValue, inputVariables = (_a = propsValue.variables) != null ? _a : {}, templateVariables2 = (await heygenApiCall({
      apiKey: auth,
      method: import_pieces_common4.HttpMethod.GET,
      resourceUri: `/template/${templateId}`,
      apiVersion: "v2"
    })).data.variables, formattedVariables = {};
    for (let [key, value] of Object.entries(inputVariables)) {
      if ((0, import_pieces_framework5.isNil)(value) || value === "") continue;
      let variable = templateVariables2[key];
      if (!variable) continue;
      let { type, name, properties } = variable, base = { name, type };
      switch (type) {
        case "text":
          formattedVariables[key] = __spreadProps(__spreadValues({}, base), {
            properties: { content: value }
          });
          break;
        case "image":
        case "video":
        case "audio":
          formattedVariables[key] = __spreadProps(__spreadValues({}, base), {
            properties: __spreadProps(__spreadValues({}, properties), { url: value })
          });
          break;
        case "character":
          formattedVariables[key] = __spreadProps(__spreadValues({}, base), {
            properties: __spreadProps(__spreadValues({}, properties), { character_id: value })
          });
          break;
        case "voice":
          formattedVariables[key] = __spreadProps(__spreadValues({}, base), {
            properties: __spreadProps(__spreadValues({}, properties), { voice_id: value })
          });
          break;
        default:
          break;
      }
    }
    let body = {
      template_id: templateId,
      title,
      caption: caption === !0,
      include_gif: includeGif === !0,
      enable_sharing: enableSharing === !0
    };
    return folderId && (body.folder_id = folderId), brandVoiceId && (body.brand_voice_id = brandVoiceId), callbackUrl && (body.callback_url = callbackUrl), dimensionWidth && dimensionHeight && (body.dimension = {
      width: dimensionWidth,
      height: dimensionHeight
    }), Object.keys(formattedVariables || {}).length && (body.variables = formattedVariables), await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: `/template/${templateId}/generate`,
      body,
      apiVersion: "v2"
    });
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/actions/retrieve-a-translated-video-status.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var retrieveTranslatedVideoStatus = (0, import_pieces_framework6.createAction)({
  auth: heygenAuth,
  name: "retrieve-translated-video-status",
  classification: "READ",
  displayName: "Retrieve Translated Video Status",
  description: "Retrieves the status of a translated video.",
  audience: "both",
  aiMetadata: {
    description: "Looks up the current processing status (and result details) of a video-translation job by its translation video ID. Use to poll whether a previously submitted Translate Video job has completed. Read-only and idempotent. Note the ID here is the translate-job ID, distinct from the avatar-video ID used by Retrieve Video Status.",
    idempotent: !0
  },
  props: {
    videoId: import_pieces_framework6.Property.ShortText({
      displayName: "Video ID",
      required: !0,
      description: "The ID of the translated video to check the status for."
    })
  },
  async run({ propsValue, auth }) {
    let { videoId } = propsValue;
    return await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common5.HttpMethod.GET,
      resourceUri: `/video_translate/${videoId}`,
      apiVersion: "v2"
    });
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/actions/retrieve-a-video-status.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var retrieveVideoStatusAction = (0, import_pieces_framework7.createAction)({
  auth: heygenAuth,
  name: "retrieve_video_status",
  classification: "READ",
  displayName: "Retrieve Video Status",
  description: "Retrieve the status and details of a video using its ID.",
  audience: "both",
  aiMetadata: {
    description: "Looks up the processing status and details (including the output URL once ready) of an avatar/template-generated video by its video ID. Use to poll whether a generation job has finished before downloading or sharing. Read-only and idempotent. For video-translation jobs use Retrieve Translated Video Status instead.",
    idempotent: !0
  },
  props: {
    videoId: import_pieces_framework7.Property.ShortText({
      displayName: "Video ID",
      description: "The ID of the video to retrieve the status for.",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let { videoId } = propsValue;
    return await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common6.HttpMethod.GET,
      resourceUri: "/video_status.get",
      query: { video_id: videoId },
      apiVersion: "v1"
    });
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/actions/retrieve-shareable-link-for-a-video.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var retrieveSharableVideoUrlAction = (0, import_pieces_framework8.createAction)({
  auth: heygenAuth,
  name: "retrieve_sharable_video_url",
  classification: "WRITE",
  displayName: "Retrieve Sharable Video URL",
  description: "Generates a public URL for a video, allowing it to be shared and accessed publicly.",
  audience: "both",
  aiMetadata: {
    description: "Enables public sharing for a finished video and returns a publicly accessible share URL, given the video ID. Use when an agent needs a link it can hand out without authentication. This is a server-side mutation that toggles the video to shareable, so treat it as not idempotent.",
    idempotent: !1
  },
  props: {
    videoId: import_pieces_framework8.Property.ShortText({
      displayName: "Video ID",
      description: "The ID of the video to generate a shareable URL for.",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let { videoId } = propsValue;
    return await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common7.HttpMethod.POST,
      resourceUri: "/video/share",
      body: { video_id: videoId },
      apiVersion: "v1"
    });
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/actions/upload-an-asset.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var uploadAssetAction = (0, import_pieces_framework9.createAction)({
  auth: heygenAuth,
  name: "upload_asset",
  classification: "WRITE",
  displayName: "Upload an Asset",
  description: "Upload media files (images, videos, or audio) to HeyGen. Supports JPEG, PNG, MP4, WEBM, and MPEG files.",
  audience: "both",
  aiMetadata: {
    description: "Uploads a media file (image, video, or audio) to HeyGen so it can be referenced in later video/template generation. Use to stage assets before composing a video. Only JPEG, PNG, MP4, WEBM, and MPEG are accepted; content type is inferred from the file extension. Each call creates a new asset, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    file: import_pieces_framework9.Property.File({
      displayName: "File",
      description: "The file to upload (JPEG, PNG, MP4, WEBM, or MPEG).",
      required: !0
    })
  },
  async run(context) {
    let { file } = context.propsValue, contentType = ((filename) => {
      let extension = filename.toLowerCase().split(".").pop();
      switch (extension) {
        case "jpg":
        case "jpeg":
          return "image/jpeg";
        case "png":
          return "image/png";
        case "mp4":
          return "video/mp4";
        case "webm":
          return "video/webm";
        case "mpeg":
        case "mpg":
          return "audio/mpeg";
        default:
          throw new Error(`Unsupported file type: ${extension}`);
      }
    })(file.filename);
    return (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.POST,
      url: "https://upload.heygen.com/v1/asset",
      headers: {
        "x-api-key": context.auth.secret_text,
        "Content-Type": contentType
      },
      body: file.data
    })).body;
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/actions/translate-a-video.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var translateVideoAction = (0, import_pieces_framework10.createAction)({
  auth: heygenAuth,
  name: "translate_video",
  classification: "WRITE",
  displayName: "Translate Video",
  description: "Translate a video into 175+ languages with natural voice and lip-sync.",
  audience: "both",
  aiMetadata: {
    description: "Submits a video-translation job that dubs a source video into a target language with natural voice and lip-sync, optionally audio-only (no face modification). Use to localize an existing video given its URL (direct, Google Drive, or YouTube) and an output language. Processing is asynchronous and each call starts a new job (not idempotent); poll Retrieve Translated Video Status for the result.",
    idempotent: !1
  },
  props: {
    videoUrl: import_pieces_framework10.Property.ShortText({
      displayName: "Video URL",
      required: !0,
      description: "URL of the video file to be translated. Supports direct URLs, Google Drive, and YouTube."
    }),
    title: import_pieces_framework10.Property.ShortText({
      displayName: "Title",
      required: !1,
      description: "Optional title of the translated video."
    }),
    outputLanguage: supportedLanguagesDropdown,
    translateAudioOnly: import_pieces_framework10.Property.Checkbox({
      displayName: "Translate Audio Only",
      required: !1,
      defaultValue: !1,
      description: "Only translate the audio without modifying faces."
    }),
    speakerNum: import_pieces_framework10.Property.Number({
      displayName: "Number of Speakers",
      required: !1,
      description: "Number of speakers in the video (if applicable)."
    }),
    brandVoiceId: brandVoiceDropdown,
    callbackId: import_pieces_framework10.Property.ShortText({
      displayName: "Callback ID",
      required: !1,
      description: "Custom ID returned in webhook callback."
    }),
    callbackUrl: import_pieces_framework10.Property.ShortText({
      displayName: "Callback URL",
      required: !1,
      description: "URL to notify when translation is complete."
    })
  },
  async run({ propsValue, auth }) {
    let {
      videoUrl,
      title,
      outputLanguage,
      translateAudioOnly,
      speakerNum,
      callbackId,
      brandVoiceId,
      callbackUrl
    } = propsValue, body = {
      video_url: videoUrl,
      output_language: outputLanguage
    };
    return title && (body.title = title), translateAudioOnly && (body.translate_audio_only = translateAudioOnly), speakerNum && (body.speaker_num = speakerNum), callbackId && (body.callback_id = callbackId), brandVoiceId && (body.brand_voice_id = brandVoiceId), callbackUrl && (body.callback_url = callbackUrl), await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common9.HttpMethod.POST,
      resourceUri: "/video_translate",
      body,
      apiVersion: "v2"
    });
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/actions/list-avatars.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var listAvatarsAction = (0, import_pieces_framework11.createAction)({
  auth: heygenAuth,
  name: "list_avatars",
  classification: "SEARCH",
  displayName: "List Avatars",
  description: "Retrieve a list of all available avatars.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves all avatars available to the account. Use to discover avatar IDs before composing or generating a video. Takes no input; read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run({ auth }) {
    return await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common10.HttpMethod.GET,
      resourceUri: "/avatars",
      apiVersion: "v2"
    });
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/actions/list-voices.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var listVoicesAction = (0, import_pieces_framework12.createAction)({
  auth: heygenAuth,
  name: "list_voices",
  classification: "SEARCH",
  displayName: "List Voices",
  description: "Retrieve a list of all available voices.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves all voices available to the account. Use to discover voice IDs before composing or generating a video. Takes no input; read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run({ auth }) {
    return await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common11.HttpMethod.GET,
      resourceUri: "/voices",
      apiVersion: "v2"
    });
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/actions/list-videos.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var listVideosAction = (0, import_pieces_framework13.createAction)({
  auth: heygenAuth,
  name: "list_videos",
  classification: "SEARCH",
  displayName: "List Videos",
  description: "Retrieve a list of all generated videos.",
  audience: "both",
  aiMetadata: {
    description: "Lists previously generated videos on the account, optionally capped by a limit (default 20, max 100). Use to enumerate videos and their IDs/statuses for follow-up lookups. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework13.Property.Number({
      displayName: "Limit",
      description: "Maximum number of videos to retrieve (max 100).",
      required: !1,
      defaultValue: 20
    })
  },
  async run({ propsValue, auth }) {
    var _a;
    return await heygenApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common12.HttpMethod.GET,
      resourceUri: "/video.list",
      query: { limit: (_a = propsValue.limit) != null ? _a : 20 },
      apiVersion: "v1"
    });
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/triggers/video-generation-completed.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var TRIGGER_KEY = "video_generation_completed_trigger", videoGenerationCompletedTrigger = (0, import_pieces_framework14.createTrigger)({
  auth: heygenAuth,
  name: "video_generation_completed",
  classification: "READ",
  displayName: "New Avatar Video Event (Success)",
  description: "Triggers when a video is generated successfully.",
  aiMetadata: {
    description: "Fires when an avatar video finishes generating successfully (HeyGen avatar_video.success webhook event), signaling the rendered output is ready with its video ID and download URLs."
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    event_type: "avatar_video.success",
    event_data: {
      video_id: "123",
      url: "https://www.example.com",
      gif_download_url: "<gif_url>",
      folder_id: "123",
      callback_id: "123"
    }
  },
  async onEnable(context) {
    let webhook = await heygenApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.POST,
      resourceUri: "/webhook/endpoint.add",
      apiVersion: "v1",
      body: {
        url: context.webhookUrl,
        events: ["avatar_video.success"]
      }
    });
    await context.store.put(TRIGGER_KEY, webhook.data.endpoint_id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(TRIGGER_KEY);
    webhookId && await heygenApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.DELETE,
      resourceUri: "/webhook/endpoint.delete",
      apiVersion: "v1",
      query: {
        endpoint_id: webhookId
      }
    });
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event_type !== "avatar_video.success" ? [] : [payload.event_data];
  }
});

// ../ap/packages/pieces/community/heygen/src/lib/triggers/video-generation-failed.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var TRIGGER_KEY2 = "video_generation_failed_trigger", videoGenerationFailedTrigger = (0, import_pieces_framework15.createTrigger)({
  auth: heygenAuth,
  name: "video_generation_failed",
  classification: "READ",
  displayName: "New Avatar Video Event (Fail)",
  description: "Triggers when a video generation process fails.",
  aiMetadata: {
    description: "Fires when an avatar video generation fails (HeyGen avatar_video.fail webhook event), providing the affected video ID and an error message so the failure can be handled."
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    event_type: "avatar_video.fail",
    event_data: {
      video_id: "abc",
      msg: "Failed",
      callback_id: "123"
    }
  },
  async onEnable(context) {
    let webhook = await heygenApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common14.HttpMethod.POST,
      resourceUri: "/webhook/endpoint.add",
      apiVersion: "v1",
      body: {
        url: context.webhookUrl,
        events: ["avatar_video.fail"]
      }
    });
    await context.store.put(TRIGGER_KEY2, webhook.data.endpoint_id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(TRIGGER_KEY2);
    webhookId && await heygenApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common14.HttpMethod.DELETE,
      resourceUri: "/webhook/endpoint.delete",
      apiVersion: "v1",
      query: {
        endpoint_id: webhookId
      }
    });
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event_type !== "avatar_video.fail" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/heygen/src/index.ts
var heygen = (0, import_pieces_framework16.createPiece)({
  displayName: "HeyGen",
  description: "Generate and manage AI avatar videos using HeyGen.",
  auth: heygenAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/heygen.jpg",
  authors: ["krushnarout", "Angelebeats"],
  categories: [import_pieces_framework17.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  actions: [
    createVideoFromTemplateAction,
    retrieveTranslatedVideoStatus,
    retrieveVideoStatusAction,
    retrieveSharableVideoUrlAction,
    uploadAssetAction,
    translateVideoAction,
    listAvatarsAction,
    listVoicesAction,
    listVideosAction,
    (0, import_pieces_common15.createCustomApiCallAction)({
      auth: heygenAuth,
      baseUrl: () => BASE_URL_V1,
      authMapping: async (auth) => ({
        "X-Api-Key": auth.secret_text
      })
    })
  ],
  triggers: [videoGenerationCompletedTrigger, videoGenerationFailedTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  heygen
});
