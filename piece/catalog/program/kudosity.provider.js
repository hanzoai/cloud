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

// ../ap/packages/pieces/community/kudosity/src/index.ts
var index_exports = {};
__export(index_exports, {
  kudosity: () => kudosity
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/kudosity/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), kudosityAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Kudosity API Key",
  required: !0
});

// ../ap/packages/pieces/community/kudosity/src/lib/actions/add-update-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/kudosity/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.transmitsms.com";
async function makeRequest(auth, method, path, body) {
  let url = `${BASE_URL}${path}`;
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url,
    headers: {
      "x-api-key": auth,
      "Content-Type": "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/kudosity/src/lib/actions/add-update-contact.ts
var addUpdateContact = (0, import_pieces_framework2.createAction)({
  auth: kudosityAuth,
  name: "addUpdateContact",
  displayName: "Add / Update Contact",
  description: "Create or update a list member in Kudosity",
  audience: "both",
  aiMetadata: {
    description: "Upsert a contact (list member) in a Kudosity recipient list, keyed on the MSISDN phone number \u2014 creates the member if absent or updates their fields if already present. Use to add subscribers or keep contact details in sync. Requires the numeric list ID, the MSISDN (phone with country code), and an email. Safe to repeat: the same input produces the same final record.",
    idempotent: !0
  },
  props: {
    listId: import_pieces_framework2.Property.ShortText({
      displayName: "List ID",
      description: "Numeric ID of the recipient list (found in the list page URL).",
      required: !0
    }),
    msisdn: import_pieces_framework2.Property.ShortText({
      displayName: "MSISDN",
      description: "Phone number with country code, e.g., +1234567890",
      required: !0
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      required: !0
    }),
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First name",
      required: !1
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last name",
      required: !1
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    customFields: import_pieces_framework2.Property.Json({
      displayName: "Custom fields",
      required: !1
    })
  },
  async run(context) {
    let payload = {
      list_id: context.propsValue.listId,
      msisdn: context.propsValue.msisdn,
      email: context.propsValue.email,
      first_name: context.propsValue.firstName,
      last_name: context.propsValue.lastName,
      phone: context.propsValue.phone,
      custom_fields: context.propsValue.customFields
    };
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/edit-list-member.json",
      payload
    );
  }
});

// ../ap/packages/pieces/community/kudosity/src/lib/actions/delete-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), deleteContact = (0, import_pieces_framework3.createAction)({
  auth: kudosityAuth,
  name: "deleteContact",
  displayName: "Delete Contact",
  description: "Remove a list member from a Kudosity recipient list",
  audience: "both",
  aiMetadata: {
    description: "Remove a contact (list member), identified by MSISDN, from Kudosity recipient lists. If a list ID is supplied the contact is removed only from that list; if omitted the removal applies across lists. Use to unsubscribe or clean up contacts. Safe to repeat \u2014 re-running once the contact is gone leaves the same end state.",
    idempotent: !0
  },
  props: {
    listId: import_pieces_framework3.Property.ShortText({
      displayName: "List ID",
      description: "Numeric ID of the recipient list (found in the list page URL).",
      required: !1
    }),
    msisdn: import_pieces_framework3.Property.ShortText({
      displayName: "MSISDN",
      description: "Phone number with country code (e.g., +1234567890)",
      required: !0
    })
  },
  async run(context) {
    let payload = {
      msisdn: context.propsValue.msisdn
    };
    return context.propsValue.listId && (payload.list_id = context.propsValue.listId), await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/delete-from-list.json",
      payload
    );
  }
});

// ../ap/packages/pieces/community/kudosity/src/lib/actions/cancel-sms.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), cancelSms = (0, import_pieces_framework4.createAction)({
  auth: kudosityAuth,
  name: "cancelSms",
  displayName: "Cancel SMS",
  description: "Cancel a scheduled SMS message in Kudosity",
  audience: "both",
  aiMetadata: {
    description: "Cancel a previously scheduled (not-yet-sent) SMS in Kudosity by its numeric message ID. Use to stop a queued message before delivery. Requires the message ID returned when the SMS was scheduled. Safe to repeat \u2014 re-cancelling an already-cancelled message leaves the same end state.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework4.Property.ShortText({
      displayName: "Message ID",
      description: "Numeric ID assigned to the message sent",
      required: !0
    })
  },
  async run(context) {
    let payload = {
      id: context.propsValue.id
    };
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/cancel-sms.json",
      payload
    );
  }
});

// ../ap/packages/pieces/community/kudosity/src/lib/actions/format-number.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), formatNumber = (0, import_pieces_framework5.createAction)({
  auth: kudosityAuth,
  name: "formatNumber",
  displayName: "Format Number",
  description: "Format a phone number to international E.164 format",
  audience: "both",
  aiMetadata: {
    description: "Normalize a phone number into international E.164 format using a supplied country code or name to resolve the local number. Use to validate or standardize numbers before sending or storing them. Requires the country and the number; this is a pure lookup with no side effect and is safe to repeat.",
    idempotent: !0
  },
  props: {
    country: import_pieces_framework5.Property.ShortText({
      displayName: "Country",
      description: "Country code or name (e.g., US, AU, NZ) to determine the number format",
      required: !0
    }),
    number: import_pieces_framework5.Property.ShortText({
      displayName: "Number",
      description: "Phone number in local format (spaces, hyphens, and other characters will be removed)",
      required: !0
    })
  },
  async run(context) {
    let payload = {
      country: context.propsValue.country,
      msisdn: context.propsValue.number
    };
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      "/format-number.json",
      payload
    );
  }
});

