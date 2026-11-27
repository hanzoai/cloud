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

// ../ap/packages/pieces/community/freshdesk/src/index.ts
var index_exports = {};
__export(index_exports, {
  freshdesk: () => freshdesk,
  freshdeskAuth: () => freshdeskAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/freshdesk/src/lib/actions/get-contact-from-id.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var getContactFromID = (0, import_pieces_framework.createAction)({
  auth: freshdeskAuth,
  name: "get_contact_from_id",
  classification: "READ",
  displayName: "Get Contact from ID",
  description: "Get contacts details from Freshdesk using ID number.",
  audience: "both",
  aiMetadata: { description: "Fetch the full details of a single Freshdesk contact by its numeric contact ID. Use when you already have a specific contact ID and need its record; to look up contacts by email, phone, or other attributes use Get Freshdesk Contacts instead. Requires a valid contact ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    contactid: import_pieces_framework.Property.ShortText({
      displayName: "Contact ID number",
      description: "The ID number of the contact",
      required: !0
    })
  },
  async run(context) {
    let FDapiToken = context.auth.props.access_token, FDcontactID = context.propsValue.contactid, headers = {
      Authorization: FDapiToken,
      "Content-Type": "application/json"
    }, url = `${context.auth.props.base_url.replace(/\/$/, "")}/api/v2/contacts/${FDcontactID}`, httprequestdata = {
      method: import_pieces_common.HttpMethod.GET,
      url,
      headers
    }, response = await import_pieces_common.httpClient.sendRequest(httprequestdata);
    return response.status == 200 ? response.body : response;
  }
});

// ../ap/packages/pieces/community/freshdesk/src/lib/actions/get-ticket-status.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var import_pieces_framework3 = require("@activepieces/pieces-framework"), getTicketStatus = (0, import_pieces_framework2.createAction)({
  auth: freshdeskAuth,
  name: "get_ticket_status",
  classification: "READ",
  displayName: "Get Ticket Status",
  description: "Get Ticket status from Freshdesk. Returns ticket_status, assigned_status, assigned_id",
  audience: "both",
  aiMetadata: { description: "Look up a single Freshdesk ticket by its numeric ticket ID and report its workflow state, returning a human-readable status (Open, Pending, Resolved, or Closed) plus whether the ticket is assigned to an agent. Use when you have a specific ticket ID and need just its status/assignment rather than the full ticket record. Requires a valid ticket ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    ticketid: import_pieces_framework2.Property.ShortText({
      displayName: "Ticket ID",
      description: "The Ticket ID to return status",
      required: !0
    })
  },
  async run(context) {
    let FDapiToken = context.auth.props.access_token, FDticketID = context.propsValue.ticketid, headers = {
      Authorization: FDapiToken,
      "Content-Type": "application/json"
    }, url = `${context.auth.props.base_url.replace(/\/$/, "")}/api/v2/tickets/${FDticketID}`, httprequestdata = {
      method: import_pieces_common2.HttpMethod.GET,
      url,
      headers
    }, response = await import_pieces_common2.httpClient.sendRequest(httprequestdata);
    if (response.status == 200) {
      let status = response.body.status, responderid = response.body.responder_id, AssignedStatusFriendlyValue = "", TicketStatusFriendlyValue = "";
      switch ((0, import_pieces_framework3.isNil)(responderid) ? AssignedStatusFriendlyValue = "NOTASSIGNED" : AssignedStatusFriendlyValue = "ASSIGNED", status) {
        case 2: {
          TicketStatusFriendlyValue = "OPEN";
          break;
        }
        case 3: {
          TicketStatusFriendlyValue = "PENDING";
          break;
        }
        case 4: {
          TicketStatusFriendlyValue = "RESOLVED";
          break;
        }
        case 5: {
          TicketStatusFriendlyValue = "CLOSED";
          break;
        }
        default: {
          TicketStatusFriendlyValue = status;
          break;
        }
      }
      return [
        {
          ticket_status: TicketStatusFriendlyValue,
          assigned_status: AssignedStatusFriendlyValue,
          assigned_id: responderid
        }
      ];
    } else
      return response.status;
  }
});

