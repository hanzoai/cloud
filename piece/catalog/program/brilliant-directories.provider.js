"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/brilliant-directories/src/index.ts
var index_exports = {};
__export(index_exports, {
  brilliantDirectories: () => brilliantDirectories,
  brilliantDirectoriesAuth: () => brilliantDirectoriesAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/brilliant-directories/src/lib/actions/create-new-user.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/brilliant-directories/src/lib/common/brilliant-directories-common.ts
function parseDirectoryURL(url) {
  let formattedUrl = url;
  return formattedUrl.startsWith("https://") || (formattedUrl = formattedUrl.startsWith("http://") ? formattedUrl.replace("http://", "https://") : "https://" + formattedUrl), formattedUrl.endsWith("/api") || (formattedUrl += formattedUrl.endsWith("/") ? "api" : "/api"), formattedUrl;
}

// ../ap/packages/pieces/community/brilliant-directories/src/lib/actions/create-new-user.ts
var createNewUser = (0, import_pieces_framework.createAction)({
  name: "create_new_user",
  classification: "WRITE",
  auth: brilliantDirectoriesAuth,
  displayName: "Create new User",
  description: "Creates a new user in your brilliant directories site",
  audience: "both",
  aiMetadata: { description: "Registers a new member account on a Brilliant Directories site via the v2 user/create API. Use to onboard a member when you have their email, password, and a subscription ID that maps to a membership plan; extra profile fields can be passed through the Meta object. Not idempotent \u2014 each call creates a new account, so calling twice with the same email may produce a duplicate or fail.", idempotent: !1 },
  props: {
    email: import_pieces_framework.Property.ShortText({
      displayName: "Email",
      description: "Email address for the users account",
      required: !0
    }),
    password: import_pieces_framework.Property.ShortText({
      displayName: "Password",
      description: "Password for the new user account",
      required: !0
    }),
    subscription: import_pieces_framework.Property.ShortText({
      displayName: "Subscription ID",
      description: "The subscription ID from your website",
      required: !0
    }),
    meta: import_pieces_framework.Property.Json({
      displayName: "Meta",
      description: "Additional fields for the new user account",
      required: !1
    })
  },
  async run(context) {
    let CREATE_NEW_USER_URL = parseDirectoryURL(context.auth.props.site_url) + "/v2/user/create", headers = {
      accept: "application/json",
      "X-Api-Key": context.auth.props.api_key,
      "Content-Type": "application/x-www-form-urlencoded"
    }, body = __spreadValues({
      email: context.propsValue.email,
      password: context.propsValue.password,
      subscription_id: context.propsValue.subscription
    }, context.propsValue.meta);
    return await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: CREATE_NEW_USER_URL,
      headers,
      body
    });
  }
});

// ../ap/packages/pieces/community/brilliant-directories/src/index.ts
var brilliantDirectoriesAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  required: !0,
  description: `
  Brilliant Directories Authentication.

  Please enter your API key which can be generated from here: https://ww2.managemydirectory.com/admin/apiSettings

  Then enter your brilliant directories website instance URL appended with /api like the example.
  `,
  props: {
    api_key: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "API Key",
      description: "The api key of the brilliant directories account account",
      required: !0
    }),
    site_url: import_pieces_framework2.Property.ShortText({
      displayName: "Instance Url",
      description: "The url of the brilliant directories instance.",
      required: !0,
      defaultValue: "https://yoursitehere.com/api"
    })
  }
}), brilliantDirectories = (0, import_pieces_framework2.createPiece)({
  displayName: "Brilliant Directories",
  description: "All-in-one membership software",
  auth: brilliantDirectoriesAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/brilliant-directories.png",
  categories: [],
  authors: ["ShayPunter", "dennisrongo", "kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    createNewUser,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: (auth) => {
        var _a;
        return ((_a = auth == null ? void 0 : auth.props) == null ? void 0 : _a.site_url) || "";
      },
      auth: brilliantDirectoriesAuth,
      authMapping: async (auth) => ({
        "X-Api-Key": `${auth.props.api_key}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  brilliantDirectories,
  brilliantDirectoriesAuth
});
