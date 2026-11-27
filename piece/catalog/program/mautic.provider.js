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

// ../ap/packages/pieces/community/mautic/src/index.ts
var index_exports = {};
__export(index_exports, {
  mautic: () => mautic
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mautic/src/lib/actions/create-contact.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/mautic/src/lib/common/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mautic/src/lib/common/helper.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/mautic/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = `
Follow these steps:

1. **Enter the Base URL:** Open your Mautic instance and copy the URL from the address bar. If your dashboard link is "https://mautic.ddev.site/s/dashboard", set your base URL as "https://mautic.ddev.site/".

2. **Enable Basic Authentication:** Log in to Mautic, go to **Settings** > **Configuration** > **API Settings**, and ensure that Basic Authentication is enabled.

`, mauticAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdownDescription,
  props: {
    base_url: import_pieces_framework.Property.ShortText({
      displayName: "Base URL",
      required: !0
    }),
    username: import_pieces_framework.Property.ShortText({
      displayName: "Username",
      required: !0
    }),
    password: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Password",
      required: !0
    })
  },
  required: !0
});

// ../ap/packages/pieces/community/mautic/src/lib/common/helper.ts
var mapMauticToActivepiecesProperty = (type, fieldMetadata, properties) => {
  switch (type) {
    case "lookup":
    case "text":
    case "email":
    case "tel":
    case "region":
    case "country":
    case "locale":
    case "timezone":
    case "url":
      return import_pieces_framework2.Property.ShortText(fieldMetadata);
    case "date":
    case "datetime":
      return import_pieces_framework2.Property.DateTime(fieldMetadata);
    case "number":
      return import_pieces_framework2.Property.Number(fieldMetadata);
    case "boolean":
      return import_pieces_framework2.Property.StaticDropdown(__spreadProps(__spreadValues({}, fieldMetadata), {
        options: {
          options: [
            { value: "no", label: "No" },
            { value: "yes", label: "Yes" }
          ]
        }
      }));
    case "multiselect":
      return import_pieces_framework2.Property.StaticMultiSelectDropdown(__spreadProps(__spreadValues({}, fieldMetadata), {
        options: {
          options: Object.values(properties)[0]
        }
      }));
    case "select":
      return import_pieces_framework2.Property.StaticDropdown(__spreadProps(__spreadValues({}, fieldMetadata), {
        options: {
          options: Object.values(properties)[0]
        }
      }));
    default:
      return console.error(`No support of type ${type}`), null;
  }
}, fetchDynamicFieldsFromMetadata = async (baseUrl, username, password, type) => {
  let request = {
    method: import_pieces_common.HttpMethod.GET,
    url: `${baseUrl.endsWith("/") ? baseUrl : baseUrl + "/"}api/fields/${type}?limit=1000`,
    headers: {
      Authorization: `Basic ${Buffer.from(`${username}:${password}`).toString(
        "base64"
      )}`,
      "Content-Type": "application/json"
    }
  }, result = await import_pieces_common.httpClient.sendRequest(request);
  if (result.status == 200)
    return Object.values(result.body.fields).reduce(
      (fields, field) => {
        let {
          label: displayName,
          alias,
          type: type2,
          properties
        } = field, fieldMetadata = {
          displayName,
          required: !1
        };
        if (!type2) return {};
        let f = mapMauticToActivepiecesProperty(
          type2,
          fieldMetadata,
          properties
        );
        return f && (fields[alias] = f), fields;
      },
      {}
    );
  throw Error(`Unable to fetch ${type} metadata`);
}, getFields = (type) => import_pieces_framework2.Property.DynamicProperties({
  auth: mauticAuth,
  displayName: "All Fields",
  description: "List of all possible fields present",
  required: !0,
  refreshers: [],
  props: async ({ auth }) => {
    if (!auth) return {};
    let { base_url, username, password } = auth.props;
    return fetchDynamicFieldsFromMetadata(base_url, username, password, type);
  }
});

// ../ap/packages/pieces/community/mautic/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var mauticCommon = {
  contactFields: __spreadValues(__spreadValues({}, getFields("contact")), getFields("lead")),
  companyFields: getFields("company"),
  id: import_pieces_framework3.Property.ShortText({
    displayName: "Id of the entity",
    required: !0
  })
}, searchEntity = async (url, searchParams, username, password) => {
  let request = {
    method: import_pieces_common2.HttpMethod.GET,
    url: `${url}${searchParams}`,
    headers: {
      Authorization: `Basic ${Buffer.from(`${username}:${password}`).toString(
        "base64"
      )}`,
      "Content-Type": "application/json"
    }
  }, response = await import_pieces_common2.httpClient.sendRequest(request), length = response.body.total;
  if (!length || length != 1)
    throw Error(
      "The query is not perfect enough to get single result. Please refine"
    );
  return response;
};

