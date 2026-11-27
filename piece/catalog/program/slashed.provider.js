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

// ../ap/packages/pieces/community/slashed/src/index.ts
var index_exports = {};
__export(index_exports, {
  slashed: () => slashed,
  slashedAuth: () => slashedAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/slashed/src/lib/actions/encode-video.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var encodeVideoAction = (0, import_pieces_framework.createAction)({
  auth: slashedAuth,
  name: "encode_video",
  displayName: "Encode a Video",
  description: "Requests a video encoding job on slashed.cloud.",
  audience: "both",
  aiMetadata: { description: "Submits a new AV1 video encoding/transcoding job to slashed.cloud for a given service ID, pointing at a source file (path or URL in the service's configured storage) and a `jobs` array of operations to apply. Use to kick off transcoding; optionally set a callback URL to be POSTed when encoding completes. Not idempotent \u2014 each call queues a separate encoding job.", idempotent: !1 },
  props: {
    service_id: import_pieces_framework.Property.Number({
      displayName: "Service ID",
      description: "Your service ID from the slashed.cloud dashboard. Found under your service settings.",
      required: !0
    }),
    source: import_pieces_framework.Property.ShortText({
      displayName: "Source File",
      description: "Path or URL of the source video file in the storage you have configured for your service.",
      required: !0
    }),
    jobs: import_pieces_framework.Property.Json({
      displayName: "Jobs",
      description: 'Array of encoding job objects to apply to the source file. Each object must include a `type` field specifying the job type (e.g. `{ "type": "encode", "output": "output.mp4" }`).',
      required: !0
    }),
    priority: import_pieces_framework.Property.StaticDropdown({
      displayName: "Priority",
      description: "Processing priority for this encoding request.",
      required: !1,
      options: {
        options: [
          { label: "Low", value: "low" },
          { label: "High", value: "high" }
        ]
      }
    }),
    callback: import_pieces_framework.Property.ShortText({
      displayName: "Callback URL",
      description: "Optional URL to receive a POST request when encoding completes. Slashed will send the result to this endpoint.",
      required: !1
    })
  },
  async run(context) {
    let { service_id, source, jobs, priority, callback } = context.propsValue, body = {
      service_id,
      source,
      jobs
    };
    return priority && (body.priority = priority), callback && (body.callback = callback), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://venc.slashed.cloud/request",
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/slashed/src/index.ts
var slashedAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Token",
  description: "Your Bearer Token from the slashed.cloud dashboard. Go to your service settings to find or generate it.",
  required: !0,
  validate: async ({ auth }) => auth && auth.trim().length > 0 ? { valid: !0 } : { valid: !1, error: "API Token must not be empty." }
}), slashed = (0, import_pieces_framework2.createPiece)({
  displayName: "SlashedCloud",
  description: "AV1 video encoding and transcoding via slashed.cloud.",
  auth: slashedAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/slashed.png",
  categories: [import_pieces_framework3.PieceCategory.DEVELOPER_TOOLS],
  authors: ["sanket-a11y"],
  actions: [
    encodeVideoAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://venc.slashed.cloud",
      auth: slashedAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  slashed,
  slashedAuth
});
