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

// ../ap/packages/pieces/community/kustomer/src/index.ts
var index_exports = {};
__export(index_exports, {
  kustomer: () => kustomer
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/kustomer/src/lib/actions/create-conversation.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/kustomer/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/kustomer/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function validateAuth({
  apiKey
}) {
  await sendRequest({
    apiKey,
    method: import_pieces_common.HttpMethod.GET,
    path: "/customers"
  });
}
async function createCustomer({
  apiKey,
  customer
}) {
  return sendRequest({
    apiKey,
    method: import_pieces_common.HttpMethod.POST,
    path: "/customers",
    body: customer
  });
}
async function createConversation({
  apiKey,
  conversation
}) {
  return sendRequest({
    apiKey,
    method: import_pieces_common.HttpMethod.POST,
    path: "/conversations",
    body: conversation
  });
}
async function updateConversation({
  apiKey,
  conversationId,
  updates
}) {
  return sendRequest({
    apiKey,
    method: import_pieces_common.HttpMethod.PUT,
    path: `/conversations/${encodeURIComponent(conversationId)}`,
    body: updates
  });
}
async function getCustomer({
  apiKey,
  customerId
}) {
  return sendRequest({
    apiKey,
    method: import_pieces_common.HttpMethod.GET,
    path: `/customers/${encodeURIComponent(customerId)}`
  });
}
async function getCustomObjects({
  apiKey,
  klassName,
  fromDate
}) {
  return sendRequest(__spreadValues({
    apiKey,
    method: import_pieces_common.HttpMethod.GET,
    path: `/kobjects/${encodeURIComponent(klassName)}`
  }, fromDate ? { queryParams: { fromDate } } : {}));
}
function createAuthHeaders({
  apiKey
}) {
  return {
    Authorization: `Bearer ${apiKey}`,
    Accept: "application/json",
    "Content-Type": "application/json"
  };
}
async function sendRequest({
  apiKey,
  method,
  path,
  body,
  queryParams
}) {
  let request = __spreadValues(__spreadValues({
    method,
    url: `${KUSTOMER_API_BASE_URL}${path}`,
    headers: createAuthHeaders({
      apiKey
    })
  }, body ? { body } : {}), queryParams ? { queryParams } : {});
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}
var kustomerClient = {
  validateAuth,
  createCustomer,
  createConversation,
  updateConversation,
  getCustomer,
  getCustomObjects,
  createAuthHeaders
}, KUSTOMER_API_BASE_URL = "https://api.kustomerapp.com/v1";

// ../ap/packages/pieces/community/kustomer/src/lib/common/auth.ts
var kustomerAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: `Authenticate using a Kustomer API token.

Paste a private Kustomer API token with access to customers, conversations, and KObjects.`,
  required: !0,
  validate: async ({ auth }) => {
    let apiKey = auth;
    if (!apiKey)
      return {
        valid: !1,
        error: "Invalid API token."
      };
    let { error } = await (0, import_pieces_framework2.tryCatch)(
      () => kustomerClient.validateAuth({
        apiKey
      })
    );
    return error ? {
      valid: !1,
      error: "Invalid API token or missing Kustomer permissions."
    } : {
      valid: !0
    };
  }
});