// ../ap/packages/pieces/community/mautic/src/lib/actions/create-contact.ts
var createContact = (0, import_pieces_framework4.createAction)({
  auth: mauticAuth,
  description: "Creates a new contact in Mautic CRM",
  // Must be a unique across the piece, this shouldn't be changed.
  audience: "both",
  aiMetadata: {
    description: "Create a new contact (lead) in Mautic from the provided field values (name, email, phone, custom fields, etc.). Use when adding a person to Mautic; to modify an existing contact use Update Contact instead, and to avoid duplicates check first with Search Contact. Not idempotent: each call inserts a new contact, so repeating it produces duplicates.",
    idempotent: !1
  },
  displayName: "Create Contact",
  name: "create_mautic_contact",
  props: {
    fields: mauticCommon.contactFields
  },
  run: async function(context) {
    let { base_url, username, password } = context.auth.props, request = {
      method: import_pieces_common3.HttpMethod.POST,
      url: (base_url.endsWith("/") ? base_url : base_url + "/") + "api/contacts/new",
      body: JSON.stringify(context.propsValue.fields),
      headers: {
        Authorization: "Basic " + Buffer.from(`${username}:${password}`).toString("base64"),
        "Content-Type": "application/json"
      }
    };
    return await import_pieces_common3.httpClient.sendRequest(request);
  }
});

// ../ap/packages/pieces/community/mautic/src/lib/actions/search-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var searchContact = (0, import_pieces_framework5.createAction)({
  auth: mauticAuth,
  description: "Search for a contact in Mautic CRM",
  // Must be a unique across the piece, this shouldn't be changed.
  audience: "both",
  aiMetadata: {
    description: "Look up a contact in Mautic by matching the supplied field values (each provided field becomes an exact-equality filter), returning the first matching contact. Use to find a contact or resolve its id before updating, or to verify whether someone already exists before creating one. Read-only and idempotent.",
    idempotent: !0
  },
  displayName: "Search Contact",
  name: "search_mautic_contact",
  props: {
    fields: mauticCommon.contactFields
  },
  run: async function(context) {
    let { base_url, username, password } = context.auth.props, url = (base_url.endsWith("/") ? base_url : base_url + "/") + "api/contacts", fields = context.propsValue.fields, keys = Object.keys(fields), count = 0, searchParams = "?";
    for (let key of keys)
      fields[key] && (searchParams += `where[${count}][col]=${key}&where[${count}][expr]=eq&where[${count}][val]=${fields[key]}&`, ++count);
    let response = await searchEntity(url, searchParams, username, password);
    return Object.values(response.body.contacts)[0];
  }
});

// ../ap/packages/pieces/community/mautic/src/lib/actions/update-contact.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var updateContact = (0, import_pieces_framework6.createAction)({
  auth: mauticAuth,
  description: "Update a contact in Mautic CRM",
  // Must be a unique across the piece, this shouldn't be changed.
  audience: "both",
  aiMetadata: {
    description: "Update an existing Mautic contact, identified by its numeric contact id, with the provided field values. Use when you already know the contact id (resolve it first with Search Contact if you only have an email or name). Idempotent: applying the same field values to the same id repeatedly leaves the contact in the same state.",
    idempotent: !0
  },
  displayName: "Update Contact With Contact Id",
  name: "update_mautic_contact",
  props: {
    id: mauticCommon.id,
    fields: mauticCommon.contactFields
  },
  run: async function(context) {
    let { base_url, username, password } = context.auth.props, request = {
      method: import_pieces_common4.HttpMethod.PATCH,
      url: `${base_url.endsWith("/") ? base_url : base_url + "/"}api/contacts/${context.propsValue.id}/edit`,
      body: JSON.stringify(context.propsValue.fields),
      headers: {
        Authorization: "Basic " + Buffer.from(`${username}:${password}`).toString("base64"),
        "Content-Type": "application/json"
      }
    };
    return await import_pieces_common4.httpClient.sendRequest(request);
  }
});

