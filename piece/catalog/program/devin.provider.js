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

// ../ap/packages/pieces/community/devin/src/index.ts
var index_exports = {};
__export(index_exports, {
  devin: () => devin,
  devinAuth: () => devinAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/devin/src/lib/actions/create-session.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var createSession = (0, import_pieces_framework.createAction)({
  name: "create_session",
  displayName: "Create Session",
  description: "Creates a new Devin session",
  audience: "both",
  aiMetadata: { description: "Starts a new Devin AI engineering session from a prompt, kicking off an autonomous coding/technical task. Use this to begin work; capture the returned session id for follow-up actions. Optionally seed the session from a snapshot or playbook, mark it unlisted, or set the idempotent flag so repeat calls reuse an existing session instead of spawning a new one. By default each call creates a new session.", idempotent: !1 },
  auth: devinAuth,
  props: {
    prompt: import_pieces_framework.Property.ShortText({
      displayName: "Prompt",
      required: !0
    }),
    snapshotId: import_pieces_framework.Property.ShortText({
      displayName: "Snapshot ID",
      required: !1
    }),
    playbookId: import_pieces_framework.Property.ShortText({
      displayName: "Playbook ID",
      required: !1
    }),
    unlisted: import_pieces_framework.Property.Checkbox({
      displayName: "Unlisted",
      required: !1
    }),
    idempotent: import_pieces_framework.Property.Checkbox({
      displayName: "Idempotent",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.devin.ai/v1/sessions",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        prompt: propsValue.prompt,
        snapshot_id: propsValue.snapshotId,
        playbook_id: propsValue.playbookId,
        unlisted: propsValue.unlisted,
        idempotent: propsValue.idempotent
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/devin/src/lib/actions/get-session-details.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var getSessionDetails = (0, import_pieces_framework2.createAction)({
  name: "get_session_details",
  displayName: "Get Session Details",
  description: "Retrieves details of a specific Devin session",
  audience: "both",
  aiMetadata: { description: "Fetches the current details and status of an existing Devin session by its session id. Use this to check progress, read results, or poll the outcome of a previously created session. Read-only and idempotent; requires a valid session id.", idempotent: !0 },
  auth: devinAuth,
  props: {
    sessionId: import_pieces_framework2.Property.ShortText({
      displayName: "Session ID",
      required: !0,
      description: "The ID of the session to retrieve details for"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `https://api.devin.ai/v1/session/${propsValue.sessionId}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/devin/src/lib/actions/send-message.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var sendMessage = (0, import_pieces_framework3.createAction)({
  name: "send_message",
  displayName: "Send Message",
  description: "Sends a message to a Devin session",
  audience: "both",
  aiMetadata: { description: "Posts a message into an existing Devin session to give it further instructions, context, or follow-up requests. Use this to continue a conversation with an already-running session rather than starting a new one. Requires the target session id and message text; not idempotent, as each call appends a new message.", idempotent: !1 },
  auth: devinAuth,
  props: {
    sessionId: import_pieces_framework3.Property.ShortText({
      displayName: "Session ID",
      required: !0,
      description: "The ID of the session to send the message to"
    }),
    message: import_pieces_framework3.Property.LongText({
      displayName: "Message",
      required: !0,
      description: "The message to send to the session"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `https://api.devin.ai/v1/session/${propsValue.sessionId}/messages`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        message: propsValue.message
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/devin/src/index.ts
var devinAuth = import_pieces_framework4.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Your Devin API key (used in the Authorization header)."
}), devin = (0, import_pieces_framework4.createPiece)({
  displayName: "Devin AI",
  description: "AI-powered engineering assistant for automating development tasks, code generation, and technical conversations.",
  logoUrl: "https://cdn.activepieces.com/pieces/devin.png",
  auth: devinAuth,
  categories: [import_pieces_framework5.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  minimumSupportedRelease: "0.20.0",
  authors: ["ahmad-swanblocks"],
  actions: [
    createSession,
    getSessionDetails,
    sendMessage
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  devin,
  devinAuth
});