// ../ap/packages/pieces/community/kustomer/src/lib/actions/create-conversation.ts
var createConversationAction = (0, import_pieces_framework3.createAction)({
  auth: kustomerAuth,
  name: "create-conversation",
  displayName: "Create Conversation",
  description: "Creates a new conversation in Kustomer.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new conversation in Kustomer attached to an existing customer. Use to open a support thread for a known customer; the customer ID is required (resolve it first via Get Customer). Optional status, priority, direction, and reply channel set the conversation's initial state. Not idempotent \u2014 each call creates a separate conversation.",
    idempotent: !1
  },
  props: {
    customer: import_pieces_framework3.Property.ShortText({
      displayName: "Customer ID",
      description: 'The Kustomer ID of the customer this conversation belongs to. You can get this from the "Get Customer" action or from the URL when viewing a customer in Kustomer.',
      required: !0
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Conversation Name",
      description: "A short subject or title for the conversation (max 256 characters).",
      required: !1
    }),
    status: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Status",
      description: "The initial status of the conversation.",
      required: !1,
      defaultValue: "open",
      options: {
        options: [
          { label: "Open", value: "open" },
          { label: "Done", value: "done" }
        ]
      }
    }),
    priority: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Priority",
      description: "Conversation priority from 1 (lowest) to 5 (highest).",
      required: !1,
      options: {
        options: [
          { label: "1 \u2014 Lowest", value: 1 },
          { label: "2 \u2014 Low", value: 2 },
          { label: "3 \u2014 Medium", value: 3 },
          { label: "4 \u2014 High", value: 4 },
          { label: "5 \u2014 Highest", value: 5 }
        ]
      }
    }),
    direction: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Direction",
      description: '"Inbound" means the customer initiated the conversation; "Outbound" means your team initiated it.',
      required: !1,
      options: {
        options: [
          { label: "Inbound", value: "in" },
          { label: "Outbound", value: "out" }
        ]
      }
    }),
    externalId: import_pieces_framework3.Property.ShortText({
      displayName: "External ID",
      description: "A unique identifier from your own system to link this conversation (max 256 characters).",
      required: !1
    }),
    replyChannel: import_pieces_framework3.Property.ShortText({
      displayName: "Reply Channel",
      description: 'The channel used to reply in this conversation (e.g. "email", "chat"). Leave empty to use the workspace default.',
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, props = context.propsValue, conversation = {
      customer: props.customer
    };
    return props.name && (conversation.name = props.name), props.status && (conversation.status = props.status), props.priority !== null && props.priority !== void 0 && (conversation.priority = props.priority), props.direction && (conversation.direction = props.direction), props.externalId && (conversation.externalId = props.externalId), props.replyChannel && (conversation.replyChannel = props.replyChannel), await kustomerClient.createConversation({
      apiKey,
      conversation
    });
  }
});

// ../ap/packages/pieces/community/kustomer/src/lib/actions/create-customer.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var createCustomerAction = (0, import_pieces_framework4.createAction)({
  auth: kustomerAuth,
  name: "create-customer",
  displayName: "Create Customer",
  description: "Creates a new customer record in Kustomer.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new customer record in Kustomer from identity and profile fields (name, email, phone, company, externalId, locale, etc.). Use to add a contact to Kustomer; pass externalId to link the record back to your own system. Not idempotent \u2014 each call creates a separate customer even with identical input, so guard against duplicates.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Full Name",
      description: `The customer's full name (e.g. "Jane Doe").`,
      required: !1
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email Address",
      description: "Primary email address of the customer.",
      required: !1
    }),
    phone: import_pieces_framework4.Property.ShortText({
      displayName: "Phone Number",
      description: 'Primary phone number in E.164 format (e.g. "+14155551234").',
      required: !1
    }),
    company: import_pieces_framework4.Property.ShortText({
      displayName: "Company",
      description: "Name of the company the customer belongs to.",
      required: !1
    }),
    externalId: import_pieces_framework4.Property.ShortText({
      displayName: "External ID",
      description: "Unique identifier from your own system (e.g. your CRM or database ID). Used to link this Kustomer record back to your data.",
      required: !1
    }),
    username: import_pieces_framework4.Property.ShortText({
      displayName: "Username",
      description: "The customer's username in your system.",
      required: !1
    }),
    locale: import_pieces_framework4.Property.ShortText({
      displayName: "Locale",
      description: 'Language/locale code for the customer (e.g. "en_US", "fr_FR", "de_DE").',
      required: !1
    }),
    timeZone: import_pieces_framework4.Property.ShortText({
      displayName: "Time Zone",
      description: 'IANA timezone identifier (e.g. "America/New_York", "Europe/London", "Asia/Tokyo").',
      required: !1
    }),
    gender: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Gender",
      description: "The customer's gender.",
      required: !1,
      options: {
        options: [
          { label: "Male", value: "m" },
          { label: "Female", value: "f" }
        ]
      }
    }),
    birthdayAt: import_pieces_framework4.Property.ShortText({
      displayName: "Birthday",
      description: `Customer's birth date in ISO 8601 format (e.g. "1990-06-15T00:00:00.000Z").`,
      required: !1
    }),
    signedUpAt: import_pieces_framework4.Property.ShortText({
      displayName: "Signed Up At",
      description: 'When the customer registered, in ISO 8601 format (e.g. "2024-01-15T10:30:00.000Z").',
      required: !1
    }),
    avatarUrl: import_pieces_framework4.Property.ShortText({
      displayName: "Avatar URL",
      description: "URL of the customer's profile image.",
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, props = context.propsValue, customer = {};
    return props.name && (customer.name = props.name), props.company && (customer.company = props.company), props.externalId && (customer.externalId = props.externalId), props.username && (customer.username = props.username), props.locale && (customer.locale = props.locale), props.timeZone && (customer.timeZone = props.timeZone), props.gender && (customer.gender = props.gender), props.birthdayAt && (customer.birthdayAt = props.birthdayAt), props.signedUpAt && (customer.signedUpAt = props.signedUpAt), props.avatarUrl && (customer.avatarUrl = props.avatarUrl), props.email && (customer.emails = [{ email: props.email }]), props.phone && (customer.phones = [{ phone: props.phone }]), await kustomerClient.createCustomer({ apiKey, customer });
  }
});