// ../ap/packages/pieces/community/freshdesk/src/lib/actions/get-tickets.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getTickets = (0, import_pieces_framework4.createAction)({
  auth: freshdeskAuth,
  name: "get_tickets",
  classification: "SEARCH",
  displayName: "Get Tickets",
  description: "Get Ticket instances from Freshdesk.",
  audience: "both",
  aiMetadata: { description: "Retrieve the list of support tickets from a Freshdesk account (the default unfiltered tickets endpoint, returning recent tickets). Use to browse or enumerate tickets when you do not have a specific ticket ID or status filter; for status-filtered results use Get All Tickets By Status instead. Read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    let headers = {
      Authorization: context.auth.props.access_token,
      "Content-Type": "application/json"
    }, url = `${context.auth.props.base_url.replace(/\/$/, "")}/api/v2/tickets/`, httprequestdata = {
      method: import_pieces_common3.HttpMethod.GET,
      url,
      headers
    }, response = await import_pieces_common3.httpClient.sendRequest(httprequestdata);
    return response.status == 200 ? response.body : response;
  }
});

// ../ap/packages/pieces/community/freshdesk/src/lib/actions/get-contacts.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getContacts = (0, import_pieces_framework5.createAction)({
  auth: freshdeskAuth,
  name: "get_contacts",
  classification: "SEARCH",
  displayName: "Get Freshdesk Contacts",
  description: "Get contact details from Freshdesk for all (optional filtered) contacts.",
  audience: "both",
  aiMetadata: { description: "List Freshdesk contacts, optionally narrowing the results by an attribute filter (email, mobile, phone, company ID, or updated-since) and/or a state filter (blocked, deleted, unverified, verified); with no filters it returns all contacts. Use to search for or enumerate contacts when you do not already have a contact ID. Per-page count caps at 100 (set 0 for the default page). Read-only and idempotent.", idempotent: !0 },
  props: {
    filter_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Optional Filter",
      description: "Select one and provide the value.",
      defaultValue: "",
      required: !1,
      options: {
        options: [
          {
            label: "E-mail",
            value: "email"
          },
          {
            label: "Mobile",
            value: "mobile"
          },
          {
            label: "Phone",
            value: "phone"
          },
          {
            label: "Company ID",
            value: "company_id"
          },
          {
            label: "Updated Since",
            value: "updated_since"
          }
        ]
      }
    }),
    filter_value: import_pieces_framework5.Property.ShortText({
      displayName: "Filter value",
      description: "Provide value if previous option selected!",
      required: !1,
      defaultValue: ""
    }),
    filter_status: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Optional Filter Status",
      description: "Can filter by state: blocked, deleted, unverified or verified.",
      defaultValue: "",
      required: !1,
      options: {
        options: [
          {
            label: "Blocked",
            value: "blocked"
          },
          {
            label: "Deleted",
            value: "deleted"
          },
          {
            label: "Unverified",
            value: "unverified"
          },
          {
            label: "Verified",
            value: "verified"
          }
        ]
      }
    }),
    per_page: import_pieces_framework5.Property.Number({
      displayName: "Results to return",
      description: "Freshdesk calls this per_page - set to 0 for all, if specified maximum is 100",
      required: !0,
      defaultValue: 0
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let headers = {
      Authorization: context.auth.props.access_token,
      "Content-Type": "application/json"
    }, queryParams = new URLSearchParams();
    ((_a = context.propsValue.filter_type) == null ? void 0 : _a.valueOf) != null && ((_b = context.propsValue.filter_value) == null ? void 0 : _b.valueOf) != null && queryParams.append(
      (_c = context.propsValue.filter_type) == null ? void 0 : _c.toString(),
      context.propsValue.filter_value || ""
    ), ((_d = context.propsValue.filter_status) == null ? void 0 : _d.valueOf) != null && queryParams.append("state", context.propsValue.filter_status || ""), context.propsValue.per_page != 0 && queryParams.append(
      "per_page",
      context.propsValue.per_page.toString() || "100"
    );
    let url = `${context.auth.props.base_url.replace(/\/$/, "")}/api/v2/contacts/?${queryParams.toString()}`, httprequestdata = {
      method: import_pieces_common4.HttpMethod.GET,
      url,
      headers
    }, response = await import_pieces_common4.httpClient.sendRequest(httprequestdata);
    return response.status == 200 ? response.body : response;
  }
});

