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

// ../ap/packages/pieces/community/photoroom/src/index.ts
var index_exports = {};
__export(index_exports, {
  photoroom: () => photoroom,
  photoroomAuth: () => photoroomAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/photoroom/src/lib/actions/remove-background.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), removeBackground = (0, import_pieces_framework.createAction)({
  name: "removeBackground",
  displayName: "Remove background",
  description: "Remove the background of the image given as input",
  audience: "both",
  aiMetadata: {
    description: "Removes the background from an input image using the Photoroom segmentation API and writes the cutout to a file. Choose this to isolate a subject or produce a transparent-background version of a photo. Requires the raw image as a file input plus a filename for the generated output. Not idempotent: each call performs a fresh server-side segmentation that produces a new file and consumes Photoroom account credits.",
    idempotent: !1
  },
  auth: photoroomAuth,
  props: {
    file: import_pieces_framework.Property.File({ displayName: "Image file", required: !0 }),
    filename: import_pieces_framework.Property.ShortText({
      displayName: "Generated filename",
      required: !0
    })
  },
  async run({ auth, propsValue, files }) {
    let form = new FormData();
    form.append("image_file", new Blob([propsValue.file.data]));
    let response = await import_pieces_common.httpClient.sendRequest({
      url: "https://sdk.photoroom.com/v1/segment",
      method: import_pieces_common.HttpMethod.POST,
      headers: {
        "x-api-key": auth.props.apiKey,
        "Content-Type": "multipart/form-data"
      },
      body: form
    }), imageUrl = await files.write({
      fileName: propsValue.filename,
      data: Buffer.from(response.body.result_b64, "base64")
    });
    return {
      fileName: propsValue.filename,
      url: imageUrl
    };
  }
});

// ../ap/packages/pieces/community/photoroom/src/index.ts
var photoroomAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  required: !0,
  props: {
    apiKey: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "API key",
      required: !0
    })
  }
}), photoroom = (0, import_pieces_framework2.createPiece)({
  displayName: "Photoroom",
  auth: photoroomAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/photoroom.png",
  authors: ["AdamSelene", "Charles-Go"],
  actions: [removeBackground],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  photoroom,
  photoroomAuth
});