// ../ap/packages/pieces/community/mautic/src/lib/actions/create-company.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createCompany = (0, import_pieces_framework7.createAction)({
  auth: mauticAuth,
  description: "Creates a new company in Mautic CRM",
  // Must be a unique across the piece, this shouldn't be changed.
  audience: "both",
  aiMetadata: {
    description: "Create a new company in Mautic from the provided field values (company name, address, custom fields, etc.). Use when adding an organization to Mautic; to modify an existing company use Update Company, and check Search Company first to avoid duplicates. Not idempotent: each call inserts a new company record.",
    idempotent: !1
  },
  displayName: "Create Company",
  name: "create_mautic_company",
  props: {
    fields: mauticCommon.companyFields
  },
  run: async function(context) {
    let { base_url, username, password } = context.auth.props, request = {
      method: import_pieces_common5.HttpMethod.POST,
      url: (base_url.endsWith("/") ? base_url : base_url + "/") + "api/companies/new",
      body: JSON.stringify(context.propsValue.fields),
      headers: {
        Authorization: "Basic " + Buffer.from(`${username}:${password}`).toString("base64"),
        "Content-Type": "application/json"
      }
    };
    return await import_pieces_common5.httpClient.sendRequest(request);
  }
});

// ../ap/packages/pieces/community/mautic/src/lib/actions/search-company.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var searchCompany = (0, import_pieces_framework8.createAction)({
  auth: mauticAuth,
  description: "Search for a company in Mautic CRM",
  // Must be a unique across the piece, this shouldn't be changed.
  audience: "both",
  aiMetadata: {
    description: "Look up a company in Mautic by matching the supplied field values (combined into a search query), returning the first matching company. Use to find a company or resolve its id before updating, or to verify whether an organization already exists before creating one. Read-only and idempotent.",
    idempotent: !0
  },
  displayName: "Search Company",
  name: "search_mautic_company",
  props: {
    fields: mauticCommon.companyFields
  },
  run: async function(context) {
    let { base_url, username, password } = context.auth.props, url = (base_url.endsWith("/") ? base_url : base_url + "/") + "api/companies", fields = context.propsValue.fields, keys = Object.keys(fields), searchParams = "?";
    for (let key of keys)
      fields[key] && (searchParams += `search=${key}:${fields[key]}&`);
    let response = await searchEntity(url, searchParams, username, password);
    return Object.values(response.body.companies)[0];
  }
});

// ../ap/packages/pieces/community/mautic/src/lib/actions/update-company.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var updateCompany = (0, import_pieces_framework9.createAction)({
  auth: mauticAuth,
  description: "Update a company in Mautic CRM",
  // Must be a unique across the piece, this shouldn't be changed.
  audience: "both",
  aiMetadata: {
    description: "Update an existing Mautic company, identified by its numeric company id, with the provided field values (empty fields are dropped so they are left unchanged). Use when you already know the company id (resolve it first with Search Company if you only have a name). Idempotent: applying the same field values to the same id repeatedly leaves the company in the same state.",
    idempotent: !0
  },
  displayName: "Update Company With Contact Id",
  name: "update_mautic_company",
  props: {
    id: mauticCommon.id,
    fields: mauticCommon.companyFields
  },
  run: async function(context) {
    let { base_url, username, password } = context.auth.props, fields = context.propsValue.fields, keys = Object.keys(fields);
    for (let key of keys)
      fields[key] || delete fields[key];
    let request = {
      method: import_pieces_common6.HttpMethod.PATCH,
      url: `${base_url.endsWith("/") ? base_url : base_url + "/"}api/companies/${context.propsValue.id}/edit`,
      body: JSON.stringify(fields),
      headers: {
        Authorization: "Basic " + Buffer.from(`${username}:${password}`).toString("base64"),
        "Content-Type": "application/json"
      }
    };
    return await import_pieces_common6.httpClient.sendRequest(request);
  }
});

