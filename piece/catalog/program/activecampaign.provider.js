"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/activecampaign/src/index.ts
var index_exports = {};
__export(index_exports, {
  activecampaign: () => activecampaign
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_framework20 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/activecampaign/src/lib/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/activecampaign/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/activecampaign/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
function emptyValueFilter(accessor) {
  return (key) => {
    let val = accessor(key);
    return val != null && (typeof val != "string" || val.length > 0);
  };
}
function prepareQuery(request) {
  let params = {};
  return request && Object.keys(request).filter(emptyValueFilter((k) => request[k])).forEach((k) => {
    params[k] = request[k].toString();
  }), params;
}
var ActiveCampaignClient = class {
  constructor(apiUrl, apiKey) {
    __publicField(this, "apiUrl", apiUrl);
    __publicField(this, "apiKey", apiKey);
  }
  async makeRequest(method, resourceUri, query, body = void 0) {
    let baseUrl = this.apiUrl.replace(/\/$/, ""), request = {
      method,
      url: baseUrl + "/api/3" + resourceUri,
      headers: {
        "Api-Token": this.apiKey
      },
      queryParams: query,
      body
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  }
  async authenticate() {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/users/me");
  }
  async subscribeWebhook(request) {
    return await this.makeRequest(import_pieces_common.HttpMethod.POST, "/webhooks", void 0, {
      webhook: request
    });
  }
  async unsubscribeWebhook(webhookId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.DELETE, `/webhooks/${webhookId}`);
  }
  async listContactLists() {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/lists",
      prepareQuery({ limit: 20 })
    );
  }
  async createAccount(request) {
    return await this.makeRequest(import_pieces_common.HttpMethod.POST, "/accounts", void 0, { account: request });
  }
  async updateAccount(accountId, request) {
    return await this.makeRequest(import_pieces_common.HttpMethod.PUT, `/accounts/${accountId}`, void 0, {
      account: request
    });
  }
  async listAccounts(search) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/accounts",
      prepareQuery({ search })
    );
  }
  async listAccountCustomFields() {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/accountCustomFieldMeta"
    );
  }
  async createContact(request) {
    return await this.makeRequest(import_pieces_common.HttpMethod.POST, "/contacts", void 0, { contact: request });
  }
  async updateContact(contactId, request) {
    return await this.makeRequest(import_pieces_common.HttpMethod.PUT, `/contacts/${contactId}`, void 0, {
      contact: request
    });
  }
  async listContacts() {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/contacts");
  }
  async listContactCustomFields() {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/fields");
  }
  async addContactToList(listId, contactId, status) {
    return await this.makeRequest(import_pieces_common.HttpMethod.POST, "/contactLists", void 0, {
      contactList: { list: listId, contact: contactId, status }
    });
  }
  async createAccountContactAssociation(contactId, accountId, jobTitle) {
    return await this.makeRequest(import_pieces_common.HttpMethod.POST, "/accountContacts", void 0, {
      accountContact: { contact: contactId, account: accountId, jobTitle }
    });
  }
  async addTagToContact(contactId, tagId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.POST, "/contactTags", void 0, {
      contactTag: { contact: contactId, tag: tagId }
    });
  }
  async listTags() {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/tags");
  }
};

// ../ap/packages/pieces/community/activecampaign/src/lib/common/constants.ts
var WEBHOOK_SOURCES = ["public", "admin", "api", "system"];

