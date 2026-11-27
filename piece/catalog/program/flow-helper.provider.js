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

// ../ap/packages/pieces/community/flow-helper/src/index.ts
var index_exports = {};
__export(index_exports, {
  flowHelper: () => flowHelper
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/flow-helper/src/lib/actions/get-run-id.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/flow-helper/src/lib/output-schemas.ts
var getRunIdActionOutputSchema = {
  fields: [
    { key: "id", label: "Run ID" },
    { key: "url", label: "Run URL", format: "url" }
  ]
}, stopFlowActionOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    { key: "message", label: "Message" }
  ]
};

// ../ap/packages/pieces/community/flow-helper/src/lib/actions/get-run-id.ts
var getRunId = (0, import_pieces_framework.createAction)({
  audience: "both",
  // auth: check https://www.activepieces.com/docs/developers/piece-reference/authentication,
  name: "getRunId",
  classification: "READ",
  displayName: "Get Run Info",
  description: "",
  aiMetadata: { description: "Returns the identifier of the flow run that is currently executing, along with a direct link to that run in the Activepieces UI. Use it when a later step needs to reference or report the run itself (e.g. embedding a run link in a Slack alert or a support ticket); it only ever describes the in-progress run and cannot look up a different one. Takes no inputs; read-only and idempotent.", idempotent: !0 },
  props: {},
  outputSchema: getRunIdActionOutputSchema,
  async run(context) {
    let publicUrlWithoutApi = context.server.publicUrl.replace("/api", "");
    return {
      id: context.run.id,
      url: `${publicUrlWithoutApi}projects/${context.project.id}/runs/${context.run.id}`
    };
  }
});

// ../ap/packages/pieces/community/flow-helper/src/lib/actions/fail-flow.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), failFlow = (0, import_pieces_framework2.createAction)({
  audience: "both",
  name: "failFlow",
  classification: "READ",
  displayName: "Fail Flow",
  description: "Fails the flow execution with a custom message.",
  aiMetadata: { description: "Aborts the current flow run by throwing the supplied message as an error, so the run is recorded as failed and no later steps execute. Use it to hard-fail on an unrecoverable condition; prefer Stop Flow when the early exit is a normal outcome that should still count as a success. Requires an error message, and the run continues past this step if continue-on-failure is enabled on it; idempotent, since it drives the run to the same failed end state and creates nothing.", idempotent: !0 },
  props: {
    message: import_pieces_framework2.Property.LongText({
      displayName: "Error Message",
      description: "The error message to show when the flow fails.",
      required: !0
    })
  },
  async run(context) {
    throw new Error(context.propsValue.message);
  }
});

// ../ap/packages/pieces/community/flow-helper/src/lib/actions/stop-flow.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var stopFlow = (0, import_pieces_framework3.createAction)({
  audience: "both",
  name: "stopFlow",
  classification: "READ",
  displayName: "Stop Flow",
  description: "Stops the flow immediately this step is reached.",
  aiMetadata: { description: "Ends the current flow run at this step and marks it as stopped rather than failed, so no later steps execute. Use it for an early exit when a condition means there is nothing left to do; prefer Fail Flow when the exit should be recorded as an error instead. Takes no inputs and stops the run without a custom webhook response; idempotent, since it drives the run to the same stopped end state and creates nothing.", idempotent: !0 },
  props: {},
  outputSchema: stopFlowActionOutputSchema,
  async run(context) {
    return context.run.stop(), {
      success: !0,
      message: "Flow stopped successfully."
    };
  }
});

// ../ap/packages/pieces/community/flow-helper/src/index.ts
var flowHelper = (0, import_pieces_framework4.createPiece)({
  displayName: "Flow Helper",
  auth: import_pieces_framework4.PieceAuth.None(),
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/flow-helper.svg",
  authors: ["AbdulTheActivePiecer", "AnkitSharmaOnGithub"],
  actions: [getRunId, failFlow, stopFlow],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  flowHelper
});
