"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/maileroo/src/index.ts
var index_exports = {};
__export(index_exports, {
  maileroo: () => maileroo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/maileroo/src/lib/actions/send-email.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/maileroo/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), markdown = `
For Sending API key, follow these steps:
1. Navigate to [Domains](https://app.maileroo.com/domains).
2. Click on the domain you want to use.
3. Click on the **Create sending key** under the API section.
4. Click **New Sending Key**.
5. Copy the key under the **Sending Key** column.

For Verification API key, follow these steps:
1. Navigate to [Verification API](https://app.maileroo.com/verifications).
2. Copy the key under the **Verification API** section.
`, mailerooAuth = import_pieces_framework.PieceAuth.CustomAuth({
  required: !0,
  description: markdown,
  props: {
    keyType: import_pieces_framework.Property.StaticDropdown({
      displayName: "Type",
      defaultValue: "sending",
      options: {
        options: [
          {
            label: "Sending Key",
            value: "sending"
          },
          {
            label: "Verification Key",
            value: "verification"
          }
        ]
      },
      required: !0
    }),
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    let PLACEHOLDER_STRING = "placeholder";
    if (auth.keyType === "sending") {
      try {
        await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.POST,
          url: "https://smtp.maileroo.com/api/v2/emails",
          body: {
            from: { address: PLACEHOLDER_STRING },
            to: [{ address: PLACEHOLDER_STRING }],
            subject: PLACEHOLDER_STRING,
            plain: PLACEHOLDER_STRING
          },
          headers: {
            "X-API-Key": auth.apiKey,
            "Content-Type": "application/json"
          }
        });
      } catch (e) {
        let status = e.response.status;
        if (status === 401)
          return {
            valid: !1,
            error: "Invalid API Sending key"
          };
        if (status >= 500)
          return {
            valid: !1,
            error: "An error occurred while validating the API key"
          };
      }
      return {
        valid: !0
      };
    } else {
      let result = await import_pieces_common.httpClient.sendRequest({
        url: "https://verify.maileroo.net/check",
        method: import_pieces_common.HttpMethod.POST,
        body: { email_address: PLACEHOLDER_STRING },
        headers: {
          "X-API-Key": auth.apiKey,
          "Content-Type": "application/json"
        }
      });
      return result.status === 200 && result.body.error_code !== "0401" ? {
        valid: !0
      } : {
        error: "Invalid Verification API key",
        valid: !1
      };
    }
  }
});

// ../ap/packages/pieces/community/maileroo/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
function toEmailObjects(addresses) {
  return addresses.filter((addr) => typeof addr == "string").map((address) => ({ address }));
}
function buildAttachmentList(attachments) {
  return attachments.filter(
    (item) => typeof item == "object" && !(0, import_pieces_framework2.isNil)(item) && "file" in item
  ).map(({ file }) => ({
    file_name: file.filename,
    content: file.base64
  }));
}

