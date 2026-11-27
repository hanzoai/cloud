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

// ../ap/packages/pieces/community/sendpulse/src/index.ts
var index_exports = {};
__export(index_exports, {
  sendpulse: () => sendpulse
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sendpulse/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sendpulse/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), cachedToken = null, tokenExpiresAt = 0;
async function getAccessToken(auth) {
  if (cachedToken && Date.now() < tokenExpiresAt)
    return cachedToken;
  let response = await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: "https://api.sendpulse.com/oauth/access_token",
    headers: {
      "Content-Type": "application/json"
    },
    body: {
      grant_type: "client_credentials",
      client_id: auth.clientId,
      client_secret: auth.clientSecret
    }
  });
  return cachedToken = response.body.access_token, tokenExpiresAt = Date.now() + response.body.expires_in * 1e3 - 30 * 1e3, cachedToken;
}
async function sendpulseApiCall({
  method,
  resourceUri,
  query,
  body,
  auth
}) {
  var _a, _b, _c;
  let token = await getAccessToken(auth), queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (queryParams[key] = String(value));
  let request = {
    method,
    url: `https://api.sendpulse.com${resourceUri}`,
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json"
    },
    queryParams,
    body
  };
  try {
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  } catch (error) {
    let statusCode = (_a = error.response) == null ? void 0 : _a.status, errorMessage = ((_c = (_b = error.response) == null ? void 0 : _b.data) == null ? void 0 : _c.message) || error.message || "Unknown error";
    throw new Error(`SendPulse API Error (${statusCode || "Unknown"}): ${errorMessage}`);
  }
}

// ../ap/packages/pieces/community/sendpulse/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), sendpulseAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Enter your SendPulse client credentials",
  props: {
    clientId: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Client ID",
      required: !0
    }),
    clientSecret: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Client Secret",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await sendpulseApiCall({
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: "/addressbooks",
        auth
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid Client ID or Client Secret"
      };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/sendpulse/src/lib/triggers/new-subscriber.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sendpulse/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var mailingListDropdown = import_pieces_framework2.Property.Dropdown({
  auth: sendpulseAuth,
  displayName: "Mailing List",
  description: "Select one of your SendPulse mailing lists",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your SendPulse account."
      };
    let typedAuth = auth.props;
    try {
      let lists = await sendpulseApiCall({
        auth: auth.props,
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: "/addressbooks?limit=100&offset=0"
      });
      return lists.length ? {
        disabled: !1,
        options: lists.map((list) => ({
          label: `${list.name} (${list.status_explain})`,
          value: list.id
        }))
      } : {
        disabled: !0,
        options: [],
        placeholder: "No mailing lists found in your account."
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: `Failed to load mailing lists: ${error.message}`
      };
    }
  }
});

// ../ap/packages/pieces/community/sendpulse/src/lib/triggers/new-subscriber.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), newSubscriberTrigger = (0, import_pieces_framework3.createTrigger)({
  auth: sendpulseAuth,
  name: "new_subscriber",
  displayName: "New Subscriber",
  description: "Fires when new subscriber is added",
  aiMetadata: {
    description: "Fires when a new email contact is added to the selected SendPulse mailing list (address book), via any source such as a subscription form or import. Each event represents one newly added subscriber and includes their email, source, and variables."
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  props: {
    mailingListId: mailingListDropdown
  },
  async onEnable(context) {
    var _a;
    let { mailingListId } = context.propsValue;
    try {
      let webhookResponse = await sendpulseApiCall({
        method: import_pieces_common4.HttpMethod.POST,
        auth: context.auth.props,
        resourceUri: "/v2/email-service/webhook",
        body: {
          url: context.webhookUrl,
          actions: ["new_emails"]
        }
      });
      if (webhookResponse.success && ((_a = webhookResponse.data) == null ? void 0 : _a.length) > 0) {
        let webhook = webhookResponse.data.find((w) => w.action === "new_emails");
        webhook && (await context.store.put("webhook_id", webhook.id), await context.store.put("mailing_list_id", mailingListId));
      } else
        throw new Error("Failed to create webhook");
    } catch (error) {
      throw new Error(`Failed to enable new subscriber trigger: ${error.message}`);
    }
  },
  async onDisable(context) {
    try {
      let webhookId = await context.store.get("webhook_id");
      webhookId && (await sendpulseApiCall({
        method: import_pieces_common4.HttpMethod.DELETE,
        auth: context.auth.props,
        resourceUri: `/v2/email-service/webhook/${webhookId}`
      }), await context.store.delete("webhook_id"), await context.store.delete("mailing_list_id"));
    } catch (error) {
      console.warn("Failed to delete webhook during disable:", error);
    }
  },
  async run(context) {
    let storedListId = await context.store.get("mailing_list_id"), payload = context.payload.body, payloads = Array.isArray(payload) ? payload : [payload], results = [];
    for (let item of payloads)
      item.event === "new_emails" && item.book_id === storedListId && results.push({
        id: `${item.email}_${item.timestamp}`,
        email: item.email,
        mailingListId: item.book_id,
        source: item.source,
        variables: item.variables || {},
        addedAt: new Date(parseInt(item.timestamp) * 1e3).toISOString(),
        timestamp: item.timestamp
      });
    return results;
  },
  async test() {
    return [
      {
        id: "demo@example.com_1496827625",
        email: "demo@example.com",
        mailingListId: "123456",
        source: "address book",
        variables: {},
        addedAt: (/* @__PURE__ */ new Date()).toISOString(),
        timestamp: "1496827625"
      }
    ];
  },
  sampleData: {
    id: "subscriber@example.com_1496827625",
    email: "subscriber@example.com",
    mailingListId: "123456",
    source: "subscription form",
    variables: {
      name: "John Doe"
    },
    addedAt: "2023-06-01T12:00:00.000Z",
    timestamp: "1496827625"
  }
});

