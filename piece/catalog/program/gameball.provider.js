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

// ../ap/packages/pieces/community/gameball/src/index.ts
var index_exports = {};
__export(index_exports, {
  gameball: () => gameball,
  gameballAuth: () => gameballAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/gameball/src/lib/actions/send-event.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var sendEvent = (0, import_pieces_framework.createAction)({
  // auth: check https://www.activepieces.com/docs/developers/piece-reference/authentication,
  name: "sendEvent",
  auth: gameballAuth,
  displayName: "Send event",
  description: "Send an event to gameball",
  audience: "both",
  aiMetadata: { description: "Records a custom behavioral event (by event name) for a specific player in Gameball, identified by their player unique ID, so it can drive loyalty/gamification rules. Use when an agent needs to log that a player performed an action. Not idempotent: each call posts the event again, so repeats register as additional occurrences.", idempotent: !1 },
  props: {
    playerUniqueId: import_pieces_framework.Property.ShortText({
      displayName: "Your Player Unique Id",
      required: !0
    }),
    eventName: import_pieces_framework.Property.ShortText({
      displayName: "Event Name",
      required: !0
    })
  },
  async run(context) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.gameball.co/api/v3.0/integrations/event",
      headers: {
        APIKey: context.auth.secret_text
        // Pass API key in headers
      },
      // update the event body with eventmetadata if requested in the future.
      body: {
        playerUniqueId: context.propsValue.playerUniqueId,
        events: {
          [context.propsValue.eventName]: {}
        }
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/gameball/src/index.ts
var gameballAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Please use your gameball api key. visit [help center](https://help.gameball.co/en/articles/3467114-get-your-account-integration-details-api-key-and-transaction-key) for more information"
}), gameball = (0, import_pieces_framework2.createPiece)({
  displayName: "Gameball",
  auth: gameballAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/gameball.png",
  authors: ["Raamyy"],
  actions: [sendEvent],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  gameball,
  gameballAuth
});
