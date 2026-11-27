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

// ../ap/packages/pieces/core/tags/src/index.ts
var index_exports = {};
__export(index_exports, {
  tags: () => tags
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/core/tags/src/lib/add-tag.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdown = `
This action add a tag to the current execution, this tag can be used to filter the execution in the **API** only at this moment.
<br>
<br>
**Note:** If you are looking to use it in the user interface, please open a feature request.
`, addTag = (0, import_pieces_framework.createAction)({
  audience: "both",
  name: "add_tag",
  displayName: "Add Tag",
  description: "Add a tag to the current execution",
  aiMetadata: { description: 'Attaches a label to the flow run that is currently executing, so that run can later be located or grouped by that tag when listing runs through the Activepieces API. Pick this to mark a run with a business identifier or a branch it took (e.g. a customer id, "retry", "escalated"); it affects only the current run and the tag is surfaced through the API rather than the run UI. Requires a tag name; idempotent, since the run holds a de-duplicated set of tags, so adding the same name again leaves the same end state.', idempotent: !0 },
  errorHandlingOptions: {
    continueOnFailure: {
      hide: !0
    },
    retryOnFailure: {
      hide: !0
    }
  },
  props: {
    info: import_pieces_framework.Property.MarkDown({
      value: markdown
    }),
    name: import_pieces_framework.Property.ShortText({
      displayName: "Tag Name",
      description: void 0,
      required: !0
    })
  },
  async run(ctx) {
    return await ctx.tags.add({
      name: ctx.propsValue.name
    }), {
      success: !0
    };
  }
});

// ../ap/packages/pieces/core/tags/src/index.ts
var tags = (0, import_pieces_framework2.createPiece)({
  displayName: "Tags",
  description: "Add custom tags to your run for filtration",
  auth: import_pieces_framework2.PieceAuth.None(),
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/new-core/tags.svg",
  categories: [import_pieces_framework3.PieceCategory.CORE],
  authors: ["kishanprmr", "MoShizzle", "abuaboud"],
  actions: [addTag],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  tags
});