// ../ap/packages/pieces/community/kustomer/src/lib/actions/get-customer.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/kustomer/src/lib/common/utils.ts
function parseRequiredString({
  value,
  fieldName
}) {
  if (typeof value != "string" || value.trim().length === 0)
    throw new Error(`${fieldName} is required.`);
  return value.trim();
}
function parseJsonObject({
  value,
  fieldName
}) {
  if (!isKustomerJsonObject(value))
    throw new Error(`${fieldName} must be a JSON object.`);
  return value;
}
function isKustomerJsonObject(value) {
  return typeof value != "object" || value === null || Array.isArray(value) ? !1 : Object.values(value).every(isKustomerJsonValue);
}
function isKustomerJsonValue(value) {
  return typeof value == "string" || typeof value == "number" || typeof value == "boolean" || value === null ? !0 : Array.isArray(value) ? value.every(isKustomerJsonValue) : isKustomerJsonObject(value);
}
var kustomerUtils = {
  parseRequiredString,
  parseJsonObject
};

// ../ap/packages/pieces/community/kustomer/src/lib/actions/get-customer.ts
var getCustomerAction = (0, import_pieces_framework5.createAction)({
  auth: kustomerAuth,
  name: "get-customer",
  displayName: "Get Customer",
  description: "Gets a customer by ID from Kustomer.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single Kustomer customer record by its Kustomer customer ID. Use to look up a customer's details when you already have their ID. Read-only and idempotent. The customer ID is required.",
    idempotent: !0
  },
  props: {
    customerId: import_pieces_framework5.Property.ShortText({
      displayName: "Customer ID",
      description: "The Kustomer customer ID.",
      required: !0
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, customerId = kustomerUtils.parseRequiredString({
      value: context.propsValue.customerId,
      fieldName: "Customer ID"
    });
    return kustomerClient.getCustomer({
      apiKey,
      customerId
    });
  }
});

// ../ap/packages/pieces/community/kustomer/src/lib/actions/get-custom-objects.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getCustomObjectsAction = (0, import_pieces_framework6.createAction)({
  auth: kustomerAuth,
  name: "get-custom-objects",
  displayName: "Get Custom Objects",
  description: "Retrieves all custom objects (KObjects) for a given class in Kustomer.",
  audience: "both",
  aiMetadata: {
    description: "Lists the custom objects (KObjects) of a given class in Kustomer, identified by the class's API name (the lowercase identifier, not the display label). Optionally pass a from-date to return only objects created or updated on or after it; leave it empty to return all objects of the class. Use to read or enumerate custom-object records. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    klassName: import_pieces_framework6.Property.ShortText({
      displayName: "KObject Class Name",
      description: 'The API name of the custom object class to fetch (e.g. "order", "ticket"). You can find this in Kustomer under Settings > Custom Objects \u2014 it is the lowercase identifier, not the display label.',
      required: !0
    }),
    fromDate: import_pieces_framework6.Property.ShortText({
      displayName: "From Date",
      description: 'Only return objects created or updated on or after this date. Use ISO 8601 format (e.g. "2024-01-15T00:00:00.000Z"). Leave empty to return all objects.',
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, { klassName, fromDate } = context.propsValue;
    return await kustomerClient.getCustomObjects(__spreadValues({
      apiKey,
      klassName
    }, fromDate ? { fromDate } : {}));
  }
});