// ../ap/packages/pieces/community/sendpulse/src/lib/triggers/new-unsubscriber.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), newUnsubscriberTrigger = (0, import_pieces_framework4.createTrigger)({
  auth: sendpulseAuth,
  name: "new_unsubscriber",
  displayName: "New Unsubscriber",
  description: "Fires when subscriber unsubscribes",
  aiMetadata: {
    description: "Fires when a contact unsubscribes from the selected SendPulse mailing list (address book). Each event represents one unsubscribe and includes the email, the reason if provided, and whether they opted out of all lists."
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  props: {
    mailingListId: mailingListDropdown
  },
  async onEnable(context) {
    var _a;
    let { mailingListId } = context.propsValue;
    try {
      let webhookResponse = await sendpulseApiCall({
        method: import_pieces_common5.HttpMethod.POST,
        auth: context.auth.props,
        resourceUri: "/v2/email-service/webhook",
        body: {
          url: context.webhookUrl,
          actions: ["unsubscribe"]
        }
      });
      if (webhookResponse.success && ((_a = webhookResponse.data) == null ? void 0 : _a.length) > 0) {
        let webhook = webhookResponse.data.find((w) => w.action === "unsubscribe");
        webhook && (await context.store.put("webhook_id", webhook.id), await context.store.put("mailing_list_id", mailingListId));
      } else
        throw new Error("Failed to create webhook");
    } catch (error) {
      throw new Error(`Failed to enable unsubscriber trigger: ${error.message}`);
    }
  },
  async onDisable(context) {
    try {
      let webhookId = await context.store.get("webhook_id");
      webhookId && (await sendpulseApiCall({
        method: import_pieces_common5.HttpMethod.DELETE,
        auth: context.auth.props,
        resourceUri: `/v2/email-service/webhook/${webhookId}`
      }), await context.store.delete("webhook_id"), await context.store.delete("mailing_list_id"));
    } catch (error) {
      console.warn("Failed to delete webhook during disable:", error);
    }
  },
  async run(context) {
    let storedListId = await context.store.get("mailing_list_id"), payload = context.payload.body, payloads = Array.isArray(payload) ? payload : [payload], results = [];
    for (let item of payloads)
      item.event === "unsubscribe" && item.book_id === storedListId && results.push({
        id: `${item.email}_${item.timestamp}`,
        email: item.email,
        mailingListId: item.book_id,
        taskId: item.task_id,
        fromAll: item.from_all === "1",
        reason: item.reason || "No reason provided",
        categories: item.categories || "",
        unsubscribedAt: new Date(parseInt(item.timestamp) * 1e3).toISOString(),
        timestamp: item.timestamp
      });
    return results;
  },
  async test() {
    return [
      {
        id: "test-unsubscribe@example.com_1496827872",
        email: "test-unsubscribe@example.com",
        mailingListId: "123456",
        taskId: "3668141",
        fromAll: !0,
        reason: "User clicked unsubscribe link",
        categories: "",
        unsubscribedAt: (/* @__PURE__ */ new Date()).toISOString(),
        timestamp: "1496827872"
      }
    ];
  },
  sampleData: {
    id: "unsub@example.com_1496827872",
    email: "unsub@example.com",
    mailingListId: "123456",
    taskId: "3668141",
    fromAll: !1,
    reason: "Manual unsubscribe",
    categories: "newsletter",
    unsubscribedAt: "2023-06-01T12:30:00.000Z",
    timestamp: "1496827872"
  }
});

