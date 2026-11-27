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

// ../ap/packages/pieces/community/twin-labs/src/index.ts
var index_exports = {};
__export(index_exports, {
  twinLabs: () => twinLabs,
  twinLabsAuth: () => twinLabsAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/twin-labs/src/lib/actions/start-browsing-task.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var API_BASE_URL = "https://paris.prod.api.twin.so", startBrowsingTask = (0, import_pieces_framework.createAction)({
  name: "startBrowsingTask",
  auth: twinLabsAuth,
  displayName: "Browse",
  description: "Browse the internet with an AI web navigation agent that can find information for you",
  audience: "both",
  aiMetadata: { description: "Launches an autonomous AI web-navigation agent that starts at a given URL and pursues a free-text goal (e.g. find a price, extract a fact, locate a page), then blocks polling until the task completes or a 15-minute timeout elapses, returning the agent's text output. Choose it when a task requires live, multi-step browsing of a website rather than a single fixed API call or scrape. Requires a starting URL and a goal; each call spins up a fresh browsing run, so it is not idempotent.", idempotent: !1 },
  props: {
    startUrl: import_pieces_framework.Property.ShortText({
      displayName: "startUrl",
      required: !0,
      description: "The URL where the browsing task should begin",
      defaultValue: ""
    }),
    goal: import_pieces_framework.Property.ShortText({
      displayName: "Goal",
      required: !0,
      description: "The goal or objective of the browsing task"
    })
  },
  async run(context) {
    let pollingUrl = (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${API_BASE_URL}/browse`,
      headers: {
        "x-api-key": context.auth.secret_text,
        "Content-Type": "application/json"
      },
      body: {
        goal: context.propsValue.goal,
        startUrl: context.propsValue.startUrl,
        outputType: "string",
        completionCallbackUrl: "https://"
      }
    })).body.url, statusResponse = {
      completed: !1,
      pending: !0
    }, timeoutAt = Date.now() + 900 * 1e3;
    for (; !statusResponse.completed && Date.now() < timeoutAt; )
      await new Promise((resolve) => setTimeout(resolve, 5e3)), statusResponse = (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: pollingUrl,
        headers: {
          "x-api-key": context.auth.secret_text,
          "Content-Type": "application/json"
        }
      })).body;
    return statusResponse;
  }
});

// ../ap/packages/pieces/community/twin-labs/src/index.ts
var twinLabsAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Please use ***your-twin-labs-api-key*** as value for API Key"
}), twinLabs = (0, import_pieces_framework2.createPiece)({
  displayName: "Twin Web Agent",
  auth: twinLabsAuth,
  minimumSupportedRelease: "0.20.0",
  logoUrl: "https://cdn.activepieces.com/pieces/twin-labs.png",
  authors: [],
  actions: [startBrowsingTask],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  twinLabs,
  twinLabsAuth
});
