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

// ../ap/packages/pieces/community/esignatures/src/index.ts
var index_exports = {};
__export(index_exports, {
  esignatures: () => esignatures
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/esignatures/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), esignaturesAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Esignatures API Key",
  required: !0
});

// ../ap/packages/pieces/community/esignatures/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/esignatures/src/lib/actions/create-contract.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), createContract = (0, import_pieces_framework2.createAction)({
  auth: esignaturesAuth,
  name: "createContract",
  displayName: "Create Contract",
  description: "Create a new contract from a template in eSignatures",
  audience: "both",
  aiMetadata: { description: "Creates an eSignatures contract from an existing template and, by default, sends it to the listed signers for signature; set save-as-draft to create it without sending. Use when an agent needs to issue a document for signing. Requires a valid template ID and at least one signer (name plus email and/or mobile). Not idempotent: each call creates and dispatches a new contract.", idempotent: !1 },
  props: {
    templateId: import_pieces_framework2.Property.ShortText({
      displayName: "Template ID",
      description: "The ID of the template to use for the contract",
      required: !0
    }),
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Contract Title",
      description: "Unique title for the contract (defaults to template title if not specified)",
      required: !1
    }),
    locale: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Language",
      description: "Language setting for the signer page and emails",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Czech", value: "cz" },
          { label: "Danish", value: "da" },
          { label: "German", value: "de" },
          { label: "Greek", value: "el" },
          { label: "English", value: "en" },
          { label: "English (UK)", value: "en-GB" },
          { label: "Spanish", value: "es" },
          { label: "Finnish", value: "fi" },
          { label: "French", value: "fr" },
          { label: "Croatian", value: "hr" },
          { label: "Hungarian", value: "hu" },
          { label: "Indonesian", value: "id" },
          { label: "Italian", value: "it" },
          { label: "Japanese", value: "ja" },
          { label: "Dutch", value: "nl" },
          { label: "Norwegian", value: "no" },
          { label: "Polish", value: "pl" },
          { label: "Portuguese", value: "pt" },
          { label: "Romanian", value: "ro" },
          { label: "Serbian", value: "rs" },
          { label: "Slovak", value: "sk" },
          { label: "Slovenian", value: "sl" },
          { label: "Swedish", value: "sv" },
          { label: "Vietnamese", value: "vi" },
          { label: "Chinese (Simplified)", value: "zh-CN" }
        ]
      }
    }),
    metadata: import_pieces_framework2.Property.ShortText({
      displayName: "Metadata",
      description: "Custom data to attach to the contract",
      required: !1
    }),
    expiresInHours: import_pieces_framework2.Property.Number({
      displayName: "Expires In Hours",
      description: "Sets the expiry time (in hours) for the contract",
      required: !1
    }),
    customWebhookUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Custom Webhook URL",
      description: "Custom URL for webhook notifications",
      required: !1
    }),
    assignedUserEmail: import_pieces_framework2.Property.ShortText({
      displayName: "Assigned User Email",
      description: "Email to assign management of the contract",
      required: !1
    }),
    labels: import_pieces_framework2.Property.Array({
      displayName: "Labels",
      description: "Labels to assign to the contract (for grouping)",
      required: !1
    }),
    test: import_pieces_framework2.Property.Checkbox({
      displayName: "Test Contract",
      description: "Mark as test/demo contract with no fees charged",
      required: !1
    }),
    saveAsDraft: import_pieces_framework2.Property.Checkbox({
      displayName: "Save as Draft",
      description: "Save as draft instead of sending to signers",
      required: !1
    }),
    signers: import_pieces_framework2.Property.Array({
      displayName: "Signers",
      description: "List of individuals required to sign (name, email, and/or mobile)",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let payload = {
      template_id: propsValue.templateId
    };
    return propsValue.title && (payload.title = propsValue.title), propsValue.locale && (payload.locale = propsValue.locale), propsValue.metadata && (payload.metadata = propsValue.metadata), propsValue.expiresInHours !== void 0 && (payload.expires_in_hours = propsValue.expiresInHours.toString()), propsValue.customWebhookUrl && (payload.custom_webhook_url = propsValue.customWebhookUrl), propsValue.assignedUserEmail && (payload.assigned_user_email = propsValue.assignedUserEmail), propsValue.labels && (payload.labels = propsValue.labels), propsValue.test !== void 0 && (payload.test = propsValue.test ? "yes" : "no"), propsValue.saveAsDraft !== void 0 && (payload.save_as_draft = propsValue.saveAsDraft ? "yes" : "no"), propsValue.signers && (payload.signers = propsValue.signers), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `https://esignatures.com/api/contracts?token=${auth.secret_text}`,
      headers: {
        "Content-Type": "application/json"
      },
      body: payload
    })).body;
  }
});

// ../ap/packages/pieces/community/esignatures/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), esignatures = (0, import_pieces_framework3.createPiece)({
  displayName: "eSignatures",
  auth: esignaturesAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/esignatures.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework4.PieceCategory.SALES_AND_CRM],
  actions: [
    createContract,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://esignatures.com/api",
      authLocation: "queryParams",
      auth: esignaturesAuth,
      authMapping: async (auth) => ({
        token: `${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  esignatures
});
