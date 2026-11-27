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
var __objRest = (source, exclude) => {
  var target = {};
  for (var prop in source)
    __hasOwnProp.call(source, prop) && exclude.indexOf(prop) < 0 && (target[prop] = source[prop]);
  if (source != null && __getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(source))
      exclude.indexOf(prop) < 0 && __propIsEnum.call(source, prop) && (target[prop] = source[prop]);
  return target;
};
var __export = (target, all) => {
  for (var name2 in all)
    __defProp(target, name2, { get: all[name2], enumerable: !0 });
}, __copyProps = (to2, from2, except, desc) => {
  if (from2 && typeof from2 == "object" || typeof from2 == "function")
    for (let key of __getOwnPropNames(from2))
      !__hasOwnProp.call(to2, key) && key !== except && __defProp(to2, key, { get: () => from2[key], enumerable: !(desc = __getOwnPropDesc(from2, key)) || desc.enumerable });
  return to2;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: !0 }), mod);

// ../ap/packages/pieces/community/convertkit/src/index.ts
var index_exports = {};
__export(index_exports, {
  convertkit: () => convertkit,
  convertkitAuth: () => convertkitAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework18 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/convertkit/src/lib/actions/custom-fields.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/convertkit/src/lib/common/custom-fields/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/convertkit/src/lib/common/constants.ts
var CONVERTKIT_API_URL = "https://api.convertkit.com/v3", BROADCASTS_API_ENDPOINT = `${CONVERTKIT_API_URL}/broadcasts`, CUSTOM_FIELDS_API_ENDPOINT = `${CONVERTKIT_API_URL}/custom_fields`, FORMS_API_ENDPOINT = `${CONVERTKIT_API_URL}/forms`, PURCHASES_API_ENDPOINT = `${CONVERTKIT_API_URL}/purchases`, SEQUENCES_API_ENDPOINT = `${CONVERTKIT_API_URL}/sequences`, SUBSCRIBERS_API_ENDPOINT = `${CONVERTKIT_API_URL}/subscribers`, TAGS_API_ENDPOINT = `${CONVERTKIT_API_URL}/tags`, WEBHOOKS_API_ENDPOINT = `${CONVERTKIT_API_URL}/automations/hooks`;

// ../ap/packages/pieces/community/convertkit/src/lib/common/service.ts
var import_pieces_common = require("@activepieces/pieces-common");
var fetchBroadcasts = async (auth, page) => {
  let request = {
    url: BROADCASTS_API_ENDPOINT,
    method: import_pieces_common.HttpMethod.GET,
    queryParams: {
      api_secret: auth,
      page: page.toString(),
      sort_order: "desc"
    }
  }, response = await import_pieces_common.httpClient.sendRequest(
    request
  ), errorMessage = `Failed to fetch broadcasts. Response code: ${response.status} Response body: ${JSON.stringify(response.body)}`;
  if (response.status !== 200)
    throw new Error(errorMessage);
  if (response.body.broadcasts)
    return response.body.broadcasts;
  throw new Error(errorMessage);
}, fetchCustomFields = async (auth) => {
  let request = {
    url: CUSTOM_FIELDS_API_ENDPOINT,
    body: {
      api_secret: auth
    },
    method: import_pieces_common.HttpMethod.GET
  }, response = await import_pieces_common.httpClient.sendRequest(request), errorMessage = `Failed to fetch custom fields. Response code: ${response.status} Response body: ${JSON.stringify(response.body)}`;
  if (response.status !== 200)
    throw new Error(errorMessage);
  if (response.body.custom_fields)
    return response.body.custom_fields;
  throw new Error(errorMessage);
}, fetchForms = async (auth) => {
  let request = {
    url: FORMS_API_ENDPOINT,
    body: {
      api_secret: auth
    },
    method: import_pieces_common.HttpMethod.GET
  }, response = await import_pieces_common.httpClient.sendRequest(request), errorMessage = `Failed to fetch forms. Response code: ${response.status} Response body: ${JSON.stringify(response.body)}`;
  if (response.status !== 200)
    throw new Error(errorMessage);
  if (response.body.forms)
    return response.body.forms;
  throw new Error(errorMessage);
}, fetchPurchases = async (auth, page) => {
  let request = {
    url: PURCHASES_API_ENDPOINT,
    body: {
      api_secret: auth,
      page
    },
    method: import_pieces_common.HttpMethod.GET
  }, response = await import_pieces_common.httpClient.sendRequest(
    request
  ), errorMessage = `Failed to fetch purchases. Response code: ${response.status} Response body: ${JSON.stringify(response.body)}`;
  if (response.status !== 200)
    throw new Error(errorMessage);
  if (response.body.purchases)
    return response.body.purchases;
  throw new Error(errorMessage);
}, fetchSequences = async (auth) => {
  let request = {
    url: SEQUENCES_API_ENDPOINT,
    body: {
      api_secret: auth
    },
    method: import_pieces_common.HttpMethod.GET
  }, response = await import_pieces_common.httpClient.sendRequest(
    request
  ), errorMessage = `Failed to fetch sequences. Response code: ${response.status} Response body: ${JSON.stringify(response.body)}`;
  if (response.status !== 200)
    throw new Error(errorMessage);
  if (response.body.courses)
    return response.body.courses;
  throw new Error(errorMessage);
}, fetchSubscriperById = async (auth, subscriberId2) => {
  let request = {
    url: `${SUBSCRIBERS_API_ENDPOINT}/${subscriberId2}`,
    body: {
      api_secret: auth
    },
    method: import_pieces_common.HttpMethod.GET
  }, response = await import_pieces_common.httpClient.sendRequest(
    request
  ), errorMessage = `Failed to fetch subscriber. Response code: ${response.status} Response body: ${JSON.stringify(response.body)}`;
  if (response.status !== 200)
    throw new Error(errorMessage);
  if (response.body.subscriber)
    return response.body.subscriber;
  throw new Error(errorMessage);
}, fetchSubscriberByEmail = async (auth, email_address) => {
  let request = {
    url: SUBSCRIBERS_API_ENDPOINT,
    body: {
      api_secret: auth,
      email_address
    },
    method: import_pieces_common.HttpMethod.GET
  }, response = await import_pieces_common.httpClient.sendRequest(
    request
  ), errorMessage = `Failed to fetch subscriber that match the provided email. Response code: ${response.status} Response body: ${JSON.stringify(response.body)}`;
  if (response.status !== 200)
    throw new Error(errorMessage);
  let subscriber = response.body.subscribers[0];
  if (subscriber)
    return subscriber;
  throw new Error(errorMessage);
}, fetchSubscribedTags = async (auth, subscriberId2) => {
  let request = {
    url: `${SUBSCRIBERS_API_ENDPOINT}/${subscriberId2}/tags`,
    body: {
      api_secret: auth
    },
    method: import_pieces_common.HttpMethod.GET
  }, response = await import_pieces_common.httpClient.sendRequest(request), errorMessage = `Failed to fetch tags. Response code: ${response.status} Response body: ${JSON.stringify(response.body)}`;
  if (response.status !== 200)
    throw new Error(errorMessage);
  if (response.body.tags)
    return response.body.tags;
  throw new Error(errorMessage);
}, fetchTags = async (auth) => {
  let request = {
    url: TAGS_API_ENDPOINT,
    body: {
      api_secret: auth
    },
    method: import_pieces_common.HttpMethod.GET
  }, response = await import_pieces_common.httpClient.sendRequest(request), errorMessage = `Failed to fetch tags. Response code: ${response.status} Response body: ${JSON.stringify(response.body)}`;
  if (response.status !== 200)
    throw new Error(errorMessage);
  if (response.body.tags)
    return response.body.tags;
  throw new Error(errorMessage);
}, createWebhook = async (auth, payload) => {
  let body = __spreadProps(__spreadValues({}, payload), { api_secret: auth }), request = {
    url: WEBHOOKS_API_ENDPOINT,
    body,
    method: import_pieces_common.HttpMethod.POST
  }, response = await import_pieces_common.httpClient.sendRequest(request), errorMessage = `Failed to create webhook. Response code: ${response.status} Response body: ${JSON.stringify(response.body)}`;
  if (response.status !== 200)
    throw new Error(errorMessage);
  if (response.body.rule)
    return response.body.rule;
  throw new Error(errorMessage);
}, removeWebhook = async (auth, ruleId) => {
  let request = {
    url: `${WEBHOOKS_API_ENDPOINT}/${ruleId}`,
    body: { api_secret: auth },
    method: import_pieces_common.HttpMethod.DELETE
  }, response = await import_pieces_common.httpClient.sendRequest(request), errorMessage = `Failed to remove webhook. Response code: ${response.status} Response body: ${JSON.stringify(response.body)}`;
  if (response.status !== 200)
    throw new Error(errorMessage);
  if (response.body.success)
    return response.body.success;
  throw new Error(errorMessage);
};

// ../ap/packages/pieces/community/convertkit/src/lib/common/custom-fields/index.ts
var fieldsArray = import_pieces_framework.Property.Array({
  displayName: "Fields",
  description: "The custom fields",
  required: !0
}), label = import_pieces_framework.Property.Dropdown({
  auth: convertkitAuth,
  displayName: "Custom Label",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => auth ? {
    options: (await fetchCustomFields(auth.secret_text)).map(
      (field) => ({
        label: field.label,
        value: field.id
      })
    )
  } : {
    disabled: !0,
    placeholder: "Connect your account",
    options: []
  }
}), new_label = import_pieces_framework.Property.ShortText({
  displayName: "New Label",
  description: "The new label for the custom field",
  required: !0
}), allFields = import_pieces_framework.Property.DynamicProperties({
  auth: convertkitAuth,
  displayName: "Custom Fields",
  description: "The custom fields",
  required: !1,
  refreshers: ["auth"],
  props: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your account",
        options: []
      };
    let fields = {};
    return (await fetchCustomFields(
      auth.secret_text
    )).forEach(
      (field) => {
        fields[field.key] = import_pieces_framework.Property.ShortText({
          displayName: field.label,
          description: `Enter the value for custom field: ${field.label}`,
          required: !1
        });
      }
    ), fields;
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/custom-fields.ts
var listFields = (0, import_pieces_framework2.createAction)({
  auth: convertkitAuth,
  name: "custom_fields_list_fields",
  displayName: "List Custom Fields",
  description: "Returns a list of all custom fields",
  audience: "both",
  aiMetadata: {
    description: "Lists every custom field defined on the account, including each field id, key, and label. Use it to discover field keys before setting custom field values on subscribers. Takes no inputs; read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    return fetchCustomFields(context.auth.secret_text);
  }
}), createField = (0, import_pieces_framework2.createAction)({
  auth: convertkitAuth,
  name: "custom_fields_create_field",
  displayName: "Create Custom Field",
  description: "Create a new custom field",
  audience: "both",
  aiMetadata: {
    description: "Creates one or more new custom fields from an array of labels (ConvertKit derives each field key from its label). Not idempotent \u2014 repeating the call creates duplicate fields, so check List Custom Fields first.",
    idempotent: !1
  },
  props: {
    fields: fieldsArray
  },
  async run(context) {
    let url = CUSTOM_FIELDS_API_ENDPOINT, body = {
      api_secret: context.auth.secret_text,
      label: context.propsValue.fields
    }, request = {
      url,
      method: import_pieces_common2.HttpMethod.POST,
      body
    }, response = await import_pieces_common2.httpClient.sendRequest(request);
    if (response.status !== 201)
      throw new Error(`Error creating field: ${response.status}`);
    return response.body;
  }
}), updateField = (0, import_pieces_framework2.createAction)({
  auth: convertkitAuth,
  name: "custom_fields_update_field",
  displayName: "Custom Fields: Update Field",
  description: "Update a custom field",
  audience: "both",
  aiMetadata: {
    description: "Renames an existing custom field, selected by its current label, to a new label. Not idempotent \u2014 once the rename succeeds the old label no longer matches, so a retry with the same inputs fails.",
    idempotent: !1
  },
  props: {
    label,
    new_label
  },
  async run(context) {
    let { label: label2, new_label: new_label2 } = context.propsValue, url = `${CUSTOM_FIELDS_API_ENDPOINT}/${label2}`, body = {
      api_secret: context.auth.secret_text,
      label: new_label2
    }, request = {
      url,
      method: import_pieces_common2.HttpMethod.PUT,
      body
    }, response = await import_pieces_common2.httpClient.sendRequest(request);
    if (response.status !== 204)
      throw new Error(`Error updating field: ${response.status}`);
    return { status: response.status, message: "Field updated", success: !0 };
  }
}), deleteField = (0, import_pieces_framework2.createAction)({
  auth: convertkitAuth,
  name: "custom_fields_delete_field",
  displayName: "Custom Fields: Delete Field",
  description: "Delete a custom field",
  audience: "both",
  aiMetadata: {
    description: "Deletes a custom field by label, removing it and its stored values from all subscribers. Destructive and not retry-safe \u2014 a repeat call fails once the field is gone.",
    idempotent: !1
  },
  props: {
    label
  },
  async run(context) {
    let { label: label2 } = context.propsValue, url = `${CUSTOM_FIELDS_API_ENDPOINT}/${label2}`, body = {
      api_secret: context.auth.secret_text
    }, request = {
      url,
      method: import_pieces_common2.HttpMethod.DELETE,
      body
    }, response = await import_pieces_common2.httpClient.sendRequest(request);
    if (response.status !== 204)
      throw new Error(`Error deleting field: ${response.status}`);
    return { status: response.status, message: "Field deleted", success: !0 };
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/subscribers.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/convertkit/src/lib/common/subscribers/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var subscriberId = import_pieces_framework3.Property.ShortText({
  displayName: "Subscriber ID",
  description: "The subscriber ID",
  required: !0
}), subscriberEmail = import_pieces_framework3.Property.ShortText({
  displayName: "Email",
  description: "The email of the subscriber",
  required: !0
}), subscriberEmailOptional = __spreadProps(__spreadValues({}, subscriberEmail), {
  required: !1
}), subscriberFirstName = import_pieces_framework3.Property.ShortText({
  displayName: "First Name",
  description: "The first name of the subscriber",
  required: !1
}), subscribersPageNumber = import_pieces_framework3.Property.Number({
  displayName: "Page",
  description: "Page number. Each page of results will contain up to 50 subscribers.",
  required: !1,
  defaultValue: 1
}), from = import_pieces_framework3.Property.DateTime({
  displayName: "From",
  description: "Return subscribers created after this date",
  required: !1
}), to = import_pieces_framework3.Property.DateTime({
  displayName: "To",
  description: "Return subscribers created before this date",
  required: !1
}), updatedFrom = import_pieces_framework3.Property.DateTime({
  displayName: "Updated From",
  description: "Return subscribers updated after this date",
  required: !1
}), updatedTo = import_pieces_framework3.Property.DateTime({
  displayName: "Updated To",
  description: "Return subscribers updated before this date",
  required: !1
}), sortOrder = import_pieces_framework3.Property.StaticDropdown({
  displayName: "Sort Order",
  description: "Sort order",
  required: !0,
  defaultValue: "asc",
  options: {
    options: [
      { value: "asc", label: "Ascending" },
      { value: "desc", label: "Descending" }
    ]
  }
}), sortField = import_pieces_framework3.Property.ShortText({
  displayName: "Sort Field",
  description: "Sort field",
  required: !1
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/subscribers.ts
var getSubscriberById = (0, import_pieces_framework4.createAction)({
  auth: convertkitAuth,
  name: "subscribers_get_subscriber_by_id",
  displayName: "Get Subscriber By Id",
  description: "Returns data for a single subscriber",
  audience: "both",
  aiMetadata: {
    description: "Fetches one subscriber record (email, name, state, custom fields) by numeric subscriber ID. Use Get Subscriber By Email when only an address is known. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    subscriberId
  },
  run(context) {
    let { subscriberId: subscriberId2 } = context.propsValue;
    return fetchSubscriperById(context.auth.secret_text, subscriberId2);
  }
}), getSubscriberByEmail = (0, import_pieces_framework4.createAction)({
  auth: convertkitAuth,
  name: "subscribers_get_subscriber_by_email",
  displayName: "Get Subscriber By Email",
  description: "Returns data for a single subscriber",
  audience: "both",
  aiMetadata: {
    description: "Looks up a single subscriber by email address and returns the full record including the numeric subscriber ID. Prefer this over Get Subscriber By Id when the ID is unknown. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    email_address: subscriberEmail
  },
  async run(context) {
    let { email_address } = context.propsValue;
    return fetchSubscriberByEmail(context.auth.secret_text, email_address);
  }
}), listSubscribers = (0, import_pieces_framework4.createAction)({
  auth: convertkitAuth,
  name: "subscribers_list_subscribers",
  displayName: "List Subscribers",
  description: "Returns a list of all subscribers",
  audience: "both",
  aiMetadata: {
    description: "Lists subscribers with optional filters (created/updated date ranges, email address), paging, and sorting. Use it for bulk audits or fuzzy searches; for one known subscriber prefer the Get Subscriber actions. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    page: subscribersPageNumber,
    sortOrder,
    sortField,
    from,
    to,
    updatedFrom,
    updatedTo,
    emailAddress: subscriberEmail
  },
  async run(context) {
    let {
      page,
      from: from2,
      to: to2,
      updatedFrom: updatedFrom2,
      updatedTo: updatedTo2,
      emailAddress,
      sortOrder: sortOrder3,
      sortField: sortField2
    } = context.propsValue, url = SUBSCRIBERS_API_ENDPOINT, body = {
      api_secret: context.auth.secret_text,
      page,
      from: from2,
      to: to2,
      updated_from: updatedFrom2,
      updated_to: updatedTo2,
      email_address: emailAddress,
      sort_order: sortOrder3,
      sort_field: sortField2
    }, request = {
      url,
      method: import_pieces_common3.HttpMethod.GET,
      body
    }, response = await import_pieces_common3.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(`Error fetching subscribers: ${response.status}`);
    return response.body.subscribers;
  }
}), updateSubscriber = (0, import_pieces_framework4.createAction)({
  auth: convertkitAuth,
  name: "subscribers_update_subscriber",
  displayName: "Update Subscriber",
  description: "Update a subscriber",
  audience: "both",
  aiMetadata: {
    description: "Updates a subscriber email address, first name, or custom field values by numeric subscriber ID. Idempotent \u2014 repeating the same update leaves the subscriber in the same state. It cannot change subscription status; use Unsubscribe Subscriber for that.",
    idempotent: !0
  },
  props: {
    subscriberId,
    emailAddress: subscriberEmailOptional,
    firstName: subscriberFirstName,
    fields: allFields
  },
  async run(context) {
    let { subscriberId: subscriberId2, emailAddress, firstName, fields } = context.propsValue, url = `${SUBSCRIBERS_API_ENDPOINT}/${subscriberId2}`, body = {
      api_secret: context.auth.secret_text,
      email_address: emailAddress,
      first_name: firstName,
      fields
    }, request = {
      url,
      method: import_pieces_common3.HttpMethod.PUT,
      body
    }, response = await import_pieces_common3.httpClient.sendRequest(
      request
    );
    if (response.status !== 200)
      throw new Error(`Error updating subscriber: ${response.status}`);
    return response.body.subscriber;
  }
}), unsubscribeSubscriber = (0, import_pieces_framework4.createAction)({
  auth: convertkitAuth,
  name: "subscribers_unsubscribe_subscriber",
  displayName: "Unsubscribe Subscriber",
  description: "Unsubscribe a subscriber",
  audience: "both",
  aiMetadata: {
    description: "Unsubscribes the given email address from all emails in the account; this is the only removal mechanism, as there is no delete-subscriber action. Treated as non-idempotent \u2014 a retry may error once the address is already unsubscribed, though the end state is the same.",
    idempotent: !1
  },
  props: {
    email: subscriberEmail
  },
  async run(context) {
    let { email } = context.propsValue, url = `${CONVERTKIT_API_URL}/unsubscribe`, body = { email, api_secret: context.auth.secret_text }, request = {
      url,
      method: import_pieces_common3.HttpMethod.PUT,
      body
    }, response = await import_pieces_common3.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(`Error unsubscribing subscriber: ${response.status}`);
    return response.body.subscriber;
  }
}), listTagsBySubscriberId = (0, import_pieces_framework4.createAction)({
  auth: convertkitAuth,
  name: "subscribers_list_tags_by_subscriber_id",
  displayName: "List Tags By Subscriber Id",
  description: "Returns a list of all subscribed tags",
  audience: "both",
  aiMetadata: {
    description: "Lists all tags currently applied to a subscriber, looked up by numeric subscriber ID. Use List Tags By Email when only an address is known. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    subscriberId
  },
  run(context) {
    let { subscriberId: subscriberId2 } = context.propsValue;
    return fetchSubscribedTags(context.auth.secret_text, subscriberId2);
  }
}), listSubscriberTagsByEmail = (0, import_pieces_framework4.createAction)({
  auth: convertkitAuth,
  name: "subscribers_list_tags_by_email",
  displayName: "List Tags By Email",
  description: "Returns a list of all subscribed tags",
  audience: "both",
  aiMetadata: {
    description: "Lists all tags applied to a subscriber, looked up by email address (the email is resolved to a subscriber ID internally). Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    email_address: subscriberEmail
  },
  async run(context) {
    let { email_address } = context.propsValue, subscriberId2 = await fetchSubscriberByEmail(
      context.auth.secret_text,
      email_address
    );
    return fetchSubscribedTags(context.auth.secret_text, subscriberId2.id);
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/webhooks.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/convertkit/src/lib/common/webhooks/index.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var initiatorValue = import_pieces_framework5.Property.ShortText({
  displayName: "Initiator Value URL",
  description: "The initiator value URL that will trigger the webhook",
  required: !0
}), webhookId = import_pieces_framework5.Property.Number({
  displayName: "Webhook Id",
  description: "The webhook rule id",
  required: !0
}), targetUrl = import_pieces_framework5.Property.ShortText({
  displayName: "Target URL",
  description: "The URL that will be called when the webhook is triggered",
  required: !0
}), eventParameter = import_pieces_framework5.Property.DynamicProperties({
  displayName: "Event Parameter",
  description: "The required parameter for the event",
  required: !1,
  refreshers: ["auth", "event"],
  auth: convertkitAuth,
  props: async ({ auth, event: event2 }) => {
    if (!event2 || !auth)
      return {
        disabled: !0,
        placeholder: "Select event first",
        options: []
      };
    let fields = {}, eventOption = createWebhookParameterOptions.find(
      (option) => option.value === event2 && option.required_parameter
    );
    if (!eventOption)
      return fields;
    let required_parameter = eventOption.required_parameter || "", param_label = eventOption.param_label || "", fieldType = eventOption.type || "";
    if (required_parameter === "tag_id") {
      let options = (await fetchTags(auth.secret_text)).map((tag2) => ({
        label: tag2.name,
        value: tag2.id
      }));
      return fields[required_parameter] = import_pieces_framework5.Property.StaticDropdown({
        displayName: "Tag",
        description: "Choose a Tag",
        required: !0,
        options: {
          options
        }
      }), fields;
    }
    if (required_parameter === "form_id") {
      let options = (await fetchForms(auth.secret_text)).map((form) => ({
        label: form.name,
        value: form.id
      }));
      return fields[required_parameter] = import_pieces_framework5.Property.StaticDropdown({
        displayName: "Form",
        description: "Choose a Form",
        required: !0,
        options: {
          options
        }
      }), fields;
    }
    if (required_parameter === "sequence_id") {
      let options = (await fetchSequences(auth.secret_text)).map((sequence) => ({
        label: sequence.name,
        value: sequence.id
      }));
      return fields[required_parameter] = import_pieces_framework5.Property.StaticDropdown({
        displayName: "Sequence",
        description: "Choose a Sequence",
        required: !0,
        options: {
          options
        }
      }), fields;
    }
    let field = {
      displayName: param_label,
      description: `The ${param_label} parameter for the event`,
      required: !0
    };
    return fieldType === "number" && (fields[required_parameter] = import_pieces_framework5.Property.Number(field)), fieldType === "string" && (fields[required_parameter] = import_pieces_framework5.Property.ShortText(field)), fields;
  }
}), createWebhookParameterOptions = [
  {
    label: "Subscriber activated",
    value: "subscriber.subscriber_activate" /* subscriberActivate */,
    required_parameter: null,
    param_label: null,
    type: null
  },
  {
    label: "Subscriber unsubscribed",
    value: "subscriber.subscriber_unsubscribe" /* subscriberUnsubscribe */,
    required_parameter: null,
    param_label: null,
    type: null
  },
  {
    label: "Subscriber bounced",
    value: "subscriber.subscriber_bounce" /* subscriberBounce */,
    required_parameter: null,
    param_label: null,
    type: null
  },
  {
    label: "Subscriber complained",
    value: "subscriber.subscriber_complain" /* subscriberComplain */,
    required_parameter: null,
    param_label: null,
    type: null
  },
  {
    label: "Form subscribed",
    value: "subscriber.form_subscribe" /* formSubscribe */,
    required_parameter: "form_id",
    param_label: "Form Id",
    type: "number"
  },
  {
    label: "Sequence subscribed",
    value: "subscriber.course_subscribe" /* courseSubscribe */,
    required_parameter: "sequence_id",
    param_label: "Sequence Id",
    type: "number"
  },
  {
    label: "Sequence completed",
    value: "subscriber.course_complete" /* courseComplete */,
    required_parameter: "sequence_id",
    param_label: "Sequence Id",
    type: "number"
  },
  {
    label: "Link clicked",
    value: "subscriber.link_click" /* linkClick */,
    required_parameter: "initiator_value",
    param_label: "Initiator Value",
    type: "string"
  },
  {
    label: "Product purchased",
    value: "subscriber.product_purchase" /* productPurchase */,
    required_parameter: "product_id",
    param_label: "Product Id",
    type: "number"
  },
  {
    label: "Tag added to subscriber",
    value: "subscriber.tag_add" /* tagAdd */,
    required_parameter: "tag_id",
    param_label: "Tag Id",
    type: "number"
  },
  {
    label: "Tag removed from subscriber",
    value: "subscriber.tag_remove" /* tagRemove */,
    required_parameter: "tag_id",
    param_label: "Tag Id",
    type: "number"
  },
  {
    label: "Purchase created",
    value: "purchase.purchase_create" /* purchaseCreate */,
    required_parameter: null,
    param_label: null,
    type: null
  }
], event = import_pieces_framework5.Property.StaticDropdown({
  displayName: "Event",
  description: "The event that will trigger the webhook",
  required: !0,
  options: {
    options: createWebhookParameterOptions
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/webhooks.ts
var createWebhook2 = (0, import_pieces_framework6.createAction)({
  auth: convertkitAuth,
  name: "create_webhook",
  displayName: "Add Webhook",
  description: "Create a webhook automation",
  audience: "both",
  aiMetadata: {
    description: "Registers a webhook automation that POSTs to a target URL whenever the chosen ConvertKit event fires (e.g. subscriber activated, tag added); some events also need an event parameter such as a tag or form ID. Not idempotent \u2014 each call registers another webhook, so keep the returned rule ID for later deletion.",
    idempotent: !1
  },
  props: {
    targetUrl,
    event,
    eventParameter
  },
  run(context) {
    let { targetUrl: targetUrl2, event: event2, eventParameter: eventParameter2 } = context.propsValue, payload = {
      event: __spreadValues({
        name: event2
      }, eventParameter2),
      target_url: targetUrl2
    };
    return createWebhook(context.auth.secret_text, payload);
  }
}), deleteWebhook = (0, import_pieces_framework6.createAction)({
  auth: convertkitAuth,
  name: "destroy_webhook",
  displayName: "Delete Webhook",
  description: "Delete a webhook automation",
  audience: "both",
  aiMetadata: {
    description: "Deletes a previously registered webhook automation by its webhook rule ID (returned when the webhook was created). Not retry-safe \u2014 a repeat call fails once the webhook is gone.",
    idempotent: !1
  },
  props: {
    webhookId
  },
  run(context) {
    let { webhookId: webhookId2 } = context.propsValue;
    return removeWebhook(context.auth.secret_text, webhookId2);
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/broadcasts.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/convertkit/src/lib/common/broadcasts/index.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), broadcastId = import_pieces_framework7.Property.ShortText({
  displayName: "Broadcast Id",
  description: "The broadcast id",
  required: !0
}), broadcastPageNumber = import_pieces_framework7.Property.Number({
  displayName: "Page",
  description: "Page number. Each page of results will contain up to 50 broadcasts.",
  required: !1,
  defaultValue: 1
}), broadcastContent = import_pieces_framework7.Property.ShortText({
  displayName: "Content",
  description: "The broadcast's email content - this can contain text and simple HTML markdown (such as h1, img or p tags)",
  required: !1
}), description = import_pieces_framework7.Property.ShortText({
  displayName: "Description",
  description: "An internal description of this broadcast",
  required: !1
}), broadcastEmailAddress = import_pieces_framework7.Property.ShortText({
  displayName: "Email Address",
  description: "Sending email address; leave blank to use your account's default sending email address",
  required: !1
}), emailLayoutTemplate = import_pieces_framework7.Property.ShortText({
  displayName: "Email Layout Template",
  description: "Name of the email template to use; leave blank to use your account's default email template",
  required: !1
}), isPublic = import_pieces_framework7.Property.Checkbox({
  displayName: "Public",
  description: "Specifies whether or not this is a public post",
  required: !1,
  defaultValue: !1
}), publishedAt = import_pieces_framework7.Property.DateTime({
  displayName: "Published At",
  description: "Specifies the time that this post was published (applicable only to public posts)",
  required: !1
}), sendAt = import_pieces_framework7.Property.DateTime({
  displayName: "Send At",
  description: "Time that this broadcast should be sent; leave blank to create a draft broadcast. If set to a future time, this is the time that the broadcast will be scheduled to send.",
  required: !1
}), subject = import_pieces_framework7.Property.ShortText({
  displayName: "Subject",
  description: "The broadcast email's subject",
  required: !1
}), thumbnailAlt = import_pieces_framework7.Property.ShortText({
  displayName: "Thumbnail Alt",
  description: "Specify the ALT attribute of the public thumbnail image (applicable only to public posts)",
  required: !1
}), thumbnailUrl = import_pieces_framework7.Property.ShortText({
  displayName: "Thumbnail Url",
  description: "Specify the URL of the thumbnail image to accompany the broadcast post (applicable only to public posts)",
  required: !1
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/broadcasts.ts
var listBroadcasts = (0, import_pieces_framework8.createAction)({
  auth: convertkitAuth,
  name: "broadcasts_list_broadcasts",
  displayName: "List Broadcasts",
  description: "List all broadcasts",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a page of broadcasts (one-off emails) in the ConvertKit account, 50 per page. Use it to find a broadcast ID before getting, updating, or deleting one. Read-only and idempotent; pass a page number to fetch beyond the first 50.",
    idempotent: !0
  },
  props: {
    page: broadcastPageNumber
  },
  run(context) {
    let page = context.propsValue.page || 1;
    return fetchBroadcasts(context.auth.secret_text, page);
  }
}), createBroadcast = (0, import_pieces_framework8.createAction)({
  auth: convertkitAuth,
  name: "broadcasts_create_broadcast",
  displayName: "Create Broadcast",
  description: "Create a new broadcast",
  audience: "both",
  aiMetadata: {
    description: "Creates a new email broadcast (one-off email), optionally scheduled via the send-at field; without it the broadcast is saved unsent. Not idempotent \u2014 every call creates another broadcast, so do not retry blindly.",
    idempotent: !1
  },
  props: {
    content: broadcastContent,
    description,
    emailAddress: broadcastEmailAddress,
    emailLayoutTemplate,
    isPublic,
    publishedAt,
    sendAt,
    subject,
    thumbnailAlt,
    thumbnailUrl
  },
  async run(context) {
    let {
      content,
      description: description2,
      emailAddress,
      emailLayoutTemplate: emailLayoutTemplate2,
      isPublic: isPublic2,
      publishedAt: publishedAt2,
      sendAt: sendAt2,
      subject: subject2,
      thumbnailAlt: thumbnailAlt2,
      thumbnailUrl: thumbnailUrl2
    } = context.propsValue, url = BROADCASTS_API_ENDPOINT, body = {
      api_secret: context.auth.secret_text,
      content,
      description: description2,
      email_address: emailAddress,
      email_layout_template: emailLayoutTemplate2,
      public: isPublic2,
      published_at: publishedAt2,
      send_at: sendAt2,
      subject: subject2,
      thumbnail_alt: thumbnailAlt2,
      thumbnail_url: thumbnailUrl2
    }, request = {
      url,
      method: import_pieces_common4.HttpMethod.POST,
      body,
      headers: {
        "Content-Type": "application/json"
      }
    }, response = await import_pieces_common4.httpClient.sendRequest(
      request
    );
    if (response.status !== 201)
      throw new Error(`Error creating broadcast: ${response.status}`);
    return response.body.broadcast;
  }
}), getBroadcastById = (0, import_pieces_framework8.createAction)({
  auth: convertkitAuth,
  name: "broadcasts_get_broadcast",
  displayName: "Get Broadcast",
  description: "Get a broadcast",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single broadcast by its ID, returning its content and settings. Use when the broadcast ID is already known (e.g. from List Broadcasts); for delivery metrics use Broadcast Stats instead. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    broadcastId
  },
  async run(context) {
    let { broadcastId: broadcastId2 } = context.propsValue, url = `${BROADCASTS_API_ENDPOINT}/${broadcastId2}`, body = {
      api_secret: context.auth.secret_text
    }, request = {
      url,
      body,
      method: import_pieces_common4.HttpMethod.GET
    }, response = await import_pieces_common4.httpClient.sendRequest(
      request
    );
    if (response.status !== 200)
      throw new Error(`Error fetching broadcast: ${response.status}`);
    return response.body.broadcast;
  }
}), updateBroadcast = (0, import_pieces_framework8.createAction)({
  auth: convertkitAuth,
  name: "broadcasts_update_broadcast",
  displayName: "Update Broadcast",
  description: "Update a broadcast",
  audience: "both",
  aiMetadata: {
    description: "Updates the content, subject, scheduling, or other fields of an existing broadcast by broadcast ID. Idempotent \u2014 repeating the same update leaves the broadcast in the same state. Requires a valid broadcast ID from List Broadcasts.",
    idempotent: !0
  },
  props: {
    broadcastId,
    content: broadcastContent,
    description,
    emailAddress: broadcastEmailAddress,
    emailLayoutTemplate,
    isPublic,
    publishedAt,
    sendAt,
    subject,
    thumbnailAlt,
    thumbnailUrl
  },
  async run(context) {
    let {
      broadcastId: broadcastId2,
      content,
      description: description2,
      emailAddress,
      emailLayoutTemplate: emailLayoutTemplate2,
      isPublic: isPublic2,
      publishedAt: publishedAt2,
      sendAt: sendAt2,
      subject: subject2,
      thumbnailAlt: thumbnailAlt2,
      thumbnailUrl: thumbnailUrl2
    } = context.propsValue, url = `${BROADCASTS_API_ENDPOINT}/${broadcastId2}`, body = {
      api_secret: context.auth.secret_text,
      content,
      description: description2,
      email_address: emailAddress,
      email_layout_template: emailLayoutTemplate2,
      public: isPublic2,
      published_at: publishedAt2,
      send_at: sendAt2,
      subject: subject2,
      thumbnail_alt: thumbnailAlt2,
      thumbnail_url: thumbnailUrl2
    }, request = {
      url,
      method: import_pieces_common4.HttpMethod.PUT,
      body,
      headers: {
        "Content-Type": "application/json"
      }
    }, response = await import_pieces_common4.httpClient.sendRequest(
      request
    );
    if (response.status !== 200)
      throw new Error(`Error updating broadcast: ${response.status}`);
    return response.body.broadcast;
  }
}), broadcastStats = (0, import_pieces_framework8.createAction)({
  auth: convertkitAuth,
  name: "broadcasts_broadcast_stats",
  displayName: "Broadcast Stats",
  description: "Get broadcast stats",
  audience: "both",
  aiMetadata: {
    description: "Retrieves delivery and engagement statistics (recipients, opens, clicks, unsubscribes) for one broadcast by ID. Pick this over Get Broadcast when performance metrics are needed rather than content. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    broadcastId
  },
  async run(context) {
    let { broadcastId: broadcastId2 } = context.propsValue, url = `${BROADCASTS_API_ENDPOINT}/${broadcastId2}/stats`, body = {
      api_secret: context.auth.secret_text
    }, request = {
      url,
      body,
      method: import_pieces_common4.HttpMethod.GET
    }, response = await import_pieces_common4.httpClient.sendRequest(
      request
    );
    if (response.status !== 200)
      throw new Error(`Error fetching broadcast stats: ${response.status}`);
    return response.body.broadcast;
  }
}), deleteBroadcast = (0, import_pieces_framework8.createAction)({
  auth: convertkitAuth,
  name: "broadcasts_delete_broadcast",
  displayName: "Delete Broadcast",
  description: "Delete a broadcast",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a broadcast by ID. Destructive and not retry-safe \u2014 a repeat call fails once the broadcast is gone, so confirm the ID via List Broadcasts first.",
    idempotent: !1
  },
  props: {
    broadcastId
  },
  async run(context) {
    let { broadcastId: broadcastId2 } = context.propsValue, url = `${BROADCASTS_API_ENDPOINT}/${broadcastId2}`, body = {
      api_secret: context.auth.secret_text
    }, request = {
      url,
      body,
      method: import_pieces_common4.HttpMethod.DELETE
    }, response = await import_pieces_common4.httpClient.sendRequest(
      request
    );
    if (response.status !== 204)
      throw new Error(`Error deleting broadcast: ${response.status}`);
    return {
      message: `Broadcast ${broadcastId2} deleted successfully`,
      status: response.status,
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/forms.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/convertkit/src/lib/common/forms/index.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var formId = import_pieces_framework9.Property.Dropdown({
  displayName: "Form",
  required: !0,
  refreshers: ["auth"],
  auth: convertkitAuth,
  options: async ({ auth }) => auth ? {
    options: (await fetchForms(auth.secret_text)).map((field) => ({
      label: field.name,
      value: field.id
    }))
  } : {
    disabled: !0,
    placeholder: "Connect your account",
    options: []
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/common/tags/index.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var tagId = import_pieces_framework10.Property.ShortText({
  displayName: "Tag Id",
  description: "The tag id",
  required: !0
}), name = import_pieces_framework10.Property.ShortText({
  displayName: "Name",
  description: "The name of the tag",
  required: !0
}), tags = import_pieces_framework10.Property.MultiSelectDropdown({
  displayName: "Tags",
  description: "Choose the Tags",
  required: !1,
  refreshers: ["auth"],
  auth: convertkitAuth,
  options: async ({ auth }) => auth ? {
    options: (await fetchTags(auth.secret_text)).map((tag2) => ({
      label: tag2.name,
      value: tag2.id
    }))
  } : {
    disabled: !0,
    placeholder: "Connect your account",
    options: []
  }
}), tagsRequired = __spreadProps(__spreadValues({}, tags), { required: !0 }), tag = import_pieces_framework10.Property.Dropdown({
  displayName: "Tag",
  description: "Choose a Tag",
  required: !0,
  refreshers: ["auth"],
  auth: convertkitAuth,
  options: async ({ auth }) => auth ? {
    options: (await fetchTags(auth.secret_text)).map((tag2) => ({
      label: tag2.name,
      value: tag2.id
    }))
  } : {
    disabled: !0,
    placeholder: "Connect your account",
    options: []
  }
}), tagsPageNumber = import_pieces_framework10.Property.Number({
  displayName: "Page",
  description: "Each page of results will contain up to 50 tags.",
  required: !1,
  defaultValue: 1
}), sortOrder2 = import_pieces_framework10.Property.StaticDropdown({
  displayName: "Sort Order",
  description: "Sort order",
  required: !1,
  options: {
    options: [
      { label: "Ascending", value: "asc" },
      { label: "Descending", value: "desc" }
    ]
  }
}), subscriberState = import_pieces_framework10.Property.StaticDropdown({
  displayName: "Subscriber State",
  description: "Subscriber state",
  required: !1,
  options: {
    options: [
      { label: "Active", value: "active" },
      { label: "canceled", value: "canceled" }
    ]
  }
}), tagIdByEmail = import_pieces_framework10.Property.Dropdown({
  displayName: "Tag",
  description: "The tag to remove",
  required: !0,
  refreshers: ["auth", "email"],
  auth: convertkitAuth,
  options: async (params) => {
    let { auth, email } = params;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your account.",
        options: []
      };
    if (!email)
      return {
        disabled: !0,
        placeholder: "Provide a subscriber email address.",
        options: []
      };
    let subscriber = await fetchSubscriberByEmail(
      auth.toString(),
      email.toString()
    );
    if (!subscriber)
      return {
        disabled: !0,
        placeholder: "No subscribers found for this email address.",
        options: []
      };
    let subscriberId2 = subscriber.id;
    return {
      disabled: !1,
      placeholder: "Choose a tag",
      options: (await fetchSubscribedTags(
        auth.toString(),
        subscriberId2.toString()
      )).map((tag2) => ({
        label: tag2.name,
        value: tag2.id
      }))
    };
  }
}), tagIdBySubscriberId = import_pieces_framework10.Property.Dropdown({
  displayName: "Tag",
  description: "The tag to remove",
  required: !0,
  auth: convertkitAuth,
  refreshers: ["auth", "subscriberId"],
  options: async ({ auth, subscriberId: subscriberId2 }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your account and",
        options: []
      };
    if (!subscriberId2)
      return {
        disabled: !0,
        placeholder: "Provide a subscriber id.",
        options: []
      };
    {
      let tags2 = await fetchSubscribedTags(
        auth.secret_text,
        subscriberId2.toString()
      );
      return tags2 ? {
        options: tags2.map((tag2) => ({
          label: tag2.name,
          value: tag2.id
        }))
      } : {
        disabled: !0,
        placeholder: "Something went wrong.",
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/forms.ts
var listForms = (0, import_pieces_framework11.createAction)({
  auth: convertkitAuth,
  name: "forms_list_forms",
  displayName: "List Forms",
  description: "Returns a list of all forms",
  audience: "both",
  aiMetadata: {
    description: "Lists all forms and landing pages in the account with their IDs and names. Use it to find a form ID before adding subscribers to a form or listing form subscriptions. Takes no inputs; read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  run(context) {
    return fetchForms(context.auth.secret_text);
  }
}), allFieldsRequiredRefreshers = __spreadProps(__spreadValues({}, allFields), {
  required: !1,
  refreshers: ["auth", "formId"]
}), addSubscriberToForm = (0, import_pieces_framework11.createAction)({
  auth: convertkitAuth,
  name: "forms_add_subscriber_to_form",
  displayName: "Add Subscriber To Form",
  description: "Add a subscriber to a form",
  audience: "both",
  aiMetadata: {
    description: "Subscribes an email address to a specific form, optionally setting first name, tags, and custom field values. This is the standard way to add a new subscriber in ConvertKit. Effectively idempotent \u2014 re-subscribing the same email to the same form upserts rather than creating a duplicate.",
    idempotent: !0
  },
  props: {
    formId,
    email: subscriberEmail,
    firstName: subscriberFirstName,
    tags,
    fields: allFieldsRequiredRefreshers
  },
  async run(context) {
    let { formId: formId2, email, firstName, tags: tags2, fields } = context.propsValue, url = `${FORMS_API_ENDPOINT}/${formId2}/subscribe`, body = {
      api_secret: context.auth.secret_text,
      email,
      first_name: firstName,
      tags: tags2,
      fields
    }, request = {
      url,
      method: import_pieces_common5.HttpMethod.POST,
      body
    }, response = await import_pieces_common5.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(`Error adding subscriber to form: ${response.status}`);
    return response.body.subscription;
  }
}), listFormSubscriptions = (0, import_pieces_framework11.createAction)({
  auth: convertkitAuth,
  name: "forms_list_form_subscriptions",
  displayName: "List Form Subscriptions",
  description: "List form subscriptions",
  audience: "both",
  aiMetadata: {
    description: "Lists the subscriptions for one form by form ID, one entry per subscriber who signed up through it. Use it to audit who joined via a given form. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    formId
  },
  async run(context) {
    let url = `${FORMS_API_ENDPOINT}/${context.propsValue.formId}/subscriptions`, body = {
      api_secret: context.auth.secret_text
    }, request = {
      url,
      body,
      method: import_pieces_common5.HttpMethod.GET
    }, response = await import_pieces_common5.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(`Error listing form subscriptions: ${response.status}`);
    return response.body.subscriptions;
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/sequences.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/convertkit/src/lib/common/sequences/index.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var sequenceId = import_pieces_framework12.Property.ShortText({
  displayName: "Sequence ID",
  description: "The sequence ID",
  required: !0
}), sequenceIdDropdown = import_pieces_framework12.Property.Dropdown({
  displayName: "Sequence",
  required: !0,
  refreshers: ["auth"],
  auth: convertkitAuth,
  options: async ({ auth }) => auth ? {
    options: (await fetchSequences(auth.secret_text)).map((field) => ({
      label: field.name,
      value: field.id
    }))
  } : {
    disabled: !0,
    placeholder: "Connect your account",
    options: []
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/sequences.ts
var listSequences = (0, import_pieces_framework13.createAction)({
  auth: convertkitAuth,
  name: "sequences_list_sequences",
  displayName: "List Sequences",
  description: "Returns a list of all sequences",
  audience: "both",
  aiMetadata: {
    description: "Lists all sequences (automated email courses) in the account with their IDs and names. Use it to find a sequence ID before enrolling subscribers or listing enrollments. Takes no inputs; read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  run(context) {
    return fetchSequences(context.auth.secret_text);
  }
}), addSubscriberToSequence = (0, import_pieces_framework13.createAction)({
  auth: convertkitAuth,
  name: "sequences_add_subscriber_to_sequence",
  displayName: "Add Subscriber To Sequence",
  description: "Add a subscriber to a sequence",
  audience: "both",
  aiMetadata: {
    description: "Enrolls an email address in a sequence so it starts receiving that automated email course, optionally setting first name, tags, and custom field values. Effectively idempotent \u2014 re-subscribing the same email to the same sequence upserts instead of duplicating the enrollment.",
    idempotent: !0
  },
  props: {
    sequenceId: sequenceIdDropdown,
    email: subscriberEmail,
    firstName: subscriberFirstName,
    tags,
    fields: allFields
  },
  async run(context) {
    let { sequenceId: sequenceId2, email, firstName, tags: tags2, fields } = context.propsValue, url = `${SEQUENCES_API_ENDPOINT}/${sequenceId2}/subscribe`, body = {
      api_secret: context.auth.secret_text,
      email,
      first_name: firstName,
      tags: tags2,
      fields
    }, request = {
      url,
      method: import_pieces_common6.HttpMethod.POST,
      body
    }, response = await import_pieces_common6.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(
        `Error adding subscriber to sequence: ${response.status}`
      );
    return response.body.subscription;
  }
}), listSubscriptionsToSequence = (0, import_pieces_framework13.createAction)({
  auth: convertkitAuth,
  name: "sequences_list_subscriptions_to_sequence",
  displayName: "List Subscriptions To Sequence",
  description: "List all subscriptions to a sequence",
  audience: "both",
  aiMetadata: {
    description: "Lists all subscriber enrollments for one sequence by sequence ID. Use List Sequences first to find the ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    sequenceId: sequenceIdDropdown
  },
  async run(context) {
    let url = `${SEQUENCES_API_ENDPOINT}/${context.propsValue.sequenceId}/subscriptions`, body = {
      api_secret: context.auth.secret_text
    }, request = {
      url,
      method: import_pieces_common6.HttpMethod.GET,
      body
    }, response = await import_pieces_common6.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(
        `Error listing subscriptions to sequence: ${response.status}`
      );
    return response.body.subscriptions;
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/tags.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var listTags = (0, import_pieces_framework14.createAction)({
  auth: convertkitAuth,
  name: "tags_list_tags",
  displayName: "List Tags",
  description: "Returns a list of all tags",
  audience: "both",
  aiMetadata: {
    description: "Lists every tag in the account with its ID and name. Use it to find a tag ID before tagging or untagging subscribers, or before listing a tag's subscriptions. Takes no inputs; read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  run(context) {
    return fetchTags(context.auth.secret_text);
  }
}), createTag = (0, import_pieces_framework14.createAction)({
  auth: convertkitAuth,
  name: "tags_create_tag",
  displayName: "Create Tag",
  description: "Create a tag",
  audience: "both",
  aiMetadata: {
    description: "Creates a new tag with the given name. Not idempotent \u2014 calling it again can create a duplicate, so check List Tags for an existing tag first.",
    idempotent: !1
  },
  props: {
    name
  },
  async run(context) {
    let url = TAGS_API_ENDPOINT, body = {
      api_secret: context.auth.secret_text,
      tag: { name: context.propsValue.name }
    }, request = {
      url,
      method: import_pieces_common7.HttpMethod.POST,
      body
    }, response = await import_pieces_common7.httpClient.sendRequest(request);
    if (response.status !== 201)
      throw new Error(`Error creating tag: ${response.status}`);
    return response.body;
  }
}), tagSubscriber = (0, import_pieces_framework14.createAction)({
  auth: convertkitAuth,
  name: "tags_tag_subscriber",
  displayName: "Tag Subscriber",
  description: "Tag a subscriber",
  audience: "both",
  aiMetadata: {
    description: "Applies one or more existing tags to a subscriber by email address (subscribing the email to the first tag and attaching the rest), optionally setting first name and custom fields; at least one tag is required. Effectively idempotent \u2014 re-applying the same tags converges to the same state.",
    idempotent: !0
  },
  props: {
    email: subscriberEmail,
    // tagId: tag,
    firstName: subscriberFirstName,
    tags: tagsRequired,
    fields: allFields
  },
  async run(context) {
    let { email, firstName, tags: tags2, fields } = context.propsValue;
    if (!tags2 || tags2.length === 0)
      throw new Error("At least one tag is required");
    let tagId2 = tags2[0], url = `${TAGS_API_ENDPOINT}/${tagId2}/subscribe`, body = {
      email,
      first_name: firstName,
      tags: tags2,
      fields,
      api_secret: context.auth.secret_text
    }, request = {
      url,
      method: import_pieces_common7.HttpMethod.POST,
      body
    }, response = await import_pieces_common7.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(`Error tagging subscriber: ${response.status}`);
    return response.body.subscription;
  }
}), removeTagFromSubscriberByEmail = (0, import_pieces_framework14.createAction)({
  auth: convertkitAuth,
  name: "tags_remove_tag_from_subscriber_by_email",
  displayName: "Remove Tag From Subscriber By Email",
  description: "Remove a tag from a subscriber by email",
  audience: "both",
  aiMetadata: {
    description: "Removes a single tag from a subscriber identified by email address. Use the By Id variant when the numeric subscriber ID is already known. Treated as non-idempotent \u2014 a retry may error once the tag is already removed, though the end state is the same.",
    idempotent: !1
  },
  props: {
    email: subscriberEmail,
    tagId: tagIdByEmail
  },
  async run(context) {
    let { email, tagId: tagId2 } = context.propsValue, url = `${TAGS_API_ENDPOINT}/${tagId2}/unsubscribe`, body = {
      email,
      api_secret: context.auth.secret_text
    }, request = {
      url,
      method: import_pieces_common7.HttpMethod.POST,
      body
    }, response = await import_pieces_common7.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(`Error removing tag from subscriber: ${response.status}`);
    return response.body;
  }
}), removeTagFromSubscriberById = (0, import_pieces_framework14.createAction)({
  auth: convertkitAuth,
  name: "tags_remove_tag_from_subscriber_by_id",
  displayName: "Remove Tag From Subscriber By Id",
  description: "Remove a tag from a subscriber by id",
  audience: "both",
  aiMetadata: {
    description: "Removes a single tag from a subscriber identified by numeric subscriber ID. Use the By Email variant when only an address is known. Treated as non-idempotent \u2014 a retry may error once the tag is already removed.",
    idempotent: !1
  },
  props: {
    subscriberId,
    tagId: tagIdBySubscriberId
  },
  async run(context) {
    let { subscriberId: subscriberId2, tagId: tagId2 } = context.propsValue, url = `${TAGS_API_ENDPOINT}/${tagId2}/unsubscribe`, body = {
      id: subscriberId2,
      api_secret: context.auth.secret_text
    }, request = {
      url,
      method: import_pieces_common7.HttpMethod.POST,
      body
    }, response = await import_pieces_common7.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(`Error removing tag from subscriber: ${response.status}`);
    return response.body;
  }
}), listSubscriptionsToATag = (0, import_pieces_framework14.createAction)({
  auth: convertkitAuth,
  name: "tags_list_subscriptions_to_tag",
  displayName: "List Subscriptions To Tag",
  description: "List all subscriptions to a tag",
  audience: "both",
  aiMetadata: {
    description: "Lists the subscribers subscribed to a specific tag, with paging, sort order, and subscriber-state filtering. Use List Tags first to find the tag ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    tagId: tag,
    page: tagsPageNumber,
    sortOrder: sortOrder2,
    subscriberState
  },
  async run(context) {
    let { tagId: tagId2, page, sortOrder: sortOrder3, subscriberState: subscriberState2 } = context.propsValue, url = `${TAGS_API_ENDPOINT}/${tagId2}/subscriptions?`, body = {
      api_secret: context.auth.secret_text,
      page,
      sort_order: sortOrder3,
      subscriber_state: subscriberState2
    }, request = {
      url,
      method: import_pieces_common7.HttpMethod.GET,
      body
    }, response = await import_pieces_common7.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(`Error listing subscriptions to tag: ${response.status}`);
    return response.body.subscriptions;
  }
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/purchases.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/convertkit/src/lib/common/purchases/index.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), purchaseId = import_pieces_framework15.Property.ShortText({
  displayName: "Purchase ID",
  description: "The purchase ID",
  required: !0
}), purchaserEmailAddress = import_pieces_framework15.Property.ShortText({
  displayName: "Email Address",
  description: "The email address of the subscriber",
  required: !0
}), purchasesPageNumber = import_pieces_framework15.Property.Number({
  displayName: "Page",
  description: "Page number. Each page of results will contain up to 50 purchases.",
  required: !0,
  defaultValue: 1
}), transactionId = import_pieces_framework15.Property.Number({
  displayName: "Transaction ID",
  description: "The transaction ID",
  required: !0
}), productId = import_pieces_framework15.Property.Number({
  displayName: "Product ID",
  description: "The product ID",
  required: !0
}), status = import_pieces_framework15.Property.StaticDropdown({
  displayName: "Status",
  description: "The status of the purchase",
  required: !1,
  options: {
    options: [
      { label: "paid", value: "paid" },
      { label: "pending", value: "pending" },
      { label: "failed", value: "failed" }
    ]
  }
}), currency = import_pieces_framework15.Property.StaticDropdown({
  displayName: "Currency",
  description: "The currency of the purchase",
  required: !0,
  options: {
    options: [
      { label: "USD", value: "USD" },
      { label: "JPY", value: "JPY" },
      { label: "GBP", value: "GBP" },
      { label: "EUR", value: "EUR" },
      { label: "CAD", value: "CAD" },
      { label: "AUD", value: "AUD" },
      { label: "NZD", value: "NZD" },
      { label: "CHF", value: "CHF" },
      { label: "HKD", value: "HKD" },
      { label: "SGD", value: "SGD" },
      { label: "SEK", value: "SEK" },
      { label: "DKK", value: "DKK" },
      { label: "PLN", value: "PLN" },
      { label: "NOK", value: "NOK" },
      { label: "HUF", value: "HUF" },
      { label: "CZK", value: "CZK" },
      { label: "ILS", value: "ILS" },
      { label: "MXN", value: "MXN" },
      { label: "MYR", value: "MYR" },
      { label: "BRL", value: "BRL" },
      { label: "PHP", value: "PHP" },
      { label: "TWD", value: "TWD" },
      { label: "THB", value: "THB" },
      { label: "TRY", value: "TRY" },
      { label: "RUB", value: "RUB" },
      { label: "INR", value: "INR" },
      { label: "KRW", value: "KRW" },
      { label: "AED", value: "AED" },
      { label: "SAR", value: "SAR" },
      { label: "ZAR", value: "ZAR" }
    ]
  }
}), transactionTime = import_pieces_framework15.Property.DateTime({
  displayName: "Transaction Time",
  description: "The transaction time",
  required: !1
}), subtotal = import_pieces_framework15.Property.Number({
  displayName: "Subtotal",
  description: "The subtotal",
  required: !1
}), shipping = import_pieces_framework15.Property.Number({
  displayName: "Shipping",
  description: "The shipping",
  required: !1
}), discount = import_pieces_framework15.Property.Number({
  displayName: "Discount",
  description: "The discount",
  required: !1
}), tax = import_pieces_framework15.Property.Number({
  displayName: "Tax",
  description: "The tax",
  required: !1
}), total = import_pieces_framework15.Property.Number({
  displayName: "Total",
  description: "The total",
  required: !1
}), products = {
  pid: import_pieces_framework15.Property.Number({
    displayName: "Product ID",
    description: "The product ID",
    required: !0
  }),
  lid: import_pieces_framework15.Property.Number({
    displayName: "Line Item ID",
    description: "The line item ID",
    required: !0
  }),
  name: import_pieces_framework15.Property.ShortText({
    displayName: "Name",
    description: "The name of the product",
    required: !0
  }),
  sku: import_pieces_framework15.Property.ShortText({
    displayName: "SKU",
    description: "The SKU of the product",
    required: !1
  }),
  unit_price: import_pieces_framework15.Property.Number({
    displayName: "Unit Price",
    description: "The unit price of the product",
    required: !0
  }),
  quantity: import_pieces_framework15.Property.Number({
    displayName: "Quantity",
    description: "The quantity of the product",
    required: !0
  })
}, multipleProducts = import_pieces_framework15.Property.Json({
  displayName: "Products",
  description: "The products",
  required: !0,
  defaultValue: [
    {
      pid: 9999,
      lid: 7777,
      name: "Floppy Disk (512k)",
      sku: "7890-ijkl",
      unit_price: 5,
      quantity: 2
    },
    {
      pid: 5555,
      lid: 7778,
      name: "Telephone Cord (data)",
      sku: "mnop-1234",
      unit_price: 10,
      quantity: 1
    }
  ]
});

// ../ap/packages/pieces/community/convertkit/src/lib/actions/purchases.ts
var listPurchases = (0, import_pieces_framework16.createAction)({
  auth: convertkitAuth,
  name: "purchases_list_purchases",
  displayName: "List Purchases",
  description: "Returns a list of all purchases",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a paginated list of all purchase records in the account; pass a page number to go beyond the first page. For purchases scoped to one subscriber, product, form, or sequence, prefer the dedicated filtered list actions. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    page: purchasesPageNumber
  },
  run(context) {
    let page = context.propsValue.page || 1;
    return fetchPurchases(context.auth.secret_text, page);
  }
}), getPurchaseById = (0, import_pieces_framework16.createAction)({
  auth: convertkitAuth,
  name: "purchases_get_purchase_by_id",
  displayName: "Get Purchase By Id",
  description: "Returns data for a single purchase",
  audience: "both",
  aiMetadata: {
    description: "Fetches one purchase record by its ConvertKit purchase ID (not the external transaction ID). Use List Purchases first if only the transaction details are known. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    purchaseId
  },
  async run(context) {
    let { purchaseId: purchaseId2 } = context.propsValue, url = `${PURCHASES_API_ENDPOINT}/${purchaseId2}`, body = {
      api_secret: context.auth.secret_text
    }, request = {
      url,
      method: import_pieces_common8.HttpMethod.GET,
      body
    }, response = await import_pieces_common8.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(`Error fetching purchase: ${response.status}`);
    return response.body;
  }
}), createSinglePurchase = (0, import_pieces_framework16.createAction)({
  auth: convertkitAuth,
  name: "purchases_create_purchase",
  displayName: "Create Purchase",
  description: "Creates a new purchase",
  audience: "both",
  aiMetadata: {
    description: "Records a purchase with a single product line item against a subscriber email, including transaction ID, status, currency, and amounts. Use Create Multiple Purchases instead when an order contains several products. Not idempotent \u2014 repeat calls create duplicate purchase records.",
    idempotent: !1
  },
  props: __spreadValues({
    transactionId,
    transactionTime,
    emailAddress: purchaserEmailAddress,
    firstName: subscriberFirstName,
    status,
    currency,
    subtotal,
    shipping,
    discount,
    tax,
    total
  }, products),
  async run(context) {
    let _a = context.propsValue, {
      transactionId: transactionId2,
      transactionTime: transactionTime2,
      emailAddress,
      firstName,
      status: status2,
      currency: currency2,
      subtotal: subtotal2,
      shipping: shipping2,
      discount: discount2,
      tax: tax2,
      total: total2
    } = _a, products2 = __objRest(_a, [
      "transactionId",
      "transactionTime",
      "emailAddress",
      "firstName",
      "status",
      "currency",
      "subtotal",
      "shipping",
      "discount",
      "tax",
      "total"
    ]), url = PURCHASES_API_ENDPOINT, body = {
      api_secret: context.auth.secret_text,
      purchase: {
        transaction_id: transactionId2,
        status: status2,
        first_name: firstName,
        email_address: emailAddress,
        currency: currency2,
        transaction_time: transactionTime2,
        subtotal: subtotal2,
        shipping: shipping2,
        discount: discount2,
        tax: tax2,
        total: total2,
        products: [products2]
      }
    }, request = {
      url,
      method: import_pieces_common8.HttpMethod.POST,
      body
    }, response = await import_pieces_common8.httpClient.sendRequest(request);
    if (response.status !== 201)
      throw new Error(`Error creating purchase: ${response.status}`);
    return response.body;
  }
}), createPurchases = (0, import_pieces_framework16.createAction)({
  auth: convertkitAuth,
  name: "purchases_create_multiple_purchases",
  displayName: "Create Multiple Purchases",
  description: "Creates multiple purchases",
  audience: "both",
  aiMetadata: {
    description: "Records one purchase transaction containing multiple product line items for a subscriber email. Pick this over Create Purchase for multi-product orders. Not idempotent \u2014 repeat calls create duplicate purchase records.",
    idempotent: !1
  },
  props: {
    transactionId,
    transactionTime,
    emailAddress: purchaserEmailAddress,
    firstName: subscriberFirstName,
    status,
    currency,
    subtotal,
    shipping,
    discount,
    tax,
    total,
    multipleProducts
  },
  async run(context) {
    let {
      transactionId: transactionId2,
      transactionTime: transactionTime2,
      emailAddress,
      firstName,
      status: status2,
      currency: currency2,
      subtotal: subtotal2,
      shipping: shipping2,
      discount: discount2,
      tax: tax2,
      total: total2,
      multipleProducts: multipleProducts2
    } = context.propsValue, url = PURCHASES_API_ENDPOINT, body = {
      api_secret: context.auth.secret_text,
      purchase: {
        transaction_id: transactionId2,
        status: status2,
        email_address: emailAddress,
        first_name: firstName,
        currency: currency2,
        transaction_time: transactionTime2,
        subtotal: subtotal2,
        shipping: shipping2,
        discount: discount2,
        tax: tax2,
        total: total2,
        products: multipleProducts2
      }
    }, request = {
      url,
      method: import_pieces_common8.HttpMethod.POST,
      body
    }, response = await import_pieces_common8.httpClient.sendRequest(request);
    if (response.status !== 201)
      throw new Error(`Error creating purchase: ${response.status}`);
    return response.body;
  }
}), listPurchasesForSubscriber = (0, import_pieces_framework16.createAction)({
  auth: convertkitAuth,
  name: "purchases_list_purchases_for_subscriber",
  displayName: "List Purchases For Subscriber",
  description: "Returns a list of all purchases for a subscriber",
  audience: "both",
  aiMetadata: {
    description: "Lists all purchases attributed to one subscriber by numeric subscriber ID. Use Get Subscriber By Email first if only an email address is known. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    subscriberId
  },
  async run(context) {
    let { subscriberId: subscriberId2 } = context.propsValue, url = PURCHASES_API_ENDPOINT, body = {
      api_secret: context.auth.secret_text,
      subscriber_id: subscriberId2
    }, request = {
      url,
      method: import_pieces_common8.HttpMethod.GET,
      body
    }, response = await import_pieces_common8.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(`Error fetching purchases: ${response.status}`);
    return response.body.purchases;
  }
}), listPurchasesForProduct = (0, import_pieces_framework16.createAction)({
  auth: convertkitAuth,
  name: "purchases_list_purchases_for_product",
  displayName: "List Purchases For Product",
  description: "Returns a list of all purchases for a product",
  audience: "both",
  aiMetadata: {
    description: "Lists all purchases of a specific product by product ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    productId
  },
  async run(context) {
    let { productId: productId2 } = context.propsValue, url = `${PURCHASES_API_ENDPOINT}?api_secret=${context.auth.secret_text}&product_id=${productId2}`, response = await fetch(url);
    return response.ok ? await response.json() : { success: !1, message: "Error fetching purchases" };
  }
}), listPurchasesForForm = (0, import_pieces_framework16.createAction)({
  auth: convertkitAuth,
  name: "purchases_list_purchases_for_form",
  displayName: "List Purchases For Form",
  description: "Returns a list of all purchases for a form",
  audience: "both",
  aiMetadata: {
    description: "Lists all purchases associated with a specific form by form ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    formId
  },
  async run(context) {
    let { formId: formId2 } = context.propsValue, url = `${PURCHASES_API_ENDPOINT}?api_secret=${context.auth.secret_text}&form_id=${formId2}`, response = await fetch(url);
    return response.ok ? await response.json() : { success: !1, message: "Error fetching purchases" };
  }
}), listPurchasesForSequence = (0, import_pieces_framework16.createAction)({
  auth: convertkitAuth,
  name: "purchases_list_purchases_for_sequence",
  displayName: "List Purchases For Sequence",
  description: "Returns a list of all purchases for a sequence",
  audience: "both",
  aiMetadata: {
    description: "Lists all purchases associated with a specific sequence by sequence ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    sequenceId
  },
  async run(context) {
    let { sequenceId: sequenceId2 } = context.propsValue, url = `${PURCHASES_API_ENDPOINT}?api_secret=${context.auth.secret_text}&sequence_id=${sequenceId2}`, response = await fetch(url);
    return response.ok ? await response.json() : { success: !1, message: "Error fetching purchases" };
  }
});

// ../ap/packages/pieces/community/convertkit/src/index.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/convertkit/src/lib/triggers/index.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var sampleData = {
  rule: {
    id: 1,
    account_id: 2,
    event: {
      name: "course_complete",
      sequence_id: 3
    }
  }
}, addTag = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_subscriber_tag_add",
  displayName: "Tag added to subscriber",
  description: "Trigger when a tag is added to a subscriber",
  aiMetadata: {
    description: "Fires when the selected tag is added to a subscriber in ConvertKit (Kit), via webhook. Returns the tagged subscriber record; use it to react to segmentation changes such as enrolling the subscriber elsewhere or syncing the tag to another system."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {
    tagId: tag
  },
  sampleData,
  async onEnable(context) {
    var _a;
    let { tagId: tagId2 } = context.propsValue, payload = {
      event: {
        name: "subscriber.tag_add",
        tag_id: tagId2
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put(
      "_webhook_subscriber_tag_add",
      {
        ruleId
      }
    ));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_subscriber_tag_add"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    return [context.payload.body.subscriber];
  }
}), removeTag = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_subscriber_tag_remove",
  displayName: "Tag removed from subscriber",
  description: "Trigger when a tag is removed from a subscriber",
  aiMetadata: {
    description: "Fires when the selected tag is removed from a subscriber in ConvertKit (Kit), via webhook. Returns the affected subscriber record; useful for reversing automations or updating external systems when a subscriber leaves a segment."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {
    tagId: tag
  },
  sampleData,
  async onEnable(context) {
    var _a;
    let { tagId: tagId2 } = context.propsValue, payload = {
      event: {
        name: "subscriber.tag_remove",
        tag_id: tagId2
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put(
      "_webhook_subscriber_tag_remove",
      {
        ruleId
      }
    ));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_subscriber_tag_remove"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    return [context.payload.body.subscriber];
  }
}), subscriberActivated = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_subscriber_activated",
  displayName: "Subscriber activated",
  description: "Trigger when a subscriber is activated. This happens when a subscriber confirms their subscription.",
  aiMetadata: {
    description: "Fires when a subscriber becomes active in ConvertKit (Kit), i.e. they confirm their subscription (double opt-in), via webhook. Returns the activated subscriber record; use it to welcome new confirmed subscribers or add them to downstream systems."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData,
  async onEnable(context) {
    var _a;
    let payload = {
      event: {
        name: "subscriber.subscriber_activate"
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put(
      "_webhook_subscriber_activated",
      {
        ruleId
      }
    ));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_subscriber_activated"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    return [context.payload.body.subscriber];
  }
}), subscriberUnsubscribed = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_subscriber_unsubscribed",
  displayName: "Subscriber unsubscribed",
  description: "Trigger when a subscriber is unsubscribed",
  aiMetadata: {
    description: "Fires when a subscriber unsubscribes from the ConvertKit (Kit) account, via webhook. Returns the unsubscribed subscriber record; use it to suppress the contact in other tools or record the opt-out."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData,
  async onEnable(context) {
    var _a;
    let payload = {
      event: {
        name: "subscriber.subscriber_unsubscribe"
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put(
      "_webhook_subscriber_unsubscribed",
      {
        ruleId
      }
    ));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_subscriber_unsubscribed"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    return [context.payload.body.subscriber];
  }
}), subscriberBounced = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_subscriber_bounced",
  displayName: "Subscriber bounced",
  description: "Trigger when a subscriber bounced. This happens when an email is sent to a subscriber and the email bounces.",
  aiMetadata: {
    description: "Fires when an email sent to a subscriber bounces in ConvertKit (Kit), marking the subscriber as bounced, via webhook. Returns the bounced subscriber record; use it to clean lists or flag invalid email addresses."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData,
  async onEnable(context) {
    var _a;
    let payload = {
      event: {
        name: "subscriber.subscriber_bounce"
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put(
      "_webhook_subscriber_bounced",
      {
        ruleId
      }
    ));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_subscriber_bounced"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    return [context.payload.body.subscriber];
  }
}), subscriberComplained = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_subscriber_complained",
  displayName: "Subscriber complained",
  description: "Trigger when a subscriber complained. This happens when a subscriber marks an email as spam.",
  aiMetadata: {
    description: "Fires when a subscriber marks an email as spam, putting them in the complained state in ConvertKit (Kit), via webhook. Returns the complaining subscriber record; use it to suppress the contact and protect sender reputation."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData,
  async onEnable(context) {
    var _a;
    let payload = {
      event: {
        name: "subscriber.subscriber_complain"
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put(
      "_webhook_subscriber_complained",
      {
        ruleId
      }
    ));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_subscriber_complained"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    return [context.payload.body.subscriber];
  }
}), formSubscribed = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_form_subscribed",
  displayName: "Form subscribed",
  description: "Trigger when a form is subscribed",
  aiMetadata: {
    description: "Fires when someone subscribes through the selected ConvertKit (Kit) form, via webhook. Returns the new subscriber record; use it to start onboarding sequences or sync new sign-ups to a CRM."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {
    formId
  },
  sampleData,
  async onEnable(context) {
    var _a;
    let { formId: formId2 } = context.propsValue, payload = {
      event: {
        name: "subscriber.form_subscribe",
        form_id: formId2
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put("_webhook_form_subscribed", {
      ruleId
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_form_subscribed"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    return [context.payload.body.subscriber];
  }
}), sequenceSubscribed = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_sequence_subscribed",
  displayName: "Sequence subscribed",
  description: "Trigger when a sequence is subscribed",
  aiMetadata: {
    description: "Fires when a subscriber is added to the selected email sequence (course) in ConvertKit (Kit), via webhook. Returns the enrolled subscriber record; use it to track enrollments or mirror them in other systems."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {
    sequenceIdChoice: sequenceIdDropdown
  },
  sampleData,
  async onEnable(context) {
    var _a;
    let { sequenceIdChoice } = context.propsValue, payload = {
      event: {
        name: "subscriber.course_subscribe",
        sequence_id: sequenceIdChoice
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put(
      "_webhook_sequence_subscribed",
      {
        ruleId
      }
    ));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_sequence_subscribed"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    return [context.payload.body.subscriber];
  }
}), sequenceCompleted = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_sequence_completed",
  displayName: "Sequence completed",
  description: "Trigger when a sequence is completed",
  aiMetadata: {
    description: "Fires when a subscriber finishes the last email of the selected sequence (course) in ConvertKit (Kit), via webhook. Returns the subscriber record; use it to follow up after a course ends, e.g. with an offer or a next sequence."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {
    sequenceIdChoice: sequenceIdDropdown
  },
  sampleData,
  async onEnable(context) {
    var _a;
    let { sequenceIdChoice } = context.propsValue, payload = {
      event: {
        name: "subscriber.course_complete",
        sequence_id: sequenceIdChoice
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put(
      "_webhook_sequence_completed",
      {
        ruleId
      }
    ));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_sequence_completed"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    return [context.payload.body.subscriber];
  }
}), linkClicked = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_link_clicked",
  displayName: "Link clicked",
  description: "Trigger when a link is clicked",
  aiMetadata: {
    description: "Fires when a subscriber clicks the specified link URL in a ConvertKit (Kit) email, via webhook. Returns the clicking subscriber record plus the watched link URL; use it to score engagement or trigger interest-based follow-ups."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {
    initiatorValue
  },
  sampleData,
  async onEnable(context) {
    var _a;
    let { initiatorValue: initiatorValue2 } = context.propsValue, payload = {
      event: {
        name: "subscriber.link_click",
        initiator_value: initiatorValue2
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put("_webhook_link_clicked", {
      ruleId
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_link_clicked"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    let { initiatorValue: initiatorValue2 } = context.propsValue;
    return [context.payload.body.subscriber, initiatorValue2];
  }
}), productPurchased = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_product_purchased",
  displayName: "Product purchased",
  description: "Trigger when a product is purchased",
  aiMetadata: {
    description: "Fires when a subscriber purchases the selected ConvertKit (Kit) Commerce product, via webhook. Returns the purchasing subscriber record plus the product ID; use it to deliver the product, tag buyers, or record the sale elsewhere."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {
    productId
  },
  sampleData,
  async onEnable(context) {
    var _a;
    let { productId: productId2 } = context.propsValue, payload = {
      event: {
        name: "subscriber.product_purchase",
        product_id: productId2
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put("_webhook_product_purchased", {
      ruleId
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_product_purchased"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    let { productId: productId2 } = context.propsValue;
    return [context.payload.body.subscriber, productId2];
  }
}), purchaseCreated = (0, import_pieces_framework17.createTrigger)({
  auth: convertkitAuth,
  name: "webhook_purchase_created",
  displayName: "Purchase created",
  description: "Trigger when a purchase is created",
  aiMetadata: {
    description: "Fires when any new purchase record is created in the ConvertKit (Kit) account (across all products), via webhook. Returns the subscriber associated with the purchase; use it for account-wide sales notifications or bookkeeping."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData,
  async onEnable(context) {
    var _a;
    let payload = {
      event: {
        name: "purchase.purchase_create"
      },
      target_url: context.webhookUrl
    }, ruleId = (await createWebhook(context.auth.secret_text, payload)).id;
    await ((_a = context.store) == null ? void 0 : _a.put("_webhook_purchase_created", {
      ruleId
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_webhook_purchase_created"
    ));
    response != null && await removeWebhook(context.auth.secret_text, response.ruleId);
  },
  async run(context) {
    return [context.payload.body.purchase.subscriber];
  }
});

// ../ap/packages/pieces/community/convertkit/src/index.ts
var convertkitAuth = import_pieces_framework18.PieceAuth.SecretText({
  displayName: "API Secret",
  description: "Enter your API Secret key",
  required: !0
}), convertkit = (0, import_pieces_framework18.createPiece)({
  displayName: "ConvertKit",
  description: "Email marketing for creators",
  auth: convertkitAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/convertkit.png",
  categories: [import_pieces_framework19.PieceCategory.MARKETING],
  authors: ["Gunther-Schulz", "kishanprmr", "abuaboud"],
  actions: [
    getSubscriberById,
    getSubscriberByEmail,
    listSubscribers,
    updateSubscriber,
    unsubscribeSubscriber,
    listSubscriberTagsByEmail,
    listTagsBySubscriberId,
    createWebhook2,
    deleteWebhook,
    listFields,
    createField,
    updateField,
    deleteField,
    listBroadcasts,
    createBroadcast,
    getBroadcastById,
    updateBroadcast,
    deleteBroadcast,
    broadcastStats,
    listForms,
    addSubscriberToForm,
    listFormSubscriptions,
    listSequences,
    addSubscriberToSequence,
    listSubscriptionsToSequence,
    listTags,
    createTag,
    tagSubscriber,
    removeTagFromSubscriberByEmail,
    removeTagFromSubscriberById,
    listSubscriptionsToATag,
    listPurchases,
    getPurchaseById,
    createSinglePurchase,
    createPurchases
  ],
  triggers: [
    addTag,
    removeTag,
    subscriberActivated,
    subscriberUnsubscribed,
    subscriberBounced,
    subscriberComplained,
    formSubscribed,
    sequenceSubscribed,
    sequenceCompleted,
    linkClicked,
    productPurchased,
    purchaseCreated
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  convertkit,
  convertkitAuth
});
