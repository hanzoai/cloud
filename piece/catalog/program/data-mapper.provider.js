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

// ../ap/packages/pieces/core/data-mapper/src/index.ts
var index_exports = {};
__export(index_exports, {
  dataMapper: () => dataMapper
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/core/data-mapper/src/lib/actions/advanced-mapping.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), advancedMapping = (0, import_pieces_framework.createAction)({
  audience: "both",
  name: "advanced_mapping",
  classification: "READ",
  displayName: "Advanced Mapping",
  description: "Map data from one format to another",
  aiMetadata: { description: "Reshapes data already present in the run by returning the Mapping object you define with every template reference inside it resolved (e.g. {{trigger.body.email}}), so you can rename, subset, or nest fields into a clean payload for a later step. Pick it when you only need to restructure or rekey existing values, not transform them - prefer text-helper for string edits, date-helper for date conversion, and the JSON piece to parse or stringify. Requires the Mapping JSON object; read-only and idempotent.", idempotent: !0 },
  errorHandlingOptions: {
    continueOnFailure: {
      hide: !0
    },
    retryOnFailure: {
      hide: !0
    }
  },
  props: {
    mapping: import_pieces_framework.Property.Json({
      displayName: "Mapping",
      description: "The mapping to use",
      required: !0,
      defaultValue: {
        newProperty: "oldProperty"
      }
    })
  },
  async run(ctx) {
    return ctx.propsValue.mapping;
  }
});

// ../ap/packages/pieces/core/data-mapper/src/index.ts
var dataMapper = (0, import_pieces_framework2.createPiece)({
  displayName: "Data Mapper",
  description: "tools to manipulate data structure",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/new-core/data-mapper.svg",
  auth: import_pieces_framework2.PieceAuth.None(),
  categories: [import_pieces_framework3.PieceCategory.CORE],
  authors: ["kishanprmr", "MoShizzle", "AbdulTheActivePiecer", "khaledmashaly", "abuaboud"],
  actions: [advancedMapping],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  dataMapper
});
