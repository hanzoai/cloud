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

// ../ap/packages/pieces/community/datocms/src/index.ts
var index_exports = {};
__export(index_exports, {
  DATO_BASE_URL: () => DATO_BASE_URL,
  datoAuth: () => datoAuth,
  datocms: () => datocms
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), DATO_BASE_URL = "https://site-api.datocms.com", datoAuth = import_pieces_framework.PieceAuth.CustomAuth({
  required: !0,
  props: {
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API key",
      required: !0
    }),
    environment: import_pieces_framework.Property.ShortText({
      displayName: "Environment",
      description: "Optional sandbox environment",
      required: !1
    })
  }
}), datocms = (0, import_pieces_framework.createPiece)({
  displayName: "Dato CMS",
  description: "Dato is a modern headless CMS",
  auth: datoAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/datocms.png",
  authors: ["AdamSelene"],
  actions: [
    (0, import_pieces_common.createCustomApiCallAction)({
      auth: datoAuth,
      baseUrl: () => DATO_BASE_URL,
      authMapping: async (auth) => {
        let { apiKey, environment } = auth.props, headers = {
          Accept: "application/json",
          "Content-Type": "application/vnd.api+json",
          "X-Api-Version": "3",
          Authorization: `Bearer ${apiKey}`
        };
        return environment && (headers["X-Environment"] = environment), headers;
      }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  DATO_BASE_URL,
  datoAuth,
  datocms
});
