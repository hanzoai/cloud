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

// ../ap/packages/pieces/community/pushbullet/src/index.ts
var index_exports = {};
__export(index_exports, {
  pushbullet: () => pushbullet
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pushbullet/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), authHelpDescription = `
1. Login to your Pushbullet Dashboard.
2. Go to **https://www.pushbullet.com/#settings/account**.
3.  **Create Access Token** then copy to the clipboard and paste it.`, bushbulletAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: authHelpDescription,
  required: !0
});

// ../ap/packages/pieces/community/pushbullet/src/lib/actions/send-a-link.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pushbullet/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.pushbullet.com/v2";
async function makeRequest(access_token, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${access_token}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/pushbullet/src/lib/actions/send-a-link.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), sendALink = (0, import_pieces_framework2.createAction)({
  auth: bushbulletAuth,
  name: "sendALink",
  classification: "WRITE",
  displayName: "Send a Link",
  description: "Send a link notification",
  audience: "both",
  aiMetadata: { description: "Pushes a clickable link notification to the authenticated Pushbullet account (and its connected devices). Use this when the agent needs to deliver a URL the recipient should open, optionally with a title and body line. Requires a URL; sends a new push on every call, so it is not idempotent.", idempotent: !1 },
  props: {
    url: import_pieces_framework2.Property.ShortText({
      displayName: "URL to send",
      description: "",
      required: !0
    }),
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Title",
      description: "",
      required: !1
    }),
    body: import_pieces_framework2.Property.ShortText({
      displayName: "Body",
      description: "",
      required: !1
    })
  },
  async run(context) {
    let { url, title, body } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/pushes",
      {
        type: "link",
        url,
        title,
        body
      }
    );
  }
});

// ../ap/packages/pieces/community/pushbullet/src/lib/actions/send-a-note.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), sendANote = (0, import_pieces_framework3.createAction)({
  auth: bushbulletAuth,
  name: "sendANote",
  classification: "WRITE",
  displayName: "Send a Note",
  description: "Send me a text notification",
  audience: "both",
  aiMetadata: { description: "Pushes a plain-text note notification (title plus body) to the authenticated Pushbullet account and its connected devices. Use this for a simple text alert or message rather than a link or file. Both title and body are required; sends a new push on every call, so it is not idempotent.", idempotent: !1 },
  props: {
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Note Title",
      description: "",
      required: !0
    }),
    body: import_pieces_framework3.Property.ShortText({
      displayName: "Note Body",
      description: "",
      required: !0
    })
  },
  async run(context) {
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/pushes",
      {
        type: "note",
        title: context.propsValue.title,
        body: context.propsValue.body
      }
    );
  }
});

// ../ap/packages/pieces/community/pushbullet/src/index.ts
var import_pieces_common4 = require("@activepieces/pieces-common");
var import_pieces_framework5 = require("@activepieces/pieces-framework"), pushbullet = (0, import_pieces_framework4.createPiece)({
  displayName: "Pushbullet",
  description: "Cross-device notification service",
  categories: [import_pieces_framework5.PieceCategory.COMMUNICATION],
  auth: bushbulletAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/pushbullet.png",
  authors: ["sanket-a11y"],
  actions: [
    sendALink,
    sendANote,
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: bushbulletAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pushbullet
});