// ../ap/packages/pieces/community/activecampaign/src/lib/common/index.ts
function makeClient(auth) {
  return new ActiveCampaignClient(auth.apiUrl, auth.apiKey);
}
var activecampaignCommon = {
  listId: (required = !1) => import_pieces_framework.Property.Dropdown({
    auth: activeCampaignAuth,
    displayName: "List",
    required,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient(auth.props).listContactLists()).lists.map((list) => ({
        label: list.name,
        value: list.id
      }))
    } : {
      disabled: !0,
      placeholder: "Please connect your account first",
      options: []
    }
  }),
  accountId: import_pieces_framework.Property.Dropdown({
    auth: activeCampaignAuth,
    displayName: "Account ID",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient(auth.props).listAccounts()).accounts.map((account) => ({
        label: account.name,
        value: account.id
      }))
    } : {
      disabled: !0,
      placeholder: "Please connect your account first",
      options: []
    }
  }),
  contactId: import_pieces_framework.Property.Dropdown({
    auth: activeCampaignAuth,
    displayName: "Contact ID",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient(auth.props).listContacts()).contacts.map((contact) => ({
        label: contact.firstName && contact.lastName ? `${contact.firstName} ${contact.lastName}` : contact.email,
        value: contact.id
      }))
    } : {
      disabled: !0,
      placeholder: "Please connect your account first",
      options: []
    }
  }),
  tagId: import_pieces_framework.Property.Dropdown({
    auth: activeCampaignAuth,
    displayName: "Tag ID",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient(auth.props).listTags()).tags.map((tag) => ({
        label: tag.tag,
        value: tag.id
      }))
    } : {
      disabled: !0,
      placeholder: "Please connect your account first",
      options: []
    }
  }),
  accountCustomFields: import_pieces_framework.Property.DynamicProperties({
    auth: activeCampaignAuth,
    displayName: "Account Custom Fields",
    refreshers: [],
    required: !0,
    props: async ({ auth }) => {
      var _a, _b;
      if (!auth) return {};
      let res = await makeClient(auth.props).listAccountCustomFields(), fields = {};
      for (let field of res.accountCustomFieldMeta)
        switch (field.fieldType) {
          case "text" /* TEXT */:
          case "hidden" /* HIDDEN */:
            fields[field.id] = import_pieces_framework.Property.ShortText({
              displayName: field.fieldLabel,
              required: !1
            });
            break;
          case "textarea" /* TEXTAREA */:
            fields[field.id] = import_pieces_framework.Property.LongText({
              displayName: field.fieldLabel,
              required: !1
            });
            break;
          case "date" /* DATE */:
            fields[field.id] = import_pieces_framework.Property.DateTime({
              displayName: field.fieldLabel,
              description: "Please use YYYY-MM-DD format.",
              required: !1
            });
            break;
          case "datetime" /* DATETIME */:
            fields[field.id] = import_pieces_framework.Property.DateTime({
              displayName: field.fieldLabel,
              required: !1
            });
            break;
          case "currency" /* MONEY */:
          case "number" /* NUMBER */:
            fields[field.id] = import_pieces_framework.Property.Number({
              displayName: field.fieldLabel,
              required: !1
            });
            break;
          case "dropdown" /* DROPDOWN */:
          case "radio" /* RADIO */:
            fields[field.id] = import_pieces_framework.Property.StaticDropdown({
              displayName: field.fieldLabel,
              required: !1,
              options: {
                disabled: !1,
                options: field.fieldOptions ? (_a = field.fieldOptions) == null ? void 0 : _a.map((option) => ({
                  label: option,
                  value: option
                })) : []
              }
            });
            break;
          case "checkbox" /* CHECKBOX */:
          case "listbox" /* LIST_BOX */:
          case "multiselect" /* MULTISELECT */:
            fields[field.id] = import_pieces_framework.Property.StaticMultiSelectDropdown({
              displayName: field.fieldLabel,
              required: !1,
              options: {
                disabled: !1,
                options: field.fieldOptions ? (_b = field.fieldOptions) == null ? void 0 : _b.map((option) => ({
                  label: option,
                  value: option
                })) : []
              }
            });
            break;
        }
      return fields;
    }
  }),
  contactCustomFields: import_pieces_framework.Property.DynamicProperties({
    auth: activeCampaignAuth,
    displayName: "Contact Custom Fields",
    refreshers: [],
    required: !0,
    props: async ({ auth }) => {
      if (!auth) return {};
      let res = await makeClient(auth.props).listContactCustomFields(), fields = {};
      for (let field of res.fields)
        switch (field.type) {
          case "text" /* TEXT */:
          case "hidden" /* HIDDEN */:
            fields[field.id] = import_pieces_framework.Property.ShortText({
              displayName: field.title,
              required: !1
            });
            break;
          case "textarea" /* TEXTAREA */:
            fields[field.id] = import_pieces_framework.Property.LongText({
              displayName: field.title,
              required: !1
            });
            break;
          case "date" /* DATE */:
            fields[field.id] = import_pieces_framework.Property.DateTime({
              displayName: field.title,
              description: "Please use YYYY-MM-DD format.",
              required: !1
            });
            break;
          case "datetime" /* DATETIME */:
            fields[field.id] = import_pieces_framework.Property.DateTime({
              displayName: field.title,
              required: !1
            });
            break;
          case "currency" /* MONEY */:
          case "number" /* NUMBER */:
            fields[field.id] = import_pieces_framework.Property.Number({
              displayName: field.title,
              required: !1
            });
            break;
          case "dropdown" /* DROPDOWN */:
          case "radio" /* RADIO */:
            fields[field.id] = import_pieces_framework.Property.StaticDropdown({
              displayName: field.title,
              required: !1,
              options: {
                disabled: !1,
                options: res.fieldOptions.filter((option) => option.field === field.id).map((option) => ({
                  label: option.label,
                  value: option.value
                }))
              }
            });
            break;
          case "checkbox" /* CHECKBOX */:
          case "listbox" /* LIST_BOX */:
          case "multiselect" /* MULTISELECT */:
            fields[field.id] = import_pieces_framework.Property.StaticMultiSelectDropdown({
              displayName: field.title,
              required: !1,
              options: {
                disabled: !1,
                options: res.fieldOptions.filter((option) => option.field === field.id).map((option) => ({
                  label: option.label,
                  value: option.value
                }))
              }
            });
            break;
        }
      return fields;
    }
  })
};

