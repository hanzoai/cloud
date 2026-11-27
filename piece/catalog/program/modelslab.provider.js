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

// ../ap/packages/pieces/community/modelslab/src/index.ts
var index_exports = {};
__export(index_exports, {
  modelsLab: () => modelsLab
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/modelslab/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), modelsLabAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Get your API key at https://modelslab.com/account/api-key",
  required: !0
});

// ../ap/packages/pieces/community/modelslab/src/lib/actions/text-to-image.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var FETCH_BASE = "https://modelslab.com/api/v6/images/fetch", MAX_POLLS = 12, POLL_MS = 5e3;
async function pollForResult(apiKey, requestId) {
  for (let i = 0; i < MAX_POLLS; i++) {
    await new Promise((r) => setTimeout(r, POLL_MS));
    let req = {
      method: import_pieces_common.HttpMethod.POST,
      url: `${FETCH_BASE}/${requestId}`,
      headers: { "Content-Type": "application/json" },
      body: { key: apiKey }
    }, { body } = await import_pieces_common.httpClient.sendRequest(req);
    if (body.status === "success" && body.output) return body.output;
    if (body.status === "error") throw new Error(body.message || "Generation failed");
  }
  throw new Error("ModelsLab image generation timed out.");
}
var textToImage = (0, import_pieces_framework2.createAction)({
  auth: modelsLabAuth,
  name: "text-to-image",
  displayName: "Text to Image",
  description: "Generate an image from a text prompt using ModelsLab.",
  audience: "both",
  aiMetadata: { description: "Generates one or more images from a natural-language text prompt via the ModelsLab text-to-image API; use it to synthesize new artwork rather than edit or fetch existing images. Optionally control the diffusion model, dimensions, sample count, and a seed. Not idempotent: each call submits a fresh generation request and returns newly created images (with the default seed of -1 results vary even for an identical prompt); it also polls until the asynchronous job completes.", idempotent: !1 },
  props: {
    prompt: import_pieces_framework2.Property.LongText({
      displayName: "Prompt",
      required: !0,
      description: "The text prompt to generate an image from."
    }),
    negative_prompt: import_pieces_framework2.Property.LongText({
      displayName: "Negative Prompt",
      required: !1,
      description: "Things to exclude from the image."
    }),
    model_id: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Model",
      required: !1,
      description: "The model to use for image generation.",
      options: {
        options: [
          { label: "Flux (default)", value: "flux" },
          { label: "Stable Diffusion XL", value: "sdxl" },
          { label: "Stable Diffusion 2.1", value: "stable-diffusion-2-1" },
          { label: "Realistic Vision", value: "realistic-vision-v5" },
          { label: "DreamShaper", value: "dream-shaper-8797" }
        ]
      }
    }),
    width: import_pieces_framework2.Property.Number({
      displayName: "Width",
      required: !1,
      description: "Image width in pixels (256\u20131024, divisible by 8).",
      defaultValue: 512
    }),
    height: import_pieces_framework2.Property.Number({
      displayName: "Height",
      required: !1,
      description: "Image height in pixels (256\u20131024, divisible by 8).",
      defaultValue: 512
    }),
    num_inference_steps: import_pieces_framework2.Property.Number({
      displayName: "Steps",
      required: !1,
      description: "Number of inference steps (20\u201350).",
      defaultValue: 30
    }),
    guidance_scale: import_pieces_framework2.Property.Number({
      displayName: "Guidance Scale",
      required: !1,
      description: "How closely the image follows the prompt (1\u201320).",
      defaultValue: 7.5
    }),
    samples: import_pieces_framework2.Property.Number({
      displayName: "Samples",
      required: !1,
      description: "Number of images to generate (1\u20134).",
      defaultValue: 1
    }),
    seed: import_pieces_framework2.Property.Number({
      displayName: "Seed",
      required: !1,
      description: "Random seed for reproducibility (-1 for random).",
      defaultValue: -1
    }),
    safety_checker: import_pieces_framework2.Property.Checkbox({
      displayName: "Safety Checker",
      required: !1,
      description: "Enable safety filter.",
      defaultValue: !0
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, {
      prompt,
      negative_prompt,
      model_id,
      width,
      height,
      num_inference_steps,
      guidance_scale,
      samples,
      seed,
      safety_checker
    } = context.propsValue, requestBody = {
      key: apiKey,
      prompt,
      model_id: model_id || "flux",
      width: width || 512,
      height: height || 512,
      num_inference_steps: num_inference_steps || 30,
      guidance_scale: guidance_scale || 7.5,
      samples: samples || 1,
      seed: seed != null ? seed : -1,
      safety_checker: safety_checker ? "yes" : "no"
    };
    negative_prompt && (requestBody.negative_prompt = negative_prompt);
    let req = {
      method: import_pieces_common.HttpMethod.POST,
      url: "https://modelslab.com/api/v6/images/text2img",
      headers: { "Content-Type": "application/json" },
      body: requestBody
    }, { body } = await import_pieces_common.httpClient.sendRequest(req);
    if (body.status === "error") throw new Error(body.message || "ModelsLab API error");
    return body.status === "processing" && body.id ? { images: await pollForResult(apiKey, body.id) } : { images: body.output || [] };
  }
});

// ../ap/packages/pieces/community/modelslab/src/index.ts
var modelsLab = (0, import_pieces_framework3.createPiece)({
  displayName: "ModelsLab",
  description: "ModelsLab is a developer-first AI API platform for text-to-image generation, video creation, voice cloning, and more.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/modelslab.png",
  categories: [import_pieces_framework4.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["adhikjoshi"],
  auth: modelsLabAuth,
  actions: [textToImage],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  modelsLab
});
