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
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: !0 });
}, __copyProps = (to, from, except, desc) => {
  if (from && typeof from == "object" || typeof from == "function")
    for (let key of __getOwnPropNames(from))
      !__hasOwnProp.call(to, key) && key !== except && __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  return to;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: !0 }), mod);

// ../ap/packages/pieces/community/smoove/src/index.ts
var index_exports = {};
__export(index_exports, {
  smoove: () => smoove
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/smoove/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/smoove/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://rest.smoove.io/v1";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/smoove/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), smooveAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "smoove API Key",
  required: !0,
  validate: async ({ auth }) => {
    if (auth)
      try {
        return await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/Lists", {}), {
          valid: !0
        };
      } catch (error) {
        return {
          valid: !1,
          error: "Invalid Api Key"
        };
      }
    return {
      valid: !1,
      error: "Invalid Api Key"
    };
  }
});

// ../ap/packages/pieces/community/smoove/src/lib/actions/add-or-update-subscriber.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/smoove/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var contactIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: smooveAuth,
  displayName: "Contact",
  description: "Select the contact to unsubscribe",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(auth.secret_text, import_pieces_common3.HttpMethod.GET, "/Contacts?fields=id,firstName,lastName,email,cellphone")).map((Contact) => {
          let name = `${Contact.firstName || ""} ${Contact.lastName || ""}`.trim(), email = Contact.email ? ` (${Contact.email})` : "", phone = !Contact.email && Contact.cellphone ? ` (${Contact.cellphone})` : "";
          return {
            label: name ? `${name}${email || phone}` : Contact.email ? Contact.email : Contact.cellphone ? Contact.cellphone : `Contact ${Contact.id}`,
            value: Contact.id
          };
        }),
        placeholder: "Select a contact to unsubscribe"
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading contacts"
      };
    }
  }
}), emailDropdown = import_pieces_framework2.Property.Dropdown({
  auth: smooveAuth,
  displayName: "Email Address",
  description: "Select an existing contact email or type a new one",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(auth.secret_text, import_pieces_common3.HttpMethod.GET, "/Contacts?fields=id,firstName,lastName,email")).filter((Contact) => Contact.email).map((Contact) => {
          let name = `${Contact.firstName || ""} ${Contact.lastName || ""}`.trim();
          return {
            label: name ? `${Contact.email} (${name})` : Contact.email,
            value: Contact.email
          };
        }),
        placeholder: "Select existing email or type new one"
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Type email address manually"
      };
    }
  }
}), listsDropdown = import_pieces_framework2.Property.MultiSelectDropdown({
  auth: smooveAuth,
  displayName: "Lists",
  description: "Select lists to subscribe the contact to",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(auth.secret_text, import_pieces_common3.HttpMethod.GET, "/Lists")).map((List) => ({
          label: List.name || `List ${List.id}`,
          value: List.id
        })),
        placeholder: "Select lists (optional)"
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading lists"
      };
    }
  }
}), LandingPageIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: smooveAuth,
  displayName: "LandingPage/Form ID",
  description: "Select the andingPage/Form  ",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(auth.secret_text, import_pieces_common3.HttpMethod.GET, "/LandingPages")).map((LandingPage) => ({
          label: LandingPage.formTitle,
          value: LandingPage.formId
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading teams"
      };
    }
  }
});

