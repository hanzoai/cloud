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

// ../ap/packages/pieces/community/mattermost/src/index.ts
var index_exports = {};
__export(index_exports, {
  mattermost: () => mattermost,
  mattermostAuth: () => mattermostAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mattermost/src/lib/actions/send-message.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var sendMessage = (0, import_pieces_framework.createAction)({
  auth: mattermostAuth,
  name: "send_message",
  displayName: "Send Message",
  description: "Send a message to a Mattermost channel",
  audience: "both",
  aiMetadata: { description: "Post a new text message to a Mattermost channel, identified by its channel ID (not name). Use when an agent needs to notify or post into a specific channel. Not idempotent: each call creates a separate post, so repeating it sends duplicate messages.", idempotent: !1 },
  props: {
    channel_id: import_pieces_framework.Property.ShortText({
      displayName: "Channel ID",
      description: "The channel to send the message to, get that ID by clicking on info near start call butto",
      required: !0
    }),
    text: import_pieces_framework.Property.LongText({
      displayName: "Message Text",
      description: "The text of the message to send",
      required: !0
    })
  },
  async run(context) {
    let baseUrl = context.auth.props.workspace_url.replace(/\/$/, "");
    try {
      return await import_pieces_common.httpClient.sendRequest({
        url: `${baseUrl}/api/v4/posts`,
        method: import_pieces_common.HttpMethod.POST,
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: context.auth.props.token
        },
        body: {
          channel_id: context.propsValue.channel_id,
          message: context.propsValue.text
        }
      });
    } catch (e) {
      if (e instanceof import_pieces_common.HttpError) {
        let httpError = e;
        if (console.log(httpError), (httpError == null ? void 0 : httpError.response.status) === 403)
          throw new Error(
            "Please make sure you have the correct bot token and channel ID."
          );
      }
      throw e;
    }
  }
});

// ../ap/packages/pieces/community/mattermost/src/index.ts
var markdownDescription = `
**Workspace URL**: The url of mattermost instance (e.g \`https://activepieces.mattermost.com\`)

**Bot Token**: Obtain it from settings > integrations > bot accounts > add bot account
`, mattermostAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  description: markdownDescription,
  required: !0,
  props: {
    workspace_url: import_pieces_framework2.Property.ShortText({
      displayName: "Workspace URL",
      description: "The workspace URL of the Mattermost instance (e.g https://activepieces.mattermost.com)",
      required: !0
    }),
    token: import_pieces_framework2.Property.ShortText({
      displayName: "Bot Token",
      description: "The bot token to use to authenticate",
      required: !0
    })
  }
}), mattermost = (0, import_pieces_framework2.createPiece)({
  displayName: "Mattermost",
  description: "Open-source, self-hosted Slack alternative",
  logoUrl: "https://cdn.activepieces.com/pieces/mattermost.png",
  minimumSupportedRelease: "0.30.0",
  categories: [import_pieces_framework3.PieceCategory.COMMUNICATION],
  authors: ["kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  auth: mattermostAuth,
  actions: [
    sendMessage,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: (auth) => auth ? auth.props.workspace_url + "/api/v4" : "",
      auth: mattermostAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mattermost,
  mattermostAuth
});