// ../ap/packages/pieces/community/activecampaign/src/lib/auth.ts
var authGuide = `
To obtain your ActiveCampaign API URL and Key, follow these steps:

1. Log in to your ActiveCampaign account.
2. Navigate to **Settings->Developer** section.
3. Under **API Access** ,you'll find your API URL and Key.
`, activeCampaignAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  required: !0,
  description: authGuide,
  props: {
    apiUrl: import_pieces_framework2.Property.ShortText({
      displayName: "API URL",
      required: !0
    }),
    apiKey: import_pieces_framework2.Property.ShortText({
      displayName: "API Key",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await makeClient(auth).authenticate(), { valid: !0 };
    } catch (error) {
      return {
        valid: !1,
        error: "Invalid API credentials"
      };
    }
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/triggers/new-deal-added-or-updated.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var newDealAddedOrUpdatedTrigger = (0, import_pieces_framework3.createTrigger)({
  auth: activeCampaignAuth,
  name: "activecampaign_new_deal_added_or_updated",
  classification: "READ",
  displayName: "New Deal Added or Updated",
  description: "Triggers when a new deal is created or existing deal is updated.",
  aiMetadata: {
    description: "Fires when a deal (CRM sales opportunity) is created or when an existing deal is updated, covering both add and update events. The payload identifies which event occurred and lists the changed fields for updates."
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let res = await makeClient(context.auth.props).subscribeWebhook({
      name: "Activepieces New Deal Hook",
      url: context.webhookUrl,
      events: ["deal_add", "deal_update"],
      sources: WEBHOOK_SOURCES
    });
    await context.store.put("activecampaign_new_deal_added_or_updated", res);
  },
  async run(context) {
    return [context.payload.body];
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "activecampaign_new_deal_added_or_updated"
    );
    webhook != null && await makeClient(context.auth.props).unsubscribeWebhook(webhook.webhook.id);
  },
  sampleData: {
    type: "deal_update",
    date_time: "2024-02-28T04:45:41-06:00",
    initiated_from: "admin",
    initiated_by: "admin",
    list: "0",
    contact: {
      id: "3",
      email: "code.test@gmail.com",
      first_name: "John",
      last_name: "Wick",
      phone: "",
      ip: "0.0.0.0",
      tags: "1233",
      customer_acct_name: "",
      orgname: ""
    },
    customer_acct_name: "",
    customer_acct_id: "0",
    orgname: "",
    deal: {
      id: "1",
      title: "Test Deal",
      create_date: "2024-02-28 04:36:09",
      create_date_iso: "2024-02-28T04:36:09-06:00",
      orgid: "1",
      orgname: "John Wick",
      stageid: "1",
      stage_title: "To Contact",
      pipelineid: "1",
      pipeline_title: "Test Pipeline",
      value: "1,044,055.00",
      value_raw: "1044055",
      currency: "usd",
      currency_symbol: "$",
      owner: "1",
      owner_firstname: "John",
      owner_lastname: "Wick",
      contactid: "3",
      contact_email: "code.test@gmail.com",
      contact_firstname: "John",
      contact_lastname: "Wick",
      status: "0",
      fields: [{ id: "1", key: "Forecasted Close Date", value: "2024-02-08 00:00:00" }]
    },
    updated_fields: ["value"]
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/actions/accounts/create-account.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var createAccountAction = (0, import_pieces_framework4.createAction)({
  auth: activeCampaignAuth,
  name: "activecampaign_create_account",
  classification: "WRITE",
  displayName: "Create Account",
  description: "Creates a new account.",
  audience: "both",
  aiMetadata: { description: "Creates a new ActiveCampaign account (CRM organization/company record) with a name and optional URL and custom field values. Use when you need to register a company in the CRM before associating contacts or deals with it. Not idempotent: each call creates a separate account even with identical input, so it can produce duplicates.", idempotent: !1 },
  props: {
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Account Name",
      required: !0
    }),
    accountUrl: import_pieces_framework4.Property.ShortText({
      displayName: "Account URL",
      required: !1
    }),
    accountCustomFields: activecampaignCommon.accountCustomFields
  },
  async run(context) {
    let { name, accountUrl, accountCustomFields } = context.propsValue, createAccountParams = {
      name,
      accountUrl,
      fields: []
    };
    return Object.entries(accountCustomFields).forEach(([key, value]) => {
      var _a;
      (_a = createAccountParams.fields) == null || _a.push({ customFieldId: Number(key), fieldValue: value });
    }), await makeClient(context.auth.props).createAccount(createAccountParams);
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/actions/accounts/update-account.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var updateAccountAction = (0, import_pieces_framework5.createAction)({
  auth: activeCampaignAuth,
  name: "activecampaign_update_account",
  classification: "WRITE",
  displayName: "Update Account",
  description: "Updates an account.",
  audience: "both",
  aiMetadata: { description: "Updates an existing ActiveCampaign account identified by account ID, changing its name, URL, and/or custom field values. Use when you already have the target account ID and want to modify its details. Idempotent: re-running with the same input leaves the account in the same state.", idempotent: !0 },
  props: {
    accountId: activecampaignCommon.accountId,
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Account Name",
      required: !1
    }),
    accountUrl: import_pieces_framework5.Property.ShortText({
      displayName: "Account URL",
      required: !1
    }),
    accountCustomFields: activecampaignCommon.accountCustomFields
  },
  async run(context) {
    let { accountId, name, accountUrl, accountCustomFields } = context.propsValue, updateAccountParams = {
      name,
      accountUrl,
      fields: []
    };
    return Object.entries(accountCustomFields).forEach(([key, value]) => {
      var _a;
      (_a = updateAccountParams.fields) == null || _a.push({ customFieldId: Number(key), fieldValue: value });
    }), await makeClient(context.auth.props).updateAccount(Number(accountId), updateAccountParams);
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/actions/contacts/subscribe-or-unsubscribe-contact-from-list.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var subscribeOrUnsubscribeContactFromListAction = (0, import_pieces_framework6.createAction)({
  auth: activeCampaignAuth,
  name: "activecampaign_subscribe_or_unsubscribe_contact_from_list",
  classification: "WRITE",
  displayName: "Subscribe or Unsubscribe Contact From List",
  description: "Subscribes a Contact to a List it is not currently associated with, or Unsubscribes a Contact from a list is currently associated with.",
  audience: "both",
  aiMetadata: { description: "Sets a contact's subscription status on a mailing list, toggling between Subscribe and Unsubscribe via the Action field. Use to add a contact to a list or remove them from one for email marketing. Requires a list ID and the contact ID; idempotent because re-applying the same status leaves the membership unchanged.", idempotent: !0 },
  props: {
    listId: activecampaignCommon.listId(!0),
    status: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Action",
      required: !0,
      options: {
        disabled: !1,
        options: [
          {
            label: "Subscribe",
            value: "1"
          },
          {
            label: "Unsubscribe",
            value: "2"
          }
        ]
      }
    }),
    contactId: import_pieces_framework6.Property.ShortText({
      displayName: "Contact ID",
      required: !0
    })
  },
  async run(context) {
    let { listId, status, contactId } = context.propsValue;
    return await makeClient(context.auth.props).addContactToList(listId, contactId, status);
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/actions/contacts/create-contact.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var createContactAction = (0, import_pieces_framework7.createAction)({
  auth: activeCampaignAuth,
  name: "activecampaign_create_contact",
  classification: "WRITE",
  displayName: "Create Contact",
  description: "Creates a new contact.",
  audience: "both",
  aiMetadata: { description: "Creates a new ActiveCampaign contact from an email (required) plus optional first name, last name, phone, and custom field values. Use when adding a new person to the CRM. Not idempotent: each call creates a contact and does not look up or merge by email, so repeated calls can create duplicates.", idempotent: !1 },
  props: {
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Email",
      required: !0
    }),
    firstName: import_pieces_framework7.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework7.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    phone: import_pieces_framework7.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    contactCustomFields: activecampaignCommon.contactCustomFields
  },
  async run(context) {
    let { email, firstName, lastName, phone, contactCustomFields } = context.propsValue, createContactParams = {
      email,
      firstName,
      lastName,
      phone,
      fieldValues: []
    };
    return Object.entries(contactCustomFields).forEach(([key, value]) => {
      createContactParams.fieldValues.push({ field: key, value });
    }), await makeClient(context.auth.props).createContact(createContactParams);
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/actions/contacts/update-contact.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var updateContactAction = (0, import_pieces_framework8.createAction)({
  auth: activeCampaignAuth,
  name: "activecampaign_update_contact",
  classification: "WRITE",
  displayName: "Update Contact",
  description: "Updates an existing contact.",
  audience: "both",
  aiMetadata: { description: "Updates an existing ActiveCampaign contact identified by contact ID, changing its email, name, phone, and/or custom field values. Use when you already have the contact ID and want to modify its details. Idempotent: re-running with the same input leaves the contact in the same state.", idempotent: !0 },
  props: {
    contactId: activecampaignCommon.contactId,
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    firstName: import_pieces_framework8.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework8.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    phone: import_pieces_framework8.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    contactCustomFields: activecampaignCommon.contactCustomFields
  },
  async run(context) {
    let { email, contactId, firstName, lastName, phone, contactCustomFields } = context.propsValue, updateContactParams = {
      email,
      firstName,
      lastName,
      phone,
      fieldValues: []
    };
    return Object.entries(contactCustomFields).forEach(([key, value]) => {
      var _a;
      (_a = updateContactParams.fieldValues) == null || _a.push({ field: key, value });
    }), await makeClient(context.auth.props).updateContact(Number(contactId), updateContactParams);
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/actions/contacts/add-contact-to-account.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var addContactToAccountAction = (0, import_pieces_framework9.createAction)({
  auth: activeCampaignAuth,
  name: "activecampaign_add_contact_to_account",
  classification: "WRITE",
  displayName: "Add Contact to Account",
  description: "Adds a contact to an ActiveCampaign account.",
  audience: "both",
  aiMetadata: { description: "Associates an existing contact with an existing account (CRM organization), optionally recording the contact's job title at that account. Use when linking a person to a company already in the CRM. Requires both the contact ID and account ID; not idempotent, as each call creates a new association record.", idempotent: !1 },
  props: {
    contactId: activecampaignCommon.contactId,
    accountId: activecampaignCommon.accountId,
    jobTitle: import_pieces_framework9.Property.ShortText({
      displayName: "Job Title",
      required: !1
    })
  },
  async run(context) {
    let { contactId, accountId, jobTitle } = context.propsValue;
    return await makeClient(context.auth.props).createAccountContactAssociation(
      Number(contactId),
      Number(accountId),
      jobTitle
    );
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/actions/contacts/add-tag-to-contact.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var addTagToContactAction = (0, import_pieces_framework10.createAction)({
  auth: activeCampaignAuth,
  name: "activecampaign_add_tag_to_contact",
  classification: "WRITE",
  displayName: "Add Tag to Contact",
  description: "Adds a tag to contact.",
  audience: "both",
  aiMetadata: { description: "Attaches an existing tag to a contact for segmentation or automation triggering. Use when you have the contact ID and tag ID and want to label that contact. Not idempotent: each call creates a new contact-tag association record.", idempotent: !1 },
  props: {
    contactId: activecampaignCommon.contactId,
    tagId: activecampaignCommon.tagId
  },
  async run(context) {
    let { contactId, tagId } = context.propsValue;
    return await makeClient(context.auth.props).addTagToContact(contactId, tagId);
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/triggers/new-deal-note.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var newDealNoteTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: activeCampaignAuth,
  name: "activecampaign_new_deal_note",
  classification: "READ",
  displayName: "New Deal Note",
  description: "Triggers when a new deal note is created.",
  aiMetadata: {
    description: "Fires when a note is added to a deal. The payload includes the note text and the associated deal."
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let res = await makeClient(context.auth.props).subscribeWebhook({
      name: "Activepieces New Deal Note Hook",
      url: context.webhookUrl,
      events: ["deal_note_add"],
      sources: WEBHOOK_SOURCES
    });
    await context.store.put("activecampaign_new_deal_note", res);
  },
  async run(context) {
    return [context.payload.body];
  },
  async onDisable(context) {
    let webhook = await context.store.get("activecampaign_new_deal_note");
    webhook != null && await makeClient(context.auth.props).unsubscribeWebhook(webhook.webhook.id);
  },
  sampleData: {
    type: "deal_note_add",
    date_time: "2024-02-28T05:58:27-06:00",
    initiated_from: "admin",
    initiated_by: "admin",
    list: "0",
    note: { id: "1", text: "Tst node" },
    deal: {
      id: "1",
      title: "Test Deal updated",
      create_date: "2024-02-28 04:36:09",
      create_date_iso: "2024-02-28T04:36:09-06:00",
      orgid: "1",
      orgname: "John wick",
      stageid: "1",
      stage_title: "To Contact",
      pipelineid: "1",
      pipeline_title: "Test Pipeline",
      value: "14,055.00",
      value_raw: "14055",
      currency: "usd",
      currency_symbol: "$",
      owner: "1",
      owner_firstname: "John",
      owner_lastname: "wick",
      contactid: "3",
      contact_email: "code.test@gmail.com",
      contact_firstname: "John",
      contact_lastname: "wick",
      status: "0",
      fields: [{ id: "1", key: "Forecasted Close Date", value: "2024-02-08 00:00:00" }]
    }
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/triggers/new-deal-task.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var newDealTaskTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: activeCampaignAuth,
  name: "activecampaign_new_deal_task",
  classification: "READ",
  displayName: "New Deal Task",
  description: "Triggers when a new deal task is created.",
  aiMetadata: {
    description: "Fires when a task is created on a deal. The payload includes the task (type, title, due date) and the associated deal and contact."
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let res = await makeClient(context.auth.props).subscribeWebhook({
      name: "Activepieces New Deal Task Hook",
      url: context.webhookUrl,
      events: ["deal_task_add"],
      sources: WEBHOOK_SOURCES
    });
    await context.store.put("activecampaign_new_deal_task", res);
  },
  async run(context) {
    return [context.payload.body];
  },
  async onDisable(context) {
    let webhook = await context.store.get("activecampaign_new_deal_task");
    webhook != null && await makeClient(context.auth.props).unsubscribeWebhook(webhook.webhook.id);
  },
  sampleData: {
    type: "deal_task_add",
    date_time: "2024-02-28T06:38:49-06:00",
    initiated_from: "admin",
    initiated_by: "admin",
    list: "0",
    contact: {
      id: "3",
      email: "code.test@gmail.com",
      first_name: "john",
      last_name: "wick",
      phone: "",
      ip: "0.0.0.0",
      tags: "1233",
      customer_acct_name: "",
      orgname: ""
    },
    customer_acct_name: "",
    customer_acct_id: "0",
    orgname: "",
    deal: {
      id: "1",
      title: "Test Deal updated",
      create_date: "2024-02-28 04:36:09",
      create_date_iso: "2024-02-28T04:36:09-06:00",
      orgid: "1",
      orgname: "john wick",
      stageid: "1",
      stage_title: "To Contact",
      pipelineid: "1",
      pipeline_title: "Test Pipeline",
      value: "14,055.00",
      value_raw: "14055",
      currency: "usd",
      currency_symbol: "$",
      owner: "1",
      owner_firstname: "john",
      owner_lastname: "wick",
      contactid: "3",
      contact_email: "code.test@gmail.com",
      contact_firstname: "john",
      contact_lastname: "wick",
      status: "0",
      fields: [{ id: "1", key: "Forecasted Close Date", value: "2024-02-08 00:00:00" }]
    },
    task: {
      id: "6",
      type_id: "1",
      title: "TEST TASK",
      note: "fsfssssf",
      duedate: "2024-02-28 06:38:41",
      duedate_iso: "2024-02-28T06:38:41-06:00",
      edate: "2024-02-28 06:53:41",
      edate_iso: "2024-02-28T06:53:41-06:00",
      type_title: "Call"
    }
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/triggers/deal-task-completed.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var dealTaskCompletedTrigger = (0, import_pieces_framework13.createTrigger)({
  auth: activeCampaignAuth,
  name: "activecampaign_deal_task_completed",
  classification: "READ",
  displayName: "Deal Task Completed",
  description: "Triggers when a deal task has been completed.",
  aiMetadata: {
    description: "Fires when a task attached to a deal is marked complete. Use to react to finished sales activities; the payload includes the related deal and contact."
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let res = await makeClient(context.auth.props).subscribeWebhook({
      name: "Activepieces Deal Task Completed Hook",
      url: context.webhookUrl,
      events: ["deal_task_complete"],
      sources: WEBHOOK_SOURCES
    });
    await context.store.put("activecampaign_deal_task_completed", res);
  },
  async run(context) {
    return [context.payload.body];
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "activecampaign_deal_task_completed"
    );
    webhook != null && await makeClient(context.auth.props).unsubscribeWebhook(webhook.webhook.id);
  },
  sampleData: {
    type: "deal_update",
    date_time: "2024-02-28T04:45:41-06:00",
    initiated_from: "admin",
    initiated_by: "admin",
    list: "0",
    contact: {
      id: "3",
      email: "code.test@gmail.com",
      first_name: "John",
      last_name: "Wick",
      phone: "",
      ip: "0.0.0.0",
      tags: "1233",
      customer_acct_name: "",
      orgname: ""
    },
    customer_acct_name: "",
    customer_acct_id: "0",
    orgname: "",
    deal: {
      id: "1",
      title: "Test Deal",
      create_date: "2024-02-28 04:36:09",
      create_date_iso: "2024-02-28T04:36:09-06:00",
      orgid: "1",
      orgname: "John Wick",
      stageid: "1",
      stage_title: "To Contact",
      pipelineid: "1",
      pipeline_title: "Test Pipeline",
      value: "1,044,055.00",
      value_raw: "1044055",
      currency: "usd",
      currency_symbol: "$",
      owner: "1",
      owner_firstname: "John",
      owner_lastname: "Wick",
      contactid: "3",
      contact_email: "code.test@gmail.com",
      contact_firstname: "John",
      contact_lastname: "Wick",
      status: "0",
      fields: [{ id: "1", key: "Forecasted Close Date", value: "2024-02-08 00:00:00" }]
    },
    updated_fields: ["value"]
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/triggers/tag-added-or-removed-from-contact.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var newtagAddedOrRemovedFromContactTrigger = (0, import_pieces_framework14.createTrigger)({
  auth: activeCampaignAuth,
  name: "activecampaign_new_tag_added_or_removed_from_contact",
  classification: "READ",
  displayName: "Tag Added or Removed From Contact",
  description: "Triggers when a a Tag is added or removed from a Contact",
  aiMetadata: {
    description: "Fires when a tag is added to or removed from a contact, covering both tag-added and tag-removed events. The payload identifies the affected contact and the tag."
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let res = await makeClient(context.auth.props).subscribeWebhook({
      name: "Activepieces Contact Tag Hook",
      url: context.webhookUrl,
      events: ["contact_tag_added", "contact_tag_removed"],
      sources: WEBHOOK_SOURCES
    });
    await context.store.put(
      "activecampaign_new_tag_added_or_removed_from_contact",
      res
    );
  },
  async run(context) {
    return [context.payload.body];
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "activecampaign_new_tag_added_or_removed_from_contact"
    );
    webhook != null && await makeClient(context.auth.props).unsubscribeWebhook(webhook.webhook.id);
  },
  sampleData: {
    type: "contact_tag_added",
    date_time: "2024-02-28T07:04:13-06:00",
    initiated_from: "admin",
    initiated_by: "admin",
    list: "0",
    contact: {
      id: "3",
      email: "code.test@gmail.com",
      first_name: "John",
      last_name: "Wick",
      phone: "",
      ip: "0.0.0.0",
      tags: "tag1, tag2",
      customer_acct_name: "",
      orgname: ""
    },
    customer_acct_name: "",
    customer_acct_id: "0",
    orgname: "",
    tag: "tag2"
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/triggers/new-or-updated-account.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var newOrUpdatedAccountTrigger = (0, import_pieces_framework15.createTrigger)({
  auth: activeCampaignAuth,
  name: "activecampaign_new_or_updated_account",
  classification: "READ",
  displayName: "New or Updated Account",
  description: "Triggers when a new account is added or an existing account\u2019s details are updated",
  aiMetadata: {
    description: "Fires when an account (CRM organization/company) is created or when an existing account is updated, covering both add and update events. The payload identifies the event and, for updates, the changed fields."
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let res = await makeClient(context.auth.props).subscribeWebhook({
      name: "Activepieces New Account Hook",
      url: context.webhookUrl,
      events: ["account_add", "account_update"],
      sources: WEBHOOK_SOURCES
    });
    await context.store.put("activecampaign_new_or_updated_account", res);
  },
  async run(context) {
    return [context.payload.body];
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "activecampaign_new_or_updated_account"
    );
    webhook != null && await makeClient(context.auth.props).unsubscribeWebhook(webhook.webhook.id);
  },
  sampleData: {
    type: "account_update",
    date_time: "2024-02-28T06:44:32-06:00",
    initiated_from: "admin",
    initiated_by: "admin",
    list: "0",
    account: {
      id: "1",
      name: "John Wick",
      account_url: "https://www.github.com",
      created_timestamp: "2024-02-28 01:09:17",
      updated_timestamp: "2024-02-28 06:44:32",
      fields: {
        0: { id: "1", key: "Description", value: "Desc" },
        1: { id: "2", key: "Address 1", value: "Address 1" },
        2: { id: "3", key: "Address 2", value: "Address 2" },
        3: { id: "4", key: "City", value: "City" },
        4: { id: "5", key: "State/Province", value: "State" },
        5: { id: "6", key: "Postal Code", value: "75156" },
        6: { id: "7", key: "Country", value: "India" },
        7: { id: "8", key: "Number of Employees", value: "101 - 500" },
        8: { id: "9", key: "Annual Revenue", value: "Less than 100K" },
        9: { id: "10", key: "Industry/Vertical", value: "Accounting/Financial" },
        10: { key: "Phone Number", value: "" },
        11: { id: "11", key: "Text Input", value: "Text Input" },
        12: { id: "12", key: "Text Area", value: "Text Area" },
        13: { id: "13", key: "Number", value: "18.000" },
        14: { id: "14", key: "money", value: "18" },
        15: { id: "15", key: "Date", value: "2024-02-28 00:00:00" },
        16: { key: "Date Time", value: "" },
        17: { id: "16", key: "Drop Down", value: "Option 3" },
        18: { id: "17", key: "List box", value: ["Option 1", "Option 2"] },
        19: { id: "18", key: "Radio Buttons", value: "Option 1" },
        20: { id: "19", key: "Check Box", value: ["Option 1", "Option 2"] },
        21: { id: "20", key: "Hidden", value: "Hidden" }
      }
    },
    updated_fields: ["name"]
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/triggers/new-contact-note.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var newContactNoteTrigger = (0, import_pieces_framework16.createTrigger)({
  auth: activeCampaignAuth,
  name: "activecampaign_new_contact_note",
  classification: "READ",
  displayName: "New Contact Note",
  description: "Triggers when a new contact note is added.",
  aiMetadata: {
    description: "Fires when a note is added to a contact. The payload includes the note text and the associated contact."
  },
  type: import_pieces_framework16.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let res = await makeClient(context.auth.props).subscribeWebhook({
      name: "Activepieces New Contact Note Hook",
      url: context.webhookUrl,
      events: ["subscriber_note"],
      sources: WEBHOOK_SOURCES
    });
    await context.store.put("activecampaign_new_contact_note", res);
  },
  async run(context) {
    return [context.payload.body];
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "activecampaign_new_contact_note"
    );
    webhook != null && await makeClient(context.auth.props).unsubscribeWebhook(webhook.webhook.id);
  },
  sampleData: {
    type: "subscriber_note",
    date_time: "2024-02-28T06:58:11-06:00",
    initiated_from: "admin",
    initiated_by: "admin",
    list: "0",
    note: "test note",
    contact: {
      id: "3",
      email: "code.test@gmail.com",
      first_name: "john",
      last_name: "wick",
      phone: "",
      ip: "0.0.0.0",
      tags: "1233",
      customer_acct_name: "",
      orgname: ""
    },
    customer_acct_name: "",
    customer_acct_id: "0",
    orgname: ""
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/triggers/new-contact-task.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var newContactTaskTrigger = (0, import_pieces_framework17.createTrigger)({
  auth: activeCampaignAuth,
  name: "activecampaign_new_contact_task",
  classification: "READ",
  displayName: "New Contact Task",
  description: "Triggers when a new contact task is added.",
  aiMetadata: {
    description: "Fires when a task is created on a contact. The payload includes the task (type, title, due date) and the associated contact."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let res = await makeClient(context.auth.props).subscribeWebhook({
      name: "Activepieces New Contact Task Hook",
      url: context.webhookUrl,
      events: ["contact_task_add"],
      sources: WEBHOOK_SOURCES
    });
    await context.store.put("activecampaign_new_contact_task", res);
  },
  async run(context) {
    return [context.payload.body];
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "activecampaign_new_contact_task"
    );
    webhook != null && await makeClient(context.auth.props).unsubscribeWebhook(webhook.webhook.id);
  },
  sampleData: {
    type: "contact_task_add",
    date_time: "2024-02-28T07:00:57-06:00",
    initiated_from: "admin",
    initiated_by: "admin",
    list: "0",
    contact: {
      id: "3",
      email: "code.test@gmail.com",
      first_name: "John",
      last_name: "Wick",
      phone: "",
      ip: "0.0.0.0",
      tags: "1233",
      customer_acct_name: "",
      orgname: ""
    },
    customer_acct_name: "",
    customer_acct_id: "0",
    orgname: "",
    task: {
      id: "7",
      type_id: "1",
      title: "test task",
      note: "Desc",
      duedate: "2024-02-28 07:00:48",
      duedate_iso: "2024-02-28T07:00:48-06:00",
      edate: "2024-02-28 07:15:48",
      edate_iso: "2024-02-28T07:15:48-06:00",
      type_title: "Call"
    }
  }
});

// ../ap/packages/pieces/community/activecampaign/src/lib/triggers/updated-contact.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var updatedContactTrigger = (0, import_pieces_framework18.createTrigger)({
  auth: activeCampaignAuth,
  name: "activecampaign_updated_contact",
  classification: "READ",
  displayName: "Updated Contact",
  description: "Triggers when an existing contact details are updated.",
  aiMetadata: {
    description: "Fires when an existing contact's details are updated. The payload contains the updated contact record including its current field values and tags."
  },
  type: import_pieces_framework18.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let res = await makeClient(context.auth.props).subscribeWebhook({
      name: "Activepieces Updated Contact Hook",
      url: context.webhookUrl,
      events: ["update"],
      sources: WEBHOOK_SOURCES
    });
    await context.store.put("activecampaign_updated_contact", res);
  },
  async run(context) {
    return [context.payload.body];
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "activecampaign_updated_contact"
    );
    webhook != null && await makeClient(context.auth.props).unsubscribeWebhook(webhook.webhook.id);
  },
  sampleData: {
    type: "update",
    date_time: "2024-02-28T07:16:48-06:00",
    initiated_from: "admin",
    initiated_by: "admin",
    list: "0",
    contact: {
      id: "3",
      email: "code.test@gmail.com",
      first_name: "John",
      last_name: "Wick",
      phone: "",
      ip: "0.0.0.0",
      tags: "tag1, tag2",
      fields: ["Option 1", "||Option 1||Option 2||"],
      customer_acct_name: "",
      orgname: ""
    },
    customer_acct_name: "",
    customer_acct_id: "0",
    orgname: ""
  }
});

// ../ap/packages/pieces/community/activecampaign/src/index.ts
var activecampaign = (0, import_pieces_framework19.createPiece)({
  displayName: "ActiveCampaign",
  description: "Email marketing, marketing automation, and CRM tools you need to create incredible customer experiences.",
  auth: activeCampaignAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/activecampaign.png",
  categories: [import_pieces_framework20.PieceCategory.MARKETING, import_pieces_framework20.PieceCategory.SALES_AND_CRM],
  authors: ["kishanprmr", "abuaboud"],
  actions: [
    addContactToAccountAction,
    addTagToContactAction,
    createAccountAction,
    createContactAction,
    updateAccountAction,
    updateContactAction,
    subscribeOrUnsubscribeContactFromListAction
  ],
  triggers: [
    dealTaskCompletedTrigger,
    newContactNoteTrigger,
    newContactTaskTrigger,
    newDealAddedOrUpdatedTrigger,
    newOrUpdatedAccountTrigger,
    newDealNoteTrigger,
    newDealTaskTrigger,
    newtagAddedOrRemovedFromContactTrigger,
    updatedContactTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  activecampaign
});
