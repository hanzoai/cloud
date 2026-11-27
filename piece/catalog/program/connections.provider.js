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

// ../ap/packages/pieces/core/connections/src/index.ts
var index_exports = {};
__export(index_exports, {
  connections: () => connections
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/core/connections/src/lib/actions/read-connection.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), markdown = `
**Advanced Piece**
<br>
Use this piece if you are unsure which connection to use beforehand, such as when the connection external ID is sent through a webhook message.

**Notes:**
- You can retrieve the external ID from the connection settings page by hovering over the connection name.
- Use this action to retrieve connection values by their external IDs from this project.
- After testing the step, you can use the dynamic value in the piece by clicking (X) and referring to this step.
`, readConnection = (0, import_pieces_framework.createAction)({
  audience: "both",
  name: "read_connection",
  classification: "READ",
  displayName: "Read Connection",
  description: "Fetch connection by name",
  aiMetadata: { description: "Looks up a stored app connection in the current project by its external ID, so a flow can resolve credentials at runtime instead of binding one fixed connection at build time (for example when the external ID arrives in a webhook payload). When the connection is known up front, configure the target piece action with its own auth instead. Requires the exact external ID and throws if no connection matches; read-only and idempotent.", idempotent: !0 },
  props: {
    info: import_pieces_framework.Property.MarkDown({
      value: markdown
    }),
    connection_name: import_pieces_framework.Property.ShortText({
      displayName: "Connection External ID",
      description: void 0,
      required: !0
    })
  },
  async run(ctx) {
    let connection = await ctx.connections.get(ctx.propsValue.connection_name);
    if ((0, import_pieces_framework2.isNil)(connection))
      throw new Error(JSON.stringify({
        message: "Connection not found",
        connectionName: ctx.propsValue.connection_name
      }));
    return connection;
  }
});

// ../ap/packages/pieces/core/connections/src/index.ts
var connections = (0, import_pieces_framework3.createPiece)({
  displayName: "Connections",
  description: "Read connections dynamically",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/new-core/connections.svg",
  categories: [import_pieces_framework4.PieceCategory.CORE],
  auth: import_pieces_framework3.PieceAuth.None(),
  authors: ["kishanprmr", "AbdulTheActivePiecer", "khaledmashaly", "abuaboud"],
  actions: [readConnection],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  connections
});
