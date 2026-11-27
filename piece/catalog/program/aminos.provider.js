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

// ../ap/packages/pieces/community/aminos/src/index.ts
var index_exports = {};
__export(index_exports, {
  aminos: () => aminos,
  aminosAuth: () => aminosAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/aminos/src/lib/actions/createUser.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var createUser = (0, import_pieces_framework.createAction)({
  // auth: check https://www.activepieces.com/docs/developers/piece-reference/authentication,
  auth: aminosAuth,
  name: "createUser",
  classification: "WRITE",
  displayName: "Create User on Aminos One",
  description: "Create a user and plan in Aminos One Panel",
  audience: "both",
  aiMetadata: { description: "Provisions a new user account on an Aminos One panel by POSTing the user email, display name, and a numeric plan ID to the configured panel base URL. Use to onboard a customer or assign them to a specific pricing plan. Not idempotent: each call attempts a fresh creation and the panel returns an error if the email already exists, so it is not a safe retry.", idempotent: !1 },
  props: {
    useremail: import_pieces_framework.Property.ShortText({
      displayName: "Username (e-mail)",
      description: "Username, should be an e-mail address",
      required: !0
    }),
    userfriendlyname: import_pieces_framework.Property.ShortText({
      displayName: "Name of user",
      description: "The name of the user",
      required: !0
    }),
    userplanid: import_pieces_framework.Property.Number({
      displayName: "Plan ID",
      description: "Plan ID number from the plans in your Aminos One panel",
      required: !0
    })
  },
  async run(context) {
    let baseUrl = context.auth.props.base_url.replace(/\/$/, ""), headers = {
      "Content-Type": "application/json"
    }, createAminosRequestBody = {
      api_key: context.auth.props.access_token,
      name: context.propsValue.userfriendlyname,
      email: context.propsValue.useremail,
      price_plan_id: context.propsValue.userplanid
    }, createAminosResponse = await fetch(`${baseUrl}/api/users`, {
      method: "POST",
      headers,
      body: JSON.stringify(createAminosRequestBody)
    });
    if (!createAminosResponse.ok)
      throw new Error(`Failed to create user. Status: ${createAminosResponse.status}`);
    return await createAminosResponse.json();
  }
});

// ../ap/packages/pieces/community/aminos/src/index.ts
var aminosAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  description: "Enter Aminos One authentication details",
  props: {
    base_url: import_pieces_framework2.Property.ShortText({
      displayName: "Base URL",
      description: "Enter the base URL",
      required: !0
    }),
    access_token: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "API key",
      description: "Enter the API key from your Aminos One panel",
      required: !0
    })
  },
  // Optional Validation
  validate: async ({ auth }) => auth ? {
    valid: !0
  } : {
    valid: !1,
    error: "Invalid Api Key"
  },
  required: !0
}), aminos = (0, import_pieces_framework2.createPiece)({
  displayName: "Aminos",
  auth: aminosAuth,
  categories: [import_pieces_framework3.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/aminos.png",
  authors: ["buttonsbond"],
  actions: [createUser],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  aminos,
  aminosAuth
});
