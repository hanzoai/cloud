"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/whatsscale/src/index.ts
var index_exports = {};
__export(index_exports, {
  whatsscale: () => whatsscale
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework35 = require("@activepieces/pieces-framework"), import_pieces_framework36 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/whatsscale/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), whatsscaleAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: `
    To obtain your API key:

    1. Log in to your WhatsScale account at https://whatsscale.com/dashboard
    2. Navigate to Setting => API Keys section
    3. Create a new API key if you don't have one already
    4. Copy your API key and paste it here
  `,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://proxy.whatsscale.com/api/auth/test",
        headers: {
          "X-Api-Key": auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key"
      };
    }
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-text-manual.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/whatsscale/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), BASE_URL = "https://proxy.whatsscale.com";
async function whatsscaleClient(auth, method, path, body, queryParams) {
  return import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      "X-Api-Key": auth,
      "Content-Type": "application/json"
    },
    body,
    queryParams
  });
}

// ../ap/packages/pieces/community/whatsscale/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var whatsscaleProps = {
  session: import_pieces_framework2.Property.Dropdown({
    auth: whatsscaleAuth,
    displayName: "WhatsApp Session",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account"
        };
      try {
        let sessions = (await whatsscaleClient(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/make/sessions"
        )).body;
        return !sessions || sessions.length === 0 ? {
          disabled: !0,
          options: [],
          placeholder: "No sessions found. Connect WhatsApp at whatsscale.com"
        } : {
          disabled: !1,
          options: sessions
        };
      } catch (e) {
        return console.debug(e), {
          disabled: !0,
          options: [],
          placeholder: "Error loading sessions"
        };
      }
    }
  }),
  contact: import_pieces_framework2.Property.Dropdown({
    auth: whatsscaleAuth,
    displayName: "Contact",
    required: !0,
    refreshers: ["session"],
    options: async ({ auth, session }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account"
        };
      if (!session)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please select a session first"
        };
      try {
        let contacts = (await whatsscaleClient(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/make/contacts",
          void 0,
          { session }
        )).body;
        return !contacts || contacts.length === 0 ? {
          disabled: !0,
          options: [],
          placeholder: "No contacts found"
        } : { disabled: !1, options: contacts };
      } catch (e) {
        return console.debug(e), {
          disabled: !0,
          options: [],
          placeholder: "Error loading contacts"
        };
      }
    }
  }),
  group: import_pieces_framework2.Property.Dropdown({
    auth: whatsscaleAuth,
    displayName: "Group",
    required: !0,
    refreshers: ["session"],
    options: async ({ auth, session }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account"
        };
      if (!session)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please select a session first"
        };
      try {
        let groups = (await whatsscaleClient(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/make/groups",
          void 0,
          { session }
        )).body;
        return !groups || groups.length === 0 ? {
          disabled: !0,
          options: [],
          placeholder: "No groups found"
        } : { disabled: !1, options: groups };
      } catch (e) {
        return console.debug(e), {
          disabled: !0,
          options: [],
          placeholder: "Error loading groups"
        };
      }
    }
  }),
  channel: import_pieces_framework2.Property.Dropdown({
    auth: whatsscaleAuth,
    displayName: "Channel",
    required: !0,
    refreshers: ["session"],
    options: async ({ auth, session }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account"
        };
      if (!session)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please select a session first"
        };
      try {
        let channels = (await whatsscaleClient(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/make/channels",
          void 0,
          { session }
        )).body;
        return !channels || channels.length === 0 ? {
          disabled: !0,
          options: [],
          placeholder: "No channels found"
        } : { disabled: !1, options: channels };
      } catch (e) {
        return console.debug(e), {
          disabled: !0,
          options: [],
          placeholder: "Error loading channels"
        };
      }
    }
  }),
  crmContact: import_pieces_framework2.Property.Dropdown({
    auth: whatsscaleAuth,
    displayName: "CRM Contact",
    required: !0,
    refreshers: ["auth"],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account"
        };
      try {
        let contacts = (await whatsscaleClient(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/make/crm/contacts"
        )).body;
        return !contacts || contacts.length === 0 ? {
          disabled: !0,
          options: [],
          placeholder: "No CRM contacts found"
        } : {
          disabled: !1,
          options: contacts.map((contact) => ({
            label: contact.name,
            value: contact.id
          }))
        };
      } catch (e) {
        return console.debug(e), {
          disabled: !0,
          options: [],
          placeholder: "Error loading CRM contacts"
        };
      }
    }
  }),
  crmTag: import_pieces_framework2.Property.Dropdown({
    auth: whatsscaleAuth,
    displayName: "Tag",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account"
        };
      try {
        let tags = (await whatsscaleClient(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/make/crm/tags"
        )).body;
        return !tags || tags.length === 0 ? {
          disabled: !0,
          options: [],
          placeholder: "No tags found"
        } : { disabled: !1, options: tags };
      } catch (e) {
        return console.debug(e), {
          disabled: !0,
          options: [],
          placeholder: "Error loading tags"
        };
      }
    }
  })
};

