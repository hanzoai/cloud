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

// ../ap/packages/pieces/core/approval/src/index.ts
var index_exports = {};
__export(index_exports, {
  approval: () => approval
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/core/approval/src/lib/actions/create-approval-link.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/core/approval/src/lib/output-schemas.ts
var createApprovalLinkActionOutputSchema = {
  fields: [
    { key: "approvalLink", label: "Approval Link", format: "url" },
    { key: "disapprovalLink", label: "Disapproval Link", format: "url" }
  ]
}, waitForApprovalActionOutputSchema = {
  fields: [{ key: "approved", label: "Approved", format: "boolean" }]
};

// ../ap/packages/pieces/core/approval/src/lib/actions/create-approval-link.ts
var createApprovalLink = (0, import_pieces_framework.createAction)({
  audience: "both",
  auth: import_pieces_framework.PieceAuth.None(),
  name: "create_approval_links",
  classification: "READ",
  displayName: "Create Approval Links",
  description: "Create links only without pausing the flow, use wait for approval to pause",
  aiMetadata: { description: "Mints a resumable approval/disapproval link for the current flow run and returns it immediately without pausing execution, so a later step can email or post it. Pick this when the flow must continue after handing out the link; use Wait for Approval instead when the run should block until someone responds. Takes no inputs and is a legacy piece that remains supported because, unlike the approval actions in the Approvals tab, it hands out a link without pausing the run. Not idempotent, since each call creates a new waitpoint and the previously issued link is not returned.", idempotent: !1 },
  outputSchema: createApprovalLinkActionOutputSchema,
  props: {
    markdown: import_pieces_framework.Property.MarkDown({
      variant: import_pieces_framework2.MarkdownVariant.INFO,
      value: "This piece is legacy but still supported. Unlike the approval actions in the Approvals tab, it hands out a link without pausing the flow."
    })
  },
  errorHandlingOptions: {
    continueOnFailure: {
      hide: !0
    },
    retryOnFailure: {
      hide: !0
    }
  },
  async run(ctx) {
    let confirmationLink = `${(await ctx.run.createWaitpoint({
      type: "WEBHOOK"
    })).resumeUrl}/confirm`;
    return {
      approvalLink: confirmationLink,
      disapprovalLink: confirmationLink
    };
  }
});

// ../ap/packages/pieces/core/approval/src/lib/actions/wait-for-approval.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var waitForApprovalLink = (0, import_pieces_framework3.createAction)({
  audience: "both",
  auth: import_pieces_framework3.PieceAuth.None(),
  name: "wait_for_approval",
  classification: "READ",
  displayName: "Wait for Approval",
  description: "Pauses the flow and wait for the approval from the user",
  aiMetadata: { description: "Pauses the current flow run at a human approval gate and resumes only when the run's waitpoint resume URL is called with an approve or disapprove response, reporting the choice. Pick this when execution must block on a human decision; it returns no link itself, so use Create Approval Links when the flow should keep running and hand a link out. Takes no inputs and is a legacy piece: for a new flow prefer the approval actions in the Approvals tab, which deliver the request and wait in one step. Not idempotent, since each execution creates a new waitpoint.", idempotent: !1 },
  outputSchema: waitForApprovalActionOutputSchema,
  props: {
    markdown: import_pieces_framework3.Property.MarkDown({
      variant: import_pieces_framework4.MarkdownVariant.INFO,
      value: "This piece is legacy but still supported. For a new flow, prefer the approval actions in the Approvals tab, which deliver the request and wait for the answer in one step."
    })
  },
  errorHandlingOptions: {
    continueOnFailure: {
      hide: !0
    },
    retryOnFailure: {
      hide: !0
    }
  },
  async run(ctx) {
    if (ctx.executionType === import_pieces_framework4.ExecutionType.BEGIN) {
      let waitpoint = await ctx.run.createWaitpoint({
        type: "WEBHOOK"
      });
      return ctx.run.waitForWaitpoint(waitpoint.id), {
        approved: !0
      };
    } else
      return {
        approved: ctx.resumePayload.queryParams.action === "approve"
      };
  }
});

// ../ap/packages/pieces/core/approval/src/index.ts
var approval = (0, import_pieces_framework5.createPiece)({
  displayName: "Approval (Legacy)",
  description: "Build approval process in your workflows",
  auth: import_pieces_framework5.PieceAuth.None(),
  minimumSupportedRelease: "0.82.0",
  logoUrl: "https://cdn.activepieces.com/pieces/new-core/approvals.svg",
  authors: ["kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  categories: [import_pieces_framework6.PieceCategory.CORE, import_pieces_framework6.PieceCategory.FLOW_CONTROL],
  actions: [waitForApprovalLink, createApprovalLink],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  approval
});
