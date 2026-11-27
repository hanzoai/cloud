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

// ../ap/packages/pieces/community/zoho-books/src/index.ts
var index_exports = {};
__export(index_exports, {
  zohoBooks: () => zohoBooks,
  zohoBooksAuth: () => zohoBooksAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), zohoBooksAuth = import_pieces_framework.PieceAuth.OAuth2({
  props: {
    location: import_pieces_framework.Property.StaticDropdown({
      displayName: "Location",
      description: "The location of your Zoho Books account",
      required: !0,
      options: {
        options: [
          {
            label: "zoho.eu (Europe)",
            value: "zoho.eu"
          },
          {
            label: "zoho.com (United States)",
            value: "zoho.com"
          },
          {
            label: "zoho.com.au (Australia)",
            value: "zoho.com.au"
          },
          {
            label: "zoho.jp (Japan)",
            value: "zoho.jp"
          },
          {
            label: "zoho.in (India)",
            value: "zoho.in"
          },
          {
            label: "zohocloud.ca (Canada)",
            value: "zohocloud.ca"
          }
        ]
      }
    })
  },
  description: "Authentication for Zoho Books",
  scope: ["ZohoBooks.fullaccess.all"],
  authUrl: "https://accounts.{location}/oauth/v2/auth",
  tokenUrl: "https://accounts.{location}/oauth/v2/token",
  required: !0
}), zohoBooks = (0, import_pieces_framework.createPiece)({
  displayName: "Zoho Books",
  description: "Comprehensive online accounting software for small businesses.",
  logoUrl: "https://cdn.activepieces.com/pieces/zoho-books.png",
  minimumSupportedRelease: "0.30.0",
  categories: [import_pieces_framework2.PieceCategory.ACCOUNTING],
  authors: ["ikus060"],
  auth: zohoBooksAuth,
  actions: [
    (0, import_pieces_common.createCustomApiCallAction)({
      baseUrl: (auth) => {
        let data = auth.data;
        return data && data.api_domain ? `${data.api_domain}/books/v3` : "";
      },
      auth: zohoBooksAuth,
      authMapping: async (auth) => ({
        Authorization: `Zoho-oauthtoken ${auth.access_token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  zohoBooks,
  zohoBooksAuth
});