// ../ap/packages/pieces/community/kustomer/src/lib/actions/update-conversation.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var updateConversationAction = (0, import_pieces_framework7.createAction)({
  auth: kustomerAuth,
  name: "update-conversation",
  displayName: "Update Conversation",
  description: "Updates an existing conversation in Kustomer.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Kustomer conversation identified by its conversation ID, changing fields such as status (open/done/snoozed), priority, name, direction, or reply channel. Only the fields you supply are modified. Use to change the state of a known conversation. Idempotent \u2014 repeating the same update yields the same end state.",
    idempotent: !0
  },
  props: {
    conversationId: import_pieces_framework7.Property.ShortText({
      displayName: "Conversation ID",
      description: 'The Kustomer ID of the conversation to update. You can get this from the "Create Conversation" action output or from the URL when viewing a conversation in Kustomer.',
      required: !0
    }),
    status: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Status",
      description: "Change the status of the conversation.",
      required: !1,
      options: {
        options: [
          { label: "Open", value: "open" },
          { label: "Done", value: "done" },
          { label: "Snoozed", value: "snoozed" }
        ]
      }
    }),
    priority: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Priority",
      description: "Conversation priority from 1 (lowest) to 5 (highest).",
      required: !1,
      options: {
        options: [
          { label: "1 \u2014 Lowest", value: 1 },
          { label: "2 \u2014 Low", value: 2 },
          { label: "3 \u2014 Medium", value: 3 },
          { label: "4 \u2014 High", value: 4 },
          { label: "5 \u2014 Highest", value: 5 }
        ]
      }
    }),
    name: import_pieces_framework7.Property.ShortText({
      displayName: "Conversation Name",
      description: "Update the subject or title of the conversation.",
      required: !1
    }),
    direction: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Direction",
      description: '"Inbound" means the customer initiated the conversation; "Outbound" means your team initiated it.',
      required: !1,
      options: {
        options: [
          { label: "Inbound", value: "in" },
          { label: "Outbound", value: "out" }
        ]
      }
    }),
    replyChannel: import_pieces_framework7.Property.ShortText({
      displayName: "Reply Channel",
      description: 'The channel used to reply in this conversation (e.g. "email", "chat").',
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, props = context.propsValue, updates = {};
    return props.status && (updates.status = props.status), props.priority !== null && props.priority !== void 0 && (updates.priority = props.priority), props.name !== null && props.name !== void 0 && (updates.name = props.name), props.direction && (updates.direction = props.direction), props.replyChannel !== null && props.replyChannel !== void 0 && (updates.replyChannel = props.replyChannel), await kustomerClient.updateConversation({
      apiKey,
      conversationId: props.conversationId,
      updates
    });
  }
});

// ../ap/packages/pieces/community/kustomer/src/index.ts
var kustomer = (0, import_pieces_framework8.createPiece)({
  displayName: "Kustomer",
  description: "Create and manage Kustomer customers, conversations, and custom objects.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/kustomer.png",
  categories: [import_pieces_framework9.PieceCategory.CUSTOMER_SUPPORT],
  authors: ["veri5ied", "sanket-a11y"],
  auth: kustomerAuth,
  actions: [
    createCustomerAction,
    createConversationAction,
    updateConversationAction,
    getCustomerAction,
    getCustomObjectsAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: kustomerAuth,
      baseUrl: () => KUSTOMER_API_BASE_URL,
      authMapping: async (auth) => {
        let apiKey = auth.secret_text;
        return kustomerClient.createAuthHeaders({
          apiKey
        });
      }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  kustomer
});