// ../ap/packages/pieces/community/kudosity/src/lib/actions/send-sms.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), sendSms = (0, import_pieces_framework6.createAction)({
  auth: kudosityAuth,
  name: "sendSms",
  displayName: "Send SMS",
  description: "Send an SMS message via Kudosity",
  audience: "both",
  aiMetadata: {
    description: "Send an SMS text message to a recipient via Kudosity. Use when an automation needs to deliver an outbound text. Requires a sender (number or alphanumeric ID assigned to the account), a recipient (local or E.164), and the message body; an optional message reference and link tracking can be set. Not idempotent \u2014 each call dispatches a new message.",
    idempotent: !1
  },
  props: {
    sender: import_pieces_framework6.Property.ShortText({
      displayName: "Sender",
      description: "Sender number or alphanumeric sender that is assigned to your account",
      required: !0
    }),
    recipient: import_pieces_framework6.Property.ShortText({
      displayName: "Recipient",
      description: "Destination number (local or E.164 format)",
      required: !0
    }),
    message: import_pieces_framework6.Property.LongText({
      displayName: "Message",
      description: "Body of the SMS message",
      required: !0
    }),
    messageRef: import_pieces_framework6.Property.ShortText({
      displayName: "Message Reference",
      description: "Optional reference string to correlate messages with your system",
      required: !1
    }),
    trackLinks: import_pieces_framework6.Property.Checkbox({
      displayName: "Track Links",
      description: "Enable link tracking for this message",
      required: !1
    })
  },
  async run(context) {
    let payload = {
      sender: context.propsValue.sender,
      recipient: context.propsValue.recipient,
      message: context.propsValue.message
    };
    return context.propsValue.messageRef && (payload.message_ref = context.propsValue.messageRef), context.propsValue.trackLinks !== void 0 && (payload.track_links = context.propsValue.trackLinks), await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: "https://api.transmitmessage.com/v2/sms",
      headers: {
        "x-api-key": `${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: payload
    });
  }
});

// ../ap/packages/pieces/community/kudosity/src/lib/actions/get-sms-info.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), getSmsInfo = (0, import_pieces_framework7.createAction)({
  auth: kudosityAuth,
  name: "getSmsInfo",
  displayName: "Get SMS info",
  description: "Retrieve details for a sent SMS by ID",
  audience: "both",
  aiMetadata: {
    description: "Look up the details and delivery status of a previously sent SMS by its message ID (the UUID returned when the message was sent). Use to check status or fetch metadata for a known message. Read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework7.Property.ShortText({
      displayName: "Message ID",
      description: "The message ID to retrieve (UUID returned when sending)",
      required: !0
    })
  },
  async run(context) {
    let id = context.propsValue.id;
    return await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `https://api.transmitmessage.com/v2/sms/${id}`,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      }
    });
  }
});

// ../ap/packages/pieces/community/kudosity/src/lib/triggers/link-hit.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), linkHit = (0, import_pieces_framework8.createTrigger)({
  auth: kudosityAuth,
  name: "linkHit",
  displayName: "Link Hit",
  description: "Triggered when a tracked link in a message is clicked",
  aiMetadata: {
    description: "Fires when a recipient clicks a tracked link in a Kudosity message (a LINK_HIT event), signaling link engagement for an outbound SMS."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhookUrl = context.webhookUrl, apiKey = context.auth.secret_text, webhookId = (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.POST,
      url: "https://api.transmitmessage.com/v2/webhook",
      headers: {
        "x-api-key": apiKey,
        "Content-Type": "application/json"
      },
      body: {
        name: "ActivePieces Link Hit Webhook",
        filter: {
          event_type: ["LINK_HIT"]
        },
        url: webhookUrl
      }
    })).body.id;
    await context.store.put("webhookId", webhookId);
  },
  async onDisable(context) {
    let apiKey = context.auth.secret_text, storedWebhookId = await context.store.get("webhookId");
    storedWebhookId && await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.DELETE,
      url: `https://api.transmitmessage.com/v2/webhook/${storedWebhookId}`,
      headers: {
        "x-api-key": apiKey,
        "Content-Type": "application/json"
      }
    }), await context.store.delete("webhookId");
  },
  async run(context) {
    return context.payload.body.event_type !== "LINK_HIT" ? [] : [context.payload.body];
  }
});

