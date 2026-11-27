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

// ../ap/packages/pieces/community/stability-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  stabilityAi: () => stabilityAi,
  stabilityAiAuth: () => stabilityAiAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/stability-ai/src/lib/actions/text-to-image.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var textToImage = (0, import_pieces_framework.createAction)({
  auth: stabilityAiAuth,
  name: "text-to-image",
  displayName: "Text to Image",
  description: "Generate an image using a text prompt",
  audience: "both",
  aiMetadata: {
    description: "Generates one or more images from a text prompt using a Stable Diffusion engine (selectable via the required Engine ID, e.g. SDXL or older v1/v2 models). Use to create original imagery from a description; optionally steer output with a style preset, dimensions, sampling steps, and prompt-adherence (cfg_scale). Not idempotent: each call invokes the generative model and returns fresh images, so repeating the same input yields different results.",
    idempotent: !1
  },
  props: {
    prompt: import_pieces_framework.Property.ShortText({
      displayName: "Prompt",
      required: !0,
      description: "The text to transform in image."
    }),
    cfg_scale: import_pieces_framework.Property.Number({
      displayName: "cfg_scale",
      description: "How strictly the diffusion process adheres to the prompt text (higher values keep your image closer to your prompt) (MIN:0; MAX:35)",
      required: !1,
      defaultValue: 7
    }),
    height: import_pieces_framework.Property.Number({
      displayName: "height",
      description: "Height of the image in pixels. Must be in increments of 64 and >= 128",
      required: !1
    }),
    width: import_pieces_framework.Property.Number({
      displayName: "width",
      description: "Width of the image in pixels. Must be in increments of 64 and >= 128",
      required: !1
    }),
    samples: import_pieces_framework.Property.ShortText({
      displayName: "samples",
      description: "Number of images to generate (MAX:10)",
      required: !1
    }),
    steps: import_pieces_framework.Property.Number({
      displayName: "steps",
      description: "Number of diffusion steps to run (MIN:10; MAX:150)",
      required: !1
    }),
    weight: import_pieces_framework.Property.Number({
      displayName: "weight",
      defaultValue: 1,
      required: !1
    }),
    clip_guidance_preset: import_pieces_framework.Property.StaticDropdown({
      displayName: "clip_guidance_preset",
      required: !1,
      options: {
        options: [
          {
            label: "NONE",
            value: "NONE"
          },
          {
            label: "FAST_BLUE",
            value: "FAST_BLUE"
          },
          {
            label: "FAST_GREEN",
            value: "FAST_GREEN"
          },
          {
            label: "SIMPLE",
            value: "SIMPLE"
          },
          {
            label: "SLOW",
            value: "SLOW"
          },
          {
            label: "SLOWER",
            value: "SLOWER"
          },
          {
            label: "SLOWEST",
            value: "SLOWEST"
          }
        ]
      }
    }),
    style_preset: import_pieces_framework.Property.StaticDropdown({
      displayName: "style_preset",
      required: !1,
      description: "Pass in a style preset to guide the image model towards a particular style.",
      options: {
        options: [
          {
            label: "enhance",
            value: "enhance"
          },
          {
            label: "anime",
            value: "anime"
          },
          {
            label: "photographic",
            value: "photographic"
          },
          {
            label: "digital-art",
            value: "digital-art"
          },
          {
            label: "comic-book",
            value: "comic-book"
          },
          {
            label: "fantasy-art",
            value: "fantasy-art"
          },
          {
            label: "line-art",
            value: "line-art"
          },
          {
            label: "analog-film",
            value: "analog-film"
          },
          {
            label: "neon-punk",
            value: "neon-punk"
          },
          {
            label: "isometric",
            value: "isometric"
          },
          {
            label: "low-poly",
            value: "low-poly"
          },
          {
            label: "origami",
            value: "origami"
          },
          {
            label: "modeling-compound",
            value: "modeling-compound"
          },
          {
            label: "cinematic",
            value: "cinematic"
          },
          {
            label: "3d-model",
            value: "3d-model"
          },
          {
            label: "pixel-art",
            value: "pixel-art"
          },
          {
            label: "tile-texture",
            value: "tile-texture"
          }
        ]
      }
    }),
    engine_id: import_pieces_framework.Property.StaticDropdown({
      displayName: "Engine ID",
      required: !0,
      options: {
        options: [
          {
            label: "stable-diffusion-xl-1024-v1-0",
            value: "stable-diffusion-xl-1024-v1-0"
          },
          {
            label: "stable-diffusion-768-v2-1",
            value: "stable-diffusion-768-v2-1"
          },
          {
            label: "stable-diffusion-512-v2-1",
            value: "stable-diffusion-512-v2-1"
          },
          {
            label: "stable-diffusion-768-v2-0",
            value: "stable-diffusion-768-v2-0"
          },
          {
            label: "stable-diffusion-512-v2-0",
            value: "stable-diffusion-512-v2-0"
          },
          {
            label: "stable-diffusion-v1-5",
            value: "stable-diffusion-v1-5"
          },
          {
            label: "stable-diffusion-v1",
            value: "stable-diffusion-v1"
          }
        ]
      }
    })
  },
  async run(context) {
    let {
      prompt,
      cfg_scale,
      clip_guidance_preset,
      height,
      width,
      samples,
      steps,
      style_preset,
      engine_id,
      weight
    } = context.propsValue, engineId = engine_id || "stable-diffusion-v1-5", apiHost = "https://api.stability.ai", apiKey = context.auth.props.api_key, requestBody = {
      text_prompts: [
        {
          text: prompt,
          weight: Number(weight) || 1
        }
      ],
      cfg_scale: Number(cfg_scale) || 7,
      clip_guidance_preset: clip_guidance_preset || "NONE",
      height: Number(height) || getDefaultSize(engine_id),
      width: Number(width) || getDefaultSize(engine_id),
      samples: Number(samples) || 1,
      steps: Number(steps) || 50,
      style_preset
    }, request = {
      method: import_pieces_common.HttpMethod.POST,
      url: `${apiHost}/v1/generation/${engineId}/text-to-image`,
      headers: {
        Authorization: `Bearer ${apiKey}`,
        Accept: "application/json"
      },
      body: requestBody
    }, { body } = await import_pieces_common.httpClient.sendRequest(request);
    return Promise.all(
      body.artifacts.map(
        (artifact) => context.files.write({
          fileName: `image-${Date.now()}.png`,
          data: Buffer.from(artifact.base64, "base64")
        }).then((file) => ({ image: file }))
      )
    );
  }
});
function getDefaultSize(engineId) {
  switch (engineId) {
    case "stable-diffusion-xl-1024-v1-0":
      return 1024;
    case "stable-diffusion-768-v2-1":
      return 768;
    case "stable-diffusion-512-v2-1":
      return 512;
    case "stable-diffusion-768-v2-0":
      return 768;
    case "stable-diffusion-512-v2-0":
      return 512;
    case "stable-diffusion-v1-5":
      return 512;
    case "stable-diffusion-v1":
      return 512;
    default:
      return 512;
  }
}

// ../ap/packages/pieces/community/stability-ai/src/index.ts
var stabilityAiAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  description: "Please visit https://platform.stability.ai/docs/getting-started/authentication to get your API Key",
  props: {
    api_key: import_pieces_framework2.Property.ShortText({
      displayName: "API Key",
      required: !0
    })
  },
  required: !0
}), stabilityAi = (0, import_pieces_framework2.createPiece)({
  displayName: "Stability AI",
  description: "Generative AI video model based on the image model Stable Diffusion.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/stability-ai.png",
  categories: [import_pieces_framework3.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["Willianwg", "camilou", "kishanprmr", "MoShizzle", "AbdulTheActivePiecer", "khaledmashaly", "abuaboud"],
  auth: stabilityAiAuth,
  actions: [
    textToImage,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://api.stability.ai/v1",
      auth: stabilityAiAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.api_key}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  stabilityAi,
  stabilityAiAuth
});
