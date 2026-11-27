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

// ../ap/packages/pieces/community/signrequest/src/index.ts
var index_exports = {};
__export(index_exports, {
  signrequest: () => signrequest
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/signrequest/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), signrequestAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Signrequest API Key",
  required: !0
});

// ../ap/packages/pieces/community/signrequest/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/signrequest/src/lib/actions/send-signrequest.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/signrequest/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://signrequest.com/api/v1";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `${api_key}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/signrequest/src/lib/actions/send-signrequest.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var sendSignrequest = (0, import_pieces_framework2.createAction)({
  auth: signrequestAuth,
  name: "send_signrequest",
  displayName: "Send signrequest",
  description: "Create a SignRequest via the Signrequest API",
  audience: "both",
  aiMetadata: { description: "Creates and sends a document signature request via SignRequest, dispatching the document to the listed signers for e-signature. Use to kick off a signing workflow for a contract or form. Requires a title and a JSON array of signer objects (email/name/role); files to sign are an optional JSON array. Not idempotent \u2014 each call sends a new, separate signature request.", idempotent: !1 },
  props: {
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Title",
      required: !0
    }),
    subject: import_pieces_framework2.Property.ShortText({
      displayName: "Subject",
      required: !1
    }),
    message: import_pieces_framework2.Property.LongText({
      displayName: "Message",
      required: !1
    }),
    signers: import_pieces_framework2.Property.Json({
      displayName: "Signers (JSON array)",
      description: 'JSON array of signer objects. Example: [{"email":"john@example.com","name":"John Doe","role":"signer"}]',
      required: !0
    }),
    files: import_pieces_framework2.Property.Json({
      displayName: "Files (JSON array)",
      description: 'Optional JSON array describing files to attach. Use the shape expected by the Signrequest API (e.g. [{"url":"https://..."}])',
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let { title, subject, message, signers, files } = propsValue, body = {
      title
    };
    return subject && (body.subject = subject), message && (body.message = message), signers && (body.signers = signers), files && (body.files = files), await makeRequest(
      auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/signrequests/",
      body
    );
  }
});

// ../ap/packages/pieces/community/signrequest/src/index.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), signrequest = (0, import_pieces_framework3.createPiece)({
  displayName: "Signrequest",
  auth: signrequestAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/signrequest.png",
  categories: [import_pieces_framework4.PieceCategory.SALES_AND_CRM],
  authors: ["sanket-a11y"],
  actions: [
    sendSignrequest,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: signrequestAuth,
      baseUrl: () => "https://signrequest.com/api/v1",
      authMapping: async (auth) => ({
        Authorization: auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  signrequest
});