// ../ap/packages/pieces/community/mautic/src/lib/triggers/index.ts
var import_pieces_common7 = require("@activepieces/pieces-common");
var import_pieces_framework10 = require("@activepieces/pieces-framework"), contactTestData = {
  contact: {
    id: 38186,
    points: 0,
    color: null,
    fields: {
      core: {
        points: {
          id: "9",
          label: "Points",
          alias: "points",
          type: "number",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: "0"
        },
        title: {
          id: "1",
          label: "Title",
          alias: "title",
          type: "lookup",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: ""
        },
        firstname: {
          id: "2",
          label: "First Name",
          alias: "firstname",
          type: "text",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: "Test"
        },
        lastname: {
          id: "3",
          label: "Last Name",
          alias: "lastname",
          type: "text",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: "Contact"
        },
        company: {
          id: "4",
          label: "Company",
          alias: "company",
          type: "text",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: ""
        },
        position: {
          id: "5",
          label: "Position",
          alias: "position",
          type: "text",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: ""
        },
        email: {
          id: "6",
          label: "Email",
          alias: "email",
          type: "email",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: "Test@email.com"
        },
        phone: {
          id: "8",
          label: "Phone",
          alias: "phone",
          type: "tel",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: ""
        },
        mobile: {
          id: "7",
          label: "Mobile",
          alias: "mobile",
          type: "tel",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: ""
        },
        address1: {
          id: "11",
          label: "Address Line 1",
          alias: "address1",
          type: "text",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: ""
        },
        address2: {
          id: "12",
          label: "Address Line 2",
          alias: "address2",
          type: "text",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: ""
        },
        city: {
          id: "13",
          label: "City",
          alias: "city",
          type: "text",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: ""
        },
        state: {
          id: "14",
          label: "State",
          alias: "state",
          type: "region",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: ""
        },
        zipcode: {
          id: "15",
          label: "Zip Code",
          alias: "zipcode",
          type: "text",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: ""
        },
        country: {
          id: "16",
          label: "Country",
          alias: "country",
          type: "country",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: ""
        },
        fax: {
          id: "10",
          label: "Fax",
          alias: "fax",
          type: "tel",
          group: "core",
          object: "lead",
          is_fixed: "0",
          value: null
        },
        preferred_locale: {
          id: "17",
          label: "Preferred Locale",
          alias: "preferred_locale",
          type: "locale",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: null
        },
        attribution_date: {
          id: "18",
          label: "Attribution Date",
          alias: "attribution_date",
          type: "datetime",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: null
        },
        attribution: {
          id: "19",
          label: "Attribution",
          alias: "attribution",
          type: "number",
          group: "core",
          object: "lead",
          is_fixed: "1",
          value: null
        },
        website: {
          id: "20",
          label: "Website",
          alias: "website",
          type: "url",
          group: "core",
          object: "lead",
          is_fixed: "0",
          value: null
        },
        boolean: {
          id: "43",
          label: "Boolean",
          alias: "boolean",
          type: "boolean",
          group: "core",
          object: "lead",
          is_fixed: "0",
          value: null
        },
        multiple_contact: {
          id: "44",
          label: "Multiple Contact",
          alias: "multiple_contact",
          type: "multiselect",
          group: "core",
          object: "lead",
          is_fixed: "0",
          value: null
        }
      },
      social: {
        facebook: {
          id: "21",
          label: "Facebook",
          alias: "facebook",
          type: "text",
          group: "social",
          object: "lead",
          is_fixed: "0",
          value: null
        },
        foursquare: {
          id: "22",
          label: "Foursquare",
          alias: "foursquare",
          type: "text",
          group: "social",
          object: "lead",
          is_fixed: "0",
          value: null
        },
        instagram: {
          id: "24",
          label: "Instagram",
          alias: "instagram",
          type: "text",
          group: "social",
          object: "lead",
          is_fixed: "0",
          value: null
        },
        linkedin: {
          id: "25",
          label: "LinkedIn",
          alias: "linkedin",
          type: "text",
          group: "social",
          object: "lead",
          is_fixed: "0",
          value: null
        },
        skype: {
          id: "26",
          label: "Skype",
          alias: "skype",
          type: "text",
          group: "social",
          object: "lead",
          is_fixed: "0",
          value: null
        },
        twitter: {
          id: "27",
          label: "Twitter",
          alias: "twitter",
          type: "text",
          group: "social",
          object: "lead",
          is_fixed: "0",
          value: null
        }
      },
      personal: [],
      professional: []
    }
  },
  channel: "email",
  old_status: "contactable",
  new_status: "manual",
  timestamp: "2017-12-01T00:05:18-06:00"
}, triggers = [
  {
    name: "lead_post_save_update",
    displayName: "Contact Updated",
    description: "Triggers when a contact is updated.",
    aiMetadata: {
      description: "Fires when an existing contact (lead) in Mautic is saved after being modified, representing a change to that contact's field values. Use to react to edits on already-existing contacts; for newly created contacts use the New Contact trigger."
    },
    sampleData: {
      "mautic.lead_post_save_update": [contactTestData]
    },
    eventType: "mautic.lead_post_save_update"
  },
  {
    name: "lead_company_change",
    displayName: "Contact Company Subscription Change",
    description: "Triggers when a commpany is added or removed to/from contact.",
    aiMetadata: {
      description: "Fires when a contact's company association changes in Mautic \u2014 that is, when the contact is added to or removed from a company. Use to react to a contact's organization membership changing."
    },
    sampleData: {
      "mautic.lead_company_change": [contactTestData]
    },
    eventType: "mautic.lead_company_change"
  },
  {
    name: "lead_channel_subscription_changed",
    displayName: "Contact Channel Subscription Change",
    description: "Triggers when a contact's channel subscription status changes.",
    aiMetadata: {
      description: "Fires when a contact's subscription status for a communication channel (such as email) changes in Mautic \u2014 for example when they opt in or out of receiving messages. Use to react to contactability/consent changes for a channel."
    },
    sampleData: {
      "mautic.lead_channel_subscription_changed": [contactTestData]
    },
    eventType: "mautic.lead_channel_subscription_changed"
  },
  {
    name: "lead_post_save_new",
    displayName: "New Contact",
    description: "Triggers when a new contact is created.",
    aiMetadata: {
      description: "Fires when a new contact (lead) is created in Mautic, representing a freshly added person. Use to react to contact creation; for changes to existing contacts use the Contact Updated trigger."
    },
    sampleData: {
      "mautic.lead_post_save_new": [contactTestData]
    },
    eventType: "mautic.lead_post_save_new"
  }
].map((props) => registerTrigger(props));
function registerTrigger({
  name,
  displayName,
  eventType,
  description,
  aiMetadata,
  sampleData
}) {
  return (0, import_pieces_framework10.createTrigger)({
    auth: mauticAuth,
    name: `mautic_${name}_trigger`,
    displayName,
    description,
    aiMetadata,
    props: {
      name: import_pieces_framework10.Property.ShortText({
        displayName: "Webhook Name",
        description: "The name the webhook will be searchable by in mautic the webhooks page.",
        required: !0
      }),
      description: import_pieces_framework10.Property.LongText({
        displayName: "Description",
        description: "A short description of the webhook",
        required: !0
      })
    },
    sampleData,
    type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
    async onEnable(context) {
      let { base_url, username, password } = context.auth.props, request = {
        method: import_pieces_common7.HttpMethod.POST,
        url: `${base_url.endsWith("/") ? base_url : base_url + "/"}api/hooks/new`,
        body: {
          name: context.propsValue.name,
          description: context.propsValue.description,
          webhookUrl: context.webhookUrl,
          eventsOrderbyDir: "ASC",
          triggers: [eventType]
        },
        headers: {
          "Content-Type": "application/json",
          Authorization: "Basic " + Buffer.from(`${username}:${password}`).toString("base64")
        },
        queryParams: {}
      }, response = await import_pieces_common7.httpClient.sendRequest(request);
      await context.store.put(`mautic_${name}_trigger`, response.body);
    },
    async onDisable(context) {
      let { base_url, username, password } = context.auth.props, webhook = await context.store.get(`mautic_${name}_trigger`);
      if (webhook != null) {
        let request = {
          method: import_pieces_common7.HttpMethod.DELETE,
          url: `${base_url.endsWith("/") ? base_url : base_url + "/"}api/hooks/${webhook.hook.id}/delete`,
          headers: {
            "Content-Type": "application/json",
            Authorization: "Basic " + Buffer.from(`${username}:${password}`).toString("base64")
          }
        }, response = await import_pieces_common7.httpClient.sendRequest(request);
        console.debug("mautic.trigger.onDisable", response);
      }
    },
    async run(context) {
      return [context.payload.body];
    }
  });
}

// ../ap/packages/pieces/community/mautic/src/index.ts
var mautic = (0, import_pieces_framework11.createPiece)({
  displayName: "Mautic",
  description: "Open-source marketing automation software",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/mautic.png",
  authors: ["bibhuty-did-this", "kanarelo", "kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  categories: [import_pieces_framework12.PieceCategory.MARKETING],
  auth: mauticAuth,
  actions: [
    createContact,
    searchContact,
    updateContact,
    createCompany,
    searchCompany,
    updateCompany,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: mauticAuth,
      baseUrl: (auth) => {
        if (!auth)
          return "";
        let { base_url } = auth.props;
        return `${base_url.endsWith("/") ? base_url : base_url + "/"}api/`;
      },
      authMapping: async (auth) => {
        let { username, password } = auth.props;
        return {
          Authorization: "Basic " + Buffer.from(`${username}:${password}`).toString("base64"),
          "Content-Type": "application/json"
        };
      }
    })
  ],
  triggers
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mautic
});