// ../ap/packages/pieces/community/sendpulse/src/lib/triggers/updated-subscriber.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
function detectChanges(previous, current) {
  let changes = {}, allKeys = /* @__PURE__ */ new Set([...Object.keys(previous), ...Object.keys(current)]);
  for (let key of allKeys)
    JSON.stringify(previous[key]) !== JSON.stringify(current[key]) && (changes[key] = {
      from: previous[key],
      to: current[key]
    });
  return changes;
}
var updatedSubscriberTrigger = (0, import_pieces_framework5.createTrigger)({
  auth: sendpulseAuth,
  name: "updated_subscriber",
  displayName: "Updated Subscriber",
  description: "Fires when subscriber details change (polling)",
  aiMetadata: {
    description: "Polls the selected SendPulse mailing list (address book) and fires when an existing subscriber's details or variables change between checks. Each event represents one changed contact and includes the previous data, current data, and a diff of the changed fields."
  },
  type: import_pieces_framework5.TriggerStrategy.POLLING,
  props: {
    mailingListId: mailingListDropdown
  },
  async onEnable(context) {
    await context.store.put("mailing_list_id", String(context.propsValue.mailingListId)), await context.store.put("last_check", Date.now().toString());
  },
  async onDisable(context) {
    await context.store.delete("mailing_list_id"), await context.store.delete("last_check"), await context.store.delete("subscribers_cache");
  },
  async run(context) {
    let mailingListId = await context.store.get("mailing_list_id");
    if (!mailingListId)
      return [];
    try {
      let currentSubscribers = await sendpulseApiCall({
        method: import_pieces_common6.HttpMethod.GET,
        auth: context.auth.props,
        resourceUri: `/addressbooks/${mailingListId}/emails`
      }), cachedSubscribers = await context.store.get("subscribers_cache");
      if (!cachedSubscribers)
        return await context.store.put("subscribers_cache", JSON.stringify(currentSubscribers)), [];
      let previousSubscribers = JSON.parse(cachedSubscribers), changes = [];
      for (let current of currentSubscribers) {
        let previous = previousSubscribers.find((p) => p.email === current.email);
        previous && JSON.stringify(current) !== JSON.stringify(previous) && changes.push({
          id: `${current.email}_${Date.now()}`,
          email: current.email,
          mailingListId,
          previousData: previous,
          currentData: current,
          updatedAt: (/* @__PURE__ */ new Date()).toISOString(),
          changes: detectChanges(previous, current)
        });
      }
      return await context.store.put("subscribers_cache", JSON.stringify(currentSubscribers)), await context.store.put("last_check", Date.now().toString()), changes;
    } catch (error) {
      return console.error("Error checking for subscriber updates:", error), [];
    }
  },
  async test() {
    return [
      {
        id: "test-updated@example.com_1234567890",
        email: "test-updated@example.com",
        mailingListId: "123456",
        previousData: {
          email: "test-updated@example.com",
          variables: { name: "Old Name" }
        },
        currentData: {
          email: "test-updated@example.com",
          variables: { name: "New Name" }
        },
        updatedAt: (/* @__PURE__ */ new Date()).toISOString(),
        changes: {
          "variables.name": {
            from: "Old Name",
            to: "New Name"
          }
        }
      }
    ];
  },
  sampleData: {
    id: "updated-user@example.com_1234567890",
    email: "updated-user@example.com",
    mailingListId: "123456",
    previousData: {
      email: "updated-user@example.com",
      variables: { phone: "+1234567890", name: "John" }
    },
    currentData: {
      email: "updated-user@example.com",
      variables: { phone: "+0987654321", name: "John Updated" }
    },
    updatedAt: "2023-06-01T12:30:00.000Z",
    changes: {
      "variables.phone": {
        from: "+1234567890",
        to: "+0987654321"
      },
      "variables.name": {
        from: "John",
        to: "John Updated"
      }
    }
  }
});

