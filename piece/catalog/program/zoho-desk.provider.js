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

// ../ap/packages/pieces/community/zoho-desk/src/index.ts
var index_exports = {};
__export(index_exports, {
  piecesZohoDesk: () => piecesZohoDesk
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/zoho-desk/src/lib/actions/list-tickets.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/zoho-desk/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), zohoDeskAuth = import_pieces_framework.PieceAuth.OAuth2({
  props: {
    location: import_pieces_framework.Property.StaticDropdown({
      displayName: "Location",
      description: "The location of your Zoho Desk account.",
      required: !0,
      options: {
        options: [
          {
            label: "zoho.eu (Europe)",
            value: "zoho.eu"
          },
          {
            label: "zoho.com (United States)",
            value: "zoho.com"
          },
          {
            label: "zoho.com.au (Australia)",
            value: "zoho.com.au"
          },
          {
            label: "zoho.jp (Japan)",
            value: "zoho.jp"
          },
          {
            label: "zoho.in (India)",
            value: "zoho.in"
          },
          {
            label: "zohocloud.ca (Canada)",
            value: "zohocloud.ca"
          }
        ]
      }
    })
  },
  description: "Authentication for Zoho Desk",
  scope: [
    "Desk.tickets.ALL",
    "Desk.tasks.ALL",
    "Desk.settings.ALL",
    "Desk.events.ALL",
    "Desk.contacts.READ",
    "Desk.contacts.WRITE",
    "Desk.contacts.UPDATE",
    "Desk.contacts.CREATE",
    "Desk.basic.READ",
    "Desk.basic.CREATE",
    "Desk.search.READ",
    "Desk.articles.READ",
    "Desk.articles.CREATE",
    "Desk.articles.UPDATE",
    "Desk.articles.DELETE"
  ],
  authUrl: "https://accounts.{location}/oauth/v2/auth",
  tokenUrl: "https://accounts.{location}/oauth/v2/token",
  required: !0
});

// ../ap/packages/pieces/community/zoho-desk/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function zohoDeskApiCall({
  auth,
  orgId,
  method,
  resourceUri,
  query,
  body
}) {
  var _a, _b;
  let baseUrl = `https://desk.${(_b = (_a = auth.props) == null ? void 0 : _a.location) != null ? _b : "zoho.com"}/api/v1`, qs = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (qs[key] = String(value));
  let headers = {
    Authorization: `Zoho-oauthtoken ${auth.access_token}`
  };
  orgId && (headers.orgId = orgId);
  let request = {
    method,
    url: baseUrl + resourceUri,
    headers,
    queryParams: qs,
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}

// ../ap/packages/pieces/community/zoho-desk/src/lib/common/props.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var organizationId = (params) => import_pieces_framework2.Property.Dropdown({
  auth: zohoDeskAuth,
  displayName: params.displayName,
  description: params.description,
  refreshers: [],
  required: params.required,
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await zohoDeskApiCall({
      auth,
      method: import_pieces_common2.HttpMethod.GET,
      resourceUri: "/organizations"
    })).data.map((org) => ({
      label: org.companyName,
      value: org.id
    }))
  } : {
    placeholder: "Please connect your account first.",
    options: [],
    disabled: !0
  }
}), departmentId = (params) => import_pieces_framework2.Property.Dropdown({
  auth: zohoDeskAuth,
  displayName: params.displayName,
  description: params.description,
  refreshers: ["orgId"],
  required: params.required,
  options: async ({ auth, orgId }) => auth ? orgId ? {
    disabled: !1,
    options: (await zohoDeskApiCall({
      auth,
      method: import_pieces_common2.HttpMethod.GET,
      resourceUri: "/departments",
      orgId
    })).data.map((department) => ({
      label: department.name,
      value: department.id
    }))
  } : {
    placeholder: "Please select organization first.",
    options: [],
    disabled: !0
  } : {
    placeholder: "Please connect your account first.",
    options: [],
    disabled: !0
  }
});

