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

// ../ap/packages/pieces/core/manual-trigger/src/index.ts
var index_exports = {};
__export(index_exports, {
  manualTriggerPiece: () => manualTriggerPiece
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/core/manual-trigger/src/lib/triggers/manual-trigger.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), manualTrigger = (0, import_pieces_framework.createTrigger)({
  name: "manual_trigger",
  classification: "READ",
  displayName: "Manual Trigger",
  description: "Manually start your own flow without any extra configurations",
  aiMetadata: {
    description: "Fires when a published flow is started manually on demand, for example a user clicking Run Flow, rather than in response to an external event; the event itself carries no payload. Use it as the entry point for flows an operator or agent invokes directly, and prefer a webhook trigger for external app events, a schedule trigger for time-based runs, or the Web Form, Chat UI, or Callable Flow triggers when the invoker must supply input."
  },
  props: {
    markdown: import_pieces_framework.Property.MarkDown({
      value: "Manual triggers are used to start a flow on demand, publish your flow and click (Run Flow) at the start of the flow.",
      variant: import_pieces_framework2.MarkdownVariant.INFO
    })
  },
  sampleData: {},
  type: import_pieces_framework.TriggerStrategy.MANUAL,
  async test() {
    return [{}];
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run() {
    return [{}];
  }
});

// ../ap/packages/pieces/core/manual-trigger/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), manualTriggerPiece = (0, import_pieces_framework3.createPiece)({
  displayName: "Manual Trigger",
  auth: import_pieces_framework3.PieceAuth.None(),
  minimumSupportedRelease: "0.78.0",
  logoUrl: "https://cdn.activepieces.com/pieces/new-core/manual-trigger.svg",
  authors: ["AbdulTheActivePiecer"],
  actions: [],
  triggers: [manualTrigger],
  categories: [import_pieces_framework4.PieceCategory.CORE]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  manualTriggerPiece
});