// ../ap/packages/pieces/community/sendpulse/src/lib/actions/add-subscriber.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var addSubscriberAction = (0, import_pieces_framework6.createAction)({
  auth: sendpulseAuth,
  name: "add-subscriber",
  displayName: "Add Subscriber",
  description: "Add subscriber to mailing list",
  audience: "both",
  aiMetadata: { description: "Adds an email contact to a specific SendPulse mailing list (address book), optionally attaching subscriber variables (e.g. name, phone) and numeric tag IDs. Use to enroll a new contact into a list; requires the target mailing list ID and a valid email. Not idempotent \u2014 each call re-submits the contact to the list.", idempotent: !1 },
  props: {
    mailingListId: mailingListDropdown,
    email: import_pieces_framework6.Property.ShortText({
      displayName: "Email Address",
      description: "Subscriber email address",
      required: !0
    }),
    variables: import_pieces_framework6.Property.Object({
      displayName: "Variables",
      description: "Optional subscriber variables (e.g., name, phone)",
      required: !1
    }),
    tags: import_pieces_framework6.Property.Array({
      displayName: "Tag IDs",
      description: "Optional tag IDs to assign",
      required: !1
    })
  },
  async run(context) {
    let { mailingListId, email, variables, tags } = context.propsValue;
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email))
      throw new Error(`Invalid email format: ${email}`);
    let emailObject = { email };
    variables && Object.keys(variables).length > 0 && (emailObject.variables = variables);
    let requestBody = {
      emails: [emailObject]
    };
    if (tags && tags.length > 0) {
      let tagNumbers = tags.map((tag) => {
        let num = Number(tag);
        if (isNaN(num))
          throw new Error(`Invalid tag ID: ${tag}. Tag IDs must be numbers.`);
        return num;
      });
      requestBody.tags = tagNumbers;
    }
    try {
      if ((await sendpulseApiCall({
        method: import_pieces_common7.HttpMethod.POST,
        auth: context.auth.props,
        resourceUri: `/addressbooks/${mailingListId}/emails`,
        body: requestBody
      })).result === !0)
        return {
          success: !0,
          message: "Subscriber added successfully",
          email,
          variables: variables || {},
          mailingListId,
          tags: requestBody.tags || []
        };
      throw new Error("SendPulse API returned failure");
    } catch (error) {
      throw new Error(`Failed to add subscriber: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/sendpulse/src/lib/actions/change-variable-for-subscriber.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var variableDropdown = import_pieces_framework7.Property.Dropdown({
  auth: sendpulseAuth,
  displayName: "Variable Name",
  description: "Select variable to update",
  required: !0,
  refreshers: ["mailingListId"],
  options: async ({ auth, mailingListId }) => {
    if (!auth || !mailingListId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a mailing list first"
      };
    try {
      let variables = await sendpulseApiCall({
        auth,
        method: import_pieces_common8.HttpMethod.GET,
        resourceUri: `/addressbooks/${mailingListId}/variables`
      });
      return variables.length ? {
        disabled: !1,
        options: variables.filter((variable) => variable.name !== "email").map((variable) => ({
          label: `${variable.name} (${variable.type})`,
          value: variable.name
        }))
      } : {
        disabled: !0,
        options: [],
        placeholder: "No variables found in this mailing list"
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: `Failed to load variables: ${error.message}`
      };
    }
  }
}), changeVariableForSubscriberAction = (0, import_pieces_framework7.createAction)({
  auth: sendpulseAuth,
  name: "change-variable-for-subscriber",
  displayName: "Change Variable for Subscriber",
  description: "Update subscriber variable",
  audience: "both",
  aiMetadata: { description: "Sets a single named variable (custom field) to a given value for one subscriber, identified by email, within a specific SendPulse mailing list. Use to overwrite a single field value; the variable must already exist on the list and its type (number/date) is validated against the value. Idempotent \u2014 repeating with the same value leaves the field unchanged.", idempotent: !0 },
  props: {
    mailingListId: mailingListDropdown,
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Email Address",
      description: "Subscriber email address",
      required: !0
    }),
    variableName: variableDropdown,
    variableValue: import_pieces_framework7.Property.ShortText({
      displayName: "Variable Value",
      description: "New value for the variable",
      required: !0
    })
  },
  async run(context) {
    let { mailingListId, email, variableName, variableValue } = context.propsValue;
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email))
      throw new Error(`Invalid email format: ${email}`);
    let variableType = null;
    try {
      let variable = (await sendpulseApiCall({
        auth: context.auth.props,
        method: import_pieces_common8.HttpMethod.GET,
        resourceUri: `/addressbooks/${mailingListId}/variables`
      })).find((v) => v.name === variableName);
      variable && (variableType = variable.type);
    } catch (error) {
    }
    if (variableType) {
      if (variableType === "number" && isNaN(Number(variableValue)))
        throw new Error(`Variable "${variableName}" expects a number, but got: ${variableValue}`);
      if (variableType === "date" && !/^\d{4}-\d{2}-\d{2}$/.test(variableValue) && !Date.parse(variableValue))
        throw new Error(`Variable "${variableName}" expects a date format (YYYY-MM-DD), but got: ${variableValue}`);
    }
    let body = {
      email,
      variables: [
        {
          name: variableName,
          value: variableValue
        }
      ]
    };
    try {
      if ((await sendpulseApiCall({
        method: import_pieces_common8.HttpMethod.POST,
        auth: context.auth.props,
        resourceUri: `/addressbooks/${mailingListId}/emails/variable`,
        body
      })).result)
        return {
          success: !0,
          message: `Variable "${variableName}" updated for ${email}`,
          email,
          variableName,
          variableValue,
          variableType
        };
      throw new Error("SendPulse API returned failure");
    } catch (error) {
      throw new Error(`Failed to update variable: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/sendpulse/src/lib/actions/delete-contact.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var deleteContactAction = (0, import_pieces_framework8.createAction)({
  auth: sendpulseAuth,
  name: "delete-contact",
  displayName: "Delete Contact",
  description: "Permanently delete contact from all mailing lists",
  audience: "both",
  aiMetadata: { description: "Permanently removes a contact, identified by email, from every SendPulse mailing list (address book) in the account at once. Use to fully purge a contact rather than unsubscribe from a single list. Destructive but idempotent \u2014 once deleted, repeating the call leaves the contact absent.", idempotent: !0 },
  props: {
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Email Address",
      description: "Email address to delete from all mailing lists",
      required: !0
    })
  },
  async run(context) {
    let { email } = context.propsValue;
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email))
      throw new Error(`Invalid email format: ${email}`);
    try {
      if ((await sendpulseApiCall({
        method: import_pieces_common9.HttpMethod.DELETE,
        auth: context.auth.props,
        resourceUri: `/emails/${encodeURIComponent(email)}`
      })).result)
        return {
          success: !0,
          message: `Contact ${email} deleted from all mailing lists`,
          email
        };
      throw new Error("SendPulse API returned failure");
    } catch (error) {
      throw new Error(`Failed to delete contact: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/sendpulse/src/lib/actions/unsubscribe-user.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var unsubscribeUserAction = (0, import_pieces_framework9.createAction)({
  auth: sendpulseAuth,
  name: "unsubscribe-user",
  displayName: "Unsubscribe User",
  description: "Remove subscribers from mailing list",
  audience: "both",
  aiMetadata: { description: "Unsubscribes one or more email addresses (up to 100 per call) from a single specified SendPulse mailing list, leaving them in other lists. Use to opt contacts out of one list rather than purge them account-wide. Idempotent \u2014 repeating leaves the same contacts unsubscribed from that list.", idempotent: !0 },
  props: {
    mailingListId: mailingListDropdown,
    emails: import_pieces_framework9.Property.Array({
      displayName: "Email Addresses",
      description: "Email addresses to unsubscribe (max 100)",
      required: !0
    })
  },
  async run(context) {
    let { mailingListId, emails } = context.propsValue;
    if (emails.length === 0)
      throw new Error("At least one email address is required");
    if (emails.length > 100)
      throw new Error("Maximum 100 email addresses allowed per request");
    let emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/, invalidEmails = emails.filter((email) => !emailRegex.test(email));
    if (invalidEmails.length > 0)
      throw new Error(`Invalid email format(s): ${invalidEmails.join(", ")}`);
    let body = { emails };
    try {
      if ((await sendpulseApiCall({
        method: import_pieces_common10.HttpMethod.DELETE,
        auth: context.auth.props,
        resourceUri: `/addressbooks/${mailingListId}/emails`,
        body
      })).result)
        return {
          success: !0,
          message: `${emails.length} subscriber(s) unsubscribed successfully`,
          unsubscribed: emails,
          mailingListId,
          count: emails.length
        };
      throw new Error("Unsubscription failed - API returned failure");
    } catch (error) {
      throw new Error(`Failed to unsubscribe users: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/sendpulse/src/lib/actions/update-subscriber.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var updateSubscriberAction = (0, import_pieces_framework10.createAction)({
  auth: sendpulseAuth,
  name: "update-subscriber",
  displayName: "Update Subscriber",
  description: "Update subscriber details and variables",
  audience: "both",
  aiMetadata: { description: "Updates an existing subscriber, identified by email, within a specific SendPulse mailing list by setting their phone number and/or one or more variables (custom fields) to new values. Use to overwrite a contact's details; at least one of phone or variables must be supplied. Idempotent \u2014 repeating with the same values leaves the contact unchanged.", idempotent: !0 },
  props: {
    mailingListId: mailingListDropdown,
    email: import_pieces_framework10.Property.ShortText({
      displayName: "Email Address",
      description: "Email address of the subscriber to update",
      required: !0
    }),
    phone: import_pieces_framework10.Property.ShortText({
      displayName: "Phone Number",
      description: "New phone number (optional)",
      required: !1
    }),
    variables: import_pieces_framework10.Property.Object({
      displayName: "Variables",
      description: "Subscriber variables to update (e.g., name, custom fields)",
      required: !1
    })
  },
  async run(context) {
    let { mailingListId, email, phone, variables } = context.propsValue, results = [], hasUpdates = !1;
    if (phone)
      try {
        (await sendpulseApiCall({
          method: import_pieces_common11.HttpMethod.PUT,
          auth: context.auth.props,
          resourceUri: `/addressbooks/${mailingListId}/phone`,
          body: { email, phone }
        })).result ? (results.push({ type: "phone", success: !0, value: phone }), hasUpdates = !0) : results.push({ type: "phone", success: !1, error: "API returned failure" });
      } catch (error) {
        results.push({ type: "phone", success: !1, error: error.message });
      }
    if (variables && Object.keys(variables).length > 0) {
      let variableUpdates = Object.entries(variables).map(([name, value]) => ({
        name,
        value: String(value)
      }));
      try {
        (await sendpulseApiCall({
          method: import_pieces_common11.HttpMethod.POST,
          auth: context.auth.props,
          resourceUri: `/addressbooks/${mailingListId}/emails/variable`,
          body: { email, variables: variableUpdates }
        })).result ? (results.push({ type: "variables", success: !0, count: variableUpdates.length }), hasUpdates = !0) : results.push({ type: "variables", success: !1, error: "API returned failure" });
      } catch (error) {
        results.push({ type: "variables", success: !1, error: error.message });
      }
    }
    if (!hasUpdates && !phone && (!variables || Object.keys(variables).length === 0))
      throw new Error("No updates provided. Please specify phone number or variables to update.");
    let successfulUpdates = results.filter((r) => r.success), failedUpdates = results.filter((r) => !r.success);
    if (successfulUpdates.length === 0)
      throw new Error(`All updates failed: ${failedUpdates.map((f) => f.error).join(", ")}`);
    return {
      success: !0,
      message: `Subscriber ${email} updated successfully`,
      email,
      updates: results,
      hasFailures: failedUpdates.length > 0
    };
  }
});

// ../ap/packages/pieces/community/sendpulse/src/index.ts
var sendpulse = (0, import_pieces_framework11.createPiece)({
  displayName: "SendPulse",
  auth: sendpulseAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/sendpulse.png",
  authors: ["aryel780", "onyedikachi-david"],
  actions: [
    addSubscriberAction,
    changeVariableForSubscriberAction,
    deleteContactAction,
    unsubscribeUserAction,
    updateSubscriberAction,
    (0, import_pieces_common12.createCustomApiCallAction)({
      auth: sendpulseAuth,
      baseUrl: () => "https://api.sendpulse.com",
      authMapping: async (auth) => {
        let typedAuth = auth.props;
        return {
          Authorization: `Bearer ${(await sendpulseApiCall({
            method: import_pieces_common12.HttpMethod.POST,
            auth: typedAuth,
            resourceUri: "/oauth/access_token",
            body: {
              grant_type: "client_credentials",
              client_id: typedAuth.clientId,
              client_secret: typedAuth.clientSecret
            }
          })).access_token}`
        };
      }
    })
  ],
  triggers: [
    newSubscriberTrigger,
    newUnsubscriberTrigger,
    updatedSubscriberTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  sendpulse
});