// ../ap/packages/pieces/community/smoove/src/lib/actions/add-or-update-subscriber.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), addOrUpdateSubscriber = (0, import_pieces_framework3.createAction)({
  auth: smooveAuth,
  name: "addOrUpdateSubscriber",
  displayName: "Add or Update Subscriber",
  description: "Create or update subscriber data, and subscribe/unsubscribe from lists",
  audience: "both",
  aiMetadata: { description: "Create or update a Smoove contact by email and optionally link it to lists, set profile/custom fields, and email/SMS consent. The Operation prop switches the upsert behavior: create-only, create-or-update-if-exists (default), restore-if-deleted, restore-if-unsubscribed, or override-nullable-values. Use to push a subscriber into Smoove. Email is required; not idempotent since it creates or mutates the contact on each call.", idempotent: !1 },
  props: {
    email: emailDropdown,
    firstName: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "The first name of the contact",
      required: !1
    }),
    lastName: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "The last name of the contact",
      required: !1
    }),
    dateOfBirth: import_pieces_framework3.Property.DateTime({
      displayName: "Date of Birth",
      description: "The date of birth of the contact",
      required: !1
    }),
    phone: import_pieces_framework3.Property.ShortText({
      displayName: "Phone Number",
      description: "The primary phone number of the contact",
      required: !1
    }),
    cellPhone: import_pieces_framework3.Property.ShortText({
      displayName: "Cell Phone",
      description: "The cell phone number of the contact",
      required: !1
    }),
    address: import_pieces_framework3.Property.ShortText({
      displayName: "Address",
      description: "The street address of the contact",
      required: !1
    }),
    city: import_pieces_framework3.Property.ShortText({
      displayName: "City",
      description: "The city of the contact",
      required: !1
    }),
    country: import_pieces_framework3.Property.ShortText({
      displayName: "Country",
      description: "The country of the contact",
      required: !1
    }),
    company: import_pieces_framework3.Property.ShortText({
      displayName: "Company",
      description: "The company name of the contact",
      required: !1
    }),
    position: import_pieces_framework3.Property.ShortText({
      displayName: "Position",
      description: "The job title or position of the contact",
      required: !1
    }),
    externalId: import_pieces_framework3.Property.ShortText({
      displayName: "External ID",
      description: "External identifier for the contact (useful for syncing with other systems)",
      required: !1
    }),
    campaignSource: import_pieces_framework3.Property.ShortText({
      displayName: "Campaign Source",
      description: "The campaign source that brought this contact",
      required: !1
    }),
    canReceiveEmails: import_pieces_framework3.Property.Checkbox({
      displayName: "Can Receive Emails",
      description: "Whether the contact can receive email communications",
      required: !1,
      defaultValue: !0
    }),
    canReceiveSmsMessages: import_pieces_framework3.Property.Checkbox({
      displayName: "Can Receive SMS",
      description: "Whether the contact can receive SMS messages",
      required: !1,
      defaultValue: !0
    }),
    lists: listsDropdown,
    customFields: import_pieces_framework3.Property.Object({
      displayName: "Custom Fields",
      description: "Custom fields data (JSON object with field IDs as keys)",
      required: !1
    }),
    operation: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Operation",
      description: "Choose the contact operation type",
      required: !0,
      defaultValue: "updateIfExists",
      options: {
        options: [
          { label: "Create Only", value: "create" },
          { label: "Create or Update if Exists", value: "updateIfExists" },
          { label: "Restore if Deleted", value: "restoreIfDeleted" },
          { label: "Restore if Unsubscribed", value: "restoreIfUnsubscribed" },
          { label: "Override Nullable Values", value: "overrideNullableValue" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let {
      email,
      firstName,
      lastName,
      dateOfBirth,
      phone,
      cellPhone,
      address,
      city,
      country,
      company,
      position,
      externalId,
      campaignSource,
      canReceiveEmails,
      canReceiveSmsMessages,
      lists,
      customFields,
      operation
    } = propsValue, endpoint = "/Contacts", queryParams = [];
    operation === "updateIfExists" && queryParams.push("updateIfExists=true"), operation === "restoreIfDeleted" && queryParams.push("restoreIfDeleted=true"), operation === "restoreIfUnsubscribed" && queryParams.push("restoreIfUnsubscribed=true"), operation === "overrideNullableValue" && queryParams.push("overrideNullableValue=true"), queryParams.length && (endpoint += "?" + queryParams.join("&"));
    let body = {
      email
    };
    return firstName && (body.firstName = firstName), lastName && (body.lastName = lastName), dateOfBirth && (body.dateOfBirth = dateOfBirth), phone && (body.phone = phone), cellPhone && (body.cellPhone = cellPhone), address && (body.address = address), city && (body.city = city), country && (body.country = country), company && (body.company = company), position && (body.position = position), externalId && (body.externalId = externalId), campaignSource && (body.campaignSource = campaignSource), typeof canReceiveEmails == "boolean" && (body.canReceiveEmails = canReceiveEmails), typeof canReceiveSmsMessages == "boolean" && (body.canReceiveSmsMessages = canReceiveSmsMessages), lists && Array.isArray(lists) && lists.length > 0 && (body.lists_Linked = lists), customFields && typeof customFields == "object" && (body.customFields = customFields), await makeRequest(auth.secret_text, import_pieces_common4.HttpMethod.POST, endpoint, body);
  }
});

