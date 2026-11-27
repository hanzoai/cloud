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

// ../ap/packages/pieces/community/vero/src/index.ts
var index_exports = {};
__export(index_exports, {
  vero: () => vero
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_framework21 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vero/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), veroAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Auth Token",
  description: "Vero auth token",
  required: !0
});

// ../ap/packages/pieces/community/vero/src/lib/actions/alias-a-user.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vero/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.getvero.com/api/v2";
async function makeRequest(auth_token, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}?auth_token=${auth_token}`,
      headers: {
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/vero/src/lib/actions/alias-a-user.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), aliasAUser = (0, import_pieces_framework2.createAction)({
  auth: veroAuth,
  name: "aliasAUser",
  displayName: "Alias a User",
  description: "Change a user's identifier by merging two user identities into one",
  audience: "both",
  aiMetadata: {
    description: "Re-identifies a Vero user by remapping an existing record from an old user ID to a new user ID, merging the two identities. Use when a user's stable identifier has changed and their history must be preserved under the new ID. Both the old and new IDs are required; re-running with the same pair is effectively idempotent.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework2.Property.ShortText({
      displayName: "Old User ID",
      description: "The old unique identifier of the user",
      required: !0
    }),
    new_id: import_pieces_framework2.Property.ShortText({
      displayName: "New User ID",
      description: "The new unique identifier of the user",
      required: !0
    })
  },
  async run(context) {
    let { id, new_id } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.PUT,
      "/users/reidentify",
      {
        id,
        new_id
      }
    );
  }
});

// ../ap/packages/pieces/community/vero/src/lib/actions/create-or-update-user.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), createOrUpdateUser = (0, import_pieces_framework3.createAction)({
  auth: veroAuth,
  name: "createOrUpdateUser",
  displayName: "Create or Update User",
  description: "Create a new user profile or update an existing user profile with custom properties",
  audience: "both",
  aiMetadata: {
    description: "Upserts a Vero user profile keyed on the supplied user ID, setting the email and any custom properties and communication channels (e.g. push device tokens). Use to register a new contact or to enrich/refresh an existing one. ID and email are required; because it is keyed on the stable ID, repeating the same call is idempotent.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework3.Property.ShortText({
      displayName: "User ID",
      description: "The unique identifier of the user",
      required: !0
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "The email address of the user",
      required: !0
    }),
    data: import_pieces_framework3.Property.Object({
      displayName: "User Data",
      description: "Custom user properties (e.g., first_name, last_name, timezone). Reserved properties: language, timezone, userAgent",
      required: !1
    }),
    channels: import_pieces_framework3.Property.Array({
      displayName: "Channels",
      description: "Device tokens for push notifications and other communication channels",
      required: !1,
      properties: {
        type: import_pieces_framework3.Property.ShortText({
          displayName: "Channel",
          description: "The channel identifier (e.g., push)",
          required: !0
        }),
        address: import_pieces_framework3.Property.ShortText({
          displayName: "Address",
          description: "The address for the channel (e.g., UNIQUE_DEVICE_TOKEN)",
          required: !0
        }),
        platform: import_pieces_framework3.Property.ShortText({
          displayName: "Platform",
          description: "The platform for push notifications (e.g., iOS, Android)",
          required: !1
        })
      }
    })
  },
  async run(context) {
    let { id, email, data, channels } = context.propsValue, payload = {
      id,
      email
    };
    return data && (payload.data = data), channels && channels.length > 0 && (payload.channels = channels), await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/users/track",
      payload
    );
  }
});

// ../ap/packages/pieces/community/vero/src/lib/actions/delete-user.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), deleteUser = (0, import_pieces_framework4.createAction)({
  auth: veroAuth,
  name: "deleteUser",
  displayName: "Delete User",
  description: "Delete a user and all their properties and activities permanently. This action is irreversible.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a Vero user, identified by user ID, along with all of their properties and activity history. This is irreversible. Re-running on an already-deleted ID has no further effect, so it is effectively idempotent on the same target.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework4.Property.ShortText({
      displayName: "User ID",
      description: "The unique identifier of the user to delete",
      required: !0
    })
  },
  async run(context) {
    let { id } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/users/delete",
      {
        id
      }
    );
  }
});

// ../ap/packages/pieces/community/vero/src/lib/actions/resubscribe-user.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), resubscribeUser = (0, import_pieces_framework5.createAction)({
  auth: veroAuth,
  name: "resubscribeUser",
  displayName: "Resubscribe User",
  description: "Globally resubscribe a user to all communications",
  audience: "both",
  aiMetadata: {
    description: "Globally resubscribes a Vero user, identified by user ID, restoring their consent to receive all communications. Use to reverse a prior unsubscribe. Repeating the call leaves the user subscribed, so it is idempotent.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework5.Property.ShortText({
      displayName: "User ID",
      description: "The unique identifier of the user",
      required: !0
    })
  },
  async run(context) {
    let { id } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      "/users/resubscribe",
      {
        id
      }
    );
  }
});

// ../ap/packages/pieces/community/vero/src/lib/actions/update-users-tags.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), updateUsersTags = (0, import_pieces_framework6.createAction)({
  auth: veroAuth,
  name: "updateUsersTags",
  displayName: "Update User Tags",
  description: "Add or remove tags from a user profile",
  audience: "both",
  aiMetadata: {
    description: "Adds and/or removes tags on a Vero user profile identified by user ID, supplying lists of tags to add, remove, or both. Use to segment or label a user. Applying the same add/remove sets converges to the same tag state, so it is idempotent.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework6.Property.ShortText({
      displayName: "User ID",
      description: "The unique identifier of the user",
      required: !0
    }),
    add: import_pieces_framework6.Property.Array({
      displayName: "Tags to Add",
      description: "An array of tags to add to the user",
      required: !1
    }),
    remove: import_pieces_framework6.Property.Array({
      displayName: "Tags to Remove",
      description: "An array of tags to remove from the user",
      required: !1
    })
  },
  async run(context) {
    let { id, add, remove } = context.propsValue, payload = {
      id
    };
    return add && add.length > 0 && (payload.add = add), remove && remove.length > 0 && (payload.remove = remove), await makeRequest(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.PUT,
      "/users/tags/edit",
      payload
    );
  }
});

// ../ap/packages/pieces/community/vero/src/lib/actions/unsubscribe.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), unsubscribe = (0, import_pieces_framework7.createAction)({
  auth: veroAuth,
  name: "unsubscribe",
  displayName: "Unsubscribe",
  description: "Globally unsubscribe a user from all communications",
  audience: "both",
  aiMetadata: {
    description: "Globally unsubscribes a Vero user, identified by user ID, withdrawing their consent so they no longer receive any communications. Use to honor an opt-out. Repeating the call leaves the user unsubscribed, so it is idempotent.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework7.Property.ShortText({
      displayName: "User ID",
      description: "The unique identifier of the user",
      required: !0
    })
  },
  async run(context) {
    let { id } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common7.HttpMethod.POST,
      "/users/unsubscribe",
      {
        id
      }
    );
  }
});

// ../ap/packages/pieces/community/vero/src/lib/actions/track-event.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), trackEvent = (0, import_pieces_framework8.createAction)({
  auth: veroAuth,
  name: "trackEvent",
  displayName: "Track Event",
  description: "Track a custom event for a user",
  audience: "both",
  aiMetadata: {
    description: "Records a custom analytics event in Vero for a user (identified by user ID and email), with an event name and optional event data and metadata. Use to log a user action that can drive campaigns or segmentation. Each call appends a new event occurrence, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    userId: import_pieces_framework8.Property.ShortText({
      displayName: "User ID",
      description: "Unique database identifier for the user",
      required: !0
    }),
    userEmail: import_pieces_framework8.Property.ShortText({
      displayName: "User Email",
      description: "Email address of the user",
      required: !0
    }),
    eventName: import_pieces_framework8.Property.ShortText({
      displayName: "Event Name",
      description: "Name of the event being tracked",
      required: !0
    }),
    eventData: import_pieces_framework8.Property.Object({
      displayName: "Event Data",
      description: "Event-specific data and properties",
      required: !1
    }),
    extras: import_pieces_framework8.Property.Object({
      displayName: "Extras",
      description: "Additional metadata for the event (e.g., source, createdAt)",
      required: !1
    })
  },
  async run(context) {
    let { userId, userEmail, eventName, eventData, extras } = context.propsValue, payload = {
      user: {
        id: userId,
        email: userEmail
      },
      eventName
    };
    return eventData && (payload.data = eventData), extras && (payload.extras = extras), await makeRequest(
      context.auth.secret_text,
      import_pieces_common8.HttpMethod.POST,
      "/events/track",
      payload
    );
  }
});

// ../ap/packages/pieces/community/vero/src/lib/triggers/email-bounced.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var emailBounced = (0, import_pieces_framework9.createTrigger)({
  auth: veroAuth,
  name: "emailBounced",
  displayName: "Email Bounced",
  description: "Triggered when an email bounces",
  aiMetadata: {
    description: "Fires when an email sent through Vero bounces (the recipient mail server rejects delivery), representing a hard or soft delivery failure for a specific user and campaign."
  },
  props: {
    instruction: import_pieces_framework9.Property.MarkDown({
      value: "## Vero Webhook Setup\nTo enable the webhooks, go to **Settings** > **Integrations** > **Webhooks** and enter the URL at which you wish to receive the webhooks.\n\n```text\n{{webhookUrl}}\n```\n"
    })
  },
  sampleData: {
    bounced_at: 1435016238,
    bounce_type: "hard",
    bounce_code: 550,
    bounce_message: "5.5.1 The email account that you tried to reach does not exist.",
    type: "bounced",
    user: {
      id: 123,
      email: "steve@getvero.com"
    },
    campaign: {
      id: 987,
      type: "transactional",
      name: "Order confirmation",
      group: "Order confirmation - Email 6",
      channel: "email",
      subject: "Your order is being processed!",
      "trigger-event": "purchased item",
      permalink: "http://app.getvero.com/view/1/341d64944577ac1f70f560e37db54a25",
      sent_to: "user@example.com",
      variation: "Variation A",
      locale: "en-US"
    },
    message_id: "20190730123456.1.1B72E094173067F0F@veromail.com"
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.type === "bounced" ? [body] : [];
  }
});

// ../ap/packages/pieces/community/vero/src/lib/triggers/email-clicked.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var emailClicked = (0, import_pieces_framework10.createTrigger)({
  auth: veroAuth,
  name: "emailClicked",
  displayName: "Email Clicked",
  description: "Triggered when an email is clicked",
  aiMetadata: {
    description: "Fires when a recipient clicks a link inside an email sent through Vero, representing link engagement for a specific user and campaign."
  },
  props: {
    instruction: import_pieces_framework10.Property.MarkDown({
      value: "## Vero Webhook Setup\nTo enable the webhooks, go to **Settings** > **Integrations** > **Webhooks** and enter the URL at which you wish to receive the webhooks.\n\n```text\n{{webhookUrl}}\n```\n"
    })
  },
  sampleData: {
    clicked_at: 1435016238,
    user_agent: "Mozilla/5.0 (compatible; MSIE 9.0; Windows NT 6.1; Trident/5.0)",
    type: "clicked",
    user: {
      id: 123,
      email: "steve@getvero.com"
    },
    campaign: {
      id: 987,
      type: "transactional",
      name: "Order confirmation",
      group: "Order confirmation - Email 6",
      channel: "email",
      subject: "Your order is being processed!",
      "trigger-event": "purchased item",
      permalink: "http://app.getvero.com/view/1/341d64944577ac1f70f560e37db54a25",
      sent_to: "user@example.com",
      variation: "Variation A",
      locale: "en-US"
    },
    message_id: "20190730123456.1.1B72E094173067F0F@veromail.com"
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.type === "clicked" ? [body] : [];
  }
});

// ../ap/packages/pieces/community/vero/src/lib/triggers/email-complained.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var emailComplained = (0, import_pieces_framework11.createTrigger)({
  auth: veroAuth,
  name: "emailComplained",
  displayName: "Email Complained",
  description: "Triggered when an email is marked as complained",
  aiMetadata: {
    description: "Fires when a recipient marks an email sent through Vero as spam or files a complaint, representing a spam/abuse report for a specific user and campaign."
  },
  props: {
    instruction: import_pieces_framework11.Property.MarkDown({
      value: "## Vero Webhook Setup\n  To enable the webhooks, go to **Settings** > **Integrations** > **Webhooks** and enter the URL at which you wish to receive the webhooks.\n  \n  ```text\n  {{webhookUrl}}\n  ```\n  "
    })
  },
  sampleData: {},
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.type === "complained" ? [body] : [];
  }
});

// ../ap/packages/pieces/community/vero/src/lib/triggers/email-converted.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var emailConverted = (0, import_pieces_framework12.createTrigger)({
  auth: veroAuth,
  name: "emailConverted",
  displayName: "Email Converted",
  description: "",
  aiMetadata: {
    description: "Fires when a recipient completes the conversion goal tied to an email sent through Vero, representing a successful campaign conversion for a specific user and campaign."
  },
  props: {
    instruction: import_pieces_framework12.Property.MarkDown({
      value: "## Vero Webhook Setup\n    To enable the webhooks, go to **Settings** > **Integrations** > **Webhooks** and enter the URL at which you wish to receive the webhooks.\n    \n    ```text\n    {{webhookUrl}}\n    ```\n    "
    })
  },
  sampleData: {
    type: "converted",
    user: {
      id: 123,
      email: "steve@getvero.com"
    },
    campaign: {
      id: 987,
      type: "transactional",
      name: "Order confirmation",
      group: "Order confirmation - Email 6",
      channel: "email",
      subject: "Your order is being processed!",
      "trigger-event": "purchased item",
      permalink: "http://app.getvero.com/view/1/341d64944577ac1f70f560e37db54a25",
      sent_to: "user@example.com",
      variation: "Variation A",
      locale: "en-US"
    },
    message_id: "20190730123456.1.1B72E094173067F0F@veromail.com"
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.type === "converted" ? [body] : [];
  }
});

// ../ap/packages/pieces/community/vero/src/lib/triggers/email-delivered.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var emailDelivered = (0, import_pieces_framework13.createTrigger)({
  auth: veroAuth,
  name: "emailDelivered",
  displayName: "Email Delivered",
  description: "",
  aiMetadata: {
    description: "Fires when an email sent through Vero is successfully delivered to the recipient mail server, representing confirmed delivery for a specific user and campaign."
  },
  props: {
    instruction: import_pieces_framework13.Property.MarkDown({
      value: "## Vero Webhook Setup\n            To enable the webhooks, go to **Settings** > **Integrations** > **Webhooks** and enter the URL at which you wish to receive the webhooks.\n            \n            ```text\n            {{webhookUrl}}\n            ```\n            "
    })
  },
  sampleData: {
    delivered_at: 1435016238,
    type: "delivered",
    user: {
      id: 123,
      email: "steve@getvero.com"
    },
    campaign: {
      id: 987,
      type: "transactional",
      name: "Order confirmation",
      group: "Order confirmation - Email 6",
      channel: "email",
      subject: "Your order is being processed!",
      "trigger-event": "purchased item",
      permalink: "http://app.getvero.com/view/1/341d64944577ac1f70f560e37db54a25",
      sent_to: "user@example.com",
      variation: "Variation A",
      locale: "en-US"
    },
    message_id: "20190730123456.1.1B72E094173067F0F@veromail.com"
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.type === "delivered" ? [body] : [];
  }
});

// ../ap/packages/pieces/community/vero/src/lib/triggers/email-opened.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var emailOpened = (0, import_pieces_framework14.createTrigger)({
  auth: veroAuth,
  name: "emailOpened",
  displayName: "Email Opened",
  description: "Triggered when an email is opened",
  aiMetadata: {
    description: "Fires when a recipient opens an email sent through Vero, representing an open/read engagement event for a specific user and campaign."
  },
  props: {
    instruction: import_pieces_framework14.Property.MarkDown({
      value: "## Vero Webhook Setup\n                To enable the webhooks, go to **Settings** > **Integrations** > **Webhooks** and enter the URL at which you wish to receive the webhooks.\n                \n                ```text\n                {{webhookUrl}}\n                ```\n                "
    })
  },
  sampleData: {
    opened_at: 1435016238,
    user_agent: "Mozilla/5.0 (compatible; MSIE 9.0; Windows NT 6.1; Trident/5.0)",
    type: "opened",
    user: {
      id: 123,
      email: "steve@getvero.com"
    },
    campaign: {
      id: 987,
      type: "transactional",
      name: "Order confirmation",
      group: "Order confirmation - Email 6",
      channel: "email",
      subject: "Your order is being processed!",
      "trigger-event": "purchased item",
      permalink: "http://app.getvero.com/view/1/341d64944577ac1f70f560e37db54a25",
      sent_to: "user@example.com",
      variation: "Variation A",
      locale: "en-US"
    },
    message_id: "20190730123456.1.1B72E094173067F0F@veromail.com"
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.type === "opened" ? [body] : [];
  }
});

// ../ap/packages/pieces/community/vero/src/lib/triggers/email-sent.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var emailSent = (0, import_pieces_framework15.createTrigger)({
  auth: veroAuth,
  name: "emailSent",
  displayName: "Email Sent",
  description: "",
  aiMetadata: {
    description: "Fires when Vero dispatches an email to a recipient, representing the send event (before delivery confirmation) for a specific user and campaign."
  },
  props: {
    instruction: import_pieces_framework15.Property.MarkDown({
      value: "## Vero Webhook Setup\n                    To enable the webhooks, go to **Settings** > **Integrations** > **Webhooks** and enter the URL at which you wish to receive the webhooks.\n                    \n                    ```text\n                    {{webhookUrl}}\n                    ```\n                    "
    })
  },
  sampleData: {
    sent_at: 1435016238,
    type: "sent",
    user: {
      id: 123,
      email: "steve@getvero.com"
    },
    campaign: {
      id: 987,
      type: "transactional",
      name: "Order confirmation",
      group: "Order confirmation - Email 6",
      channel: "email",
      subject: "Your order is being processed!",
      "trigger-event": "purchased item",
      permalink: "http://app.getvero.com/view/1/341d64944577ac1f70f560e37db54a25",
      sent_to: "user@example.com",
      variation: "Variation A",
      locale: "en-US"
    },
    message_id: "20190730123456.1.1B72E094173067F0F@veromail.com"
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.type === "sent" ? [body] : [];
  }
});

// ../ap/packages/pieces/community/vero/src/lib/triggers/new-user.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var newUser = (0, import_pieces_framework16.createTrigger)({
  auth: veroAuth,
  name: "newUser",
  displayName: "New User",
  description: "Triggered when a new user is created",
  aiMetadata: {
    description: "Fires when a new user profile is created in Vero, representing a newly added contact along with their initial properties."
  },
  props: {
    instruction: import_pieces_framework16.Property.MarkDown({
      value: "## Vero Webhook Setup\n                    To enable the webhooks, go to **Settings** > **Integrations** > **Webhooks** and enter the URL at which you wish to receive the webhooks.\n                    \n                    ```text\n                    {{webhookUrl}}\n                    ```\n                    "
    })
  },
  sampleData: {
    type: "user_created",
    user: {
      firstname: "Steve",
      company: "Vero",
      role: "Bot",
      id: 123,
      email: "steve@getvero.com"
    }
  },
  type: import_pieces_framework16.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.type === "user_created" ? [body] : [];
  }
});

// ../ap/packages/pieces/community/vero/src/lib/triggers/update-user.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var updateUser = (0, import_pieces_framework17.createTrigger)({
  auth: veroAuth,
  name: "updateUser",
  displayName: "Update User",
  description: "",
  aiMetadata: {
    description: "Fires when an existing user profile is updated in Vero, representing a change to a contact such as edited properties or added/removed tags."
  },
  props: {
    instruction: import_pieces_framework17.Property.MarkDown({
      value: "## Vero Webhook Setup\n                                To enable the webhooks, go to **Settings** > **Integrations** > **Webhooks** and enter the URL at which you wish to receive the webhooks.\n                                \n                                ```text\n                                {{webhookUrl}}\n                                ```\n                                "
    })
  },
  sampleData: {
    action: "user_updated",
    type: "user_updated",
    user: {
      id: 123,
      email: "steve@getvero.com"
    },
    id: "123",
    email: "steve@getvero.com",
    changes: {
      _tags: {
        add: ["active-customer"],
        remove: ["unactive-180-days"]
      }
    }
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.type === "user_updated" ? [body] : [];
  }
});

// ../ap/packages/pieces/community/vero/src/lib/triggers/unsubscribe-user.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var unsubscribeUser = (0, import_pieces_framework18.createTrigger)({
  auth: veroAuth,
  name: "unsubscribeUser",
  displayName: "Unsubscribe User",
  description: "",
  aiMetadata: {
    description: "Fires when a user unsubscribes from communications in Vero, representing a withdrawal of consent for a specific user (and, when applicable, the campaign that prompted it)."
  },
  props: {
    instruction: import_pieces_framework18.Property.MarkDown({
      value: "## Vero Webhook Setup\n                            To enable the webhooks, go to **Settings** > **Integrations** > **Webhooks** and enter the URL at which you wish to receive the webhooks.\n                            \n                            ```text\n                            {{webhookUrl}}\n                            ```\n                            "
    })
  },
  sampleData: {
    unsubscribed_at: 1435016238,
    type: "unsubscribed",
    user: {
      id: 123,
      email: "steve@getvero.com"
    },
    campaign: {
      campaign_title: "Order confirmation",
      series_title: "Order Tracking",
      channel: "email",
      message_id: "20190730123456.1.1B72E094173067F0F@veromail.com",
      email_subject: "Your order is being processed!",
      variation_name: "Control",
      template: "Order Template"
    }
  },
  type: import_pieces_framework18.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.type === "unsubscribed" ? [body] : [];
  }
});

// ../ap/packages/pieces/community/vero/src/lib/triggers/resubscribe-user.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var resubscribeUserTrigger = (0, import_pieces_framework19.createTrigger)({
  auth: veroAuth,
  name: "resubscribeUser",
  displayName: "Resubscribe User",
  description: "",
  aiMetadata: {
    description: "Fires when a user resubscribes to communications in Vero, representing restored consent for a specific user after a prior unsubscribe."
  },
  props: {
    instruction: import_pieces_framework19.Property.MarkDown({
      value: "## Vero Webhook Setup\n                        To enable the webhooks, go to **Settings** > **Integrations** > **Webhooks** and enter the URL at which you wish to receive the webhooks.\n                        \n                        ```text\n                        {{webhookUrl}}\n                        ```\n                        "
    })
  },
  sampleData: {
    type: "resubscribed",
    user: {
      id: 123,
      email: "steve@getvero.com"
    }
  },
  type: import_pieces_framework19.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.type === "resubscribed" ? [body] : [];
  }
});

// ../ap/packages/pieces/community/vero/src/index.ts
var vero = (0, import_pieces_framework20.createPiece)({
  displayName: "Vero",
  auth: veroAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/vero.png",
  description: "Vero is an event-based messaging platform. Increase conversions and customer satisfaction by sending more targeted emails.",
  categories: [import_pieces_framework21.PieceCategory.COMMUNICATION],
  authors: ["sanket-a11y"],
  actions: [
    aliasAUser,
    createOrUpdateUser,
    deleteUser,
    resubscribeUser,
    trackEvent,
    unsubscribe,
    updateUsersTags
  ],
  triggers: [
    emailBounced,
    emailClicked,
    emailComplained,
    emailConverted,
    emailDelivered,
    emailOpened,
    emailSent,
    newUser,
    resubscribeUserTrigger,
    unsubscribeUser,
    updateUser
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  vero
});
