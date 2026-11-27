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

// ../ap/packages/pieces/community/pylon/src/index.ts
var index_exports = {};
__export(index_exports, {
  pylon: () => pylon
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), auth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0
}), pylon = (0, import_pieces_framework.createPiece)({
  displayName: "Pylon",
  auth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/pylon.png",
  authors: [],
  actions: [
    (0, import_pieces_common.createCustomApiCallAction)({
      auth,
      baseUrl: () => "https://api.usepylon.com",
      authMapping: async (auth2) => ({
        Authorization: `Bearer ${auth2.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pylon
});