// ../ap/packages/pieces/community/smoove/src/lib/actions/create-a-list.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), createAList = (0, import_pieces_framework4.createAction)({
  auth: smooveAuth,
  name: "createAList",
  displayName: "Create a List",
  description: "Create a new mailing list with custom settings and descriptions",
  audience: "both",
  aiMetadata: { description: "Create a new mailing list in Smoove with names, descriptions, and subscribe/unsubscribe/portal/visibility permissions. Use when a new list must exist before linking subscribers to it. Requires a list name; not idempotent \u2014 each call creates a separate list even with identical input.", idempotent: !1 },
  props: {
    name: import_pieces_framework4.Property.ShortText({
      displayName: "List Name",
      description: "The name of the list to be created",
      required: !0
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "Internal description of the list for your reference",
      required: !1,
      defaultValue: "This list is a list for new subscribers"
    }),
    publicName: import_pieces_framework4.Property.ShortText({
      displayName: "Public Name",
      description: "The public-facing name of the list visible to subscribers",
      required: !1,
      defaultValue: "My subscribers public name list"
    }),
    publicDescription: import_pieces_framework4.Property.LongText({
      displayName: "Public Description",
      description: "Public description of the list visible to subscribers",
      required: !1,
      defaultValue: "Public name - This list is a list for new subscribers"
    }),
    isPublic: import_pieces_framework4.Property.Checkbox({
      displayName: "Is Public",
      description: "Make this list publicly visible",
      required: !0,
      defaultValue: !0
    }),
    allowsUsersToSubscribe: import_pieces_framework4.Property.Checkbox({
      displayName: "Allow Users to Subscribe",
      description: "Allow users to subscribe to this list themselves",
      required: !0,
      defaultValue: !0
    }),
    allowsUsersToUnsubscribe: import_pieces_framework4.Property.Checkbox({
      displayName: "Allow Users to Unsubscribe",
      description: "Allow users to unsubscribe from this list themselves",
      required: !0,
      defaultValue: !0
    }),
    isPortal: import_pieces_framework4.Property.Checkbox({
      displayName: "Is Portal",
      description: "Enable portal access for this list",
      required: !0,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    let { name, description, publicName, publicDescription, isPublic, allowsUsersToSubscribe, allowsUsersToUnsubscribe, isPortal } = propsValue, body = {
      name,
      description,
      publicName,
      publicDescription,
      permissions: {
        isPublic,
        allowsUsersToSubscribe,
        allowsUsersToUnsubscribe,
        isPortal
      }
    };
    return await makeRequest(auth.secret_text, import_pieces_common5.HttpMethod.POST, "/Lists", body);
  }
});

// ../ap/packages/pieces/community/smoove/src/lib/actions/find-subscriber.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), findSubscriber = (0, import_pieces_framework5.createAction)({
  auth: smooveAuth,
  name: "findSubscriber",
  displayName: "Find Subscriber",
  description: "Search for subscribers by unique identifiers (ID, email, phone, external ID) or name",
  audience: "both",
  aiMetadata: { description: "Look up Smoove contacts via the Search Type prop, which selects one of three modes: by identifier (contact ID, email, phone, or external ID), by name (first/last name), or all active contacts with pagination. Use to resolve a contact ID or confirm a subscriber exists before acting on them. Identifier search requires at least one identifier and name search requires a name; idempotent read-only lookup.", idempotent: !0 },
  props: {
    searchType: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Search Type",
      description: "Choose how to search for subscribers",
      required: !0,
      defaultValue: "identifier",
      options: {
        options: [
          { label: "By Identifier (ID, Email, Phone, External ID)", value: "identifier" },
          { label: "By Name (First Name, Last Name)", value: "name" },
          { label: "All Active Contacts (with pagination)", value: "all" }
        ]
      }
    }),
    id: import_pieces_framework5.Property.ShortText({
      displayName: "Contact ID",
      description: "The unique ID of the contact",
      required: !1
    }),
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email Address",
      description: "The email address of the contact",
      required: !1
    }),
    cellphone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone Number",
      description: "The phone number of the contact (cellphone)",
      required: !1
    }),
    externalId: import_pieces_framework5.Property.ShortText({
      displayName: "External ID",
      description: "The external ID of the contact",
      required: !1
    }),
    firstName: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      description: "Search by first name",
      required: !1
    }),
    lastName: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      description: "Search by last name",
      required: !1
    }),
    fields: import_pieces_framework5.Property.ShortText({
      displayName: "Fields to Return",
      description: "Comma-separated list of fields to return (e.g., id,email,firstName,lastName). Leave empty for all fields.",
      required: !1
    }),
    includeCustomFields: import_pieces_framework5.Property.Checkbox({
      displayName: "Include Custom Fields",
      description: "Include custom fields in the response",
      required: !1,
      defaultValue: !1
    }),
    includeLinkedLists: import_pieces_framework5.Property.Checkbox({
      displayName: "Include Linked Lists",
      description: "Include linked lists in the response",
      required: !1,
      defaultValue: !1
    }),
    skip: import_pieces_framework5.Property.Number({
      displayName: "Skip Records",
      description: "Number of records to skip (for pagination, default: 0)",
      required: !1,
      defaultValue: 0
    }),
    take: import_pieces_framework5.Property.Number({
      displayName: "Max Records",
      description: "Maximum number of records to return (default: 100, max: 100)",
      required: !1,
      defaultValue: 100
    }),
    sort: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Sort By",
      description: "Sort the results by a specific field",
      required: !1,
      defaultValue: "-id",
      options: {
        options: [
          { label: "ID (Newest First)", value: "-id" },
          { label: "ID (Oldest First)", value: "id" },
          { label: "Email (A-Z)", value: "email" },
          { label: "Email (Z-A)", value: "-email" },
          { label: "First Name (A-Z)", value: "firstName" },
          { label: "First Name (Z-A)", value: "-firstName" },
          { label: "Last Changed (Newest)", value: "-lastChanged" },
          { label: "Last Changed (Oldest)", value: "lastChanged" },
          { label: "Signup Date (Newest)", value: "-timestampSignup" },
          { label: "Signup Date (Oldest)", value: "timestampSignup" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let {
      searchType,
      id,
      email,
      cellphone,
      externalId,
      firstName,
      lastName,
      fields,
      includeCustomFields,
      includeLinkedLists,
      skip,
      take,
      sort
    } = propsValue, queryParams = [];
    if (searchType === "identifier") {
      if (!id && !email && !cellphone && !externalId)
        throw new Error("For identifier search, please provide at least one: Contact ID, Email, Phone, or External ID");
      id && queryParams.push(`id=${encodeURIComponent(id)}`), email && queryParams.push(`email=${encodeURIComponent(email)}`), cellphone && queryParams.push(`cellphone=${encodeURIComponent(cellphone)}`), externalId && queryParams.push(`externalid=${encodeURIComponent(externalId)}`);
    } else if (searchType === "name") {
      if (!firstName && !lastName)
        throw new Error("For name search, please provide First Name and/or Last Name");
      firstName && queryParams.push(`firstname=${encodeURIComponent(firstName)}`), lastName && queryParams.push(`lastname=${encodeURIComponent(lastName)}`);
    }
    fields && fields.trim() && queryParams.push(`fields=${encodeURIComponent(fields.trim())}`), includeCustomFields && queryParams.push("includeCustomFields=true"), includeLinkedLists && queryParams.push("includeLinkedLists=true");
    let skipValue = Math.max(0, skip || 0), takeValue = Math.min(Math.max(1, take || 100), 100);
    queryParams.push(`skip=${skipValue}`), queryParams.push(`take=${takeValue}`), sort && queryParams.push(`sort=${encodeURIComponent(sort)}`);
    let endpoint = `/Contacts?${queryParams.join("&")}`;
    try {
      let response = await makeRequest(auth.secret_text, import_pieces_common6.HttpMethod.GET, endpoint);
      if (!response)
        return {
          success: !1,
          message: "No response from server",
          data: [],
          total: 0
        };
      let contacts = Array.isArray(response) ? response : [response];
      return {
        success: !0,
        message: `Found ${contacts.length} contact(s)`,
        data: contacts,
        total: contacts.length,
        searchType,
        pagination: {
          skip: skipValue,
          take: takeValue,
          returned: contacts.length
        }
      };
    } catch (error) {
      if (error.message && error.message.includes("404"))
        return {
          success: !1,
          message: "No contacts found matching the provided criteria",
          data: [],
          total: 0,
          searchType
        };
      throw error;
    }
  }
});