// ../ap/packages/pieces/community/whatsscale/src/lib/common/recipients.ts
function buildRecipientBody(type, session, recipientValue, chatType) {
  let base = { session };
  switch (type) {
    case "contact" /* CONTACT */:
    case "group" /* GROUP */:
    case "channel" /* CHANNEL */:
      return __spreadProps(__spreadValues({}, base), { chatId: recipientValue });
    case "manual" /* MANUAL */: {
      let suffix = chatType === "contact" /* CONTACT */ ? "@c.us" : "@g.us";
      return __spreadProps(__spreadValues({}, base), { chatId: recipientValue.includes("@") ? recipientValue : recipientValue + suffix });
    }
    case "crm_contact" /* CRM_CONTACT */:
      return __spreadProps(__spreadValues({}, base), {
        contact_type: "crm_contact",
        crm_contact_id: recipientValue
      });
    default:
      throw new Error(`Unknown recipient type: ${type}`);
  }
}

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-text-manual.ts
var sendTextManualAction = (0, import_pieces_framework3.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_text_manual",
  classification: "WRITE",
  displayName: "Send a Message (Manual Entry)",
  description: "Send a text message by entering a phone number or group ID manually",
  audience: "both",
  aiMetadata: { description: "Sends a text message to a recipient typed in directly rather than picked from a list. Set the send-to type to Contact (then a phone number with country code) or Group (then a bare group ID). Pick this when you already hold the raw number/ID; use the contact, group, CRM-contact, or channel text variants when selecting from session lists. Not idempotent: each call sends another message.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    chatType: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Send To",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Contact (Phone Number)", value: "contact" /* CONTACT */ },
          { label: "Group", value: "group" /* GROUP */ }
        ]
      }
    }),
    recipient: import_pieces_framework3.Property.ShortText({
      displayName: "Phone Number or Group ID",
      required: !0,
      description: "For contacts: phone number with country code (e.g., +31649931832). For groups: group ID (e.g., 120363318673245672)"
    }),
    text: import_pieces_framework3.Property.LongText({
      displayName: "Message",
      required: !0,
      description: "The text message to send"
    })
  },
  async run(context) {
    let { session, chatType, recipient, text } = context.propsValue, auth = context.auth.secret_text, body = buildRecipientBody(
      "manual" /* MANUAL */,
      session,
      recipient,
      chatType
    );
    return (await whatsscaleClient(
      auth,
      import_pieces_common4.HttpMethod.POST,
      "/api/sendText",
      __spreadProps(__spreadValues({}, body), { text })
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-text-to-contact.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var sendTextToContactAction = (0, import_pieces_framework4.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_text_to_contact",
  classification: "WRITE",
  displayName: "Send a Message to a Contact",
  description: "Send a text message to a WhatsApp contact selected from your contact list",
  audience: "both",
  aiMetadata: { description: "Sends a text message to a WhatsApp contact whose chat ID is chosen from the session contact list. Pick this when the recipient is a known direct contact; use the manual-entry text action for a raw phone number, or the group/CRM/channel text variants for other recipient types. Not idempotent: each call sends another message.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    contact: whatsscaleProps.contact,
    text: import_pieces_framework4.Property.LongText({
      displayName: "Message",
      required: !0,
      description: "The text message to send"
    })
  },
  async run(context) {
    let { session, contact, text } = context.propsValue, auth = context.auth.secret_text, body = buildRecipientBody("contact" /* CONTACT */, session, contact);
    return (await whatsscaleClient(
      auth,
      import_pieces_common5.HttpMethod.POST,
      "/api/sendText",
      __spreadProps(__spreadValues({}, body), { text })
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-text-to-group.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var sendTextToGroupAction = (0, import_pieces_framework5.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_text_to_group",
  classification: "WRITE",
  displayName: "Send a Message to a Group",
  description: "Send a text message to a WhatsApp group selected from your group list",
  audience: "both",
  aiMetadata: { description: "Sends a text message to a WhatsApp group whose chat ID is chosen from the session group list. Pick this when the recipient is a known group; use the manual-entry text action for a raw group ID, or the contact/CRM/channel text variants for other recipient types. Not idempotent: each call sends another message.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    group: whatsscaleProps.group,
    text: import_pieces_framework5.Property.LongText({
      displayName: "Message",
      required: !0,
      description: "The text message to send"
    })
  },
  async run(context) {
    let { session, group, text } = context.propsValue, auth = context.auth.secret_text, body = buildRecipientBody("group" /* GROUP */, session, group);
    return (await whatsscaleClient(
      auth,
      import_pieces_common6.HttpMethod.POST,
      "/api/sendText",
      __spreadProps(__spreadValues({}, body), { text })
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-text-to-channel.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var sendTextToChannelAction = (0, import_pieces_framework6.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_text_to_channel",
  classification: "WRITE",
  displayName: "Send a Text to a Channel",
  description: "Send a text message to a WhatsApp Channel",
  audience: "both",
  aiMetadata: { description: "Broadcasts a text message to a WhatsApp Channel whose ID is chosen from the session channel list. Pick this for one-to-many channel posts rather than the contact/group/CRM text variants used for direct chats. Not idempotent: each call posts another message to the channel.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    channel: whatsscaleProps.channel,
    text: import_pieces_framework6.Property.LongText({
      displayName: "Message",
      required: !0,
      description: "The text message to send"
    })
  },
  async run(context) {
    let { session, channel, text } = context.propsValue, auth = context.auth.secret_text, body = buildRecipientBody("channel" /* CHANNEL */, session, channel);
    return (await whatsscaleClient(
      auth,
      import_pieces_common7.HttpMethod.POST,
      "/api/sendText",
      __spreadProps(__spreadValues({}, body), { text })
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-text-to-crm-contact.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var sendTextToCrmContactAction = (0, import_pieces_framework7.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_text_to_crm_contact",
  classification: "WRITE",
  displayName: "Send a Message to a CRM Contact",
  description: "Send a text message to a contact from your WhatsScale CRM",
  audience: "both",
  aiMetadata: { description: "Sends a text message to a contact stored in the WhatsScale CRM, identified by CRM contact ID chosen from the dropdown. Pick this when the recipient is a managed CRM record; use the plain contact, group, manual-entry, or channel text variants for non-CRM recipients. Not idempotent: each call sends another message.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    crmContact: whatsscaleProps.crmContact,
    text: import_pieces_framework7.Property.LongText({
      displayName: "Message",
      required: !0,
      description: "The text message to send"
    })
  },
  async run(context) {
    let { session, crmContact, text } = context.propsValue, auth = context.auth.secret_text, body = buildRecipientBody(
      "crm_contact" /* CRM_CONTACT */,
      session,
      crmContact
    );
    return (await whatsscaleClient(
      auth,
      import_pieces_common8.HttpMethod.POST,
      "/api/sendText",
      __spreadProps(__spreadValues({}, body), { text })
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-image-to-contact.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/whatsscale/src/lib/common/prepare-file.ts
var import_pieces_common9 = require("@activepieces/pieces-common");
async function prepareFile(apiKey, fileUrl, mediaType) {
  let body = { fileUrl };
  return mediaType && (body.mediaType = mediaType), (await whatsscaleClient(apiKey, import_pieces_common9.HttpMethod.POST, "/make/prepareFile", body)).body.url;
}

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-image-to-contact.ts
var sendImageToContactAction = (0, import_pieces_framework8.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_image_to_contact",
  classification: "WRITE",
  displayName: "Send an Image to a Contact",
  description: "Send an image to a WhatsApp contact selected from the dropdown.",
  audience: "both",
  aiMetadata: { description: "Sends an image to a WhatsApp contact whose chat ID is chosen from the session contact list, with an optional caption. Pick this when the recipient is a known direct contact; use the manual-entry image action for a raw phone number, or the group/CRM/channel variants for other recipient types. Requires a directly downloadable image URL. Not idempotent: each call delivers another image.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    contact: whatsscaleProps.contact,
    imageUrl: import_pieces_framework8.Property.ShortText({
      displayName: "Image URL",
      description: "Direct URL to the image file.",
      required: !0
    }),
    caption: import_pieces_framework8.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the image (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, contact, imageUrl, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, imageUrl);
    return (await whatsscaleClient(apiKey, import_pieces_common10.HttpMethod.POST, "/api/sendImage", {
      session,
      chatId: contact,
      file: preparedUrl,
      caption: caption != null ? caption : ""
    })).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-image-to-group.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var sendImageToGroupAction = (0, import_pieces_framework9.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_image_to_group",
  classification: "WRITE",
  displayName: "Send an Image to a Group",
  description: "Send an image to a WhatsApp group selected from the dropdown.",
  audience: "both",
  aiMetadata: { description: "Sends an image to a WhatsApp group whose chat ID is chosen from the session group list, with an optional caption. Pick this when the recipient is a known group; use the manual-entry image action to target a raw group ID, or the contact/CRM/channel variants for other recipient types. Requires a directly downloadable image URL. Not idempotent: each call delivers another image.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    group: whatsscaleProps.group,
    imageUrl: import_pieces_framework9.Property.ShortText({
      displayName: "Image URL",
      description: "Direct URL to the image file.",
      required: !0
    }),
    caption: import_pieces_framework9.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the image (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, group, imageUrl, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, imageUrl);
    return (await whatsscaleClient(apiKey, import_pieces_common11.HttpMethod.POST, "/api/sendImage", {
      session,
      chatId: group,
      file: preparedUrl,
      caption: caption != null ? caption : ""
    })).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-image-to-channel.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var sendImageToChannelAction = (0, import_pieces_framework10.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_image_to_channel",
  classification: "WRITE",
  displayName: "Send an Image to a Channel",
  description: "Broadcast an image to a WhatsApp Channel selected from the dropdown.",
  audience: "both",
  aiMetadata: { description: "Broadcasts an image to a WhatsApp Channel whose ID is chosen from the session channel list, with an optional caption. Pick this for one-to-many channel broadcasts rather than the contact/group/CRM variants used for direct chats. Requires a directly downloadable image URL. Not idempotent: each call posts another image to the channel.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    channel: whatsscaleProps.channel,
    imageUrl: import_pieces_framework10.Property.ShortText({
      displayName: "Image URL",
      description: "Direct URL to the image file.",
      required: !0
    }),
    caption: import_pieces_framework10.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the image (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, channel, imageUrl, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, imageUrl);
    return (await whatsscaleClient(apiKey, import_pieces_common12.HttpMethod.POST, "/api/sendImage", {
      session,
      chatId: channel,
      file: preparedUrl,
      caption: caption != null ? caption : ""
    })).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-image-to-crm-contact.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var sendImageToCrmContactAction = (0, import_pieces_framework11.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_image_to_crm_contact",
  classification: "WRITE",
  displayName: "Send an Image to a CRM Contact",
  description: "Send an image to a WhatsScale CRM contact selected from the dropdown.",
  audience: "both",
  aiMetadata: { description: "Sends an image to a contact stored in the WhatsScale CRM, identified by CRM contact ID chosen from the dropdown, with an optional caption. Pick this when the recipient is a managed CRM record; use the plain contact, group, manual-entry, or channel image variants for non-CRM recipients. Requires a directly downloadable image URL. Not idempotent: each call delivers another image.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    crmContact: whatsscaleProps.crmContact,
    imageUrl: import_pieces_framework11.Property.ShortText({
      displayName: "Image URL",
      description: "Direct URL to the image file.",
      required: !0
    }),
    caption: import_pieces_framework11.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the image (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, crmContact, imageUrl, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, imageUrl);
    return (await whatsscaleClient(apiKey, import_pieces_common13.HttpMethod.POST, "/api/sendImage", {
      session,
      contact_type: "crm_contact",
      crm_contact_id: crmContact,
      file: preparedUrl,
      caption: caption != null ? caption : ""
    })).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-image-manual.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var sendImageManualAction = (0, import_pieces_framework12.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_image_manual",
  classification: "WRITE",
  displayName: "Send an Image (Manual Entry)",
  description: "Send an image by entering a phone number or group ID manually.",
  audience: "both",
  aiMetadata: { description: "Sends an image to a recipient typed in directly rather than picked from a list, with an optional caption. Set the chat type to Contact (then a phone number with country code) or Group (then a bare group ID). Pick this when you already hold the raw number/ID; use the contact, group, CRM-contact, or channel image variants when selecting from session lists. Requires a directly downloadable image URL. Not idempotent: each call delivers another image.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    chatType: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Chat Type",
      description: "Select whether you are sending to a contact or a group.",
      required: !0,
      options: {
        options: [
          { label: "Contact", value: "contact" /* CONTACT */ },
          { label: "Group", value: "group" /* GROUP */ }
        ]
      }
    }),
    recipient: import_pieces_framework12.Property.ShortText({
      displayName: "Phone Number / Group ID",
      description: "Phone number with country code (digits only) or group ID (without suffix).",
      required: !0
    }),
    imageUrl: import_pieces_framework12.Property.ShortText({
      displayName: "Image URL",
      description: "Direct URL to the image file.",
      required: !0
    }),
    caption: import_pieces_framework12.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the image (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, chatType, recipient, imageUrl, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, imageUrl), recipientBody = buildRecipientBody(
      "manual" /* MANUAL */,
      session,
      recipient,
      chatType
    );
    return (await whatsscaleClient(apiKey, import_pieces_common14.HttpMethod.POST, "/api/sendImage", __spreadProps(__spreadValues({}, recipientBody), {
      file: preparedUrl,
      caption: caption != null ? caption : ""
    }))).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-video-to-contact.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/whatsscale/src/lib/common/poll-job.ts
var import_pieces_common15 = require("@activepieces/pieces-common");
var POLL_INTERVAL_MS = 3e3, MAX_ATTEMPTS = 20;
async function pollJob(apiKey, jobId) {
  var _a, _b;
  if (!jobId) throw new Error("No jobId returned from API");
  for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
    let body = (await whatsscaleClient(apiKey, import_pieces_common15.HttpMethod.GET, `/api/status/${jobId}`)).body;
    if (body.status === "COMPLETED") return (_a = body.result) != null ? _a : body;
    if (body.status === "FAILED") throw new Error((_b = body.error) != null ? _b : "Job failed");
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
  }
  throw new Error("Job timed out after 20 attempts");
}

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-video-to-contact.ts
var sendVideoToContactAction = (0, import_pieces_framework13.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_video_to_contact",
  classification: "WRITE",
  displayName: "Send a Video to a Contact",
  description: "Send a video to a WhatsApp contact selected from the dropdown.",
  audience: "both",
  aiMetadata: { description: "Sends a video to a WhatsApp contact whose chat ID is chosen from the session contact list, with an optional caption. Pick this when the recipient is a known direct contact; use the manual-entry video action for a raw phone number, or the group/CRM/channel variants for other recipient types. Requires a directly downloadable video URL. Not idempotent: each call delivers another video.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    contact: whatsscaleProps.contact,
    videoUrl: import_pieces_framework13.Property.ShortText({
      displayName: "Video URL",
      description: "Direct URL to the video file.",
      required: !0
    }),
    caption: import_pieces_framework13.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the video (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, contact, videoUrl, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, videoUrl), sendResponse = await whatsscaleClient(apiKey, import_pieces_common16.HttpMethod.POST, "/api/sendVideo", {
      session,
      chatId: contact,
      file: preparedUrl,
      caption: caption != null ? caption : ""
    }), { jobId } = sendResponse.body;
    return await pollJob(apiKey, jobId);
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-video-to-group.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var sendVideoToGroupAction = (0, import_pieces_framework14.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_video_to_group",
  classification: "WRITE",
  displayName: "Send a Video to a Group",
  description: "Send a video to a WhatsApp group selected from the dropdown.",
  audience: "both",
  aiMetadata: { description: "Sends a video to a WhatsApp group whose chat ID is chosen from the session group list, with an optional caption. Pick this when the recipient is a known group; use the manual-entry video action instead to target a raw group ID, or the contact/CRM/channel variants for other recipient types. Requires a directly downloadable video URL. Not idempotent: each call delivers another video.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    group: whatsscaleProps.group,
    videoUrl: import_pieces_framework14.Property.ShortText({
      displayName: "Video URL",
      description: "Direct URL to the video file.",
      required: !0
    }),
    caption: import_pieces_framework14.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the video (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, group, videoUrl, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, videoUrl), sendResponse = await whatsscaleClient(apiKey, import_pieces_common17.HttpMethod.POST, "/api/sendVideo", {
      session,
      chatId: group,
      file: preparedUrl,
      caption: caption != null ? caption : ""
    }), { jobId } = sendResponse.body;
    return await pollJob(apiKey, jobId);
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-video-to-channel.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common");
var sendVideoToChannelAction = (0, import_pieces_framework15.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_video_to_channel",
  classification: "WRITE",
  displayName: "Send a Video to a Channel",
  description: "Broadcast a video to a WhatsApp Channel selected from the dropdown.",
  audience: "both",
  aiMetadata: { description: "Broadcasts a video to a WhatsApp Channel whose ID is chosen from the session channel list, with an optional caption. Pick this for one-to-many channel broadcasts rather than the contact/group/CRM variants used for direct chats. Requires a directly downloadable video URL. Not idempotent: each call posts another video to the channel.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    channel: whatsscaleProps.channel,
    videoUrl: import_pieces_framework15.Property.ShortText({
      displayName: "Video URL",
      description: "Direct URL to the video file.",
      required: !0
    }),
    caption: import_pieces_framework15.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the video (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, channel, videoUrl, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, videoUrl), sendResponse = await whatsscaleClient(apiKey, import_pieces_common18.HttpMethod.POST, "/api/sendVideo", {
      session,
      chatId: channel,
      file: preparedUrl,
      caption: caption != null ? caption : ""
    }), { jobId } = sendResponse.body;
    return await pollJob(apiKey, jobId);
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-video-to-crm-contact.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common");
var sendVideoToCrmContactAction = (0, import_pieces_framework16.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_video_to_crm_contact",
  classification: "WRITE",
  displayName: "Send a Video to a CRM Contact",
  description: "Send a video to a WhatsScale CRM contact selected from the dropdown.",
  audience: "both",
  aiMetadata: { description: "Sends a video to a contact stored in the WhatsScale CRM, identified by CRM contact ID chosen from the dropdown, with an optional caption. Pick this when the recipient is a managed CRM record; use the plain contact, group, manual-entry, or channel video variants for non-CRM recipients. Requires a directly downloadable video URL. Not idempotent: each call delivers another video.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    crmContact: whatsscaleProps.crmContact,
    videoUrl: import_pieces_framework16.Property.ShortText({
      displayName: "Video URL",
      description: "Direct URL to the video file.",
      required: !0
    }),
    caption: import_pieces_framework16.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the video (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, crmContact, videoUrl, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, videoUrl), sendResponse = await whatsscaleClient(apiKey, import_pieces_common19.HttpMethod.POST, "/api/sendVideo", {
      session,
      contact_type: "crm_contact",
      crm_contact_id: crmContact,
      file: preparedUrl,
      caption: caption != null ? caption : ""
    }), { jobId } = sendResponse.body;
    return await pollJob(apiKey, jobId);
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-video-manual.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common20 = require("@activepieces/pieces-common");
var sendVideoManualAction = (0, import_pieces_framework17.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_video_manual",
  classification: "WRITE",
  displayName: "Send a Video (Manual Entry)",
  description: "Send a video by entering a phone number or group ID manually.",
  audience: "both",
  aiMetadata: { description: "Sends a video to a recipient typed in directly rather than picked from a list, with an optional caption. Set the chat type to Contact (then a phone number with country code) or Group (then a bare group ID). Pick this when you already hold the raw number/ID; use the contact, group, CRM-contact, or channel video variants when selecting from session lists. Requires a directly downloadable video URL. Not idempotent: each call delivers another video.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    chatType: import_pieces_framework17.Property.StaticDropdown({
      displayName: "Chat Type",
      description: "Select whether you are sending to a contact or a group.",
      required: !0,
      options: {
        options: [
          { label: "Contact", value: "contact" /* CONTACT */ },
          { label: "Group", value: "group" /* GROUP */ }
        ]
      }
    }),
    recipient: import_pieces_framework17.Property.ShortText({
      displayName: "Phone Number / Group ID",
      description: "Phone number with country code (digits only) or group ID (without suffix).",
      required: !0
    }),
    videoUrl: import_pieces_framework17.Property.ShortText({
      displayName: "Video URL",
      description: "Direct URL to the video file.",
      required: !0
    }),
    caption: import_pieces_framework17.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the video (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, chatType, recipient, videoUrl, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, videoUrl), recipientBody = buildRecipientBody(
      "manual" /* MANUAL */,
      session,
      recipient,
      chatType
    ), sendResponse = await whatsscaleClient(apiKey, import_pieces_common20.HttpMethod.POST, "/api/sendVideo", __spreadProps(__spreadValues({}, recipientBody), {
      file: preparedUrl,
      caption: caption != null ? caption : ""
    })), { jobId } = sendResponse.body;
    return await pollJob(apiKey, jobId);
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-document-to-contact.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common21 = require("@activepieces/pieces-common");
var sendDocumentToContactAction = (0, import_pieces_framework18.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_document_to_contact",
  classification: "WRITE",
  displayName: "Send a Document to a Contact",
  description: "Send a document to a WhatsApp contact selected from the dropdown.",
  audience: "both",
  aiMetadata: { description: "Sends a document/file to a WhatsApp contact whose chat ID is chosen from the session contact list, with an optional display filename and caption. Pick this when the recipient is a known direct contact; use the manual-entry document action for a raw phone number, or the group/CRM/channel variants for other recipient types. Requires a directly downloadable document URL. Not idempotent: each call delivers another document.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    contact: whatsscaleProps.contact,
    documentUrl: import_pieces_framework18.Property.ShortText({
      displayName: "Document URL",
      description: "Direct URL to the document file.",
      required: !0
    }),
    filename: import_pieces_framework18.Property.ShortText({
      displayName: "Filename",
      description: "Optional filename shown in WhatsApp (e.g. report.pdf). Auto-detected from URL if not provided.",
      required: !1
    }),
    caption: import_pieces_framework18.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the document (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, contact, documentUrl, filename, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, documentUrl, "document"), body = {
      session,
      chatId: contact,
      file: preparedUrl,
      caption: caption != null ? caption : ""
    };
    filename && (body.filename = filename);
    let sendResponse = await whatsscaleClient(apiKey, import_pieces_common21.HttpMethod.POST, "/api/sendDocument", body), { jobId } = sendResponse.body;
    return await pollJob(apiKey, jobId);
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-document-to-group.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common22 = require("@activepieces/pieces-common");
var sendDocumentToGroupAction = (0, import_pieces_framework19.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_document_to_group",
  classification: "WRITE",
  displayName: "Send a Document to a Group",
  description: "Send a document to a WhatsApp group selected from the dropdown.",
  audience: "both",
  aiMetadata: { description: "Sends a document/file to a WhatsApp group whose chat ID is chosen from the session group list, with an optional display filename and caption. Pick this when the recipient is a known group; use the manual-entry, contact, CRM-contact, or channel variants for other recipient types. Requires a directly downloadable document URL. Not idempotent: each call delivers another document.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    group: whatsscaleProps.group,
    documentUrl: import_pieces_framework19.Property.ShortText({
      displayName: "Document URL",
      description: "Direct URL to the document file.",
      required: !0
    }),
    filename: import_pieces_framework19.Property.ShortText({
      displayName: "Filename",
      description: "Optional filename shown in WhatsApp (e.g. report.pdf). Auto-detected from URL if not provided.",
      required: !1
    }),
    caption: import_pieces_framework19.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the document (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, group, documentUrl, filename, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, documentUrl, "document"), body = {
      session,
      chatId: group,
      file: preparedUrl,
      caption: caption != null ? caption : ""
    };
    filename && (body.filename = filename);
    let sendResponse = await whatsscaleClient(apiKey, import_pieces_common22.HttpMethod.POST, "/api/sendDocument", body), { jobId } = sendResponse.body;
    return await pollJob(apiKey, jobId);
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/messaging/send-document-to-crm-contact.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_common23 = require("@activepieces/pieces-common");
var sendDocumentToCrmContactAction = (0, import_pieces_framework20.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_send_document_to_crm_contact",
  classification: "WRITE",
  displayName: "Send a Document to a CRM Contact",
  description: "Send a document to a WhatsScale CRM contact selected from the dropdown.",
  audience: "both",
  aiMetadata: { description: "Sends a document/file to a contact stored in the WhatsScale CRM, identified by CRM contact ID chosen from the dropdown, with an optional display filename and caption. Pick this when the recipient is a managed CRM record; use the plain contact, group, manual-entry, or channel document variants for non-CRM recipients. Requires a directly downloadable document URL. Not idempotent: each call delivers another document.", idempotent: !1 },
  props: {
    session: whatsscaleProps.session,
    crmContact: whatsscaleProps.crmContact,
    documentUrl: import_pieces_framework20.Property.ShortText({
      displayName: "Document URL",
      description: "Direct URL to the document file.",
      required: !0
    }),
    filename: import_pieces_framework20.Property.ShortText({
      displayName: "Filename",
      description: "Optional filename shown in WhatsApp (e.g. report.pdf). Auto-detected from URL if not provided.",
      required: !1
    }),
    caption: import_pieces_framework20.Property.ShortText({
      displayName: "Caption",
      description: "Optional caption for the document (max 1024 characters).",
      required: !1
    })
  },
  async run(context) {
    let { session, crmContact, documentUrl, filename, caption } = context.propsValue, apiKey = context.auth.secret_text, preparedUrl = await prepareFile(apiKey, documentUrl, "document"), body = {
      session,
      contact_type: "crm_contact",
      crm_contact_id: crmContact,
      file: preparedUrl,
      caption: caption != null ? caption : ""
    };
    filename && (body.filename = filename);
    let sendResponse = await whatsscaleClient(apiKey, import_pieces_common23.HttpMethod.POST, "/api/sendDocument", body), { jobId } = sendResponse.body;
    return await pollJob(apiKey, jobId);
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/crm/create-crm-contact.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common24 = require("@activepieces/pieces-common");
var createCrmContactAction = (0, import_pieces_framework21.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_create_crm_contact",
  classification: "WRITE",
  displayName: "Create a CRM Contact",
  description: "Add a new contact to your WhatsScale CRM",
  audience: "both",
  aiMetadata: { description: "Create a new WhatsScale CRM contact from a phone number (with country code), optionally setting a name and comma-separated tags. Not idempotent: calling it again may produce a duplicate contact, so look the phone up first with Find a CRM Contact by Phone if you need to avoid duplicates.", idempotent: !1 },
  props: {
    phone: import_pieces_framework21.Property.ShortText({
      displayName: "Phone Number",
      description: "With country code e.g. +31612345678",
      required: !0
    }),
    name: import_pieces_framework21.Property.ShortText({
      displayName: "Name",
      description: "Full name of the contact (e.g. John Smith)",
      required: !1
    }),
    tags: import_pieces_framework21.Property.ShortText({
      displayName: "Tags",
      description: "Comma-separated tags (e.g. vip, customer, lead). Tags are automatically lowercased.",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth.secret_text, { phone, name, tags } = context.propsValue, body = { phone };
    return name && (body.name = name), tags && (body.tags = tags), (await whatsscaleClient(auth, import_pieces_common24.HttpMethod.POST, "/api/crm/contacts", body)).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/crm/get-crm-contact.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common25 = require("@activepieces/pieces-common");
var getCrmContactAction = (0, import_pieces_framework22.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_get_crm_contact",
  classification: "READ",
  displayName: "Get a CRM Contact",
  description: "Retrieve a CRM contact by ID",
  audience: "both",
  aiMetadata: { description: "Fetch a single WhatsScale CRM contact by its contact ID. Read-only; pick this when you already hold the ID (e.g. from List CRM Contacts), versus Find a CRM Contact by Phone when you only have the phone number.", idempotent: !0 },
  props: {
    contactId: whatsscaleProps.crmContact
  },
  async run(context) {
    let auth = context.auth.secret_text, { contactId } = context.propsValue;
    return (await whatsscaleClient(
      auth,
      import_pieces_common25.HttpMethod.GET,
      `/api/crm/contacts/${contactId}`,
      void 0
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/crm/find-crm-contact-by-phone.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_common26 = require("@activepieces/pieces-common");
var findCrmContactByPhoneAction = (0, import_pieces_framework23.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_find_crm_contact_by_phone",
  classification: "READ",
  displayName: "Find a CRM Contact by Phone",
  description: "Look up a CRM contact using their phone number",
  audience: "both",
  aiMetadata: { description: "Fetch a single WhatsScale CRM contact by exact phone number (include country code). Read-only lookup; pick this when you already have the phone and want that one contact, versus List CRM Contacts for browsing/filtering or searching by name. Returns no match if the number is not in the CRM.", idempotent: !0 },
  props: {
    phone: import_pieces_framework23.Property.ShortText({
      displayName: "Phone Number",
      required: !0
    })
  },
  async run(context) {
    let auth = context.auth.secret_text, { phone } = context.propsValue, encodedPhone = encodeURIComponent(phone);
    return (await whatsscaleClient(
      auth,
      import_pieces_common26.HttpMethod.GET,
      `/api/crm/contacts/phone/${encodedPhone}`,
      void 0
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/crm/update-crm-contact.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework"), import_pieces_common27 = require("@activepieces/pieces-common");
var updateCrmContactAction = (0, import_pieces_framework24.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_update_crm_contact",
  classification: "WRITE",
  displayName: "Update a CRM Contact",
  description: "Update the name or tags of an existing CRM contact",
  audience: "both",
  aiMetadata: { description: "Updates the name and/or tags of an existing WhatsScale CRM contact identified by contact ID. Leave a field empty to keep its current value unchanged; tags are replaced wholesale (comma-separated), not merged. Idempotent: re-running with the same values converges the contact to the same state.", idempotent: !0 },
  props: {
    contactId: whatsscaleProps.crmContact,
    name: import_pieces_framework24.Property.ShortText({
      displayName: "Name",
      description: "Leave empty to keep the current name unchanged. Set to a blank value to clear it.",
      required: !1
    }),
    tags: import_pieces_framework24.Property.ShortText({
      displayName: "Tags",
      description: "Replaces all existing tags. Comma-separated (e.g. vip, lead). Leave empty to keep current tags unchanged.",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth.secret_text, { contactId, name, tags } = context.propsValue, body = {};
    return name != null && (body.name = name), tags != null && (body.tags = tags), (await whatsscaleClient(
      auth,
      import_pieces_common27.HttpMethod.PATCH,
      `/api/crm/contacts/${contactId}`,
      body
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/crm/delete-crm-contact.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework"), import_pieces_common28 = require("@activepieces/pieces-common");
var deleteCrmContactAction = (0, import_pieces_framework25.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_delete_crm_contact",
  classification: "DESTRUCTIVE",
  displayName: "Delete a CRM Contact",
  description: "Permanently delete a contact from your WhatsScale CRM",
  audience: "both",
  aiMetadata: { description: "Permanently remove a WhatsScale CRM contact by its contact ID. Destructive and irreversible, but idempotent: re-running converges on the contact being absent. Confirm the ID (via the list or lookup actions) before deleting.", idempotent: !0 },
  props: {
    contactId: whatsscaleProps.crmContact
  },
  async run(context) {
    let auth = context.auth.secret_text, { contactId } = context.propsValue;
    return (await whatsscaleClient(
      auth,
      import_pieces_common28.HttpMethod.DELETE,
      `/api/crm/contacts/${contactId}`,
      void 0
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/crm/add-crm-contact-tag.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework"), import_pieces_common29 = require("@activepieces/pieces-common");
var addCrmContactTagAction = (0, import_pieces_framework26.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_add_crm_contact_tag",
  classification: "WRITE",
  displayName: "Add a Tag to a CRM Contact",
  description: "Add a single tag to an existing CRM contact",
  audience: "both",
  aiMetadata: { description: "Attach one tag to an existing WhatsScale CRM contact identified by contact ID (tags are lowercased automatically). Adds a single tag per call; use Remove a Tag from a CRM Contact to reverse it. Requires a valid contact ID, which you can obtain via the list or lookup actions.", idempotent: !1 },
  props: {
    contactId: whatsscaleProps.crmContact,
    tag: import_pieces_framework26.Property.ShortText({
      displayName: "Tag",
      description: "Tag to add to the contact (e.g. vip). Tags are automatically lowercased.",
      required: !0
    })
  },
  async run(context) {
    let auth = context.auth.secret_text, { contactId, tag } = context.propsValue;
    return (await whatsscaleClient(
      auth,
      import_pieces_common29.HttpMethod.POST,
      `/api/crm/contacts/${contactId}/tags`,
      { tag }
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/crm/remove-crm-contact-tag.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework"), import_pieces_common30 = require("@activepieces/pieces-common");
var removeCrmContactTagAction = (0, import_pieces_framework27.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_remove_crm_contact_tag",
  classification: "WRITE",
  displayName: "Remove a Tag from a CRM Contact",
  description: "Remove a single tag from a CRM contact",
  audience: "both",
  aiMetadata: { description: "Detach one tag from a WhatsScale CRM contact identified by contact ID. Reverses Add a Tag to a CRM Contact; idempotent since the end state is the tag being absent. The contact itself is left intact.", idempotent: !0 },
  props: {
    contactId: whatsscaleProps.crmContact,
    tag: whatsscaleProps.crmTag
  },
  async run(context) {
    let auth = context.auth.secret_text, { contactId, tag } = context.propsValue, encodedTag = encodeURIComponent(tag);
    return (await whatsscaleClient(
      auth,
      import_pieces_common30.HttpMethod.DELETE,
      `/api/crm/contacts/${contactId}/tags/${encodedTag}`,
      void 0
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/crm/list-crm-contacts.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework"), import_pieces_common31 = require("@activepieces/pieces-common");
var listCrmContactsAction = (0, import_pieces_framework28.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_list_crm_contacts",
  classification: "SEARCH",
  displayName: "List CRM Contacts",
  description: "Retrieve a paginated list of CRM contacts with optional filters",
  audience: "both",
  aiMetadata: { description: "Browse WhatsScale CRM contacts as a paginated list, optionally filtered by tag or a free-text search over name/phone. Read-only; use this to discover or enumerate contacts when you do not have an exact phone (use Find a CRM Contact by Phone) or contact ID (use Get a CRM Contact). Control volume with limit/page.", idempotent: !0 },
  props: {
    tag: import_pieces_framework28.Property.Dropdown({
      auth: whatsscaleAuth,
      displayName: "Filter by Tag",
      description: "Optional. Filter contacts by tag.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account"
          };
        try {
          let tags = (await whatsscaleClient(
            auth.secret_text,
            import_pieces_common31.HttpMethod.GET,
            "/make/crm/tags",
            void 0
          )).body;
          return !tags || tags.length === 0 ? {
            disabled: !0,
            options: [],
            placeholder: "No tags found"
          } : { disabled: !1, options: tags };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load tags"
          };
        }
      }
    }),
    search: import_pieces_framework28.Property.ShortText({
      displayName: "Search",
      description: "Optional. Search contacts by name or phone number.",
      required: !1
    }),
    limit: import_pieces_framework28.Property.Number({
      displayName: "Limit",
      description: "Max results to return. Default 50.",
      required: !1
    }),
    page: import_pieces_framework28.Property.Number({
      displayName: "Page",
      description: "Page number. Default 1.",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth.secret_text, { tag, limit, page } = context.propsValue, search = context.propsValue.search, qp = {};
    tag && (qp.tag = tag), search && (qp.search = search), limit != null && (qp.limit = String(limit)), page != null && (qp.page = String(page));
    let params = Object.keys(qp).length > 0 ? qp : void 0;
    return (await whatsscaleClient(
      auth,
      import_pieces_common31.HttpMethod.GET,
      "/api/crm/contacts",
      void 0,
      params
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/actions/utility/check-whatsapp.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework"), import_pieces_common32 = require("@activepieces/pieces-common");
var checkWhatsappAction = (0, import_pieces_framework29.createAction)({
  auth: whatsscaleAuth,
  name: "whatsscale_check_whatsapp",
  classification: "READ",
  displayName: "Check WhatsApp Number",
  description: "Check if a phone number has WhatsApp",
  audience: "both",
  aiMetadata: { description: "Checks whether a given phone number is registered on WhatsApp for the selected session. Use to validate or filter recipients before attempting to send any message; accepts the number in nearly any format with or without a country-code prefix. Read-only and idempotent.", idempotent: !0 },
  props: {
    session: whatsscaleProps.session,
    phone: import_pieces_framework29.Property.ShortText({
      displayName: "Phone Number",
      description: "Phone number with country code (e.g. +31612345678 or 31612345678). Any format accepted.",
      required: !0
    })
  },
  async run(context) {
    let auth = context.auth.secret_text;
    return (await whatsscaleClient(
      auth,
      import_pieces_common32.HttpMethod.POST,
      "/make/checkWhatsapp",
      {
        session: context.propsValue.session,
        phone: context.propsValue.phone
      }
    )).body;
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/triggers/watch-incoming-messages.ts
var import_pieces_framework30 = require("@activepieces/pieces-framework"), import_pieces_common33 = require("@activepieces/pieces-common");
var watchIncomingMessagesTrigger = (0, import_pieces_framework30.createTrigger)({
  auth: whatsscaleAuth,
  name: "watch_incoming_messages",
  classification: "READ",
  displayName: "Watch Incoming Messages",
  description: "Triggers when a new 1-on-1 WhatsApp message is received.",
  aiMetadata: {
    description: "Fires when a new direct (one-on-one) WhatsApp message is received on the connected session, excluding group and channel messages. Each event represents a single inbound private message and includes the sender phone number and name, message body, media details, and reply/forward metadata. Use to react to incoming customer or contact messages in private chats."
  },
  type: import_pieces_framework30.TriggerStrategy.WEBHOOK,
  props: {
    session: whatsscaleProps.session
  },
  sampleData: {
    message_id: "3EB0A1B2C3D4E5F6G7H8I9",
    chat_id: "31612345678",
    from_number: "31612345678",
    from_name: "John Doe",
    body: "Hello, I have a question about your product",
    has_media: !1,
    media_type: "text",
    media_url: "",
    media_mimetype: "",
    media_filename: "",
    timestamp: "2026-02-04T10:30:00.000Z",
    is_forwarded: !1,
    quoted_message_id: "",
    quoted_body: "",
    is_reply: !1,
    session_name: "my_session"
  },
  async onEnable(context) {
    let auth = context.auth.secret_text;
    await whatsscaleClient(auth, import_pieces_common33.HttpMethod.POST, "/make/hooks/subscribe", {
      session: context.propsValue.session,
      webhook_url: context.webhookUrl,
      platform: "activepieces"
      // No trigger_type — proxy defaults to '1on1' when absent
    });
  },
  async onDisable(context) {
    let auth = context.auth.secret_text;
    await whatsscaleClient(auth, import_pieces_common33.HttpMethod.POST, "/make/hooks/unsubscribe", {
      webhook_url: context.webhookUrl
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/triggers/watch-group-messages.ts
var import_pieces_framework31 = require("@activepieces/pieces-framework"), import_pieces_common34 = require("@activepieces/pieces-common");
var watchGroupMessagesTrigger = (0, import_pieces_framework31.createTrigger)({
  auth: whatsscaleAuth,
  name: "watch_group_messages",
  classification: "READ",
  displayName: "Watch Group Messages",
  description: "Triggers when a new message is received in any WhatsApp group.",
  aiMetadata: {
    description: "Fires when a new message is received in any WhatsApp group the connected session belongs to. Each event represents a single inbound group message and includes the originating group ID, the sender (participant) ID and name, message body, media details, and reply/forward metadata. Use to react to group activity across all groups rather than a single chosen one."
  },
  type: import_pieces_framework31.TriggerStrategy.WEBHOOK,
  props: {
    session: whatsscaleProps.session
  },
  sampleData: {
    message_id: "3EB0A1B2C3D4E5F6G7H8I9",
    group_id: "120363423663126276@g.us",
    participant_id: "43001330020491@lid",
    participant_name: "Jane Smith",
    body: "Hey everyone, meeting at 3pm today!",
    has_media: !1,
    media_type: "text",
    media_url: "",
    media_mimetype: "",
    media_filename: "",
    timestamp: "2026-02-18T10:30:00.000Z",
    is_forwarded: !1,
    quoted_message_id: "",
    quoted_body: "",
    is_reply: !1,
    from_name: "Jane Smith",
    chat_id: "120363423663126276@g.us",
    session_name: "my_session"
  },
  async onEnable(context) {
    let auth = context.auth.secret_text;
    await whatsscaleClient(auth, import_pieces_common34.HttpMethod.POST, "/make/hooks/subscribe", {
      session: context.propsValue.session,
      webhook_url: context.webhookUrl,
      platform: "activepieces",
      trigger_type: "group"
    });
  },
  async onDisable(context) {
    let auth = context.auth.secret_text;
    await whatsscaleClient(auth, import_pieces_common34.HttpMethod.POST, "/make/hooks/unsubscribe", {
      webhook_url: context.webhookUrl
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/triggers/watch-channel-messages.ts
var import_pieces_framework32 = require("@activepieces/pieces-framework"), import_pieces_common35 = require("@activepieces/pieces-common");
var watchChannelMessagesTrigger = (0, import_pieces_framework32.createTrigger)({
  auth: whatsscaleAuth,
  name: "watch_channel_messages",
  classification: "READ",
  displayName: "Watch Channel Messages",
  description: "Triggers when a new message is posted to any WhatsApp Channel (includes own posts).",
  aiMetadata: {
    description: "Fires when a new message is posted to any WhatsApp Channel the connected session follows, including the session owner's own posts. Each event represents a single channel post and includes the originating channel ID, message body, and media details. Use to monitor channel activity across all followed channels rather than a single chosen one."
  },
  type: import_pieces_framework32.TriggerStrategy.WEBHOOK,
  props: {
    session: whatsscaleProps.session
  },
  sampleData: {
    message_id: "false_123123@newsletter_1111111111111111111111",
    channel_id: "120363401850139775@newsletter",
    body: "New product launch announcement!",
    has_media: !1,
    media_type: "text",
    media_url: "",
    media_mimetype: "",
    media_filename: "",
    timestamp: "2026-02-18T10:30:00.000Z",
    session_name: "my_session"
  },
  async onEnable(context) {
    let auth = context.auth.secret_text;
    await whatsscaleClient(auth, import_pieces_common35.HttpMethod.POST, "/make/hooks/subscribe", {
      session: context.propsValue.session,
      webhook_url: context.webhookUrl,
      platform: "activepieces",
      trigger_type: "channel"
    });
  },
  async onDisable(context) {
    let auth = context.auth.secret_text;
    await whatsscaleClient(auth, import_pieces_common35.HttpMethod.POST, "/make/hooks/unsubscribe", {
      webhook_url: context.webhookUrl
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/triggers/watch-specific-group-messages.ts
var import_pieces_framework33 = require("@activepieces/pieces-framework"), import_pieces_common36 = require("@activepieces/pieces-common");
var watchSpecificGroupMessagesTrigger = (0, import_pieces_framework33.createTrigger)({
  auth: whatsscaleAuth,
  name: "watch_specific_group_messages",
  classification: "READ",
  displayName: "Watch Specific Group Messages",
  description: "Triggers when a new message is received in a specific WhatsApp group.",
  aiMetadata: {
    description: "Fires when a new message is posted in one chosen WhatsApp group (selected by group ID) on the connected session. Each event represents a single inbound group message and includes the message body, sender (participant) ID and name, media details, and reply/forward metadata. Use to react only to a specific group rather than every group the session belongs to."
  },
  type: import_pieces_framework33.TriggerStrategy.WEBHOOK,
  props: {
    session: whatsscaleProps.session,
    group: whatsscaleProps.group
  },
  sampleData: {
    message_id: "3EB0A1B2C3D4E5F6G7H8I9",
    group_id: "120363423663126276@g.us",
    participant_id: "43001330020491@lid",
    participant_name: "Jane Smith",
    body: "Hey everyone, meeting at 3pm today!",
    has_media: !1,
    media_type: "text",
    media_url: "",
    media_mimetype: "",
    media_filename: "",
    timestamp: "2026-02-18T10:30:00.000Z",
    is_forwarded: !1,
    quoted_message_id: "",
    quoted_body: "",
    is_reply: !1,
    from_name: "Jane Smith",
    chat_id: "120363423663126276@g.us",
    session_name: "my_session"
  },
  async onEnable(context) {
    let auth = context.auth.secret_text;
    await whatsscaleClient(auth, import_pieces_common36.HttpMethod.POST, "/make/hooks/subscribe", {
      session: context.propsValue.session,
      webhook_url: context.webhookUrl,
      platform: "activepieces",
      trigger_type: "group",
      filter_id: context.propsValue.group
      // includes @g.us — send as-is
    });
  },
  async onDisable(context) {
    let auth = context.auth.secret_text;
    await whatsscaleClient(auth, import_pieces_common36.HttpMethod.POST, "/make/hooks/unsubscribe", {
      webhook_url: context.webhookUrl
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/whatsscale/src/lib/triggers/watch-specific-channel-messages.ts
var import_pieces_framework34 = require("@activepieces/pieces-framework"), import_pieces_common37 = require("@activepieces/pieces-common");
var watchSpecificChannelMessagesTrigger = (0, import_pieces_framework34.createTrigger)({
  auth: whatsscaleAuth,
  name: "watch_specific_channel_messages",
  classification: "READ",
  displayName: "Watch Specific Channel Messages",
  description: "Triggers when a new message is posted to a specific WhatsApp Channel.",
  aiMetadata: {
    description: "Fires when a new message is posted to one chosen WhatsApp Channel (selected by channel ID) on the connected session. Each event represents a single channel post and includes the channel ID, message body, and media details. Use to monitor a specific channel rather than every channel the session follows."
  },
  type: import_pieces_framework34.TriggerStrategy.WEBHOOK,
  props: {
    session: whatsscaleProps.session,
    channel: whatsscaleProps.channel
  },
  sampleData: {
    message_id: "false_123123@newsletter_1111111111111111111111",
    channel_id: "120363401850139775@newsletter",
    body: "New product launch announcement!",
    has_media: !1,
    media_type: "text",
    media_url: "",
    media_mimetype: "",
    media_filename: "",
    timestamp: "2026-02-18T10:30:00.000Z",
    session_name: "my_session"
  },
  async onEnable(context) {
    let auth = context.auth.secret_text;
    await whatsscaleClient(auth, import_pieces_common37.HttpMethod.POST, "/make/hooks/subscribe", {
      session: context.propsValue.session,
      webhook_url: context.webhookUrl,
      platform: "activepieces",
      trigger_type: "channel",
      filter_id: context.propsValue.channel
      // includes @newsletter — send as-is
    });
  },
  async onDisable(context) {
    let auth = context.auth.secret_text;
    await whatsscaleClient(auth, import_pieces_common37.HttpMethod.POST, "/make/hooks/unsubscribe", {
      webhook_url: context.webhookUrl
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/whatsscale/src/index.ts
var import_pieces_common38 = require("@activepieces/pieces-common"), whatsscale = (0, import_pieces_framework35.createPiece)({
  displayName: "WhatsScale",
  auth: whatsscaleAuth,
  minimumSupportedRelease: "0.36.0",
  logoUrl: "https://cdn.activepieces.com/pieces/whatsscale.png",
  authors: ["whatsscale"],
  description: "Send WhatsApp messages, manage contacts, and automate conversations via WAHA",
  categories: [
    import_pieces_framework36.PieceCategory.COMMUNICATION,
    import_pieces_framework36.PieceCategory.MARKETING,
    import_pieces_framework36.PieceCategory.SALES_AND_CRM
  ],
  actions: [
    // Text actions
    sendTextManualAction,
    sendTextToContactAction,
    sendTextToGroupAction,
    sendTextToChannelAction,
    sendTextToCrmContactAction,
    // Image actions
    sendImageToContactAction,
    sendImageToGroupAction,
    sendImageToChannelAction,
    sendImageToCrmContactAction,
    sendImageManualAction,
    // Video actions
    sendVideoToContactAction,
    sendVideoToGroupAction,
    sendVideoToChannelAction,
    sendVideoToCrmContactAction,
    sendVideoManualAction,
    // Document actions
    sendDocumentToContactAction,
    sendDocumentToGroupAction,
    sendDocumentToCrmContactAction,
    // CRM actions
    createCrmContactAction,
    getCrmContactAction,
    findCrmContactByPhoneAction,
    updateCrmContactAction,
    deleteCrmContactAction,
    addCrmContactTagAction,
    removeCrmContactTagAction,
    listCrmContactsAction,
    // Utility actions
    checkWhatsappAction,
    (0, import_pieces_common38.createCustomApiCallAction)({
      auth: whatsscaleAuth,
      baseUrl: () => "https://proxy.whatsscale.com",
      authMapping: async (auth) => ({
        "X-Api-Key": `${auth.secret_text}`
      })
    })
  ],
  triggers: [
    watchChannelMessagesTrigger,
    watchGroupMessagesTrigger,
    watchIncomingMessagesTrigger,
    watchSpecificGroupMessagesTrigger,
    watchSpecificChannelMessagesTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  whatsscale
});