// ../ap/packages/pieces/community/kudosity/src/lib/triggers/sms-received.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), smsReceived = (0, import_pieces_framework9.createTrigger)({
  auth: kudosityAuth,
  name: "smsReceived",
  displayName: "SMS received",
  description: "Triggered when an inbound SMS is received",
  aiMetadata: {
    description: "Fires when an inbound SMS is received on a Kudosity number (an SMS_INBOUND event), representing a reply or incoming message from a recipient."
  },
  props: {},
  sampleData: {
    event_type: "SMS_INBOUND",
    timestamp: "2021-05-06T05:16:33.345678Z",
    webhook_id: "fd0e6485-b905-44c1-bd55-fee1d0d6d864",
    webhook_name: "SMS Inbound Webhook",
    mo: {
      type: "SMS",
      id: "alss-2way-605b31c7-d2c49104",
      message: "Stop",
      recipient: "61481074190",
      routed_via: "447507333300",
      sender: "447507222200",
      last_message: {
        type: "SMS",
        id: "a51ebe4e-a412-440e-a8d9-464e68a521cc",
        message: "Hey, check this out!",
        message_ref: "ncc5009d",
        recipient: "447507222200",
        routed_via: "447507333300",
        sender: "61481074190"
      }
    }
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhookUrl = context.webhookUrl, apiKey = context.auth.secret_text, webhookId = (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.POST,
      url: "https://api.transmitmessage.com/v2/webhook",
      headers: {
        "x-api-key": apiKey,
        "Content-Type": "application/json"
      },
      body: {
        name: "ActivePieces SMS Inbound Webhook",
        filter: {
          event_type: ["SMS_INBOUND"]
        },
        url: webhookUrl
      }
    })).body.id;
    await context.store.put("webhookId", webhookId);
  },
  async onDisable(context) {
    let apiKey = context.auth.secret_text, storedWebhookId = await context.store.get("webhookId");
    storedWebhookId && await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.DELETE,
      url: `https://api.transmitmessage.com/v2/webhook/${storedWebhookId}`,
      headers: {
        "x-api-key": apiKey,
        "Content-Type": "application/json"
      }
    }), await context.store.delete("webhookId");
  },
  async run(context) {
    return context.payload.body.event_type !== "SMS_INBOUND" ? [] : [context.payload.body];
  }
});

// ../ap/packages/pieces/community/kudosity/src/lib/triggers/sms-sent.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common"), smsSent = (0, import_pieces_framework10.createTrigger)({
  auth: kudosityAuth,
  name: "smsSent",
  displayName: "SMS Sent",
  description: "Triggered when an SMS sent",
  aiMetadata: {
    description: "Fires when an outbound SMS reaches the SENT status in Kudosity (an SMS_STATUS event with status SENT), confirming a message left the platform toward the carrier."
  },
  props: {},
  sampleData: {
    event_type: "SMS_STATUS",
    timestamp: "2025-04-23T00:05:18.123456Z",
    webhook_id: "fd0e6485-b905-44c1-bd55-fee1d0d6d864",
    webhook_name: "SMS Status Webhook",
    status: {
      id: "e48d3e71-264e-4afb-9c06-edec1f0bb9d4",
      type: "SMS",
      message_ref: "test V2 QA SMS Send",
      sender: "447507222200",
      routed_via: "6140000000",
      recipient: "6140000000",
      status: "SENT"
    }
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhookUrl = context.webhookUrl, apiKey = context.auth.secret_text, webhookId = (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.POST,
      url: "https://api.transmitmessage.com/v2/webhook",
      headers: {
        "x-api-key": apiKey,
        "Content-Type": "application/json"
      },
      body: {
        name: "ActivePieces SMS Sent Webhook",
        filter: {
          event_type: ["SMS_STATUS"],
          status: ["SENT"]
        },
        url: webhookUrl
      }
    })).body.id;
    await context.store.put("webhookId", webhookId);
  },
  async onDisable(context) {
    let apiKey = context.auth.secret_text, storedWebhookId = await context.store.get("webhookId");
    storedWebhookId && await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.DELETE,
      url: `https://api.transmitmessage.com/v2/webhook/${storedWebhookId}`,
      headers: {
        "x-api-key": apiKey,
        "Content-Type": "application/json"
      }
    }), await context.store.delete("webhookId");
  },
  async run(context) {
    let body = context.payload.body;
    return body.event_type !== "SMS_STATUS" && body.status.status !== "SENT" ? [] : [context.payload.body];
  }
});

// ../ap/packages/pieces/community/kudosity/src/index.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework"), kudosity = (0, import_pieces_framework11.createPiece)({
  displayName: "Kudosity",
  auth: kudosityAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/kudosity.png",
  authors: ["sanket-a11y"],
  description: "Kudosity is a cloud-based SMS platform that enables businesses to send and receive text messages globally.",
  categories: [import_pieces_framework12.PieceCategory.COMMUNICATION],
  actions: [
    addUpdateContact,
    deleteContact,
    cancelSms,
    formatNumber,
    sendSms,
    getSmsInfo,
    (0, import_pieces_common11.createCustomApiCallAction)({
      auth: kudosityAuth,
      baseUrl: () => "https://api.transmitsms.com",
      authMapping: async (auth) => ({
        "x-api-key": `${auth.secret_text}`
      })
    })
  ],
  triggers: [linkHit, smsReceived, smsSent]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  kudosity
});