// ../ap/packages/pieces/community/smoove/src/lib/actions/unsubscribe.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), unsubscribe = (0, import_pieces_framework6.createAction)({
  auth: smooveAuth,
  name: "unsubscribe",
  displayName: "Unsubscribe Subscriber",
  description: 'Unsubscribe a contact from all lists and move them to the "Unsubscribed" list',
  audience: "both",
  aiMetadata: { description: "Unsubscribe a Smoove contact from all lists and move them to the Unsubscribed list, recording a reason. Use to opt a subscriber out of all communications. Requires a contact ID; not idempotent \u2014 it posts a state-changing unsubscribe with a reason on each call.", idempotent: !1 },
  props: {
    contactId: contactIdDropdown,
    reason: import_pieces_framework6.Property.ShortText({
      displayName: "Unsubscribe Reason",
      description: "Reason for unsubscribing the contact",
      required: !0,
      defaultValue: "Unsubscribed via automation"
    })
  },
  async run({ auth, propsValue }) {
    let { contactId, reason } = propsValue, contactIdStr = contactId;
    if (!contactIdStr || contactIdStr.trim() === "")
      throw new Error("Please select a contact from the dropdown");
    let body = {
      reason: reason || "Unsubscribed via automation"
    };
    return await makeRequest(
      auth.secret_text,
      import_pieces_common7.HttpMethod.POST,
      `/Contacts/${encodeURIComponent(contactIdStr.trim())}/Unsubscribe?by=ContactId`,
      body
    );
  }
});

