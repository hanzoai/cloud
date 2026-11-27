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

// ../ap/packages/pieces/community/campaign-monitor/src/index.ts
var index_exports = {};
__export(index_exports, {
  campaignMonitor: () => campaignMonitor
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/campaign-monitor/src/lib/actions/add-subscriber-to-list.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/campaign-monitor/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.createsend.com/api/v3.3";
async function makeRequest(auth, method, path, body) {
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      "Content-Type": "application/json"
    },
    authentication: {
      type: import_pieces_common.AuthenticationType.BASIC,
      username: auth.apiKey,
      password: "x"
    },
    body
  });
}
function transformCustomFields(inputFields) {
  let fields = {};
  for (let { Key, Value } of inputFields)
    fields[Key] ? Array.isArray(fields[Key]) ? fields[Key].push(Value) : fields[Key] = [fields[Key], Value] : fields[Key] = Value;
  return fields;
}

// ../ap/packages/pieces/community/campaign-monitor/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = `
To use Campaign Monitor, you need to get an API key:
1. Login to your account at https://www.campaignmonitor.com.
2. Navigate to Account Settings.
3. Click on API Keys.
4. Create a new API key or use an existing one.
`, campaignMonitorAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0
});

// ../ap/packages/pieces/community/campaign-monitor/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");
var clientId = import_pieces_framework2.Property.Dropdown({
  displayName: "Client Account",
  refreshers: [],
  required: !0,
  auth: campaignMonitorAuth,
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await makeRequest(
      { apiKey: auth.secret_text },
      import_pieces_common2.HttpMethod.GET,
      "/clients.json"
    )).body.map((client) => ({
      label: client.Name,
      value: client.ClientID
    }))
  } : {
    disabled: !0,
    placeholder: "Please connect your account.",
    options: []
  }
}), listId = import_pieces_framework2.Property.Dropdown({
  auth: campaignMonitorAuth,
  displayName: "List ID",
  refreshers: ["clientId"],
  required: !0,
  options: async ({ auth, clientId: clientId2 }) => !auth || !clientId2 ? {
    disabled: !0,
    placeholder: "Please connect your account.",
    options: []
  } : {
    disabled: !1,
    options: (await makeRequest(
      { apiKey: auth.secret_text },
      import_pieces_common2.HttpMethod.GET,
      `/clients/${clientId2}/lists.json`
    )).body.map((list) => ({
      label: list.Name,
      value: list.ListID
    }))
  }
}), customFields = import_pieces_framework2.Property.DynamicProperties({
  auth: campaignMonitorAuth,
  displayName: "Custom Fields",
  refreshers: ["listId"],
  required: !0,
  props: async ({ auth, listId: listId2 }) => {
    if (!auth || !listId2) return {};
    let fields = {}, listFields = (await makeRequest(
      {
        apiKey: auth.secret_text
      },
      import_pieces_common2.HttpMethod.GET,
      `/lists/${listId2}/customfields.json`
    )).body;
    for (let field of listFields)
      switch (field.DataType) {
        case "Text":
          fields[field.Key] = import_pieces_framework2.Property.ShortText({
            displayName: field.FieldName,
            required: !1
          });
          break;
        case "Number":
          fields[field.Key] = import_pieces_framework2.Property.Number({
            displayName: field.FieldName,
            required: !1
          });
          break;
        case "Date":
          fields[field.Key] = import_pieces_framework2.Property.DateTime({
            displayName: field.FieldName,
            required: !1
          });
          break;
        case "MultiSelectOne":
          fields[field.Key] = import_pieces_framework2.Property.StaticDropdown({
            displayName: field.FieldName,
            required: !1,
            options: {
              disabled: !1,
              options: field.FieldOptions.map((option) => ({
                label: option,
                value: option
              }))
            }
          });
          break;
        case "MultiSelectMany":
          fields[field.Key] = import_pieces_framework2.Property.StaticMultiSelectDropdown({
            displayName: field.FieldName,
            required: !1,
            options: {
              disabled: !1,
              options: field.FieldOptions.map((option) => ({
                label: option,
                value: option
              }))
            }
          });
          break;
      }
    return fields;
  }
});

// ../ap/packages/pieces/community/campaign-monitor/src/lib/actions/add-subscriber-to-list.ts
var addSubscriberToListAction = (0, import_pieces_framework3.createAction)({
  auth: campaignMonitorAuth,
  name: "add_subscriber_to_list",
  classification: "WRITE",
  displayName: "Add Subscriber",
  description: "Adds a new subscriber to a list.",
  audience: "both",
  aiMetadata: {
    description: "Adds a subscriber (by email) to a specific Campaign Monitor list under a client, optionally setting name, phone, custom fields, and tracking/SMS consent. Choose this to enroll a new contact into an email list; enable Resubscribe to re-add someone who previously unsubscribed. Not idempotent: each call posts a new subscription and may overwrite or re-add the contact, so guard against duplicate runs.",
    idempotent: !1
  },
  props: {
    clientId,
    listId,
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email Address",
      required: !0
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    phone: import_pieces_framework3.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    consentToTrack: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Consent to Track",
      description: "Whether the subscriber has consented to tracking.",
      required: !0,
      defaultValue: "Unchanged",
      options: {
        options: [
          { label: "Yes", value: "Yes" },
          { label: "No", value: "No" },
          { label: "Unchanged", value: "Unchanged" }
        ]
      }
    }),
    consentToSendSms: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Consent to Send SMS",
      required: !1,
      description: "Whether the subscriber has consented to send SMS.",
      defaultValue: "Unchanged",
      options: {
        options: [
          { label: "Yes", value: "Yes" },
          { label: "No", value: "No" },
          { label: "Unchanged", value: "Unchanged" }
        ]
      }
    }),
    resubscribe: import_pieces_framework3.Property.Checkbox({
      displayName: "Resubscribe",
      description: "If true, the subscriber will be resubscribed if they previously unsubscribed.",
      required: !1,
      defaultValue: !1
    }),
    fields: customFields
  },
  async run({ propsValue, auth }) {
    let {
      listId: listId2,
      email,
      name,
      phone,
      consentToSendSms,
      consentToTrack,
      resubscribe,
      fields
    } = propsValue, payload = {
      EmailAddress: email,
      Name: name || "",
      ConsentToTrack: consentToTrack,
      ConsentToSendSms: consentToSendSms,
      Resubscribe: resubscribe != null ? resubscribe : !0,
      MobileNumber: phone,
      CustomFields: Object.entries(fields).flatMap(
        ([key, value]) => Array.isArray(value) ? value.map((v) => ({ Key: key, Value: v })) : [{ Key: key, Value: value }]
      )
    }, response = await makeRequest(
      { apiKey: auth.secret_text },
      import_pieces_common3.HttpMethod.POST,
      `/subscribers/${listId2}.json`,
      payload
    );
    return response.status === 201 ? {
      success: !0
    } : {
      success: !1,
      error: response.body
    };
  }
});

// ../ap/packages/pieces/community/campaign-monitor/src/lib/actions/update-subscriber-details.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var updateSubscriberDetailsAction = (0, import_pieces_framework4.createAction)({
  auth: campaignMonitorAuth,
  name: "update_subscriber_details",
  classification: "WRITE",
  displayName: "Update Subscriber",
  description: "Update an existing subscriber in a list.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing subscriber, identified by their current email address, in a specific Campaign Monitor list under a client \u2014 changing name, phone, custom fields, and tracking/SMS consent. Choose this to modify a contact already on a list rather than to add one. Idempotent: it keys on the email and overwrites the same record, so repeating with identical input leaves the subscriber in the same state.",
    idempotent: !0
  },
  props: {
    clientId,
    listId,
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email Address",
      required: !0
    }),
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    phone: import_pieces_framework4.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    consentToTrack: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Consent to Track",
      description: "Whether the subscriber has consented to tracking.",
      required: !1,
      defaultValue: "Yes",
      options: {
        options: [
          { label: "Yes", value: "Yes" },
          { label: "No", value: "No" },
          { label: "Unchanged", value: "Unchanged" }
        ]
      }
    }),
    consentToSendSms: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Consent to Send SMS",
      required: !1,
      description: "Whether the subscriber has consented to send SMS.",
      defaultValue: "Unchanged",
      options: {
        options: [
          { label: "Yes", value: "Yes" },
          { label: "No", value: "No" },
          { label: "Unchanged", value: "Unchanged" }
        ]
      }
    }),
    resubscribe: import_pieces_framework4.Property.Checkbox({
      displayName: "Resubscribe",
      description: "If true, the subscriber will be resubscribed if they previously unsubscribed.",
      required: !1,
      defaultValue: !1
    }),
    fields: customFields
  },
  async run({ propsValue, auth }) {
    let {
      listId: listId2,
      email,
      name,
      consentToSendSms,
      phone,
      resubscribe,
      consentToTrack,
      fields
    } = propsValue, payload = {
      MobileNumber: phone,
      Name: name,
      ConsentToTrack: consentToTrack,
      ConsentToSendSms: consentToSendSms,
      Resubscribe: resubscribe != null ? resubscribe : !0,
      CustomFields: Object.entries(fields).flatMap(
        ([key, value]) => Array.isArray(value) ? value.map((v) => ({ Key: key, Value: v })) : [{ Key: key, Value: value }]
      )
    }, response = await makeRequest(
      { apiKey: auth.secret_text },
      import_pieces_common4.HttpMethod.PUT,
      `/subscribers/${listId2}.json?email=${encodeURIComponent(email)}`,
      payload
    );
    return response.status === 200 ? {
      success: !0
    } : {
      success: !1,
      error: response.body
    };
  }
});

// ../ap/packages/pieces/community/campaign-monitor/src/lib/actions/unsubscribe-subscriber.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var unsubscribeSubscriberAction = (0, import_pieces_framework5.createAction)({
  auth: campaignMonitorAuth,
  name: "unsubscribe_subscriber",
  classification: "DESTRUCTIVE",
  displayName: "Unsubscribe Subscriber",
  description: "Remove a subscriber from a list.",
  audience: "both",
  aiMetadata: {
    description: "Unsubscribes a subscriber, identified by email, from a specific Campaign Monitor list under a client, marking them as opted out. Choose this to suppress a contact from further sends on that list. Idempotent: repeating the call leaves the subscriber in the same unsubscribed state.",
    idempotent: !0
  },
  props: {
    clientId,
    listId,
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email Address",
      description: "The email address of the subscriber to unsubscribe.",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let { listId: listId2, email } = propsValue, payload = {
      EmailAddress: email
    }, response = await makeRequest(
      { apiKey: auth.secret_text },
      import_pieces_common5.HttpMethod.POST,
      `/subscribers/${listId2}/unsubscribe.json`,
      payload
    );
    return response.status === 200 ? {
      success: !0
    } : {
      success: !1,
      error: response.body
    };
  }
});

// ../ap/packages/pieces/community/campaign-monitor/src/lib/actions/find-subscriber.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var findSubscriberAction = (0, import_pieces_framework6.createAction)({
  auth: campaignMonitorAuth,
  name: "find_subscriber",
  classification: "READ",
  displayName: "Find Subscriber",
  description: "Find a subscriber by email in a specific list.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a single subscriber by exact email address within a specific Campaign Monitor list under a client, returning their details and tracking preference. Choose this to check whether a contact exists on a list or to read their state before adding/updating; a missing subscriber resolves as not found rather than an error. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    clientId,
    listId,
    email: import_pieces_framework6.Property.ShortText({
      displayName: "Email Address",
      description: "The email address of the subscriber to find",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let { listId: listId2, email } = propsValue;
    try {
      let response = await makeRequest(
        { apiKey: auth.secret_text },
        import_pieces_common6.HttpMethod.GET,
        `/subscribers/${listId2}.json?email=${encodeURIComponent(
          email
        )}&includetrackingpreference=true`
      );
      return {
        found: !0,
        result: __spreadProps(__spreadValues({}, response.body), {
          CustomFields: transformCustomFields(response.body.CustomFields)
        })
      };
    } catch (error) {
      return {
        found: !1,
        result: null
      };
    }
  }
});

// ../ap/packages/pieces/community/campaign-monitor/src/lib/triggers/new-subscriber-added.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var newSubscriberAddedTrigger = (0, import_pieces_framework7.createTrigger)({
  auth: campaignMonitorAuth,
  name: "new_subscriber_added",
  classification: "READ",
  displayName: "New Subscriber Added",
  description: "Triggered when a new subscriber is added to a list.",
  aiMetadata: {
    description: "Fires when a contact subscribes to the specified Campaign Monitor list under a client, via a Subscribe webhook event, and reports the new subscriber together with their details and custom fields."
  },
  props: {
    clientId,
    listId
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  sampleData: {
    EmailAddress: "subscriber@example.com",
    Name: "New Subscriber",
    Date: "2023-07-15T15:30:00Z",
    ListID: "xyz",
    CustomFields: {
      website: "https://example.com"
    },
    State: "Active",
    ConsentToTrack: "Yes"
  },
  async onEnable(context) {
    let { listId: listId2 } = context.propsValue, webhook = (await makeRequest(
      { apiKey: context.auth.secret_text },
      import_pieces_common7.HttpMethod.POST,
      `/lists/${listId2}/webhooks.json`,
      {
        Events: ["Subscribe"],
        Url: context.webhookUrl,
        PayloadFormat: "json"
      }
    )).body;
    await context.store.put("new_subscriber_added_webhook", webhook);
  },
  async onDisable(context) {
    let { listId: listId2 } = context.propsValue, storedData = await context.store.get(
      "new_subscriber_added_webhook"
    );
    (0, import_pieces_framework8.isNil)(storedData) || await makeRequest(
      { apiKey: context.auth.secret_text },
      import_pieces_common7.HttpMethod.DELETE,
      `/lists/${listId2}/webhooks/${storedData}.json`
    );
  },
  async run(context) {
    let payload = context.payload.body;
    if (payload.ListID !== context.propsValue.listId)
      return [];
    let result = [];
    for (let event of payload.Events)
      if (event.Type === "Subscribe") {
        let response = await makeRequest(
          { apiKey: context.auth.secret_text },
          import_pieces_common7.HttpMethod.GET,
          `/subscribers/${context.propsValue.listId}.json?email=${encodeURIComponent(
            event.EmailAddress
          )}`,
          payload
        );
        result.push(__spreadProps(__spreadValues({}, response.body), {
          CustomFields: transformCustomFields(response.body.CustomFields)
        }));
      }
    return result;
  }
});

// ../ap/packages/pieces/community/campaign-monitor/src/lib/triggers/subscriber-unsubscribed.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var subscriberUnsubscribedTrigger = (0, import_pieces_framework9.createTrigger)({
  auth: campaignMonitorAuth,
  name: "subscriber_unsubscribed",
  classification: "READ",
  displayName: "Subscriber Unsubscribed",
  description: "Triggered when a subscriber unsubscribes from a list",
  aiMetadata: {
    description: "Fires when a contact unsubscribes from the specified Campaign Monitor list under a client, via a Deactivate webhook event with an Unsubscribed state, and reports the affected subscriber and their details."
  },
  props: {
    clientId,
    listId
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  sampleData: {
    EmailAddress: "subscriber@example.com",
    Name: "Former Subscriber",
    Date: "2023-07-15T15:30:00Z",
    ListID: "xyz",
    State: "Unsubscribed"
  },
  async onEnable(context) {
    let { listId: listId2 } = context.propsValue, webhook = (await makeRequest(
      { apiKey: context.auth.secret_text },
      import_pieces_common8.HttpMethod.POST,
      `/lists/${listId2}/webhooks.json`,
      {
        Events: ["Deactivate"],
        Url: context.webhookUrl,
        ListID: listId2,
        PayloadFormat: "json"
      }
    )).body;
    await context.store.put("subscriber_unsubscribed_webhook", webhook);
  },
  async onDisable(context) {
    let storedData = await context.store.get(
      "subscriber_unsubscribed_webhook"
    );
    (0, import_pieces_framework10.isNil)(storedData) || await makeRequest(
      { apiKey: context.auth.secret_text },
      import_pieces_common8.HttpMethod.DELETE,
      `/lists/${listId}/webhooks/${storedData}.json`
    );
  },
  async run(context) {
    let payload = context.payload.body;
    if (payload.ListID !== context.propsValue.listId)
      return [];
    let result = [];
    for (let event of payload.Events)
      if (event.Type === "Deactivate" && event.State === "Unsubscribed") {
        let response = await makeRequest(
          { apiKey: context.auth.secret_text },
          import_pieces_common8.HttpMethod.GET,
          `/subscribers/${context.propsValue.listId}.json?email=${encodeURIComponent(
            event.EmailAddress
          )}`,
          payload
        );
        result.push(__spreadProps(__spreadValues({}, response.body), {
          CustomFields: transformCustomFields(response.body.CustomFields)
        }));
      }
    return result;
  }
});

// ../ap/packages/pieces/community/campaign-monitor/src/lib/triggers/new-client.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var newClientTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: campaignMonitorAuth,
  name: "new_client",
  classification: "READ",
  displayName: "New Client",
  description: "Triggered when a new client is added to Campaign Monitor.",
  aiMetadata: {
    description: "Fires when a new client account appears in the authenticated Campaign Monitor login, detected by polling the account client list for IDs not previously seen."
  },
  props: {},
  type: import_pieces_framework11.TriggerStrategy.POLLING,
  sampleData: {
    ClientID: "xyz",
    Name: "New Client"
  },
  async onEnable(context) {
    let clients = (await makeRequest(
      { apiKey: context.auth.secret_text },
      import_pieces_common9.HttpMethod.GET,
      "/clients.json"
    )).body;
    await context.store.put("existing_clients", {
      clients: clients.map((c) => c.ClientID)
    });
  },
  async onDisable(context) {
    await context.store.delete("existing_clients");
  },
  async test(context) {
    return (await makeRequest(
      { apiKey: context.auth.secret_text },
      import_pieces_common9.HttpMethod.GET,
      "/clients.json"
    )).body;
  },
  async run(context) {
    let storedClients = await context.store.get(
      "existing_clients"
    ), existingClientIds = (storedClients == null ? void 0 : storedClients.clients) || [], allClients = (await makeRequest(
      { apiKey: context.auth.secret_text },
      import_pieces_common9.HttpMethod.GET,
      "/clients.json"
    )).body, newClients = allClients.filter(
      (client) => !existingClientIds.includes(client.ClientID)
    ), updatedClientIds = Array.from(
      /* @__PURE__ */ new Set([...existingClientIds, ...allClients.map((c) => c.ClientID)])
    );
    return await context.store.put("existing_clients", {
      clients: updatedClientIds
    }), newClients;
  }
});

// ../ap/packages/pieces/community/campaign-monitor/src/index.ts
var campaignMonitor = (0, import_pieces_framework12.createPiece)({
  displayName: "Campaign Monitor",
  description: "Email marketing platform for delivering exceptional email campaigns.",
  logoUrl: "https://cdn.activepieces.com/pieces/campaign-monitor.png",
  authors: ["AnkitSharmaOnGithub", "kishanprmr"],
  auth: campaignMonitorAuth,
  actions: [
    addSubscriberToListAction,
    updateSubscriberDetailsAction,
    unsubscribeSubscriberAction,
    findSubscriberAction
  ],
  triggers: [
    newSubscriberAddedTrigger,
    subscriberUnsubscribedTrigger,
    newClientTrigger
  ],
  categories: [import_pieces_framework13.PieceCategory.MARKETING]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  campaignMonitor
});
