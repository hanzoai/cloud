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

// ../ap/packages/pieces/community/vidlab7/src/index.ts
var index_exports = {};
__export(index_exports, {
  vidlab7: () => vidlab7
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vidlab7/src/lib/actions/create-video.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/vidlab7/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), vidlab7Auth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To generate your API key:

1. Log in to [studio.vidlab7.com](https://studio.vidlab7.com/)
2. Click on **API** in the bottom section of the left-hand menu
3. Click the **Generate new key** button at the top right
4. Give your key a descriptive name and click **Generate**
5. Copy and store the key securely (you'll only see it once)
  `,
  required: !0
});

// ../ap/packages/pieces/community/vidlab7/src/lib/actions/create-video.ts
var createVideo = (0, import_pieces_framework2.createAction)({
  auth: vidlab7Auth,
  name: "createVideo",
  displayName: "Create Video",
  description: "Generate a video using a studio avatar",
  audience: "both",
  aiMetadata: {
    description: "Submits a video-generation job to VidLab7 from a studio avatar, a script, and a voice (avatar ID, voice ID, and script text are required). By default returns immediately with the video ID and status; enable wait-for-completion to poll until the video finishes and return its URL (this blocks for up to 300 seconds). Each call starts a new generation job, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    avatarId: import_pieces_framework2.Property.ShortText({
      displayName: "Avatar ID",
      description: "The ID of the avatar to be used for the video generation",
      required: !0
    }),
    script: import_pieces_framework2.Property.LongText({
      displayName: "Script",
      description: "The script to be used for the video generation",
      required: !0
    }),
    voiceId: import_pieces_framework2.Property.ShortText({
      displayName: "Voice ID",
      description: "The ID of the voice to be used for the video generation",
      required: !0
    }),
    webhookUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Webhook URL",
      description: "The URL to which the webhook will be sent when video generation is complete",
      required: !0
    }),
    similarity_boost: import_pieces_framework2.Property.Number({
      displayName: "Similarity Boost",
      description: "Voice similarity boost setting (0-100)",
      required: !1,
      defaultValue: 50
    }),
    use_speaker_boost: import_pieces_framework2.Property.Checkbox({
      displayName: "Use Speaker Boost",
      description: "Enable speaker boost for better voice quality",
      required: !1,
      defaultValue: !0
    }),
    style: import_pieces_framework2.Property.Number({
      displayName: "Style",
      description: "Voice style setting",
      required: !1,
      defaultValue: 0
    }),
    stability: import_pieces_framework2.Property.Number({
      displayName: "Stability",
      description: "Voice stability setting (0-100)",
      required: !1,
      defaultValue: 50
    }),
    waitForCompletion: import_pieces_framework2.Property.Checkbox({
      displayName: "Wait for Completion",
      description: "Wait for the video generation to complete and return the video URL. If disabled, only returns the video ID.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      avatarId,
      script,
      voiceId,
      webhookUrl,
      similarity_boost,
      use_speaker_boost,
      style,
      stability,
      waitForCompletion
    } = context.propsValue, initialResult = (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api-prd.vidlab7.com/api/studio-avatar/generate-video",
      headers: {
        "Content-Type": "application/json",
        "x-api-key": context.auth.secret_text
      },
      body: {
        avatarId,
        script,
        voiceId,
        webhookUrl,
        voice_settings: {
          similarity_boost,
          use_speaker_boost,
          style,
          stability
        }
      }
    })).body;
    if (!waitForCompletion)
      return initialResult;
    let videoId = initialResult.id, interval = 5 * 1e3, maxWait = 300 * 1e3, startTime = Date.now();
    for (; Date.now() - startTime < maxWait; ) {
      await new Promise((resolve) => setTimeout(resolve, interval));
      let statusData = (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `https://api-prd.vidlab7.com/api/studio-avatar/generated-video/${videoId}`,
        headers: {
          "Content-Type": "application/json",
          "x-api-key": context.auth.secret_text
        }
      })).body;
      if (statusData.success && statusData.data.status === "COMPLETED")
        return statusData.data;
      if (statusData.data.status === "FAILED")
        throw new Error("Video generation failed");
    }
    throw new Error("Video generation timed out after 300 seconds");
  }
});

// ../ap/packages/pieces/community/vidlab7/src/index.ts
var vidlab7 = (0, import_pieces_framework3.createPiece)({
  displayName: "VidLab7",
  auth: vidlab7Auth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/vidlab7.png",
  categories: [import_pieces_framework4.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  description: "AI Avatars that pitch, show demos, qualify buyers, follow up, secure meetings and close deals \u2013 on autopilot.",
  authors: ["sanket-a11y"],
  actions: [createVideo],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  vidlab7
});