// ../ap/packages/pieces/community/smoove/src/lib/triggers/new-list-created.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common8.DedupeStrategy.LAST_ITEM,
  items: async ({ auth, propsValue }) => {
    try {
      let { fields, sortBy, maxItems = 50 } = propsValue, queryParams = [];
      fields && fields.trim() && queryParams.push(`fields=${encodeURIComponent(fields.trim())}`), sortBy && queryParams.push(`sort=${encodeURIComponent(sortBy)}`), queryParams.push("skip=0"), queryParams.push(`take=${Math.min(maxItems, 100)}`);
      let endpoint = `/Lists${queryParams.length ? "?" + queryParams.join("&") : ""}`, response = await makeRequest(auth.secret_text, import_pieces_common8.HttpMethod.GET, endpoint);
      return response ? (Array.isArray(response) ? response : [response]).filter((item) => item.id).sort((a, b) => (b.id || 0) - (a.id || 0)).map((item) => ({
        id: item.id.toString(),
        data: item
      })) : [];
    } catch (error) {
      return console.error("Error fetching lists:", error), [];
    }
  }
}, newListCreated = (0, import_pieces_framework7.createTrigger)({
  auth: smooveAuth,
  name: "newListCreated",
  displayName: "New List Created",
  description: "Fires when a new mailing list is created in your Smoove account",
  aiMetadata: {
    description: "Fires when a new mailing list is created in the connected Smoove account, polling the Lists endpoint to detect newly added lists."
  },
  props: {
    fields: import_pieces_framework7.Property.ShortText({
      displayName: "Fields to Include",
      description: "Comma-separated list of fields to include (e.g., id,name,description,contactsCount). Leave empty for all fields.",
      required: !1,
      defaultValue: "id,name,description,publicName,contactsCount,permissions"
    }),
    sortBy: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Sort Order",
      description: "How to sort the lists for monitoring",
      required: !1,
      defaultValue: "-id",
      options: {
        options: [
          { label: "Newest First (by ID)", value: "-id" },
          { label: "Oldest First (by ID)", value: "id" },
          { label: "Name (A-Z)", value: "name" },
          { label: "Name (Z-A)", value: "-name" }
        ]
      }
    }),
    maxItems: import_pieces_framework7.Property.Number({
      displayName: "Max Lists to Monitor",
      description: "Maximum number of lists to check for new additions (1-100)",
      required: !1,
      defaultValue: 50
    })
  },
  sampleData: {
    id: 173584,
    name: "Welcome Series Subscribers",
    description: "Automated welcome email series for new signups",
    publicName: "Welcome Series",
    publicDescription: "Join our welcome series to get started with our platform",
    permissions: {
      isPublic: !0,
      allowsUsersToSubscribe: !0,
      allowsUsersToUnsubscribe: !0,
      isPortal: !1
    },
    contactsCount: 0,
    dateCreated: "2025-01-22T14:30:00Z",
    isActive: !0,
    tags: ["welcome", "automation", "new-users"]
  },
  type: import_pieces_framework7.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common8.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common8.pollingHelper.onEnable(polling, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common8.pollingHelper.onDisable(polling, { store, auth, propsValue });
  },
  async run(context) {
    return await import_pieces_common8.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/smoove/src/lib/triggers/new-subscriber.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common9.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    try {
      let {
        fields,
        includeCustomFields = !1,
        includeLinkedLists = !1,
        joinSourceFilter,
        sortBy = "-timestampSignup",
        maxItems = 100
      } = propsValue, queryParams = [];
      fields && fields.trim() ? queryParams.push(`fields=${encodeURIComponent(fields.trim())}`) : queryParams.push("fields=id,email,firstName,lastName,timestampSignup,joinSource,lastChanged,canReceiveEmails,campaignSource,ipSignup"), includeCustomFields && queryParams.push("includeCustomFields=true"), includeLinkedLists && queryParams.push("includeLinkedLists=true"), queryParams.push(`sort=${encodeURIComponent(sortBy)}`), queryParams.push("skip=0"), queryParams.push(`take=${Math.min(maxItems, 100)}`);
      let endpoint = `/Contacts?${queryParams.join("&")}`, response = await makeRequest(auth.secret_text, import_pieces_common9.HttpMethod.GET, endpoint);
      return response ? (Array.isArray(response) ? response : [response]).filter((item) => {
        if (!item.timestampSignup) return !1;
        let isNew = new Date(item.timestampSignup).getTime() > (lastFetchEpochMS != null ? lastFetchEpochMS : 0);
        return joinSourceFilter && joinSourceFilter !== "All" && item.joinSource !== joinSourceFilter ? !1 : isNew;
      }).sort((a, b) => new Date(b.timestampSignup || 0).getTime() - new Date(a.timestampSignup || 0).getTime()).map((item) => {
        var _a;
        return {
          epochMilliSeconds: new Date(item.timestampSignup || 0).getTime(),
          data: __spreadProps(__spreadValues(__spreadValues(__spreadValues({
            id: (_a = item.id) == null ? void 0 : _a.toString(),
            email: item.email,
            firstName: item.firstName,
            lastName: item.lastName,
            phone: item.phone,
            cellPhone: item.cellPhone,
            address: item.address,
            city: item.city,
            country: item.country,
            company: item.company,
            position: item.position,
            dateOfBirth: item.dateOfBirth,
            externalId: item.externalId,
            canReceiveEmails: item.canReceiveEmails,
            canReceiveSmsMessages: item.canReceiveSmsMessages,
            ipSignup: item.ipSignup,
            timestampSignup: item.timestampSignup,
            joinSource: item.joinSource,
            campaignSource: item.campaignSource,
            lastChanged: item.lastChanged,
            listAssociationTime: item.listAssociationTime,
            c_DaysSinceSignup: item.c_DaysSinceSignup,
            deleted: item.deleted
          }, includeLinkedLists && item.lists_Linked ? { lists_Linked: item.lists_Linked } : {}), includeCustomFields && item.customFields ? { customFields: item.customFields } : {}), item.unsubscribeReasonType ? {
            unsubscribeReasonType: item.unsubscribeReasonType,
            unsubscribeReasonComment: item.unsubscribeReasonComment,
            timestampUnsubscribed: item.timestampUnsubscribed
          } : {}), {
            triggerInfo: {
              detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
              source: "smoove",
              type: "new_subscriber",
              joinSource: item.joinSource,
              daysSinceSignup: item.c_DaysSinceSignup || 0
            }
          })
        };
      }) : [];
    } catch (error) {
      return console.error("Error fetching new subscribers:", error), [];
    }
  }
}, newSubscriber = (0, import_pieces_framework8.createTrigger)({
  auth: smooveAuth,
  name: "newSubscriber",
  displayName: "New Subscriber",
  description: "Fires when a new subscriber is added to your Smoove account",
  aiMetadata: {
    description: "Fires when a new contact signs up in the connected Smoove account, detected by polling contacts for recent signup timestamps; can be filtered to a specific join source (e.g. landing page, form, API, import)."
  },
  props: {
    fields: import_pieces_framework8.Property.ShortText({
      displayName: "Fields to Include",
      description: "Comma-separated fields to include (e.g., id,email,firstName,lastName,timestampSignup). Leave empty for default fields.",
      required: !1,
      defaultValue: "id,email,firstName,lastName,timestampSignup,joinSource,campaignSource,canReceiveEmails,ipSignup"
    }),
    includeCustomFields: import_pieces_framework8.Property.Checkbox({
      displayName: "Include Custom Fields",
      description: "Include custom field data in subscriber information",
      required: !1,
      defaultValue: !1
    }),
    includeLinkedLists: import_pieces_framework8.Property.Checkbox({
      displayName: "Include Linked Lists",
      description: "Include list associations in subscriber information",
      required: !1,
      defaultValue: !1
    }),
    joinSourceFilter: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Join Source Filter",
      description: 'Filter subscribers by how they joined (leave as "All" to monitor all sources)',
      required: !1,
      defaultValue: "All",
      options: {
        options: [
          { label: "All Sources", value: "All" },
          { label: "Unknown", value: "Unknown" },
          { label: "Manual Entry", value: "ByHand" },
          { label: "Import", value: "Import" },
          { label: "Facebook", value: "FaceBook" },
          { label: "API", value: "API" },
          { label: "Landing Page", value: "LandingPage" },
          { label: "Embed Form", value: "Embed" },
          { label: "Popup", value: "Popup" },
          { label: "QR Code", value: "QR" },
          { label: "SMS", value: "SMS" },
          { label: "WhatsApp", value: "Whatsapp" },
          { label: "Form", value: "Form" },
          { label: "Automation", value: "Automation" }
        ]
      }
    }),
    sortBy: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Sort Order",
      description: "How to sort contacts for monitoring new subscribers",
      required: !1,
      defaultValue: "-timestampSignup",
      options: {
        options: [
          { label: "Newest Signups First", value: "-timestampSignup" },
          { label: "Oldest Signups First", value: "timestampSignup" },
          { label: "Recently Changed First", value: "-lastChanged" },
          { label: "ID (Newest First)", value: "-id" },
          { label: "Email (A-Z)", value: "email" }
        ]
      }
    }),
    maxItems: import_pieces_framework8.Property.Number({
      displayName: "Max Items to Check",
      description: "Maximum number of contacts to check for new subscribers (1-100)",
      required: !1,
      defaultValue: 100
    })
  },
  sampleData: {
    id: "845986993",
    email: "john.doe@example.com",
    firstName: "John",
    lastName: "Doe",
    phone: "+1234567890",
    cellPhone: "+1234567890",
    address: "123 Main Street",
    city: "New York",
    country: "United States",
    company: "Tech Corp",
    position: "Marketing Manager",
    dateOfBirth: "1990-05-15T00:00:00Z",
    externalId: "ext_12345",
    canReceiveEmails: !0,
    canReceiveSmsMessages: !0,
    ipSignup: "192.168.1.100",
    timestampSignup: "2025-01-22T14:30:00Z",
    joinSource: "LandingPage",
    campaignSource: "winter-campaign-2025",
    lastChanged: "2025-01-22T14:30:00Z",
    listAssociationTime: "2025-01-22T14:30:00Z",
    c_DaysSinceSignup: 0,
    deleted: !1,
    lists_Linked: [5556, 8886],
    customFields: {
      industry: "Technology",
      lead_score: 85,
      interests: ["email marketing", "automation"]
    },
    triggerInfo: {
      detectedAt: "2025-01-22T14:35:00Z",
      source: "smoove",
      type: "new_subscriber",
      joinSource: "LandingPage",
      daysSinceSignup: 0
    }
  },
  type: import_pieces_framework8.TriggerStrategy.POLLING,
  async test(context) {
    var _a;
    try {
      let response = await makeRequest(context.auth.secret_text, import_pieces_common9.HttpMethod.GET, "/Contacts?take=1&sort=-timestampSignup"), item = Array.isArray(response) ? response[0] : response;
      if (!item)
        throw new Error("No subscribers found to test with");
      return [{
        id: (_a = item.id) == null ? void 0 : _a.toString(),
        email: item.email,
        firstName: item.firstName,
        lastName: item.lastName,
        phone: item.phone,
        cellPhone: item.cellPhone,
        timestampSignup: item.timestampSignup,
        joinSource: item.joinSource,
        campaignSource: item.campaignSource,
        canReceiveEmails: item.canReceiveEmails,
        canReceiveSmsMessages: item.canReceiveSmsMessages,
        triggerInfo: {
          detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
          source: "smoove",
          type: "new_subscriber_test"
        }
      }];
    } catch (error) {
      throw new Error("Unable to test trigger - please check your API connection");
    }
  },
  async onEnable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common9.pollingHelper.onEnable(polling2, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common9.pollingHelper.onDisable(polling2, { store, auth, propsValue });
  },
  async run(context) {
    return await import_pieces_common9.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/smoove/src/lib/triggers/new-form-created.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var polling3 = {
  strategy: import_pieces_common10.DedupeStrategy.LAST_ITEM,
  items: async ({ auth, propsValue }) => {
    try {
      let endpoint = "/LandingPages", { formType } = propsValue;
      formType && formType !== "All" && (endpoint += `?type=${encodeURIComponent(formType)}`);
      let response = await makeRequest(auth.secret_text, import_pieces_common10.HttpMethod.GET, endpoint);
      return response ? (Array.isArray(response) ? response : [response]).filter((item) => item.formId && item.formTitle).sort((a, b) => (b.formId || 0) - (a.formId || 0)).map((item) => {
        let _a = item, { formId, formTitle, formType: formType2 } = _a, otherFields = __objRest(_a, ["formId", "formTitle", "formType"]);
        return {
          id: formId.toString(),
          data: __spreadValues({
            formId,
            formTitle,
            formType: formType2 || "Unknown"
          }, otherFields)
        };
      }) : [];
    } catch (error) {
      return console.error("Error fetching landing pages:", error), [];
    }
  }
}, newFormCreated = (0, import_pieces_framework9.createTrigger)({
  auth: smooveAuth,
  name: "newFormCreated",
  displayName: "New Form Created",
  description: "Fires when a new form/landing page is created in your Smoove account",
  aiMetadata: {
    description: "Fires when a new form or landing page is created in the connected Smoove account, optionally filtered to a specific form type (landing page, Facebook, mobile, embed, or popup)."
  },
  props: {
    formType: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Form Type Filter",
      description: 'Filter by specific form type, or select "All" to monitor all form types',
      required: !1,
      defaultValue: "All",
      options: {
        options: [
          { label: "All Types", value: "All" },
          { label: "Landing Page", value: "LandingPage" },
          { label: "Facebook Form", value: "Facebook" },
          { label: "Mobile Form", value: "Mobile" },
          { label: "Embed Form", value: "Embed" },
          { label: "Popup Form", value: "Popup" }
        ]
      }
    })
  },
  sampleData: {
    formId: 581014,
    formTitle: "Contact Us Landing Page",
    formType: "LandingPage",
    dateCreated: "2025-01-22T10:30:00Z",
    isActive: !0,
    url: "https://example.smoove.io/landing/contact-us",
    description: "Main contact form for lead generation",
    fields: [
      {
        name: "email",
        type: "email",
        required: !0
      },
      {
        name: "firstName",
        type: "text",
        required: !0
      },
      {
        name: "phone",
        type: "tel",
        required: !1
      }
    ]
  },
  type: import_pieces_framework9.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common10.pollingHelper.test(polling3, context);
  },
  async onEnable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common10.pollingHelper.onEnable(polling3, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common10.pollingHelper.onDisable(polling3, { store, auth, propsValue });
  },
  async run(context) {
    return await import_pieces_common10.pollingHelper.poll(polling3, context);
  }
});

