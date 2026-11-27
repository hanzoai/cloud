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

// ../ap/packages/pieces/community/synthesia/src/index.ts
var index_exports = {};
__export(index_exports, {
  synthesia: () => synthesia
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/synthesia/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = `
To get your Synthesia API Key:

1. Go to the upper right corner of the Synthesia application and click on your account.
2. Select **Integrations**
3. Click on **Add** to add a new Synthesia API key.
4. Copy this key using the 3-dot button on the right side of your API key.
5. Paste it below.

**Note:** When you create an API key it will belong to your account, not the workspace.
`, synthesiaAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0
});

// ../ap/packages/pieces/community/synthesia/src/lib/actions/create-a-video-from-a-template.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/synthesia/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), templateIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: synthesiaAuth,
  displayName: "Template",
  description: "Select the template to use for video creation",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Connect your account"
      };
    try {
      let apiKey = auth == null ? void 0 : auth.secret_text;
      return {
        disabled: !1,
        options: (await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: "https://api.synthesia.io/v2/templates",
          headers: {
            Authorization: `Bearer ${apiKey}`
          }
        })).body.templates.map((template) => ({
          label: template.title,
          value: template.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Unable to fetch templates"
      };
    }
  }
});

// ../ap/packages/pieces/community/synthesia/src/lib/actions/create-a-video-from-a-template.ts
var createAVideoFromATemplate = (0, import_pieces_framework3.createAction)({
  auth: synthesiaAuth,
  name: "createAVideoFromATemplate",
  displayName: "Create a video from a template",
  description: "Create a video based on a template created in Synthesia",
  audience: "both",
  aiMetadata: {
    description: "Submits a video-generation job to Synthesia from an existing template, passing template-variable values as case-sensitive key-value pairs. Choose this when the user wants a video built from a pre-designed template rather than authored scene-by-scene; requires a valid template ID. Each call queues a new video, so it is not idempotent. Enable test mode to produce a watermarked preview.",
    idempotent: !1
  },
  props: {
    templateId: templateIdDropdown,
    templateData: import_pieces_framework3.Property.Object({
      displayName: "Template Data",
      description: "Key-value pairs for template variables. Keys must match variable names in the template (case-sensitive)",
      required: !1
    }),
    test: import_pieces_framework3.Property.Checkbox({
      displayName: "Test Mode",
      description: "If enabled, creates a test video with a watermark",
      required: !1,
      defaultValue: !1
    }),
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Title",
      description: "Custom title for the video",
      required: !1
    }),
    visibility: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Visibility",
      description: "The visibility setting for the video",
      required: !1,
      options: {
        options: [
          { label: "Private", value: "private" },
          { label: "Public", value: "public" }
        ]
      },
      defaultValue: "private"
    }),
    description: import_pieces_framework3.Property.LongText({
      displayName: "Description",
      description: "Custom description for the video",
      required: !1
    }),
    brandKitId: import_pieces_framework3.Property.ShortText({
      displayName: "Brand Kit ID",
      description: 'The ID of the brand kit to use (use "workspace_default" for default)',
      required: !1,
      defaultValue: "workspace_default"
    })
  },
  async run(context) {
    let {
      templateId,
      templateData,
      test,
      visibility,
      title,
      description,
      brandKitId
    } = context.propsValue, body = {
      templateId,
      test: test ? "true" : "false",
      visibility: visibility || "private"
    };
    return templateData && (body.templateData = templateData), title && (body.title = title), description && (body.description = description), brandKitId && (body.brandKitId = brandKitId), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.synthesia.io/v2/videos/fromTemplate",
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/synthesia/src/lib/actions/create-video.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createVideo = (0, import_pieces_framework4.createAction)({
  auth: synthesiaAuth,
  name: "createVideo",
  displayName: "Create video",
  description: "Create a video within your Synthesia account with one or more scenes",
  audience: "both",
  aiMetadata: {
    description: "Submits a video-generation job to Synthesia built from a text script, a chosen avatar, and a background (stock preset, custom asset ID, or URL). Choose this to generate a video from scratch from raw script text rather than from a saved template; script text and avatar are required. Each call queues a new video, so it is not idempotent. Enable test mode for a watermarked preview, or set a webhook URL to be notified when the video completes.",
    idempotent: !1
  },
  props: {
    input_scriptText: import_pieces_framework4.Property.LongText({
      displayName: "Input Script Text",
      description: "An array of scenes to be included in the video",
      required: !0
    }),
    input_scriptAudioUrl: import_pieces_framework4.Property.ShortText({
      displayName: "Input Script Audio URL",
      description: "URL of the audio file to be used for the video script (optional)",
      required: !1
    }),
    input_avatar: import_pieces_framework4.Property.ShortText({
      displayName: "Input Avatar",
      description: "The avatar to be used in the video https://docs.synthesia.io/reference/avatars) ",
      required: !0,
      defaultValue: "anna_costume1_cameraA"
    }),
    input_background: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Background",
      description: "Background for the video. Can be a stock background, custom asset ID, or URL",
      required: !0,
      defaultValue: "green_screen",
      options: {
        options: [
          { label: "Green Screen (Transparent)", value: "green_screen" },
          { label: "Off White (Solid)", value: "off_white" },
          { label: "Warm White (Solid)", value: "warm_white" },
          { label: "Light Pink (Solid)", value: "light_pink" },
          { label: "Soft Pink (Solid)", value: "soft_pink" },
          { label: "Light Blue (Solid)", value: "light_blue" },
          { label: "Dark Blue (Solid)", value: "dark_blue" },
          { label: "Soft Cyan (Solid)", value: "soft_cyan" },
          { label: "Strong Cyan (Solid)", value: "strong_cyan" },
          { label: "Light Orange (Solid)", value: "light_orange" },
          { label: "Soft Orange (Solid)", value: "soft_orange" },
          { label: "White Studio (Image)", value: "white_studio" },
          { label: "White Cafe (Image)", value: "white_cafe" },
          { label: "Luxury Lobby (Image)", value: "luxury_lobby" },
          { label: "Large Window (Image)", value: "large_window" },
          {
            label: "White Meeting Room (Image)",
            value: "white_meeting_room"
          },
          { label: "Open Office (Image)", value: "open_office" }
        ]
      }
    }),
    test: import_pieces_framework4.Property.Checkbox({
      displayName: "Test Mode",
      description: "If enabled, creates a test video with a watermark",
      required: !1,
      defaultValue: !1
    }),
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Title",
      description: "Custom title for the video (displayed on the share page)",
      required: !1
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "Custom description for the video (displayed on the share page)",
      required: !1
    }),
    visibility: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Visibility",
      description: "Video visibility setting",
      required: !1,
      options: {
        options: [
          { label: "Private", value: "private" },
          { label: "Public", value: "public" }
        ]
      },
      defaultValue: "private"
    }),
    brandKitId: import_pieces_framework4.Property.ShortText({
      displayName: "Brand Kit ID",
      description: 'The ID of the brand kit to use (use "workspace_default" for default)',
      required: !1,
      defaultValue: "workspace_default"
    }),
    callbackId: import_pieces_framework4.Property.ShortText({
      displayName: "Callback ID",
      description: "Arbitrary metadata/identifier for the video",
      required: !1
    }),
    pushServiceUrl: import_pieces_framework4.Property.ShortText({
      displayName: "Webhook URL",
      description: "Webhook URL for push notifications when video completes",
      required: !1
    })
  },
  async run(context) {
    let {
      input_avatar,
      input_background,
      input_scriptText,
      input_scriptAudioUrl,
      test,
      title,
      description,
      visibility,
      brandKitId,
      callbackId,
      pushServiceUrl
    } = context.propsValue, body = {
      input: {
        script: {
          type: "text",
          text: input_scriptText
        },
        avatar: input_avatar,
        background: input_background,
        audio: input_scriptAudioUrl
      },
      test: test ? "true" : "false",
      visibility: visibility || "private"
    };
    return title && (body.title = title), description && (body.description = description), brandKitId && (body.brandKitId = brandKitId), callbackId && (body.callbackId = callbackId), pushServiceUrl && (body.pushServiceUrl = pushServiceUrl), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://api.synthesia.io/v2/videos",
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/synthesia/src/lib/triggers/new-video-ready.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), newVideoReady = (0, import_pieces_framework5.createTrigger)({
  auth: synthesiaAuth,
  name: "newVideoReady",
  displayName: "New Video Ready",
  description: "Trigger when a new video is completed in Synthesia",
  aiMetadata: {
    description: "Fires when a Synthesia video finishes rendering and becomes ready (the video.completed event), delivered via a registered webhook. Represents a single completed video; use it to act on a video once generation has finished."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhookUrl = context.webhookUrl, response = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: "https://api.synthesia.io/v2/webhooks",
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: {
        url: webhookUrl,
        events: ["video.completed"]
      }
    });
    await context.store.put("webhookData", {
      webhookId: response.body.id
    });
  },
  async onDisable(context) {
    let webhookData = await context.store.get("webhookData");
    webhookData && await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.DELETE,
      url: `https://api.synthesia.io/v2/webhooks/${webhookData.webhookId}`,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      }
    }), await context.store.delete("webhookData");
  },
  async run(context) {
    return context.payload.type !== "video.completed" ? [] : [context.payload.body];
  }
});

// ../ap/packages/pieces/community/synthesia/src/index.ts
var synthesia = (0, import_pieces_framework6.createPiece)({
  displayName: "Synthesia",
  auth: synthesiaAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/synthesia.png",
  categories: [import_pieces_framework7.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  description: "Create AI videos from text in minutes using Synthesia",
  authors: ["sanket-a11y"],
  actions: [createAVideoFromATemplate, createVideo],
  triggers: [newVideoReady]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  synthesia
});