// ../ap/packages/pieces/community/maileroo/src/lib/actions/send-email.ts
var sendEmail = (0, import_pieces_framework3.createAction)({
  auth: mailerooAuth,
  name: "sendEmail",
  classification: "WRITE",
  displayName: "Send Email",
  description: "Sends an email using the Maileroo API.",
  audience: "both",
  aiMetadata: {
    description: "Sends a custom email through Maileroo with inline subject and body content (plain text or HTML), optionally with CC/BCC, reply-to, file attachments, and open/click tracking. Choose this to compose and deliver a one-off message; use the template action instead when the body comes from a saved Maileroo template. The sender address must belong to a verified domain, and the content type prop must match whether the content is plain or HTML. Not idempotent: each call dispatches a new email.",
    idempotent: !1
  },
  props: {
    from: import_pieces_framework3.Property.ShortText({
      displayName: "From Email",
      description: "Sender email address. Must be from a verified domain.",
      required: !0
    }),
    from_name: import_pieces_framework3.Property.ShortText({
      displayName: "From Name",
      description: "Sender display name.",
      required: !1
    }),
    to: import_pieces_framework3.Property.Array({
      displayName: "To",
      description: "Recipient email addresses.",
      required: !0
    }),
    subject: import_pieces_framework3.Property.ShortText({
      displayName: "Subject",
      description: "Email subject (max 255 characters).",
      required: !0
    }),
    cc: import_pieces_framework3.Property.Array({
      displayName: "CC",
      description: "Carbon copy recipients.",
      required: !1
    }),
    bcc: import_pieces_framework3.Property.Array({
      displayName: "BCC",
      description: "Blind carbon copy recipients.",
      required: !1
    }),
    reply_to: import_pieces_framework3.Property.ShortText({
      displayName: "Reply To",
      description: "Email address to receive replies.",
      required: !1
    }),
    content_type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Content Type",
      required: !0,
      defaultValue: "text",
      options: {
        disabled: !1,
        options: [
          { label: "Plain Text", value: "text" },
          { label: "HTML", value: "html" }
        ]
      }
    }),
    content: import_pieces_framework3.Property.ShortText({
      displayName: "Content",
      description: "HTML is only allowed if you selected HTML as type",
      required: !0
    }),
    attachments: import_pieces_framework3.Property.Array({
      displayName: "Attachments",
      required: !1,
      properties: {
        file: import_pieces_framework3.Property.File({
          displayName: "File",
          required: !0
        })
      }
    }),
    tracking: import_pieces_framework3.Property.Checkbox({
      displayName: "Enable Tracking",
      description: "Enable open and click tracking. Defaults to account settings if not specified.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let {
      from,
      from_name,
      to,
      subject,
      cc,
      bcc,
      reply_to,
      content,
      content_type,
      tracking
    } = context.propsValue, attachments = (_a = context.propsValue.attachments) != null ? _a : [], mappedAttachments = buildAttachmentList(attachments != null ? attachments : []), body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      from: __spreadValues({
        address: from
      }, (0, import_pieces_framework4.spreadIfDefined)("display_name", from_name)),
      to: toEmailObjects(to),
      subject
    }, (0, import_pieces_framework4.spreadIfDefined)("cc", cc && cc.length > 0 ? toEmailObjects(cc) : void 0)), (0, import_pieces_framework4.spreadIfDefined)("bcc", bcc && bcc.length > 0 ? toEmailObjects(bcc) : void 0)), (0, import_pieces_framework4.spreadIfDefined)("reply_to", reply_to ? [{ address: reply_to }] : void 0)), content_type === "html" ? { html: content } : { plain: content }), (0, import_pieces_framework4.spreadIfDefined)("tracking", tracking != null ? tracking : void 0)), (0, import_pieces_framework4.spreadIfDefined)("attachments", mappedAttachments.length > 0 ? mappedAttachments : void 0));
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://smtp.maileroo.com/api/v2/emails",
      body,
      headers: {
        "X-API-Key": context.auth.props.apiKey,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/maileroo/src/lib/actions/send-from-template.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var sendFromTemplate = (0, import_pieces_framework5.createAction)({
  auth: mailerooAuth,
  name: "sendFromTemplate",
  classification: "WRITE",
  displayName: "Send Email using Template",
  description: "Sends an email from an existing template.",
  audience: "both",
  aiMetadata: {
    description: "Sends an email through Maileroo whose body is rendered from a saved template, passing key-value template data to fill its {{ variable }} placeholders (subject also supports these). Choose this over the plain send action when the message content lives in a Maileroo template; supports CC/BCC, reply-to, attachments, and tracking. Requires a numeric template_id from the Templates dashboard, and the sender address must be on a verified domain. Not idempotent: each call dispatches a new email.",
    idempotent: !1
  },
  props: {
    from: import_pieces_framework5.Property.ShortText({
      displayName: "From Email",
      description: "Sender email address. Must be from a verified domain.",
      required: !0
    }),
    from_name: import_pieces_framework5.Property.ShortText({
      displayName: "From Name",
      description: "Sender display name.",
      required: !1
    }),
    to: import_pieces_framework5.Property.Array({
      displayName: "To",
      description: "Recipient email addresses.",
      required: !0
    }),
    subject: import_pieces_framework5.Property.ShortText({
      displayName: "Subject",
      description: "Email subject (max 255 characters). Supports template variables using {{ variable_name }} syntax.",
      required: !0
    }),
    template_id: import_pieces_framework5.Property.Number({
      displayName: "Template ID",
      description: "The ID of the template to use (found in the Templates dashboard).",
      required: !0
    }),
    template_data: import_pieces_framework5.Property.Object({
      displayName: "Template Data",
      description: 'Key-value pairs to fill template variables. The string {{ name }} in the template will be replaced with the value of "name".',
      required: !1
    }),
    cc: import_pieces_framework5.Property.Array({
      displayName: "CC",
      description: "Carbon copy recipients.",
      required: !1
    }),
    bcc: import_pieces_framework5.Property.Array({
      displayName: "BCC",
      description: "Blind carbon copy recipients.",
      required: !1
    }),
    reply_to: import_pieces_framework5.Property.ShortText({
      displayName: "Reply To",
      description: "Email address to receive replies.",
      required: !1
    }),
    attachments: import_pieces_framework5.Property.Array({
      displayName: "Attachments",
      required: !1,
      properties: {
        file: import_pieces_framework5.Property.File({
          displayName: "File",
          required: !0
        })
      }
    }),
    tracking: import_pieces_framework5.Property.Checkbox({
      displayName: "Enable Tracking",
      description: "Enable open and click tracking. Defaults to account settings if not specified.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let {
      from,
      from_name,
      to,
      subject,
      template_id,
      template_data,
      cc,
      bcc,
      reply_to,
      tracking
    } = context.propsValue, attachments = (_a = context.propsValue.attachments) != null ? _a : [], mappedAttachments = buildAttachmentList(attachments != null ? attachments : []), body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      from: __spreadValues({
        address: from
      }, (0, import_pieces_framework6.spreadIfDefined)("display_name", from_name)),
      to: toEmailObjects(to),
      subject,
      template_id
    }, (0, import_pieces_framework6.spreadIfDefined)("template_data", template_data)), (0, import_pieces_framework6.spreadIfDefined)("cc", cc && cc.length > 0 ? toEmailObjects(cc) : void 0)), (0, import_pieces_framework6.spreadIfDefined)("bcc", bcc && bcc.length > 0 ? toEmailObjects(bcc) : void 0)), (0, import_pieces_framework6.spreadIfDefined)("reply_to", reply_to ? [{ address: reply_to }] : void 0)), (0, import_pieces_framework6.spreadIfDefined)("tracking", tracking != null ? tracking : void 0)), (0, import_pieces_framework6.spreadIfDefined)("attachments", mappedAttachments.length > 0 ? mappedAttachments : void 0));
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://smtp.maileroo.com/api/v2/emails/template",
      body,
      headers: {
        "X-API-Key": context.auth.props.apiKey,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/maileroo/src/lib/actions/verify-email.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var verifyEmail = (0, import_pieces_framework7.createAction)({
  auth: mailerooAuth,
  name: "verifyEmail",
  classification: "READ",
  displayName: "Verify Email",
  description: "Verifies an email address.",
  audience: "both",
  aiMetadata: {
    description: "Checks the validity and deliverability of a single email address via Maileroo verification service. Use it to validate an address before sending or to clean a contact list. Takes one email string as input; it is a read-only lookup that sends no mail, so repeating the call with the same address is safe and idempotent.",
    idempotent: !0
  },
  props: {
    content: import_pieces_framework7.Property.ShortText({
      displayName: "Email",
      description: "Email to verify",
      required: !0
    })
  },
  async run(context) {
    return (await import_pieces_common4.httpClient.sendRequest({
      url: "https://verify.maileroo.net/check",
      method: import_pieces_common4.HttpMethod.POST,
      body: {
        email_address: context.propsValue.content
      },
      headers: {
        "X-API-Key": context.auth.props.apiKey,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/maileroo/src/index.ts
var maileroo = (0, import_pieces_framework8.createPiece)({
  displayName: "Maileroo",
  auth: mailerooAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/maileroo.png",
  categories: [
    import_pieces_framework9.PieceCategory.MARKETING,
    import_pieces_framework9.PieceCategory.BUSINESS_INTELLIGENCE,
    import_pieces_framework9.PieceCategory.COMMUNICATION
  ],
  description: "Email Delivery Service with Real-Time Analytics and Reporting",
  authors: ["codegino"],
  actions: [sendEmail, sendFromTemplate, verifyEmail],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  maileroo
});