// ../ap/packages/pieces/community/freshdesk/src/lib/actions/get-all-tickets-by-status.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getAllTicketsByStatus = (0, import_pieces_framework6.createAction)({
  auth: freshdeskAuth,
  name: "get_all_tickets_by_status",
  classification: "SEARCH",
  displayName: "Get All Tickets By Status",
  description: "Get All Tickets by selected status from Freshdesk.",
  audience: "both",
  aiMetadata: { description: "Search Freshdesk tickets filtered to one or more selected workflow states (Open, Pending, Resolved, Closed), combining the chosen statuses with OR. Use when you want tickets matching specific statuses rather than the full unfiltered list from Get Tickets. At least one status must be selected. Read-only and idempotent.", idempotent: !0 },
  props: {
    status_filter: import_pieces_framework6.Property.StaticMultiSelectDropdown({
      displayName: "Choose Status(es)",
      description: "Select one or status values",
      required: !0,
      options: {
        options: [
          {
            label: "Open",
            value: "status:2"
          },
          {
            label: "Pending",
            value: "status:3"
          },
          {
            label: "Resolved",
            value: "status:4"
          },
          {
            label: "Closed",
            value: "status:5"
          }
        ]
      }
    })
  },
  async run(context) {
    let headers = {
      Authorization: context.auth.props.access_token,
      "Content-Type": "application/json"
    }, baseUrl = context.auth.props.base_url.replace(/\/$/, ""), queryParams = new URLSearchParams(), replacedString = '"' + context.propsValue.status_filter.map((str) => str.replace(/,/g, " OR ")).join(" OR ") + '"';
    queryParams.append("query", replacedString || "");
    let url = `${baseUrl}/api/v2/search/tickets/?${queryParams.toString()}`, httprequestdata = {
      method: import_pieces_common5.HttpMethod.GET,
      url,
      headers
    }, response = await import_pieces_common5.httpClient.sendRequest(httprequestdata);
    if (response.status == 200) {
      let ticketResults = response.body.results;
      ticketResults.sort((a, b) => a.requester_id - b.requester_id);
      let tickets = [];
      return ticketResults.forEach((ticketResult) => {
        let statusString;
        switch (ticketResult.status) {
          case 2:
            statusString = "Open";
            break;
          case 3:
            statusString = "Pending";
            break;
          case 4:
            statusString = "Resolved";
            break;
          case 5:
            statusString = "Closed";
            break;
          default:
            statusString = "Unknown";
        }
        tickets.push({
          id: ticketResult.id,
          requester_id: ticketResult.requester_id,
          responder_id: ticketResult.responder_id,
          company_id: ticketResult.company_id,
          status: statusString,
          subject: ticketResult.subject,
          created_at: ticketResult.created_at,
          updated_at: ticketResult.updated_at,
          description_text: ticketResult.description_text,
          description: ticketResult.description
          // Add more properties if necessary
        });
      }), tickets;
    } else
      return response.status;
  }
});

// ../ap/packages/pieces/community/freshdesk/src/index.ts
var freshdeskAuth = import_pieces_framework7.PieceAuth.CustomAuth({
  props: {
    base_url: import_pieces_framework7.Property.ShortText({
      displayName: "Base URL",
      description: "Enter the base URL",
      required: !0
    }),
    access_token: import_pieces_framework7.Property.ShortText({
      displayName: "API Token",
      description: "Enter the API token",
      required: !0
    })
  },
  description: "Get the API token by visiting your profile settings and clicking View API key",
  required: !0
}), freshdesk = (0, import_pieces_framework7.createPiece)({
  displayName: "Freshdesk",
  description: "Customer support software",
  logoUrl: "https://cdn.activepieces.com/pieces/freshdesk.png",
  categories: [import_pieces_framework8.PieceCategory.CUSTOMER_SUPPORT],
  authors: ["buttonsbond", "kishanprmr", "MoShizzle", "AbdulTheActivePiecer", "abuaboud"],
  auth: freshdeskAuth,
  actions: [
    getTickets,
    getContactFromID,
    getTicketStatus,
    getContacts,
    getAllTicketsByStatus,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: (auth) => {
        var _a;
        return (_a = auth == null ? void 0 : auth.props.base_url) != null ? _a : "";
      },
      auth: freshdeskAuth,
      authMapping: async (auth) => ({
        Authorization: auth.props.access_token
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  freshdesk,
  freshdeskAuth
});