// ../ap/packages/pieces/community/zoho-desk/src/lib/actions/list-tickets.ts
var listTicketsAction = (0, import_pieces_framework3.createAction)({
  auth: zohoDeskAuth,
  name: "list_tickets",
  description: "List tickets",
  audience: "both",
  aiMetadata: { description: "Retrieves tickets from a Zoho Desk organization, optionally expanding related data (contacts, products, departments, team, isRead, assignee) via the include option. Use to browse or page through existing tickets when you do not have a specific ticket ID; requires the organization ID. Read-only and idempotent.", idempotent: !0 },
  displayName: "List tickets.",
  props: {
    orgId: organizationId({ displayName: "Organization", required: !0 }),
    include: import_pieces_framework3.Property.StaticMultiSelectDropdown({
      displayName: "include",
      required: !1,
      description: "Additional information related to the tickets.",
      options: {
        disabled: !1,
        options: [
          { label: "contacts", value: "contacts" },
          { label: "products", value: "products" },
          { label: "departments", value: "departments" },
          { label: "team", value: "team" },
          { label: "isRead", value: "isRead" },
          { label: "assignee", value: "assignee" }
        ]
      }
    })
  },
  async run({ propsValue, auth }) {
    let queryParams = {};
    return propsValue.include && (queryParams.include = propsValue.include.join(",")), await zohoDeskApiCall({
      auth,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: "/tickets",
      orgId: propsValue.orgId,
      query: queryParams
    });
  }
});

// ../ap/packages/pieces/community/zoho-desk/src/lib/actions/create-ticket.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createTicketAction = (0, import_pieces_framework4.createAction)({
  auth: zohoDeskAuth,
  name: "create_ticket",
  displayName: "Create Ticket",
  description: "Creates a new ticket.",
  audience: "both",
  aiMetadata: { description: "Creates a new support ticket in a Zoho Desk organization. Use when an agent needs to log a new issue or request; requires the organization ID, a department ID, a subject, and a description, with optional contact, status, priority, channel, assignee, and custom fields. Not idempotent \u2014 each call creates a separate ticket.", idempotent: !1 },
  props: {
    orgId: organizationId({ displayName: "Organization", required: !0 }),
    departmentId: departmentId({ displayName: "Department", required: !0 }),
    contactId: import_pieces_framework4.Property.ShortText({
      displayName: "Contact ID",
      required: !1,
      description: "ID of the contact raising the ticket"
    }),
    subject: import_pieces_framework4.Property.ShortText({
      displayName: "Subject",
      required: !0,
      description: "Subject of the ticket"
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      required: !0,
      description: "Description of the issue in the ticket"
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      required: !1,
      description: "Email address of the contact raising the ticket"
    }),
    phone: import_pieces_framework4.Property.ShortText({
      displayName: "Phone",
      required: !1,
      description: "Phone number of the contact raising the ticket"
    }),
    status: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Status",
      required: !1,
      description: "Status of the ticket",
      options: {
        options: [
          { label: "Open", value: "Open" },
          { label: "On Hold", value: "On Hold" },
          { label: "Escalated", value: "Escalated" },
          { label: "Closed", value: "Closed" }
        ]
      },
      defaultValue: "Open"
    }),
    priority: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Priority",
      required: !1,
      description: "Priority of the ticket",
      options: {
        options: [
          { label: "Low", value: "Low" },
          { label: "Medium", value: "Medium" },
          { label: "High", value: "High" },
          { label: "Urgent", value: "Urgent" }
        ]
      },
      defaultValue: "Medium"
    }),
    category: import_pieces_framework4.Property.ShortText({
      displayName: "Category",
      required: !1,
      description: "Category of the ticket"
    }),
    subCategory: import_pieces_framework4.Property.ShortText({
      displayName: "Sub Category",
      required: !1,
      description: "Sub-category of the ticket"
    }),
    dueDate: import_pieces_framework4.Property.DateTime({
      displayName: "Due Date",
      required: !1,
      description: "Due date for the ticket (ISO format)"
    }),
    channel: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Channel",
      required: !1,
      description: "Channel through which the ticket is created",
      options: {
        options: [
          { label: "Email", value: "Email" },
          { label: "Phone", value: "Phone" },
          { label: "Chat", value: "Chat" },
          { label: "Web", value: "Web" },
          { label: "Social", value: "Social" }
        ]
      },
      defaultValue: "Web"
    }),
    assigneeId: import_pieces_framework4.Property.ShortText({
      displayName: "Assignee ID",
      required: !1,
      description: "ID of the agent to whom the ticket is assigned"
    }),
    productId: import_pieces_framework4.Property.ShortText({
      displayName: "Product ID",
      required: !1,
      description: "ID of the product to which the ticket belongs"
    }),
    classification: import_pieces_framework4.Property.ShortText({
      displayName: "Classification",
      required: !1,
      description: "Classification of the ticket"
    }),
    language: import_pieces_framework4.Property.ShortText({
      displayName: "Language",
      required: !1,
      description: "Language of the ticket",
      defaultValue: "English"
    }),
    entitySkills: import_pieces_framework4.Property.Array({
      displayName: "Entity Skills",
      required: !1,
      description: "Array of skill IDs associated with the ticket"
    }),
    customFields: import_pieces_framework4.Property.Object({
      displayName: "Custom Fields",
      required: !1,
      description: "Custom fields in the ticket"
    })
  },
  async run({ propsValue, auth }) {
    let {
      orgId,
      departmentId: departmentId2,
      contactId,
      subject,
      description,
      email,
      phone,
      status,
      priority,
      category,
      subCategory,
      dueDate,
      channel,
      assigneeId,
      productId,
      classification,
      language,
      entitySkills,
      customFields
    } = propsValue, requestBody = {
      departmentId: departmentId2,
      subject,
      description
    };
    return contactId && (requestBody.contactId = contactId), email && (requestBody.email = email), phone && (requestBody.phone = phone), status && (requestBody.status = status), priority && (requestBody.priority = priority), category && (requestBody.category = category), subCategory && (requestBody.subCategory = subCategory), dueDate && (requestBody.dueDate = dueDate), channel && (requestBody.channel = channel), assigneeId && (requestBody.assigneeId = assigneeId), productId && (requestBody.productId = productId), classification && (requestBody.classification = classification), language && (requestBody.language = language), entitySkills && entitySkills.length > 0 && (requestBody.entitySkills = entitySkills), customFields && (requestBody.cf = customFields), await zohoDeskApiCall({
      auth,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: "/tickets",
      orgId,
      body: requestBody
    });
  }
});

