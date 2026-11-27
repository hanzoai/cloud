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

// ../ap/packages/pieces/community/octopush-sms/src/index.ts
var index_exports = {};
__export(index_exports, {
  octopushSms: () => octopushSms
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/octopush-sms/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdown = `

`, octopushAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdown,
  required: !0,
  props: {
    api_login: import_pieces_framework.Property.ShortText({
      displayName: "API Login",
      description: " Your Octopush API Login",
      required: !0
    }),
    api_key: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: " Your Octopush API Key",
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/octopush-sms/src/lib/actions/add-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/octopush-sms/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.octopush.com/v1/public";
async function makeRequest(api_key, api_login, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "api-login": api_login,
        "api-key": api_key
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/octopush-sms/src/lib/actions/add-contact.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), addContact = (0, import_pieces_framework2.createAction)({
  auth: octopushAuth,
  name: "addContact",
  displayName: "Add Contact",
  description: "Add one or more contacts to a contact list",
  audience: "both",
  aiMetadata: {
    description: 'Creates or updates a contact in an Octopush contact list, keyed by phone number (international format required). Use to register or enrich a recipient before messaging; optionally assign it to a named list and a tag. By default an existing contact is updated, but enabling "Do Not Overwrite" makes it skip already-present numbers. Not idempotent: re-running can re-add or re-update the contact.',
    idempotent: !1
  },
  props: {
    phone_number: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Number",
      description: "The phone number in international format (e.g., +33612345678)",
      required: !0
    }),
    first_name: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "First name of the contact",
      required: !1
    }),
    last_name: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the contact",
      required: !1
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "Email address of the contact",
      required: !1
    }),
    list_name: import_pieces_framework2.Property.ShortText({
      displayName: "List Name",
      description: "Name of the contact list to add the contact to (optional, max 30 chars)",
      required: !1
    }),
    tag_name: import_pieces_framework2.Property.ShortText({
      displayName: "Tag Name",
      description: "Tag to attach to the contact (optional, max 20 chars)",
      required: !1
    }),
    do_not_overwrite: import_pieces_framework2.Property.Checkbox({
      displayName: "Do Not Overwrite",
      description: "If enabled, will skip the contact if it already exists instead of updating it",
      required: !1,
      defaultValue: !1
    }),
    auto_remove_blacklisted: import_pieces_framework2.Property.Checkbox({
      displayName: "Auto Remove Blacklisted",
      description: "If enabled, blacklisted numbers will be automatically removed from the list",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      phone_number,
      first_name,
      last_name,
      email,
      list_name,
      tag_name,
      do_not_overwrite,
      auto_remove_blacklisted
    } = context.propsValue, contact = {
      phone_number
    };
    first_name && (contact.first_name = first_name), last_name && (contact.last_name = last_name), email && (contact.email = email);
    let request = {
      contacts: [contact]
    };
    return list_name && (request.list_name = list_name), tag_name && (request.tag_name = tag_name), do_not_overwrite && (request.do_not_overwrite = do_not_overwrite), auto_remove_blacklisted && (request.auto_remove_blacklisted_numbers = auto_remove_blacklisted), await makeRequest(
      context.auth.props.api_key,
      context.auth.props.api_login,
      import_pieces_common2.HttpMethod.POST,
      "/contact/create",
      request
    );
  }
});

// ../ap/packages/pieces/community/octopush-sms/src/lib/actions/send-a-new-sms.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), sendANewSms = (0, import_pieces_framework3.createAction)({
  auth: octopushAuth,
  name: "sendANewSms",
  displayName: "Send a New SMS",
  description: "Send an SMS message to one or more recipients",
  audience: "both",
  aiMetadata: {
    description: 'Sends an SMS campaign to a recipient via Octopush, with an optional "Send At" time to schedule delivery instead of sending immediately. Use to deliver alerts, transactional, marketing, or OTP messages; requires the message text and a 3-11 character alphanumeric sender ID, and the recipient phone number in international format. Not idempotent: each call dispatches a new message.',
    idempotent: !1
  },
  props: {
    phone_number: import_pieces_framework3.Property.ShortText({
      displayName: "Phone Number",
      description: "Recipient phone number in international format (e.g., +33612345678)",
      required: !0
    }),
    first_name: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "Recipient first name (optional)",
      required: !1
    }),
    last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "Recipient last name (optional)",
      required: !1
    }),
    message: import_pieces_framework3.Property.LongText({
      displayName: "Message Text",
      description: "The SMS message content (1-1224 characters)",
      required: !0
    }),
    sender: import_pieces_framework3.Property.ShortText({
      displayName: "Sender ID",
      description: "Sender name/ID (3-11 alphanumeric characters)",
      required: !0
    }),
    purpose: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Purpose",
      description: "Type of SMS campaign",
      required: !1,
      options: {
        options: [
          { label: "Alert", value: "alert" },
          { label: "Transactional", value: "transactional" },
          { label: "Marketing", value: "marketing" },
          { label: "Wholesale", value: "wholesale" }
        ]
      }
    }),
    send_at: import_pieces_framework3.Property.ShortText({
      displayName: "Send At",
      description: "Schedule sending time in ISO8601 format (e.g., 2024-12-25T10:30:00+01:00) - optional",
      required: !1
    }),
    with_replies: import_pieces_framework3.Property.Checkbox({
      displayName: "Enable Replies",
      description: "Enable to receive recipient replies",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      phone_number,
      first_name,
      last_name,
      message,
      sender,
      purpose,
      send_at,
      with_replies
    } = context.propsValue, recipient = {
      phone_number
    };
    first_name && (recipient.first_name = first_name), last_name && (recipient.last_name = last_name);
    let request = {
      text: message,
      recipients: [recipient],
      sender
    };
    return purpose && (request.purpose = purpose), send_at && (request.send_at = send_at), with_replies && (request.with_replies = with_replies), await makeRequest(
      context.auth.props.api_key,
      context.auth.props.api_login,
      import_pieces_common3.HttpMethod.POST,
      "/sms-campaign/send",
      request
    );
  }
});

// ../ap/packages/pieces/community/octopush-sms/src/index.ts
var import_pieces_common4 = require("@activepieces/pieces-common");
var import_pieces_framework5 = require("@activepieces/pieces-framework"), octopushSms = (0, import_pieces_framework4.createPiece)({
  displayName: "Octopush SMS",
  auth: octopushAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/octopush-sms.png",
  description: "Send bulk messaging for your promotions, order tracking, appointment reminders, voice messages, one-time passwords (OTP), 2-way chat sms and much more!",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework5.PieceCategory.COMMUNICATION],
  actions: [
    addContact,
    sendANewSms,
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: octopushAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        "api-key": auth.props.api_key,
        "api-login": auth.props.api_login
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  octopushSms
});