// ../ap/packages/pieces/community/smoove/src/lib/triggers/new-lead-submitted.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var polling4 = {
  strategy: import_pieces_common11.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    try {
      let {
        monitoringMode,
        landingPageId,
        includeCustomFields = !1,
        includeContactDetails = !0
      } = propsValue, allSubmissions = [];
      if (monitoringMode === "specific" && landingPageId)
        allSubmissions = await fetchSubmissionsForPage(
          auth,
          landingPageId,
          includeCustomFields,
          includeContactDetails,
          lastFetchEpochMS
        );
      else if (monitoringMode === "all")
        try {
          let landingPages = await makeRequest(auth.secret_text, import_pieces_common11.HttpMethod.GET, "/LandingPages"), pagePromises = (Array.isArray(landingPages) ? landingPages : []).slice(0, 10).map(async (page) => page.formId ? await fetchSubmissionsForPage(
            auth,
            page.formId.toString(),
            includeCustomFields,
            includeContactDetails,
            lastFetchEpochMS
          ) : []);
          allSubmissions = (await Promise.all(pagePromises)).flat();
        } catch (error) {
          return console.error("Error fetching all landing pages:", error), [];
        }
      return allSubmissions.filter(
        (submission, index, self) => index === self.findIndex((s) => s.id === submission.id)
      ).sort((a, b) => new Date(b.timeStamp).getTime() - new Date(a.timeStamp).getTime()).map((submission) => ({
        epochMilliSeconds: new Date(submission.timeStamp).getTime(),
        data: submission
      }));
    } catch (error) {
      return console.error("Error fetching lead submissions:", error), [];
    }
  }
};
async function fetchSubmissionsForPage(auth, pageId, includeCustomFields, includeContactDetails, lastFetchEpochMS) {
  try {
    let queryParams = [
      "fields=id,email,timestampSignup,firstName,lastName,cellPhone,externalId",
      "page=1",
      "itemsPerPage=100",
      `includeCustomFields=${includeCustomFields}`,
      "includeLinkedLists=false"
    ], endpoint = `/LandingPages/${pageId}/Recipients?${queryParams.join("&")}`, response = await makeRequest(auth.secret_text, import_pieces_common11.HttpMethod.GET, endpoint);
    return response ? (Array.isArray(response) ? response : [response]).filter((item) => item.timestampSignup ? new Date(item.timestampSignup).getTime() > (lastFetchEpochMS != null ? lastFetchEpochMS : 0) : !1).map((item) => __spreadValues({
      id: item.id || Date.now(),
      contactId: item.id,
      pageUrl: `https://lp.smoove.io/form/${pageId}`,
      userIP: item.ipSignup || "Unknown",
      timeStamp: item.timestampSignup,
      contact: {
        id: item.id,
        email: item.email,
        firstName: item.firstName,
        lastName: item.lastName,
        cellPhone: item.cellPhone,
        timestampSignup: item.timestampSignup,
        externalId: item.externalId
      },
      formId: parseInt(pageId),
      formType: "LandingPage"
    }, includeCustomFields && item.customFields ? { customFields: item.customFields } : {})) : [];
  } catch (error) {
    return console.error(`Error fetching submissions for page ${pageId}:`, error), [];
  }
}
var newLeadSubmitted = (0, import_pieces_framework10.createTrigger)({
  auth: smooveAuth,
  name: "newLeadSubmitted",
  displayName: "New Lead Submitted",
  description: "Fires when a lead submits via form, popup, or mobile campaign",
  aiMetadata: {
    description: "Fires when a new lead is submitted through a Smoove form or landing page, with submission and contact details; the Monitoring Mode prop selects between watching all forms or a single specified landing page."
  },
  props: {
    monitoringMode: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Monitoring Mode",
      description: "Choose whether to monitor all forms or a specific form",
      required: !0,
      defaultValue: "all",
      options: {
        options: [
          { label: "All Forms/Landing Pages", value: "all" },
          { label: "Specific Form/Landing Page", value: "specific" }
        ]
      }
    }),
    landingPageId: LandingPageIdDropdown,
    includeCustomFields: import_pieces_framework10.Property.Checkbox({
      displayName: "Include Custom Fields",
      description: "Include custom field data in the lead submission",
      required: !1,
      defaultValue: !1
    }),
    includeContactDetails: import_pieces_framework10.Property.Checkbox({
      displayName: "Include Full Contact Details",
      description: "Include complete contact information in the submission data",
      required: !1,
      defaultValue: !0
    })
  },
  sampleData: {
    id: 965381765,
    contactId: 845986993,
    pageUrl: "https://lp.smoove.io/form/581014",
    userIP: "180.151.116.12",
    timeStamp: "2025-01-22T18:41:48.747Z",
    formId: 581014,
    formType: "LandingPage",
    contact: {
      id: 845986993,
      email: "john.doe@example.com",
      firstName: "John",
      lastName: "Doe",
      cellPhone: "+1234567890",
      timestampSignup: "2025-01-22T18:41:48.747Z",
      externalId: "ext_12345"
    },
    customFields: {
      company: "Tech Corp",
      interests: ["marketing", "automation"],
      lead_score: 85
    }
  },
  type: import_pieces_framework10.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common11.pollingHelper.test(polling4, context);
  },
  async onEnable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common11.pollingHelper.onEnable(polling4, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common11.pollingHelper.onDisable(polling4, { store, auth, propsValue });
  },
  async run(context) {
    return await import_pieces_common11.pollingHelper.poll(polling4, context);
  }
});

// ../ap/packages/pieces/community/smoove/src/index.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), smoove = (0, import_pieces_framework11.createPiece)({
  displayName: "Smoove",
  auth: smooveAuth,
  description: "Smoove is a platform for creating and managing your email list and sending emails to your subscribers.",
  categories: [import_pieces_framework12.PieceCategory.MARKETING],
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/smoove.png",
  authors: ["Sanket6652", "onyedikachi-david"],
  actions: [addOrUpdateSubscriber, createAList, findSubscriber, unsubscribe],
  triggers: [newListCreated, newSubscriber, newFormCreated, newLeadSubmitted]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  smoove
});