// ../ap/packages/pieces/community/zoho-desk/src/lib/actions/find-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), findContactAction = (0, import_pieces_framework5.createAction)({
  auth: zohoDeskAuth,
  name: "find-contact",
  displayName: "Find Contact",
  description: "Finds an existing contact by email.",
  audience: "both",
  aiMetadata: { description: "Searches a Zoho Desk organization for contacts matching an exact email address, returning whether any were found along with the matches. Use to resolve a contact (e.g. before creating or routing a ticket) when you have the email rather than an ID; requires the organization ID and email. Read-only and idempotent.", idempotent: !0 },
  props: {
    orgId: organizationId({ displayName: "Organization", required: !0 }),
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      required: !0
    })
  },
  async run(context) {
    let response = await zohoDeskApiCall({
      auth: context.auth,
      method: import_pieces_common5.HttpMethod.GET,
      resourceUri: "/contacts/search",
      orgId: context.propsValue.orgId,
      query: {
        email: context.propsValue.email
      }
    });
    if ((0, import_pieces_framework6.isNil)(response) || response === "")
      return {
        found: !1,
        result: []
      };
    let contacts = response;
    return {
      found: contacts.data.length > 0,
      result: contacts.data
    };
  }
});

// ../ap/packages/pieces/community/zoho-desk/src/index.ts
var piecesZohoDesk = (0, import_pieces_framework7.createPiece)({
  displayName: "Zoho Desk",
  description: "Helpdesk management software",
  auth: zohoDeskAuth,
  categories: [import_pieces_framework8.PieceCategory.CUSTOMER_SUPPORT],
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/zoho-desk.png",
  authors: ["volubile", "kishanprmr"],
  actions: [
    listTicketsAction,
    createTicketAction,
    findContactAction,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: (auth) => {
        var _a, _b;
        return `https://desk.${(_b = (_a = auth.props) == null ? void 0 : _a.location) != null ? _b : "zoho.com"}/api/v1`;
      },
      auth: zohoDeskAuth,
      authMapping: async (auth) => ({
        Authorization: `Zoho-oauthtoken ${auth.access_token}`
      }),
      extraProps: {
        orgId: organizationId({
          displayName: "Organization ID",
          description: "Select organization ID to include in auth headers.",
          required: !1
        })
      }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  piecesZohoDesk
});
