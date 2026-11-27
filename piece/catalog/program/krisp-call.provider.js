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

// ../ap/packages/pieces/community/krisp-call/src/index.ts
var index_exports = {};
__export(index_exports, {
  KrispCall: () => KrispCall
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/krisp-call/src/lib/actions/add-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/krisp-call/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), krispcallAuth = import_pieces_framework.PieceAuth.CustomAuth({
  props: {
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API key",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://app.krispcall.com/api/v3/platform/activepiece/me",
        headers: {
          "X-API-KEY": auth.apiKey
        }
      }), { valid: !0 };
    } catch (error) {
      return { valid: !1, error: error.message };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/krisp-call/src/lib/actions/add-contact.ts
var addContact = (0, import_pieces_framework2.createAction)({
  name: "addContact",
  displayName: "Add Contact",
  auth: krispcallAuth,
  description: "Add contact in Krispcall",
  audience: "both",
  aiMetadata: {
    description: "Create a contact in the connected KrispCall account, keyed by phone number. Use when an agent needs to register a new contact (name, company, email, address are optional; only the contact number is required). Not idempotent \u2014 calling it again with the same number creates or re-submits another contact entry.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      description: "Enter your name",
      required: !1
    }),
    number: import_pieces_framework2.Property.ShortText({
      displayName: "Contact number",
      description: "Enter contact number",
      required: !0
    }),
    address: import_pieces_framework2.Property.ShortText({
      displayName: "Address",
      description: "Enter your address",
      required: !1
    }),
    company: import_pieces_framework2.Property.ShortText({
      displayName: "Company",
      description: "Enter your company",
      required: !1
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "Enter your email",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://app.krispcall.com/api/v3/platform/activepiece/add-contact",
      headers: {
        "X-API-KEY": auth.props.apiKey
      },
      body: {
        name: propsValue.name,
        number: propsValue.number,
        company: propsValue.company,
        email: propsValue.email,
        address: propsValue.address
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/krisp-call/src/lib/actions/delete-contacts.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var deleteContacts = (0, import_pieces_framework3.createAction)({
  name: "deleteContacts",
  displayName: "Delete Contacts",
  auth: krispcallAuth,
  description: "Delete contacts from krispcall.",
  audience: "both",
  aiMetadata: {
    description: "Delete one or more contacts from the connected KrispCall account, given a list of contacts to remove. Use when an agent needs to clean up or remove KrispCall contacts. Idempotent in effect \u2014 re-running after the contacts are gone leaves the account in the same state.",
    idempotent: !0
  },
  props: {
    contacts: import_pieces_framework3.Property.Array({
      displayName: "Contacts",
      description: "Enter contact which you want to delete.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.DELETE,
      url: "https://app.krispcall.com/api/v3/platform/activepiece/delete-contacts",
      headers: {
        "X-API-KEY": auth.props.apiKey
      },
      body: {
        contacts: propsValue.contacts
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/krisp-call/src/lib/actions/send-sms.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var import_pieces_framework5 = require("@activepieces/pieces-framework"), sendSms = (0, import_pieces_framework4.createAction)({
  name: "sendSms",
  displayName: "Send SMS",
  auth: krispcallAuth,
  description: "Send sms in Krispcall.",
  audience: "both",
  aiMetadata: {
    description: "Send a text-only SMS message from one of the connected KrispCall account numbers to a destination phone number. Use when an agent needs to send a plain SMS; the from number must be one provisioned on the account and the message content is required. Not idempotent \u2014 each call dispatches a new message.",
    idempotent: !1
  },
  props: {
    from_number: import_pieces_framework5.Property.Dropdown({
      displayName: "From Number",
      description: "Select an Number",
      required: !0,
      refreshers: ["auth"],
      refreshOnSearch: !1,
      auth: krispcallAuth,
      options: async ({ auth }) => {
        var _a;
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          let authVaue = auth.props, res = await import_pieces_common4.httpClient.sendRequest({
            method: import_pieces_common4.HttpMethod.GET,
            url: "https://app.krispcall.com/api/v3/platform/activepiece/get-numbers",
            headers: {
              "X-API-KEY": authVaue.apiKey
            }
          });
          return {
            disabled: !1,
            options: (_a = res == null ? void 0 : res.body) == null ? void 0 : _a.map((item) => ({
              label: item.name,
              value: item.number
            }))
          };
        } catch (error) {
          return console.error(error), { disabled: !0, options: [] };
        }
      }
    }),
    to_number: import_pieces_framework5.Property.ShortText({
      displayName: "To Number",
      description: "Enter the number to which you want to send sms.",
      required: !0
    }),
    content: import_pieces_framework5.Property.ShortText({
      displayName: "content",
      description: "Enter your message here.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: "https://app.krispcall.com/api/v3/platform/activepiece/send-sms",
      headers: {
        "X-API-KEY": auth.props.apiKey
      },
      body: {
        from_number: propsValue.from_number,
        to_number: propsValue.to_number,
        content: propsValue.content
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/krisp-call/src/lib/actions/send-mms.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var import_pieces_framework7 = require("@activepieces/pieces-framework"), sendMms = (0, import_pieces_framework6.createAction)({
  name: "sendMms",
  displayName: "Send MMS",
  auth: krispcallAuth,
  description: "Send mms in krispcall.",
  audience: "both",
  aiMetadata: {
    description: "Send an MMS message carrying one or more media URLs from a connected KrispCall account number to a destination phone number, with optional text content. Use when an agent needs to send media (images/files) rather than a plain SMS; the from number must be provisioned on the account and at least one media URL is required. Not idempotent \u2014 each call dispatches a new message.",
    idempotent: !1
  },
  props: {
    from_number: import_pieces_framework7.Property.Dropdown({
      auth: krispcallAuth,
      displayName: "from number",
      description: "Select an number",
      required: !0,
      refreshers: ["auth"],
      refreshOnSearch: !1,
      options: async ({ auth }) => {
        var _a;
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          let authVaue = auth.props, res = await import_pieces_common5.httpClient.sendRequest({
            method: import_pieces_common5.HttpMethod.GET,
            url: "https://app.krispcall.com/api/v3/platform/activepiece/get-numbers",
            headers: {
              "X-API-KEY": authVaue.apiKey
            }
          });
          return {
            disabled: !1,
            options: (_a = res == null ? void 0 : res.body) == null ? void 0 : _a.map((item) => ({
              label: item.name,
              value: item.number
            }))
          };
        } catch (error) {
          return console.error(error), { disabled: !0, options: [] };
        }
      }
    }),
    to_number: import_pieces_framework7.Property.ShortText({
      displayName: "To Number",
      description: "Enter the number to which you want to send sms.",
      required: !0
    }),
    content: import_pieces_framework7.Property.ShortText({
      displayName: "content",
      description: "Enter your message here.",
      required: !1
    }),
    medias: import_pieces_framework7.Property.Array({
      displayName: "Medias url",
      description: "Enter medias urls",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: "https://app.krispcall.com/api/v3/platform/activepiece/send-mms",
      headers: {
        "X-API-KEY": auth.props.apiKey
      },
      body: {
        from_number: propsValue.from_number,
        to_number: propsValue.to_number,
        content: propsValue.content,
        medias: propsValue.medias
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/krisp-call/src/lib/triggers/index.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), triggers = [
  {
    name: "newVoicemail",
    displayName: "New Voicemail",
    description: "Trigger when a new voicemail is received.",
    aiMetadata: {
      description: "Fires when a new voicemail is left on a connected KrispCall number, representing a recorded caller message (with caller number, duration, call time, and audio link)."
    },
    action: "new_voicemail",
    sampleData: {
      id: "",
      from: "+9779821110987",
      duration: "5 seconds",
      call_time: "2000-10-31T01:30:00.000-05:00",
      voicemail_audio: "voicemail.mp4"
    }
  },
  {
    name: "newMms",
    displayName: "New MMS/SMS",
    description: "Trigger when a new MMS/SMS is received.",
    aiMetadata: {
      description: "Fires when an inbound SMS or MMS is received on a connected KrispCall number, representing a message from an external sender (with from/to numbers, text content, and any media link)."
    },
    action: "new_sms_or_mms",
    sampleData: {
      id: "YiW2nyxqtJPYqkRKbrcJQ7",
      from_number: "+16466813538",
      to_number: "+12517327005",
      content: "Last testing",
      media_link: "https://api.twilio.com/2010-04-01/Accounts/LINK/Media/SOMETHING"
    }
  },
  {
    name: "newContact",
    displayName: "New Contact",
    description: "Trigger when a new contact is added.",
    aiMetadata: {
      description: "Fires when a new contact is added to the connected KrispCall account, representing a newly created contact record (with name, contact number, email, company, and address)."
    },
    action: "new_contact",
    sampleData: {
      id: "1",
      email: "john@example.com",
      company: "KrispCall",
      address: "Singapore",
      name: "John Smith",
      contactNumber: "+9779834509123"
    }
  },
  {
    name: "newCallLog",
    displayName: "New Call Log",
    description: "Trigger when a new call log is recorded.",
    aiMetadata: {
      description: "Fires when a new call log entry is recorded on the connected KrispCall account, representing a completed call (inbound or outbound) with from/to numbers, direction, duration, outcome, and recording link."
    },
    action: "new_call_log",
    sampleData: {
      id: "101",
      callFrom: "+11234567890",
      callTo: "+11234567891",
      direction: "Outgoing",
      duration: "0hr 01min 30sec",
      outcome: "Completed",
      callRecording: "http://example.com/recording.mp3"
    }
  },
  {
    name: "OutboundSMS/MMS",
    displayName: "Outbound MMS/SMS",
    description: "Trigger when a new MMS/SMS is sent.",
    aiMetadata: {
      description: "Fires when an outbound SMS or MMS is sent from a connected KrispCall number, representing a message dispatched to an external recipient (with from/to numbers, text content, and any media link)."
    },
    action: "outbound_sms_or_mms",
    sampleData: {
      id: "YiW2nyxqtJPYqkRKbrcJQ7",
      from_number: "+16466813538",
      to_number: "+12517327005",
      content: "Last testing",
      media_link: "https://api.twilio.com/2010-04-01/Accounts/LINK/Media/SOMETHING"
    }
  }
].map((trigger) => (0, import_pieces_framework8.createTrigger)({
  name: trigger.name,
  displayName: trigger.displayName,
  auth: krispcallAuth,
  description: trigger.description,
  aiMetadata: trigger.aiMetadata,
  props: {},
  sampleData: trigger.sampleData,
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: "https://app.krispcall.com/api/v3/platform/activepiece/subscribe",
      body: {
        hookUrl: context.webhookUrl,
        action: trigger.action
      },
      headers: {
        "X-API-KEY": context.auth.props.apiKey
      }
    });
    await context.store.put("_webhook_id", response.body.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("_webhook_id");
    await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.DELETE,
      url: "https://app.krispcall.com/api/v3/platform/activepiece/unsubscribe",
      body: {
        hookUrl: webhookId
      },
      headers: {
        "X-API-KEY": context.auth.props.apiKey
      }
    });
  },
  async run(context) {
    return [context.payload.body];
  }
}));

// ../ap/packages/pieces/community/krisp-call/src/index.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var KrispCall = (0, import_pieces_framework9.createPiece)({
  displayName: "KrispCall",
  description: "KrispCall is a cloud telephony system for modern businesses, offering advanced features for high-growth startups and modern enterprises.",
  categories: [import_pieces_framework10.PieceCategory.COMMUNICATION],
  auth: krispcallAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/krispcall.svg",
  authors: ["deependra321"],
  actions: [addContact, deleteContacts, sendSms, sendMms],
  triggers
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  KrispCall
});
